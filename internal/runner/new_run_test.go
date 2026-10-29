package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/schedule"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// TestRunStartsFreshAfterQuantityChange は旧仮想玉や未反映通知を復元せず、上限変更後に新規起動します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestRunStartsFreshAfterQuantityChange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatal("新規起動試験で変更APIを呼びました")
		}
		http.Error(w, "読取停止中の試験", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cfg := startupConfig(t, server.URL, "NK225micro")
	base := filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), "dryrun", cfg.InstanceID())
	logs := filepath.Join(cfg.Path(cfg.Runtime.LogDirectory), "dryrun", cfg.InstanceID(), "NK225micro")
	path := filepath.Join(base, "NK225micro.sqlite")
	old, err := execution.Open(logs, path, true, "NK225micro", 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 26, 10, 0, 0, 0, time.FixedZone("JST", 9*3600))
	if _, err = old.Submit(context.Background(), nil, execution.Intent{Owner: "core", Product: "NK225micro", ID: "old-id", Session: "2026-10-26_Day", ContractSymbol: "micro-contract", Side: "buy", TradeType: "new", Quantity: 20, Price: 65000, At: at, ExpiresAt: at.Add(time.Hour)}, controller.OrdersView{InstanceID: "old"}); err != nil {
		t.Fatal(err)
	}
	oldTrades, _ := json.Marshal(old.Records())
	if err = old.Checkpoint(json.RawMessage(`{"version":99,"Signature":"old","namespace":"old-run","Portfolio":{"unknown":"saved"},"Queue":[{"past":"new"}]}`)); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()
	cfg.Methods = config.Methods{Day: "TG04", Night: "TG04"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = Run(ctx, cfg); err != nil {
		t.Fatal("上限20→10、旧戦略・予約ありで新規起動できません", err)
	}
	fresh, err := execution.Open(logs, path, true, "NK225micro", 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	newTrades, _ := json.Marshal(fresh.Records())
	var saved savedRuntime
	if err = json.Unmarshal(fresh.State(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Namespace == "old-run" || len(saved.Namespace) != 32 || len(saved.Queue) != 0 || len(saved.Portfolio) > 0 && string(saved.Portfolio) != "null" || len(fresh.Positions()) != 0 || len(fresh.OutstandingFills()) != 0 || string(oldTrades) != string(newTrades) {
		t.Fatal("過去状態の持越し又は過去約定改変があります", saved)
	}
}

// TestResearchFreshWarmupDoesNotReplayVirtualTrades は過去観測を指標だけに使い、候補を新鮮な入力から始めます。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearchFreshWarmupDoesNotReplayVirtualTrades(t *testing.T) {
	a, m, calls, now := integrationApp(t, "NK225", true)
	a.cfg.Methods = config.Methods{Day: "TG04", Night: "TG04"}
	contract := m.Saved.Contract
	a.registrations = map[string]controller.Future{contract.FutureCode: contract}
	m.Portfolio = nil
	m.Saved.Plan = schedule.Plan{}
	m.Saved.Namespace = "new-run"
	row := strategy.Observation{Reference: "NK225", Index: 90000, Time: seconds(now.Add(-time.Minute)), Received: seconds(now.Add(-time.Minute)), Last: 65000, Bid: 64990, Ask: 65000, ATR: 80, QuoteOK: true}
	p := &prepared{Features: a.features, Plan: a.plan, Generation: a.generation, Cutoff: now.Add(-time.Second), Rows: []strategy.Observation{row}, Last: map[string]strategy.Observation{"NK225": row}}
	if err := a.activate(p); err != nil {
		t.Fatal(err)
	}
	if m.Portfolio == nil || m.Portfolio.Research == nil || m.Portfolio.LastIndex != -1 {
		t.Fatal("旧履歴を新候補の売買経路に再生しました")
	}
	for _, sleeve := range m.Portfolio.Research.Sleeves {
		if sleeve.Source.LastIndex != -1 || len(sleeve.Accepted) != 0 || len(sleeve.Lots) != 0 {
			t.Fatal("過去の親や仮想玉が新担当へ残りました", sleeve.Name)
		}
	}
	row.Index++
	row.Time, row.Received = seconds(now), seconds(now)
	if _, err := m.Portfolio.Observe(row, true); err != nil {
		t.Fatal("暖機後の大きな連番から候補を開始できません", err)
	}
	for _, sleeve := range m.Portfolio.Research.Sleeves {
		if sleeve.Source.LastIndex != row.Index {
			t.Fatal("新鮮な受信へ接続できません", sleeve.Name)
		}
	}
	if calls.Load() != 0 || len(m.Manager.Records()) != 0 || len(m.Saved.Queue) != 0 {
		t.Fatal("暖機だけで発注・架空約定を生成しました")
	}
}
