package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// TestSelectedCancelUnsentQueuePersists は未送信の指定新規だけを消し、返済と他の新規を保持します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。保存復元後にも古い予約を再送しません。
func TestSelectedCancelUnsentQueuePersists(t *testing.T) {
	a, m, calls, now := integrationApp(t, "NK225", true)
	signal := queueExtra(t, a, m, now)
	m.Saved.Queue = append(m.Saved.Queue, queuedSignal{Signal: strategy.Signal{ID: "other", Action: "new"}}, queuedSignal{Signal: strategy.Signal{ID: "repay", Action: "close"}})
	if err := a.cancelSelectedEntries(context.Background(), m, []string{signal.ID, "repay"}, now); err != nil {
		t.Fatal(err)
	}
	if len(m.Saved.Queue) != 2 || m.Saved.Queue[0].Signal.ID != "other" || m.Saved.Queue[1].Signal.ID != "repay" || !m.Portfolio.Orders[signal.ID].Done || calls.Load() != 0 {
		t.Fatal("未送信取消が対象外へ及びました")
	}
	restoreRuntime(t, m)
	if len(m.Saved.Queue) != 2 || !m.Portfolio.Orders[signal.ID].Done || len(m.Manager.Orders()) != 0 {
		t.Fatal("取消が保存復元できません")
	}
}

// TestSelectedCancelSentEntryWaitsForRacingFill は受付済み新規を取消し、競合約定と終了を照合してから予約を解除します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。通信はローカルの模擬controllerだけです。
func TestSelectedCancelSentEntryWaitsForRacingFill(t *testing.T) {
	a, m, _, now := integrationApp(t, "NK225", false)
	var sends, cancels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/redirect/sendorder/future":
			sends.Add(1)
		case "/api/v1/redirect/cancelorder":
			cancels.Add(1)
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["OrderId"] != "owned-selected" {
				t.Error("取消対象が異なります", request, err)
			}
		default:
			t.Error("想定外の要求", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"Result":0,"OrderId":"owned-selected"}`))
	}))
	defer server.Close()
	var err error
	a.client, err = controller.New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a.client.SetDryRun(false)
	signal := queueExtra(t, a, m, now)
	queued := m.Saved.Queue[0]
	if err = a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	// 保存直後の中断で同じIDがQueueに残っていても、送信済み新規を未送信拒否へ読み替えません。
	m.Saved.Queue = append(m.Saved.Queue, queued)
	for range 2 {
		if err = a.cancelSelectedEntries(context.Background(), m, []string{signal.ID}, now); err != nil {
			t.Fatal(err)
		}
	}
	if sends.Load() != 1 || cancels.Load() != 1 || !m.Manager.HasPending() || m.Portfolio.Orders[signal.ID].Done || len(m.Saved.Queue) != 0 {
		t.Fatal("取消受付で戦略予約を消しました")
	}
	view := a.view
	view.BrokerOrders = []controller.BrokerOrder{{ID: "owned-selected", Symbol: m.Saved.Contract.Symbol, Side: "2", State: 5, CumQty: 1, Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "racing-owned", Qty: 1, Price: 110.25, TransactTime: now.Format(time.RFC3339Nano)}}}}
	view.Positions = []controller.Position{{ExecutionID: "racing-owned", Symbol: m.Saved.Contract.Symbol, Side: "buy", Quantity: 1, AvailableQuantity: 1}}
	if _, err = m.Manager.Reconcile(view); err != nil {
		t.Fatal(err)
	}
	if err = a.applyFills(m); err != nil {
		t.Fatal(err)
	}
	if m.Manager.HasPending() || m.Manager.Quantity("extra", "buy") != 1 || !m.Saved.Done[signal.ID] || !m.Portfolio.Orders[signal.ID].Done {
		t.Fatal("競合約定又はDoneを反映できません")
	}
	restoreRuntime(t, m)
	if err = a.cancelSelectedEntries(context.Background(), m, []string{signal.ID}, now); err != nil || cancels.Load() != 1 || m.Manager.Quantity("extra", "buy") != 1 {
		t.Fatal("復元後の重複取消又は所有消失です", err)
	}
}

// TestSelectedCancelCheckpointFailureIsFatal は未送信取消の保存失敗を照合待ちへ隠しません。
//
// 引数: t(*testing.T)は試験です。返り値: なし。保存器を閉じる人工障害を使います。
func TestSelectedCancelCheckpointFailureIsFatal(t *testing.T) {
	a, m, calls, now := integrationApp(t, "NK225", true)
	signal := queueExtra(t, a, m, now)
	if err := m.Manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.cancelSelectedEntries(context.Background(), m, []string{signal.ID}, now); err == nil || calls.Load() != 0 {
		t.Fatal("保存失敗を無視して通信しました", err)
	}
}
