package webui

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionPLTestHandler は売買workerを一切開始せず読取APIだけを準備します。
//
// 引数: t(*testing.T)。返り値: http.Handler、共通ログディレクトリ(string)。
func sessionPLTestHandler(t *testing.T) (http.Handler, string) {
	t.Helper()
	cfg := testConfig(t)
	cfg.Runtime.LogDirectory = "session-report-test"
	handler, err := NewHandler(&Manager{cfg: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return handler, cfg.Path(cfg.Runtime.LogDirectory)
}

// writeSessionPLTestCSV は公開契約の7列をテスト専用領域へ保存します。
//
// 引数: t(*testing.T)、root(string)、rows([][]string)。返り値: 保存内容([]byte)。
func writeSessionPLTestCSV(t *testing.T, root string, rows [][]string) []byte {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	b.WriteString("\ufeff")
	w := csv.NewWriter(&b)
	if err := w.WriteAll(append([][]string{{"利益", "損失", "収支", "新規建合計qty", "セッション", "銘柄", "モード"}}, rows...)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sessionPL.csv"), b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), b.Bytes()...)
}

// TestSessionPLRESTMissingIsEmptyWithoutReconstruction はCSV欠損を正常空一覧とし、別記録を補完しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSessionPLRESTMissingIsEmptyWithoutReconstruction(t *testing.T) {
	handler, root := sessionPLTestHandler(t)
	res := apiRequest(t, handler, http.MethodGet, "/api/session-pl", "", nil)
	if res.Code != http.StatusOK || strings.TrimSpace(res.Body.String()) != "{\"rows\":[]}" {
		t.Fatalf("欠損を空配列へできません: %d %s", res.Code, res.Body.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("読取だけで保存先を作成しました", err)
	}
	// ----------------------------------------
	// worker別のCSVや旧summaryがあっても共通CSVへ復元しません。
	child := filepath.Join(root, "live", "NK225mini")
	writeSessionPLTestCSV(t, child, [][]string{{"999", "0", "999", "1", "2026-10-26_Day", "NK225mini", "live"}})
	if err := os.WriteFile(filepath.Join(root, "summary.csv"), []byte("旧集計を読まない"), 0600); err != nil {
		t.Fatal(err)
	}
	res = apiRequest(t, handler, http.MethodGet, "/api/session-pl", "", nil)
	if res.Code != http.StatusOK || strings.TrimSpace(res.Body.String()) != "{\"rows\":[]}" {
		t.Fatalf("共通CSVの欠損を別保存先から補いました: %s", res.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "sessionPL.csv")); !os.IsNotExist(err) {
		t.Fatal("損益CSVを読取時に再作成しました", err)
	}
}

// TestSessionPLRESTReadsCurrentSharedCSV は商品・モードを分離した実数値と再読込・削除反映を検査します。
//
// 引数: t(*testing.T)。返り値: なし。設定や売買状態は操作しません。
func TestSessionPLRESTReadsCurrentSharedCSV(t *testing.T) {
	handler, root := sessionPLTestHandler(t)
	rows := [][]string{{"12000", "2000", "10000", "3", "2026-10-26_Day", "NK225mini", "live"}, {"500", "1500", "-1000", "2", "2026-10-26_Day", "NK225mini", "dryrun"}, {"0", "0", "0", "0", "2026-10-26_Night", "TOPIXmini", "dryrun"}}
	written := writeSessionPLTestCSV(t, root, rows)
	res := apiRequest(t, handler, http.MethodGet, "/api/session-pl", "", nil)
	var body struct {
		Rows []struct {
			Session  string  `json:"session"`
			Product  string  `json:"product"`
			Mode     string  `json:"mode"`
			Profit   float64 `json:"profit"`
			Loss     float64 `json:"loss"`
			Net      float64 `json:"net"`
			EntryQty int     `json:"entry_qty"`
		} `json:"rows"`
	}
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &body) != nil || len(body.Rows) != 3 {
		t.Fatalf("損益読取失敗: %d %s", res.Code, res.Body.String())
	}
	seen := map[string]bool{}
	for _, row := range body.Rows {
		key := row.Session + "/" + row.Product + "/" + row.Mode
		seen[key] = true
		switch key {
		case "2026-10-26_Day/NK225mini/live":
			if row.Profit != 12000 || row.Loss != 2000 || row.Net != 10000 || row.EntryQty != 3 {
				t.Fatal("実取引値が異なります", row)
			}
		case "2026-10-26_Day/NK225mini/dryrun":
			if row.Profit != 500 || row.Loss != 1500 || row.Net != -1000 || row.EntryQty != 2 {
				t.Fatal("仮想値が実取引へ混在", row)
			}
		case "2026-10-26_Night/TOPIXmini/dryrun":
			if row.Profit != 0 || row.Loss != 0 || row.Net != 0 || row.EntryQty != 0 {
				t.Fatal("無取引の0円が不正", row)
			}
		default:
			t.Fatal("別保存先の行が混入", row)
		}
	}
	if len(seen) != 3 {
		t.Fatal("商品・モード・セッションが混ざりました")
	}
	if res.Header().Get("Cache-Control") != "no-store" || !strings.Contains(res.Header().Get("Content-Type"), "application/json") {
		t.Fatal("RESTの再読込又はJSON契約が不正")
	}
	current, err := os.ReadFile(filepath.Join(root, "sessionPL.csv"))
	if err != nil || !bytes.Equal(current, written) {
		t.Fatal("GETがCSVを書き換えました", err)
	}
	writeSessionPLTestCSV(t, root, [][]string{{"30", "5", "25", "1", "2026-10-27_Day", "NK225micro", "live"}})
	res = apiRequest(t, handler, http.MethodGet, "/api/session-pl", "", nil)
	if json.Unmarshal(res.Body.Bytes(), &body) != nil || len(body.Rows) != 1 || body.Rows[0].Net != 25 {
		t.Fatal("CSV更新が次GETへ反映されません", res.Body.String())
	}
	if err := os.Remove(filepath.Join(root, "sessionPL.csv")); err != nil {
		t.Fatal(err)
	}
	res = apiRequest(t, handler, http.MethodGet, "/api/session-pl", "", nil)
	if res.Code != 200 || strings.TrimSpace(res.Body.String()) != "{\"rows\":[]}" {
		t.Fatal("CSV削除後も古いデータを返しました", res.Body.String())
	}
}

// TestSessionPLRESTReadFailureIsNotSuccess は読取不能とCSV異常を空の成功応答にしないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSessionPLRESTReadFailureIsNotSuccess(t *testing.T) {
	for _, kind := range []string{"列不正", "数値不正", "ファイルがディレクトリ"} {
		t.Run(kind, func(t *testing.T) {
			handler, root := sessionPLTestHandler(t)
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "列不正":
				if err := os.WriteFile(filepath.Join(root, "sessionPL.csv"), []byte("利益,損失\n1,2\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "数値不正":
				writeSessionPLTestCSV(t, root, [][]string{{"NaN", "0", "0", "1", "2026-10-26_Day", "NK225mini", "live"}})
			default:
				if err := os.Mkdir(filepath.Join(root, "sessionPL.csv"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			res := apiRequest(t, handler, http.MethodGet, "/api/session-pl", "", nil)
			var body map[string]json.RawMessage
			if res.Code != http.StatusInternalServerError || json.Unmarshal(res.Body.Bytes(), &body) != nil || len(body["error"]) == 0 || body["rows"] != nil {
				t.Fatalf("異常を成功又は空一覧にしました: %d %s", res.Code, res.Body.String())
			}
		})
	}
}
