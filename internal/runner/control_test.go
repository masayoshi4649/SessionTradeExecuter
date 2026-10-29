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
	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
)

// TestControlledOffRejectsQueuedEntry はOFF直前の未送信新規が後から発注されないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestControlledOffRejectsQueuedEntry(t *testing.T) {
	a, m, calls, now := integrationApp(t, "NK225", true)
	a.control = NewControl(true)
	signal := queueExtra(t, a, m, now)
	a.control.SetEnabled(false)
	if a.canEnter(m, now) {
		t.Fatal("OFF中に新規が許可されています")
	}
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	if len(m.Saved.Queue) != 0 || len(m.Manager.Orders()) != 0 || len(m.Manager.Positions()) != 0 {
		t.Fatal("OFF中の新規が予約・約定に残りました")
	}
	if !m.Portfolio.Orders[signal.ID].Done {
		t.Fatal("破棄した新規の戦略予約が解除されていません")
	}
	a.control.SetEnabled(true)
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	if len(m.Manager.Positions()) != 0 || calls.Load() != 0 {
		t.Fatal("再ON時に古い新規を再送しました")
	}
}

// TestControlledFastOffOnStillClearsQueuedEntry はtickより短いOFFも未送信新規へ適用することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestControlledFastOffOnStillClearsQueuedEntry(t *testing.T) {
	a, m, calls, now := integrationApp(t, "NK225", true)
	a.control = NewControl(true)
	signal := queueExtra(t, a, m, now)
	a.control.SetEnabled(false)
	a.control.SetEnabled(true)
	if !a.control.Enabled() || a.canEnter(m, now) {
		t.Fatal("再ONの指定と取消照合中の実許可が区別されていません")
	}
	release, permitted := a.control.entryPermit()
	release()
	if permitted {
		t.Fatal("停止整理前に新規送信が許可されました")
	}
	if err := a.tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(m.Saved.Queue) != 0 || len(m.Manager.Orders()) != 0 || !m.Portfolio.Orders[signal.ID].Done || calls.Load() != 0 {
		t.Fatal("短いOFFの未送信新規を破棄できませんでした")
	}
	if !a.canEnter(m, now) {
		t.Fatal("停止整理の完了後も再開を禁止しています", a.entryBlockReason(m, now))
	}
}

// TestControlledOffKeepsPositionAndAllowsClose はOFFが保有放棄や即全返済にならないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestControlledOffKeepsPositionAndAllowsClose(t *testing.T) {
	a, m, calls, now := integrationApp(t, "TOPIX", true)
	a.control = NewControl(true)
	queueExtra(t, a, m, now)
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	positions := m.Manager.Positions()
	if len(positions) == 0 {
		t.Fatal("準備用の仮想新規がありません")
	}
	portfolio := m.Portfolio
	a.control.SetEnabled(false)
	if err := a.tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(m.Manager.Positions()) != len(positions) || m.Portfolio != portfolio {
		t.Fatal("OFFで既存玉または戦略状態が失われました")
	}
	if err := a.enqueue(m, m.Portfolio.Flatten(seconds(now), "返済条件の検査"), 0, now); err != nil {
		t.Fatal(err)
	}
	if err := a.dispatch(context.Background(), m, now); err != nil {
		t.Fatal(err)
	}
	if len(m.Manager.Positions()) != 0 || calls.Load() != 0 {
		t.Fatal("OFF中の仮想返済が完了しない、または実注文を送信しました")
	}
	a.control.SetEnabled(true)
	if m.Portfolio != portfolio {
		t.Fatal("再ONで戦略を初期化しました")
	}
}

// TestControlledOffCancelsOnlyPendingNew は受付済み新規を取消要求し、照合待ちを保持することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestControlledOffCancelsOnlyPendingNew(t *testing.T) {
	a, m, _, now := integrationApp(t, "NK225", false)
	var sends, cancels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/redirect/sendorder/future":
			sends.Add(1)
			_, _ = w.Write([]byte(`{"Result":0,"OrderId":"owned-order"}`))
		case "/api/v1/redirect/cancelorder":
			cancels.Add(1)
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["OrderId"] != "owned-order" {
				t.Error("自分の未完了注文だけを指定していません", request, err)
			}
			_, _ = w.Write([]byte(`{"Result":0,"OrderId":"owned-order"}`))
		default:
			t.Error("想定外の変更API", r.URL.Path)
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
	a.control.SetEnabled(false)
	// 運転ループがOFFを読む前に再ONしても、取消要求は失われてはいけません。
	a.control.SetEnabled(true)
	if err = a.tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 1 || cancels.Load() != 1 || !m.Manager.HasPending() {
		t.Fatal("受付済み新規の取消と終了照合を分離していません", sends.Load(), cancels.Load())
	}
	if err = a.tick(context.Background(), now.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if cancels.Load() != 1 {
		t.Fatal("取消結果の照合前に取消を再送しました")
	}
	if a.entriesEnabled() {
		t.Fatal("取消受付だけで新規再開を許可しました")
	}
	view := a.view
	view.BrokerOrders = []controller.BrokerOrder{{ID: "owned-order", Symbol: m.Saved.Contract.Symbol, Side: "2", State: 5, CumQty: 0}}
	if err = a.handle(context.Background(), feedEvent{Kind: "口座", At: now, View: view}); err != nil {
		t.Fatal(err)
	}
	if err = a.tick(context.Background(), now.Add(2*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if !a.entriesEnabled() || m.Manager.HasPending() {
		t.Fatal("上流注文の終了照合後も新規を再開できません")
	}
}

// TestControlPauseAcknowledgementKeepsNewerOff は古い停止の完了で新しい停止要求を消さないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestControlPauseAcknowledgementKeepsNewerOff(t *testing.T) {
	a, _, _, _ := integrationApp(t, "TOPIX", true)
	a.control = NewControl(true)
	a.control.SetEnabled(false)
	first := a.control.pauseSequence.Load()
	a.control.SetEnabled(true)
	a.control.SetEnabled(false)
	a.control.SetEnabled(true)
	a.acknowledgePause(first)
	if a.entriesEnabled() {
		t.Fatal("古い停止完了で次のOFFの整理を省略しました")
	}
	a.acknowledgePause(a.control.pauseSequence.Load())
	if !a.entriesEnabled() {
		t.Fatal("最新の停止完了が反映されていません")
	}
}

// TestControlSerializesOffWithSubmit はOFFの完了後に新規送信を開始できないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestControlSerializesOffWithSubmit(t *testing.T) {
	c := NewControl(true)
	release, ok := c.entryPermit()
	if !ok {
		t.Fatal("初期ONが反映されていません")
	}
	done := make(chan struct{})
	go func() {
		c.SetEnabled(false)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("送信中にOFF完了を返しました")
	case <-time.After(10 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("送信終了後もOFFが完了しません")
	}
	release, ok = c.entryPermit()
	defer release()
	if ok || c.Enabled() {
		t.Fatal("OFFの完了後に新規送信が許可されています")
	}
}

// TestSnapshotUsesCurrentSessionNetFills は当場の損益・約定を軽量にコピーし二重計上しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSnapshotUsesCurrentSessionNetFills(t *testing.T) {
	a, m, _, now := integrationApp(t, "TOPIX", true)
	a.control = NewControl(false)
	fill := execution.Fill{ID: "確認済み約定", At: now, Product: "TOPIXmini", Owner: "core", TradeType: "close", Side: "sell", Quantity: 2, Price: 100, PnL: 5000, Fee: 83}
	recordSnapshotFill(m, fill)
	recordSnapshotFill(m, fill)
	var snapshots []Snapshot
	a.publish = func(s Snapshot) { snapshots = append(snapshots, s) }
	a.publishSnapshot(now, true)
	a.publishSnapshot(now.Add(500*time.Millisecond), false)
	if len(snapshots) != 1 {
		t.Fatal("状態通知の周期が守られていません")
	}
	s := snapshots[0]
	if s.RealizedYen != 4917 || s.SessionRealizedYen != 4917 || s.FeesYen != 83 || s.EntriesEnabled || len(s.RecentTrades) != 1 {
		t.Fatal("手数料後損益か新規許可の表示が不正です", s)
	}
	s.RecentTrades[0].Reason = "表示先で変更"
	if m.RecentTrades[0].Reason != "" {
		t.Fatal("画面のコピーから運転状態を変更できました")
	}
	m.Saved.Plan.ID = "次の場"
	a.publishSnapshot(now.Add(time.Second), false)
	if snapshots[1].RealizedYen != 0 || snapshots[1].SessionRealizedYen != 0 || snapshots[1].FeesYen != 0 || len(snapshots[1].RecentTrades) != 0 {
		t.Fatal("前場の損益・手数料・直近約定が新しい場へ混入しています")
	}
}
