package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/schedule"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// nextSessionFixture は新しい場だけの指標とcontrollerの場情報を人工生成します。
//
// 引数: t(*testing.T)、a(*app)、m(*marketRuntime)、kind(string)はDay/Nightです。
// 返り値: *preparedは注文を再生していない次場の準備結果です。
func nextSessionFixture(t *testing.T, a *app, m *marketRuntime, kind string) *prepared {
	t.Helper()
	start := time.Date(2026, 10, 28, 8, 45, 0, 0, time.FixedZone("JST", 9*3600))
	length := 7 * time.Hour
	if kind == "Night" {
		start = time.Date(2026, 10, 28, 17, 0, 0, 0, start.Location())
		length = 13 * time.Hour
	}
	plan, err := schedule.Build(start.Format("2006-01-02")+"_"+kind, start, start.Add(length), a.cfg.Schedule)
	if err != nil {
		t.Fatal(err)
	}
	session := strategy.Session{ID: plan.ID, Kind: kind, Start: seconds(plan.Start), ForceClose: seconds(plan.ForceClose), End: seconds(plan.End), USOpen: seconds(plan.USOpen)}
	features, err := strategy.NewFeatures(session, map[string]float64{"TOPIX": .5, "NK225": 10}, strategy.DefaultParameters(m.Reference, kind).Features)
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.Windows = nil
	a.registrations = map[string]controller.Future{"TOPIX": {Symbol: "large-TOPIX"}, "NK225": {Symbol: "large-NK225"}, a.cfg.Markets[m.Reference].Product: m.Saved.Contract}
	a.view.Session = &controller.Session{StartsAt: plan.Start, EndsAt: plan.End}
	a.bundle.One["TOPIX"] = controller.Candles{Snapshot: controller.Snapshot{SessionID: plan.ID, Generation: "next-generation", Session: *a.view.Session}}
	return &prepared{Plan: plan, Features: features, Generation: "next-generation", Cutoff: plan.Start, Last: map[string]strategy.Observation{}}
}

// TestSessionSwitchResetsDisplayStrategyAndFeatures は昼夜両方向で旧場の状態が混入しないことを検証します。
//
// 引数: t(*testing.T)。返り値: なし。無取引の新場を0円で表示し、旧場履歴は保持します。
func TestSessionSwitchResetsDisplayStrategyAndFeatures(t *testing.T) {
	for _, nextKind := range []string{"Day", "Night"} {
		t.Run(nextKind, func(t *testing.T) {
			a, m, calls, now := integrationApp(t, "TOPIX", true)
			oldKind := "Night"
			if nextKind == "Night" {
				oldKind = "Day"
			}
			m.Saved.Plan.ID, m.Saved.Plan.Kind = "2026-10-27_"+oldKind, oldKind
			a.plan = m.Saved.Plan
			oldID, oldPortfolio, oldFeatures := m.Saved.Plan.ID, m.Portfolio, a.features
			m.Portfolio.CoreEntered, m.Portfolio.ExtraAttempted = true, true
			m.Saved.Done["旧場注文"] = true
			m.Saved.Queue = []queuedSignal{{Signal: strategy.Signal{ID: "未送信旧場", Action: "new"}}}
			a.lastHandled, a.inputCounts, a.decisionCounts = map[string]time.Time{"TOPIX": now}, map[string]uint64{"TOPIX": 9}, map[string]uint64{"TOPIX": 8}
			recordSnapshotFill(m, execution.Fill{ID: "旧場約定", Session: oldID, PnL: 12345, Fee: 45})
			p := nextSessionFixture(t, a, m, nextKind)
			if !a.needsPrepare() {
				t.Fatal("平坦な旧場から次場の準備が開始されません")
			}
			if err := a.activate(p); err != nil {
				t.Fatal(err)
			}
			var got Snapshot
			a.publish = func(s Snapshot) { got = s }
			a.publishSnapshot(p.Plan.Start, true)
			if got.Session != p.Plan.ID || got.RealizedYen != 0 || got.SessionRealizedYen != 0 || got.FeesYen != 0 || len(got.RecentTrades) != 0 || got.DecisionCount != 0 || !got.DecisionAt.IsZero() {
				t.Fatal("旧場の表示値が新場へ残っています", got)
			}
			if a.features == oldFeatures || a.features != p.Features || m.Portfolio == oldPortfolio || m.Portfolio.Session.ID != p.Plan.ID || m.Portfolio.CoreEntered || m.Portfolio.ExtraAttempted || len(m.Saved.Done) != 0 || len(m.Saved.Queue) != 0 {
				t.Fatal("旧場の戦略・指標・発注待ちが初期化されません")
			}
			if m.SessionRealized[oldID] != 12300 || calls.Load() != 0 {
				t.Fatal("旧場履歴を消したか切替だけで実注文しました")
			}
			// 遅延通知の帰属は表示中の場ではなく、確定約定が持つ元の場です。
			recordSnapshotFill(m, execution.Fill{ID: "旧場遅延", Session: oldID, PnL: -100, Fee: 1})
			a.publishSnapshot(p.Plan.Start, true)
			if got.RealizedYen != 0 || got.FeesYen != 0 || len(got.RecentTrades) != 0 || m.SessionRealized[oldID] != 12199 {
				t.Fatal("旧場の遅延約定を新場へ誤計上しました", got)
			}
		})
	}
}

// directSessionOrder は切替障害試験用の所有注文をfakeまたはdryrunで登録します。
//
// 引数: t(*testing.T)、a(*app)、m(*marketRuntime)、now(time.Time)、action/id(string)。返り値: なし。
func directSessionOrder(t *testing.T, a *app, m *marketRuntime, now time.Time, action, id string) {
	t.Helper()
	side, price := "buy", 100.0
	if action == "close" {
		side, price = "sell", 102
	}
	_, err := m.Manager.Submit(context.Background(), a.client, execution.Intent{Owner: "core", Product: a.cfg.Markets[m.Reference].Product,
		ID: id, Session: m.Saved.Plan.ID, ContractSymbol: m.Saved.Contract.Symbol, Side: side, TradeType: action,
		Quantity: 1, Price: price, At: now, ExpiresAt: now.Add(time.Minute)}, a.view)
	if err != nil {
		t.Fatal(err)
	}
}

// TestSessionSwitchPreservesOwnedExposure は所有玉・結果不明・未反映返済を初期化で消さないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。通信はhttptestのfake以外へ送信しません。
func TestSessionSwitchPreservesOwnedExposure(t *testing.T) {
	for _, scenario := range []string{"所有玉", "未確定注文", "未反映返済"} {
		t.Run(scenario, func(t *testing.T) {
			a, m, _, now := integrationApp(t, "TOPIX", scenario != "未確定注文")
			directSessionOrder(t, a, m, now, "new", "old-new")
			if scenario != "未確定注文" {
				if err := m.Manager.Checkpoint(nil); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "未反映返済" {
				directSessionOrder(t, a, m, now.Add(time.Second), "close", "old-close")
			}
			oldID, oldPortfolio, records, positions := m.Saved.Plan.ID, m.Portfolio, len(m.Manager.Records()), len(m.Manager.Positions())
			p := nextSessionFixture(t, a, m, "Day")
			if a.needsPrepare() || a.activate(p) == nil {
				t.Fatal("未解決の旧場を残して新場へ進みました")
			}
			if m.Saved.Plan.ID != oldID || m.Portfolio != oldPortfolio || len(m.Manager.Records()) != records || len(m.Manager.Positions()) != positions || !strings.Contains(a.sessionTransitionReason(m), "旧状態を保持") {
				t.Fatal("旧場の所有情報を変更したか停止理由が明示されません")
			}
		})
	}
}

// TestSessionSwitchIgnoresObsoleteSummary は廃止した集計先が場替わりを妨げないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。旧集計の削除や修復も行いません。
func TestSessionSwitchIgnoresObsoleteSummary(t *testing.T) {
	a, m, _, _ := integrationApp(t, "TOPIX", true)
	oldPortfolio, oldID := m.Portfolio, m.Saved.Plan.ID
	blocked := filepath.Join(a.cfg.Root, "log", "TOPIXmini", "session_summary_dryrun.csv")
	if err := os.MkdirAll(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	p := nextSessionFixture(t, a, m, "Day")
	if err := a.activate(p); err != nil || m.Portfolio == oldPortfolio || m.Saved.Plan.ID == oldID {
		t.Fatal("廃止した集計先の状態が場替わりへ影響しています", err)
	}
	if info, err := os.Stat(blocked); err != nil || !info.IsDir() {
		t.Fatal("旧集計のパスを変更しました", err)
	}
}

// TestSessionDisplayRestoresAcrossFreshRuns は同一場のOFF/ON相当の新起動でも表示損益を合算します。
//
// 引数: t(*testing.T)。返り値: なし。旧売買状態を復元せず確定約定だけを二重計上なく復元します。
func TestSessionDisplayRestoresAcrossFreshRuns(t *testing.T) {
	a, m, _, now := integrationApp(t, "TOPIX", true)
	directSessionOrder(t, a, m, now, "new", "first-run-new")
	if err := m.Manager.Checkpoint(nil); err != nil {
		t.Fatal(err)
	}
	directSessionOrder(t, a, m, now.Add(time.Second), "close", "first-run-close")
	expected, fees := 0.0, 0.0
	for _, trade := range m.Manager.Records() {
		expected += trade.PnL - trade.Fee
		fees += trade.Fee
	}
	if err := m.Manager.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := execution.OpenNewRun(context.Background(), nil, filepath.Join(a.cfg.Root, "log", "TOPIXmini"), filepath.Join(a.cfg.Root, "TOPIXmini.sqlite"), true, "TOPIXmini", 1000, 3, "次起動", json.RawMessage(`{"namespace":"次起動"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	fresh := &marketRuntime{Reference: m.Reference, Manager: manager, Saved: savedRuntime{Plan: m.Saved.Plan}}
	restoreSnapshotHistory(fresh)
	restoreSnapshotHistory(fresh)
	a.markets[m.Reference] = fresh
	var got Snapshot
	a.publish = func(s Snapshot) { got = s }
	a.publishSnapshot(now.Add(2*time.Second), true)
	if got.RealizedYen != expected || got.SessionRealizedYen != expected || got.FeesYen != fees || len(got.RecentTrades) != 2 || got.BuyQuantity != 0 || got.SellQuantity != 0 {
		t.Fatal("同場の新起動で履歴を失うか重複計上しました", got)
	}
}
