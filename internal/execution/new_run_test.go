package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

type newRunAPI struct {
	view         func(int) controller.OrdersView
	queries      int
	cancelled    []string
	reservations []string
	cancelError  error
}

// Orders は実APIへ接続せず、照会順に応答を返します。
//
// 引数: context.Context/stringは試験では未使用。返り値: OrdersView/errorです。
func (a *newRunAPI) Orders(context.Context, string) (controller.OrdersView, error) {
	a.queries++
	return a.view(a.queries), nil
}

// CancelFuture は指定された注文番号だけを記録します。
//
// 引数: context.Context、id(string)。返り値: OrderResponse/errorです。
func (a *newRunAPI) CancelFuture(_ context.Context, id string) (controller.OrderResponse, error) {
	a.cancelled = append(a.cancelled, id)
	return controller.OrderResponse{}, a.cancelError
}

// CancelReservation は指定された予約番号とcontroller世代を記録します。
//
// 引数: context.Context、id/instance(string)。返り値: errorです。
func (a *newRunAPI) CancelReservation(_ context.Context, id, instance string) error {
	a.reservations = append(a.reservations, id+"/"+instance)
	return a.cancelError
}

// newRunRuntime は前回の建玉を含まない新戦略識別子を作ります。
//
// 引数: id(string)。返り値: json.RawMessageです。
func newRunRuntime(id string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"namespace": id, "version": 1})
	return raw
}

// TestNewRunDryDropsOldOwnershipKeepsTrades は上限変更・未反映約定・旧仮想玉を過去記録として保存します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestNewRunDryDropsOldOwnershipKeepsTrades(t *testing.T) {
	m, path := setup(t, true, "NK225mini")
	if _, err := m.Submit(context.Background(), nil, testIntent("old-open", "buy", "new", 8, 65000), testView()); err != nil {
		t.Fatal(err)
	}
	before := m.Records()
	beforeJSON, _ := json.Marshal(before)
	// 旧戦略の署名やjson構造は復元に使わず、旧版移行元の排他情報だけ保持する。
	m.state.RuntimeState = json.RawMessage(`{"namespace":"old","Signature":"obsolete","legacy_data_directory":"S:/old","Portfolio":{"unknown_version":10}}`)
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenNewRun(context.Background(), nil, filepath.Join(filepath.Dir(path), "log"), path, true, "NK225mini", 100, 2, "new-run", newRunRuntime("new-run"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	afterJSON, _ := json.Marshal(fresh.Records())
	if len(fresh.Positions()) != 0 || fresh.HasPending() || len(fresh.OutstandingFills()) != 0 || fresh.state.MaxPosition != 2 || fresh.state.Multiplier != 100 || string(beforeJSON) != string(afterJSON) {
		t.Fatal("旧玉を引継いだか、旧約定を変更したか、新設定を採用しませんでした")
	}
	if !strings.Contains(string(fresh.State()), "S:/old") || strings.Contains(string(fresh.State()), "obsolete") {
		t.Fatal("新運転への持越し範囲が不正です", string(fresh.State()))
	}
	if _, err := fresh.Submit(context.Background(), nil, testIntent("new-run-open", "sell", "new", 2, 65100), testView()); err != nil {
		t.Fatal("旧8枚を上限へ混ぜて新2枚を拒否しました", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw []byte
	if err = db.QueryRow("SELECT payload FROM events WHERE kind='起動時状態切替' ORDER BY seq DESC LIMIT 1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "old-open") || !strings.Contains(string(raw), "old") {
		t.Fatal("旧所有状態が監査保存されていません", string(raw))
	}
}

// TestNewRunDryDoesNotExecutePrepared は旧仮想予約を約定させず捨て、確定記録を捏造しません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestNewRunDryDoesNotExecutePrepared(t *testing.T) {
	m, path := setup(t, true, "NK225mini")
	m.state.Orders = append(m.state.Orders, &pending{Intent: testIntent("old-prepared", "buy", "new", 5, 65000), Status: "prepared"})
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	fresh, err := OpenNewRun(context.Background(), nil, filepath.Join(filepath.Dir(path), "log"), path, true, "NK225mini", 100, 10, "next", newRunRuntime("next"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if len(fresh.Records()) != 0 || len(fresh.Positions()) != 0 || fresh.HasPending() {
		t.Fatal("旧仮想予約を約定・持越ししました")
	}
}

// TestNewRunLiveCancelsOwnedOrderAndHandsFilledLotToManual は取消中の約定を保存し、旧玉を新戦略へ引継ぎません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestNewRunLiveCancelsOwnedOrderAndHandsFilledLotToManual(t *testing.T) {
	m, path := setup(t, false, "NK225mini")
	sender := &fakeAPI{result: controller.OrderResponse{OrderID: "owned-order"}}
	intent := testIntent("old-entry", "buy", "new", 2, 65000)
	if _, err := m.Submit(context.Background(), sender, intent, testView()); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	api := &newRunAPI{view: func(n int) controller.OrdersView {
		view := testView()
		owned := controller.BrokerOrder{ID: "owned-order", Symbol: intent.ContractSymbol, Side: "2", State: 4, OrderQty: 2}
		if n >= 2 {
			owned.State, owned.Finished, owned.CumQty = 5, true, 1
			owned.Details = []controller.BrokerDetail{{RecType: 8, ExecutionID: "old-partial", Qty: 1, Price: 65005, TransactTime: intent.At.Format(time.RFC3339)}}
			view.Positions = []controller.Position{{ExecutionID: "old-partial", Symbol: intent.ContractSymbol, Side: "buy", Quantity: 1, AvailableQuantity: 1}}
		}
		view.BrokerOrders = []controller.BrokerOrder{owned, {ID: "foreign-order", Symbol: intent.ContractSymbol, Side: "2", State: 4, OrderQty: 20}}
		return view
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	fresh, err := OpenNewRun(ctx, api, filepath.Join(filepath.Dir(path), "log"), path, false, "NK225mini", 100, 10, "fresh-live", newRunRuntime("fresh-live"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if !reflect.DeepEqual(api.cancelled, []string{"owned-order"}) || len(api.reservations) != 0 || len(fresh.Positions()) != 0 || fresh.HasPending() || len(fresh.OutstandingFills()) != 0 {
		t.Fatal("所有注文以外の取消、旧玉の持越し、又は未終了の取りこぼしです", api.cancelled)
	}
	if len(fresh.Records()) != 1 || fresh.Records()[0].ExecutionID != "old-partial" {
		t.Fatal("取消競合の実約定記録を失いました")
	}
	// 手動1枚＋新システム10枚＝口座11枚を認める。同じ旧玉をシステムが返済しない。
	view := api.view(2)
	newSender := &fakeAPI{result: controller.OrderResponse{OrderID: "fresh-order"}}
	if _, err = fresh.Submit(context.Background(), newSender, testIntent("fresh-live-entry", "buy", "new", 10, 65100), view); err != nil {
		t.Fatal("旧手動1枚が新上限10枚を減らしました", err)
	}
	view.BrokerOrders = append(view.BrokerOrders, controller.BrokerOrder{ID: "fresh-order", Symbol: intent.ContractSymbol, Side: "2", State: 5, Finished: true, CumQty: 10,
		Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "fresh-ten", Qty: 10, Price: 65100, TransactTime: intent.At.Format(time.RFC3339)}}})
	view.Positions = append(view.Positions, controller.Position{ExecutionID: "fresh-ten", Symbol: intent.ContractSymbol, Side: "buy", Quantity: 10, AvailableQuantity: 10})
	if _, err = fresh.Reconcile(view); err != nil {
		t.Fatal(err)
	}
	acknowledge(t, fresh)
	if fresh.Quantity("core", "buy") != 10 || len(view.Positions) != 2 || newSender.calls != 1 {
		t.Fatal("口座11枚と当起動10枚を分離できません")
	}
	if _, err = fresh.Submit(context.Background(), newSender, testIntent("fresh-over", "buy", "new", 1, 65100), view); err == nil {
		t.Fatal("新運転の上限10枚を超えました")
	}
	wrongClose := testIntent("wrong-close", "sell", "close", 1, 65200)
	wrongClose.ClosePositions = []controller.ClosePosition{{HoldID: "old-partial", Quantity: 1}}
	if _, err = fresh.Submit(context.Background(), newSender, wrongClose, view); err == nil || newSender.calls != 1 {
		t.Fatal("旧手動玉を新運転から返済しました")
	}
}

// TestNewRunUnknownOrUnconfirmedDoesNotDiscard は不明注文を捨てず、取消受付だけで新運転へ移りません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestNewRunUnknownOrUnconfirmedDoesNotDiscard(t *testing.T) {
	for _, mode := range []string{"unknown", "cancel-error", "accepted-only"} {
		t.Run(mode, func(t *testing.T) {
			m, path := setup(t, false, "NK225mini")
			intent := testIntent("unresolved", "buy", "new", 2, 65000)
			p := &pending{Intent: intent, Raw: true, Status: "unknown", InstanceID: "instance1", Seen: map[string]int{}, CloseConsumed: map[string]int{}}
			if mode != "unknown" {
				p.BrokerOrderID = "owned-pending"
			}
			m.state.Orders = append(m.state.Orders, p)
			if err := m.save(); err != nil {
				t.Fatal(err)
			}
			_ = m.Close()
			api := &newRunAPI{view: func(int) controller.OrdersView {
				v := testView()
				v.BrokerOrders = []controller.BrokerOrder{{ID: "owned-pending", Symbol: intent.ContractSymbol, Side: "2", State: 4, OrderQty: 2}}
				return v
			}}
			if mode == "cancel-error" {
				api.cancelError = errors.New("応答不明")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			fresh, err := OpenNewRun(ctx, api, filepath.Join(filepath.Dir(path), "log"), path, false, "NK225mini", 100, 3, "rejected-new", newRunRuntime("rejected-new"))
			if err == nil || fresh != nil {
				t.Fatal("終了未確認の旧注文を忘れて新規開始しました")
			}
			if (mode == "unknown" && len(api.cancelled) != 0) || len(api.cancelled) > 1 {
				t.Fatal("不明注文又は重複取消を送信しました")
			}
			restored, err := Open(filepath.Join(filepath.Dir(path), "log"), path, false, "NK225mini", 1000, 10)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if !restored.HasPending() || restored.state.MaxPosition != 10 || len(restored.Records()) != 0 {
				t.Fatal("旧未確定状態を失ったか、不明約定を捏造しました")
			}
		})
	}
}

// TestNewRunRejectsHistoricalExecutionReuse は別起動で既記録の約定IDを再び所有しません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestNewRunRejectsHistoricalExecutionReuse(t *testing.T) {
	m, path := setup(t, true, "NK225mini")
	if _, err := m.Submit(context.Background(), nil, testIntent("old", "buy", "new", 1, 65000), testView()); err != nil {
		t.Fatal(err)
	}
	oldID := m.Records()[0].ExecutionID
	_ = m.Close()
	fresh, err := OpenNewRun(context.Background(), nil, filepath.Join(filepath.Dir(path), "log"), path, true, "NK225mini", 100, 10, "new", newRunRuntime("new"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	p := &pending{Intent: testIntent("different-order", "buy", "new", 1, 65000)}
	if err = fresh.applyFill(p, oldID, 1, 65000, p.Intent.At); err == nil || len(fresh.Positions()) != 0 || len(fresh.Records()) != 1 {
		t.Fatal("過去約定を別起動の新規として重複計上しました")
	}
}

// TestNewRunCancelsOnlyOwnedReservation は同じcontroller世代・意図IDの予約だけを止め、終了まで待ちます。
//
// 引数: t(*testing.T)。返り値: なし。
func TestNewRunCancelsOnlyOwnedReservation(t *testing.T) {
	m, path := setup(t, false, "NK225mini")
	intent := testIntent("owned-intent", "sell", "new", 2, 65000)
	m.state.Orders = append(m.state.Orders, &pending{Intent: intent, InstanceID: "instance1", Status: "accepted", Seen: map[string]int{}, CloseConsumed: map[string]int{}})
	if err := m.save(); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()
	api := &newRunAPI{view: func(n int) controller.OrdersView {
		view := testView()
		view.Reservations = []controller.Reservation{
			{ID: "owned-reservation", ClientOrderID: intent.ID, Symbol: intent.ContractSymbol, Side: intent.Side, Quantity: 2, Finished: n >= 2},
			{ID: "foreign-reservation", ClientOrderID: "other", Symbol: intent.ContractSymbol, Side: intent.Side, Quantity: 10},
		}
		return view
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	fresh, err := OpenNewRun(ctx, api, filepath.Join(filepath.Dir(path), "log"), path, false, "NK225mini", 100, 10, "new", newRunRuntime("new"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if !reflect.DeepEqual(api.reservations, []string{"owned-reservation/instance1"}) || len(api.cancelled) != 0 || fresh.HasPending() || len(fresh.Records()) != 0 || api.queries < 2 {
		t.Fatal("予約の所有範囲・終了確認・約定ゼロが不正です", api)
	}
}
