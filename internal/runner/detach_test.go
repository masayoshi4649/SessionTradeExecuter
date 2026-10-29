package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// TestDetachGateStopsEntryAndClose は送信中の完了を待ち、管理終了後の新規と返済を禁止します。
//
// 引数: t(*testing.T)。返り値: なし。通信は発生しません。
func TestDetachGateStopsEntryAndClose(t *testing.T) {
	for _, action := range []string{"new", "close"} {
		t.Run(action, func(t *testing.T) {
			control := NewControl(true)
			release, allowed := control.tradePermit(action)
			if !allowed {
				t.Fatal("準備用の送信が許可されませんでした")
			}
			done := make(chan struct{})
			go func() {
				control.RequestDetachStop()
				close(done)
			}()
			select {
			case <-done:
				t.Fatal("送信中の要求が完了する前にOFFを確定しました")
			case <-time.After(10 * time.Millisecond):
			}
			release()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("送信完了後もOFFを確定できませんでした")
			}
			control.SetEnabled(true)
			for _, nextAction := range []string{"new", "close"} {
				release, allowed = control.tradePermit(nextAction)
				release()
				if allowed || control.Enabled() {
					t.Fatal("管理終了要求後に新規・返済送信を許可しました", nextAction)
				}
			}
		})
	}
}

// TestDetachDiscardsCloseWithoutFlattening は管理終了が既存玉を返済せず、終了時計にも優先することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。仮想約定と試験用通信だけを使用します。
func TestDetachDiscardsCloseWithoutFlattening(t *testing.T) {
	a, m, calls, now := integrationApp(t, "TOPIX", true)
	a.control = NewControl(true)
	queueExtra(t, a, m, now)
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	before := m.Manager.Quantity("extra", "buy")
	if before == 0 {
		t.Fatal("準備用の仮想建玉がありません")
	}
	if err := a.enqueue(m, m.Portfolio.Flatten(seconds(now), "OFF直前の返済判断"), 0, now); err != nil {
		t.Fatal(err)
	}
	if len(m.Saved.Queue) == 0 {
		t.Fatal("準備用の未送信返済がありません")
	}
	a.control.RequestDetachStop()
	// 通常終了・場内返済期限に重なっても、OFF後の保有玉を強制返済しません。
	a.stopping = true
	m.Saved.Plan.ForceClose = now.Add(-time.Second)
	if err := a.tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	if m.Manager.Quantity("extra", "buy") != before || len(m.Manager.Records()) != 1 || len(m.Saved.Queue) != 0 || calls.Load() != 0 {
		t.Fatal("OFF後に返済したか、旧建玉の記録を消しました")
	}
	if !a.detachStopReady() {
		t.Fatal("未確定注文がないのに旧建玉の返済を待っています")
	}
	if err := a.finishDetach(); err != nil {
		t.Fatal(err)
	}
}

// TestDetachCancellationKeepsRacingFill は取消受付だけで終了せず、競合約定を保存して管理外へ移します。
//
// 引数: t(*testing.T)。返り値: なし。HTTP通信先は試験専用サーバーです。
func TestDetachCancellationKeepsRacingFill(t *testing.T) {
	a, m, _, now := integrationApp(t, "NK225", false)
	var sends, cancels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/redirect/sendorder/future":
			sends.Add(1)
			_, _ = w.Write([]byte(`{"Result":0,"OrderId":"owned-entry"}`))
		case "/api/v1/redirect/cancelorder":
			cancels.Add(1)
			_, _ = w.Write([]byte(`{"Result":0,"OrderId":"owned-entry"}`))
		default:
			t.Error("想定外の試験通信です", r.URL.Path)
		}
	}))
	defer server.Close()
	var err error
	a.client, err = controller.New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a.client.SetDryRun(false)
	a.control = NewControl(true)
	queueExtra(t, a, m, now)
	if err = a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	a.view.BrokerOrders = []controller.BrokerOrder{{ID: "owned-entry", Symbol: m.Saved.Contract.Symbol, Side: "2", State: 4}}
	// 新規送信時間外でも、当運転の所有注文の取消は進めます。
	a.view.Session.SendUntil = now.Add(-time.Second)
	a.control.RequestDetachStop()
	if err = a.tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 1 || cancels.Load() != 1 || !m.Manager.HasPending() || a.detachStopReady() {
		t.Fatal("取消受付だけで終了したか、送信時間外の取消を止めました")
	}
	if err = a.tick(context.Background(), now.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if cancels.Load() != 1 {
		t.Fatal("取消結果の照合前に取消を再送しました")
	}
	view := a.view
	view.BrokerOrders = []controller.BrokerOrder{{ID: "owned-entry", Symbol: m.Saved.Contract.Symbol, Side: "2", State: 5, CumQty: 2,
		Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "late-owned-fill", Qty: 2, Price: 111, TransactTime: now.Format(time.RFC3339Nano)}}}}
	view.Positions = []controller.Position{{ExecutionID: "late-owned-fill", Symbol: m.Saved.Contract.Symbol, Side: "buy", Quantity: 2, AvailableQuantity: 2}}
	if err = a.handle(context.Background(), feedEvent{Kind: "口座", At: now, View: view}); err != nil {
		t.Fatal(err)
	}
	if err = a.tick(context.Background(), now.Add(2*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if !a.detachStopReady() || m.Manager.Quantity("extra", "buy") != 2 || len(m.Manager.Records()) != 1 || sends.Load() != 1 {
		t.Fatal("取消競合の約定を保存しない、または管理外の旧玉を返済しました")
	}
}

// TestDetachUnknownOrderBlocksCompletion は送信結果不明の注文を残したまま管理終了しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。HTTP通信先は試験専用サーバーです。
func TestDetachUnknownOrderBlocksCompletion(t *testing.T) {
	a, m, _, now := integrationApp(t, "NK225", false)
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		http.Error(w, "試験用の送信結果不明", http.StatusBadGateway)
	}))
	defer server.Close()
	var err error
	a.client, err = controller.New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a.client.SetDryRun(false)
	a.control = NewControl(true)
	queueExtra(t, a, m, now)
	if err = a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	a.control.RequestDetachStop()
	if err = a.tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if a.detachStopReady() || !m.Manager.HasPending() || m.Manager.PendingError() == "" || sends.Load() != 1 {
		t.Fatal("結果不明の注文を捨てたか、再送しました")
	}
}
