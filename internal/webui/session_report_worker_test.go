package webui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

// TestOffProductReportSurvivesRestart はOFFの商品でも終了場の保存だけが進むことを検査します。
//
// 引数: t(*testing.T)は一時DBと仮想売買を使う試験です。
// 返り値: なし。売買workerを起動せず、削除したCSVを次の確認で復活させません。
func TestOffProductReportSurvivesRestart(t *testing.T) {
	cfg := testConfig(t)
	root := cfg.Path(cfg.Runtime.LogDirectory)
	path := filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), "dryrun", "nk225mini", "NK225mini.sqlite")
	store, err := journal.Open(path, filepath.Join(root, "dryrun", "nk225mini", "NK225mini"))
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 10, 28, 15, 45, 0, 0, time.FixedZone("JST", 9*3600))
	if err = store.RegisterReportSession("2026-10-28_Day", true, "NK225mini", end); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(cfg, func(context.Context, config.Config, *runner.Control, func(runner.Snapshot)) error {
		t.Error("OFF商品のレポート処理が売買を起動しました")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.finalizeStoredSessionReports(end.Add(-time.Second))
	rows, err := journal.ReadSessionPL(root)
	if err != nil || len(rows) != 0 {
		t.Fatal("終了前の場を表示しました", rows, err)
	}
	manager.finalizeStoredSessionReports(end)
	rows, err = journal.ReadSessionPL(root)
	if err != nil || len(rows) != 1 || rows[0].Mode != "dryrun" || rows[0].Product != "NK225mini" {
		t.Fatal("OFF商品の終了場を保存できません", rows, err, manager.State().ReportError)
	}
	if err := os.Remove(filepath.Join(root, "sessionPL.csv")); err != nil {
		t.Fatal(err)
	}
	manager.finalizeStoredSessionReports(end.Add(time.Minute))
	rows, err = journal.ReadSessionPL(root)
	if err != nil || len(rows) != 0 {
		t.Fatal("削除したCSVを再生成しました", rows, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), "live")); !os.IsNotExist(err) {
		t.Fatal("存在しない実売買DBを作成しました", err)
	}
}
