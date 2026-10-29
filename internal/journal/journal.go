// Package journal はSQLiteを正本として、復元状態・約定・受信原文・判断履歴を保存します。
package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"

	_ "modernc.org/sqlite"
)

// Trade は、受付とは区別した確認済みの実約定または仮想約定です。
type Trade struct {
	ID            string                     `json:"id"`
	At            time.Time                  `json:"at"`
	Session       string                     `json:"session"`
	Symbol        string                     `json:"symbol"`
	Product       string                     `json:"product"`
	Owner         string                     `json:"owner"`
	MethodID      string                     `json:"method_id,omitempty"`
	Evidence      json.RawMessage            `json:"evidence,omitempty"`
	TradeType     string                     `json:"trade_type"`
	Side          string                     `json:"side"`
	Quantity      int                        `json:"quantity"`
	Price         float64                    `json:"price"`
	PnL           float64                    `json:"pnl"`
	Fee           float64                    `json:"fee,omitempty"`
	HoldID        string                     `json:"hold_id,omitempty"`
	Closed        []controller.ClosePosition `json:"closed,omitempty"`
	ClosedBasis   string                     `json:"closed_basis,omitempty"`
	Reason        string                     `json:"reason"`
	ClientOrderID string                     `json:"client_order_id"`
	ReservationID string                     `json:"reservation_id"`
	BrokerOrderID string                     `json:"broker_order_id"`
	ExecutionID   string                     `json:"execution_id"`
	DryRun        bool                       `json:"dryrun"`
}

// BookRecord は、同値受信も省略せず保存する一件の入力原文です。
type BookRecord struct {
	Session    string
	Generation string
	Code       string
	Received   time.Time
	Raw        json.RawMessage
	Source     string
}

// Store は復元状態・約定・原板・監査イベントを単一SQLiteで管理します。
type Store struct {
	mu     sync.Mutex
	db     *sql.DB
	closed bool
}

// ProjectionError は旧呼出側との型互換のため残す、SQLite確定後の派生出力エラーです。
// 通常の保存処理はCSV・JSONLを出力しないため、この型を新たに返しません。
type ProjectionError struct{ Err error }

// Error は、正本を巻き戻してはいけない保存状態を説明します。
//
// 引数: なし。
// 返り値: string はエラー説明です。
func (e *ProjectionError) Error() string {
	return "SQLite正本は保存済みですがログ更新に失敗しました: " + e.Err.Error()
}

// Unwrap は、派生ログの保存エラーを返します。
//
// 引数: なし。
// 返り値: error は元の入出力エラーです。
func (e *ProjectionError) Unwrap() error { return e.Err }

var sessionPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_(Day|Night)$`)

const schema = `
CREATE TABLE IF NOT EXISTS snapshot (
 id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL,
 state BLOB NOT NULL CHECK(json_valid(state)), updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS trades (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
 session TEXT NOT NULL, dryrun INTEGER NOT NULL CHECK(dryrun IN(0,1)),
 body BLOB NOT NULL CHECK(json_valid(body))
);
CREATE INDEX IF NOT EXISTS trades_session_dryrun_seq ON trades(session,dryrun,seq);
CREATE TABLE IF NOT EXISTS sessions (
 session TEXT NOT NULL, dryrun INTEGER NOT NULL CHECK(dryrun IN(0,1)),
 PRIMARY KEY(session,dryrun)
);
CREATE TABLE IF NOT EXISTS session_products (
 session TEXT NOT NULL, dryrun INTEGER NOT NULL CHECK(dryrun IN(0,1)), product TEXT NOT NULL,
 PRIMARY KEY(session,dryrun)
);
CREATE TABLE IF NOT EXISTS report_sessions (
 session TEXT NOT NULL, dryrun INTEGER NOT NULL CHECK(dryrun IN(0,1)),
 product TEXT NOT NULL, end_at TEXT NOT NULL, emitted INTEGER NOT NULL DEFAULT 0,
 last_trade_seq INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(session,dryrun,product)
);
CREATE TABLE IF NOT EXISTS viewer_sessions (
 session TEXT NOT NULL, dryrun INTEGER NOT NULL CHECK(dryrun IN(0,1)),
 product TEXT NOT NULL, end_at TEXT NOT NULL, point_value REAL NOT NULL,
 emitted INTEGER NOT NULL DEFAULT 0, last_trade_seq INTEGER NOT NULL DEFAULT 0,
 last_event_seq INTEGER NOT NULL DEFAULT 0, content_sha256 TEXT NOT NULL DEFAULT '',
 resource_ids TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(resource_ids)),
 PRIMARY KEY(session,dryrun,product)
);
CREATE TABLE IF NOT EXISTS books (
 id INTEGER PRIMARY KEY AUTOINCREMENT, session TEXT NOT NULL,
 generation TEXT NOT NULL, code TEXT NOT NULL, received_at TEXT NOT NULL,
 source TEXT NOT NULL, raw BLOB NOT NULL CHECK(json_valid(raw))
);
CREATE INDEX IF NOT EXISTS books_session_code ON books(session,code,id);
CREATE TABLE IF NOT EXISTS events (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL,
 session TEXT NOT NULL, code TEXT NOT NULL, correlation TEXT NOT NULL,
 at TEXT NOT NULL, payload BLOB NOT NULL CHECK(json_valid(payload))
);
CREATE INDEX IF NOT EXISTS events_session ON events(session,seq);
CREATE INDEX IF NOT EXISTS events_viewer_lifecycle ON events(seq)
 WHERE kind IN ('保有玉の管理外移管','起動時状態切替');
PRAGMA user_version=1;
`

// Open は純GoのSQLiteだけを開き、旧CSV・JSONLの生成や再構成を行いません。
//
// 引数: path(string)は永続DB、logDir(string)は呼出互換の旧引数で、保存先として使用しません。
// 返り値: *Storeとerror。旧ログが存在しても変更・削除せず、空のlogDirも許容します。
func Open(path, logDir string) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("永続DBのパスを指定してください")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := openJournalSQLite(path, false)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	fail := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	// WALはDBファイルに永続する設定です。接続単位の設定は共通DSNが毎回適用します。
	var journalMode string
	if err = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&journalMode); err != nil {
		return fail(err)
	}
	if journalMode != "wal" {
		return fail(fmt.Errorf("SQLiteのWALを有効にできません: %s", journalMode))
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version != 0 && version != 1 {
		return fail(fmt.Errorf("未対応のSQLite形式です: %d", version))
	}
	var integrity string
	if err = db.QueryRow("PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		return fail(fmt.Errorf("SQLite整合性確認に失敗しました: %s: %v", integrity, err))
	}
	tx, err := db.Begin()
	if err != nil {
		return fail(err)
	}
	if _, err = tx.Exec(schema); err != nil {
		_ = tx.Rollback()
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return s, nil
}

// Load は、最後に確定した状態と、重複のない全約定を復元します。
//
// 引数: dst any は状態の格納先ポインタです。
// 返り値: []Trade は確定順の全約定、error はDB・JSONの復元失敗です。
func (s *Store) Load(dst any) ([]Trade, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw []byte
	err := s.db.QueryRow("SELECT state FROM snapshot WHERE id=1").Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, dst); err != nil {
			return nil, err
		}
	}
	return readTrades(s.db)
}

// Save は、状態・新約定・台帳変化履歴を一つのトランザクションで確定します。
//
// 引数: state any は復元用状態、trades []Trade は従来の確定順を保つ全約定です。
// 返り値: errorはSQLite保存の失敗です。通常ログの出力処理は呼びません。
func (s *Store) Save(state any, trades []Trade) error {
	return s.save(state, trades, nil)
}

// ArchiveAndSave は旧復元状態を監査保存し、新運転状態と約定履歴を原子的に確定します。
//
// 引数: state(any)は新状態、trades([]Trade)は従来の確定順を保つ全約定、metadata(any)は切替理由です。
// 返り値: errorは保存失敗です。旧snapshotは一回だけSQLiteのeventsへ全量保存します。
func (s *Store) ArchiveAndSave(state any, trades []Trade, metadata any) error {
	return s.save(state, trades, &snapshotArchive{Metadata: metadata})
}

// snapshotArchive は通常保存と明示的な運転状態切替を区別します。
type snapshotArchive struct{ Metadata any }

// save は通常保存と監査付き状態切替に共通のトランザクションを実行します。
//
// 引数: state(any)、trades([]Trade)、archive(*snapshotArchive)はnilなら通常保存です。
// 返り値: errorは検証・DB保存の失敗です。全約定の不変性を両経路で確認します。
func (s *Store) save(state any, trades []Trade, archive *snapshotArchive) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	bodies := make([][]byte, len(trades))
	seen := make(map[string]bool)
	for i, trade := range trades {
		if trade.ID == "" || seen[trade.ID] || !sessionPattern.MatchString(trade.Session) {
			return fmt.Errorf("約定IDの空欄・重複または不正セッションです: %s", trade.ID)
		}
		seen[trade.ID] = true
		if bodies[i], err = json.Marshal(trade); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := readTrades(tx)
	if err != nil {
		return err
	}
	if len(trades) < len(old) {
		return fmt.Errorf("確定済み約定を消す保存はできません")
	}
	for i, trade := range old {
		body, marshalErr := json.Marshal(trade)
		if marshalErr != nil {
			return marshalErr
		}
		if !bytes.Equal(body, bodies[i]) {
			return fmt.Errorf("確定済み約定の内容または順序が変わっています: %s", trade.ID)
		}
	}
	var previous []byte
	var revision int64
	err = tx.QueryRow("SELECT state,revision FROM snapshot WHERE id=1").Scan(&previous, &revision)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	now := time.Now()
	if archive != nil {
		prior := json.RawMessage(previous)
		if len(prior) == 0 {
			prior = json.RawMessage("null")
		}
		payload := map[string]any{"previous_revision": revision, "previous_sha256": digest(previous), "previous_state": prior, "next_state_sha256": digest(raw), "metadata": archive.Metadata}
		if err = insertEvent(tx, "起動時状態切替", "", "", "", now, payload); err != nil {
			return err
		}
	}
	if !bytes.Equal(previous, raw) {
		revision++
		if _, err = tx.Exec("INSERT INTO snapshot(id,revision,state,updated_at) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,state=excluded.state,updated_at=excluded.updated_at", revision, raw, now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		// 復元用の全状態はsnapshotだけに保ち、更新のたびにeventsへ複製しません。
		payload := map[string]any{"revision": revision, "previous_sha256": digest(previous), "state_sha256": digest(raw), "state_bytes": len(raw), "new_trade_count": len(trades) - len(old), "state_location": "snapshot", "detail_policy": "復元状態の全量はsnapshot、約定はtradesを参照"}
		if err = insertEvent(tx, "台帳更新", "", "", "", now, payload); err != nil {
			return err
		}
	}
	for i := len(old); i < len(trades); i++ {
		trade := trades[i]
		if _, err = tx.Exec("INSERT INTO trades(id,session,dryrun,body) VALUES(?,?,?,?)", trade.ID, trade.Session, trade.DryRun, bodies[i]); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO sessions(session,dryrun) VALUES(?,?)", trade.Session, trade.DryRun); err != nil {
			return err
		}
		if err = insertEvent(tx, "約定確定", trade.Session, trade.Symbol, trade.ClientOrderID, now, trade); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// EnsureSession は無約定のセッションもSQLiteの場一覧へ登録します。
//
// 引数: session string はAPI由来セッション名、dryRun bool は仮想運転です。
// 返り値: errorはDB保存の失敗です。ファイル出力や終了レポートの登録は行いません。
func (s *Store) EnsureSession(session string, dryRun bool) error {
	return s.EnsureProductSession(session, dryRun, "")
}

// EnsureProductSession は無取引の場も商品名付きで集計へ登録します。
//
// 引数: session(string)は場ID、dryRun(bool)は仮想指定、product(string)は商品名です。旧呼出しの空欄を許容します。
// 返り値: errorは保存失敗です。同一DB・同一場の商品変更は拒否し、確定約定は変更しません。
func (s *Store) EnsureProductSession(session string, dryRun bool, product string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sessionPattern.MatchString(session) {
		return fmt.Errorf("不正なセッション名です: %s", session)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if product != "" {
		var existing string
		err = tx.QueryRow("SELECT product FROM session_products WHERE session=? AND dryrun=?", session, dryRun).Scan(&existing)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if existing != "" && existing != product {
			return fmt.Errorf("同一セッションの商品を変更できません: %s / %s → %s", session, existing, product)
		}
		if _, err = tx.Exec("INSERT INTO session_products(session,dryrun,product) VALUES(?,?,?) ON CONFLICT(session,dryrun) DO UPDATE SET product=excluded.product", session, dryRun, product); err != nil {
			return err
		}
	}
	result, err := tx.Exec("INSERT OR IGNORE INTO sessions(session,dryrun) VALUES(?,?)", session, dryRun)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		if err = insertEvent(tx, "セッション記録開始", session, "", "", time.Now(), map[string]any{"dryrun": dryRun}); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// RecordBook は、一受信の原文を重複排除せず保存します。
//
// 引数: session/generation/code string、received time.Time、raw json.RawMessage は受信識別情報と原文です。
// 返り値: int64はDB入力ID、errorはSQLite保存の失敗です。
func (s *Store) RecordBook(session, generation, code string, received time.Time, raw json.RawMessage) (int64, error) {
	ids, err := s.RecordBooks([]BookRecord{{Session: session, Generation: generation, Code: code, Received: received, Raw: raw}})
	if len(ids) == 0 {
		return 0, err
	}
	return ids[0], err
}

// RecordBooks は、履歴バッチを一つのトランザクションで受信順に保存します。
//
// 引数: records []BookRecord は同値受信も含む原文群です。
// 返り値: []int64 は対応する入力ID、error は保存失敗です。未確定時にはIDを返しません。
func (s *Store) RecordBooks(records []BookRecord) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(records) == 0 {
		return []int64{}, nil
	}
	for _, r := range records {
		if !sessionPattern.MatchString(r.Session) || r.Generation == "" || r.Code == "" || r.Received.IsZero() || !json.Valid(r.Raw) {
			return nil, fmt.Errorf("受信原文の識別情報またはJSONが不正です")
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("INSERT INTO books(session,generation,code,received_at,source,raw) VALUES(?,?,?,?,?,?)")
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	ids := make([]int64, len(records))
	for i, r := range records {
		result, execErr := stmt.Exec(r.Session, r.Generation, r.Code, r.Received.Format(time.RFC3339Nano), r.Source, []byte(r.Raw))
		if execErr != nil {
			return nil, execErr
		}
		if ids[i], err = result.LastInsertId(); err != nil {
			return nil, err
		}
	}
	if err = insertEvent(tx, "入力原文保存", records[0].Session, records[0].Code, "", time.Now(), map[string]any{"count": len(ids), "first_input_id": ids[0], "last_input_id": ids[len(ids)-1]}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// Event は判断・照合・異常などの汎用履歴をSQLiteのeventsへ保存します。
//
// 引数: kind/session/code/correlation string、at time.Time、payload any は分類・相関ID・発生時刻・JSON化可能な内容です。
// 返り値: error は保存失敗です。
func (s *Store) Event(kind, session, code, correlation string, at time.Time, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == "" || at.IsZero() || (session != "" && !sessionPattern.MatchString(session)) {
		return fmt.Errorf("イベント分類・時刻・セッションが不正です")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = insertEvent(tx, kind, session, code, correlation, at, payload); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// Close はDB接続を閉じます。正本は各変更の完了時点で確定済みです。
//
// 引数: なし。
// 返り値: error は終了失敗です。複数回の終了は許容します。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

// queryer はDBとトランザクションの共通読取操作です。
type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

// readTrades は確定順に約定を読み込みます。
//
// 引数: q queryer はDBまたはトランザクションです。
// 返り値: []Trade は約定列、error は読取失敗です。
func readTrades(q queryer) ([]Trade, error) {
	rows, err := q.Query("SELECT body FROM trades ORDER BY seq")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Trade{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var trade Trade
		if err = json.Unmarshal(raw, &trade); err != nil {
			return nil, err
		}
		out = append(out, trade)
	}
	return out, rows.Err()
}

// insertEvent は同じトランザクションへ履歴を挿入します。
//
// 引数: tx *sql.Tx、kind/session/code/correlation string、at time.Time、payload any。
// 返り値: error はJSON化・挿入失敗です。
func insertEvent(tx *sql.Tx, kind, session, code, correlation string, at time.Time, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO events(kind,session,code,correlation,at,payload) VALUES(?,?,?,?,?,?)", kind, session, code, correlation, at.Format(time.RFC3339Nano), raw)
	return err
}

// digest は状態変更の前後を照合する指紋を返します。
//
// 引数: raw []byte は状態JSONです。
// 返り値: string はSHA256の16進表現です。
func digest(raw []byte) string {
	value := sha256.Sum256(raw)
	return hex.EncodeToString(value[:])
}
