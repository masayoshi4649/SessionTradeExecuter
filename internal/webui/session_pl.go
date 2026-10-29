package webui

import "github.com/masayoshi4649/SessionTradeExecuter/internal/journal"

// SessionPL は共通ログ領域のセッション損益CSVをその都度読み取ります。
//
// 引数: なし。返り値: []journal.SessionPLとerror。欠損は空一覧、読取失敗はerrorです。
// 商品別workerの保存先やSQLite・旧集計には遡らず、設定取得後は管理ロックを解放します。
func (m *Manager) SessionPL() ([]journal.SessionPL, error) {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	rows, err := journal.ReadSessionPL(cfg.Path(cfg.Runtime.LogDirectory))
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []journal.SessionPL{}
	}
	return rows, nil
}
