package journal

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// summaryRows は終了CSVのBOMを除き、正式なCSV引用規則で読み取ります。
//
// 引数: t(*testing.T)、path(string)。返り値: [][]stringは見出しを含む全行です。
func summaryRows(t *testing.T, path string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// reportTestEnd は試験場の正式終了時計を返します。
//
// 引数: なし。返り値: time.Time。強制返済予定時計とは別です。
func reportTestEnd() time.Time {
	return time.Date(2026, 10, 21, 15, 45, 0, 0, time.FixedZone("JST", 9*3600))
}

// saveReportTrades は実行器と同じ最小snapshotを約定正本と保存します。
//
// 引数: t(*testing.T)、s(*Store)、trades([]Trade)、status(string)、ack(int)。返り値: なし。
func saveReportTrades(t *testing.T, s *Store, trades []Trade, status string, ack int) {
	t.Helper()
	state := map[string]any{"ack_count": ack, "orders": []map[string]string{{"status": status}}, "holdings": map[string]any{"管理外": map[string]int{"quantity": 1}}}
	if err := s.Save(state, trades); err != nil {
		t.Fatal(err)
	}
}

// requireNoReport は未確定場がCSVを生成していないことを検査します。
//
// 引数: t(*testing.T)、root(string)。返り値: なし。
func requireNoReport(t *testing.T, root string) {
	t.Helper()
	rows, err := ReadSessionPL(root)
	if err != nil || len(rows) != 0 {
		t.Fatal("未確定場を表示しました", rows, err)
	}
	if _, err = os.Stat(filepath.Join(root, "TOPIXmini", "2026-10-21_Day_dryrun.csv")); !os.IsNotExist(err) {
		t.Fatal("未確定場の約定CSVを作りました", err)
	}
}

// TestReportsColumnsGrossAndMethods は四売買種別・手数料分離・当時の手法補完を照合します。
//
// 引数: t(*testing.T)。返り値: なし。現在設定から過去手法を推測しません。
func TestReportsColumnsGrossAndMethods(t *testing.T) {
	s, _, _ := testStore(t)
	root := t.TempDir()
	end := reportTestEnd()
	trades := []Trade{testTrade("買新"), testTrade("売返"), testTrade("売新"), testTrade("買返")}
	trades[0].MethodID = "TOPIX_T04"
	trades[0].PnL = 999
	trades[1].TradeType, trades[1].Side, trades[1].PnL = "close", "sell", 1250
	trades[1].Evidence = json.RawMessage(`{"decision":{"rules_profile":"s01"}}`)
	trades[2].Side = "sell"
	trades[2].Evidence = json.RawMessage(`{"rules_profile":"p05"}`)
	trades[3].TradeType, trades[3].Side, trades[3].PnL = "close", "buy", -250
	for i := range trades {
		trades[i].Fee = 100
		trades[i].Quantity = i + 1
	}
	saveReportTrades(t, s, trades, "done", len(trades))
	if err := s.RegisterReportSession(trades[0].Session, true, "TOPIXmini", end); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeReports(root, end.Add(-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	requireNoReport(t, root)
	if err := s.FinalizeReports(root, end); err != nil {
		t.Fatal(err)
	}
	rows := summaryRows(t, filepath.Join(root, "TOPIXmini", trades[0].Session+"_dryrun.csv"))
	if len(rows) != 5 || !reflect.DeepEqual(rows[0], reportTradeHeader) {
		t.Fatal(rows)
	}
	for i, want := range []string{"LongN", "LongR", "ShortN", "ShortR"} {
		if rows[i+1][0] != want || len(rows[i+1]) != 6 || rows[i+1][3] != trades[i].At.Format(time.RFC3339Nano) {
			t.Fatal(rows[i+1])
		}
	}
	if rows[1][4] != "0" || rows[2][4] != "+1250" || rows[4][4] != "-250" || rows[1][5] != "TOPIX_T04" || rows[2][5] != "NK225_S01" || rows[3][5] != "NK225_P05" || rows[4][5] != "不明" {
		t.Fatal(rows)
	}
	pl, err := ReadSessionPL(root)
	if err != nil || len(pl) != 1 || pl[0].Profit != 1250 || pl[0].Loss != 250 || pl[0].Net != 1000 || pl[0].EntryQty != 4 {
		t.Fatal(pl, err)
	}
	if err = s.FinalizeReports(root, end.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadSessionPL(root); !reflect.DeepEqual(got, pl) {
		t.Fatal("重複終了で変化", got)
	}
}

// TestReportsPendingAndAcknowledgement は終了時刻だけで未照合注文・未反映約定を確定しません。
//
// 引数: t(*testing.T)。返り値: なし。取消受付や結果不明も延期します。
func TestReportsPendingAndAcknowledgement(t *testing.T) {
	s, _, _ := testStore(t)
	root := t.TempDir()
	trade := testTrade("確認待ち")
	if err := s.RegisterReportSession(trade.Session, true, trade.Product, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"prepared", "submitted", "accepted", "unknown", "cancel_requested", ""} {
		saveReportTrades(t, s, []Trade{trade}, status, 1)
		if err := s.FinalizeReports(root, reportTestEnd()); err != nil {
			t.Fatal(err)
		}
		requireNoReport(t, root)
	}
	saveReportTrades(t, s, []Trade{trade}, "done", 0)
	if err := s.FinalizeReports(root, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	requireNoReport(t, root)
	saveReportTrades(t, s, []Trade{trade}, "rejected", 1)
	if err := s.FinalizeReports(root, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	if rows, err := ReadSessionPL(root); err != nil || len(rows) != 1 || rows[0].EntryQty != 5 {
		t.Fatal(rows, err)
	}
}

// TestReportsUnsentRuntimeQueue は上流未送信の新規・返済判断でも終了確定を延期します。
//
// 引数: t(*testing.T)。返り値: なし。停止処理で待ち列が解消された後だけ確定します。
func TestReportsUnsentRuntimeQueue(t *testing.T) {
	for _, action := range []string{"new", "close"} {
		t.Run(action, func(t *testing.T) {
			s, path, _ := testStore(t)
			root := t.TempDir()
			if err := s.RegisterReportSession("2026-10-21_Day", true, "TOPIXmini", reportTestEnd()); err != nil {
				t.Fatal(err)
			}
			state := map[string]any{"ack_count": 0, "orders": []any{}, "runtime_state": map[string]any{"Queue": []any{map[string]any{"Signal": map[string]string{"Action": action}}}}}
			if err := s.Save(state, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.FinalizeReports(root, reportTestEnd()); err != nil {
				t.Fatal(err)
			}
			requireNoReport(t, root)
			if err := FinalizeStoredReports(path, root, reportTestEnd()); err != nil {
				t.Fatal(err)
			}
			requireNoReport(t, root)
			state["runtime_state"] = map[string]any{"Queue": []any{}}
			if err := s.Save(state, nil); err != nil {
				t.Fatal(err)
			}
			if err := FinalizeStoredReports(path, root, reportTestEnd()); err != nil {
				t.Fatal(err)
			}
			if rows, err := ReadSessionPL(root); err != nil || len(rows) != 1 || rows[0].EntryQty != 0 {
				t.Fatal("待ち列解消後の無取引確定", rows, err)
			}
		})
	}
}

// TestReportsRestartDeletionAndLateFill は出力済み印を再起動後も維持し、削除を尊重します。
//
// 引数: t(*testing.T)。返り値: なし。遅着分は現存するCSVだけ訂正します。
func TestReportsRestartDeletionAndLateFill(t *testing.T) {
	s, path, logs := testStore(t)
	root := t.TempDir()
	trades := []Trade{testTrade("初回")}
	saveReportTrades(t, s, trades, "done", 1)
	if err := s.RegisterReportSession(trades[0].Session, true, "TOPIXmini", reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeReports(root, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.RegisterReportSession(trades[0].Session, true, "TOPIXmini", reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	late := testTrade("遅着1")
	late.TradeType, late.Side, late.PnL = "close", "sell", 300
	trades = append(trades, late)
	saveReportTrades(t, reopened, trades, "done", 2)
	if err = reopened.FinalizeReports(root, reportTestEnd().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rows, _ := ReadSessionPL(root); len(rows) != 1 || rows[0].Net != 300 {
		t.Fatal(rows)
	}
	tradePath := filepath.Join(root, "TOPIXmini", trades[0].Session+"_dryrun.csv")
	if err = os.Remove(tradePath); err != nil {
		t.Fatal(err)
	}
	late.ID = "遅着2"
	late.ExecutionID = late.ID
	late.PnL = -100
	trades = append(trades, late)
	saveReportTrades(t, reopened, trades, "done", 3)
	if err = reopened.FinalizeReports(root, reportTestEnd().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(tradePath); !os.IsNotExist(err) {
		t.Fatal("削除明細を復元", err)
	}
	if rows, _ := ReadSessionPL(root); len(rows) != 1 || rows[0].Net != 200 {
		t.Fatal(rows)
	}
	if err = os.Remove(filepath.Join(root, "sessionPL.csv")); err != nil {
		t.Fatal(err)
	}
	late.ID = "遅着3"
	late.ExecutionID = late.ID
	trades = append(trades, late)
	saveReportTrades(t, reopened, trades, "done", 4)
	if err = reopened.FinalizeReports(root, reportTestEnd().Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	requireNoReport(t, root)
	if err = FinalizeStoredReports(path, root, reportTestEnd().Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	requireNoReport(t, root)
}

// TestReportsWriteRetryAndMarkerCrash は二ファイル間の失敗とDB印直前の中断を再試行します。
//
// 引数: t(*testing.T)。返り値: なし。成功済みCSVを二重追記しません。
func TestReportsWriteRetryAndMarkerCrash(t *testing.T) {
	s, _, _ := testStore(t)
	root := t.TempDir()
	trade := testTrade("一度だけ")
	saveReportTrades(t, s, []Trade{trade}, "done", 1)
	if err := s.RegisterReportSession(trade.Session, true, trade.Product, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(root, "sessionPL.csv")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeReports(root, reportTestEnd()); err == nil {
		t.Fatal("書込み失敗を成功にした")
	}
	if scalar(t, s, "SELECT SUM(emitted) FROM report_sessions") != 0 {
		t.Fatal("失敗を出力済みにした")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeReports(root, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	// ----------------------------------------
	// CSVは同期済み、SQLiteの確定だけが中断した状態を再現します。
	if _, err := s.db.Exec("UPDATE report_sessions SET emitted=0,last_trade_seq=0"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeReports(root, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	if rows := summaryRows(t, blocked); len(rows) != 2 {
		t.Fatal("集計重複", rows)
	}
	if rows := summaryRows(t, filepath.Join(root, trade.Product, trade.Session+"_dryrun.csv")); len(rows) != 2 {
		t.Fatal("明細重複", rows)
	}
}

// TestReportsConcurrentProductsAndModes は別DBから共通集計へ書く同時商品を検査します。
//
// 引数: t(*testing.T)。返り値: なし。無取引も登録した場だけ0行として残します。
func TestReportsConcurrentProductsAndModes(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	errors := make(chan error, 12)
	for _, product := range []string{"NK225mini", "NK225micro", "TOPIXmini"} {
		for _, dry := range []bool{false, true} {
			s, _, _ := testStore(t)
			if err := s.RegisterReportSession("2026-10-21_Day", dry, product, reportTestEnd()); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func(s *Store) { defer wg.Done(); errors <- s.FinalizeReports(root, reportTestEnd()) }(s)
			}
		}
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := ReadSessionPL(root)
	if err != nil || len(rows) != 6 {
		t.Fatal(rows, err)
	}
	for _, v := range rows {
		if v.Net != 0 || v.EntryQty != 0 {
			t.Fatal(v)
		}
	}
}

// TestReportsInactiveExistingDBOnly は停止商品処理でDBや旧監査CSVを勝手に生成しません。
//
// 引数: t(*testing.T)。返り値: なし。既登録場だけを直接DBから確定します。
func TestReportsInactiveExistingDBOnly(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "新レポート")
	missing := filepath.Join(dir, "存在しない.sqlite")
	if err := FinalizeStoredReports(missing, root, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("未作成DBが生じた", err)
	}
	path, logs := filepath.Join(dir, "日本語 # DB.sqlite"), filepath.Join(dir, "詳細監査")
	s, err := Open(path, logs)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterReportSession("2026-10-21_Day", false, "NK225mini", reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(logs, "trace.jsonl")
	assertLegacyLogAbsent(t, logs)
	if err = FinalizeStoredReports(path, root, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(trace); !os.IsNotExist(err) {
		t.Fatal("inactive処理が監査ログを復元", err)
	}
	if rows, e := ReadSessionPL(root); e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
}

// TestReportsReadOnlyValidation は欠損と破損を区別し、読取でファイルを作りません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestReportsReadOnlyValidation(t *testing.T) {
	root := t.TempDir()
	if rows, err := ReadSessionPL(root); err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatal("読取がファイルを作成")
	}
	for _, rows := range [][][]string{
		{{"NaN", "0", "0", "0", "2026-10-21_Day", "NK225mini", "live"}},
		{{"10", "0", "9", "0", "2026-10-21_Day", "NK225mini", "live"}},
		{{"0", "0", "0", "0", "2026-10-21_Day", "NK225mini", "live"}, {"0", "0", "0", "0", "2026-10-21_Day", "NK225mini", "live"}},
	} {
		raw, _ := reportCSV(reportPLHeader, rows)
		if err := os.WriteFile(filepath.Join(root, "sessionPL.csv"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSessionPL(root); err == nil {
			t.Fatal("不正集計を受理", rows)
		}
	}
}

// TestReportsUnchangedMetadataOnly は出力不要時に約定本文とファイル排他へ触れません。
//
// 引数: t(*testing.T)。返り値: なし。終了前と出力済み無変更を検査し、遅着時は通常処理へ戻ります。
func TestReportsUnchangedMetadataOnly(t *testing.T) {
	s, path, _ := testStore(t)
	root := t.TempDir()
	trade := testTrade("大きな本文の代用")
	saveReportTrades(t, s, []Trade{trade}, "done", 1)
	if err := s.RegisterReportSession(trade.Session, true, trade.Product, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	valid, _ := json.Marshal(trade)
	if _, err := s.db.Exec("UPDATE trades SET body=?", `"約定型としては不正だがJSONとしては有効"`); err != nil {
		t.Fatal(err)
	}
	// 出力先自体が通常ファイルでも、出力不要ならディレクトリ作成を試みません。
	blocked := filepath.Join(root, "出力禁止")
	if err := os.WriteFile(blocked, []byte("そのまま"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeReports(blocked, reportTestEnd().Add(-time.Nanosecond)); err != nil {
		t.Fatal("終了前に本文又は排他を使用", err)
	}
	if err := FinalizeStoredReports(path, blocked, reportTestEnd().Add(-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeReports(root, reportTestEnd()); err == nil {
		t.Fatal("出力候補の不正本文を見逃した")
	}
	if _, err := s.db.Exec("UPDATE trades SET body=?", valid); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeReports(root, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE trades SET body=?", `"本文を読めば必ず型エラー"`); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, ".sessionPL.lock")
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeReports(root, reportTestEnd().Add(time.Hour)); err != nil {
		t.Fatal("出力済み無変更で本文を使用", err)
	}
	if err := FinalizeStoredReports(path, root, reportTestEnd().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatal("出力済み無変更で排他ファイルを作成", err)
	}
	if _, err := s.db.Exec("UPDATE trades SET body=?", valid); err != nil {
		t.Fatal(err)
	}
	late := testTrade("新連番")
	late.TradeType, late.Side, late.PnL = "close", "sell", 75
	saveReportTrades(t, s, []Trade{trade, late}, "done", 2)
	if err := s.FinalizeReports(root, reportTestEnd().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rows, err := ReadSessionPL(root); err != nil || len(rows) != 1 || rows[0].Net != 75 {
		t.Fatal("遅着の更新候補を無視", rows, err)
	}
}

// TestReportsOSLock は別プロセスとの排他とプロセス終了時のOS解放を検査します。
//
// 引数: t(*testing.T)。返り値: なし。ロック用ファイルの残置は再取得を妨げません。
func TestReportsOSLock(t *testing.T) {
	if root := os.Getenv("SESSION_REPORT_LOCK_TEST"); root != "" {
		f, err := acquireReportFileLock(root)
		if err != nil {
			os.Exit(3)
		}
		_ = f
		os.Exit(0)
	}
	root := t.TempDir()
	f, err := acquireReportFileLock(root)
	if err != nil {
		t.Fatal(err)
	}
	run := func() error {
		cmd := exec.Command(os.Args[0], "-test.run=^TestReportsOSLock$")
		cmd.Env = append(os.Environ(), "SESSION_REPORT_LOCK_TEST="+root)
		return cmd.Run()
	}
	if err = run(); err == nil {
		t.Fatal("他プロセスが同時取得")
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = run(); err != nil {
		t.Fatal("残置ファイルで取得不可", err)
	}
	f, err = acquireReportFileLock(root)
	if err != nil {
		t.Fatal("プロセス終了後に未解放", err)
	}
	f.Close()
}
