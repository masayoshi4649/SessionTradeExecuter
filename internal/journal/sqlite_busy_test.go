package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// sqliteErrorCode はドライバのSQLite拡張エラー番号を取得します。
//
// 引数: err は error 型の元エラーです。
// 返り値: int 型のSQLite番号です。SQLite以外は0です。
func sqliteErrorCode(err error) int {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code()
	}
	return 0
}

// TestSQLiteDeferredSnapshotUpgradeReproducesBusy は旧保存順序の競合を決定的に再現します。
//
// 引数: t は *testing.T 型です。
// 返り値: ありません。実際の別接続出力が確定した後の昇格が517になることを確認します。
func TestSQLiteDeferredSnapshotUpgradeReproducesBusy(t *testing.T) {
	for _, output := range []string{"Viewer", "終了CSV", "writer保持中"} {
		t.Run(output, func(t *testing.T) {
			s, path, _ := testStore(t)
			trade := viewerTestTrade("保存済み", "TOPIXmini", true, 0)
			saveReportTrades(t, s, []Trade{trade}, "done", 1)
			if err := s.RegisterReportSession(trade.Session, true, trade.Product, reportTestEnd(), 1000); err != nil {
				t.Fatal(err)
			}
			// ----------------------------------------
			// 修正前と同じDEFERRED接続を明示し、読取後の別writer確定を挟みます。
			legacy, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer legacy.Close()
			legacy.SetMaxOpenConns(1)
			if _, err = legacy.Exec("PRAGMA busy_timeout=5000"); err != nil {
				t.Fatal(err)
			}
			tx, err := legacy.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = readTrades(tx); err != nil {
				t.Fatal(err)
			}
			var state []byte
			if err = tx.QueryRow("SELECT state FROM snapshot WHERE id=1").Scan(&state); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			expectedCode := 517
			if output == "writer保持中" {
				other, e := openJournalSQLite(path, true)
				if e != nil {
					t.Fatal(e)
				}
				defer other.Close()
				writer, e := other.Begin()
				if e != nil {
					t.Fatal(e)
				}
				defer writer.Rollback()
				expectedCode = 5
			} else if output == "Viewer" {
				err = PublishStoredViewer(path, root, reportTestEnd())
			} else {
				err = FinalizeStoredReports(path, root, reportTestEnd())
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec("UPDATE snapshot SET revision=revision+1 WHERE id=1")
			if sqliteErrorCode(err) != expectedCode {
				t.Fatalf("旧順序のSQLite競合を再現できません: 番号=%d 期待=%d %v", sqliteErrorCode(err), expectedCode, err)
			}
			t.Logf("別接続%s確定後の旧DEFERRED昇格: %v", output, err)
		})
	}
}

// TestSQLiteURIKeepsLiteralPath はDSNへ変換しても記号をDB名の一部として保持します。
//
// 引数: t は *testing.T 型です。
// 返り値: ありません。日本語・空白・#・%・&を持つDBを別接続から復元します。
func TestSQLiteURIKeepsLiteralPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "状態 # % &.sqlite")
	s, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Save(map[string]bool{"保存": true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("指定名と異なるDBを作成しました", err)
	}
	other, err := openJournalSQLite(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var raw []byte
	if err = other.QueryRow("SELECT state FROM snapshot WHERE id=1").Scan(&raw); err != nil || string(raw) != `{"保存":true}` {
		t.Fatalf("別接続が同じ指定DBを開いていません: %s %v", raw, err)
	}
}

// TestSQLiteSettingsSurviveReconnect は物理接続の交換後も全保存設定が残ることを検査します。
//
// 引数: t は *testing.T 型です。
// 返り値: ありません。idleを0にして毎回別接続でPRAGMAを確認します。
func TestSQLiteSettingsSurviveReconnect(t *testing.T) {
	s, path, _ := testStore(t)
	background, err := openJournalSQLite(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer background.Close()
	for name, db := range map[string]*sql.DB{"Store": s.db, "別接続出力": background} {
		db.SetMaxIdleConns(0)
		for _, tc := range []struct {
			pragma string
			want   int
		}{{"busy_timeout", 5000}, {"synchronous", 2}, {"foreign_keys", 1}} {
			var got int
			if err := db.QueryRow("PRAGMA " + tc.pragma).Scan(&got); err != nil || got != tc.want {
				t.Errorf("%s再接続後の%s: 実値=%d 期待=%d error=%v", name, tc.pragma, got, tc.want, err)
			}
		}
		var mode string
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("%s再接続後のWAL: %s %v", name, mode, err)
		}
	}
	if err := s.Save(map[string]any{"再接続後": true}, nil); err != nil {
		t.Fatal(err)
	}
}

// TestSQLiteReadOnlyTransactionDoesNotReserveWriter は読取指定の診断がwriterを先取りしません。
//
// 引数: t は *testing.T 型です。
// 返り値: ありません。ReadOnly指定をドライバがDEFERREDで開始することを確認します。
func TestSQLiteReadOnlyTransactionDoesNotReserveWriter(t *testing.T) {
	s, path, _ := testStore(t)
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = readTrades(tx); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = other.ExecContext(ctx, "INSERT INTO sessions(session,dryrun) VALUES('2026-10-21_Day',1)"); err != nil {
		t.Fatalf("診断読取が別writerを止めました: %v", err)
	}
}

// TestSQLiteSaveWaitsBeforeReading は保存が読取前のBeginで別writer終了を待つことを検査します。
//
// 引数: t は *testing.T 型です。
// 返り値: ありません。待機後も保存を1回だけ確定し、約定本文を変えません。
func TestSQLiteSaveWaitsBeforeReading(t *testing.T) {
	s, path, _ := testStore(t)
	if err := s.Save(map[string]int{"revision": 0}, nil); err != nil {
		t.Fatal(err)
	}
	other, err := openJournalSQLite(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	writer, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	trade := viewerTestTrade("競合後の確定", "TOPIXmini", true, 0)
	done := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		done <- s.Save(map[string]int{"revision": 1}, []Trade{trade})
	}()
	<-started
	select {
	case err = <-done:
		t.Fatalf("別writer中の保存が待機せず終了しました: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err = writer.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("別writer終了後も保存が完了しません")
	}
	var state map[string]int
	fills, err := s.Load(&state)
	actual, marshalErr := json.Marshal(fills)
	want, wantErr := json.Marshal([]Trade{trade})
	if err != nil || marshalErr != nil || wantErr != nil || state["revision"] != 1 || !bytes.Equal(actual, want) || scalar(t, s, "SELECT count(*) FROM events WHERE kind='約定確定'") != 1 {
		t.Fatalf("待機後に保存が重複又は欠落しました: state=%v fills=%v error=%v", state, fills, err)
	}
}

// TestSQLiteBusyDeadlineIsExplicit は期限を超えるwriter競合を成功へ置き換えません。
//
// 引数: t は *testing.T 型です。
// 返り値: ありません。試験だけ50msへ短縮し、失敗した状態・約定が未確定であることを確認します。
func TestSQLiteBusyDeadlineIsExplicit(t *testing.T) {
	s, path, _ := testStore(t)
	if err := s.Save(map[string]int{"revision": 0}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA busy_timeout=50"); err != nil {
		t.Fatal(err)
	}
	other, err := openJournalSQLite(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	writer, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	trade := viewerTestTrade("未確定", "TOPIXmini", true, 0)
	started := time.Now()
	err = s.Save(map[string]int{"revision": 1}, []Trade{trade})
	if sqliteErrorCode(err)&255 != 5 || time.Since(started) > 2*time.Second {
		t.Fatalf("期限競合を明示しませんでした: 時間=%v error=%v", time.Since(started), err)
	}
	if err = writer.Rollback(); err != nil {
		t.Fatal(err)
	}
	var state map[string]int
	fills, err := s.Load(&state)
	if err != nil || state["revision"] != 0 || len(fills) != 0 || scalar(t, s, "SELECT count(*) FROM events WHERE kind='約定確定'") != 0 {
		t.Fatalf("失敗した保存を確定扱いしました: %v %v %v", state, fills, err)
	}
}

// TestSQLiteStoredOutputsAndSaveConcurrent は本体保存と2種類の別接続出力を並行反復します。
//
// 引数: t は *testing.T 型です。
// 返り値: ありません。30所有玉・60約定のID、金額、手数料、出力件数を独立に照合します。
func TestSQLiteStoredOutputsAndSaveConcurrent(t *testing.T) {
	s, path, _ := testStore(t)
	root := t.TempDir()
	const pairs = 30
	trades := make([]Trade, 0, pairs*2)
	for i := 0; i < pairs; i++ {
		entry := viewerTestTrade(fmt.Sprintf("新規-%02d", i), "TOPIXmini", true, time.Duration(i*2)*time.Second)
		close := viewerTestTrade(fmt.Sprintf("返済-%02d", i), "TOPIXmini", true, time.Duration(i*2+1)*time.Second)
		close.TradeType, close.Side, close.Price, close.PnL, close.HoldID = "close", "sell", 101, 2000, ""
		close.Closed = []controller.ClosePosition{{HoldID: entry.HoldID, Quantity: 2}}
		trades = append(trades, entry, close)
	}
	saveReportTrades(t, s, nil, "done", 0)
	if err := s.RegisterReportSession(trades[0].Session, true, "TOPIXmini", reportTestEnd(), 1000); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errors := make(chan error, 3)
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		<-start
		for i := 1; i <= len(trades); i++ {
			if err := s.Save(map[string]any{"ack_count": i, "orders": []any{}}, trades[:i]); err != nil {
				errors <- fmt.Errorf("本体保存%d: %w", i, err)
				return
			}
		}
	}()
	for _, output := range []func(string, string, time.Time) error{PublishStoredViewer, FinalizeStoredReports} {
		go func(output func(string, string, time.Time) error) {
			defer workers.Done()
			<-start
			for i := 0; i < pairs; i++ {
				if err := output(path, root, reportTestEnd()); err != nil {
					errors <- err
					return
				}
			}
		}(output)
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	if err := PublishStoredViewer(path, root, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeStoredReports(path, root, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	fills, err := s.Load(&state)
	actual, marshalErr := json.Marshal(fills)
	want, wantErr := json.Marshal(trades)
	if err != nil || marshalErr != nil || wantErr != nil || !bytes.Equal(actual, want) || state["ack_count"] != float64(len(trades)) {
		t.Fatalf("並行保存後に正本の順序・本文が変わりました: 件数=%d error=%v", len(fills), err)
	}
	if scalar(t, s, "SELECT count(*) FROM trades") != pairs*2 || scalar(t, s, "SELECT count(*) FROM events WHERE kind='約定確定'") != pairs*2 {
		t.Fatal("並行保存で約定又は監査イベントが重複・欠落しました")
	}
	rows := viewerTestRows(t, filepath.Join(root, "TOPIXmini", trades[0].Session+"_dryrun.trades.jsonl"))
	ids := map[string]bool{}
	positions := map[string]int{}
	fees, realized := 0.0, 0.0
	entryPrice := map[string]float64{}
	for _, row := range rows {
		if ids[row.ID] || row.Price == nil {
			t.Fatal("Viewer約定ID重複又は価格欠測です", row)
		}
		ids[row.ID] = true
		fees += row.FeeYen
		if row.Type == "LongN" {
			positions[row.PositionID] += row.Qty
			entryPrice[row.PositionID] = *row.Price
		} else if row.Type == "LongR" {
			if positions[row.PositionID] < row.Qty {
				t.Fatal("Viewerで別所有玉を返済しました", row)
			}
			positions[row.PositionID] -= row.Qty
			realized += (*row.Price - entryPrice[row.PositionID]) * float64(row.Qty) * 1000
		} else {
			t.Fatal("人工入力にない売買種別です", row.Type)
		}
	}
	if len(rows) != pairs*2 || len(positions) != pairs || realized != pairs*2000 || fees != pairs*2*83 {
		t.Fatalf("Viewerの全所有玉会計が不一致です: 行%d 玉%d 損益%v 手数料%v", len(rows), len(positions), realized, fees)
	}
	for id, qty := range positions {
		if qty != 0 {
			t.Fatalf("返済済み所有玉が残りました: %s=%d", id, qty)
		}
	}
	pl, err := ReadSessionPL(root)
	if err != nil || len(pl) != 1 || pl[0].Net != pairs*2000 || pl[0].EntryQty != pairs*2 {
		t.Fatalf("終了CSVの場・数量・損益が不一致です: %+v %v", pl, err)
	}
}
