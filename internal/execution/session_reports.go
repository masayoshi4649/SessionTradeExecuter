package execution

import "time"

// RegisterSessionReport はAPIで確定した立会終了時刻をレポート台帳へ保存します。
//
// 引数: session(string)は場ID、end(time.Time)は返済開始時刻ではなく立会終了時刻です。
// 返り値: errorは登録失敗です。商品とモードはこの注文台帳の値を使用します。
func (m *Manager) RegisterSessionReport(session string, end time.Time) error {
	return m.store.RegisterReportSession(session, m.state.DryRun, m.state.Symbol, end, m.state.Multiplier)
}

// PublishViewer は確認済み約定と管理外移管だけを小さい閲覧用ファイルへ反映します。
//
// 引数: logRoot(string)は共通ログ保存先、now(time.Time)は確認時計です。
// 返り値: errorは派生出力の失敗です。未確定注文を約定と扱わず、注文送信・再送を行いません。
func (m *Manager) PublishViewer(logRoot string, now time.Time) error {
	return m.store.PublishViewer(logRoot, now)
}

// FinalizeReports は照合済みの終了場を共通ログ領域へ出力します。
//
// 引数: logRoot(string)は全商品共通のログ領域、now(time.Time)は現在時刻です。
// 返り値: errorは保存失敗です。未確定注文や戦略へ未通知の約定があれば延期します。
func (m *Manager) FinalizeReports(logRoot string, now time.Time) error {
	if m.HasPending() || len(m.OutstandingFills()) != 0 {
		return nil
	}
	return m.store.FinalizeReports(logRoot, now)
}
