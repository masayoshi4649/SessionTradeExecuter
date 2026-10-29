package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
)

// diagnosticConfig は外部APIへ接続しない人工設定を作ります。
//
// 引数: t(*testing.T)。返り値: config.Configです。
func diagnosticConfig(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "conf")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	text := "[startup]\naction='diagnostics'\ndiagnostic_session='2026-10-26_Day'\n[market]\nproduct='NK225mini'\nmax_per_side=2\n[methods]\nday='TG04'\nnight='TG04'\n[trading]\ndryrun=false\n"
	if err := os.WriteFile(filepath.Join(dir, "settings.toml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// diagnosticFixture はWALを開いたまま約定と大きい旧eventを作ります。
//
// 引数: t(*testing.T)、cfg(config.Config)。返り値: *journal.Storeと*sql.DBとDBパスです。
func diagnosticFixture(t *testing.T, cfg config.Config) (*journal.Store, *sql.DB, string) {
	t.Helper()
	base := filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), "live", cfg.InstanceID())
	path := filepath.Join(base, "NK225mini.sqlite")
	store, err := journal.Open(path, filepath.Join(cfg.Root, "original-log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	at := time.Date(2026, 10, 26, 10, 1, 0, 0, time.FixedZone("JST", 9*3600))
	trade := journal.Trade{ID: "約定1", At: at, Session: "2026-10-26_Day", Symbol: "NK225mini", Product: "NK225mini", Owner: "core", TradeType: "new", Side: "buy", Quantity: 1, Price: 65000, Fee: 38, HoldID: "実玉1", Evidence: json.RawMessage(`{"risk":90,"time":100}`)}
	state := map[string]any{"symbol": "NK225mini", "dryrun": false, "max_position": 2, "runtime_state": map[string]any{"Algorithm": "00131-v1", "Plan": map[string]any{"ID": "2026-10-26_Day"}}, "holdings": map[string]any{"実玉1": map[string]any{"hold_id": "実玉1", "quantity": 1, "price": 65000, "evidence": strings.Repeat("秘匿原文", 1000)}}, "orders": []any{map[string]any{"status": "submitted", "intent": map[string]any{"id": "発注2", "quantity": 1, "evidence": "秘匿原文"}}, map[string]any{"status": "done"}}}
	state["runtime_state"].(map[string]any)["Portfolio"] = map[string]any{"Research": map[string]any{"Sleeves": []any{map[string]any{"Name": "core", "Capacity": 1, "FixedR": 100, "Stage": 0, "Budget": 0, "Source": map[string]any{"Legacy": map[string]any{"Paper": map[string]any{"normal": map[string]any{"ATR": 100, "TrailFloor": 64920}}}}}}}}
	if err = store.Save(state, []journal.Trade{trade}); err != nil {
		t.Fatal(err)
	}
	if err = store.Event("稼働", "2026-10-26_Day", "NK225", "", at, map[string]any{"profit": 30000, "exit_floor": 50}); err != nil {
		t.Fatal(err)
	}
	if err = store.Event("旧場記録", "2026-10-23_Day", "NK225", "", at.AddDate(0, 0, -3), map[string]any{"exclude": true}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	huge := `{"revision":99,"state_sha256":"saved-hash","state":{"history":"` + strings.Repeat("巨大旧履歴", 200000) + `"}}`
	if _, err = db.Exec("INSERT INTO events(kind,session,code,correlation,at,payload) VALUES(?,?,?,?,?,?)", "台帳更新", "", "", "", at.Format(time.RFC3339Nano), huge); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO events(kind,session,code,correlation,at,payload) VALUES(?,?,?,?,?,?)", "入力原文保存", "2026-10-26_Day", "NK225", "", at.Format(time.RFC3339Nano), `{"count":1}`); err != nil {
		t.Fatal(err)
	}
	return store, db, path
}

// readArchive はmanifestを使い、断片を元の論理ファイルへ戻します。
//
// 引数: t(*testing.T)、paths([]string)。返り値: map[string][]byteです。
func readArchive(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	type piece struct {
		index int
		data  []byte
	}
	parts := map[string][]piece{}
	for _, path := range paths {
		stat, err := os.Stat(path)
		if err != nil || stat.Size() > partLimit {
			t.Fatal("容量上限", stat, err)
		}
		r, err := zip.OpenReader(path)
		if err != nil {
			t.Fatal(err)
		}
		contents := map[string][]byte{}
		for _, f := range r.File {
			reader, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(reader)
			_ = reader.Close()
			if e != nil {
				t.Fatal(e)
			}
			contents[f.Name] = data
		}
		_ = r.Close()
		var m struct {
			Fragments []fragment `json:"fragments"`
		}
		if err = json.Unmarshal(contents["manifest.json"], &m); err != nil {
			t.Fatal(err)
		}
		for _, f := range m.Fragments {
			parts[f.Logical] = append(parts[f.Logical], piece{f.Index, contents[f.Entry]})
		}
		if bytes.Contains(contents["settings.json"], []byte("10.10.100.1")) {
			t.Fatal("接続先を外部診断へ含めた")
		}
		if len(contents["settings.json"]) > 2 && !bytes.Contains(contents["settings.json"], []byte("configuration_context")) {
			t.Fatal("出力時設定と運転当時設定を区別していない")
		}
	}
	out := map[string][]byte{}
	for name, v := range parts {
		sort.Slice(v, func(i, j int) bool { return v[i].index < v[j].index })
		for i, p := range v {
			if p.index != i+1 {
				t.Fatal("断片欠落", name)
			}
			out[name] = append(out[name], p.data...)
		}
	}
	return out
}

// TestExportReadOnlyWALAndCompact は実運転中相当のWAL読取・全約定・旧巨大全状態の省略を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestExportReadOnlyWALAndCompact(t *testing.T) {
	cfg := diagnosticConfig(t)
	_, db, path := diagnosticFixture(t, cfg)
	var beforeState string
	var beforeEvents int
	if err := db.QueryRow("SELECT state FROM snapshot WHERE id=1").Scan(&beforeState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM events").Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	walBefore, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal("WALがない", err)
	}
	paths, err := Export(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	files := readArchive(t, paths)
	if !bytes.Contains(files["NK225mini/trades.jsonl"], []byte("約定1")) || !bytes.Contains(files["NK225mini/trades.csv"], []byte("65000")) {
		t.Fatal("約定が欠落")
	}
	if !bytes.Contains(files["NK225mini/events.jsonl"], []byte("exit_floor")) || !bytes.Contains(files["NK225mini/events.jsonl"], []byte("saved-hash")) {
		t.Fatal("診断根拠が欠落")
	}
	for name, data := range files {
		if bytes.Contains(data, []byte("巨大旧履歴")) || bytes.Contains(data, []byte("秘匿原文")) || bytes.Contains(data, []byte("旧場記録")) {
			t.Fatal("不要原文を含めた", name)
		}
	}
	if len(files["NK225mini/events.jsonl"]) > 20000 {
		t.Fatal("イベントが縮小していない")
	}
	if !bytes.Contains(files["NK225mini/current_holdings.jsonl"], []byte("実玉1")) || !bytes.Contains(files["NK225mini/current_pending_orders.jsonl"], []byte("発注2")) {
		t.Fatal("現在所有情報欠落")
	}
	if !bytes.Contains(files["NK225mini/current_exit_state.jsonl"], []byte(`"fixed_r":100`)) || !bytes.Contains(files["NK225mini/current_exit_state.jsonl"], []byte(`"TrailFloor":64920`)) {
		t.Fatal("読取可能な現在の出口条件が欠落")
	}
	var afterState string
	var afterEvents int
	_ = db.QueryRow("SELECT state FROM snapshot WHERE id=1").Scan(&afterState)
	_ = db.QueryRow("SELECT count(*) FROM events").Scan(&afterEvents)
	walAfter, err := os.ReadFile(path + "-wal")
	if err != nil || !bytes.Equal(walBefore, walAfter) || beforeState != afterState || beforeEvents != afterEvents {
		t.Fatal("正本を書き換えた", err)
	}
	info, _ := os.Stat(paths[0])
	t.Logf("旧イベント約%dバイト→ZIP%dバイト", len("巨大旧履歴")*200000, info.Size())
}

// TestExportMissingDoesNotCreateDatabase は不存在のDBを作らないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestExportMissingDoesNotCreateDatabase(t *testing.T) {
	cfg := diagnosticConfig(t)
	if _, err := Export(context.Background(), cfg); err == nil {
		t.Fatal("不存在を受理")
	}
	if _, err := os.Stat(filepath.Join(cfg.Root, "data")); !os.IsNotExist(err) {
		t.Fatal("dataを作成", err)
	}
}

// TestArchiveSplitKeepsEveryByte は圧縮しにくい大量原文も8MiB以内に分割し損なわないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestArchiveSplitKeepsEveryByte(t *testing.T) {
	a := &archives{dir: t.TempDir(), prefix: "人工", settings: []byte(`{}`), summary: map[string]any{"test": true}}
	defer a.abort()
	input := make([]byte, 19<<20)
	if _, err := rand.Read(input); err != nil {
		t.Fatal(err)
	}
	w := &chunkWriter{archive: a, name: "全約定.jsonl"}
	if _, err := w.Write(input); err != nil {
		t.Fatal(err)
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	if err := a.finish(true); err != nil {
		t.Fatal(err)
	}
	if len(a.paths) < 3 {
		t.Fatal("分割されていない")
	}
	files := readArchive(t, a.paths)
	if !bytes.Equal(input, files["全約定.jsonl"]) {
		t.Fatal("断片復元不一致")
	}
}

// TestExportLatestAndUnknownSession は最新場選択と未記録場の拒否を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestExportLatestAndUnknownSession(t *testing.T) {
	cfg := diagnosticConfig(t)
	diagnosticFixture(t, cfg)
	cfg.Startup.DiagnosticSession = ""
	paths, err := Export(context.Background(), cfg)
	if err != nil || len(paths) != 1 || !strings.Contains(paths[0], "2026-10-26_Day") {
		t.Fatal(paths, err)
	}
	cfg.Startup.DiagnosticSession = "2026-10-27_Day"
	if _, err = Export(context.Background(), cfg); err == nil {
		t.Fatal("未記録場を受理")
	}
}

// TestExportUnassignedEventDoesNotInventSession は場IDのない記録の時計だけで未記録場を作らないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。端末の実日付に依存しない人工イベントを使います。
func TestExportUnassignedEventDoesNotInventSession(t *testing.T) {
	cfg := diagnosticConfig(t)
	_, db, _ := diagnosticFixture(t, cfg)
	if _, err := db.Exec("INSERT INTO events(kind,session,code,correlation,at,payload) VALUES(?,?,?,?,?,?)", "台帳更新", "", "", "", "2026-11-03T10:00:00+09:00", `{"revision":100}`); err != nil {
		t.Fatal(err)
	}
	cfg.Startup.DiagnosticSession = "2026-11-03_Day"
	if _, err := Export(context.Background(), cfg); err == nil {
		t.Fatal("場のない補助イベントだけで未記録セッションを受理しました")
	}
	if _, err := db.Exec("INSERT INTO sessions(session,dryrun) VALUES(?,?)", cfg.Startup.DiagnosticSession, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), cfg); err != nil {
		t.Fatal("明示登録された無取引セッションが拒否されました", err)
	}
}

// TestOversizeSnapshotSkipsParsingButKeepsAllTrades は巨大旧状態を解析せず、当場約定は断片化して全量出すことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。構文解析を避けたことを確認するため、試験DBだけに巨大な不正JSONを挿入します。
func TestOversizeSnapshotSkipsParsingButKeepsAllTrades(t *testing.T) {
	cfg := diagnosticConfig(t)
	_, db, _ := diagnosticFixture(t, cfg)
	var first []byte
	if err := db.QueryRow("SELECT body FROM trades ORDER BY seq LIMIT 1").Scan(&first); err != nil {
		t.Fatal(err)
	}
	second := journal.Trade{ID: "大きな約定2", At: time.Date(2026, 10, 26, 11, 0, 0, 0, time.FixedZone("JST", 9*3600)), Session: "2026-10-26_Day", Symbol: "NK225mini", Product: "NK225mini", Owner: "core", TradeType: "close", Side: "sell", Quantity: 1, Price: 65300, PnL: 30000, Fee: 38}
	second.Evidence, _ = json.Marshal(map[string]string{"判断原文": strings.Repeat("境界を跨ぐ日本語", 100000)})
	body, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO trades(id,session,dryrun,body) VALUES(?,?,?,?)", second.ID, second.Session, false, body); err != nil {
		t.Fatal(err)
	}
	// ----------------------------------------
	// 正常な小さいJSONなら解析可能。閾値を超えた資料は構文が壊れていても解析を試みない。
	huge := append([]byte(`{"欠損した過去状態":"`), bytes.Repeat([]byte("x"), snapshotParseLimit+1)...)
	if _, err = db.Exec("PRAGMA ignore_check_constraints=ON"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE snapshot SET state=? WHERE id=1", huge); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO events(kind,session,code,correlation,at,payload) VALUES(?,?,?,?,?,?)", "台帳更新", "2026-10-26_Day", "NK225", "巨大台帳", second.At.Format(time.RFC3339Nano), huge); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA ignore_check_constraints=OFF"); err != nil {
		t.Fatal(err)
	}
	paths, err := Export(context.Background(), cfg)
	if err != nil {
		t.Fatal("巨大状態を構文解析して失敗しました", err)
	}
	files := readArchive(t, paths)
	want := append(append(append([]byte(nil), first...), '\n'), body...)
	want = append(want, '\n')
	if !bytes.Equal(files["NK225mini/trades.jsonl"], want) {
		t.Fatal("巨大状態に巻き込まれて当場約定原文が失われました")
	}
	for _, name := range []string{"current_snapshot_summary.json", "current_holdings.jsonl", "current_pending_orders.jsonl", "current_exit_state.jsonl"} {
		if !bytes.Contains(files["NK225mini/"+name], []byte(`"structure_parsing_skipped":true`)) || !bytes.Contains(files["NK225mini/"+name], []byte("ゼロを意味しません")) {
			t.Fatal("省略を空の所有状態と区別しません", name)
		}
	}
	if !bytes.Contains(files["NK225mini/events.jsonl"], []byte(`"structure_parsing_skipped":1`)) || !bytes.Contains(files["summary.json"], []byte(`"oversize_ledger_events_not_parsed":1`)) {
		t.Fatal("巨大台帳の構文解析省略が追跡できません")
	}
}
