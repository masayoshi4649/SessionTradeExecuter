package runner

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// TestPreopenEmptyWarmupContinuesIntoDay は寄前に採用原板が0行でも当場の受信で準備が進むことを確認します。
//
// 引数: t(*testing.T) は試験です。
// 返り値: なし。人工的な二市場同時上昇を使い、実データの収益性は評価しません。
func TestPreopenEmptyWarmupContinuesIntoDay(t *testing.T) {
	cfg, bundle, histories, start, _ := bootstrapFixture(t)
	delete(cfg.Algorithm, "features.atr_min_bars")
	cutoff := start.Add(-75 * time.Second)
	for _, reference := range []string{"TOPIX", "NK225"} {
		one := bundle.One[reference]
		one.Data = nil
		one.Snapshot.AsOf = cutoff
		bundle.One[reference] = one
		bundle.Five[reference] = one
		previous := bootstrapBoard(t, start.Add(-3*time.Hour).Format(time.RFC3339Nano), 100)
		histories[reference] = controller.BookHistory{Snapshot: one.Snapshot, Data: []controller.Board{previous}}
	}
	store, _, _ := bootstrapStore(t)
	prepared, err := prepareHistory(context.Background(), cfg, bundle, histories, store, cutoff, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Rows) != 0 {
		t.Fatal("寄前の前場価格を当場暖機に使いました")
	}
	portfolios := map[string]*strategy.Portfolio{}
	for _, reference := range []string{"TOPIX", "NK225"} {
		p, err := strategy.New(strategy.Config{Reference: reference, Product: reference, CoreQuantity: 5,
			ExtraMaxQuantity: 5, Cap: 10, LargeTick: cfg.Products[reference].Tick,
			PointValue: cfg.Products[reference].PointValue, Parameters: strategy.DefaultParameters(reference, "Day")},
			strategy.Session{ID: prepared.Plan.ID, Kind: "Day", Start: seconds(start), ForceClose: seconds(prepared.Plan.ForceClose),
				End: seconds(prepared.Plan.End), ExtraAt: seconds(prepared.Plan.TopixExtra)})
		if err != nil {
			t.Fatal(err)
		}
		portfolios[reference] = p
	}
	// ----------------------------------------
	// 確定足を時間経過ごとに追加し、未来足を事前投入せずに同じ上昇を二市場へ供給します。
	firstEntry := map[string]float64{}
	for second := 0; second <= 75*60; second++ {
		for index, reference := range []string{"TOPIX", "NK225"} {
			base, slope, spread := 4000.0, .02, .5
			if reference == "NK225" {
				base, slope, spread = 65000, .4, 10
			}
			at := seconds(start) + float64(second) + float64(index+1)*.1
			if second > 0 {
				for _, interval := range []int{60, 300} {
					if second%interval != 0 {
						continue
					}
					open := base + slope*float64(second-interval)
					close := base + slope*float64(second-1)
					row := strategy.Candle{Time: seconds(start) + float64(second-interval), Open: open,
						High: close + spread/2, Low: open - spread/2, Close: close, Closed: true}
					if _, err = prepared.Features.AddCandles(reference, interval, []strategy.Candle{row}, at); err != nil {
						t.Fatal(err)
					}
				}
			}
			price := base + slope*float64(second)
			observation, err := prepared.Features.OnBoard(strategy.Board{Reference: reference, Time: at, Received: at,
				Last: price, Bid: price - spread/2, Ask: price + spread/2, BidQuantity: 100, AskQuantity: 100})
			if err != nil {
				t.Fatal(err)
			}
			if second == 16*60 && observation.ATR <= 0 {
				t.Fatal("寄前0行から確定5分足3本によるATRの準備が進みません", reference)
			}
			if second < 64*60 && observation.GateDirection != 0 {
				t.Fatal("EMA60本と傾き3本の準備前に方向が成立しました", reference, second)
			}
			signals, err := portfolios[reference].Observe(observation, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, signal := range signals {
				if signal.Action == "new" && firstEntry[reference] == 0 {
					firstEntry[reference] = signal.Time
				}
			}
		}
	}
	for _, reference := range []string{"TOPIX", "NK225"} {
		at := firstEntry[reference]
		if at == 0 || math.IsNaN(at) {
			t.Fatal("寄前0行から当場受信後に新規判断まで進みません", reference)
		}
		if at < seconds(start.Add(65*time.Minute)) {
			t.Fatal("相手市場の追加公開待機が完了する前に新規判断しました", reference)
		}
		t.Logf("%s 初回新規判断=%s", reference, timeFrom(at).In(start.Location()).Format(time.RFC3339Nano))
	}
}
