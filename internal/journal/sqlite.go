package journal

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
)

// openJournalSQLite は全保存接続に同じ耐久性・待機・書込予約方針を適用します。
//
// 引数: path は string 型のDBパス、existing は bool 型でtrueなら既存DBだけを開きます。
// 返り値: *sql.DB 型の接続プールと error 型のパス・接続生成エラーです。
// modernc.org/sqlite v1.36.3の_pragmaは物理接続ごとに実行され、_txlockは
// ReadOnlyでないBeginをIMMEDIATEにします。ReadOnly指定の診断はDEFERREDのままです。
// 待機はbusy_timeoutの5秒以内に限定し、保存本文・約定・注文を再実行しません。
func openJournalSQLite(path string, existing bool) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	filePath := filepath.ToSlash(abs)
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}
	query := url.Values{}
	query.Set("mode", "rwc")
	if existing {
		query.Set("mode", "rw")
	}
	query.Set("_txlock", "immediate")
	for _, pragma := range []string{"busy_timeout(5000)", "synchronous(FULL)", "foreign_keys(ON)"} {
		query.Add("_pragma", pragma)
	}
	u := url.URL{Scheme: "file", Path: filePath, RawQuery: query.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}
