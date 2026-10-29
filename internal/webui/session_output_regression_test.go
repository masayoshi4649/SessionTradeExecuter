package webui

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

type reportNoNetworkAPI struct{ calls int }

// SendFuture は通信を行わず、仮想実行からの誤送信を失敗として返します。
//
// 引数: context.Context、controller.BrokerRequestは受信値です。
// 返り値: controller.OrderResponseとerror。正常な仮想実行では呼ばれません。
func (a *reportNoNetworkAPI) SendFuture(context.Context, controller.BrokerRequest) (controller.OrderResponse, error) {
	a.calls++
	return controller.OrderResponse{}, fmt.Errorf("回帰試験からの発注は禁止しています")
}

// reportOutputFiles は正式CSV・小さいViewer入力以外の旧派生物や旧商品階層を検出します。
//
// 引数: t(*testing.T)、root(string)、required(...string)は必要な正式CSVです。
// 返り値: なし。Web操作監査・共通排他・新しい閲覧用約定だけを追加許可します。
func reportOutputFiles(t *testing.T, root string, required ...string) {
	t.Helper()
	allowed := map[string]bool{"web/操作.jsonl": true, ".sessionPL.lock": true, "sessionviewer.resources.json": true}
	viewerFile := regexp.MustCompile(`^NK225mini/\d{4}-\d{2}-\d{2}_(Day|Night)(_dryrun)?\.trades\.jsonl$`)
	missing := map[string]bool{}
	for _, name := range required {
		allowed[name], missing[name] = true, true
	}
	err := filepath.WalkDir(root, func(path string, item fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if item.IsDir() {
			if rel != "." && rel != "web" && rel != "NK225mini" {
				t.Errorf("終了CSVに不要なログ階層を生成しました: %s", rel)
			}
			return nil
		}
		if !allowed[rel] && !viewerFile.MatchString(rel) {
			t.Errorf("指定CSV・Viewer入力以外の売買ログを生成しました: %s", rel)
		}
		delete(missing, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("正式CSVが不足しています: %v", missing)
	}
}

// reportAPIValues はCSV読取APIの値を実約定から別計算した期待値と照合します。
//
// 引数: t(*testing.T)、manager(*Manager)、want([]journal.SessionPL)。
// 返り値: なし。欠損はnullではなく空配列であることも確認します。
func reportAPIValues(t *testing.T, manager *Manager, want []journal.SessionPL) {
	t.Helper()
	handler, err := NewHandler(manager)
	if err != nil {
		t.Fatal(err)
	}
	response := apiRequest(t, handler, http.MethodGet, "/api/session-pl", "", nil)
	var body struct {
		Rows []journal.SessionPL `json:"rows"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
		t.Fatalf("CSV読取APIに失敗しました: %d %s", response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(body.Rows, want) {
		t.Fatalf("実約定からの集計がAPIと不一致: 実際=%+v 期待=%+v", body.Rows, want)
	}
}

// TestSessionOutputsUseExecutionAndDoNotRebuildDeletedCSV は実保存器を使い終了CSVのライフサイクルを検査します。
//
// 引数: t(*testing.T)は通信のない人工売買試験です。
// 返り値: なし。手書きの損益CSVを使わず、約定・再起動・削除・次場を連結します。
func TestSessionOutputsUseExecutionAndDoNotRebuildDeletedCSV(t *testing.T) {
	for _, legacyArgument := range []bool{false, true} {
		name := "共通ログ保存先"
		if legacyArgument {
			name = "旧階層引数も派生出力を再開しない"
		}
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			root := cfg.Path(cfg.Runtime.LogDirectory)
			database := filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), "dryrun", "nk225mini", "NK225mini.sqlite")
			logArgument := root
			if legacyArgument {
				logArgument = filepath.Join(root, "dryrun", "nk225mini", "NK225mini")
			}
			newWeb := func() *Manager {
				manager, err := NewManager(cfg, func(context.Context, config.Config, *runner.Control, func(runner.Snapshot)) error {
					t.Error("終了レポート処理から売買workerを起動しました")
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = manager.Close() })
				return manager
			}
			openExecution := func() *execution.Manager {
				manager, err := execution.Open(logArgument, database, true, "NK225mini", 100, 10)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = manager.Close() })
				return manager
			}
			web, account := newWeb(), openExecution()
			api := &reportNoNetworkAPI{}
			end := time.Date(2026, 10, 28, 15, 45, 0, 0, time.FixedZone("JST", 9*3600))
			session := "2026-10-28_Day"
			if err := account.EnsureSession(session); err != nil {
				t.Fatal(err)
			}
			if err := account.RegisterSessionReport(session, end); err != nil {
				t.Fatal(err)
			}
			if seq, err := account.RecordBook(session, "人工世代", "NK225", end.Add(-time.Hour), json.RawMessage(`{"CurrentPrice":65000}`)); err != nil || seq <= 0 {
				t.Fatal("板をSQLiteへ保存できません", seq, err)
			}
			if err := account.Event("判断", session, "NK225", "人工判断", end.Add(-time.Hour), map[string]any{"理由": "通信しない回帰試験"}); err != nil {
				t.Fatal(err)
			}
			submit := func(id, scene, side, kind string, qty int, price float64, at time.Time, ack bool) {
				intent := execution.Intent{Owner: "core", Product: "NK225mini", MethodID: "NK225_D164_15", ID: id,
					Session: scene, ContractSymbol: "167120019", Side: side, TradeType: kind, Quantity: qty, Price: price,
					At: at, ExpiresAt: at.Add(time.Minute), Reason: "人工約定"}
				fills, err := account.Submit(context.Background(), api, intent, controller.OrdersView{
					InstanceID: "回帰試験", Connected: true, Session: &controller.Session{Exchange: 33}})
				if err != nil || len(fills) != 1 {
					t.Fatalf("仮想約定に失敗しました: %s %+v %v", id, fills, err)
				}
				if ack {
					if err := account.Checkpoint(json.RawMessage(`{"確認":"完了"}`)); err != nil {
						t.Fatal(err)
					}
				}
			}
			submit("買新規", session, "buy", "new", 3, 65000, end.Add(-5*time.Minute), true)
			submit("買利益部分返済", session, "sell", "close", 1, 65100, end.Add(-4*time.Minute), true)
			submit("買損失残返済", session, "sell", "close", 2, 64980, end.Add(-3*time.Minute), true)
			submit("売新規", session, "sell", "new", 2, 65000, end.Add(-2*time.Minute), true)
			submit("売利益返済", session, "buy", "close", 2, 64950, end.Add(-time.Minute), false)

			// ----------------------------------------
			// 立会終了だけでは不足し、最後の約定を戦略へ確定反映してから正式出力します。
			web.finalizeStoredSessionReports(end)
			if web.State().ReportError != "" {
				t.Fatal(web.State().ReportError)
			}
			reportAPIValues(t, web, []journal.SessionPL{})
			reportOutputFiles(t, root)
			if err := account.Checkpoint(json.RawMessage(`{"確認":"完了"}`)); err != nil {
				t.Fatal(err)
			}
			if err := account.FinalizeReports(root, end.Add(-time.Nanosecond)); err != nil {
				t.Fatal(err)
			}
			reportAPIValues(t, web, []journal.SessionPL{})
			web.finalizeStoredSessionReports(end)
			if web.State().ReportError != "" {
				t.Fatal(web.State().ReportError)
			}
			expected := []journal.SessionPL{{Session: session, Product: "NK225mini", Mode: "dryrun", Profit: 20000, Loss: 4000, Net: 16000, EntryQty: 5}}
			reportAPIValues(t, web, expected)
			tradeName := "NK225mini/" + session + "_dryrun.csv"
			reportOutputFiles(t, root, "sessionPL.csv", tradeName)
			tradeRaw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(tradeName)))
			if err != nil {
				t.Fatal(err)
			}
			tradeRows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(tradeRaw, []byte("\xef\xbb\xbf")))).ReadAll()
			if err != nil || len(tradeRows) != 6 || !reflect.DeepEqual(tradeRows[0], []string{"売買種別", "price", "qty", "timestamp", "損益", "手法ID"}) {
				t.Fatal("約定明細の見出し又は行数が不正", tradeRows, err)
			}
			wantKinds, wantPnL := []string{"LongN", "LongR", "LongR", "ShortN", "ShortR"}, []string{"0", "+10000", "-4000", "0", "+10000"}
			for i, row := range tradeRows[1:] {
				if row[0] != wantKinds[i] || row[4] != wantPnL[i] || row[5] != "NK225_D164_15" {
					t.Fatalf("約定から出した明細が不一致: %v", row)
				}
			}
			if len(account.Positions()) != 0 || api.calls != 0 {
				t.Fatal("未返済又は通信が発生しました", account.Positions(), api.calls)
			}

			// ----------------------------------------
			// 実SQLiteを再読込しても重複出力せず、その後の削除はDBから埋め戻しません。
			if err := account.Close(); err != nil {
				t.Fatal(err)
			}
			if err := web.Close(); err != nil {
				t.Fatal(err)
			}
			account, web = openExecution(), newWeb()
			if len(account.Records()) != 5 || len(account.OutstandingFills()) != 0 {
				t.Fatal("実SQLiteからの約定復元が不正")
			}
			web.finalizeStoredSessionReports(end.Add(time.Minute))
			reportAPIValues(t, web, expected)
			current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(tradeName)))
			if err != nil || !bytes.Equal(current, tradeRaw) {
				t.Fatal("再起動が終了済み明細を変更しました", err)
			}
			for _, path := range []string{"sessionPL.csv", tradeName} {
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(path))); err != nil {
					t.Fatal(err)
				}
			}
			if err := account.Close(); err != nil {
				t.Fatal(err)
			}
			account = openExecution()
			if err := account.FinalizeReports(root, end.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			web.finalizeStoredSessionReports(end.Add(time.Hour))
			reportAPIValues(t, web, []journal.SessionPL{})
			reportOutputFiles(t, root)

			// ----------------------------------------
			// 新しい場を保存しても、利用者が削除した古い場の明細・集計行は復活しません。
			nextSession, nextEnd := "2026-10-29_Day", end.AddDate(0, 0, 1)
			if err := account.EnsureSession(nextSession); err != nil {
				t.Fatal(err)
			}
			if err := account.RegisterSessionReport(nextSession, nextEnd); err != nil {
				t.Fatal(err)
			}
			submit("次場買新規", nextSession, "buy", "new", 1, 65000, nextEnd.Add(-2*time.Minute), true)
			submit("次場買返済", nextSession, "sell", "close", 1, 65010, nextEnd.Add(-time.Minute), true)
			web.finalizeStoredSessionReports(nextEnd)
			reportAPIValues(t, web, []journal.SessionPL{{Session: nextSession, Product: "NK225mini", Mode: "dryrun", Profit: 1000, Net: 1000, EntryQty: 1}})
			reportOutputFiles(t, root, "sessionPL.csv", "NK225mini/"+nextSession+"_dryrun.csv")
			if api.calls != 0 {
				t.Fatal("仮想試験が発注を要求しました")
			}
			db, err := sql.Open("sqlite", database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, check := range []struct {
				query string
				count int
			}{{"SELECT COUNT(*) FROM trades", 7}, {"SELECT COUNT(*) FROM books", 1}, {"SELECT COUNT(*) FROM events WHERE kind='判断'", 1}} {
				var count int
				if err := db.QueryRow(check.query).Scan(&count); err != nil || count != check.count {
					t.Fatal("CSV削除でSQLiteの原記録まで失われました", check.query, count, err)
				}
			}
		})
	}
}
