package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/schedule"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// slotFixture は実通信しない統合運転へ、現在時刻に境界を持つ二枠を設定します。
//
// 引数: t(*testing.T)、dry(bool)。返り値: *app、*marketRuntime、time.Time は運転器・担当・境界です。
func slotFixture(t *testing.T, dry bool) (*app, *marketRuntime, time.Time) {
	t.Helper()
	a, m, _, boundary := integrationApp(t, "TOPIX", dry)
	a.cfg.Windows = []config.MethodWindow{
		{ID: "前半", Session: "Night", Start: "SESSION_START", End: "US_OPEN+03:00", Method: "T04"},
		{ID: "後半", Session: "Night", Start: "US_OPEN+03:00", End: "SESSION_END", Method: "T04"},
	}
	m.Saved.Namespace = "人工運転"
	slot, err := schedule.ResolveSlot(m.Saved.Plan, a.cfg, m.Reference, boundary.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	m.Saved.ActiveSlot = &slot
	return a, m, boundary
}

// TestSlotLegacyKeepsPortfolio は時間枠が未指定なら既存経路を一切初期化しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSlotLegacyKeepsPortfolio(t *testing.T) {
	a, m, _, now := integrationApp(t, "TOPIX", true)
	old, features := m.Portfolio, a.features
	ready, err := a.updateMethodSlot(context.Background(), m, now)
	if err != nil || !ready || m.Portfolio != old || a.features != features || m.Saved.ActiveSlot != nil {
		t.Fatal("旧設定の運転状態が変更されました", err)
	}
}

// TestSlotBoundaryRejectsUnsentAndResetsSameMethod は同手法の別枠でも未送信を破棄し新規状態で始めます。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSlotBoundaryRejectsUnsentAndResetsSameMethod(t *testing.T) {
	a, m, boundary := slotFixture(t, true)
	signal := queueExtra(t, a, m, boundary.Add(-time.Second))
	old, features := m.Portfolio, a.features
	m.Saved.LastRawHash = "旧枠の原文"
	if a.slotBlockReason(m, boundary) == "" {
		t.Fatal("tick前の境界でも新規が許可されました")
	}
	ready, err := a.updateMethodSlot(context.Background(), m, boundary)
	if err != nil || !ready {
		t.Fatal(err)
	}
	if len(m.Manager.Records()) != 0 || len(m.Saved.Queue) != 0 || len(old.Status().Pending) != 0 {
		t.Fatal("未送信の旧新規が残りました", signal)
	}
	if m.Portfolio == old || a.features != features || m.Saved.ActiveSlot.ID != "後半" || m.Portfolio.Config.Namespace != "人工運転-slot-1" {
		t.Fatal("指標を継続した同手法の新規状態切替になっていません")
	}
	if m.Portfolio.LastIndex != -1 || len(m.Portfolio.Executions) != 0 || m.Saved.LastRawHash != "" {
		t.Fatal("新枠へ過去の影売買を持ち込みました")
	}
}

// TestSlotBoundaryClosesOwnedBeforeSwitch は仮想玉を境界で返済し、台帳完了後だけ次枠へ移行します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSlotBoundaryClosesOwnedBeforeSwitch(t *testing.T) {
	a, m, boundary := slotFixture(t, true)
	queueExtra(t, a, m, boundary.Add(-time.Second))
	if err := a.dispatch(context.Background(), m, boundary.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(m.Manager.Positions()) == 0 {
		t.Fatal("人工の保有玉がありません")
	}
	ready, err := a.updateMethodSlot(context.Background(), m, boundary)
	if err != nil || !ready || len(m.Manager.Positions()) != 0 || m.Manager.HasPending() {
		t.Fatal("返済後の切替に失敗しました", err)
	}
	records := m.Manager.Records()
	if len(records) != 2 || records[1].TradeType != "close" || !strings.Contains(records[1].Reason, "時間帯") {
		t.Fatal("境界返済の記録が不正です", records)
	}
}

// TestSlotGapWaitsAndFreshNamespace は無枠静観から復帰するときも新しい注文ID空間を使います。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSlotGapWaitsAndFreshNamespace(t *testing.T) {
	a, m, boundary := slotFixture(t, true)
	a.cfg.Windows[1].Start = "US_OPEN+03:01"
	features := a.features
	ready, err := a.updateMethodSlot(context.Background(), m, boundary)
	if err != nil || ready || m.Saved.ActiveSlot.Enabled || m.Saved.SlotSwitch != nil {
		t.Fatal("無枠時間が静観になっていません", err)
	}
	firstNamespace := m.Portfolio.Config.Namespace
	ready, err = a.updateMethodSlot(context.Background(), m, boundary.Add(time.Minute))
	if err != nil || !ready || m.Saved.ActiveSlot.ID != "後半" || firstNamespace == m.Portfolio.Config.Namespace || a.features != features {
		t.Fatal("静観後の新規状態切替が不正です", err)
	}
}

// TestSlotCancellationReconcilesPartialFillAndPreservesExternal は取消競合約定だけを返済し外部玉を触りません。
//
// 引数: t(*testing.T)。返り値: なし。全HTTP通信は試験サーバーだけに送信します。
func TestSlotCancellationReconcilesPartialFillAndPreservesExternal(t *testing.T) {
	a, m, boundary := slotFixture(t, false)
	var sends, cancels atomic.Int32
	var closeRequest controller.BrokerRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/redirect/sendorder/future":
			count := sends.Add(1)
			if count == 2 {
				if err := json.NewDecoder(r.Body).Decode(&closeRequest); err != nil {
					t.Error(err)
				}
			}
			_, _ = fmt.Fprintf(w, `{"Result":0,"OrderId":"owned-%d"}`, count)
		case "/api/v1/redirect/cancelorder":
			cancels.Add(1)
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["OrderId"] != "owned-1" {
				t.Error("当運転の注文以外を取り消しました", request, err)
			}
			_, _ = fmt.Fprint(w, `{"Result":0,"OrderId":"owned-1"}`)
		default:
			t.Error("想定外の通信です", r.URL.Path)
		}
	}))
	defer server.Close()
	var err error
	a.client, err = controller.New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a.client.SetDryRun(false)
	queueExtra(t, a, m, boundary.Add(-time.Second))
	if err = a.dispatch(context.Background(), m, boundary.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	old := m.Portfolio
	a.view.BrokerOrders = []controller.BrokerOrder{
		{ID: "owned-1", Symbol: m.Saved.Contract.Symbol, Side: "2", State: 4},
		{ID: "external-order", Symbol: m.Saved.Contract.Symbol, Side: "2", State: 4},
	}
	external := controller.Position{ExecutionID: "external-hold", Symbol: m.Saved.Contract.Symbol, Side: "buy", Quantity: 8, AvailableQuantity: 8}
	a.view.Positions = []controller.Position{external}
	for i := 0; i < 2; i++ {
		ready, updateErr := a.updateMethodSlot(context.Background(), m, boundary.Add(time.Duration(i)*time.Millisecond))
		if updateErr != nil || ready || m.Portfolio != old || m.Saved.SlotSwitch == nil {
			t.Fatal("取消受付で戦略を切り替えました", updateErr)
		}
	}
	if sends.Load() != 1 || cancels.Load() != 1 || !m.Manager.HasPending() {
		t.Fatal("取消受付と終了照合を区別していません")
	}
	view := a.view
	view.BrokerOrders[0] = controller.BrokerOrder{ID: "owned-1", Symbol: m.Saved.Contract.Symbol, Side: "2", State: 5, CumQty: 2,
		Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "owned-fill", Qty: 2, Price: 111, TransactTime: boundary.Format(time.RFC3339Nano)}}}
	view.Positions = append([]controller.Position{external}, controller.Position{ExecutionID: "owned-fill", Symbol: m.Saved.Contract.Symbol, Side: "buy", Quantity: 2, AvailableQuantity: 2})
	if err = a.handle(context.Background(), feedEvent{Kind: "口座", At: boundary, View: view}); err != nil {
		t.Fatal(err)
	}
	ready, err := a.updateMethodSlot(context.Background(), m, boundary.Add(2*time.Millisecond))
	if err != nil || ready || m.Portfolio != old || m.Saved.SlotSwitch.Phase != "close" {
		t.Fatal("競合約定の返済確認前に切り替えました", err)
	}
	if sends.Load() != 2 || len(closeRequest.ClosePositions) != 1 || closeRequest.ClosePositions[0].HoldID != "owned-fill" || closeRequest.ClosePositions[0].Qty != 2 {
		t.Fatal("今回の2枚以外を返済対象に含めました", closeRequest)
	}
	view.BrokerOrders = append(view.BrokerOrders, controller.BrokerOrder{ID: "owned-2", Symbol: m.Saved.Contract.Symbol, Side: "1", State: 4, CumQty: 1,
		Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "owned-close-1", Qty: 1, Price: 109, TransactTime: boundary.Format(time.RFC3339Nano)}}})
	view.Positions[1].Quantity, view.Positions[1].AvailableQuantity = 1, 1
	if err = a.handle(context.Background(), feedEvent{Kind: "口座", At: boundary, View: view}); err != nil {
		t.Fatal(err)
	}
	ready, err = a.updateMethodSlot(context.Background(), m, boundary.Add(3*time.Millisecond))
	if err != nil || ready || m.Portfolio != old || m.Manager.Quantity("extra", "buy") != 1 || sends.Load() != 2 || cancels.Load() != 1 {
		t.Fatal("部分返済だけで切り替えるか、境界返済を重複発注しました", err)
	}
	closing := &view.BrokerOrders[len(view.BrokerOrders)-1]
	closing.State, closing.CumQty = 5, 2
	closing.Details = append(closing.Details, controller.BrokerDetail{RecType: 8, ExecutionID: "owned-close-2", Qty: 1, Price: 109, TransactTime: boundary.Format(time.RFC3339Nano)})
	view.Positions = []controller.Position{external}
	if err = a.handle(context.Background(), feedEvent{Kind: "口座", At: boundary, View: view}); err != nil {
		t.Fatal(err)
	}
	ready, err = a.updateMethodSlot(context.Background(), m, boundary.Add(4*time.Millisecond))
	if err != nil || !ready || m.Portfolio == old || len(m.Manager.Positions()) != 0 || m.Manager.HasPending() || sends.Load() != 2 || cancels.Load() != 1 {
		t.Fatal("所有分の返済完了後の切替が不正です", err)
	}
}

// TestSlotUnknownOrderNeverSwitches は結果不明注文が残る間は次枠を起動しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。全HTTP通信は試験サーバーだけに送信します。
func TestSlotUnknownOrderNeverSwitches(t *testing.T) {
	a, m, boundary := slotFixture(t, false)
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		http.Error(w, "試験用の結果不明", http.StatusBadGateway)
	}))
	defer server.Close()
	var err error
	a.client, err = controller.New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a.client.SetDryRun(false)
	queueExtra(t, a, m, boundary.Add(-time.Second))
	if err = a.dispatch(context.Background(), m, boundary.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	old := m.Portfolio
	ready, err := a.updateMethodSlot(context.Background(), m, boundary)
	if err != nil || ready || m.Portfolio != old || !m.Manager.HasPending() || m.Manager.PendingError() == "" || sends.Load() != 1 {
		t.Fatal("結果不明を捨てるか再送しました", err)
	}
}

// TestSlotClockRollbackStopsEntryWithoutReturningToOldWindow は時計逆行中に過去枠を再生成しません。
//
// 引数: t(*testing.T)。返り値: なし。既存玉の返済が引き続き許可されることも検査します。
func TestSlotClockRollbackStopsEntryWithoutReturningToOldWindow(t *testing.T) {
	a, m, boundary := slotFixture(t, true)
	if ready, err := a.updateMethodSlot(context.Background(), m, boundary); err != nil || !ready {
		t.Fatal(err)
	}
	queueExtra(t, a, m, boundary)
	if err := a.dispatch(context.Background(), m, boundary); err != nil {
		t.Fatal(err)
	}
	if len(m.Manager.Positions()) == 0 {
		t.Fatal("人工の所有玉がありません")
	}
	old, namespace := m.Portfolio, m.Portfolio.Config.Namespace
	maximum := m.Saved.SlotEvaluatedAt
	rollback := boundary.Add(-time.Second)
	if _, err := a.updateMethodSlot(context.Background(), m, rollback); err != nil {
		t.Fatal(err)
	}
	if m.Portfolio != old || m.Saved.ActiveSlot.ID != "後半" || m.Portfolio.Config.Namespace != namespace || !m.Saved.SlotEvaluatedAt.Equal(maximum) {
		t.Fatal("時計逆行で過去枠へ戻るか新規状態を作りました")
	}
	if !strings.Contains(a.slotBlockReason(m, rollback), "時計") || a.canEnter(m, rollback) {
		t.Fatal("時計逆行中の新規を許可しました")
	}
	resolved, err := a.resolveMethodSlot(m, rollback)
	if err != nil || resolved.ID != "後半" {
		t.Fatal("時計逆行で過去枠を解決しました", err)
	}
	if err = a.enqueue(m, m.Portfolio.Flatten(seconds(rollback), "時計逆行中の保護返済"), 0, rollback); err != nil {
		t.Fatal(err)
	}
	if err = a.dispatch(context.Background(), m, rollback); err != nil {
		t.Fatal(err)
	}
	if len(m.Manager.Positions()) != 0 || len(m.Manager.Records()) != 2 {
		t.Fatal("時計逆行中の返済まで禁止しました")
	}
	if ready, err := a.updateMethodSlot(context.Background(), m, boundary.Add(time.Second)); err != nil || !ready || m.Portfolio != old {
		t.Fatal("時計復帰で再び手法を初期化しました", err)
	}
}

// TestSlotClockRollbackKeepsClosingTarget は切替中の時計逆行でも元の次枠と返済処理を保持します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSlotClockRollbackKeepsClosingTarget(t *testing.T) {
	a, m, boundary := slotFixture(t, true)
	queueExtra(t, a, m, boundary.Add(-time.Second))
	if err := a.dispatch(context.Background(), m, boundary.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	target, err := schedule.ResolveSlot(m.Saved.Plan, a.cfg, m.Reference, boundary)
	if err != nil {
		t.Fatal(err)
	}
	m.Saved.SlotSwitch = &slotTransition{Target: target, Phase: "close", StartedAt: boundary}
	m.Saved.SlotEvaluatedAt = boundary.Round(0)
	old := m.Portfolio
	ready, err := a.updateMethodSlot(context.Background(), m, boundary.Add(-time.Second))
	if err != nil || ready || m.Portfolio != old || m.Saved.SlotSwitch == nil || m.Saved.SlotSwitch.Target.ID != "後半" || len(m.Manager.Positions()) != 0 {
		t.Fatal("逆行中に次枠を捨てたか、返済せず停止しました", err)
	}
	ready, err = a.updateMethodSlot(context.Background(), m, boundary)
	if err != nil || !ready || m.Saved.ActiveSlot.ID != "後半" || m.Portfolio == old {
		t.Fatal("壁時計の復帰後にだけ次枠へ移行できませんでした", err)
	}
}

// TestSlotRejectsUnpreparedSharedFeatures は共通指標の設定差と未準備の研究特徴を拒否します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSlotRejectsUnpreparedSharedFeatures(t *testing.T) {
	a, m, boundary := slotFixture(t, true)
	a.features.E01.Config.EMASlow++
	if ready, err := a.updateMethodSlot(context.Background(), m, boundary); err == nil || ready || !strings.Contains(err.Error(), "共通指標") {
		t.Fatal("異なる設定の共通指標を黙って流用しました", err)
	}
	b, n, _, now := integrationApp(t, "NK225", true)
	slot := schedule.Slot{ID: "研究手法", Method: "D07", Enabled: true, Start: now, End: n.Saved.Plan.End}
	if _, err := b.newSlotPortfolio(n, n.Saved.Plan, &slot, "人工"); err == nil || !strings.Contains(err.Error(), "研究特徴") {
		t.Fatal("研究特徴が準備されていないままD07へ切り替えました", err)
	}
}

// TestSlotSubmitRechecksClockAfterPermit は開始時計が有効でも送信直前の境界越えを拒否します。
//
// 引数: t(*testing.T)。返り値: なし。外部通信は行わず仮想約定も作らないことを検査します。
func TestSlotSubmitRechecksClockAfterPermit(t *testing.T) {
	a, m, boundary := slotFixture(t, true)
	a.control = NewControl(true)
	queueExtra(t, a, m, boundary.Add(-time.Second))
	clockCalls := 0
	a.submissionClock = func(observed time.Time) time.Time {
		clockCalls++
		if a.control.submit.TryLock() {
			a.control.submit.Unlock()
			t.Error("送信許可ロック取得前に最終時刻を検査しています")
		}
		return boundary
	}
	if err := a.dispatch(context.Background(), m, boundary.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if clockCalls != 1 || len(m.Manager.Records()) != 0 || m.Manager.HasPending() || len(m.Saved.Queue) != 0 || len(m.Portfolio.Status().Pending) != 0 {
		t.Fatal("古い開始時計で境界後の新規を送信しました")
	}
}

// TestSlotHistoryReconnectSkipsPreActivationRows は切替直後の履歴同期で旧枠の売買影を再生しません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSlotHistoryReconnectSkipsPreActivationRows(t *testing.T) {
	a, m, now := indexDayApp(t)
	a.cfg.Windows = []config.MethodWindow{{ID: "day", Session: "Day", Start: "SESSION_START", End: "SESSION_END", Method: "TOPIX_T06"}}
	m.Saved.SlotActivatedAt = now
	m.Saved.LastRawHash = "旧枠の原文"
	rows := []strategy.Observation{indexObservation(0, seconds(now)-20), indexObservation(1, seconds(now)-1),
		indexObservation(2, seconds(now)+1), indexObservation(3, seconds(now)+2)}
	hashes := []string{"前の原文", m.Saved.LastRawHash, "新枠1", "新枠2"}
	p := &prepared{Features: a.features, Plan: a.plan, Generation: "切替後の履歴", Cutoff: now.Add(3 * time.Second),
		Rows: rows, Last: map[string]strategy.Observation{"TOPIX": rows[3]},
		SourceHashes: map[string][]string{"TOPIX": hashes}}
	if err := a.activate(p); err != nil {
		t.Fatal(err)
	}
	if m.Portfolio.Paper.Row != 1 || len(m.Portfolio.PaperRows) != 2 || m.Portfolio.LastIndex != 3 || m.Saved.LastRawHash != "新枠2" {
		t.Fatal("切替前の原行を新手法の影へ再投入しました", m.Portfolio.Paper.Row, m.Portfolio.PaperRows)
	}
	if _, exists := m.Portfolio.PaperRows[0]; exists {
		t.Fatal("切替前の原行が紙影へ残りました")
	}
	if len(m.Manager.Records()) != 0 || len(m.Saved.Queue) != 0 {
		t.Fatal("履歴再接続だけで注文を生成しました")
	}
}
