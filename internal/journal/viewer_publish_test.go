package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// viewerTestTrade は所有証跡を持つ当場の人工新規約定を作ります。
//
// 引数: id/product(string)、dry(bool)、offset(time.Duration)。返り値: Trade。
func viewerTestTrade(id, product string, dry bool, offset time.Duration) Trade {
	at := reportTestEnd().Add(-6 * time.Hour).Add(offset)
	return Trade{ID: id, At: at, Session: "2026-10-21_Day", Product: product, Symbol: product, Owner: "core", MethodID: "入口A", TradeType: "new", Side: "buy", Quantity: 2, Price: 100, Fee: 83, HoldID: "玉/" + id, ExecutionID: "約定/" + id, DryRun: dry}
}

// viewerTestRows は最小JSONLの全約定片を読みます。
//
// 引数: t(*testing.T)、path(string)。返り値: []viewerTrade。
func viewerTestRows(t *testing.T, path string) []viewerTrade {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	scan := bufio.NewScanner(bytes.NewReader(raw))
	rows := []viewerTrade{}
	for scan.Scan() {
		var row viewerTrade
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

// viewerTestCatalog はリンク列挙を取得します。
//
// 引数: t(*testing.T)、root(string)。返り値: viewerCatalog。
func viewerTestCatalog(t *testing.T, root string) viewerCatalog {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "sessionviewer.resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value viewerCatalog
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

// TestViewerOnlyOptedSessionsAndChangedFacts は既存CSVの場を勝手に移行せず、場中の確定事実だけ出すか検査します。
//
// 引数: t(*testing.T)。返り値: なし。snapshot更新だけではJSONLを触りません。
func TestViewerOnlyOptedSessionsAndChangedFacts(t *testing.T) {
	s, dbPath, _ := testStore(t)
	root := t.TempDir()
	trade := viewerTestTrade("初回", "TOPIXmini", true, 0)
	saveReportTrades(t, s, []Trade{trade}, "done", 1)
	if err := s.RegisterReportSession(trade.Session, true, trade.Product, reportTestEnd()); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishViewer(root, trade.At); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("未登録場を出力しました", entries, err)
	}
	if err := s.RegisterReportSession(trade.Session, true, trade.Product, reportTestEnd(), 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishViewer(root, trade.At); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, trade.Product, trade.Session+"_dryrun.trades.jsonl")
	rows := viewerTestRows(t, path)
	if len(rows) != 1 || rows[0].PositionID != trade.HoldID || rows[0].Type != "LongN" {
		t.Fatal(rows)
	}
	catalog := viewerTestCatalog(t, root)
	if catalog.PathBase != "catalog" || catalog.BaseDir != root || len(catalog.Resources) != 2 || catalog.Resources[0].PointValue != 1000 {
		t.Fatal(catalog)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Save(map[string]any{"snapshotだけ更新": true}, []Trade{trade}); err != nil {
		t.Fatal(err)
	}
	if err = s.PublishViewer(root, trade.At.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = PublishStoredViewer(dbPath, root, trade.At.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil || before.ModTime() != after.ModTime() {
		t.Fatal("変化のない約定を再出力しました", err)
	}
	if _, err = os.Stat(filepath.Join(root, "sessionPL.csv")); !os.IsNotExist(err) {
		t.Fatal("場中のViewer出力から終了CSVを作りました", err)
	}
}

// TestViewerAllocationAndLifecycle は複数対象返済の所有・手数料・移管の重複を確認します。
//
// 引数: t(*testing.T)。返り値: なし。移管に価格・架空PLを付けません。
func TestViewerAllocationAndLifecycle(t *testing.T) {
	s, _, _ := testStore(t)
	root := t.TempDir()
	a := viewerTestTrade("先玉", "NK225mini", true, 0)
	b := viewerTestTrade("後玉", "NK225mini", true, time.Minute)
	b.Quantity, b.Price, b.MethodID = 1, 120, "入口B"
	close := viewerTestTrade("複合返済", "NK225mini", true, 2*time.Minute)
	close.TradeType, close.Side, close.Quantity, close.Price, close.PnL, close.Fee = "close", "sell", 2, 130, 4000, 77
	close.HoldID = ""
	close.Closed = []controller.ClosePosition{{HoldID: b.HoldID, Quantity: 1}, {HoldID: a.HoldID, Quantity: 1}}
	saveReportTrades(t, s, []Trade{a, b, close}, "done", 3)
	position := map[string]any{"hold_id": a.HoldID, "product": a.Product, "session": a.Session, "side": "buy", "quantity": 1}
	if err := s.Event("保有玉の管理外移管", a.Session, "NK225", "", close.At.Add(time.Minute), map[string]any{"dryrun": true, "positions": []any{position}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Event("起動時状態切替", "", "", "", close.At.Add(2*time.Minute), map[string]any{"metadata": map[string]any{"dryrun": true, "previous_holdings": []any{position}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterReportSession(a.Session, true, a.Product, reportTestEnd(), 100); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishViewer(root, close.At.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	rows := viewerTestRows(t, filepath.Join(root, a.Product, a.Session+"_dryrun.trades.jsonl"))
	if len(rows) != 5 || rows[2].PositionID != b.HoldID || rows[3].PositionID != a.HoldID || rows[2].FeeYen+rows[3].FeeYen != 77 || rows[4].Type != "Detach" || rows[4].Qty != 1 || rows[4].Price != nil || rows[4].FeeYen != 0 {
		t.Fatal(rows)
	}
	if len(viewerTestCatalog(t, root).Resources) != 3 {
		t.Fatal("入口の異なる2手法の参照がありません")
	}
}

// TestViewerDeletionLateTradeAndNewScene は削除資料・削除参照の復活を防ぎ、新場だけ追加します。
//
// 引数: t(*testing.T)。返り値: なし。遅着を反映しても既存の他資源を消しません。
func TestViewerDeletionLateTradeAndNewScene(t *testing.T) {
	s, _, _ := testStore(t)
	root := t.TempDir()
	trade := viewerTestTrade("旧場", "TOPIXmini", true, 0)
	trades := []Trade{trade}
	saveReportTrades(t, s, trades, "done", 1)
	if err := s.RegisterReportSession(trade.Session, true, trade.Product, reportTestEnd(), 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishViewer(root, trade.At); err != nil {
		t.Fatal(err)
	}
	listPath := filepath.Join(root, "sessionviewer.resources.json")
	catalog := viewerTestCatalog(t, root)
	kept := []viewerResource{}
	for _, item := range catalog.Resources {
		if item.MethodID == "*" {
			kept = append(kept, item)
		}
	}
	catalog.Resources = kept
	raw, _ := json.Marshal(catalog)
	if err := os.WriteFile(listPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	late := viewerTestTrade("遅着返済1", "TOPIXmini", true, time.Minute)
	late.TradeType, late.Side, late.Quantity, late.Price, late.PnL = "close", "sell", 1, 110, 10000
	late.HoldID = ""
	late.Closed = []controller.ClosePosition{{HoldID: trade.HoldID, Quantity: 1}}
	trades = append(trades, late)
	saveReportTrades(t, s, trades, "done", 2)
	if err := s.PublishViewer(root, late.At); err != nil {
		t.Fatal(err)
	}
	if got := viewerTestCatalog(t, root); len(got.Resources) != 1 || got.Resources[0].MethodID != "*" {
		t.Fatal("削除済み手法参照を復活しました", got)
	}
	if err := os.Remove(listPath); err != nil {
		t.Fatal(err)
	}
	late.ID, late.ExecutionID, late.At = "遅着返済2", "遅着約定2", late.At.Add(time.Minute)
	trades = append(trades, late)
	saveReportTrades(t, s, trades, "done", 3)
	if err := s.PublishViewer(root, late.At); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(listPath); !os.IsNotExist(err) {
		t.Fatal("削除済み一覧を復活しました", err)
	}
	tradePath := filepath.Join(root, trade.Product, trade.Session+"_dryrun.trades.jsonl")
	if err := os.Remove(tradePath); err != nil {
		t.Fatal(err)
	}
	newTrade := viewerTestTrade("同場の後続", "TOPIXmini", true, 3*time.Minute)
	newTrade.MethodID = "入口C"
	trades = append(trades, newTrade)
	saveReportTrades(t, s, trades, "done", 4)
	if err := s.PublishViewer(root, newTrade.At); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tradePath); !os.IsNotExist(err) {
		t.Fatal("削除済み約定JSONLを復活しました", err)
	}
	night := viewerTestTrade("新場", "TOPIXmini", true, 0)
	night.Session = "2026-10-21_Night"
	night.At = reportTestEnd().Add(2 * time.Hour)
	trades = append(trades, night)
	saveReportTrades(t, s, trades, "done", 5)
	if err := s.RegisterReportSession(night.Session, true, night.Product, reportTestEnd().Add(14*time.Hour+15*time.Minute), 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishViewer(root, night.At); err != nil {
		t.Fatal(err)
	}
	for _, item := range viewerTestCatalog(t, root).Resources {
		if item.Session != night.Session {
			t.Fatal("過去の削除参照を新場で復活しました", item)
		}
	}
}

// TestViewerStableLateClockAndNoFabricatedClose は遅着時刻を整列し、因果不備や旧不十分な返済を拒否します。
//
// 引数: t(*testing.T)。返り値: なし。正本順序は不変です。
func TestViewerStableLateClockAndNoFabricatedClose(t *testing.T) {
	s, _, _ := testStore(t)
	root := t.TempDir()
	a := viewerTestTrade("確認先", "NK225micro", false, 2*time.Minute)
	b := viewerTestTrade("確認後", "NK225micro", false, time.Minute)
	saveReportTrades(t, s, []Trade{a, b}, "done", 2)
	if err := s.RegisterReportSession(a.Session, false, a.Product, reportTestEnd(), 10); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishViewer(root, a.At); err != nil {
		t.Fatal(err)
	}
	rows := viewerTestRows(t, filepath.Join(root, a.Product, a.Session+".trades.jsonl"))
	if rows[0].PositionID != b.HoldID || rows[1].PositionID != a.HoldID {
		t.Fatal(rows)
	}
	loaded, err := s.Load(&map[string]any{})
	if err != nil || loaded[0].ID != a.ID || loaded[1].ID != b.ID {
		t.Fatal("正本を並べ替えました", loaded, err)
	}
	close := viewerTestTrade("対象不明", "NK225micro", false, 3*time.Minute)
	close.TradeType, close.Side = "close", "sell"
	close.HoldID = ""
	saveReportTrades(t, s, []Trade{a, b, close}, "done", 3)
	if err = s.PublishViewer(root, close.At); err == nil {
		t.Fatal("返済対象を推測しました")
	}
	if len(viewerTestRows(t, filepath.Join(root, a.Product, a.Session+".trades.jsonl"))) != 2 {
		t.Fatal("不十分な返済を派生出力しました")
	}
	for _, bad := range []Trade{func() Trade {
		x := close
		x.Closed = []controller.ClosePosition{{HoldID: a.HoldID, Quantity: 2}}
		x.PnL = 999
		return x
	}(), func() Trade {
		x := close
		x.Closed = []controller.ClosePosition{{HoldID: a.HoldID, Quantity: 3}}
		return x
	}()} {
		facts := []viewerFact{{At: a.At, Trade: &a}, {At: bad.At, Trade: &bad}}
		if _, _, err = buildViewerTrades(viewerSession{PointValue: 10, Session: a.Session, Product: a.Product}, facts); err == nil {
			t.Fatal("不一致会計を通しました", bad)
		}
	}
}

// TestViewerParallelProductsAndModes は別DBから同じ一覧への更新が相互消失しないか確認します。
//
// 引数: t(*testing.T)。返り値: なし。商品2種類・実仮想2種類を並行で出力します。
func TestViewerParallelProductsAndModes(t *testing.T) {
	root := t.TempDir()
	stores := []*Store{}
	for _, product := range []string{"TOPIXmini", "NK225mini"} {
		for _, dry := range []bool{false, true} {
			s, _, _ := testStore(t)
			trade := viewerTestTrade(product, product, dry, 0)
			saveReportTrades(t, s, []Trade{trade}, "done", 1)
			point := 100.0
			if product == "TOPIXmini" {
				point = 1000
			}
			if err := s.RegisterReportSession(trade.Session, dry, product, reportTestEnd(), point); err != nil {
				t.Fatal(err)
			}
			stores = append(stores, s)
		}
	}
	var wait sync.WaitGroup
	failures := make(chan error, len(stores))
	for _, s := range stores {
		wait.Add(1)
		go func(store *Store) {
			defer wait.Done()
			failures <- store.PublishViewer(root, reportTestEnd().Add(-time.Hour))
		}(s)
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := viewerTestCatalog(t, root); len(got.Resources) != 8 {
		t.Fatal("並行更新で資源が消失しました", got)
	}
	count := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	if err != nil || count != 6 {
		t.Fatal("必要以上のファイルを生成しました", count, err)
	}
}

// TestViewerPublishRetryAndPostSessionDetach は保存障害の再試行と場末へ遡及しない管理外移管を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestViewerPublishRetryAndPostSessionDetach(t *testing.T) {
	s, _, _ := testStore(t)
	root := t.TempDir()
	trade := viewerTestTrade("再試行", "NK225micro", true, 0)
	saveReportTrades(t, s, []Trade{trade}, "done", 1)
	if err := s.RegisterReportSession(trade.Session, true, trade.Product, reportTestEnd(), 10); err != nil {
		t.Fatal(err)
	}
	if err := s.Event("保有玉の管理外移管", trade.Session, "NK225", "", reportTestEnd().Add(time.Minute), map[string]any{"dryrun": true, "positions": []viewerPosition{{HoldID: trade.HoldID, Product: trade.Product, Session: trade.Session, Side: "buy", Quantity: 2}}}); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(root, "sessionviewer.resources.json")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishViewer(root, reportTestEnd().Add(time.Hour)); err == nil {
		t.Fatal("出力障害を隠しました")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishViewer(root, reportTestEnd().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows := viewerTestRows(t, filepath.Join(root, trade.Product, trade.Session+"_dryrun.trades.jsonl"))
	if len(rows) != 1 || rows[0].Type != "LongN" {
		t.Fatal("場末に架空の移管を作りました", rows)
	}
}

// TestViewerBadSceneDoesNotBlockGoodScene は不十分な旧場の再試行と正常新場の出力を両立します。
//
// 引数: t(*testing.T)。返り値: なし。エラーは失敗場を明記し、正常場の出力済み位置は独立確定します。
func TestViewerBadSceneDoesNotBlockGoodScene(t *testing.T) {
	s, _, _ := testStore(t)
	root := t.TempDir()
	bad := viewerTestTrade("旧場の不十分返済", "TOPIXmini", true, time.Minute)
	bad.TradeType, bad.Side = "close", "sell"
	bad.HoldID = ""
	good := viewerTestTrade("新場の証明済み新規", "TOPIXmini", true, 0)
	good.Session = "2026-10-21_Night"
	good.At = reportTestEnd().Add(2 * time.Hour)
	saveReportTrades(t, s, []Trade{bad, good}, "done", 2)
	if err := s.RegisterReportSession(bad.Session, true, bad.Product, reportTestEnd(), 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterReportSession(good.Session, true, good.Product, reportTestEnd().Add(14*time.Hour+15*time.Minute), 1000); err != nil {
		t.Fatal(err)
	}
	err := s.PublishViewer(root, good.At)
	if err == nil || !strings.Contains(err.Error(), bad.Session) {
		t.Fatal("失敗場を明記した出力エラーがありません", err)
	}
	path := filepath.Join(root, good.Product, good.Session+"_dryrun.trades.jsonl")
	if rows := viewerTestRows(t, path); len(rows) != 1 || rows[0].Type != "LongN" {
		t.Fatal("正常な新場が旧場のエラーで出ません", rows)
	}
	catalog := viewerTestCatalog(t, root)
	if len(catalog.Resources) != 2 {
		t.Fatal(catalog)
	}
	var oldEmitted, newEmitted bool
	if err = s.db.QueryRow(`SELECT emitted FROM viewer_sessions WHERE session=?`, bad.Session).Scan(&oldEmitted); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT emitted FROM viewer_sessions WHERE session=?`, good.Session).Scan(&newEmitted); err != nil {
		t.Fatal(err)
	}
	if oldEmitted || !newEmitted {
		t.Fatal("場ごとの出力位置が独立していません", oldEmitted, newEmitted)
	}
	before, _ := os.Stat(path)
	if err = s.PublishViewer(root, good.At.Add(time.Minute)); err == nil {
		t.Fatal("不十分な旧場の再試行が消えました")
	}
	after, _ := os.Stat(path)
	if before.ModTime() != after.ModTime() {
		t.Fatal("正常場まで再出力しました")
	}
}

// TestViewerLifecycleUsesSmallPartialIndex は大量の通常イベントを移管確認で走査しないか検査します。
//
// 引数: t(*testing.T)。返り値: なし。索引は移管2種類だけを対象とします。
func TestViewerLifecycleUsesSmallPartialIndex(t *testing.T) {
	s, _, _ := testStore(t)
	for _, query := range []string{
		`EXPLAIN QUERY PLAN SELECT seq,kind,at FROM events WHERE kind IN ('保有玉の管理外移管','起動時状態切替') ORDER BY seq`,
		`EXPLAIN QUERY PLAN SELECT 1 FROM events WHERE kind IN ('保有玉の管理外移管','起動時状態切替') AND seq>123`,
	} {
		rows, err := s.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if strings.Contains(detail, "events_viewer_lifecycle") {
				found = true
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		if !found {
			t.Fatal("移管だけの索引が使われません", query)
		}
	}
}
