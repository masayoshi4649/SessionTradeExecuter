package runner

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// switchPortfolio は外部接続のない試験器へ指定した旧手法の保存状態を用意します。
//
// 引数: t(*testing.T)、a(*app)、m(*marketRuntime)、kind/method(string)。
// 返り値: []byteは台帳にも保存した切替前の状態です。
func switchPortfolio(t *testing.T, a *app, m *marketRuntime, kind, method string) []byte {
	t.Helper()
	a.cfg.Methods = config.Methods{Day: "T06", Night: "U06"}
	if method == "TG04" || method == "PG04" {
		a.cfg.Methods = config.Methods{Day: method, Night: method}
	}
	params, err := strategy.ParametersForMethod(m.Reference, kind, method)
	if err != nil {
		t.Fatal(err)
	}
	cfg, session := m.Portfolio.Config, m.Portfolio.Session
	cfg.Namespace, cfg.Parameters = "previous-namespace", params
	session.Kind = kind
	session.ID = "2026-10-22_" + kind
	m.Portfolio, err = strategy.New(cfg, session)
	if err != nil {
		t.Fatal(err)
	}
	m.Saved.Plan.ID, m.Saved.Plan.Kind = session.ID, kind
	m.Saved.Namespace, m.Saved.Signature = cfg.Namespace, a.cfg.Signature()
	m.Saved.Done = map[string]bool{"previous-completed-session": true}
	m.Saved.IndexOffset, m.Saved.LastRawHash = 123, "previous-row"
	if err := a.checkpoint(m); err != nil {
		t.Fatal(err)
	}
	return m.Manager.State()
}

// switchTrade はローカル模擬APIまたは仮想約定だけで切替検査用注文を作ります。
//
// 引数: t(*testing.T)、a(*app)、m(*marketRuntime)、at(time.Time)、id/action(string)。
// 返り値: なし。外部の注文APIには接続しません。
func switchTrade(t *testing.T, a *app, m *marketRuntime, at time.Time, id, action string) {
	t.Helper()
	side, price := "buy", 110.25
	if action == "close" {
		side, price = "sell", 111.25
	}
	intent := execution.Intent{ID: id, Owner: "core", Product: m.Saved.Contract.FutureCode,
		Session: m.Saved.Plan.ID, ContractSymbol: m.Saved.Contract.Symbol, Side: side,
		TradeType: action, Quantity: 1, Price: price, At: at, ExpiresAt: at.Add(time.Minute)}
	if _, err := m.Manager.Submit(context.Background(), a.client, intent, a.view); err != nil {
		t.Fatal(err)
	}
}

// switchEvents は試験専用SQLiteから手法切替の監査イベントを読み取ります。
//
// 引数: t(*testing.T)、a(*app)、m(*marketRuntime)。
// 返り値: []map[string]anyは保存した旧状態・新手法・名前空間の記録です。
func switchEvents(t *testing.T, a *app, m *marketRuntime) []map[string]any {
	t.Helper()
	// 切替後は契約情報を破棄しているため、選択商品でDBを特定します。
	db, err := sql.Open("sqlite", filepath.Join(a.cfg.Root, a.cfg.Markets[m.Reference].Product+".sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT payload FROM events WHERE kind = ? ORDER BY seq", "保有ゼロでの手法切替")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		var event map[string]any
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// ----------------------------------------

// TestResearchStrategySwitchPreservesLedger は空の旧手法だけを再暖機状態へ移し、過去の取引を維持します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearchStrategySwitchPreservesLedger(t *testing.T) {
	for _, tc := range []struct{ kind, old, next string }{
		{"Day", "T06", "TG04"}, {"Night", "U06", "TG04"},
		{"Day", "TG04", "PG04"}, {"Night", "TG04", "PG04"},
	} {
		t.Run(tc.kind+"_"+tc.old+"_"+tc.next, func(t *testing.T) {
			a, m, calls, now := integrationApp(t, "NK225", true)
			previous := switchPortfolio(t, a, m, tc.kind, tc.old)
			switchTrade(t, a, m, now, "historical-open", "new")
			if err := m.Manager.Checkpoint(previous); err != nil {
				t.Fatal(err)
			}
			switchTrade(t, a, m, now.Add(time.Second), "historical-close", "close")
			if err := m.Manager.Checkpoint(previous); err != nil {
				t.Fatal(err)
			}
			before := m.Manager.Records()
			if len(before) != 2 || len(m.Manager.Positions()) != 0 || len(m.Manager.OutstandingFills()) != 0 {
				t.Fatal("試験の完結済み売買が不正です")
			}
			cfg := a.cfg
			cfg.Methods = config.Methods{Day: tc.next, Night: tc.next}
			if err := resetChangedStrategy(m, cfg, previous); err != nil {
				t.Fatal(err)
			}
			a.cfg = cfg
			if len(m.Saved.Namespace) != 32 || m.Saved.Namespace == "previous-namespace" || m.Saved.Signature != cfg.Signature() {
				t.Fatalf("名前空間または設定指紋が更新されません: %+v", m.Saved)
			}
			if m.Portfolio != nil || len(m.Saved.Portfolio) != 0 || m.Saved.Plan.ID != "" || len(m.Saved.Queue) != 0 || m.Saved.Done == nil || len(m.Saved.Done) != 0 || m.Saved.IndexOffset != 0 || m.Saved.LastRawHash != "" {
				t.Fatalf("旧セッションの判断状態が再暖機へ初期化されません: %+v", m.Saved)
			}
			events := switchEvents(t, a, m)
			if len(events) != 1 || events[0]["previous_state"] != string(previous) || events[0]["new_signature"] != cfg.Signature() || events[0]["new_namespace"] != m.Saved.Namespace || events[0]["day_method"] != tc.next || events[0]["night_method"] != tc.next {
				t.Fatalf("旧状態の監査記録が不一致です: %+v", events)
			}
			if !reflect.DeepEqual(before, m.Manager.Records()) || !bytes.Equal(previous, m.Manager.State()) {
				t.Fatal("切替準備で過去の取引または旧正本が書き換わりました")
			}
			if err := a.checkpoint(m); err != nil {
				t.Fatal(err)
			}
			var saved savedRuntime
			if err := json.Unmarshal(m.Manager.State(), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Namespace != m.Saved.Namespace || saved.Signature != cfg.Signature() || (len(saved.Portfolio) != 0 && string(saved.Portfolio) != "null") || !reflect.DeepEqual(before, m.Manager.Records()) || calls.Load() != 0 {
				t.Fatal("新正本の保存または過去の取引の保全が不正です")
			}
			if saved.Methods == nil || *saved.Methods != cfg.Methods {
				t.Fatal("昼夜の明示手法が保存されません")
			}
			if err := m.Manager.Close(); err != nil {
				t.Fatal(err)
			}
			product := a.cfg.Markets[m.Reference].Product
			reopened, err := execution.Open(filepath.Join(a.cfg.Root, "log", product), filepath.Join(a.cfg.Root, product+".sqlite"), true, product, a.cfg.Products[product].PointValue, a.cfg.Markets[m.Reference].MaxPerSide)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if !reflect.DeepEqual(before, reopened.Records()) || len(reopened.Positions()) != 0 || len(reopened.OutstandingFills()) != 0 {
				t.Fatal("再起動後に過去の完結取引が失われました")
			}
		})
	}
}

// TestResearchStrategySwitchMixedMethods は当場を維持した反対場だけの変更も署名照合します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearchStrategySwitchMixedMethods(t *testing.T) {
	for _, tc := range []struct {
		kind      string
		old, next config.Methods
	}{
		{"Day", config.Methods{Day: "TG04", Night: "TG04"}, config.Methods{Day: "TG04", Night: "PG04"}},
		{"Night", config.Methods{Day: "TG04", Night: "TG04"}, config.Methods{Day: "PG04", Night: "TG04"}},
		{"Day", config.Methods{Day: "TG04", Night: "PG04"}, config.Methods{Day: "PG04", Night: "PG04"}},
		{"Night", config.Methods{Day: "TG04", Night: "PG04"}, config.Methods{Day: "TG04", Night: "TG04"}},
		{"Day", config.Methods{Day: "T06", Night: "U06"}, config.Methods{Day: "T06", Night: "TG04"}},
		{"Night", config.Methods{Day: "T06", Night: "U06"}, config.Methods{Day: "TG04", Night: "U06"}},
	} {
		t.Run(tc.kind+"_"+tc.old.Day+"_"+tc.old.Night+"_"+tc.next.Day+"_"+tc.next.Night, func(t *testing.T) {
			a, m, calls, _ := integrationApp(t, "NK225", true)
			current := tc.old.Day
			if tc.kind == "Night" {
				current = tc.old.Night
			}
			switchPortfolio(t, a, m, tc.kind, current)
			a.cfg.Methods = tc.old
			m.Saved.Signature = a.cfg.Signature()
			m.Saved.Methods = nil
			if err := a.checkpoint(m); err != nil {
				t.Fatal(err)
			}
			previous := m.Manager.State()
			cfg := a.cfg
			cfg.Methods = tc.next
			if err := resetChangedStrategy(m, cfg, previous); err != nil {
				t.Fatal(err)
			}
			a.cfg = cfg
			if err := a.checkpoint(m); err != nil {
				t.Fatal(err)
			}
			var saved savedRuntime
			if err := json.Unmarshal(m.Manager.State(), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Methods == nil || *saved.Methods != tc.next || saved.Signature != cfg.Signature() || saved.Namespace == "previous-namespace" || m.Portfolio != nil || calls.Load() != 0 {
				t.Fatal("昼夜独立の変更が安全に保存されません")
			}
			if events := switchEvents(t, a, m); len(events) != 1 || events[0]["previous_state"] != string(previous) {
				t.Fatal("旧状態が監査保存されません")
			}
		})
	}
}

// TestResearchStrategySwitchLegacyMetadata は昼夜IDがない旧形式の推測範囲を制限します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearchStrategySwitchLegacyMetadata(t *testing.T) {
	for _, tc := range []struct {
		kind, method string
		next         config.Methods
		allow        bool
	}{
		{"Day", "T06", config.Methods{Day: "TG04", Night: "TG04"}, true},
		{"Night", "U06", config.Methods{Day: "TG04", Night: "TG04"}, true},
		{"Day", "TG04", config.Methods{Day: "PG04", Night: "PG04"}, true},
		{"Night", "TG04", config.Methods{Day: "PG04", Night: "PG04"}, true},
		{"Day", "TG04", config.Methods{Day: "TG04", Night: "PG04"}, false},
		{"Night", "TG04", config.Methods{Day: "PG04", Night: "TG04"}, false},
		{"Day", "T06", config.Methods{Day: "T06", Night: "TG04"}, false},
		{"Night", "U06", config.Methods{Day: "TG04", Night: "U06"}, false},
	} {
		t.Run(tc.kind+"_"+tc.method+"_"+tc.next.Day+"_"+tc.next.Night, func(t *testing.T) {
			a, m, _, _ := integrationApp(t, "NK225", true)
			switchPortfolio(t, a, m, tc.kind, tc.method)
			m.Saved.Methods = nil
			previous, err := json.Marshal(m.Saved)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Manager.Checkpoint(previous); err != nil {
				t.Fatal(err)
			}
			cfg := a.cfg
			cfg.Methods = tc.next
			err = resetChangedStrategy(m, cfg, previous)
			if (err == nil) != tc.allow {
				t.Fatalf("旧形式の許容範囲が不一致です: allow=%v err=%v", tc.allow, err)
			}
			if !tc.allow {
				after, _ := json.Marshal(m.Saved)
				if !bytes.Equal(previous, after) || len(switchEvents(t, a, m)) != 0 {
					t.Fatal("旧形式の拒否時に状態が変わりました")
				}
			}
		})
	}
}

// TestResearchStrategySwitchRejectsOtherChanges は手法を変えても他の売買設定変更を暗黙許可しません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearchStrategySwitchRejectsOtherChanges(t *testing.T) {
	for _, name := range []string{"上限枚数", "初回枚数", "静観時刻", "dryrun", "接続先", "商品規格", "上書き条件", "手法欠落", "手法署名不一致", "手法戦略不一致"} {
		t.Run(name, func(t *testing.T) {
			a, m, _, _ := integrationApp(t, "NK225", true)
			switchPortfolio(t, a, m, "Day", "TG04")
			cfg := a.cfg
			cfg.Methods = config.Methods{Day: "TG04", Night: "PG04"}
			switch name {
			case "上限枚数", "初回枚数":
				cfg.Markets = make(map[string]config.Market, len(a.cfg.Markets))
				for k, v := range a.cfg.Markets {
					cfg.Markets[k] = v
				}
				market := cfg.Markets[m.Reference]
				if name == "上限枚数" {
					market.MaxPerSide++
				} else {
					market.CoreQuantity++
				}
				cfg.Markets[m.Reference] = market
			case "静観時刻":
				cfg.Schedule.USOpen = "09:31"
			case "dryrun":
				cfg.Trading.DryRun = false
			case "接続先":
				cfg.Controller.BaseURL = "http://127.0.0.1:1"
			case "商品規格":
				cfg.Products = make(map[string]config.Product, len(a.cfg.Products))
				for k, v := range a.cfg.Products {
					cfg.Products[k] = v
				}
				product := cfg.Products["NK225micro"]
				product.PointValue++
				cfg.Products["NK225micro"] = product
			case "上書き条件":
				cfg.Algorithm = map[string]float64{"day.risk_floor": .8}
			case "手法欠落":
				m.Saved.Methods = &config.Methods{Day: "TG04"}
			case "手法署名不一致":
				m.Saved.Methods = &config.Methods{Day: "TG04", Night: "PG04"}
			case "手法戦略不一致":
				oldConfig := m.Portfolio.Config
				oldConfig.Parameters.Method = "PG04"
				var err error
				m.Portfolio, err = strategy.New(oldConfig, m.Portfolio.Session)
				if err != nil {
					t.Fatal(err)
				}
				m.Saved.Portfolio, err = m.Portfolio.Dump()
				if err != nil {
					t.Fatal(err)
				}
			}
			previous, err := json.Marshal(m.Saved)
			if err != nil {
				t.Fatal(err)
			}
			if err = resetChangedStrategy(m, cfg, previous); err == nil {
				t.Fatal("設定の不整合を受け入れました")
			}
			after, _ := json.Marshal(m.Saved)
			if !bytes.Equal(previous, after) || len(switchEvents(t, a, m)) != 0 {
				t.Fatal("不整合の拒否時に状態が変わりました")
			}
		})
	}
}

// TestResearchStrategySwitchRejectsOutstanding は担当間の残数不一致も含め未解決状態を拒否します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearchStrategySwitchRejectsOutstanding(t *testing.T) {
	for _, name := range []string{"建玉", "未確定注文", "未反映約定", "送信待ち", "旧戦略だけ建玉", "旧戦略だけ予約", "旧版不明", "戦略破損", "手法不変"} {
		t.Run(name, func(t *testing.T) {
			a, m, calls, now := integrationApp(t, "NK225", name != "未確定注文")
			method := "U06"
			if name == "手法不変" {
				method = "TG04"
			}
			previous := switchPortfolio(t, a, m, "Night", method)
			switch name {
			case "建玉", "未確定注文", "未反映約定":
				switchTrade(t, a, m, now, "outstanding-open", "new")
				if err := m.Manager.Checkpoint(previous); err != nil {
					t.Fatal(err)
				}
				if name == "未反映約定" {
					switchTrade(t, a, m, now.Add(time.Second), "unacknowledged-close", "close")
					if len(m.Manager.Positions()) != 0 || len(m.Manager.OutstandingFills()) != 1 {
						t.Fatal("未反映約定だけ残る試験を構成できません")
					}
				}
			case "送信待ち":
				m.Saved.Queue = []queuedSignal{{Signal: strategy.Signal{ID: "queued-only"}}}
			case "旧戦略だけ建玉":
				m.Portfolio.Core.Position.Quantity, m.Portfolio.Core.Position.Side = 1, 1
				if err := a.checkpoint(m); err != nil {
					t.Fatal(err)
				}
			case "旧戦略だけ予約":
				queueExtra(t, a, m, now)
				m.Saved.Queue = nil
				if err := a.checkpoint(m); err != nil {
					t.Fatal(err)
				}
			case "旧版不明":
				m.Saved.Version = 999
			case "戦略破損":
				m.Saved.Portfolio = json.RawMessage(`{"Version":999}`)
			}
			before, err := json.Marshal(m.Saved)
			if err != nil {
				t.Fatal(err)
			}
			beforeTrades, beforeSnapshot, beforePortfolio := m.Manager.Records(), m.Manager.State(), m.Portfolio
			cfg := a.cfg
			cfg.Methods = config.Methods{Day: "TG04", Night: "TG04"}
			if name == "手法不変" {
				cfg.Schedule.DayExtra = "13:01"
			}
			if err := resetChangedStrategy(m, cfg, before); err == nil {
				t.Fatal("未解決の状態または手法以外の変更を受け入れました")
			}
			after, _ := json.Marshal(m.Saved)
			if !bytes.Equal(before, after) || beforePortfolio != m.Portfolio || !bytes.Equal(beforeSnapshot, m.Manager.State()) || !reflect.DeepEqual(beforeTrades, m.Manager.Records()) || len(switchEvents(t, a, m)) != 0 {
				t.Fatal("拒否した切替が保存状態または監査台帳を変更しました")
			}
			wantCalls := int32(0)
			if name == "未確定注文" {
				wantCalls = 1
				if !m.Manager.HasPending() {
					t.Fatal("模擬APIの未確定注文が残っていません")
				}
			}
			if calls.Load() != wantCalls {
				t.Fatal("切替によって注文を送信しました")
			}
		})
	}
}

// TestResearchQuietCoversEveryOwner は研究手法の全担当と旧手法の主担当だけを静観対象にします。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearchQuietCoversEveryOwner(t *testing.T) {
	for _, method := range []string{"U06", "TG04", "PG04"} {
		t.Run(method, func(t *testing.T) {
			a, m, _, _ := integrationApp(t, "NK225", true)
			switchPortfolio(t, a, m, "Night", method)
			for _, owner := range []string{"core", "extra", "addon", "episode"} {
				want := owner == "core" || method != "U06"
				if got := quietOwner(m, owner); got != want {
					t.Fatalf("静観範囲が不一致です: %s/%s got=%v want=%v", method, owner, got, want)
				}
			}
		})
	}
}
