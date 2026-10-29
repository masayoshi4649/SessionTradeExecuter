package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

type fakeAPI struct {
	calls   int
	request controller.BrokerRequest
	result  controller.OrderResponse
	err     error
	before  func()
	cancels int
}

// CancelFuture は取消回数を保持して指定結果を返します。
//
// 引数: context.Context と string は試験では未使用です。
// 返り値: controller.OrderResponse と error は試験結果です。
func (f *fakeAPI) CancelFuture(_ context.Context, _ string) (controller.OrderResponse, error) {
	f.cancels++
	return f.result, f.err
}

// SendFuture は通信を行わず送信回数と本文を保持します。
//
// 引数: context.Context は未使用、request controller.BrokerRequest は本文です。
// 返り値: controller.OrderResponse と error は指定した試験結果です。
func (f *fakeAPI) SendFuture(_ context.Context, request controller.BrokerRequest) (controller.OrderResponse, error) {
	f.calls++
	f.request = request
	if f.before != nil {
		f.before()
	}
	return f.result, f.err
}

// setup は一時ディレクトリに試験台帳を準備します。
//
// 引数: t *testing.T は試験、dry bool は仮想指定、code string は商品です。
// 返り値: *Manager は台帳、string は正本パスです。
func setup(t *testing.T, dry bool, code string) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	m, err := Open(filepath.Join(dir, "log"), path, dry, code, 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, path
}

// testIntent は固定時刻の発注意図を作成します。
//
// 引数: id/side/kind string は識別・方向・区分、qty int は枚数、price float64 は単価です。
// 返り値: Intent は試験用意図です。
func testIntent(id, side, kind string, qty int, price float64) Intent {
	at := time.Date(2026, 10, 2, 10, 0, 0, 0, time.FixedZone("JST", 9*3600))
	return Intent{Owner: "core", ID: id, Session: "2026-10-02_Day", ContractSymbol: "167120019", Side: side, TradeType: kind, Quantity: qty, Price: price, At: at, ExpiresAt: at.Add(time.Hour), Reason: "検証"}
}

// testView は新鮮な口座応答を作成します。
//
// 引数: なし。
// 返り値: controller.OrdersView は接続中の応答です。
func testView() controller.OrdersView {
	return controller.OrdersView{InstanceID: "instance1", Connected: true, Session: &controller.Session{Exchange: 33}}
}

// acknowledge は試験用戦略状態を約定と同時に確定します。
//
// 引数: t *testing.T は試験、m *Manager は台帳です。
// 返り値: なし。
func acknowledge(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Checkpoint(json.RawMessage(`{"確認":"完了"}`)); err != nil {
		t.Fatal(err)
	}
}

// TestDryRunRoundTripRestart は仮想約定・実現損益・再通知・CSVの整合性を確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestDryRunRoundTripRestart(t *testing.T) {
	m, path := setup(t, true, "TOPIXmini")
	if err := m.EnsureSession("2026-10-02_Day"); err != nil {
		t.Fatal(err)
	}
	a := &fakeAPI{}
	fills, err := m.Submit(context.Background(), a, testIntent("open", "buy", "new", 5, 4000), testView())
	if err != nil || len(fills) != 1 {
		t.Fatalf("%v %+v", err, fills)
	}
	if a.calls != 0 {
		t.Fatal("仮想モードが実送信しました")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(filepath.Join(filepath.Dir(path), "log"), path, true, "TOPIXmini", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if len(restored.OutstandingFills()) != 1 || len(restored.Positions()) != 1 {
		t.Fatal("再起動で未反映約定を復元できません")
	}
	acknowledge(t, restored)
	fills, err = restored.Submit(context.Background(), a, testIntent("close", "sell", "close", 2, 4004), testView())
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 1 || fills[0].PnL != 8000 || restored.Positions()[0].Quantity != 3 {
		t.Fatalf("返済が不一致: %+v", fills)
	}
	acknowledge(t, restored)
	fills, err = restored.Submit(context.Background(), a, testIntent("close", "sell", "close", 2, 4004), testView())
	if err != nil || len(fills) != 0 || len(restored.Records()) != 2 {
		t.Fatal("同じ意図が重複約定しました")
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(path), "log")); !os.IsNotExist(err) {
		t.Fatal("約定記録又は再起動が旧ログ出力先を作成しました", err)
	}
}

// TestOpenReportsSavedAndCurrentSettings は復元拒否の理由と復旧先を示し、保存済み建玉を保持することを確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。通信せず、一時DBで設定差分の表示と元設定による再開を検査します。
func TestOpenReportsSavedAndCurrentSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "NK225micro.sqlite")
	logs := filepath.Join(dir, "log")
	m, err := Open(logs, path, true, "NK225micro", 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	if _, err = m.Submit(context.Background(), api, testIntent("saved-open", "sell", "new", 20, 65000), testView()); err != nil {
		_ = m.Close()
		t.Fatal(err)
	}
	acknowledge(t, m)
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	// ----------------------------------------
	// 上限を下げても保存状態を変更せず、相違項目だけを具体的に通知します。
	for _, tc := range []struct {
		name       string
		symbol     string
		dryrun     bool
		multiplier float64
		cap        int
		want       string
	}{
		{"上限20から10", "NK225micro", true, 10, 10, "market.max_per_side: 保存=20枚 / 現在=10枚"},
		{"銘柄", "NK225mini", true, 10, 20, `market.product: 保存="NK225micro" / 現在="NK225mini"`},
		{"仮想指定", "NK225micro", false, 10, 20, "trading.dryrun: 保存=true / 現在=false"},
		{"円換算倍率", "NK225micro", true, 100, 20, "円換算倍率: 保存=10 / 現在=100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rejected, openErr := Open(logs, path, tc.dryrun, tc.symbol, tc.multiplier, tc.cap)
			if rejected != nil {
				_ = rejected.Close()
				t.Fatal("設定が異なる台帳の復元を受け入れました")
			}
			if openErr == nil {
				t.Fatal("設定の相違が通知されませんでした")
			}
			for _, want := range []string{tc.want, "対象DB: " + path, "conf/*.toml", "元の設定（上記の保存値）へ戻してください", "保存済みの建玉・注文は変更していません"} {
				if !strings.Contains(openErr.Error(), want) {
					t.Fatalf("復元エラーに必要な情報がありません: %q\n%v", want, openErr)
				}
			}
			// ----------------------------------------
			// 拒否後も元設定で同じ約定・残玉を復元でき、新たな注文は作りません。
			restored, restoreErr := Open(logs, path, true, "NK225micro", 10, 20)
			if restoreErr != nil {
				t.Fatal(restoreErr)
			}
			defer func() { _ = restored.Close() }()
			if restored.state.MaxPosition != 20 || restored.Quantity("core", "sell") != 20 || len(restored.Records()) != 1 ||
				len(restored.Orders()) != 1 || string(restored.State()) != `{"確認":"完了"}` || api.calls != 0 {
				t.Fatal("設定不一致の拒否によって保存状態又は注文数が変わりました")
			}
		})
	}
}

// TestAcceptedNotFilledPartialAndOwnedClose は受付・部分約定・所有建玉指定を分離します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestAcceptedNotFilledPartialAndOwnedClose(t *testing.T) {
	m, _ := setup(t, false, "TOPIXmini")
	api := &fakeAPI{result: controller.OrderResponse{OrderID: "order1"}}
	api.before = func() {
		var persisted savedState
		_, err := m.store.Load(&persisted)
		if err != nil || len(persisted.Orders) == 0 {
			t.Fatal("送信前に正本がありません")
		}
		last := persisted.Orders[len(persisted.Orders)-1]
		if last.Status != "prepared" || last.BrokerOrderID != "" {
			t.Fatal("送信前の意図が SQLite に確定していません")
		}
	}
	view := testView()
	fills, err := m.Submit(context.Background(), api, testIntent("open", "buy", "new", 5, 4000), view)
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 0 || len(m.Positions()) != 0 || !m.HasPending() {
		t.Fatal("受付を約定と誤認しました")
	}
	view.BrokerOrders = []controller.BrokerOrder{{ID: "order1", Symbol: "167120019", Side: "2", State: 5, CumQty: 3, Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "hold1", Qty: 3, Price: 4001}}}}
	view.Positions = []controller.Position{{ExecutionID: "hold1", Symbol: "167120019", Side: "buy", Quantity: 3, AvailableQuantity: 3, Price: 4001}, {ExecutionID: "external", Symbol: "167120019", Side: "buy", Quantity: 2, AvailableQuantity: 2, Price: 3999}}
	fills, err = m.Reconcile(view)
	if err != nil || len(fills) != 1 || fills[0].Quantity != 3 || m.HasPending() {
		t.Fatalf("部分約定結果: %+v %v", fills, err)
	}
	acknowledge(t, m)
	if f, err := m.Reconcile(view); err != nil || len(f) != 0 {
		t.Fatal("約定が二重反映されました")
	}
	api.result.OrderID = "order2"
	_, err = m.Submit(context.Background(), api, testIntent("close", "sell", "close", 3, 4002), view)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.request.ClosePositions) != 1 || api.request.ClosePositions[0].HoldID != "hold1" || api.request.ClosePositions[0].Qty != 3 {
		t.Fatal("所有外の建玉を指定しました")
	}
	view.BrokerOrders = append(view.BrokerOrders, controller.BrokerOrder{ID: "order2", Symbol: "167120019", Side: "1", State: 5, CumQty: 2, Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "closefill", Qty: 2, Price: 4003}}})
	view.Positions[0].Quantity = 1
	view.Positions[0].AvailableQuantity = 1
	fills, err = m.Reconcile(view)
	if err != nil || len(fills) != 1 || fills[0].PnL != 4000 || m.Positions()[0].Quantity != 1 || m.HasPending() {
		t.Fatalf("部分返済: %+v %v", fills, err)
	}
}

// TestUnknownNeverResends は応答喪失・再起動・同一意図再実行で重複送信しないことを確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestUnknownNeverResends(t *testing.T) {
	m, path := setup(t, false, "NK225micro")
	api := &fakeAPI{err: &controller.APIError{Unknown: true, Message: "応答喪失"}}
	intent := testIntent("unknown", "buy", "new", 2, 4000)
	_, err := m.Submit(context.Background(), api, intent, testView())
	if err == nil || !m.HasPending() || api.request.Exchange != 23 {
		t.Fatal("結果不明が保持されません")
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = Open(filepath.Join(filepath.Dir(path), "log"), path, false, "NK225micro", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	view := testView()
	view.InstanceID = "restarted"
	if _, err = m.Reconcile(view); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Submit(context.Background(), api, intent, view)
	if api.calls != 1 || !m.HasPending() || m.PendingError() == "" {
		t.Fatal("結果不明注文を再送しました")
	}
	if _, err = m.Submit(context.Background(), api, testIntent("different", "buy", "new", 2, 4000), view); err == nil {
		t.Fatal("結果不明のまま新規を受けました")
	}
}

// TestAccountCapAndStale は当起動の所有玉だけの上限と古い口座情報の拒否を確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestAccountCapAndStale(t *testing.T) {
	m, _ := setup(t, false, "TOPIXmini")
	api := &fakeAPI{}
	view := testView()
	view.Positions = []controller.Position{{Symbol: "167120019", Side: "buy", Quantity: 8}}
	m.state.Holdings["owned"] = &Holding{HoldID: "owned", Owner: "core", Side: "buy", Quantity: 8}
	if _, err := m.Submit(context.Background(), api, testIntent("cap", "buy", "new", 3, 4000), view); err == nil {
		t.Fatal("当起動の所有枚数上限を超えました")
	}
	m.state.Holdings = map[string]*Holding{}
	view.Positions = nil
	view.PositionsStale = true
	if _, err := m.Submit(context.Background(), api, testIntent("stale", "buy", "new", 3, 4000), view); err == nil {
		t.Fatal("古い口座情報で発注しました")
	}
	if api.calls != 0 {
		t.Fatal("検証前に送信しました")
	}
}

// TestExternalPendingDoesNotBlockEntry は他媒体の未完了注文・予約を所有玉や取消対象へ混ぜないことを確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestExternalPendingDoesNotBlockEntry(t *testing.T) {
	m, _ := setup(t, false, "TOPIXmini")
	api := &fakeAPI{result: controller.OrderResponse{OrderID: "new"}}
	view := testView()
	view.BrokerOrders = []controller.BrokerOrder{{ID: "external", Symbol: "167120019", State: 4, Side: "2", OrderQty: 10}}
	view.Reservations = []controller.Reservation{{ID: "reservation", Symbol: "167120019", State: "watching"}}
	if _, err := m.Submit(context.Background(), api, testIntent("allowed", "buy", "new", 3, 4000), view); err != nil {
		t.Fatal(err)
	}
	if api.calls != 1 || api.cancels != 0 || len(m.Positions()) != 0 {
		t.Fatal("外部注文を理由に新規を遮断したか、外部玉・取消を取り込みました")
	}
}

// TestKnownOrderSurvivesControllerRestart は既知注文IDだけを使う照合を確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestKnownOrderSurvivesControllerRestart(t *testing.T) {
	m, _ := setup(t, false, "TOPIXmini")
	api := &fakeAPI{result: controller.OrderResponse{OrderID: "known"}}
	view := testView()
	_, err := m.Submit(context.Background(), api, testIntent("open", "buy", "new", 1, 4000), view)
	if err != nil {
		t.Fatal(err)
	}
	view.InstanceID = "restart"
	view.BrokerOrders = []controller.BrokerOrder{{ID: "known", Symbol: "167120019", Side: "2", State: 5, CumQty: 1, Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "hold", Qty: 1, Price: 4002}}}}
	view.Positions = []controller.Position{{ExecutionID: "hold", Symbol: "167120019", Side: "buy", Quantity: 1}}
	fills, err := m.Reconcile(view)
	if err != nil || len(fills) != 1 || m.HasPending() {
		t.Fatalf("既知IDの復旧失敗 %+v %v", fills, err)
	}
}

// TestCancelAcknowledgementStillNeedsReconcile は取消受付後の遅延約定と重複取消防止を確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestCancelAcknowledgementStillNeedsReconcile(t *testing.T) {
	m, _ := setup(t, false, "TOPIXmini")
	api := &fakeAPI{result: controller.OrderResponse{OrderID: "known"}}
	intent := testIntent("open", "buy", "new", 1, 4000)
	view := testView()
	if _, err := m.Submit(context.Background(), api, intent, view); err != nil {
		t.Fatal(err)
	}
	if err := m.CancelPending(context.Background(), api, intent.At, false); err != nil {
		t.Fatal(err)
	}
	if api.cancels != 0 {
		t.Fatal("期限前に取消しました")
	}
	if err := m.CancelPending(context.Background(), api, intent.ExpiresAt, false); err != nil {
		t.Fatal(err)
	}
	if err := m.CancelPending(context.Background(), api, intent.ExpiresAt, true); err != nil {
		t.Fatal(err)
	}
	if api.cancels != 1 || !m.HasPending() {
		t.Fatal("取消受付を完了と誤認しました")
	}
	view.BrokerOrders = []controller.BrokerOrder{{ID: "known", Symbol: "167120019", Side: "2", State: 5, CumQty: 1, Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "latefill", Qty: 1, Price: 4001}}}}
	view.Positions = []controller.Position{{ExecutionID: "latefill", Symbol: "167120019", Side: "buy", Quantity: 1}}
	fills, err := m.Reconcile(view)
	if err != nil || len(fills) != 1 || m.HasPending() {
		t.Fatal("取消中の約定を照合できません")
	}
}

// TestPartialCloseUsesActualHoldReduction は部分返済対象を指定順だけで推測しないことを確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestPartialCloseUsesActualHoldReduction(t *testing.T) {
	m, _ := setup(t, false, "TOPIXmini")
	api := &fakeAPI{result: controller.OrderResponse{OrderID: "open"}}
	view := testView()
	_, err := m.Submit(context.Background(), api, testIntent("open", "buy", "new", 5, 4000), view)
	if err != nil {
		t.Fatal(err)
	}
	view.BrokerOrders = []controller.BrokerOrder{{ID: "open", Symbol: "167120019", Side: "2", State: 5, CumQty: 5, Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "holdA", Qty: 2, Price: 4000}, {RecType: 8, ExecutionID: "holdB", Qty: 3, Price: 4002}}}}
	view.Positions = []controller.Position{{ExecutionID: "holdA", Symbol: "167120019", Side: "buy", Quantity: 2, AvailableQuantity: 2}, {ExecutionID: "holdB", Symbol: "167120019", Side: "buy", Quantity: 3, AvailableQuantity: 3}}
	if _, err = m.Reconcile(view); err != nil {
		t.Fatal(err)
	}
	acknowledge(t, m)
	api.result.OrderID = "close"
	if _, err = m.Submit(context.Background(), api, testIntent("close", "sell", "close", 4, 4004), view); err != nil {
		t.Fatal(err)
	}
	view.BrokerOrders = append(view.BrokerOrders, controller.BrokerOrder{ID: "close", Symbol: "167120019", Side: "1", State: 5, CumQty: 2, Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "close2", Qty: 2, Price: 4004}}})
	if f, e := m.Reconcile(view); e != nil || len(f) != 0 || !m.HasPending() {
		t.Fatal("建玉照会が追い付く前に返済先を推測しました")
	}
	view.Positions[1].Quantity = 1
	view.Positions[1].AvailableQuantity = 1
	fills, err := m.Reconcile(view)
	if err != nil || len(fills) != 1 || fills[0].PnL != 4000 {
		t.Fatalf("実残数量の返済照合失敗: %+v %v", fills, err)
	}
	if m.state.Holdings["holdA"].Quantity != 2 || m.state.Holdings["holdB"].Quantity != 1 {
		t.Fatal("返済した建玉IDが不一致です")
	}
}

// TestLegacyCSVDoesNotAffectCommittedFill は旧CSV保存先の異常が約定保存を妨げないことを確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestLegacyCSVDoesNotAffectCommittedFill(t *testing.T) {
	m, path := setup(t, false, "TOPIXmini")
	api := &fakeAPI{result: controller.OrderResponse{OrderID: "known"}}
	view := testView()
	if _, err := m.Submit(context.Background(), api, testIntent("open", "buy", "new", 1, 4000), view); err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(filepath.Dir(path), "log", "2026-10-02_Day.csv")
	if err := os.MkdirAll(csvPath, 0700); err != nil {
		t.Fatal(err)
	}
	view.BrokerOrders = []controller.BrokerOrder{{ID: "known", Symbol: "167120019", Side: "2", State: 5, CumQty: 1, Details: []controller.BrokerDetail{{RecType: 8, ExecutionID: "hold", Qty: 1, Price: 4002}}}}
	view.Positions = []controller.Position{{ExecutionID: "hold", Symbol: "167120019", Side: "buy", Quantity: 1}}
	if _, err := m.Reconcile(view); err != nil {
		t.Fatal("廃止した旧CSV出力先にアクセスしています", err)
	}
	if len(m.Records()) != 1 || len(m.OutstandingFills()) != 1 || m.HasPending() {
		t.Fatal("正本確定済みの約定を巻き戻しました")
	}
	if err := os.Remove(csvPath); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(filepath.Join(filepath.Dir(path), "log"), path, false, "TOPIXmini", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	if len(restored.Records()) != 1 || len(restored.OutstandingFills()) != 1 {
		t.Fatal("再起動で正本約定が失われました")
	}
}
