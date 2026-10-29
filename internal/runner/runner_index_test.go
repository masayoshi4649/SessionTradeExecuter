package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/e01"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// indexDayApp は途中起動と復元だけを検査する日中の人工運転器を作ります。
//
// 引数: t *testing.T は試験です。
// 返り値: *app、*marketRuntime、time.Time は運転器・担当・現在時計です。
func indexDayApp(t *testing.T) (*app, *marketRuntime, time.Time) {
	t.Helper()
	a, m, _, now := integrationApp(t, "TOPIX", true)
	a.plan.ID = now.Format("2006-01-02") + "_Day"
	a.plan.Kind = "Day"
	a.plan.TopixExtra = now.Add(time.Hour)
	a.plan.NKExtra = now.Add(time.Hour)
	m.Saved.Plan = a.plan
	params := strategy.DefaultParameters(m.Reference, "Day")
	cfg := m.Portfolio.Config
	cfg.Parameters = params
	session := strategy.Session{ID: a.plan.ID, Kind: "Day", Start: seconds(a.plan.Start),
		ForceClose: seconds(a.plan.ForceClose), End: seconds(a.plan.End), ExtraAt: seconds(a.plan.TopixExtra)}
	var err error
	m.Portfolio, err = strategy.New(cfg, session)
	if err != nil {
		t.Fatal(err)
	}
	a.features, err = strategy.NewFeatures(session, map[string]float64{"TOPIX": .5, "NK225": 10}, params.Features)
	if err != nil {
		t.Fatal(err)
	}
	a.symbols = map[string]string{"large-TOPIX": "TOPIX", "large-NK225": "NK225"}
	a.registrations = map[string]controller.Future{m.Saved.Contract.FutureCode: m.Saved.Contract}
	a.lastHandled = map[string]time.Time{}
	snapshot := controller.Snapshot{SessionID: a.plan.ID, Generation: a.generation,
		Code: "TOPIX", Session: controller.Session{StartsAt: a.plan.Start, EndsAt: a.plan.End}}
	a.bundle.One["TOPIX"] = controller.Candles{Snapshot: snapshot}
	return a, m, now
}

// indexObservation は注文を出さない既知価格と任意の特徴連番を作ります。
//
// 引数: index int64、at float64 は連番と時計です。
// 返り値: strategy.Observation は人工観測です。
func indexObservation(index int64, at float64) strategy.Observation {
	return strategy.Observation{Reference: "TOPIX", Index: index, Time: at, Received: at,
		Last: 100, Bid: 99.5, Ask: 100.5, QuoteOK: true,
		E01: e01.Observation{InputIndex: index, AdverseSource: -1}}
}

// indexFeed は同価格時計でも原文を区別できる板受信を作ります。
//
// 引数: t *testing.T、at time.Time、price any は試験・受信時計・JSON価格です。
// 返り値: feedEvent は原文を保持する受信です。
func indexFeed(t *testing.T, at time.Time, price any) feedEvent {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"Symbol": "large-TOPIX", "CurrentPriceTime": at.Format(time.RFC3339Nano),
		"CurrentPrice": price, "Buy1": map[string]any{"Price": 99.5, "Qty": 5},
		"Sell1": map[string]any{"Price": 100.5, "Qty": 5}})
	if err != nil {
		t.Fatal(err)
	}
	var board controller.Board
	if err = json.Unmarshal(raw, &board); err != nil {
		t.Fatal(err)
	}
	return feedEvent{Kind: "板", At: at, Board: board}
}

// TestRunnerDayWarmupKeepsFeatureIndex は暖機後の90000番を0へ潰さず紙影へ接続します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestRunnerDayWarmupKeepsFeatureIndex(t *testing.T) {
	a, m, now := indexDayApp(t)
	a.features.Counts["TOPIX"] = 90000
	market := a.features.E01.Markets["TOPIX"]
	market.InputIndex = 89999
	market.Adverse, market.AdverseSource = -1, 89999
	at := seconds(now)
	market.AdverseAt = &at
	if err := a.onBoard(context.Background(), indexFeed(t, now, 100.)); err != nil {
		t.Fatal("途中初起動で過去根拠が未来連番になりました", err)
	}
	if m.Portfolio.LastIndex != 90000 || m.Portfolio.Paper.Row != 0 ||
		m.Portfolio.PaperRows[90000] != 0 || m.Saved.LastRawHash == "" {
		t.Fatal("特徴連番と紙影の接続が不正です", m.Portfolio.LastIndex, m.Portfolio.Paper.Row)
	}
	if _, exists := m.Portfolio.PaperRows[89999]; exists {
		t.Fatal("暖機だけの行を処理済みの紙影として参照しました")
	}
	next := indexFeed(t, now.Add(time.Millisecond), 101.)
	market.AdverseSource = 90000
	if err := a.onBoard(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if m.Portfolio.LastIndex != 90001 || m.Portfolio.PaperRows[90001] != 1 {
		t.Fatal("初回の後で連番が飛びました")
	}
	if len(m.Manager.Records()) != 0 || len(m.Saved.Queue) != 0 {
		t.Fatal("暖機だけで仮想約定又は過去発注を作りました")
	}
}

// TestRunnerShiftCoordinatesAndUnknownSources は差分接続と欠番・未来根拠の不採用を確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestRunnerShiftCoordinatesAndUnknownSources(t *testing.T) {
	a, m, now := indexDayApp(t)
	_ = a
	first := indexObservation(700, seconds(now))
	if err := m.Portfolio.Advance(first); err != nil {
		t.Fatal(err)
	}
	input := indexObservation(2, seconds(now)+1)
	input.E01.Adverse, input.E01.AdverseSource = -1, 1
	shifted := shiftObservation(input, 699)
	if shifted.Index != 701 || shifted.E01.InputIndex != 701 || shifted.E01.AdverseSource != 700 ||
		shifted.Time != input.Time || shifted.Received != input.Received {
		t.Fatal("連番だけを同じ座標へ移していません", shifted)
	}
	if err := m.Portfolio.Advance(shifted); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		index, source int64
		at            float64
	}{{707, 706, seconds(now) + 2}, {710, 999999, seconds(now) + 3}, {711, -20, seconds(now) + 4}} {
		o := indexObservation(tc.index, tc.at)
		o.E01.Adverse, o.E01.AdverseSource = -1, tc.source
		if err := m.Portfolio.Advance(o); err != nil {
			t.Fatal("未処理の根拠を紙影の未来参照として使いました", tc, err)
		}
	}
	if m.Portfolio.Paper.Row != 4 || m.Portfolio.PaperRows[711] != 4 {
		t.Fatal("原行欠番で紙影の逐次連番が崩れました")
	}
	missing := shiftObservation(indexObservation(30, seconds(now)+5), -20)
	if missing.E01.AdverseSource != -1 {
		t.Fatal("欠測の -1 に差分を加算しました")
	}
}

// TestRunnerRestoreAnchorSameClock は同時計の原文を特定し、後続同時計も管理へ進めます。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestRunnerRestoreAnchorSameClock(t *testing.T) {
	a, m, now := indexDayApp(t)
	at := seconds(now)
	if err := m.Portfolio.Advance(indexObservation(700, at)); err != nil {
		t.Fatal(err)
	}
	m.Saved.LastRawHash = rawHash(json.RawMessage(`{"位置":"照合対象"}`))
	hashes := []string{rawHash(json.RawMessage(`{"位置":"前"}`)), m.Saved.LastRawHash,
		rawHash(json.RawMessage(`{"位置":"同時計の後"}`)), rawHash(json.RawMessage(`{"位置":"次時計"}`))}
	rows := []strategy.Observation{indexObservation(0, at-1), indexObservation(1, at),
		indexObservation(2, at), indexObservation(3, at+1)}
	p := &prepared{Features: a.features, Plan: a.plan, Generation: "再取得世代", Cutoff: now.Add(2 * time.Second),
		Rows: rows, Last: map[string]strategy.Observation{"TOPIX": rows[3]},
		SourceHashes: map[string][]string{"TOPIX": hashes}}
	anchor, exact := restoreAnchor(m, p)
	if anchor != 1 || !exact {
		t.Fatal("同時計の最後へ飛ばして原文を無視しました", anchor, exact)
	}
	if err := a.activate(p); err != nil {
		t.Fatal(err)
	}
	if m.Saved.IndexOffset != 699 || m.Portfolio.LastIndex != 702 || m.Portfolio.Paper.Row != 2 ||
		m.Portfolio.PaperRows[701] != 1 || m.Saved.LastRawHash != hashes[3] {
		t.Fatal("同時計の後続を管理へ取り込めていません", m.Saved.IndexOffset, m.Portfolio.LastIndex, m.Portfolio.Paper.Row)
	}
	if len(m.Manager.Records()) != 0 || len(m.Saved.Queue) != 0 {
		t.Fatal("復元中に過去注文を生成しました")
	}
}

// TestRunnerPartialHistoryAnchor は履歴の開始位置が変わっても時計を逆行させません。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestRunnerPartialHistoryAnchor(t *testing.T) {
	for _, mode := range []string{"過去不足", "既処理時計あり", "負の差分", "空履歴"} {
		t.Run(mode, func(t *testing.T) {
			a, m, now := indexDayApp(t)
			at := seconds(now)
			lastIndex := int64(700)
			if mode == "負の差分" {
				lastIndex = 1
			}
			if err := m.Portfolio.Advance(indexObservation(lastIndex, at)); err != nil {
				t.Fatal(err)
			}
			m.Saved.LastRawHash = "履歴にはない指紋"
			rows := []strategy.Observation{indexObservation(0, at+1), indexObservation(1, at+2)}
			expectedAnchor, count := int64(-1), int64(2)
			switch mode {
			case "既処理時計あり":
				rows = []strategy.Observation{indexObservation(0, at-1), indexObservation(1, at),
					indexObservation(2, at), indexObservation(3, at+1)}
				expectedAnchor, count = 2, 1
			case "負の差分":
				rows = []strategy.Observation{indexObservation(0, at-2), indexObservation(1, at-1),
					indexObservation(2, at), indexObservation(3, at), indexObservation(4, at+1)}
				expectedAnchor, count = 3, 1
				rows[4].E01.Adverse, rows[4].E01.AdverseSource = -1, 0
			case "空履歴":
				rows, count = nil, 0
			}
			p := &prepared{Features: a.features, Plan: a.plan, Generation: mode, Cutoff: now.Add(3 * time.Second),
				Rows: rows, Last: map[string]strategy.Observation{}, SourceHashes: map[string][]string{}}
			if len(rows) > 0 {
				p.Last["TOPIX"] = rows[len(rows)-1]
			}
			anchor, exact := restoreAnchor(m, p)
			if anchor != expectedAnchor || exact {
				t.Fatal("概略復元の位置が不正です", anchor, exact)
			}
			if err := a.activate(p); err != nil {
				t.Fatal("履歴範囲の変化で復元できません", err)
			}
			if m.Portfolio.LastIndex != lastIndex+count || m.Portfolio.Paper.Row != count || m.Portfolio.LastTime < at {
				t.Fatal("過去行を再実行又は新しい行を脱落させました", m.Portfolio.LastIndex, m.Portfolio.Paper.Row)
			}
			if len(m.Manager.Records()) != 0 || len(m.Saved.Queue) != 0 {
				t.Fatal("欠損履歴から注文を作りました")
			}
		})
	}
}

// TestRunnerInvalidLivePriceIsSavedOnly はnullと0の価格を原文保存し、運転を停止しません。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestRunnerInvalidLivePriceIsSavedOnly(t *testing.T) {
	a, m, now := indexDayApp(t)
	for i, price := range []any{nil, 0., -1.} {
		e := indexFeed(t, now.Add(time.Duration(i)*time.Millisecond), price)
		if err := a.onBoard(context.Background(), e); err != nil {
			t.Fatal("不正現在値で運転が停止しました", price, err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(a.cfg.Root, "market.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow("SELECT count(*) FROM books").Scan(&count); err != nil || count != 3 {
		t.Fatal("判断除外した受信原文を保存していません", count, err)
	}
	if a.features.Counts["TOPIX"] != 0 || m.Portfolio.LastIndex != -1 ||
		len(m.Saved.Queue) != 0 || len(m.Manager.Records()) != 0 {
		t.Fatal("不正現在値を特徴又は注文へ適用しました")
	}
	if err = a.onBoard(context.Background(), indexFeed(t, now.Add(3*time.Millisecond), 100.)); err != nil {
		t.Fatal("正常価格へ回復後も処理できません", err)
	}
	if m.Portfolio.LastIndex != 0 || a.features.Counts["TOPIX"] != 1 {
		t.Fatal("正常復帰後の初回連番が不正です")
	}
}

// TestRunnerRecoveryRetainsExitWithoutCreatingOrders は過去の停止到達で架空注文列を増やしません。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestRunnerRecoveryRetainsExitWithoutCreatingOrders(t *testing.T) {
	_, m, now := indexDayApp(t)
	p := m.Portfolio
	p.Core.Position = strategy.Position{Owner: "core", Side: 1, Quantity: 5, InitialQuantity: 5,
		Average: 100, Risk: 10, RiskFraction: 1}
	for i := int64(0); i < 1000; i++ {
		o := indexObservation(i, seconds(now)+float64(i)/1000)
		o.Last, o.Bid, o.Ask = 89, 88.5, 89.5
		if err := p.Advance(o); err != nil {
			t.Fatal(err)
		}
	}
	if p.Core.Position.Quantity != 5 || p.Core.CloseTarget != 0 {
		t.Fatal("過去価格で決済したか、観測済みの返済要求を捨てました")
	}
	if len(p.Orders) != 0 || p.Sequence != 0 || len(p.Status().Pending) != 0 {
		t.Fatal("復元した過去行ごとに架空の返済注文を作りました", len(p.Orders), p.Sequence)
	}
	current := indexObservation(1000, seconds(now)+1)
	s, err := p.Observe(current, false)
	if err != nil || len(s) != 1 || s[0].Action != "close" || s[0].Quantity != 5 {
		t.Fatal("現在へ追い付いた後に一度だけ返済依頼を出せません", s, err)
	}
}

// TestRunnerEntryPermissionDoesNotPreserveBrokenRetest は新規停止中も既知の突破線破綻を取り消します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestRunnerEntryPermissionDoesNotPreserveBrokenRetest(t *testing.T) {
	_, m, _, now := integrationApp(t, "NK225", true)
	p := m.Portfolio
	p.Session.ExtraAt = p.Session.End
	p.Core.Arm.Side = 1
	p.Core.Arm.Stage = 2
	p.Core.Arm.Level = 100
	p.Core.Arm.Risk = 10
	p.Core.Arm.At = seconds(now) - 120
	p.Core.Arm.ReturnExtreme = 101
	p.Core.Arm.ProgressIndex = 0
	p.Core.Arm.ReturnIndex = 1
	o := indexObservation(2, seconds(now))
	o.Reference, o.Last, o.Bid, o.Ask, o.ATR = "NK225", 97, 96.75, 97.25, 10
	signals, err := p.Observe(o, false)
	if err != nil || len(signals) != 0 {
		t.Fatal("新規許可なしで発注しました", signals, err)
	}
	if p.Core.Arm.Side != 0 {
		t.Fatal("送信許可の停止を、既知の突破線破綻の未観測として扱いました")
	}
}
