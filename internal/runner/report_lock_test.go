package runner

import (
	"os"
	"testing"
)

// TestReportOwnerLockWithoutLogHierarchy はレポートの上書き防止と旧階層の非生成を確認します。
//
// 引数: t(*testing.T)は一時領域だけを使う試験です。
// 返り値: なし。接続先やデータ先が違っても同じ商品・モード・出力先を二重所有しません。
func TestReportOwnerLockWithoutLogHierarchy(t *testing.T) {
	cfg := startupConfig(t, "http://127.0.0.1:18080", "NK225micro")
	first, err := acquireReportOwnerLock(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	other := cfg
	other.Controller.BaseURL = "http://127.0.0.1:18081"
	other.Runtime.DataDirectory = "another-data"
	if duplicate, err := acquireReportOwnerLock(other); err == nil {
		_ = duplicate.Close()
		t.Fatal("同じレポートを別台帳から上書きできます")
	}
	other.Trading.DryRun = !cfg.Trading.DryRun
	second, err := acquireReportOwnerLock(other)
	if err != nil {
		t.Fatal("別モードのレポートを併用できません", err)
	}
	defer second.Close()
	if _, err := os.Stat(cfg.Path(cfg.Runtime.LogDirectory)); !os.IsNotExist(err) {
		t.Fatal("レポート所有ロックがlogへ不要な階層を作りました", err)
	}
}
