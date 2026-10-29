package runner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

type candleBundle struct{ One, Five map[string]controller.Candles }
type feedEvent struct {
	Kind          string
	At            time.Time
	Board         controller.Board
	View          controller.OrdersView
	Registrations controller.Registrations
	Candles       candleBundle
	Err           error
}

// duration は設定秒数をtime.Durationに変換します。
//
// 引数: seconds(float64)。返り値: time.Duration。
func duration(seconds float64) time.Duration { return time.Duration(seconds * float64(time.Second)) }

// sendEvent は中断可能なキューへ受信順を保って投入します。
//
// 引数: ctx(context.Context)、out(chan feedEvent)、event(feedEvent)。
// 返り値: boolは投入できた場合trueです。価格を黙って捨てません。
func sendEvent(ctx context.Context, out chan<- feedEvent, event feedEvent) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// pause はネットワーク再接続を中断可能に待ちます。
//
// 引数: ctx(context.Context)、wait(time.Duration)。
// 返り値: boolは待機完了です。
func pause(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// fetchCandles は両市場・両足の世代が一致する一組を取得します。
//
// 引数: ctx(context.Context)、client(*controller.Client)。
// 返り値: candleBundleとerror。異なる立会・世代を混合しません。
func fetchCandles(ctx context.Context, client *controller.Client) (candleBundle, error) {
	bundle := candleBundle{One: map[string]controller.Candles{}, Five: map[string]controller.Candles{}}
	type answer struct {
		code, interval string
		data           controller.Candles
		err            error
	}
	replies := make(chan answer, 4)
	for _, code := range []string{"TOPIX", "NK225"} {
		for _, interval := range []string{"1min", "5min"} {
			go func(code, interval string) {
				data, err := client.Candles(ctx, code, interval, "", time.Time{}, time.Time{})
				replies <- answer{code, interval, data, err}
			}(code, interval)
		}
	}
	var firstErr error
	for i := 0; i < 4; i++ {
		item := <-replies
		if item.err != nil {
			if firstErr == nil {
				firstErr = item.err
			}
			continue
		}
		if item.interval == "1min" {
			bundle.One[item.code] = item.data
		} else {
			bundle.Five[item.code] = item.data
		}
	}
	if firstErr != nil {
		return bundle, firstErr
	}
	base := bundle.One["TOPIX"].Snapshot
	if base.SessionID == "" || base.Generation == "" || base.Session.StartsAt.IsZero() {
		return bundle, fmt.Errorf("足履歴の立会・世代がありません")
	}
	for _, items := range []map[string]controller.Candles{bundle.One, bundle.Five} {
		for code, data := range items {
			if data.Snapshot.SessionID != base.SessionID || data.Snapshot.Generation != base.Generation || data.Snapshot.Code != code ||
				!data.Snapshot.Session.StartsAt.Equal(base.Session.StartsAt) || !data.Snapshot.Session.EndsAt.Equal(base.Session.EndsAt) {
				return bundle, fmt.Errorf("二市場の足履歴の世代・立会が一致しません")
			}
		}
	}
	return bundle, nil
}

// startFeed はPUSHと口座・確定足の取得を独立に継続します。
//
// 引数: parent(context.Context)、client(*controller.Client)、cfg(config.Config)。
// 返り値: <-chan feedEvent、停止関数。PUSH断は再接続し、古い値を新価格に置換しません。
func startFeed(parent context.Context, client *controller.Client, cfg config.Config) (<-chan feedEvent, func()) {
	ctx, cancel := context.WithCancel(parent)
	out := make(chan feedEvent, 32768)
	var workers sync.WaitGroup
	var symbols sync.Map
	workers.Add(3)
	go func() {
		defer workers.Done()
		for {
			reg, err := client.Registrations(ctx)
			if err == nil {
				for _, future := range reg.Futures {
					wanted := future.FutureCode == "TOPIX" || future.FutureCode == "NK225"
					for _, market := range cfg.Markets {
						if market.Enabled && market.Product == future.FutureCode {
							wanted = true
						}
					}
					if wanted {
						symbols.Store(future.Symbol, true)
					}
				}
				if !sendEvent(ctx, out, feedEvent{Kind: "登録", At: time.Now(), Registrations: reg}) {
					return
				}
			} else {
				if !sendEvent(ctx, out, feedEvent{Kind: "通信", At: time.Now(), Err: err}) {
					return
				}
			}
			view, err := client.Orders(ctx, "all")
			if err == nil {
				if !sendEvent(ctx, out, feedEvent{Kind: "口座", At: time.Now(), View: view}) {
					return
				}
			} else {
				if !sendEvent(ctx, out, feedEvent{Kind: "通信", At: time.Now(), Err: err}) {
					return
				}
			}
			if !pause(ctx, duration(cfg.Controller.AccountPollSeconds)) {
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		for {
			data, err := fetchCandles(ctx, client)
			kind := "足"
			if err != nil {
				kind = "通信"
			}
			if !sendEvent(ctx, out, feedEvent{Kind: kind, At: time.Now(), Candles: data, Err: err}) {
				return
			}
			if !pause(ctx, duration(cfg.Controller.CandlePollSeconds)) {
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		for {
			err := client.Stream(ctx, func(board controller.Board) error {
				if _, ok := symbols.Load(board.Symbol); !ok {
					return nil
				}
				if !sendEvent(ctx, out, feedEvent{Kind: "板", At: time.Now(), Board: board}) {
					return ctx.Err()
				}
				return nil
			})
			if !sendEvent(ctx, out, feedEvent{Kind: "PUSH中断", At: time.Now(), Err: err}) {
				return
			}
			if !pause(ctx, duration(cfg.Controller.ReconnectSeconds)) {
				return
			}
		}
	}()
	go func() { workers.Wait(); close(out) }()
	return out, cancel
}

// loadBooks は取得開始時までの当場原板を完読します。
//
// 引数: ctx(context.Context)、client(*controller.Client)、cutoff(time.Time)は受信範囲の上限。
// 返り値: 市場別BookHistoryとerror。二市場の世代不一致は再取得対象です。
func loadBooks(ctx context.Context, client *controller.Client, cutoff time.Time) (map[string]controller.BookHistory, error) {
	type answer struct {
		code string
		data controller.BookHistory
		err  error
	}
	replies := make(chan answer, 2)
	for _, code := range []string{"TOPIX", "NK225"} {
		go func(code string) {
			data, err := client.Book(ctx, code, time.Time{}, cutoff)
			replies <- answer{code, data, err}
		}(code)
	}
	data := map[string]controller.BookHistory{}
	var firstErr error
	for i := 0; i < 2; i++ {
		r := <-replies
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
		} else {
			data[r.code] = r.data
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	a, b := data["TOPIX"].Snapshot, data["NK225"].Snapshot
	if a.SessionID != b.SessionID || a.Generation != b.Generation {
		return nil, fmt.Errorf("二市場の生板履歴の世代が異なります")
	}
	return data, nil
}
