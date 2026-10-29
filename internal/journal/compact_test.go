package journal

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCompactPayloadKeepsDecisions は発注根拠と約定を丸ごと残し、資料の省略を明示することを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestCompactPayloadKeepsDecisions(t *testing.T) {
	large, err := json.Marshal(map[string]any{"count": 3, "state": map[string]string{"保有": strings.Repeat("保持", 100000)}, "raw": map[string]int{"価格": 65000}, "reason": "利益を伸ばす"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"発注判断", "約定確定"} {
		got, err := CompactPayload(kind, large)
		if err != nil || !bytes.Equal(got, large) {
			t.Fatal("売買の判断根拠を省略しました", kind, err)
		}
	}
	got, err := CompactPayload("台帳更新", large)
	if err != nil || len(got) >= len(large)/100 {
		t.Fatal("巨大状態を十分に軽量化できません", len(got), err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(got, &fields); err != nil || string(fields["reason"]) != `"利益を伸ばす"` || string(fields["count"]) != "3" || !alreadyOmitted(fields["state"]) || !alreadyOmitted(fields["raw"]) || len(fields["_trace_compaction"]) == 0 {
		t.Fatal("省略と判断の区別が不正です", string(got), err)
	}
	second, err := CompactPayload("台帳更新", got)
	if err != nil || !bytes.Equal(got, second) {
		t.Fatal("二重適用が元資料の指紋を変えました", err)
	}
	for _, bad := range []json.RawMessage{nil, []byte(`{"壊れた":`)} {
		if _, err = CompactPayload("台帳更新", bad); err == nil {
			t.Fatal("不正JSONを受理しました")
		}
	}
	for _, raw := range []json.RawMessage{[]byte("null"), []byte("true"), []byte("13.5"), []byte(`"短い記録"`)} {
		got, err = CompactPayload("補助", raw)
		if err != nil || !bytes.Equal(raw, got) {
			t.Fatal("短い値を変更しました", err)
		}
	}
}

// TestCompactArchiveRuntimeDoesNotExpand は起動切替の旧runtimeを派生traceへ巨大複製しないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestCompactArchiveRuntimeDoesNotExpand(t *testing.T) {
	runtime := map[string]string{"履歴": strings.Repeat("旧runtime全量", 100000)}
	raw, err := json.Marshal(map[string]any{"previous_state": map[string]any{"runtime_state": runtime}, "metadata": map[string]any{"reason": "新しい運転", "previous_runtime": runtime}})
	if err != nil {
		t.Fatal(err)
	}
	compact, err := CompactPayload("起動時状態切替", raw)
	if err != nil || len(compact) > 4096 || bytes.Contains(compact, []byte("旧runtime全量")) {
		t.Fatal("起動切替のruntimeを派生へ再展開しました", len(compact), err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(compact, &fields); err != nil || !alreadyOmitted(fields["previous_state"]) || !alreadyOmitted(fields["metadata"]) {
		t.Fatal("切替資料の省略を識別できません", err)
	}
}

// TestSnapshotAndLegacyFilesRemainUnchanged は旧全量イベントと既存ファイルを再起動で変更しません。
//
// 引数: t(*testing.T)。返り値: なし。通常の軽量trace再生成も廃止しています。
func TestSnapshotAndLegacyFilesRemainUnchanged(t *testing.T) {
	s, path, logs := testStore(t)
	state := map[string]any{"runtime": strings.Repeat("全量復元用", 100000), "保有": 2}
	trade := testTrade("大きな状態の約定")
	if err := s.Save(state, []Trade{trade}); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := s.db.QueryRow("SELECT payload FROM events WHERE kind='台帳更新'").Scan(&payload); err != nil || len(payload) > 1024 || bytes.Contains(payload, []byte(`"state":`)) {
		t.Fatal("全状態の不要な複製", len(payload), err)
	}
	if err := s.Event("台帳更新", "", "", "", time.Now(), map[string]any{"revision": 1, "state": state}); err != nil {
		t.Fatal(err)
	}
	queries := map[string]string{"snapshot": "SELECT state FROM snapshot ORDER BY id", "trades": "SELECT body FROM trades ORDER BY seq", "events": "SELECT payload FROM events ORDER BY seq"}
	fingerprints := map[string]string{}
	for name, query := range queries {
		fingerprints[name], _ = rowDigest(t, s, query)
	}
	assertLegacyLogAbsent(t, logs)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	old := []byte("旧traceも変更・削除しない")
	if err := os.WriteFile(filepath.Join(logs, "trace.jsonl"), old, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	for name, query := range queries {
		got, _ := rowDigest(t, restored, query)
		if got != fingerprints[name] {
			t.Fatal("正本変更", name)
		}
	}
	after, err := os.ReadFile(filepath.Join(logs, "trace.jsonl"))
	if err != nil || !bytes.Equal(after, old) {
		t.Fatal("旧trace変更", err)
	}
	var loaded map[string]any
	fills, err := restored.Load(&loaded)
	wanted, _ := json.Marshal([]Trade{trade})
	actual, _ := json.Marshal(fills)
	if err != nil || loaded["runtime"] != state["runtime"] || !bytes.Equal(wanted, actual) {
		t.Fatal("正本復元差", err)
	}
}

// TestArchiveAndSaveAtomic は運転切替の監査と新状態が同時確定し、旧約定が不変なことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestArchiveAndSaveAtomic(t *testing.T) {
	s, path, logs := testStore(t)
	previous := map[string]any{"run": "旧運転", "holdings": 2}
	trades := []Trade{testTrade("旧約定")}
	if err := s.Save(previous, trades); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := s.db.QueryRow("SELECT state FROM snapshot").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER fail_transition BEFORE UPDATE ON snapshot BEGIN SELECT RAISE(ABORT,'切替中断'); END"); err != nil {
		t.Fatal(err)
	}
	next := map[string]any{"run": "新運転", "holdings": 0}
	if err := s.ArchiveAndSave(next, trades, map[string]string{"reason": "起動"}); err == nil {
		t.Fatal("失敗した状態切替を受理しました")
	}
	if scalar(t, s, "SELECT count(*) FROM events WHERE kind='起動時状態切替'") != 0 {
		t.Fatal("失敗した切替の監査だけが確定しました")
	}
	var after []byte
	if err := s.db.QueryRow("SELECT state FROM snapshot").Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("切替中断で旧状態を失いました", err)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_transition"); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveAndSave(next, nil, nil); err == nil {
		t.Fatal("切替で過去約定を削除できました")
	}
	if err := s.ArchiveAndSave(next, trades, map[string]string{"reason": "起動"}); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := s.db.QueryRow("SELECT payload FROM events WHERE kind='起動時状態切替'").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var archive map[string]json.RawMessage
	if err := json.Unmarshal(payload, &archive); err != nil || !bytes.Equal(archive["previous_state"], before) {
		t.Fatal("旧状態が監査へ保存されていません", err)
	}
	_ = s.Close()
	restored, err := Open(path, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var loaded map[string]any
	fills, err := restored.Load(&loaded)
	wantedFills, _ := json.Marshal(trades)
	actualFills, _ := json.Marshal(fills)
	if err != nil || loaded["run"] != "新運転" || !bytes.Equal(wantedFills, actualFills) {
		t.Fatal("再起動で切替先状態または約定が変わりました", err)
	}
	assertLegacyLogAbsent(t, logs)
}

// rowDigest はBLOB原文を行順に読み、正本の変更を検出する指紋を返します。
//
// 引数: t(*testing.T)、s(*Store)、query(string)は単一BLOB列の読取SQLです。
// 返り値: stringはSHA256、int64は全BLOBの合計バイト数です。
func rowDigest(t *testing.T, s *Store, query string) (string, int64) {
	t.Helper()
	rows, err := s.db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	hash, size := sha256.New(), int64(0)
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		_, _ = hash.Write(body)
		_, _ = hash.Write([]byte{0})
		size += int64(len(body))
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil)), size
}

// TestLegacyFixtureStorage は任意指定した作業用コピーで四正本不変と無出力を確認します。
//
// 引数: t(*testing.T)。返り値: なし。SESSIONTRADER_COMPACT_FIXTUREは原DBではなく作業用コピーです。
func TestLegacyFixtureStorage(t *testing.T) {
	path := os.Getenv("SESSIONTRADER_COMPACT_FIXTURE")
	if path == "" {
		t.Skip("旧DBの作業コピー指定なし")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	baseline := &Store{db: db}
	queries := map[string]string{"snapshot": "SELECT state FROM snapshot ORDER BY id", "trades": "SELECT body FROM trades ORDER BY seq", "books": "SELECT raw FROM books ORDER BY id", "events": "SELECT payload FROM events ORDER BY seq"}
	before := map[string]string{}
	for table, query := range queries {
		before[table], _ = rowDigest(t, baseline, query)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(t.TempDir(), "未生成ログ")
	s, err := Open(path, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for table, query := range queries {
		after, _ := rowDigest(t, s, query)
		if after != before[table] {
			t.Fatal("正本変更", table)
		}
	}
	assertLegacyLogAbsent(t, logs)
}
