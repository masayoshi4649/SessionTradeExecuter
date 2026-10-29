package webui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

// managedTestRun は実通信をせずに所有状態と終了時刻を指定できる試験用運転です。
type managedTestRun struct {
	cfg     config.Config
	control *runner.Control
	publish func(runner.Snapshot)
	finish  chan error
}

// controlledManager は個別の運転終了を試験側から通知する管理器を作ります。
//
// 引数: t(*testing.T)。返り値: *Managerと<-chan managedTestRun。証券口座には接続しません。
func controlledManager(t *testing.T) (*Manager, <-chan managedTestRun) {
	t.Helper()
	runs := make(chan managedTestRun, 20)
	m, err := NewManager(testConfig(t), func(ctx context.Context, cfg config.Config, control *runner.Control, publish func(runner.Snapshot)) error {
		finish := make(chan error, 1)
		runs <- managedTestRun{cfg: cfg, control: control, publish: publish, finish: finish}
		select {
		case err := <-finish:
			return err
		case <-ctx.Done():
			return nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, runs
}

// nextRun は次の起動通知を短い期限で受け取ります。
//
// 引数: t(*testing.T)、runs(<-chan managedTestRun)。返り値: managedTestRun。
func nextRun(t *testing.T, runs <-chan managedTestRun) managedTestRun {
	t.Helper()
	select {
	case run := <-runs:
		return run
	case <-time.After(3 * time.Second):
		t.Fatal("次の運転が開始されませんでした")
		return managedTestRun{}
	}
}

// setTestMode は最新の設定版で一商品のモードを操作します。
//
// 引数: t(*testing.T)、m(*Manager)、product/mode(string)。返り値: なし。
func setTestMode(t *testing.T, m *Manager, product, mode string) {
	t.Helper()
	if err := m.SetMode(product, mode, m.State().SettingsRevision, "試験"); err != nil {
		t.Fatal(err)
	}
}

// TestModeSwitchWaitsForOldWorker は画面が空になっただけでは切り替えず、旧運転の終了を待つことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。全モードで試験用運転だけを使用します。
func TestModeSwitchWaitsForOldWorker(t *testing.T) {
	m, runs := controlledManager(t)
	setTestMode(t, m, "NK225mini", "dryrun")
	old := nextRun(t, runs)
	old.publish(runner.Snapshot{Product: "NK225mini", BuyQuantity: 2, Pending: true, Queued: 1})
	setTestMode(t, m, "NK225mini", "live")
	s := m.State().Products[1]
	if s.Mode != "live" || s.ActiveMode != "dryrun" || !s.Pending || s.Enabled || s.Snapshot.BuyQuantity != 2 {
		t.Fatal("希望状態と旧運転の所有状態を保持していません", s)
	}
	if !old.control.DetachStopRequested() || old.control.Enabled() {
		t.Fatal("旧運転の新規・返済を禁止して管理終了を要求していません")
	}
	old.publish(runner.Snapshot{Product: "NK225mini"})
	select {
	case <-runs:
		t.Fatal("画面の空表示だけで次の運転を開始しました")
	default:
	}
	old.publish(runner.Snapshot{Product: "NK225mini", BuyQuantity: 2})
	old.finish <- nil
	next := nextRun(t, runs)
	if next.cfg.Trading.DryRun || next.control == old.control || !next.control.Enabled() {
		t.Fatal("旧運転の終了後に独立した実取引運転を開始していません")
	}
	s = m.State().Products[1]
	if s.ActiveMode != "live" || s.Pending || !s.Enabled || s.DetachedSnapshot == nil || s.DetachedSnapshot.BuyQuantity != 2 {
		t.Fatal("切替完了が画面へ反映されていません", s)
	}
}

// TestPendingModeUsesLatestDesired は切替待ちの追加操作で旧運転を再開せず、最後の希望だけを適用します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestPendingModeUsesLatestDesired(t *testing.T) {
	m, runs := controlledManager(t)
	setTestMode(t, m, "TOPIXmini", "live")
	old := nextRun(t, runs)
	old.publish(runner.Snapshot{Product: "TOPIXmini", SellQuantity: 1})
	setTestMode(t, m, "TOPIXmini", "dryrun")
	setTestMode(t, m, "TOPIXmini", "off")
	setTestMode(t, m, "TOPIXmini", "live")
	if old.control.Enabled() || !old.control.DetachStopRequested() || !m.State().Products[4].Pending {
		t.Fatal("切替待ちの旧運転を再開しました")
	}
	old.publish(runner.Snapshot{Product: "TOPIXmini"})
	old.finish <- nil
	latest := nextRun(t, runs)
	if latest.cfg.Trading.DryRun {
		t.Fatal("古いdryrun希望で開始しました")
	}
}

// TestLiveOffDetachesOldHoldings は実取引OFFで旧玉を管理外へ移し、次のONを新規運転にすることを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestLiveOffDetachesOldHoldings(t *testing.T) {
	m, runs := controlledManager(t)
	setTestMode(t, m, "NK225mini", "live")
	old := nextRun(t, runs)
	old.publish(runner.Snapshot{Product: "NK225mini", BuyQuantity: 1})
	setTestMode(t, m, "NK225mini", "off")
	s := m.State().Products[1]
	if s.Mode != "off" || s.ActiveMode != "live" || !s.Pending || !old.control.DetachStopRequested() || old.control.Enabled() || s.Snapshot.BuyQuantity != 1 {
		t.Fatal("OFFで注文の取消照合待ちを保持していません", s)
	}
	old.finish <- nil
	until(t, func() bool { return !m.State().Products[1].Pending })
	s = m.State().Products[1]
	if s.ActiveMode != "off" || s.Snapshot != nil || s.DetachedSnapshot == nil || s.DetachedSnapshot.BuyQuantity != 1 || !s.SettingsEditable {
		t.Fatal("旧建玉を管理外の監査表示に分離していません", s)
	}
	setTestMode(t, m, "NK225mini", "live")
	next := nextRun(t, runs)
	if old.control.Enabled() || next.control == old.control || !next.control.Enabled() || m.State().Products[1].Snapshot != nil {
		t.Fatal("次のONで旧運転を再利用しました")
	}
}

// TestProductSettingsRequireOffAndDetached はON中・取消照合中の変更を拒否し、管理外に移した旧玉があっても編集できます。
//
// 引数: t(*testing.T)。返り値: なし。
func TestProductSettingsRequireOffAndDetached(t *testing.T) {
	m, runs := controlledManager(t)
	setTestMode(t, m, "NK225mini", "dryrun")
	old := nextRun(t, runs)
	old.publish(runner.Snapshot{Product: "NK225mini"})
	if err := m.UpdateProduct("NK225mini", 8, "TG04", "TG04", m.State().SettingsRevision, "試験"); err == nil {
		t.Fatal("ON中の数量変更を受け付けました")
	}
	setTestMode(t, m, "NK225mini", "off")
	for _, snapshot := range []runner.Snapshot{{BuyQuantity: 1}, {SellQuantity: 1}, {Pending: true}, {PendingError: "照合待ち"}, {Queued: 1}} {
		old.publish(snapshot)
		if err := m.UpdateProduct("NK225mini", 8, "TG04", "TG04", m.State().SettingsRevision, "試験"); err == nil {
			t.Fatal("保有・未確定状態の数量変更を受け付けました", snapshot)
		}
	}
	old.publish(runner.Snapshot{Product: "NK225mini", BuyQuantity: 1})
	old.finish <- nil
	until(t, func() bool { return !m.State().Products[1].Pending })
	if err := m.UpdateProduct("NK225mini", 8, "TG04", "TG04", m.State().SettingsRevision, "試験"); err != nil {
		t.Fatal(err)
	}
	if m.State().Products[1].Pending || m.State().Products[1].MaxPerSide != 8 || !m.State().Products[1].SettingsEditable {
		t.Fatal("旧建玉を管理外へ移した後の新数量を保存していません")
	}
	setTestMode(t, m, "NK225mini", "dryrun")
	next := nextRun(t, runs)
	_, market := next.cfg.Selected()
	if market.MaxPerSide != 8 {
		t.Fatal("新数量が次の運転へ反映されていません")
	}
}

// TestGlobalSettingsRejectRunningAndKeepWebRestart は稼働中の共通設定変更を拒否し、待受変更だけを再起動予約します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestGlobalSettingsRejectRunningAndKeepWebRestart(t *testing.T) {
	m, runs := controlledManager(t)
	setTestMode(t, m, "NK225mini", "dryrun")
	old := nextRun(t, runs)
	old.publish(runner.Snapshot{Product: "NK225mini"})
	s, _ := m.Settings()
	s.Controller.AccountPollSeconds++
	if err := m.UpdateSettings(s, "試験"); err == nil {
		t.Fatal("ON中の共通接続設定変更を受け付けました")
	}
	s, _ = m.Settings()
	s.Web.Port++
	if err := m.UpdateSettings(s, "試験"); err != nil {
		t.Fatal(err)
	}
	if !m.State().RestartRequired || old.control.DetachStopRequested() {
		t.Fatal("待受先だけの変更で売買運転を切り替えました")
	}
	setTestMode(t, m, "NK225mini", "off")
	old.finish <- nil
	until(t, func() bool { return !m.State().Products[1].Pending })
	s, _ = m.Settings()
	s.Controller.AccountPollSeconds++
	if err := m.UpdateSettings(s, "試験"); err != nil {
		t.Fatal(err)
	}
	setTestMode(t, m, "NK225mini", "dryrun")
	next := nextRun(t, runs)
	if next.cfg.Controller.AccountPollSeconds != s.Controller.AccountPollSeconds {
		t.Fatal("次の運転へ共通接続設定を適用していません")
	}
}

// TestModesPersistAndStaleRevisionDoesNotStart は希望モードの保存と古い画面からの更新拒否を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestModesPersistAndStaleRevisionDoesNotStart(t *testing.T) {
	m, runs := controlledManager(t)
	stale := m.State().SettingsRevision
	setTestMode(t, m, "NK225mini", "live")
	_ = nextRun(t, runs)
	if err := m.SetMode("TOPIXmini", "live", stale, "試験"); err == nil {
		t.Fatal("古い版で別商品を開始しました")
	}
	loaded, err := config.Load(filepath.Join(m.cfg.Root, "conf"))
	if err != nil {
		t.Fatal(err)
	}
	if config.ModeOf(loaded.Instruments[1], loaded.Trading.DryRun) != "live" || config.ModeOf(loaded.Instruments[4], loaded.Trading.DryRun) != "off" {
		t.Fatal("商品別の希望モードが保存されていません")
	}
}

// TestFailedDrainNeverStartsReplacement は切替中の失敗を隠して次のモードを開始しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestFailedDrainNeverStartsReplacement(t *testing.T) {
	m, runs := controlledManager(t)
	setTestMode(t, m, "NK225mini", "dryrun")
	old := nextRun(t, runs)
	old.publish(runner.Snapshot{Product: "NK225mini", BuyQuantity: 1})
	setTestMode(t, m, "NK225mini", "live")
	old.finish <- errors.New("所有状態の照合失敗")
	until(t, func() bool { return m.State().Products[1].Error != "" })
	if err := m.SetMode("NK225mini", "live", m.State().SettingsRevision, "試験"); err == nil {
		t.Fatal("エラー停止した商品を再起動しました")
	}
	s := m.State().Products[1]
	if s.Enabled || s.Pending || s.ActiveMode != "dryrun" || s.Snapshot.BuyQuantity != 1 {
		t.Fatal("停止前の所有情報またはエラーを失いました", s)
	}
	select {
	case <-runs:
		t.Fatal("エラーを隠して次の運転を開始しました")
	default:
	}
}

// TestStaleOffStillDetachesAndNeverRestartsImplicitly は古い画面のOFFも受け付け、別商品の保存で復活させません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestStaleOffStillDetachesAndNeverRestartsImplicitly(t *testing.T) {
	m, runs := controlledManager(t)
	stale := m.State().SettingsRevision
	setTestMode(t, m, "NK225mini", "live")
	old := nextRun(t, runs)
	old.publish(runner.Snapshot{Product: "NK225mini", BuyQuantity: 2})
	if err := m.SetMode("NK225mini", "off", stale, "試験"); err == nil {
		t.Fatal("古い版の保存エラーを隠しました")
	}
	s := m.State().Products[1]
	if !old.control.DetachStopRequested() || old.control.Enabled() || s.Mode != "off" || s.Error == "" {
		t.Fatal("設定競合によりOFFの実行まで拒否しました", s)
	}
	settings, _ := m.Settings()
	if settings.Instruments[1].Mode != "off" {
		t.Fatal("別商品の保存に旧liveが混ざります")
	}
	old.finish <- nil
	until(t, func() bool { return !m.State().Products[1].Pending })
	setTestMode(t, m, "TOPIXmini", "dryrun")
	other := nextRun(t, runs)
	_, market := other.cfg.Selected()
	if market.Product != "TOPIXmini" || m.State().Products[1].Mode != "off" || m.State().Products[1].Enabled {
		t.Fatal("別商品の操作でOFF商品を再起動しました")
	}
	select {
	case <-runs:
		t.Fatal("OFF商品の追加起動があります")
	default:
	}
}

// TestWholeSettingsCannotEnableTrading は全般設定の保存から運転モードを変更できないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestWholeSettingsCannotEnableTrading(t *testing.T) {
	m, runs := controlledManager(t)
	s, _ := m.Settings()
	s.Instruments[1].Mode = "live"
	if err := m.UpdateSettings(s, "試験"); err == nil {
		t.Fatal("全般保存から実取引を開始しました")
	}
	select {
	case <-runs:
		t.Fatal("拒否した操作から運転が始まりました")
	default:
	}
}

// TestLogDirectoryMovesOperationAudit は保存先変更後の操作記録が新しい共通ログ先へ移ることを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestLogDirectoryMovesOperationAudit(t *testing.T) {
	m, _ := controlledManager(t)
	s, _ := m.Settings()
	s.Runtime.LogDirectory = "変更後ログ"
	if err := m.UpdateSettings(s, "試験"); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(m.cfg.Root, "変更後ログ", "web", "操作.jsonl")
	if m.audit.Name() != newPath {
		t.Fatal("操作記録のファイルを交換していません", m.audit.Name())
	}
	if raw, err := os.ReadFile(newPath); err != nil || len(raw) == 0 {
		t.Fatal("新しい操作記録を保存していません", err)
	}
	before := m.State().SettingsRevision
	blocked := filepath.Join(m.cfg.Root, "ファイルのためフォルダにできない")
	if err := os.WriteFile(blocked, []byte("試験用の保存不能先"), 0600); err != nil {
		t.Fatal(err)
	}
	s, _ = m.Settings()
	s.Runtime.LogDirectory = filepath.Join(blocked, "log")
	if err := m.UpdateSettings(s, "試験"); err == nil {
		t.Fatal("書けない操作記録先を保存しました")
	}
	if m.State().SettingsRevision != before || m.audit.Name() != newPath {
		t.Fatal("保存先の準備失敗で設定を適用しました")
	}
}
