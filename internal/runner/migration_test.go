package runner

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/schedule"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

type migrationFixture struct {
	Config                  config.Config
	LegacyDirectory, Source string
	Store                   *journal.Store
	State                   map[string]any
	Trades                  []journal.Trade
}

// legacyMigrationFixture はWALを閉じない旧台帳と実約定・未確定注文を作成します。
//
// 引数: t *testing.T は試験です。
// 返り値: migrationFixture は外部接続を必要としない移行素材です。
func legacyMigrationFixture(t *testing.T) migrationFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "旧台帳 #保持")
	conf := filepath.Join(root, "conf-legacy")
	if err := os.MkdirAll(conf, 0700); err != nil {
		t.Fatal(err)
	}
	body := `
[trading]
dryrun=true
[schedule]
extra_cutoff_seconds=900
[markets.TOPIX]
enabled=true
product="TOPIXmini"
max_per_side=10
[markets.NK225]
enabled=true
product="NK225micro"
max_per_side=20
[products.TOPIX]
tick=0.5
point_value=10000
[products.TOPIXmini]
tick=0.25
point_value=1000
[products.NK225]
tick=10
point_value=1000
[products.NK225micro]
tick=5
point_value=10
`
	endpointID := sha256.Sum256([]byte(root))
	body += fmt.Sprintf("\n[controller]\nbase_url='http://migration-%x.invalid'\n", endpointID[:8])
	if err := os.WriteFile(filepath.Join(conf, "old.toml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, signature, err := config.ReadLegacyForMigration(conf)
	if err != nil {
		t.Fatal(err)
	}
	cfg := legacy
	cfg.Markets = map[string]config.Market{"TOPIX": legacy.Markets["TOPIX"]}
	cfg.Trading.InstanceID = "topix-mini"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse(time.RFC3339, "2026-10-20T17:00:00+09:00")
	end := start.Add(13 * time.Hour)
	plan, err := schedule.Build("2026-10-20_Night", start, end, cfg.Schedule)
	if err != nil {
		t.Fatal(err)
	}
	parameters := strategy.DefaultParameters("TOPIX", "Night")
	parameters.Extra.EntryCutoffSeconds = cfg.Schedule.ExtraCutoffSeconds
	portfolio, err := strategy.New(strategy.Config{Reference: "TOPIX", Product: "TOPIXmini", CoreQuantity: 5,
		ExtraMaxQuantity: 5, Cap: 10, LargeTick: 0.5, PointValue: 1000, Parameters: parameters},
		strategy.Session{ID: plan.ID, Kind: plan.Kind, Start: seconds(start), End: seconds(end), ForceClose: seconds(plan.ForceClose), USOpen: seconds(plan.USOpen), ExtraAt: seconds(plan.TopixExtra)})
	if err != nil {
		t.Fatal(err)
	}
	now := plan.TopixExtra.Add(time.Second)
	history := strategy.History{Known: true, Values: [15]float64{99, 98, 2, 102, 90, .5, 2, 4, 95, seconds(now) - 60, 2, 4, .5, 5, seconds(now) - 120}}
	signals, err := portfolio.Observe(strategy.Observation{Reference: "TOPIX", Time: seconds(now), Received: seconds(now), Index: 0,
		Last: 100, Bid: 99.75, Ask: 100.25, ATR: 10, QuoteOK: true, MidHistory: history}, true)
	if err != nil || len(signals) != 1 {
		t.Fatalf("人工新規生成: %v / %v", err, signals)
	}
	signal := signals[0]
	tradeID := signal.ID + ":broker-fill-1"
	if err := portfolio.OnFill(strategy.Fill{SignalID: signal.ID, ExecutionID: tradeID, Quantity: 2, Price: 100.25, Time: seconds(now)}); err != nil {
		t.Fatal(err)
	}
	raw, err := portfolio.Dump()
	if err != nil {
		t.Fatal(err)
	}
	runtime := savedRuntime{Version: 1, Algorithm: algorithmVersion, Signature: signature, Plan: plan,
		Contract: controller.Future{FutureCode: "TOPIXmini", Symbol: "old-contract"}, Portfolio: raw,
		Queue: []queuedSignal{{Signal: signal, Evidence: json.RawMessage(`{"input_id":42}`), Created: now}}, Done: map[string]bool{"old-done": true}, IndexOffset: 123}
	state := map[string]any{"symbol": "TOPIXmini", "dryrun": true, "multiplier": 1000, "max_position": 10, "ack_count": 0,
		"runtime_state": runtime,
		"orders": []any{map[string]any{"intent": execution.Intent{Owner: "extra", Product: "TOPIXmini", ID: signal.ID, Session: plan.ID,
			ContractSymbol: "old-contract", Side: "buy", TradeType: "new", Quantity: 5, Price: 100.25, At: now},
			"status": "unknown", "broker_order_id": "broker-order-1", "seen": map[string]int{"broker-fill-1": 2}, "instance_id": "controller-old"}},
		"holdings": map[string]any{"broker-fill-1": execution.Holding{Owner: "extra", Product: "TOPIXmini", HoldID: "broker-fill-1",
			ContractSymbol: "old-contract", Side: "buy", Quantity: 2, Price: 100.25, OpenedAt: now, Session: plan.ID}},
		"future_extension": map[string]any{"value": "未知の上位項目も保存"},
	}
	trades := []journal.Trade{{ID: tradeID, At: now, Session: plan.ID, Symbol: "TOPIXmini", Product: "TOPIXmini", Owner: "extra", TradeType: "new", Side: "buy", Quantity: 2, Price: 100.25,
		ClientOrderID: signal.ID, BrokerOrderID: "broker-order-1", ExecutionID: "broker-fill-1", DryRun: true}}
	source := filepath.Join(root, "data", "dryrun", "TOPIXmini.sqlite")
	store, err := journal.Open(source, filepath.Join(root, "log", "dryrun", "TOPIXmini"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Save(state, trades); err != nil {
		t.Fatal(err)
	}
	return migrationFixture{cfg, conf, source, store, state, trades}
}

// readMigrationSnapshot は正本を読み取り、JSON値の比較用に復号します。
//
// 引数: t *testing.T は試験、path string は台帳パスです。
// 返り値: []byteは原文、map[string]json.RawMessageはsnapshot項目です。
func readMigrationSnapshot(t *testing.T, path string) ([]byte, map[string]json.RawMessage) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw []byte
	if err = db.QueryRow("SELECT state FROM snapshot WHERE id=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var value map[string]json.RawMessage
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return raw, value
}

// sameMigrationJSON は空白に依存せずJSON項目が不変か検査します。
//
// 引数: a,b json.RawMessage は前後の項目です。
// 返り値: boolは数値・配列順・キー内容が等しい場合trueです。
func sameMigrationJSON(a, b json.RawMessage) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

// TestLegacyMigrationPreservesWALAndOwnership はWALを含む複製、所有ID・未通知約定・待ち行列の不変性を確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestLegacyMigrationPreservesWALAndOwnership(t *testing.T) {
	f := legacyMigrationFixture(t)
	before, oldFields := readMigrationSnapshot(t, f.Source)
	if stat, err := os.Stat(f.Source + "-wal"); err != nil || stat.Size() == 0 {
		t.Fatalf("WALがある試験条件ではありません: %v", err)
	}
	if err := MigrateLegacy(f.Config, f.LegacyDirectory); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(f.Source), f.Config.InstanceID(), "TOPIXmini.sqlite")
	unchanged, _ := readMigrationSnapshot(t, f.Source)
	if string(unchanged) != string(before) {
		t.Fatal("移行元の正本を変更しました")
	}
	_, newFields := readMigrationSnapshot(t, target)
	for key, old := range oldFields {
		if key != "runtime_state" && !sameMigrationJSON(old, newFields[key]) {
			t.Fatalf("注文台帳の%sを変更しました", key)
		}
	}
	var oldRuntime, newRuntime map[string]json.RawMessage
	_ = json.Unmarshal(oldFields["runtime_state"], &oldRuntime)
	_ = json.Unmarshal(newFields["runtime_state"], &newRuntime)
	for _, key := range []string{"Queue", "Done", "IndexOffset", "Contract", "Plan"} {
		if !sameMigrationJSON(oldRuntime[key], newRuntime[key]) {
			t.Fatalf("戦略の%sを変更しました", key)
		}
	}
	var saved savedRuntime
	if err := json.Unmarshal(newFields["runtime_state"], &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Signature != f.Config.Signature() || saved.Namespace == "" {
		t.Fatal("移行先署名と新運転IDがありません")
	}
	if saved.LegacyDataDirectory != filepath.Dir(f.Source) || !filepath.IsAbs(saved.LegacyDataDirectory) {
		t.Fatal("旧版との排他に必要な移行元絶対パスを保存していません", saved.LegacyDataDirectory)
	}
	portfolio, err := strategy.Restore(saved.Portfolio)
	if err != nil || portfolio.Config.Namespace != saved.Namespace {
		t.Fatalf("戦略の運転ID不整合: %v", err)
	}
	manager, err := execution.Open(filepath.Join(f.Config.Path("log"), "dryrun", f.Config.InstanceID(), "TOPIXmini"), target, true, "TOPIXmini", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if len(manager.OutstandingFills()) != 1 || !manager.HasPending() || manager.Positions()[0].HoldID != "broker-fill-1" {
		t.Fatal("未通知約定・結果不明注文・所有建玉を保持していません")
	}
	if err := MigrateLegacy(f.Config, f.LegacyDirectory); err == nil {
		t.Fatal("同じ移行先へ再コピーしました")
	}
	other := f.Config
	other.Trading.InstanceID = "other-instance"
	if err := MigrateLegacy(other, f.LegacyDirectory); err == nil {
		t.Fatal("同じ旧所有建玉を別の実行IDへ複製しました")
	}
}

// TestLegacyMigrationRejectsMismatchAndLocks は実行中・署名差異・数量差異をコピー前に拒否します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestLegacyMigrationRejectsMismatchAndLocks(t *testing.T) {
	for _, kind := range []string{"旧版運転中", "新版運転中", "数量変更", "旧署名差異", "旧内部係数差異"} {
		t.Run(kind, func(t *testing.T) {
			f := legacyMigrationFixture(t)
			var lock *processLock
			var err error
			switch kind {
			case "旧版運転中":
				lock, err = acquireLock(filepath.Dir(f.Source))
			case "新版運転中":
				lock, err = acquireSharedLock(filepath.Dir(f.Source))
			case "数量変更":
				market := f.Config.Markets["TOPIX"]
				market.CoreQuantity = 4
				f.Config.Markets["TOPIX"] = market
			case "旧署名差異":
				runtime := f.State["runtime_state"].(savedRuntime)
				runtime.Signature = "違う旧設定"
				f.State["runtime_state"] = runtime
			case "旧内部係数差異":
				runtime := f.State["runtime_state"].(savedRuntime)
				portfolio, _ := strategy.Restore(runtime.Portfolio)
				portfolio.Config.Parameters.Night.StopR = 2
				runtime.Portfolio, _ = portfolio.Dump()
				f.State["runtime_state"] = runtime
			}
			if err != nil {
				t.Fatal(err)
			}
			if lock != nil {
				defer lock.Close()
			}
			if strings.Contains(kind, "差異") {
				if err := f.Store.Save(f.State, f.Trades); err != nil {
					t.Fatal(err)
				}
			}
			if err := MigrateLegacy(f.Config, f.LegacyDirectory); err == nil {
				t.Fatal("不整合または競合中に移行しました")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(f.Source), f.Config.InstanceID(), "TOPIXmini.sqlite")); !os.IsNotExist(err) {
				t.Fatal("失敗時に新台帳を作成しました")
			}
		})
	}
}
