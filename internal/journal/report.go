package journal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var reportsMu sync.Mutex

// reportSession はDB内の終了予定と出力済み位置を保持します。
type reportSession struct {
	Session, Product, End string
	Dry, Emitted          bool
	LastSeq               int64
}

// RegisterReportSession は運転した場だけを終了予定へ登録します。
//
// 引数: session(string)、dry(bool)、product(string)、end(time.Time)、viewerPointValue(...float64)は任意の既知円倍率です。
// 返り値: error。再登録で出力済み印を消さず、倍率指定時だけViewerの場を登録します。
func (s *Store) RegisterReportSession(session string, dry bool, product string, end time.Time, viewerPointValue ...float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sessionPattern.MatchString(session) || !validReportProduct(product) || end.IsZero() {
		return fmt.Errorf("終了レポートの場・商品・終了時計が不正です")
	}
	if len(viewerPointValue) > 1 || len(viewerPointValue) == 1 && (!viewerFinitePositive(viewerPointValue[0]) || viewerReference(product) == "") {
		return fmt.Errorf("Viewerの既知円倍率又は商品が不正です")
	}
	if _, err := s.db.Exec("INSERT OR IGNORE INTO report_sessions(session,dryrun,product,end_at) VALUES(?,?,?,?)", session, dry, product, end.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	var oldEnd string
	if err := s.db.QueryRow("SELECT end_at FROM report_sessions WHERE session=? AND dryrun=? AND product=?", session, dry, product).Scan(&oldEnd); err != nil {
		return err
	}
	prior, err := time.Parse(time.RFC3339Nano, oldEnd)
	if err != nil || !prior.Equal(end) {
		return fmt.Errorf("登録済み終了時計を変更できません")
	}
	if len(viewerPointValue) == 1 {
		return registerViewerSession(s.db, session, dry, product, end, viewerPointValue[0])
	}
	return nil
}

// FinalizeReports は終了済みで注文照合も完了した場だけを利用者向けCSVへ確定します。
//
// 引数: logRoot(string)、now(time.Time)。返り値: error。判断待機の有無は呼出側でも確認します。
func (s *Store) FinalizeReports(logRoot string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return finalizeReports(s.db, logRoot, now)
}

// FinalizeStoredReports は停止商品に既存DBがある場合だけ終了レポートを確定します。
//
// 引数: dbPath/logRoot(string)、now(time.Time)。返り値: error。Openや監査CSVの再生成を呼びません。
func FinalizeStoredReports(dbPath, logRoot string, now time.Time) error {
	info, err := os.Stat(dbPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("終了レポートのDBが通常ファイルではありません")
	}
	db, err := openJournalSQLite(dbPath, true)
	if err != nil {
		return err
	}
	defer db.Close()
	return finalizeReports(db, logRoot, now)
}

// pendingReportSnapshot は未送信判断・確認待ち注文・戦略未反映の約定を検査します。
//
// 引数: tx(*sql.Tx)、tradeCount(int)。返り値: boolは延期、errorは破損です。保有玉そのものでは延期しません。
func pendingReportSnapshot(tx *sql.Tx, tradeCount int) (bool, error) {
	var raw []byte
	err := tx.QueryRow("SELECT state FROM snapshot WHERE id=1").Scan(&raw)
	if err == sql.ErrNoRows {
		return tradeCount > 0, nil
	}
	if err != nil {
		return false, err
	}
	var state struct {
		Orders []struct {
			Status string `json:"status"`
		} `json:"orders"`
		Ack     *int `json:"ack_count"`
		Runtime struct {
			Queue []json.RawMessage `json:"Queue"`
		} `json:"runtime_state"`
	}
	if err = json.Unmarshal(raw, &state); err != nil {
		return false, err
	}
	if len(state.Runtime.Queue) > 0 {
		return true, nil
	}
	for _, order := range state.Orders {
		if order.Status != "done" && order.Status != "rejected" {
			return true, nil
		}
	}
	if state.Ack == nil {
		return tradeCount > 0, nil
	}
	if *state.Ack < 0 || *state.Ack > tradeCount {
		return false, fmt.Errorf("終了レポートの約定反映位置が不正です")
	}
	return *state.Ack < tradeCount, nil
}

// reportCandidates は約定本文を読まず、終了済み未出力又は遅着連番のある場だけを返します。
//
// 引数: q(queryer)、now(time.Time)。返り値: []reportSessionとerror。UTCの終了時計を解析して比較します。
func reportCandidates(q queryer, now time.Time) ([]reportSession, error) {
	rows, err := q.Query(`SELECT r.session,r.dryrun,r.product,r.end_at,r.emitted,r.last_trade_seq
FROM report_sessions r WHERE r.emitted=0 OR EXISTS
(SELECT 1 FROM trades t WHERE t.session=r.session AND t.dryrun=r.dryrun AND t.seq>r.last_trade_seq)
ORDER BY r.session,r.product,r.dryrun`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []reportSession{}
	for rows.Next() {
		var r reportSession
		if err = rows.Scan(&r.Session, &r.Dry, &r.Product, &r.End, &r.Emitted, &r.LastSeq); err != nil {
			return nil, err
		}
		end, e := time.Parse(time.RFC3339Nano, r.End)
		if e != nil || !sessionPattern.MatchString(r.Session) || !validReportProduct(r.Product) {
			return nil, fmt.Errorf("保存済み終了レポート予定が不正です")
		}
		if !now.Before(end) {
			items = append(items, r)
		}
	}
	return items, rows.Err()
}

// finalizeReports はCSV更新中も正本を確定させず、全書込み成功後に出力済み印を進めます。
//
// 引数: db(*sql.DB)、logRoot(string)、now(time.Time)。返り値: error。遅着分は現存CSVだけを訂正します。
func finalizeReports(db *sql.DB, logRoot string, now time.Time) error {
	if strings.TrimSpace(logRoot) == "" || now.IsZero() {
		return fmt.Errorf("終了レポートの保存先又は現在時計が不正です")
	}
	var exists int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='report_sessions'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	items, err := reportCandidates(db, now)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	reportsMu.Lock()
	defer reportsMu.Unlock()
	lock, err := acquireReportFileLock(logRoot)
	if err != nil {
		return err
	}
	defer lock.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// ----------------------------------------
	// 共通接続のBEGIN IMMEDIATEが読取前に書込権を確保します。
	// 排他待ちの間に他の接続が出力した場合も、約定本文へ進む前に再確認します。
	items, err = reportCandidates(tx, now)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	trades, err := readTrades(tx)
	if err != nil {
		return err
	}
	pending, err := pendingReportSnapshot(tx, len(trades))
	if err != nil {
		return err
	}
	if pending {
		return nil
	}
	for _, r := range items {
		var last int64
		if err = tx.QueryRow("SELECT COALESCE(MAX(seq),0) FROM trades WHERE session=? AND dryrun=?", r.Session, r.Dry).Scan(&last); err != nil {
			return err
		}
		if r.Emitted && r.LastSeq == last {
			continue
		}
		mode := "live"
		if r.Dry {
			mode = "dryrun"
		}
		value, raw, e := buildReport(SessionPL{Session: r.Session, Product: r.Product, Mode: mode}, trades)
		if e != nil {
			return e
		}
		path := filepath.Join(logRoot, r.Product, sessionName(r.Session, r.Dry)+".csv")
		_, statErr := os.Stat(path)
		if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		if !r.Emitted || statErr == nil {
			if err = atomicWrite(path, raw); err != nil {
				return err
			}
		}
		if err = writeSessionPL(logRoot, value, !r.Emitted); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE report_sessions SET emitted=1,last_trade_seq=? WHERE session=? AND dryrun=? AND product=?", last, r.Session, r.Dry, r.Product); err != nil {
			return err
		}
	}
	return tx.Commit()
}
