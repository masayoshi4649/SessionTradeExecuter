package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testStore は各試験専用のSQLiteと未作成の旧ログ先を用意します。
//
// 引数: t(*testing.T)。返り値: *Store、string、stringは保存器・DBパス・未使用ログ先です。
func testStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	path, logs := filepath.Join(dir, "state.sqlite"), filepath.Join(dir, "log")
	s, err := Open(path, logs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path, logs
}

// testTrade は所有担当と判断根拠を持つ仮想約定を返します。
//
// 引数: id(string)。返り値: Tradeは試験用の一約定です。
func testTrade(id string) Trade {
	return Trade{ID: id, At: time.Date(2026, 10, 21, 9, 0, 0, 0, time.FixedZone("JST", 9*3600)),
		Session: "2026-10-21_Day", Symbol: "TOPIXmini", Product: "TOPIXmini", Owner: "core",
		TradeType: "new", Side: "buy", Quantity: 5, Price: 4000.5, DryRun: true,
		Reason: "理由,改行\nあり", Evidence: json.RawMessage(`{"input_id":10,"peer_input_id":9}`),
		ClientOrderID: "intent-1", BrokerOrderID: "order-1", ExecutionID: id}
}

// scalar は試験用SQLの一整数結果を取得します。
//
// 引数: t(*testing.T)、s(*Store)、query(string)。返り値: intは整数結果です。
func scalar(t *testing.T, s *Store, query string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// assertLegacyLogAbsent は通常保存で旧ログ先すら生成しないことを検査します。
//
// 引数: t(*testing.T)、path(string)。返り値: なし。
func assertLegacyLogAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("旧ログ先を生成しました", path, err)
	}
}

// TestSaveReopenWithoutFileProjection は正本の復元と既存ファイルの非変更を確認します。
//
// 引数: t(*testing.T)。返り値: なし。旧ログを作らず、残置・削除のどちらも尊重します。
func TestSaveReopenWithoutFileProjection(t *testing.T) {
	s, path, logs := testStore(t)
	if scalar(t, s, "PRAGMA synchronous") != 2 {
		t.Fatal("FULL同期ではありません")
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatal(mode, err)
	}
	if err := s.EnsureSession("2026-10-21_Day", true); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"status": "sending", "intent": "intent-1"}
	trade := testTrade("fill-1")
	if err := s.Save(state, []Trade{trade}); err != nil {
		t.Fatal(err)
	}
	count := scalar(t, s, "SELECT count(*) FROM events")
	if err := s.Save(state, []Trade{trade}); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s, "SELECT count(*) FROM events") != count || scalar(t, s, "SELECT count(*) FROM trades") != 1 {
		t.Fatal("同一保存が増殖")
	}
	assertLegacyLogAbsent(t, logs)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	names := []string{"2026-10-21_Day_dryrun.csv", "2026-10-21_Day_dryrun.jsonl", "trace.jsonl", "session_summary_dryrun.csv", "session_summary.csv"}
	old := []byte("利用者が保持する旧出力。途中で切れていても変更しない")
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(logs, name), old, 0600); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := Open(path, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var loaded map[string]any
	fills, err := restored.Load(&loaded)
	if err != nil || len(fills) != 1 || loaded["status"] != "sending" || fills[0].Owner != "core" || fills[0].Reason != trade.Reason {
		t.Fatal("正本復元失敗", err)
	}
	if err = restored.Save(state, fills); err != nil {
		t.Fatal(err)
	}
	if err = restored.Event("再起動確認", "", "", "", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		got, e := os.ReadFile(filepath.Join(logs, name))
		if e != nil || !bytes.Equal(got, old) {
			t.Fatal("旧出力の変更又は削除", name, e)
		}
	}
	if err = os.Remove(filepath.Join(logs, "trace.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	assertLegacyLogAbsent(t, filepath.Join(logs, "trace.jsonl"))
	if scalar(t, again, "SELECT count(*) FROM events") != count+1 {
		t.Fatal("監査イベントの消失")
	}
}

// TestAtomicRollbackAndDuplicate は状態・約定・履歴が同時に確定することを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestAtomicRollbackAndDuplicate(t *testing.T) {
	s, _, _ := testStore(t)
	before := map[string]int{"stage": 1}
	if err := s.Save(before, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER fail_trade BEFORE INSERT ON trades WHEN NEW.id='fail' BEGIN SELECT RAISE(ABORT,'試験中断'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(map[string]int{"stage": 2}, []Trade{testTrade("ok"), testTrade("fail")}); err == nil {
		t.Fatal("挿入中断を見逃した")
	}
	var loaded map[string]int
	fills, err := s.Load(&loaded)
	if err != nil || loaded["stage"] != 1 || len(fills) != 0 || scalar(t, s, "SELECT count(*) FROM events") != 1 {
		t.Fatal("部分確定", err)
	}
	trade := testTrade("fill-1")
	if err = s.Save(before, []Trade{trade}); err != nil {
		t.Fatal(err)
	}
	conflict := trade
	conflict.Quantity = 4
	for _, items := range [][]Trade{nil, {conflict}, {trade, trade}} {
		if err = s.Save(map[string]int{"stage": 9}, items); err == nil {
			t.Fatal("確定済み約定の消失・変更・重複を許可")
		}
	}
	fills, err = s.Load(&loaded)
	if err != nil || loaded["stage"] != 1 || len(fills) != 1 || fills[0].Quantity != 5 {
		t.Fatal("拒否後の正本変更", err)
	}
}

// TestLegacyLogFailureCannotBlockDB は使用不能な旧ログ先でもDBの全保存機能が動くことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。旧パスを書かず、ファイルとして存在しても上書きしません。
func TestLegacyLogFailureCannotBlockDB(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "旧ログ先")
	old := []byte("書換え禁止")
	if err := os.WriteFile(logs, old, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(dir, "db", "state.sqlite"), logs)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.EnsureProductSession("2026-10-21_Day", true, "TOPIXmini"); err != nil {
		t.Fatal(err)
	}
	if err = s.Save(map[string]int{"stage": 1}, []Trade{testTrade("fill")}); err != nil {
		t.Fatal(err)
	}
	if err = s.Event("判断", "2026-10-21_Day", "TOPIX", "", time.Now(), map[string]int{"既知": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordBook("2026-10-21_Day", "g", "TOPIX", time.Now(), json.RawMessage(`{"CurrentPrice":4000}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.ArchiveAndSave(map[string]int{"stage": 2}, []Trade{testTrade("fill")}, map[string]string{"理由": "再起動"}); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s, "SELECT count(*) FROM trades") != 1 || scalar(t, s, "SELECT count(*) FROM books") != 1 || scalar(t, s, "SELECT count(*) FROM events WHERE kind='起動時状態切替'") != 1 {
		t.Fatal("DB記録が不足")
	}
	got, err := os.ReadFile(logs)
	if err != nil || !bytes.Equal(got, old) {
		t.Fatal("旧パスへ書込み", err)
	}
	empty, err := Open(filepath.Join(dir, "empty.sqlite"), "")
	if err != nil {
		t.Fatal("未使用の旧引数を必須にした", err)
	}
	empty.Close()
}

// TestBookBatchAndEvent は同原文の別受信・原文保持・バッチ原子性を確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestBookBatchAndEvent(t *testing.T) {
	s, _, logs := testStore(t)
	raw := json.RawMessage(" { \"CurrentPrice\" : 4000.5 } ")
	record := BookRecord{Session: "2026-10-21_Day", Generation: "g1", Code: "TOPIX", Received: time.Now(), Source: "履歴", Raw: raw}
	ids, err := s.RecordBooks([]BookRecord{record, record})
	if err != nil || len(ids) != 2 || ids[1] != ids[0]+1 {
		t.Fatal("別受信消失", err)
	}
	var restored []byte
	if err = s.db.QueryRow("SELECT raw FROM books WHERE id=?", ids[0]).Scan(&restored); err != nil || !bytes.Equal(raw, restored) {
		t.Fatal("原文変更", err)
	}
	if _, err = s.db.Exec("CREATE TRIGGER fail_book BEFORE INSERT ON books WHEN NEW.code='BAD' BEGIN SELECT RAISE(ABORT,'試験中断'); END"); err != nil {
		t.Fatal(err)
	}
	bad := record
	bad.Code = "BAD"
	if ids, err = s.RecordBooks([]BookRecord{record, bad}); err == nil || len(ids) != 0 {
		t.Fatal("未確定IDを返した")
	}
	if scalar(t, s, "SELECT count(*) FROM books") != 2 || scalar(t, s, "SELECT count(*) FROM events") != 1 {
		t.Fatal("バッチ部分確定")
	}
	if err = s.Event("入口見送り", record.Session, record.Code, "decision-1", time.Now(), map[string]any{"input_id": 2, "reason": "相手市場の逆行"}); err != nil {
		t.Fatal(err)
	}
	var correlation string
	if err = s.db.QueryRow("SELECT correlation FROM events WHERE seq=2 AND kind='入口見送り'").Scan(&correlation); err != nil || correlation != "decision-1" {
		t.Fatal("イベント追跡失敗", err)
	}
	record.Raw = json.RawMessage(`{"途中":`)
	if _, err = s.RecordBooks([]BookRecord{record}); err == nil {
		t.Fatal("不正原文を受理")
	}
	if err = s.Event("不正内容", "", "", "", time.Now(), make(chan int)); err == nil {
		t.Fatal("不正内容を受理")
	}
	if scalar(t, s, "SELECT count(*) FROM events") != 2 {
		t.Fatal("不正イベントを保存")
	}
	assertLegacyLogAbsent(t, logs)
}

// TestConcurrentRecords は同時受信でも入力ID・履歴順が衝突しないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestConcurrentRecords(t *testing.T) {
	s, _, logs := testStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.RecordBook("2026-10-21_Day", "g1", "NK225", time.Now(), json.RawMessage(`{"CurrentPrice":65000}`))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if scalar(t, s, "SELECT count(*) FROM books") != 20 || scalar(t, s, "SELECT count(*) FROM events") != 20 {
		t.Fatal("同時受信の欠落")
	}
	assertLegacyLogAbsent(t, logs)
}

// TestCrashCommittedState はCloseなしのプロセス終了後も送信途中状態を復元します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestCrashCommittedState(t *testing.T) {
	dir := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
	command.Env = append(os.Environ(), "SESSIONTRADER_JOURNAL_CRASH="+dir)
	err := command.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatal("意図しない終了", err)
	}
	s, err := Open(filepath.Join(dir, "state.sqlite"), filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var state map[string]string
	fills, err := s.Load(&state)
	if err != nil || state["status"] != "sending" || state["intent"] != "fixed-intent" || len(fills) != 0 {
		t.Fatal("復元失敗", err)
	}
	assertLegacyLogAbsent(t, filepath.Join(dir, "log"))
}

// TestCrashHelper は別プロセス内で確定直後に終了する試験専用入口です。
//
// 引数: t(*testing.T)。返り値: なし。専用環境変数がなければ実行しません。
func TestCrashHelper(t *testing.T) {
	dir := os.Getenv("SESSIONTRADER_JOURNAL_CRASH")
	if dir == "" {
		return
	}
	s, err := Open(filepath.Join(dir, "state.sqlite"), filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Save(map[string]string{"status": "sending", "intent": "fixed-intent"}, nil); err != nil {
		t.Fatal(err)
	}
	os.Exit(23)
}

// TestInvalidSessionAndVersion は不正な場IDおよび未来形式のDBを拒否します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestInvalidSessionAndVersion(t *testing.T) {
	s, path, logs := testStore(t)
	if err := s.EnsureSession("../escape", false); err == nil {
		t.Fatal("不正な場IDを許可")
	}
	if _, err := s.db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := Open(path, logs); err == nil || !strings.Contains(err.Error(), "未対応") {
		t.Fatal("未知形式を受理", err)
	}
}
