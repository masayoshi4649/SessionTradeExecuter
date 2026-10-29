package runner

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/schedule"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// TestS01FaultStopsExistingQueue は実口座用の送信待ちが存在しても専用保護又は独立影の異常で送信を止めます。
//
// 引数: t(*testing.T)は人工運転の検査対象です。
// 返り値: なし。ローカルの人工送信器だけを使い、送信0件・売買記録0件・待機依頼不変を確認します。
func TestS01FaultStopsExistingQueue(t *testing.T) {
	for _, kind := range []string{"専用保護", "独立影"} {
		t.Run(kind, func(t *testing.T) {
			a, m, calls, now := integrationApp(t, "NK225", false)
			cfg, session := m.Portfolio.Config, m.Portfolio.Session
			var err error
			cfg.Parameters, err = strategy.ParametersForMethod("NK225", "Day", "S01")
			if err != nil {
				t.Fatal(err)
			}
			session.Kind = "Day"
			m.Portfolio, err = strategy.New(cfg, session)
			if err != nil {
				t.Fatal(err)
			}
			pending := queuedSignal{Signal: strategy.Signal{ID: "S01送信待ち", Owner: "episode", Action: "new", Side: 1, Quantity: 1, Time: seconds(now), SourceIndex: 1}, Created: now}
			m.Saved.Queue = []queuedSignal{pending}
			if kind == "専用保護" {
				m.Portfolio.S01.Failure = "人工検査: S01利益床が不正"
			} else {
				m.Portfolio.S01.Shadow.Rules.Bool("人工検査: 存在しない影条件", nil)
			}
			want := m.Portfolio.StrategyError()
			if want == nil {
				t.Fatal("人工異常を保持していません")
			}
			err = a.dispatch(context.Background(), m, now)
			if err == nil || !strings.Contains(err.Error(), want.Error()) {
				t.Fatalf("戦略異常の入口で停止していません: %v", err)
			}
			if calls.Load() != 0 || len(m.Manager.Records()) != 0 || m.Manager.HasPending() {
				t.Fatal("異常後に発注又は約定記録を生成しました")
			}
			if len(m.Saved.Queue) != 1 || m.Saved.Queue[0].Signal.ID != pending.Signal.ID {
				t.Fatal("送信停止時に待機依頼を消費しました")
			}
		})
	}
}

// TestS01FreshWarmupAndNightReset は起動前の影売買を作らず、夜へS01状態を持ち込まないことを確認します。
//
// 引数: t(*testing.T)は人工運転の検査対象です。
// 返り値: なし。実controllerには接続せず、送信回数0と実玉・影の初期化を検査します。
func TestS01FreshWarmupAndNightReset(t *testing.T) {
	a, m, calls, now := integrationApp(t, "NK225", true)
	a.cfg.Methods = config.Methods{Day: "NK225_S01", Night: "NK225_D07"}
	a.cfg.Windows = nil
	a.plan.ID, a.plan.Kind = now.Format("2006-01-02")+"_Day", "Day"
	a.registrations = map[string]controller.Future{m.Saved.Contract.FutureCode: m.Saved.Contract}
	m.Portfolio = nil
	m.Saved.Plan = schedule.Plan{}
	m.Saved.Namespace = "S01新規運転"
	row := strategy.Observation{Reference: "NK225", Index: 90000, Time: seconds(now.Add(-time.Minute)),
		Received: seconds(now.Add(-time.Minute)), Last: 65000, Bid: 64990, Ask: 65000, ATR: 80, QuoteOK: true}
	p := &prepared{Features: a.features, Plan: a.plan, Generation: a.generation, Cutoff: now.Add(-time.Second),
		Rows: []strategy.Observation{row}, Last: map[string]strategy.Observation{"NK225": row}}
	if err := a.activate(p); err != nil {
		t.Fatal(err)
	}
	if m.Portfolio == nil || m.Portfolio.S01 == nil || m.Portfolio.S01.Shadow == nil ||
		m.Portfolio.LastIndex != -1 || m.Portfolio.S01.Shadow.LastIndex != -1 {
		t.Fatal("起動前の履歴からS01の実玉又は影売買を作りました")
	}
	for _, position := range m.Portfolio.Status().Positions {
		if position.Quantity != 0 {
			t.Fatal("履歴暖機だけで実保有が生成されました")
		}
	}
	if len(m.Portfolio.S01.Shadow.Orders) != 0 || len(m.Saved.Queue) != 0 {
		t.Fatal("履歴暖機だけで保有又は注文予約が生成されました")
	}
	row.Index++
	row.Time, row.Received = seconds(now), seconds(now)
	if _, err := m.Portfolio.ObserveWithFreshness(row, true, true); err != nil {
		t.Fatal(err)
	}
	if m.Portfolio.S01.Shadow.LastIndex != row.Index {
		t.Fatal("新鮮な観測から独立影を開始できません")
	}
	// ----------------------------------------
	// 次の場は既存D07で新規開始し、昼の親利益床・占有影・予約を破棄します。
	old := m.Portfolio
	next := nextSessionFixture(t, a, m, "Night")
	if err := a.activate(next); err != nil {
		t.Fatal(err)
	}
	if m.Portfolio == old || m.Portfolio.S01 != nil || m.Portfolio.Config.Parameters.Method != "D07" ||
		m.Portfolio.LastIndex != -1 || len(m.Saved.Queue) != 0 {
		t.Fatal("昼のS01状態が夜のD07へ残りました")
	}
	if calls.Load() != 0 || len(m.Manager.Records()) != 0 {
		t.Fatal("起動又は場の切替だけで発注しました")
	}
}

// TestS01MethodSlotValidation は昼だけの選択と時間帯で共有する指標の条件を確認します。
//
// 引数: t(*testing.T)は設定検査対象です。
// 返り値: なし。夜・TOPIXへの誤適用を接続前に拒否します。
func TestS01MethodSlotValidation(t *testing.T) {
	cfg := config.Defaults()
	cfg.Methods = config.Methods{Day: "NK225_S01", Night: "NK225_D07"}
	day, err := methodParametersFor(cfg, "NK225", "Day", "NK225_S01")
	if err != nil {
		t.Fatal(err)
	}
	base, err := methodParametersFor(cfg, "NK225", "Day", "NK225_D07")
	if err != nil || validateSharedFeatureParameters(base, day) != nil {
		t.Fatal("S01への切替で共通指標が変わりました", err)
	}
	if _, err := methodParametersFor(cfg, "NK225", "Night", "NK225_S01"); err == nil {
		t.Fatal("夜間S01を許可しました")
	}
	if _, err := methodParametersFor(cfg, "TOPIX", "Day", "NK225_S01"); err == nil {
		t.Fatal("TOPIXへS01を適用しました")
	}
}
