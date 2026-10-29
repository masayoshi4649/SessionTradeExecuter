package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
)

// startupConfig は実口座に接続しない単一商品起動検査の設定を作ります。
//
// 引数: t(*testing.T)、endpoint/product(string)。返り値: config.Config は検証済み設定です。
func startupConfig(t *testing.T, endpoint, product string) config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Root = t.TempDir()
	cfg.Controller.BaseURL = endpoint
	cfg.Controller.RequestTimeoutSeconds = 1
	cfg.Controller.HistoryTimeoutSeconds = 1
	cfg.Runtime.ShutdownSeconds = 1
	cfg.Market = &config.SingleMarket{Product: product, MaxPerSide: 10}
	cfg.Products = map[string]config.Product{
		"TOPIX": {Tick: .5, PointValue: 10000}, "NK225": {Tick: 10, PointValue: 1000},
		"TOPIXmini": {Tick: .25, PointValue: 1000}, "NK225micro": {Tick: 5, PointValue: 10},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// storedRuntime は停止済み運転器のSQLiteから保存された運転識別子を読みます。
//
// 引数: t(*testing.T)、cfg(config.Config)。返り値: savedRuntime は復元された状態です。
func storedRuntime(t *testing.T, cfg config.Config) savedRuntime {
	t.Helper()
	_, market := cfg.Selected()
	base := filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), mode(cfg.Trading.DryRun), cfg.InstanceID())
	logs := filepath.Join(cfg.Path(cfg.Runtime.LogDirectory), mode(cfg.Trading.DryRun), cfg.InstanceID(), market.Product)
	manager, err := execution.Open(logs, filepath.Join(base, market.Product+".sqlite"), cfg.Trading.DryRun,
		market.Product, cfg.Products[market.Product].PointValue, market.MaxPerSide)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	var saved savedRuntime
	if err = json.Unmarshal(manager.State(), &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

// TestRunPersistsNamespaceBeforeFirstSession は場の準備前に運転IDを確定し、毎起動で新しいIDにすることを検査します。
//
// 引数: t(*testing.T) は検査環境です。返り値: なし。
func TestRunPersistsNamespaceBeforeFirstSession(t *testing.T) {
	mutations := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
		}
		http.Error(w, "検査中の通信停止", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, product := range []string{"TOPIXmini", "NK225micro"} {
		t.Run(product, func(t *testing.T) {
			cfg := startupConfig(t, server.URL, product)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := Run(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			first := storedRuntime(t, cfg)
			if len(first.Namespace) != 32 || (len(first.Portfolio) != 0 && string(first.Portfolio) != "null") {
				t.Fatal("場の準備前に運転IDを保存していません", first.Namespace)
			}
			base := filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), mode(cfg.Trading.DryRun), cfg.InstanceID())
			other := "NK225micro"
			if product == other {
				other = "TOPIXmini"
			}
			if _, err := os.Stat(filepath.Join(base, other+".sqlite")); !os.IsNotExist(err) {
				t.Fatal("非選択商品に運転台帳を作成しました", err)
			}
			if err := Run(ctx, cfg); err != nil {
				t.Fatal("再起動に失敗しました", err)
			}
			if next := storedRuntime(t, cfg); next.Namespace == first.Namespace || len(next.Namespace) != 32 {
				t.Fatal("再起動で新しい運転IDを発行しませんでした", first.Namespace, next.Namespace)
			}
			if _, err := os.Stat(cfg.Path(cfg.Runtime.LogDirectory)); !os.IsNotExist(err) {
				t.Fatal("場の準備前に旧ログ階層又はtraceを生成しました", err)
			}
		})
	}
	if mutations.Load() != 0 {
		t.Fatal("起動検査で変更APIを呼び出しました")
	}
}

// TestSingleMarketStillObservesBothLargeMarkets は非売買側のラージも特徴へ反映し、戦略と注文台帳は選択側だけに保ちます。
//
// 引数: t(*testing.T) は検査環境です。返り値: なし。
func TestSingleMarketStillObservesBothLargeMarkets(t *testing.T) {
	a, m, now := indexDayApp(t)
	peer := indexFeed(t, now.Add(-time.Millisecond), 100.)
	peer.Board.Symbol = "large-NK225"
	peer.Board.Raw = nil
	if err := a.onBoard(context.Background(), peer); err != nil {
		t.Fatal(err)
	}
	if err := a.onBoard(context.Background(), indexFeed(t, now, 100.)); err != nil {
		t.Fatal(err)
	}
	if a.features.Counts["TOPIX"] != 1 || a.features.Counts["NK225"] != 1 {
		t.Fatal("どちらかのラージ入力を更新しませんでした", a.features.Counts)
	}
	if len(a.markets) != 1 || a.markets["NK225"] != nil || a.decisionCounts["NK225"] != 0 || a.decisionCounts["TOPIX"] != 1 {
		t.Fatal("非選択側にも売買担当を作成しました")
	}
	if len(m.Manager.Records()) != 0 {
		t.Fatal("特徴準備だけで約定を生成しました")
	}
}

// TestForeignDatabaseCannotCloseOwnedHolding は同商品・同担当名でも別台帳の約定IDを自分の所有として返済できないことを検査します。
//
// 引数: t(*testing.T) は検査環境です。返り値: なし。
func TestForeignDatabaseCannotCloseOwnedHolding(t *testing.T) {
	a, m, calls, now := integrationApp(t, "TOPIX", false)
	queueExtra(t, a, m, now)
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	view := a.view
	view.BrokerOrders = []controller.BrokerOrder{{ID: "mock-order", Symbol: m.Saved.Contract.Symbol, Side: "2", State: 5, CumQty: 2,
		Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "owned-hold", Qty: 2, Price: 111, TransactTime: now.Format(time.RFC3339Nano)}}}}
	view.Positions = []controller.Position{{ExecutionID: "owned-hold", Symbol: m.Saved.Contract.Symbol, Side: "buy", Quantity: 2, AvailableQuantity: 2}}
	if err := a.handle(context.Background(), feedEvent{Kind: "口座", At: now, View: view}); err != nil {
		t.Fatal(err)
	}
	if m.Manager.Quantity("extra", "buy") != 2 {
		t.Fatal("検査元の所有建玉を確定できません")
	}
	root := t.TempDir()
	other, err := execution.Open(filepath.Join(root, "log"), filepath.Join(root, "other.sqlite"), false, "TOPIXmini", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	intent := execution.Intent{ID: "他運転の返済", Owner: "extra", Product: "TOPIXmini", Session: a.plan.ID,
		ContractSymbol: m.Saved.Contract.Symbol, Side: "sell", TradeType: "close", Quantity: 2, Price: 110,
		At: now, ExpiresAt: now.Add(time.Minute)}
	if _, err := other.Submit(context.Background(), a.client, intent, view); err == nil {
		t.Fatal("別台帳で所有していない建玉を返済できました")
	}
	if calls.Load() != 1 || len(other.Orders()) != 0 || len(other.Records()) != 0 || m.Manager.Quantity("extra", "buy") != 2 {
		t.Fatal("他運転の返済要求で送信又は所有状態が変わりました")
	}
}
