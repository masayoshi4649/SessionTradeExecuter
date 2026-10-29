package runner

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
)

// bootstrapFixture は二市場・二足幅の同一世代と検査用原文を作ります。
//
// 引数: t *testing.T は試験です。
// 返り値: config.Config、candleBundle、履歴map、開始と境界のtime.Timeです。
func bootstrapFixture(t *testing.T) (config.Config, candleBundle, map[string]controller.BookHistory, time.Time, time.Time) {
	t.Helper()
	start := time.Date(2026, 10, 21, 8, 45, 0, 0, time.FixedZone("JST", 9*3600))
	cutoff := start.Add(720 * time.Second)
	cfg := config.Defaults()
	cfg.Markets = map[string]config.Market{"TOPIX": {Enabled: true, Product: "TOPIX", MaxPerSide: 10, CoreQuantity: 5, ExtraQuantity: 5}}
	cfg.Products = map[string]config.Product{"TOPIX": {Tick: .5, PointValue: 10000}, "NK225": {Tick: 10, PointValue: 1000}}
	cfg.Algorithm["features.atr_min_bars"] = 1
	bundle := candleBundle{One: map[string]controller.Candles{}, Five: map[string]controller.Candles{}}
	histories := map[string]controller.BookHistory{}
	for _, reference := range []string{"TOPIX", "NK225"} {
		snap := controller.Snapshot{SessionID: "2026-10-21_Day", Generation: "generation-1", Code: reference,
			AsOf: cutoff, Sequence: 100, Session: controller.Session{StartsAt: start, EndsAt: start.Add(7 * time.Hour)}}
		price := 100.
		if reference == "NK225" {
			price = 200.
		}
		first := controller.Candle{Time: start, Open: price, High: price + 4, Low: price - 2, Close: price, Closed: true}
		bundle.One[reference] = controller.Candles{Snapshot: snap, Data: []controller.Candle{first}}
		bundle.Five[reference] = controller.Candles{Snapshot: snap, Data: []controller.Candle{first}}
		histories[reference] = controller.BookHistory{Snapshot: snap}
	}
	return cfg, bundle, histories, start, cutoff
}

// bootstrapBoard はJSON復号を通して未知項目も含む原文付き板を作ります。
//
// 引数: t *testing.T、at string、price float64 は試験・価格時計・現在値です。
// 返り値: controller.Board は受信原文付きの板です。
func bootstrapBoard(t *testing.T, at string, price float64) controller.Board {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"CurrentPriceTime": at, "CurrentPrice": price,
		"Buy1":  map[string]any{"Price": price - .5, "Qty": 10},
		"Sell1": map[string]any{"Price": price + .5, "Qty": 12}, "Unknown": "原文の追加情報"})
	if err != nil {
		t.Fatal(err)
	}
	var board controller.Board
	if err = json.Unmarshal(raw, &board); err != nil {
		t.Fatal(err)
	}
	return board
}

// bootstrapStore は試験専用の市場正本を作ります。
//
// 引数: t *testing.T は試験です。
// 返り値: *journal.Store、string、string は保存器・DBパス・ログディレクトリです。
func bootstrapStore(t *testing.T) (*journal.Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	dbpath, logs := filepath.Join(dir, "market.sqlite"), filepath.Join(dir, "market-log")
	store, err := journal.Open(dbpath, logs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, dbpath, logs
}

// TestBootstrapClockOrderAndPreservation は原配列逆行を捨てず保存し採用列だけ安定整列します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestBootstrapClockOrderAndPreservation(t *testing.T) {
	cfg, bundle, histories, start, cutoff := bootstrapFixture(t)
	top := histories["TOPIX"]
	for _, pair := range []struct {
		second int
		price  float64
	}{{2, 102}, {1, 9999}, {2, 103}, {-1, 101}, {10, 0}, {360, 104}} {
		top.Data = append(top.Data, bootstrapBoard(t, start.Add(time.Duration(pair.second)*time.Second).Format(time.RFC3339Nano), pair.price))
	}
	top.Data = append(top.Data, bootstrapBoard(t, "", 105))
	histories["TOPIX"] = top
	nk := histories["NK225"]
	for _, second := range []int{1, 2, 359, 360} {
		nk.Data = append(nk.Data, bootstrapBoard(t, start.Add(time.Duration(second)*time.Second).Format(time.RFC3339Nano), 200+float64(second)/1000))
	}
	histories["NK225"] = nk
	store, dbpath, logs := bootstrapStore(t)
	received := cutoff.Add(5 * time.Second)
	result, err := prepareHistory(context.Background(), cfg, bundle, histories, store, cutoff, received)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 7 {
		t.Fatal("暖機行数が不正です", len(result.Rows))
	}
	expected := []string{"NK225", "TOPIX", "TOPIX", "NK225", "NK225", "TOPIX", "NK225"}
	for i, observation := range result.Rows {
		if observation.Reference != expected[i] || observation.Received != observation.Time ||
			(i > 0 && observation.Time < result.Rows[i-1].Time) {
			t.Fatal("安定順または暖機時計が不正です", i, observation)
		}
		for _, market := range []float64{observation.LastMarket.PeerTime, observation.MidMarket.PeerTime} {
			if market != 0 && market >= observation.Time {
				t.Fatal("同秒または未来の相手板を参照しました")
			}
		}
	}
	if result.Last["TOPIX"].ATR != 6 || result.Last["NK225"].ATR != 20 {
		t.Fatal("特徴設定の上書きか名目公開待機が反映されていません")
	}
	db, err := sql.Open("sqlite", dbpath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow("SELECT count(*) FROM books").Scan(&count); err != nil || count != 11 {
		t.Fatal("除外原板を保存していません", count, err)
	}
	var at, source string
	if err = db.QueryRow("SELECT received_at,source FROM books ORDER BY id LIMIT 1").Scan(&at, &source); err != nil ||
		at != received.Format(time.RFC3339Nano) || source != "起動履歴" {
		t.Fatal("暖機時計と実取込時計を混同しました", err)
	}
	var payload []byte
	if err = db.QueryRow("SELECT payload FROM events WHERE kind='履歴準備完了'").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Inputs map[string][]int64        `json:"observation_input_ids"`
		Counts map[string]map[string]int `json:"counts"`
	}
	if err = json.Unmarshal(payload, &detail); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(detail.Inputs["TOPIX"], []int64{1, 3, 6}) || detail.Counts["TOPIX"]["reversed"] != 1 {
		t.Fatal("採用原文と特徴Indexの対応が不正です", string(payload))
	}
	// ----------------------------------------
	// 同時計の異なる板も原文指紋で区別し、特徴連番と正本の対応を確認します。
	for reference, inputIDs := range detail.Inputs {
		if len(result.SourceHashes[reference]) != len(inputIDs) {
			t.Fatal("原文指紋と採用連番の件数が異なります", reference)
		}
		for index, inputID := range inputIDs {
			var raw []byte
			if err = db.QueryRow("SELECT raw FROM books WHERE id=?", inputID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(raw)
			if result.SourceHashes[reference][index] != fmt.Sprintf("%x", digest) {
				t.Fatal("原文指紋が採用した原行と一致しません", reference, index)
			}
		}
	}
	if result.SourceHashes["TOPIX"][0] == result.SourceHashes["TOPIX"][1] {
		t.Fatal("同時計の異なる原文を同一として扱いました")
	}
	if !strings.Contains(string(payload), `"historical_orders":0`) {
		t.Fatal("SQLiteに過去発注なしの記録がありません")
	}
	if _, err = os.Stat(filepath.Join(logs, "trace.jsonl")); !os.IsNotExist(err) {
		t.Fatal("廃止したtrace.jsonlを生成しました", err)
	}
}

// TestBootstrapRejectsMixedGeneration は全6応答の世代と立会を照合します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestBootstrapRejectsMixedGeneration(t *testing.T) {
	for _, mode := range []string{"板世代", "足世代", "立会終了", "欠落"} {
		t.Run(mode, func(t *testing.T) {
			cfg, bundle, histories, _, cutoff := bootstrapFixture(t)
			switch mode {
			case "板世代":
				row := histories["NK225"]
				row.Snapshot.Generation = "generation-2"
				histories["NK225"] = row
			case "足世代":
				row := bundle.Five["NK225"]
				row.Snapshot.Generation = "generation-2"
				bundle.Five["NK225"] = row
			case "立会終了":
				row := bundle.Five["TOPIX"]
				row.Snapshot.Session.EndsAt = row.Snapshot.Session.EndsAt.Add(time.Minute)
				bundle.Five["TOPIX"] = row
			case "欠落":
				delete(histories, "TOPIX")
			}
			store, _, _ := bootstrapStore(t)
			if _, err := prepareHistory(context.Background(), cfg, bundle, histories, store, cutoff, cutoff); err == nil {
				t.Fatal("混在した履歴を受理しました")
			}
		})
	}
}

// TestBootstrapNoFutureAndRelease は将来足の毒化が既存観測を変えず公開前ATRを使わないことを確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestBootstrapNoFutureAndRelease(t *testing.T) {
	cfg, bundle, histories, start, cutoff := bootstrapFixture(t)
	for _, reference := range []string{"TOPIX", "NK225"} {
		item := histories[reference]
		for _, second := range []int{359, 360, 700, 800} {
			item.Data = append(item.Data, bootstrapBoard(t, start.Add(time.Duration(second)*time.Second).Format(time.RFC3339Nano), 100))
		}
		histories[reference] = item
	}
	store, _, _ := bootstrapStore(t)
	first, err := prepareHistory(context.Background(), cfg, bundle, histories, store, cutoff, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range first.Rows {
		if o.Time < bootstrapSeconds(start.Add(360*time.Second)) && o.ATR != 0 {
			t.Fatal("名目確定遅延の前にATRが公開されました")
		}
	}
	// ----------------------------------------
	// 既存の価格時刻より後に公開される足と板を大きく変え、既存接頭を比較する。
	for _, reference := range []string{"TOPIX", "NK225"} {
		later := bundle.Five[reference]
		later.Data = append(later.Data, controller.Candle{Time: start.Add(600 * time.Second), Open: 99999,
			High: 199999, Low: 1, Close: 99999, Closed: true})
		later.Snapshot.AsOf = start.Add(1200 * time.Second)
		bundle.Five[reference] = later
		one := bundle.One[reference]
		one.Data = append(one.Data, controller.Candle{Time: start.Add(780 * time.Second), Open: 99999,
			High: 199999, Low: 1, Close: 99999, Closed: true})
		one.Snapshot.AsOf = later.Snapshot.AsOf
		bundle.One[reference] = one
	}
	nextStore, _, _ := bootstrapStore(t)
	second, err := prepareHistory(context.Background(), cfg, bundle, histories, nextStore,
		start.Add(1200*time.Second), start.Add(1201*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Rows, second.Rows[:len(first.Rows)]) {
		t.Fatal("将来の足または板が暖機接頭を変更しました")
	}
}

// TestBootstrapBatchPersistence は1000行を超える履歴と中断を扱います。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestBootstrapBatchPersistence(t *testing.T) {
	cfg, bundle, histories, start, cutoff := bootstrapFixture(t)
	item := histories["TOPIX"]
	for i := 0; i < 1001; i++ {
		item.Data = append(item.Data, bootstrapBoard(t, start.Add(time.Duration(i)*time.Millisecond).Format(time.RFC3339Nano), 100))
	}
	histories["TOPIX"] = item
	store, dbpath, _ := bootstrapStore(t)
	result, err := prepareHistory(context.Background(), cfg, bundle, histories, store, cutoff, cutoff)
	if err != nil || len(result.Rows) != 1001 {
		t.Fatal("複数バッチの保存または暖機失敗", err)
	}
	db, err := sql.Open("sqlite", dbpath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow("SELECT count(*) FROM events WHERE kind='入力原文保存'").Scan(&count); err != nil || count != 2 {
		t.Fatal("1000行バッチになっていません", count, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = prepareHistory(ctx, cfg, bundle, histories, store, cutoff, cutoff); err == nil {
		t.Fatal("中断した履歴処理を継続しました")
	}
}
