package execution

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

type selectedCancelAPI struct {
	ids    []string
	err    error
	before func()
}

// CancelFuture は指定番号と保存済み状態を検査する模擬取消です。
//
// 引数: context.Contextは未使用、id(string)は注文番号です。返り値: 応答とerrorは人工結果です。
func (a *selectedCancelAPI) CancelFuture(_ context.Context, id string) (controller.OrderResponse, error) {
	if a.before != nil {
		a.before()
	}
	a.ids = append(a.ids, id)
	return controller.OrderResponse{}, a.err
}

// selectedPending は実通信を伴わない保存注文の試験値を作ります。
//
// 引数: id/kind(string)は意図と区分です。返り値: *pendingは既知注文番号の受付済み注文です。
func selectedPending(id, kind string) *pending {
	return &pending{Intent: testIntent(id, "buy", kind, 2, 4000), Raw: true, BrokerOrderID: "broker-" + id, Status: "accepted", Seen: map[string]int{}, CloseConsumed: map[string]int{}}
}

// TestSelectedCancelOnlyNamedEntry は対象以外の新規・返済・外部番号を触らず、送信前保存を確認します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。
func TestSelectedCancelOnlyNamedEntry(t *testing.T) {
	m, _ := setup(t, false, "TOPIXmini")
	m.state.Orders = []*pending{selectedPending("target", "new"), selectedPending("other", "new"), selectedPending("repay", "close")}
	api := &selectedCancelAPI{before: func() {
		var saved savedState
		if _, err := m.store.Load(&saved); err != nil || !saved.Orders[0].CancelRequested || saved.Orders[0].CancelState != "prepared" {
			t.Fatal("通信前に取消を保存していません", err)
		}
	}}
	ids := []string{"target", "target", "repay", "external"}
	for range 2 {
		if err := m.CancelSelectedEntries(context.Background(), api, ids, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(api.ids, []string{"broker-target"}) || m.state.Orders[1].CancelRequested || m.state.Orders[2].CancelRequested || !m.HasPending() {
		t.Fatal("対象外取消又は受付の完了扱いです", api.ids)
	}
}

// TestSelectedCancelUnknownAndReopenNeverResend は結果不明を復元後も再送しません。
//
// 引数: t(*testing.T)は試験です。返り値: なし。実通信は模擬関数だけです。
func TestSelectedCancelUnknownAndReopenNeverResend(t *testing.T) {
	m, path := setup(t, false, "TOPIXmini")
	m.state.Orders = []*pending{selectedPending("target", "new")}
	api := &selectedCancelAPI{err: errors.New("応答を受信できません")}
	err := m.CancelSelectedEntries(context.Background(), api, []string{"target"}, time.Now())
	var awaiting *SelectedCancellationPending
	if !errors.As(err, &awaiting) || m.state.Orders[0].CancelState != "unknown" {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(filepath.Join(filepath.Dir(path), "log"), path, false, "TOPIXmini", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err = restored.CancelSelectedEntries(context.Background(), api, []string{"target"}, time.Now()); err != nil || len(api.ids) != 1 || !restored.HasPending() {
		t.Fatal("結果不明を再送又は完了扱いしました", err)
	}
}

// TestSelectedCancelKeepsRacingPartialFill は取消中の一部約定をReconcileからそのまま所有します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。取消受付から所有量を推測しません。
func TestSelectedCancelKeepsRacingPartialFill(t *testing.T) {
	m, _ := setup(t, false, "TOPIXmini")
	m.state.Orders = []*pending{selectedPending("target", "new")}
	api := &selectedCancelAPI{}
	if err := m.CancelSelectedEntries(context.Background(), api, []string{"target"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	view := testView()
	view.BrokerOrders = []controller.BrokerOrder{{ID: "broker-target", Symbol: "167120019", Side: "2", State: 5, CumQty: 1, Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "late", Qty: 1, Price: 4001}}}}
	view.Positions = []controller.Position{{ExecutionID: "late", Symbol: "167120019", Side: "buy", Quantity: 1}}
	fills, err := m.Reconcile(view)
	if err != nil || len(fills) != 1 || fills[0].Quantity != 1 || m.Quantity("core", "buy") != 1 || m.HasPending() {
		t.Fatal("取消競合の約定が欠落しました", fills, err)
	}
}

// TestSelectedCancelDryRunAndMissingID は仮想未実行の終了と注文番号不明の照合待ちを区別します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。
func TestSelectedCancelDryRunAndMissingID(t *testing.T) {
	m, _ := setup(t, true, "TOPIXmini")
	m.state.Orders = []*pending{selectedPending("target", "new"), selectedPending("repay", "close")}
	if err := m.CancelSelectedEntries(context.Background(), nil, []string{"target", "repay"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if m.state.Orders[0].Status != "done" || m.state.Orders[1].Status != "accepted" || len(m.Records()) != 0 {
		t.Fatal("仮想取消で約定又は返済を作りました")
	}
	live, _ := setup(t, false, "TOPIXmini")
	p := selectedPending("unknown", "new")
	p.BrokerOrderID = ""
	live.state.Orders = []*pending{p}
	api := &selectedCancelAPI{}
	var awaiting *SelectedCancellationPending
	if err := live.CancelSelectedEntries(context.Background(), api, []string{"unknown"}, time.Now()); !errors.As(err, &awaiting) || len(api.ids) != 0 || !live.HasPending() {
		t.Fatal("不明注文番号で取消しました", err)
	}
}

// TestSelectedCancelSaveFailureStopsNetwork は取消要求の保存失敗時に通信しません。
//
// 引数: t(*testing.T)は試験です。返り値: なし。失敗を照合待ちに読み替えません。
func TestSelectedCancelSaveFailureStopsNetwork(t *testing.T) {
	m, _ := setup(t, false, "TOPIXmini")
	m.state.Orders = []*pending{selectedPending("target", "new")}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	api := &selectedCancelAPI{}
	err := m.CancelSelectedEntries(context.Background(), api, []string{"target"}, time.Now())
	var awaiting *SelectedCancellationPending
	if err == nil || errors.As(err, &awaiting) || len(api.ids) != 0 {
		t.Fatal("保存失敗後に取消しました", err, api.ids)
	}
}
