package execution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

type bridgeRecord struct {
	ID         string  `json:"id"`
	Timestamp  string  `json:"timestamp"`
	Type       string  `json:"type"`
	PositionID string  `json:"position_id"`
	Price      float64 `json:"price"`
	Qty        int     `json:"qty"`
	Fee        float64 `json:"fee_yen"`
	Method     string  `json:"method_id"`
	SourceLine *int64  `json:"source_line"`
}

// bridgeAt は架空の検証セッション内の時計を返します。
//
// 引数: minute(int)は09時からの分。返り値: time.TimeはJST時計です。
func bridgeAt(minute int) time.Time {
	return time.Date(2026, 10, 2, 9, minute, 0, 0, time.FixedZone("JST", 9*3600))
}

// bridgeIntent は実通信を使わない新規・返済意図を組み立てます。
//
// 引数: id/side/kind/method(string)、minute/qty(int)、price(float64)。返り値: Intent。
func bridgeIntent(id, side, kind, method string, minute, qty int, price float64) Intent {
	i := testIntent(id, side, kind, qty, price)
	i.At, i.ExpiresAt, i.MethodID, i.Product = bridgeAt(minute), bridgeAt(minute).Add(time.Minute), method, "NK225mini"
	i.Evidence = json.RawMessage(`{"order_namespace":"独立検証","input_id":42}`)
	return i
}

// bridgeRead は生成された小容量JSONLを標準JSONだけで読みます。
//
// 引数: t(*testing.T)、path(string)。返り値: []bridgeRecord。
func bridgeRead(t *testing.T, path string) []bridgeRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows := []bridgeRecord{}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var row bridgeRecord
		if err = json.Unmarshal(scan.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	if err = scan.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

// bridgeAssertFacts は確定注文と無関係な再生器を使わず、既知の人工約定事実を照合します。
//
// 引数: t(*testing.T)、rows([]bridgeRecord)。返り値: なし。
func bridgeAssertFacts(t *testing.T, rows []bridgeRecord) {
	t.Helper()
	if len(rows) != 9 {
		t.Fatalf("約定片と管理外イベントは9行のはずです: %d %+v", len(rows), rows)
	}
	fees, kinds, seen := 0.0, map[string]int{}, map[string]bool{}
	previous := time.Time{}
	closed := map[string]int{}
	for _, row := range rows {
		if seen[row.ID] || row.PositionID == "" || row.Qty < 1 || row.SourceLine != nil {
			t.Fatalf("識別子・枚数又は捏造原行: %+v", row)
		}
		seen[row.ID] = true
		at, err := time.Parse(time.RFC3339Nano, row.Timestamp)
		if err != nil || at.Before(previous) {
			t.Fatalf("表示JSONL時計順が不正: %+v %v", row, err)
		}
		previous = at
		fees += row.Fee
		kinds[row.Type]++
		if row.Type == "LongR" {
			closed[row.PositionID] += row.Qty
			if row.Method != "NK225_終了担当" {
				t.Fatalf("返済時のIDを入口IDへ書換えました: %+v", row)
			}
		}
		if row.Type == "Detach" && (row.PositionID != "dry-買A" || row.Qty != 1 || row.Price != 0 || row.Fee != 0) {
			t.Fatalf("管理外を架空返済にしました: %+v", row)
		}
	}
	if !reflect.DeepEqual(kinds, map[string]int{"LongN": 2, "ShortN": 2, "LongR": 2, "ShortR": 2, "Detach": 1}) {
		t.Fatalf("売買内訳: %+v", kinds)
	}
	if !reflect.DeepEqual(closed, map[string]int{"dry-買A": 1, "dry-買B": 2}) {
		t.Fatalf("指定した複数玉返済をFIFO等へ変えました: %+v", closed)
	}
	if math.Abs(fees-499) > 1e-8 {
		t.Fatalf("配賦で手数料総額が変わりました: %v", fees)
	}
}

// TestViewerBridgeActualDryrunFacts は実行器・SQLite・出力器を通じた全所有履歴を検査します。
//
// 引数: t(*testing.T)。返り値: なし。全注文は仮想、外部発注は0回です。
func TestViewerBridgeActualDryrunFacts(t *testing.T) {
	root := t.TempDir()
	logRoot, database := filepath.Join(root, "log"), filepath.Join(root, "data", "NK225mini.sqlite")
	m, err := Open(logRoot, database, true, "NK225mini", 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	end := bridgeAt(0).Add(6*time.Hour + 45*time.Minute)
	if err = m.RegisterSessionReport("2026-10-02_Day", end); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	submit := func(intent Intent) {
		t.Helper()
		fills, e := m.Submit(context.Background(), api, intent, testView())
		if e != nil || len(fills) != 1 {
			t.Fatalf("人工約定が失敗: %s %+v %v", intent.ID, fills, e)
		}
		acknowledge(t, m)
	}
	submit(bridgeIntent("買A", "buy", "new", "NK225_入口A", 0, 2, 65000))
	submit(bridgeIntent("買B", "buy", "new", "NK225_入口B", 1, 2, 65060))
	submit(bridgeIntent("売C", "sell", "new", "NK225_入口C", 2, 1, 65030))
	group := bridgeIntent("指定返済", "sell", "close", "NK225_終了担当", 3, 3, 65040)
	group.ClosePositions = []controller.ClosePosition{{HoldID: "dry-買B", Quantity: 2}, {HoldID: "dry-買A", Quantity: 1}}
	submit(group)
	shortClose := bridgeIntent("売返済", "buy", "close", "NK225_終了担当", 4, 1, 65010)
	shortClose.ClosePositions = []controller.ClosePosition{{HoldID: "dry-売C", Quantity: 1}}
	submit(shortClose)
	// ----------------------------------------
	// 出力障害は正本約定へ波及せず、回復後の出力も約定を増やしません。
	before := m.Records()
	if err = os.WriteFile(logRoot, []byte("保存障害"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.store.PublishViewer(logRoot, bridgeAt(4)); err == nil {
		t.Fatal("保存先がファイルなのに成功しました")
	}
	if !reflect.DeepEqual(before, m.Records()) || api.calls != 0 {
		t.Fatal("派生出力障害で売買正本又は送信回数が変わりました")
	}
	if err = os.Remove(logRoot); err != nil {
		t.Fatal(err)
	}
	if err = m.store.PublishViewer(logRoot, bridgeAt(4)); err != nil {
		t.Fatal(err)
	}
	// ----------------------------------------
	// OFFは返済せず管理外。次の起動による旧snapshot保存でも二重除外しません。
	positions := m.Positions()
	if len(positions) != 1 || positions[0].HoldID != "dry-買A" || positions[0].Quantity != 1 {
		t.Fatal("OFF前の元玉が不正です", positions)
	}
	if err = m.Event("保有玉の管理外移管", "2026-10-02_Day", "NK225", "", bridgeAt(5), map[string]any{"positions": positions, "dryrun": true}); err != nil {
		t.Fatal(err)
	}
	if err = m.store.PublishViewer(logRoot, bridgeAt(5)); err != nil {
		t.Fatal(err)
	}
	if err = m.Event("起動時状態切替", "", "", "", bridgeAt(6), map[string]any{"metadata": map[string]any{"previous_holdings": positions, "dryrun": true, "product": "NK225mini"}}); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = OpenNewRun(context.Background(), nil, logRoot, database, true, "NK225mini", 100, 10, "次の運転", json.RawMessage(`{"新規運転":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Positions()) != 0 || len(m.Records()) != 5 {
		t.Fatal("再起動が旧玉を復活又は過去約定を削除しました")
	}
	if err = m.RegisterSessionReport("2026-10-02_Day", end); err != nil {
		t.Fatal(err)
	}
	submit(bridgeIntent("売D", "sell", "new", "NK225_入口C", 7, 2, 65050))
	secondClose := bridgeIntent("新運転返済", "buy", "close", "NK225_終了担当", 8, 2, 65030)
	secondClose.ClosePositions = []controller.ClosePosition{{HoldID: "dry-売D", Quantity: 2}}
	submit(secondClose)
	if err = m.store.PublishViewer(logRoot, bridgeAt(9)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(logRoot, "NK225mini", "2026-10-02_Day_dryrun.trades.jsonl")
	rows := bridgeRead(t, path)
	bridgeAssertFacts(t, rows)
	if api.calls != 0 {
		t.Fatal("仮想注文が外部送信しました")
	}
	gross, fees := 0.0, 0.0
	for _, record := range m.Records() {
		gross += record.PnL
		fees += record.Fee
	}
	if gross != 6000 || fees != 499 {
		t.Fatalf("元実行器の人工事実が不正: gross=%v fees=%v", gross, fees)
	}
	manifestPath := filepath.Join(logRoot, "sessionviewer.resources.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]any
	if err = json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	resources, ok := catalog["resources"].([]any)
	if !ok || len(resources) != 4 {
		t.Fatalf("全体+新規3手法に一致しません: %s", data)
	}
	paths, methods := map[string]bool{}, map[string]bool{}
	for _, raw := range resources {
		r := raw.(map[string]any)
		p := r["pnl_path"].(string)
		paths[p] = true
		if filepath.IsAbs(p) {
			t.Fatalf("相対ファイル参照になっていません: %s", p)
		}
		if r["point_value"].(float64) != 100 || r["product"] != "NK225mini" || r["symbol"] != "NK225" {
			t.Fatalf("商品単位と参照原板が不正です: %+v", r)
		}
		if e, ok := r["entry_method_id"].(string); ok && e != "" {
			methods[e] = true
		}
	}
	if len(paths) != 1 || !reflect.DeepEqual(methods, map[string]bool{"NK225_入口A": true, "NK225_入口B": true, "NK225_入口C": true}) {
		t.Fatalf("手法別コピー又は返済だけの孤児資源: paths=%v methods=%v", paths, methods)
	}
	// ----------------------------------------
	// 読取サーバーは明示した試験用URLだけを使用し、通常試験は通信なしです。
	if viewerURL := os.Getenv("SESSION_VIEWER_URL"); viewerURL != "" {
		bridgeVerifyViewer(t, viewerURL, root, catalog, gross, fees)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.store.PublishViewer(logRoot, bridgeAt(10)); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !bytes.Equal(first, again) || !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("無変更の出力でファイルを再保存しました")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// 新しい約定で再投影の候補になっても、削除済みの過去資料は復活させません。
	submit(bridgeIntent("削除後新規", "buy", "new", "NK225_削除後", 11, 1, 65010))
	lateClose := bridgeIntent("削除後返済", "sell", "close", "NK225_終了担当", 12, 1, 65020)
	lateClose.ClosePositions = []controller.ClosePosition{{HoldID: "dry-削除後新規", Quantity: 1}}
	submit(lateClose)
	if err = m.store.PublishViewer(logRoot, bridgeAt(13)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("削除した出力を復活しました", err)
	}
	if api.calls != 0 {
		t.Fatal("削除後の仮想売買が外部へ送信されました")
	}
}

// bridgeVerifyViewer は生成資料をViewer本人へ渡し、全体と入口手法別の会計を照合します。
//
// 引数: t(*testing.T)、base/root(string)、catalog(map[string]any)、gross/fees(float64)。返り値: なし。
func bridgeVerifyViewer(t *testing.T, base, root string, catalog map[string]any, gross, fees float64) {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		t.Fatal("SESSION_VIEWER_URLが不正です")
	}
	book := filepath.Join(root, "人工原板.jsonl")
	f, err := os.Create(book)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(f)
	for minute := 0; minute <= 10; minute++ {
		for _, offset := range []time.Duration{0, 30 * time.Second} {
			at := bridgeAt(minute).Add(offset)
			if err = encoder.Encode(map[string]any{"CurrentPriceTime": at.Format(time.RFC3339Nano), "CurrentPrice": 65020, "Buy1": map[string]float64{"Price": 65010}, "Sell1": map[string]float64{"Price": 65030}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	resources := catalog["resources"].([]any)
	for _, raw := range resources {
		raw.(map[string]any)["market_path"] = book
	}
	client := &http.Client{Timeout: 15 * time.Second}
	for _, basis := range []string{"gross", "net"} {
		for _, raw := range resources {
			raw.(map[string]any)["pnl_basis"] = basis
		}
		body, err := json.Marshal(map[string]any{"catalog": catalog})
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Post(strings.TrimRight(base, "/")+"/api/catalogs/load", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var loaded struct {
			CatalogID string `json:"catalog_id"`
			Error     string `json:"error"`
		}
		err = json.NewDecoder(response.Body).Decode(&loaded)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("Viewerの一覧読込失敗: %d %+v %v", response.StatusCode, loaded, err)
		}
		sum := 0.0
		for _, raw := range resources {
			r := raw.(map[string]any)
			query := url.Values{"catalog": {loaded.CatalogID}, "resource": {r["id"].(string)}, "width": {"300"}}
			response, err = client.Get(strings.TrimRight(base, "/") + "/api/data?" + query.Encode())
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Summary struct {
					Long       *int     `json:"long_qty"`
					Short      *int     `json:"short_qty"`
					Realized   *float64 `json:"realized_pnl_yen"`
					Unrealized *float64 `json:"unrealized_pnl_yen"`
				} `json:"summary"`
				Error  string           `json:"error"`
				Events []map[string]any `json:"events"`
			}
			err = json.NewDecoder(response.Body).Decode(&out)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("Viewerで実出力を読めません: %s %d %s %v", r["id"], response.StatusCode, out.Error, err)
			}
			if out.Summary.Long == nil || out.Summary.Short == nil || out.Summary.Realized == nil || out.Summary.Unrealized == nil || *out.Summary.Long != 0 || *out.Summary.Short != 0 || *out.Summary.Unrealized != 0 {
				t.Fatalf("管理外又は返済玉を保有し続けました: %+v", out.Summary)
			}
			entry, _ := r["entry_method_id"].(string)
			if entry == "" {
				want := gross
				if basis == "net" {
					want -= fees
				}
				if math.Abs(*out.Summary.Realized-want) > 1e-7 || len(out.Events) != 9 {
					t.Fatalf("全体会計の不一致: basis=%s PL=%v want=%v events=%d", basis, *out.Summary.Realized, want, len(out.Events))
				}
			} else {
				sum += *out.Summary.Realized
				if basis == "gross" {
					want := map[string]float64{"NK225_入口A": 4000, "NK225_入口B": -4000, "NK225_入口C": 6000}[entry]
					if math.Abs(*out.Summary.Realized-want) > 1e-7 {
						t.Fatalf("手法を跨ぐ返済の帰属不一致: %s got=%v want=%v", entry, *out.Summary.Realized, want)
					}
				}
			}
			t.Logf("Viewer本人: %s/%s PL=%+.4f円、残玉0、%dイベント", basis, fmt.Sprint(r["method_id"]), *out.Summary.Realized, len(out.Events))
		}
		want := gross
		if basis == "net" {
			want -= fees
		}
		if math.Abs(sum-want) > 1e-7 {
			t.Fatalf("手法別合計が全体と不一致: %s got=%v want=%v", basis, sum, want)
		}
	}
}
