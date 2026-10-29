package journal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type viewerResource struct {
	ID            string  `json:"id"`
	Title         string  `json:"title"`
	Symbol        string  `json:"symbol"`
	Session       string  `json:"session"`
	Product       string  `json:"product"`
	MethodID      string  `json:"method_id"`
	EntryMethodID string  `json:"entry_method_id,omitempty"`
	PointValue    float64 `json:"point_value"`
	PNLFormat     string  `json:"pnl_format"`
	PNLBasis      string  `json:"pnl_basis"`
	PNLPath       string  `json:"pnl_path"`
	MarketPath    string  `json:"market_path,omitempty"`
}

type viewerCatalog struct {
	SchemaVersion int              `json:"schema_version"`
	PathBase      string           `json:"path_base"`
	BaseDir       string           `json:"base_dir"`
	Resources     []viewerResource `json:"resources"`
}

// PublishViewer は確定した約定・所有終了だけをViewer資料へ投影します。
//
// 引数: logRoot(string)、now(time.Time)。返り値: error。売買や確定済みDBの約定本文を変更しません。
func (s *Store) PublishViewer(logRoot string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return publishViewer(s.db, logRoot, now)
}

// PublishStoredViewer は停止商品でも既存SQLiteだけから小さいViewer資料を更新します。
//
// 引数: dbPath/logRoot(string)、now(time.Time)。返り値: error。新しいDB・過去全場の登録を作りません。
func PublishStoredViewer(dbPath, logRoot string, now time.Time) error {
	info, err := os.Stat(dbPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("ViewerのDBが通常ファイルではありません")
	}
	db, err := openJournalSQLite(dbPath, true)
	if err != nil {
		return err
	}
	defer db.Close()
	return publishViewer(db, logRoot, now)
}

// viewerResources は同じJSONLを再利用する全手法・入口手法別の参照を作ります。
//
// 引数: session(viewerSession)、relative(string)、methods([]string)。返り値: []viewerResource。
func viewerResources(session viewerSession, relative string, methods []string) []viewerResource {
	mode, label := "live", "実売買"
	if session.Dry {
		mode, label = "dryrun", "仮想売買"
	}
	base := session.Product + "/" + session.Session + "/" + mode + "/"
	common := viewerResource{ID: base + "all", Title: session.Product + "・" + label + "・全手法", Symbol: viewerReference(session.Product), Session: session.Session, Product: session.Product, MethodID: "*", PointValue: session.PointValue, PNLFormat: "trades", PNLBasis: "gross", PNLPath: filepath.ToSlash(relative)}
	result := []viewerResource{common}
	for _, method := range methods {
		item := common
		item.ID = base + "entry/" + method
		item.Title = session.Product + "・" + label + "・" + method
		item.MethodID = method
		item.EntryMethodID = method
		result = append(result, item)
	}
	return result
}

// writeViewerCatalog は他商品・モードと利用者が残した参照を保持して一覧を更新します。
//
// 引数: root(string)、items([]viewerResource)、known(map[string]bool)。返り値: error。
// 一度掲載したIDが削除されていれば再追加せず、新たな場・入口手法だけを追加します。
func writeViewerCatalog(root string, items []viewerResource, known map[string]bool) error {
	path := filepath.Join(root, "sessionviewer.resources.json")
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	catalog := viewerCatalog{SchemaVersion: 1, PathBase: "catalog", BaseDir: root, Resources: []viewerResource{}}
	if err == nil {
		if err = json.Unmarshal(old, &catalog); err != nil {
			return fmt.Errorf("Viewer一覧が破損しています: %w", err)
		}
		if catalog.SchemaVersion != 1 || catalog.PathBase != "catalog" {
			return fmt.Errorf("Viewer一覧の形式が異なります")
		}
	}
	indices := map[string]int{}
	for i, item := range catalog.Resources {
		if item.ID == "" {
			return fmt.Errorf("Viewer一覧のIDが空です")
		}
		if _, ok := indices[item.ID]; ok {
			return fmt.Errorf("Viewer一覧のIDが重複しています")
		}
		indices[item.ID] = i
	}
	changed := false
	for _, item := range items {
		if i, ok := indices[item.ID]; ok {
			catalog.Resources[i] = item
			changed = true
			continue
		}
		if known[item.ID] {
			continue
		}
		indices[item.ID] = len(catalog.Resources)
		catalog.Resources = append(catalog.Resources, item)
		changed = true
	}
	if !changed {
		return nil
	}
	catalog.BaseDir, catalog.PathBase, catalog.SchemaVersion = root, "catalog", 1
	sort.Slice(catalog.Resources, func(i, j int) bool { return catalog.Resources[i].ID < catalog.Resources[j].ID })
	raw, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if bytes.Equal(old, raw) {
		return nil
	}
	return atomicWrite(path, raw)
}

// publishViewer は更新のある場だけを排他し、資料保存後に独立した出力位置を確定します。
//
// 引数: db(*sql.DB)、logRoot(string)、now(time.Time)。返り値: error。毎tickの全状態・原板を複製しません。
func publishViewer(db *sql.DB, logRoot string, now time.Time) error {
	if strings.TrimSpace(logRoot) == "" || now.IsZero() {
		return fmt.Errorf("Viewerの保存先又は現在時計が不正です")
	}
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='viewer_sessions'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	items, err := viewerCandidates(db)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	root, err := filepath.Abs(logRoot)
	if err != nil {
		return err
	}
	reportsMu.Lock()
	defer reportsMu.Unlock()
	lock, err := acquireReportFileLock(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	var result error
	for _, session := range items {
		if err = publishViewerSession(db, root, session); err != nil {
			mode := "live"
			if session.Dry {
				mode = "dryrun"
			}
			result = errors.Join(result, fmt.Errorf("%s/%s/%s: %w", session.Session, session.Product, mode, err))
		}
	}
	return result
}

// publishViewerSession は一場の成功だけを独立確定し、不十分な旧場で他場を巻き戻しません。
//
// 引数: db(*sql.DB)、root(string)、session(viewerSession)。返り値: error。呼出元がファイル排他を保持します。
func publishViewerSession(db *sql.DB, root string, session viewerSession) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 共通接続のBEGIN IMMEDIATEで予約後に再確認し、読取snapshotの昇格を避けます。
	row := tx.QueryRow(viewerCandidateSelect+` WHERE v.session=? AND v.dryrun=? AND v.product=? AND `+viewerCandidateChange, session.Session, session.Dry, session.Product)
	err = row.Scan(&session.Session, &session.Dry, &session.Product, &session.End, &session.PointValue, &session.Emitted, &session.LastTrade, &session.LastEvent, &session.ContentHash, &session.ResourceIDs)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	facts, lastTrade, lastEvent, e := readViewerFacts(tx, session)
	if e != nil {
		return e
	}
	raw, methods, e := buildViewerTrades(session, facts)
	if e != nil {
		return e
	}
	hash := digest(raw)
	known := map[string]bool{}
	var priorIDs []string
	if e = json.Unmarshal([]byte(session.ResourceIDs), &priorIDs); e != nil {
		return e
	}
	for _, id := range priorIDs {
		known[id] = true
	}
	relative := filepath.Join(session.Product, sessionName(session.Session, session.Dry)+".trades.jsonl")
	path := filepath.Join(root, relative)
	_, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	resources := viewerResources(session, relative, methods)
	if !session.Emitted || statErr == nil {
		if !session.Emitted || hash != session.ContentHash {
			if e = atomicWrite(path, raw); e != nil {
				return e
			}
		}
		if e = writeViewerCatalog(root, resources, known); e != nil {
			return e
		}
	}
	for _, item := range resources {
		known[item.ID] = true
	}
	ids := make([]string, 0, len(known))
	for id := range known {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	encoded, e := json.Marshal(ids)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE viewer_sessions SET emitted=1,last_trade_seq=?,last_event_seq=?,content_sha256=?,resource_ids=? WHERE session=? AND dryrun=? AND product=?`, lastTrade, lastEvent, hash, string(encoded), session.Session, session.Dry, session.Product); e != nil {
		return e
	}
	return tx.Commit()
}
