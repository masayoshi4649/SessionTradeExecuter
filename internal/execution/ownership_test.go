package execution

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// TestOwnersCannotCloseEachOther は同方向の二担当でも返済玉を分離します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestOwnersCannotCloseEachOther(t *testing.T) {
	m, _ := setup(t, true, "TOPIXmini")
	first := testIntent("core-open", "buy", "new", 5, 4000)
	first.Evidence = json.RawMessage(`{"判断":"既存"}`)
	if _, err := m.Submit(context.Background(), nil, first, testView()); err != nil {
		t.Fatal(err)
	}
	acknowledge(t, m)
	second := testIntent("extra-open", "buy", "new", 5, 4010)
	second.Owner = "extra"
	if _, err := m.Submit(context.Background(), nil, second, testView()); err != nil {
		t.Fatal(err)
	}
	acknowledge(t, m)
	excess := testIntent("over-cap", "buy", "new", 1, 4011)
	if _, err := m.Submit(context.Background(), nil, excess, testView()); err == nil {
		t.Fatal("別担当を足さず上限を超えました")
	}
	tooMany := testIntent("bad-close", "sell", "close", 6, 4020)
	tooMany.Owner = "extra"
	if _, err := m.Submit(context.Background(), nil, tooMany, testView()); err == nil {
		t.Fatal("追加担当が既存担当の建玉を返済しました")
	}
	closeExtra := testIntent("extra-close", "sell", "close", 5, 4020)
	closeExtra.Owner = "extra"
	fills, err := m.Submit(context.Background(), nil, closeExtra, testView())
	if err != nil || len(fills) != 1 || fills[0].Owner != "extra" || fills[0].PnL != 50000 {
		t.Fatalf("担当別価格の返済が不正です: %v %+v", err, fills)
	}
	if m.Quantity("core", "buy") != 5 || m.Quantity("extra", "buy") != 0 {
		t.Fatal("担当別残数量が不正です")
	}
	positions := m.Positions()
	if len(positions) != 1 || positions[0].Owner != "core" || string(positions[0].Evidence) != `{"判断":"既存"}` {
		t.Fatal("建玉の根拠が保存されません")
	}
	positions[0].Evidence[0] = 'x'
	if m.Positions()[0].Evidence[0] != '{' {
		t.Fatal("外部の変更で内部根拠が変化しました")
	}
}

// TestPreparedRestartCannotResubmit は送信前記録後の中断で注文を再送しません。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestPreparedRestartCannotResubmit(t *testing.T) {
	m, path := setup(t, false, "TOPIXmini")
	intent := testIntent("prepared", "buy", "new", 2, 4000)
	intent.Product = "TOPIXmini"
	m.state.Orders = append(m.state.Orders, &pending{Intent: intent, Raw: true, Status: "prepared", Seen: map[string]int{}, CloseConsumed: map[string]int{}})
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(filepath.Join(filepath.Dir(path), "log"), path, false, "TOPIXmini", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	api := &fakeAPI{}
	if _, err = restarted.Submit(context.Background(), api, intent, testView()); err != nil {
		t.Fatal(err)
	}
	if api.calls != 0 || !restarted.HasPending() || restarted.Orders()[0].Status != "unknown" {
		t.Fatal("送信済みか判らない意図を再送しました")
	}
	other := testIntent("another", "buy", "new", 1, 4000)
	other.Owner = "extra"
	if _, err = restarted.Submit(context.Background(), api, other, testView()); err == nil || api.calls != 0 {
		t.Fatal("結果不明中に別担当を進めました")
	}
}

// TestForcedStopPreservesCloseUntilExpiry は停止中も返済注文を即座に取り消しません。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestForcedStopPreservesCloseUntilExpiry(t *testing.T) {
	for _, kind := range []string{"new", "close"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := setup(t, false, "TOPIXmini")
			intent := testIntent("pending-"+kind, "buy", kind, 2, 4000)
			intent.Product = "TOPIXmini"
			intent.ExpiresAt = intent.At.Add(15 * time.Second)
			m.state.Orders = append(m.state.Orders, &pending{Intent: intent, Raw: true, BrokerOrderID: "order",
				Status: "submitted", Seen: map[string]int{}, CloseConsumed: map[string]int{}})
			if err := m.save(); err != nil {
				t.Fatal(err)
			}
			api := &fakeAPI{}
			if err := m.CancelPending(context.Background(), api, intent.At.Add(250*time.Millisecond), true); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if kind == "new" {
				expected = 1
			}
			if api.cancels != expected {
				t.Fatal("停止直後の新規・返済の区別が不正です")
			}
			if err := m.CancelPending(context.Background(), api, intent.ExpiresAt, true); err != nil {
				t.Fatal(err)
			}
			if api.cancels != 1 {
				t.Fatal("期限到達の取消または重複取消防止が不正です")
			}
		})
	}
}

// TestOwnedSnapshotVerification は他媒体の玉を所有と混ぜず、既存所有玉の変更を検知します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestOwnedSnapshotVerification(t *testing.T) {
	m, _ := setup(t, false, "TOPIXmini")
	m.state.Holdings["core-hold"] = &Holding{HoldID: "core-hold", Owner: "core", ContractSymbol: "selected", Side: "buy", Quantity: 5}
	m.state.Holdings["extra-hold"] = &Holding{HoldID: "extra-hold", Owner: "extra", ContractSymbol: "selected", Side: "sell", Quantity: 2}
	base := testView()
	base.Positions = []controller.Position{
		{ExecutionID: "core-hold", Symbol: "selected", Side: "buy", Quantity: 5},
		{ExecutionID: "extra-hold", Symbol: "selected", Side: "sell", Quantity: 2},
		{ExecutionID: "external", Symbol: "selected", Side: "buy", Quantity: 3},
	}
	if err := m.VerifyOwned(base); err != nil {
		t.Fatal("外部玉を所有玉へ混ぜました:", err)
	}
	for _, mutation := range []func(*controller.OrdersView){
		func(v *controller.OrdersView) { v.Positions = v.Positions[1:] },
		func(v *controller.OrdersView) { v.Positions[0].Quantity = 4 },
		func(v *controller.OrdersView) { v.Positions[0].Quantity = 6 },
		func(v *controller.OrdersView) { v.Positions[0].Quantity = math.Inf(1) },
		func(v *controller.OrdersView) { v.Positions[0].Side = "sell" },
		func(v *controller.OrdersView) { v.Positions[0].Symbol = "other" },
		func(v *controller.OrdersView) { v.Positions = append(v.Positions, v.Positions[0]) },
	} {
		view := base
		view.Positions = append([]controller.Position(nil), base.Positions...)
		mutation(&view)
		if err := m.VerifyOwned(view); err == nil {
			t.Fatal("所有玉の差異を見落としました")
		}
		if m.Quantity("core", "buy") != 5 || m.Quantity("extra", "sell") != 2 {
			t.Fatal("照合だけで台帳数量を自動修正しました")
		}
		view.PositionsStale = true
		if err := m.VerifyOwned(view); err != nil {
			t.Fatal("古い口座を確定した差異と誤認しました")
		}
	}
	view := base
	view.Positions = nil
	m.state.Orders = append(m.state.Orders, &pending{Status: "submitted"})
	if err := m.VerifyOwned(view); err != nil {
		t.Fatal("未確定注文中に非同時な口座差を確定しました")
	}
	dry, _ := setup(t, true, "TOPIXmini")
	dry.state.Holdings["virtual"] = &Holding{HoldID: "virtual", Owner: "core", Quantity: 5}
	if err := dry.VerifyOwned(base); err != nil {
		t.Fatal("仮想玉を実口座へ照合しました")
	}
}

// TestOwnershipAndIdValidation は担当省略と同一 ID の改変再送を防ぎます。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestOwnershipAndIdValidation(t *testing.T) {
	m, _ := setup(t, true, "TOPIXmini")
	intent := testIntent("id", "buy", "new", 1, 4000)
	intent.Owner = ""
	if _, err := m.Submit(context.Background(), nil, intent, testView()); err == nil {
		t.Fatal("担当がない注文を受け付けました")
	}
	intent.Owner = "core"
	if _, err := m.Submit(context.Background(), nil, intent, testView()); err != nil {
		t.Fatal(err)
	}
	intent.Owner = "extra"
	if _, err := m.Submit(context.Background(), nil, intent, testView()); err == nil {
		t.Fatal("同一注文 ID の担当を変更できました")
	}
}

// TestTerminalPartialReportsActual は FAK 終了で残りを仮想約定せず実数量を通知します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestTerminalPartialReportsActual(t *testing.T) {
	m, _ := setup(t, false, "TOPIXmini")
	api := &fakeAPI{result: controller.OrderResponse{OrderID: "o"}}
	intent := testIntent("partial", "buy", "new", 5, 4000)
	intent.Owner = "extra"
	if _, err := m.Submit(context.Background(), api, intent, testView()); err != nil {
		t.Fatal(err)
	}
	view := testView()
	view.BrokerOrders = []controller.BrokerOrder{{ID: "o", Symbol: intent.ContractSymbol, Side: "2", State: 5, CumQty: 2,
		Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "h", Qty: 2, Price: 4001, TransactTime: "2026-10-02T10:00:01+09:00"}}}}
	view.Positions = []controller.Position{{ExecutionID: "h", Symbol: intent.ContractSymbol, Side: "buy", Quantity: 2, AvailableQuantity: 2}}
	fills, err := m.Reconcile(view)
	if err != nil || len(fills) != 1 || fills[0].Quantity != 2 || fills[0].Owner != "extra" || fills[0].At.Second() != 1 {
		t.Fatalf("部分約定が不正です: %v %+v", err, fills)
	}
	states := m.Orders()
	if len(states) != 1 || !states[0].Done || states[0].Filled != 2 || states[0].Requested != 5 || m.HasPending() {
		t.Fatal("終了した部分約定の状態が不正です")
	}
	acknowledge(t, m)
	if fills, err = m.Reconcile(view); err != nil || len(fills) != 0 || m.Quantity("extra", "buy") != 2 {
		t.Fatal("同じ照合で重複約定しました")
	}
}

// TestTerminalZeroFill は約定ゼロの終了を約定通知とは別に確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestTerminalZeroFill(t *testing.T) {
	m, _ := setup(t, false, "NK225micro")
	api := &fakeAPI{result: controller.OrderResponse{OrderID: "zero"}}
	intent := testIntent("zero-intent", "sell", "new", 5, 68000)
	if _, err := m.Submit(context.Background(), api, intent, testView()); err != nil {
		t.Fatal(err)
	}
	view := testView()
	view.BrokerOrders = []controller.BrokerOrder{{ID: "zero", Symbol: intent.ContractSymbol, Side: "1", State: 5}}
	if fills, err := m.Reconcile(view); err != nil || len(fills) != 0 || m.HasPending() {
		t.Fatalf("ゼロ約定終了の照合が不正です: %v", err)
	}
	if states := m.Orders(); len(states) != 1 || !states[0].Done || states[0].Filled != 0 {
		t.Fatal("戦略へゼロ約定終了を通知できません")
	}
}
