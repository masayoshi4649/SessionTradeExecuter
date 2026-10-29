package execution

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// seedOwnedLots は口座照合試験用に、同担当の新旧二玉を用意します。
//
// 引数: t(*testing.T)、dry(bool)は試験と仮想指定です。
// 返り値: *Manager、controller.OrdersViewは所有台帳と対応する口座です。
func seedOwnedLots(t *testing.T, dry bool) (*Manager, controller.OrdersView) {
	t.Helper()
	m, _ := setup(t, dry, "NK225mini")
	view := testView()
	for _, id := range []string{"old", "new"} {
		m.state.Holdings[id] = &Holding{HoldID: id, Owner: "episode", Product: "NK225mini", ContractSymbol: "167120019", Side: "buy", Quantity: 2, Price: 100}
		view.Positions = append(view.Positions, controller.Position{ExecutionID: id, Symbol: "167120019", Side: "buy", Quantity: 2, AvailableQuantity: 2})
	}
	return m, view
}

// TestExplicitCloseKeepsOldestAndRestores は指定順の返済が最古玉を残し、費用と内訳を再通知することを確認します。
//
// 引数: t(*testing.T)は試験です。
// 返り値: なし。実通信せず仮想注文・SQLite復元を検査します。
func TestExplicitCloseKeepsOldestAndRestores(t *testing.T) {
	m, path := setup(t, true, "NK225mini")
	api := &fakeAPI{}
	for index, id := range []string{"old", "new"} {
		intent := testIntent(id, "buy", "new", 2, float64(100+index*10))
		intent.Owner = "episode"
		fills, err := m.Submit(context.Background(), api, intent, testView())
		if err != nil || len(fills) != 1 || fills[0].Fee != 77 || fills[0].HoldID != "dry-"+id {
			t.Fatal("新規IDと手数料が不一致", fills, err)
		}
		acknowledge(t, m)
	}
	intent := testIntent("selected", "sell", "close", 3, 115)
	intent.Owner = "episode"
	intent.ClosePositions = []controller.ClosePosition{{HoldID: "dry-new", Quantity: 2}, {HoldID: "dry-old", Quantity: 1}}
	want := append([]controller.ClosePosition(nil), intent.ClosePositions...)
	fills, err := m.Submit(context.Background(), api, intent, testView())
	if err != nil || len(fills) != 1 || fills[0].Fee != 115 || fills[0].PnL != 25000 || !reflect.DeepEqual(fills[0].Closed, want) || fills[0].ClosedBasis != "仮想約定の指定順配賦" {
		t.Fatal("指定玉の返済損益・費用・内訳が不一致", fills, err)
	}
	if api.calls != 0 || len(m.Positions()) != 1 || m.Positions()[0].HoldID != "dry-old" || m.Positions()[0].Quantity != 1 || m.Positions()[0].EntryIntentID != "old" {
		t.Fatal("最古玉を残せませんでした", m.Positions())
	}
	// ----------------------------------------
	// 呼出元が返した配列を書き換えても、永続化対象の指定・実約定は変わりません。
	intent.ClosePositions[0].Quantity = 99
	fills[0].Closed[0].Quantity = 99
	records := m.Records()
	records[len(records)-1].Closed[0].HoldID = "改変"
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(filepath.Join(filepath.Dir(path), "log"), path, true, "NK225mini", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	fills = restored.OutstandingFills()
	if len(fills) != 1 || fills[0].Fee != 115 || !reflect.DeepEqual(fills[0].Closed, want) || restored.state.Orders[2].Intent.ClosePositions[0].Quantity != 2 || fills[0].ClosedBasis != "仮想約定の指定順配賦" {
		t.Fatal("返済内訳が再起動で変化しました", fills)
	}
}

// TestInvalidExplicitTargetsNeverSend は指定IDの所有・口座・予約の不一致を発注前に拒否します。
//
// 引数: t(*testing.T)は試験です。
// 返り値: なし。拒否した要求で台帳・建玉・送信回数が変わらないことを確認します。
func TestInvalidExplicitTargetsNeverSend(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manager, *Intent, *controller.OrdersView)
	}{
		{"存在しない玉", func(_ *Manager, i *Intent, _ *controller.OrdersView) { i.ClosePositions[0].HoldID = "unknown" }},
		{"他担当", func(m *Manager, _ *Intent, _ *controller.OrdersView) { m.state.Holdings["new"].Owner = "addon" }},
		{"他商品", func(m *Manager, _ *Intent, _ *controller.OrdersView) { m.state.Holdings["new"].Product = "NK225micro" }},
		{"他限月", func(m *Manager, _ *Intent, _ *controller.OrdersView) {
			m.state.Holdings["new"].ContractSymbol = "other"
		}},
		{"同じ売買側", func(m *Manager, _ *Intent, _ *controller.OrdersView) { m.state.Holdings["new"].Side = "sell" }},
		{"所有数超過", func(_ *Manager, i *Intent, _ *controller.OrdersView) {
			i.Quantity = 3
			i.ClosePositions[0].Quantity = 3
		}},
		{"合計不足", func(_ *Manager, i *Intent, _ *controller.OrdersView) { i.Quantity = 3 }},
		{"重複ID", func(_ *Manager, i *Intent, _ *controller.OrdersView) {
			i.ClosePositions = []controller.ClosePosition{{HoldID: "new", Quantity: 1}, {HoldID: "new", Quantity: 1}}
		}},
		{"返済可能数不足", func(_ *Manager, _ *Intent, v *controller.OrdersView) { v.Positions[1].AvailableQuantity = 1 }},
		{"口座残数相違", func(_ *Manager, _ *Intent, v *controller.OrdersView) { v.Positions[1].Quantity = 3 }},
		{"口座ID重複", func(_ *Manager, _ *Intent, v *controller.OrdersView) {
			v.Positions = append(v.Positions, v.Positions[1])
		}},
		{"非有限数量", func(_ *Manager, _ *Intent, v *controller.OrdersView) { v.Positions[1].AvailableQuantity = math.NaN() }},
		{"他注文拘束", func(_ *Manager, _ *Intent, v *controller.OrdersView) { v.Positions[1].HeldQuantity = 1 }},
		{"未終了予約", func(_ *Manager, _ *Intent, v *controller.OrdersView) {
			v.Reservations = []controller.Reservation{{Legs: []controller.Leg{{ClosePositions: []controller.ClosePosition{{HoldID: "new", Quantity: 1}}}}}}
		}},
		{"新規への指定", func(_ *Manager, i *Intent, _ *controller.OrdersView) { i.TradeType = "new" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m, view := seedOwnedLots(t, false)
			intent := testIntent("invalid", "sell", "close", 2, 110)
			intent.Owner = "episode"
			intent.ClosePositions = []controller.ClosePosition{{HoldID: "new", Quantity: 2}}
			test.mutate(m, &intent, &view)
			before, _ := json.Marshal(m.state)
			api := &fakeAPI{result: controller.OrderResponse{OrderID: "never"}}
			if _, err := m.Submit(context.Background(), api, intent, view); err == nil {
				t.Fatal("不正な指定を受け付けました")
			}
			after, _ := json.Marshal(m.state)
			if api.calls != 0 || string(before) != string(after) || len(m.Records()) != 0 {
				t.Fatal("拒否した要求が送信・台帳更新を起こしました")
			}
		})
	}
}

// TestExplicitTargetsReachRawRequest は順序付きHoldIDが透過注文へそのまま渡ることを確認します。
//
// 引数: t(*testing.T)は試験です。
// 返り値: なし。APIは記録だけを行うモックです。
func TestExplicitTargetsReachRawRequest(t *testing.T) {
	m, view := seedOwnedLots(t, false)
	intent := testIntent("ordered", "sell", "close", 3, 110)
	intent.Owner = "episode"
	intent.ClosePositions = []controller.ClosePosition{{HoldID: "new", Quantity: 2}, {HoldID: "old", Quantity: 1}}
	api := &fakeAPI{result: controller.OrderResponse{OrderID: "close-order"}}
	if _, err := m.Submit(context.Background(), api, intent, view); err != nil {
		t.Fatal(err)
	}
	want := []controller.BrokerClosePosition{{HoldID: "new", Qty: 2}, {HoldID: "old", Qty: 1}}
	if api.calls != 1 || !reflect.DeepEqual(api.request.ClosePositions, want) {
		t.Fatal("指定順序を変更しました", api.request)
	}
}

// TestPartialFillFeeAndActualTargets は分割約定の注文累計料と口座で減った玉を確認します。
//
// 引数: t(*testing.T)は試験です。
// 返り値: なし。五商品の注文をモック照合し、日経miniは途中再起動も行います。
func TestPartialFillFeeAndActualTargets(t *testing.T) {
	for _, test := range []struct {
		product       string
		first, second float64
	}{
		{"NK225", 275, 550}, {"NK225mini", 38, 77}, {"NK225micro", 11, 22}, {"TOPIX", 330, 660}, {"TOPIXmini", 41, 84},
	} {
		t.Run(test.product, func(t *testing.T) {
			m, path := setup(t, false, test.product)
			intent := testIntent("open", "buy", "new", 3, 100)
			intent.Owner = "addon"
			api := &fakeAPI{result: controller.OrderResponse{OrderID: "open-order"}}
			view := testView()
			if _, err := m.Submit(context.Background(), api, intent, view); err != nil {
				t.Fatal(err)
			}
			view.BrokerOrders = []controller.BrokerOrder{{ID: "open-order", Symbol: intent.ContractSymbol, Side: "2", CumQty: 1, Details: []controller.BrokerDetail{{ExecutionID: "hold1", RecType: 8, Qty: 1, Price: 100}}}}
			view.Positions = []controller.Position{{ExecutionID: "hold1", Symbol: intent.ContractSymbol, Side: "buy", Quantity: 1, AvailableQuantity: 1}}
			fills, err := m.Reconcile(view)
			if err != nil || len(fills) != 1 || fills[0].Fee != test.first || fills[0].HoldID != "hold1" {
				t.Fatal("初回分割料が不一致", fills, err)
			}
			acknowledge(t, m)
			if test.product == "NK225mini" {
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
				m, err = Open(filepath.Join(filepath.Dir(path), "log"), path, false, test.product, 1000, 10)
				if err != nil {
					t.Fatal(err)
				}
				defer m.Close()
			}
			view.BrokerOrders[0].Details = append(view.BrokerOrders[0].Details, controller.BrokerDetail{ExecutionID: "hold2", RecType: 8, Qty: 2, Price: 100})
			view.BrokerOrders[0].CumQty, view.BrokerOrders[0].Finished = 3, true
			view.Positions = append(view.Positions, controller.Position{ExecutionID: "hold2", Symbol: intent.ContractSymbol, Side: "buy", Quantity: 2, AvailableQuantity: 2})
			fills, err = m.Reconcile(view)
			if err != nil || len(fills) != 1 || fills[0].Fee != test.second || fills[0].HoldID != "hold2" {
				t.Fatal("累計差分の分割料が不一致", fills, err)
			}
			acknowledge(t, m)
			closing := testIntent("close", "sell", "close", 3, 110)
			closing.Owner = "addon"
			closing.ClosePositions = []controller.ClosePosition{{HoldID: "hold2", Quantity: 2}, {HoldID: "hold1", Quantity: 1}}
			api.result.OrderID = "close-order"
			if _, err := m.Submit(context.Background(), api, closing, view); err != nil {
				t.Fatal(err)
			}
			view.BrokerOrders = append(view.BrokerOrders, controller.BrokerOrder{ID: "close-order", Symbol: intent.ContractSymbol, Side: "1", CumQty: 1, Details: []controller.BrokerDetail{{ExecutionID: "close1", RecType: 8, Qty: 1, Price: 110}}})
			view.Positions = view.Positions[1:]
			fills, err = m.Reconcile(view)
			want := []controller.ClosePosition{{HoldID: "hold1", Quantity: 1}}
			if err != nil || len(fills) != 1 || fills[0].Fee != test.first || !reflect.DeepEqual(fills[0].Closed, want) || fills[0].ClosedBasis != "口座残数照合による配賦" {
				t.Fatal("指定順ではなく実減少した玉を記録できません", fills, err)
			}
			acknowledge(t, m)
			view.BrokerOrders[1].Details = append(view.BrokerOrders[1].Details, controller.BrokerDetail{ExecutionID: "close2", RecType: 8, Qty: 2, Price: 110})
			view.BrokerOrders[1].CumQty, view.BrokerOrders[1].Finished = 3, true
			view.Positions = nil
			fills, err = m.Reconcile(view)
			if err != nil || len(fills) != 1 || fills[0].Fee != test.second || !reflect.DeepEqual(fills[0].Closed, []controller.ClosePosition{{HoldID: "hold2", Quantity: 2}}) || len(m.Positions()) != 0 {
				t.Fatal("最後の返済と料が不一致", fills, err)
			}
			fee, pnl := 0., 0.
			for _, record := range m.Records() {
				fee += record.Fee
				pnl += record.PnL
			}
			if fee != 2*(test.first+test.second) || pnl != 30000 {
				t.Fatal("費用前PnLまたは注文合計料を変更しました", fee, pnl)
			}
		})
	}
}

// TestFIFOAndRejectedFillStayAtomic は指定なしのFIFOと、不正内訳の無変更拒否を確認します。
//
// 引数: t(*testing.T)は試験です。
// 返り値: なし。約定の一部だけを台帳へ反映しません。
func TestFIFOAndRejectedFillStayAtomic(t *testing.T) {
	m, view := seedOwnedLots(t, true)
	intent := testIntent("fifo", "sell", "close", 3, 110)
	intent.Owner = "episode"
	intent.Product = "NK225mini"
	m.state.Holdings["old"].OpenedAt = intent.At.Add(-time.Minute)
	m.state.Holdings["new"].OpenedAt = intent.At
	targets, err := m.closeTargets(intent, view)
	want := []controller.ClosePosition{{HoldID: "old", Quantity: 2}, {HoldID: "new", Quantity: 1}}
	if err != nil || !reflect.DeepEqual(targets, want) {
		t.Fatal("指定なしFIFOを変更しました", targets, err)
	}
	bad := &pending{Intent: intent, Request: controller.Request{ClosePositions: []controller.ClosePosition{{HoldID: "old", Quantity: 1}, {HoldID: "missing", Quantity: 2}}}, CloseConsumed: map[string]int{}}
	before, _ := json.Marshal(m.state)
	if err = m.applyFill(bad, "invalid-fill", 3, 110, intent.At); err == nil {
		t.Fatal("指定外の玉を含む返済を受け付けました")
	}
	after, _ := json.Marshal(m.state)
	if string(before) != string(after) || len(m.Records()) != 0 || len(bad.CloseConsumed) != 0 {
		t.Fatal("エラー前に一部だけ返済を適用しました")
	}
}
