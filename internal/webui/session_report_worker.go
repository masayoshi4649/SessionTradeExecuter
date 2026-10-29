package webui

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
)

// startSessionReportsLocked は商品OFFでも動く終了レポートの監視を一度だけ開始します。
//
// 引数: なし。呼出元はoperationとmuを保持します。
// 返り値: なし。既存DBだけを読み、API接続・売買・新しい商品台帳の作成を行いません。
func (m *Manager) startSessionReportsLocked() {
	m.reportsOnce.Do(func() {
		m.wg.Add(1)
		go m.runSessionReports()
	})
}

// runSessionReports は終了CSVとViewer資料を五秒ごとに確認し、Web終了時に監視を停止します。
//
// 引数: なし。
// 返り値: なし。未確定注文はjournal側で延期し、保存失敗は画面とコンソールへ公開します。
func (m *Manager) runSessionReports() {
	defer m.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		m.finalizeStoredSessionReports(time.Now())
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// finalizeStoredSessionReports はON・OFFと実/仮想の全商品について確定記録を閲覧資料へ反映します。
//
// 引数: now(time.Time)は終了判定時計です。
// 返り値: なし。削除された出力の復活はjournal側の出力済み印で防ぎます。
func (m *Manager) finalizeStoredSessionReports(now time.Time) {
	type source struct{ database, logs, product, mode string }
	m.mu.Lock()
	paths := make([]source, 0, len(m.products)*2)
	for _, worker := range m.products {
		cfg := worker.cfg
		_, product := cfg.Selected()
		for _, mode := range []string{"live", "dryrun"} {
			paths = append(paths, source{filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), mode, cfg.InstanceID(), product.Product+".sqlite"),
				cfg.Path(cfg.Runtime.LogDirectory), product.Product, mode})
		}
	}
	m.mu.Unlock()
	var result error
	for _, path := range paths {
		csvErr := journal.FinalizeStoredReports(path.database, path.logs, now)
		viewerErr := journal.PublishStoredViewer(path.database, path.logs, now)
		if err := errors.Join(csvErr, viewerErr); err != nil {
			result = errors.Join(result, fmt.Errorf("%s/%s: %w", path.product, path.mode, err))
		}
	}
	message := ""
	if result != nil {
		message = result.Error()
	}
	m.mu.Lock()
	previous := m.reportError
	m.reportError = message
	m.mu.Unlock()
	if previous != message {
		if message != "" {
			fmt.Printf("%s [セッションレポート保存待ち] %s\n", now.Format(time.RFC3339), message)
		} else {
			fmt.Printf("%s [セッションレポート保存復旧]\n", now.Format(time.RFC3339))
		}
	}
}
