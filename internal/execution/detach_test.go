package execution

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// TestDetachCancelsEntriesClosesAndReservationsWithoutLiquidating は新規・返済の未約定だけを止め、所有玉をそのまま残します。
//
// 引数: t(*testing.T)。返り値: なし。取消先はモックで、実口座には接続しません。
func TestDetachCancelsEntriesClosesAndReservationsWithoutLiquidating(t *testing.T) {
	m, _ := setup(t, false, "NK225mini")
	entry := testIntent("owned-new", "sell", "new", 2, 65000)
	closeIntent := testIntent("owned-close", "sell", "close", 1, 65000)
	reserved := testIntent("owned-reserve", "buy", "new", 1, 65000)
	m.state.Holdings["own-lot"] = &Holding{HoldID: "own-lot", Owner: "core", Side: "buy", Quantity: 1, Price: 64000, ContractSymbol: entry.ContractSymbol}
	for _, item := range []struct {
		intent Intent
		id     string
		raw    bool
	}{{entry, "broker-new", true}, {closeIntent, "broker-close", true}, {reserved, "", false}} {
		m.state.Orders = append(m.state.Orders, &pending{Intent: item.intent, BrokerOrderID: item.id, Raw: item.raw,
			InstanceID: "instance1", Status: "accepted", Seen: map[string]int{}, CloseConsumed: map[string]int{}})
	}
	view := testView()
	view.BrokerOrders = []controller.BrokerOrder{
		{ID: "broker-new", Symbol: entry.ContractSymbol, Side: "1", State: 4},
		{ID: "broker-close", Symbol: entry.ContractSymbol, Side: "1", State: 4},
		{ID: "manual-order", Symbol: entry.ContractSymbol, Side: "1", State: 4},
	}
	view.Reservations = []controller.Reservation{
		{ID: "owned-reservation", ClientOrderID: reserved.ID, Symbol: reserved.ContractSymbol},
		{ID: "manual-reservation", ClientOrderID: "manual", Symbol: reserved.ContractSymbol},
	}
	before := m.Positions()
	api := &newRunAPI{}
	for range 2 {
		if err := m.CancelAllOwnedPending(context.Background(), api, view, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(api.cancelled, []string{"broker-new", "broker-close"}) || !reflect.DeepEqual(api.reservations, []string{"owned-reservation/instance1"}) {
		t.Fatal("所有範囲外を取り消したか、返済注文の取消漏れ・再送があります", api)
	}
	if !reflect.DeepEqual(before, m.Positions()) || len(m.Records()) != 0 || !m.HasPending() {
		t.Fatal("未約定の取消受付を建玉返済・約定又は終了と誤認しました")
	}
}

// TestDetachUnknownCancellationDoesNotResend は取消結果不明を完了扱いせず、二重送信しないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestDetachUnknownCancellationDoesNotResend(t *testing.T) {
	m, _ := setup(t, false, "NK225mini")
	intent := testIntent("owned", "buy", "new", 1, 65000)
	m.state.Orders = append(m.state.Orders, &pending{Intent: intent, BrokerOrderID: "owned-broker", Raw: true, Status: "accepted"})
	view := testView()
	view.BrokerOrders = []controller.BrokerOrder{{ID: "owned-broker", Symbol: intent.ContractSymbol, Side: "2", State: 4}}
	api := &newRunAPI{cancelError: errors.New("応答切断")}
	if err := m.CancelAllOwnedPending(context.Background(), api, view, time.Now()); err == nil {
		t.Fatal("取消結果不明を成功にしました")
	}
	_ = m.CancelAllOwnedPending(context.Background(), api, view, time.Now())
	if len(api.cancelled) != 1 || !m.HasPending() || m.state.Orders[0].CancelState != "unknown" {
		t.Fatal("取消を再送したか、未確定注文を消しました")
	}
}

// TestDetachDryPreparedDoesNotFill は未実行の仮想注文を約定させず、旧仮想玉を維持します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestDetachDryPreparedDoesNotFill(t *testing.T) {
	m, _ := setup(t, true, "NK225mini")
	m.state.Holdings["dry-lot"] = &Holding{HoldID: "dry-lot", Owner: "core", Side: "buy", Quantity: 2, Price: 64000}
	m.state.Orders = append(m.state.Orders, &pending{Intent: testIntent("dry-close", "sell", "close", 2, 65000), Status: "prepared"})
	if err := m.CancelAllOwnedPending(context.Background(), nil, controller.OrdersView{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if m.HasPending() || len(m.Positions()) != 1 || m.Positions()[0].Quantity != 2 || len(m.Records()) != 0 {
		t.Fatal("仮想の未実行返済を約定させました")
	}
}
