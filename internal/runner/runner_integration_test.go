package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/schedule"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// integrationApp は外部接続なしの実装一式と送信回数計を用意します。
//
// 引数: t *testing.T は試験、reference string はラージ名、dry bool は仮想指定です。
// 返り値: *app、*marketRuntime、*atomic.Int32、time.Time は運転器・担当・HTTP送信数・現在です。
func integrationApp(t *testing.T, reference string, dry bool) (*app, *marketRuntime, *atomic.Int32, time.Time) {
	t.Helper()
	now := time.Now().Truncate(time.Millisecond)
	root := t.TempDir()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/redirect/sendorder/future" {
			t.Errorf("想定外の送信先です: %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"Result":0,"OrderId":"mock-order"}`)
	}))
	t.Cleanup(server.Close)
	client, err := controller.New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.SetDryRun(dry)
	cfg := config.Defaults()
	cfg.Root, cfg.Trading.DryRun, cfg.Controller.BaseURL = root, dry, server.URL
	cfg.Products = map[string]config.Product{
		"TOPIX": {Tick: .5, PointValue: 10000}, "NK225": {Tick: 10, PointValue: 1000},
		"TOPIXmini": {Tick: .25, PointValue: 1000}, "NK225micro": {Tick: 5, PointValue: 10},
	}
	product, quantity, cap := "TOPIXmini", 5, 10
	if reference == "NK225" {
		product, quantity, cap = "NK225micro", 10, 20
	}
	cfg.Markets[reference] = config.Market{Enabled: true, Product: product, CoreQuantity: quantity, ExtraQuantity: quantity, MaxPerSide: cap}
	plan := schedule.Plan{ID: now.Format("2006-01-02") + "_Night", Kind: "Night", Start: now.Add(-time.Hour), ForceClose: now.Add(time.Hour), End: now.Add(2 * time.Hour), USOpen: now.Add(-3 * time.Hour), TopixExtra: now.Add(-time.Second), NKExtra: now.Add(-time.Second)}
	session := strategy.Session{ID: plan.ID, Kind: plan.Kind, Start: seconds(plan.Start), ForceClose: seconds(plan.ForceClose), End: seconds(plan.End), USOpen: seconds(plan.USOpen), ExtraAt: seconds(now.Add(-time.Second))}
	params := strategy.DefaultParameters(reference, "Night")
	portfolio, err := strategy.New(strategy.Config{Reference: reference, Product: product, CoreQuantity: quantity, ExtraMaxQuantity: quantity,
		Cap: cap, LargeTick: cfg.Products[reference].Tick, PointValue: cfg.Products[product].PointValue, Parameters: params}, session)
	if err != nil {
		t.Fatal(err)
	}
	features, err := strategy.NewFeatures(session, map[string]float64{"TOPIX": .5, "NK225": 10}, params.Features)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := execution.Open(filepath.Join(root, "log", product), filepath.Join(root, product+".sqlite"), dry, product, cfg.Products[product].PointValue, cap)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	store, err := journal.Open(filepath.Join(root, "market.sqlite"), filepath.Join(root, "log", "market"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	contract := controller.Future{FutureCode: product, Symbol: "contract-" + product}
	m := &marketRuntime{Reference: reference, Manager: manager, Portfolio: portfolio,
		Saved: savedRuntime{Version: 1, Algorithm: algorithmVersion, Signature: cfg.Signature(), Plan: plan, Contract: contract, Done: map[string]bool{}}}
	a := &app{cfg: cfg, client: client, store: store, markets: map[string]*marketRuntime{reference: m}, quotes: map[string]quote{},
		submissionClock: func(observed time.Time) time.Time { return observed },
		lastMessage:     map[string]time.Time{}, lastSource: map[string]float64{}, features: features, plan: plan, generation: "test-generation",
		viewAt: now, activeAt: now.Add(-time.Minute), cutoff: now.Add(-time.Minute),
		view: controller.OrdersView{InstanceID: "test-instance", Connected: true, BrokerUpdatedAt: &now, PositionsUpdatedAt: &now,
			Session: &controller.Session{Name: "night", Exchange: 34, StartsAt: plan.Start, SendFrom: plan.Start, SendUntil: plan.End, EndsAt: plan.End}},
		bundle: candleBundle{One: map[string]controller.Candles{"TOPIX": {Snapshot: controller.Snapshot{Generation: "test-generation"}}}},
	}
	for _, code := range []string{"TOPIX", "NK225", product} {
		symbol, bid, ask := "large-"+code, 99.75, 100.25
		if code == product {
			symbol, bid, ask = contract.Symbol, 109.75, 110.25
		}
		a.quotes[code] = quote{Board: controller.Board{Symbol: symbol, CurrentPrice: 100, CurrentPriceTime: now.Format(time.RFC3339Nano),
			Buy1: controller.Level{Price: bid, Qty: 5}, Sell1: controller.Level{Price: ask, Qty: 5}}, Received: now}
	}
	return a, m, calls, now
}

// queueExtra は人工観測で実際の戦略から追加新規を作り、注文前状態を保存します。
//
// 引数: t *testing.T は試験、a *app と m *marketRuntime は実装、now time.Time は判断時刻です。
// 返り値: strategy.Signal は生成された追加新規です。
func queueExtra(t *testing.T, a *app, m *marketRuntime, now time.Time) strategy.Signal {
	t.Helper()
	h := strategy.History{Known: true, Values: [15]float64{99, 98, 2, 102, 90, .5, 2, 4, 95, seconds(now) - 60, 2, 4, .5, 5, seconds(now) - 120}}
	o := strategy.Observation{Reference: m.Reference, Time: seconds(now), Received: seconds(now), Index: 0,
		Last: 100, Bid: 99.75, Ask: 100.25, ATR: 10, QuoteOK: true, MidHistory: h}
	signals, err := m.Portfolio.Observe(o, true)
	if err != nil || len(signals) != 1 || signals[0].Owner != "extra" {
		t.Fatalf("追加新規の生成が不正です: %v %+v", err, signals)
	}
	if err = a.enqueue(m, signals, 42, now); err != nil {
		t.Fatal(err)
	}
	return signals[0]
}

// restoreRuntime は台帳の戦略正本だけを読み直します。
//
// 引数: t *testing.T は試験、m *marketRuntime は復旧先です。
// 返り値: なし。
func restoreRuntime(t *testing.T, m *marketRuntime) {
	t.Helper()
	if err := json.Unmarshal(m.Manager.State(), &m.Saved); err != nil {
		t.Fatal(err)
	}
	p, err := strategy.Restore(m.Saved.Portfolio)
	if err != nil {
		t.Fatal(err)
	}
	m.Portfolio = p
}

// TestIntegrationDryRunUsesExecutionProduct はラージとは別のミニ・マイクロ気配を使用します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestIntegrationDryRunUsesExecutionProduct(t *testing.T) {
	for _, ref := range []string{"TOPIX", "NK225"} {
		t.Run(ref, func(t *testing.T) {
			a, m, calls, now := integrationApp(t, ref, true)
			signal := queueExtra(t, a, m, now)
			if !a.canEnter(m, now) {
				t.Fatal("人工の新鮮状態が拒否されました")
			}
			restoreRuntime(t, m)
			if len(m.Saved.Queue) != 1 || len(m.Manager.Records()) != 0 {
				t.Fatal("注文前のキューと未約定を復元できません")
			}
			if err := a.dispatch(context.Background(), m, now); err != nil {
				t.Fatal(err)
			}
			trades := m.Manager.Records()
			if len(trades) != 1 || trades[0].Price != 110.25 || trades[0].Owner != "extra" || trades[0].Quantity != signal.Quantity {
				t.Fatalf("発注商品の気配・担当と不一致です: %+v", trades)
			}
			if calls.Load() != 0 || len(m.Manager.OutstandingFills()) != 0 || len(m.Saved.Queue) != 0 {
				t.Fatal("仮想送信またはACKが不正です")
			}
			signals := m.Portfolio.Flatten(seconds(now.Add(time.Second)), "検証返済")
			if err := a.enqueue(m, signals, 43, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := a.dispatch(context.Background(), m, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			trades = m.Manager.Records()
			if len(trades) != 2 || trades[1].Price != 109.75 || m.Manager.Quantity("extra", "buy") != 0 || calls.Load() != 0 {
				t.Fatal("仮想返済が発注商品の売気配を使っていません")
			}
		})
	}
}

// TestIntegrationSubmissionCrashBeforeACK は約定保存後・戦略ACK前の中断から一度だけ復旧します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestIntegrationSubmissionCrashBeforeACK(t *testing.T) {
	a, m, calls, now := integrationApp(t, "TOPIX", true)
	s := queueExtra(t, a, m, now)
	intent := execution.Intent{ID: s.ID, Owner: s.Owner, Product: m.Saved.Contract.FutureCode, Session: m.Saved.Plan.ID,
		ContractSymbol: m.Saved.Contract.Symbol, Side: "buy", TradeType: "new", Quantity: s.Quantity, Price: 110.25,
		At: now, ExpiresAt: now.Add(time.Minute)}
	if _, err := m.Manager.Submit(context.Background(), a.client, intent, a.view); err != nil {
		t.Fatal(err)
	}
	// 台帳の約定だけ確定済み、戦略正本は送信前のままの状態を復元します。
	restoreRuntime(t, m)
	if len(m.Manager.OutstandingFills()) != 1 {
		t.Fatal("クラッシュ境界の未反映約定がありません")
	}
	if err := a.applyFills(m); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	restoreRuntime(t, m)
	if err := a.applyFills(m); err != nil {
		t.Fatal(err)
	}
	if len(m.Manager.Records()) != 1 || m.Manager.Quantity("extra", "buy") != s.Quantity ||
		len(m.Manager.OutstandingFills()) != 0 || len(m.Saved.Queue) != 0 || calls.Load() != 0 {
		t.Fatal("約定を失ったか、重複送信・重複ACKしました")
	}
}

// TestIntegrationPartialAndOwner は実注文受付と部分約定終了を分け、追加担当へだけ反映します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestIntegrationPartialAndOwner(t *testing.T) {
	a, m, calls, now := integrationApp(t, "NK225", false)
	s := queueExtra(t, a, m, now)
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(m.Manager.Records()) != 0 || !m.Manager.HasPending() {
		t.Fatal("受付だけで約定させました")
	}
	view := a.view
	view.BrokerOrders = []controller.BrokerOrder{{ID: "mock-order", Symbol: m.Saved.Contract.Symbol, Side: "2", State: 5, CumQty: 2,
		Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "mock-hold", Qty: 2, Price: 111, TransactTime: now.Format(time.RFC3339Nano)}}}}
	view.Positions = []controller.Position{{ExecutionID: "mock-hold", Symbol: m.Saved.Contract.Symbol, Side: "buy", Quantity: 2, AvailableQuantity: 2}}
	if err := a.handle(context.Background(), feedEvent{Kind: "口座", At: now, View: view}); err != nil {
		t.Fatal(err)
	}
	status := m.Portfolio.Status()
	extraQuantity := 0
	for _, position := range status.Positions {
		if position.Owner == "extra" {
			extraQuantity = position.Quantity
		}
	}
	if extraQuantity != 2 ||
		!m.Saved.Done[s.ID] || m.Manager.Quantity("core", "buy") != 0 || m.Manager.HasPending() {
		t.Fatal("部分約定または担当の照合に誤りがあります")
	}
	if err := a.handle(context.Background(), feedEvent{Kind: "口座", At: now, View: view}); err != nil || len(m.Manager.Records()) != 1 {
		t.Fatal("同じ約定を重複反映しました")
	}
}

// TestIntegrationMissingSessionAndStaleQuote は相場情報の欠落で新規を送信しません。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestIntegrationMissingSessionAndStaleQuote(t *testing.T) {
	for _, fault := range []string{"場なし", "商品気配期限", "ラージ価格期限", "世代違い", "商品違い"} {
		t.Run(fault, func(t *testing.T) {
			a, m, calls, now := integrationApp(t, "TOPIX", true)
			queueExtra(t, a, m, now)
			switch fault {
			case "場なし":
				a.view.Session = nil
			case "商品気配期限":
				q := a.quotes["TOPIXmini"]
				q.Received = now.Add(-time.Hour)
				a.quotes["TOPIXmini"] = q
			case "ラージ価格期限":
				q := a.quotes["NK225"]
				q.Board.CurrentPriceTime = now.Add(-time.Hour).Format(time.RFC3339Nano)
				a.quotes["NK225"] = q
			case "世代違い":
				a.generation = "new-generation"
			case "商品違い":
				q := a.quotes["TOPIXmini"]
				q.Board.Symbol = "old-contract"
				a.quotes["TOPIXmini"] = q
			}
			if a.canEnter(m, now) {
				t.Fatal("不正な鮮度・場・限月を見落としました")
			}
			if err := a.dispatch(context.Background(), m, now); err != nil {
				t.Fatal(err)
			}
			if len(m.Manager.Records()) != 0 || calls.Load() != 0 {
				t.Fatal("不正な情報で新規を送信しました")
			}
		})
	}
}

// TestIntegrationQuoteMustBeFinite は有限でない板を正常と認めません。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestIntegrationQuoteMustBeFinite(t *testing.T) {
	a, _, _, now := integrationApp(t, "TOPIX", true)
	q := a.quotes["TOPIXmini"]
	q.Board.Sell1.Price = math.Inf(1)
	a.quotes["TOPIXmini"] = q
	if _, ok := a.validQuote("TOPIXmini", now); ok {
		t.Fatal("無限大の気配を正常と認めました")
	}
}

// TestIntegrationStopRejectsUnsentQueue は停止時の未送信新規を復旧後にも実行しません。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestIntegrationStopRejectsUnsentQueue(t *testing.T) {
	a, m, calls, now := integrationApp(t, "TOPIX", true)
	queueExtra(t, a, m, now)
	if a.flat() {
		t.Fatal("未送信キューを残したまま停止完了と判断しました")
	}
	a.stopping = true
	if err := a.tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	restoreRuntime(t, m)
	if len(m.Saved.Queue) != 0 || len(m.Portfolio.Status().Pending) != 0 || !a.flat() {
		t.Fatal("停止した新規が復旧状態へ残りました")
	}
	a.stopping = false
	if err := a.dispatch(context.Background(), m, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || len(m.Manager.Records()) != 0 {
		t.Fatal("停止後の再開で古い新規を実行しました")
	}
}

// TestIntegrationManagerOnlyHoldingRejected は戦略から欠落した台帳玉を無視しません。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestIntegrationManagerOnlyHoldingRejected(t *testing.T) {
	a, m, _, now := integrationApp(t, "TOPIX", true)
	queueExtra(t, a, m, now)
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	m.Portfolio.Extra.Position.Quantity = 0
	m.Portfolio.Extra.Position.Side = 0
	if err := a.applyFills(m); err == nil {
		t.Fatal("戦略から消えた担当玉を見落としました")
	}
}

// TestIntegrationExpiredSignalIsNotRepriced は古い条件成立を現在価格で新規しません。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestIntegrationExpiredSignalIsNotRepriced(t *testing.T) {
	a, m, calls, now := integrationApp(t, "TOPIX", true)
	queueExtra(t, a, m, now)
	m.Saved.Queue[0].Created = now.Add(-time.Hour)
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	if len(m.Saved.Queue) != 0 || len(m.Portfolio.Status().Pending) != 0 || calls.Load() != 0 || len(m.Manager.Records()) != 0 {
		t.Fatal("期限切れ新規の取消が不正です")
	}
}

// TestIntegrationNoPrematureAck はキューだけのチェックポイントで存在しない約定を作りません。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestIntegrationNoPrematureAck(t *testing.T) {
	a, m, calls, now := integrationApp(t, "NK225", false)
	queueExtra(t, a, m, now)
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	restoreRuntime(t, m)
	if err := a.applyFills(m); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(m.Manager.Records()) != 0 || !m.Manager.HasPending() || len(m.Saved.Queue) != 0 {
		t.Fatal("受付済み未約定を再送または約定扱いしました")
	}
}
