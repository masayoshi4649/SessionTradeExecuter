package webui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

// storageCheckpoint は人工約定の確認位置と復元順序を照合する保存状態です。
type storageCheckpoint struct {
	AckCount    int      `json:"ack_count"`
	LastTradeID string   `json:"last_trade_id"`
	Orders      []string `json:"orders"`
}

// storageViewerTrade は公開JSONLの所有ID・価格・時刻だけを検証する読取型です。
type storageViewerTrade struct {
	ID         string  `json:"id"`
	Timestamp  string  `json:"timestamp"`
	Type       string  `json:"type"`
	PositionID string  `json:"position_id"`
	Price      float64 `json:"price"`
	Qty        int     `json:"qty"`
}

// storageConcurrentTrades は相互に異なる40所有玉の人工新規・全返済を生成します。
//
// 引数: end(time.Time)は人工場の終了時計です。
// 返り値: []journal.Tradeは80約定、float64二つは独立計算した粗利益・粗損失、intは新規数量合計です。
func storageConcurrentTrades(end time.Time) ([]journal.Trade, float64, float64, int) {
	const owners = 40
	trades := make([]journal.Trade, 0, owners*2)
	profit, loss, entries := 0.0, 0.0, 0
	for index := 0; index < owners; index++ {
		quantity := 1 + index%3
		price := 65000 + float64(index*10)
		side, closeSide := "buy", "sell"
		if index%2 != 0 {
			side, closeSide = "sell", "buy"
		}
		method := "NK225_人工入口A"
		if index%2 != 0 {
			method = "NK225_人工入口B"
		}
		entry := journal.Trade{ID: fmt.Sprintf("人工新規-%02d", index), At: end.Add(-2 * time.Hour).Add(time.Duration(index*2) * time.Second),
			Session: "2026-10-28_Day", Symbol: "人工限月", Product: "NK225mini", Owner: method, MethodID: method,
			TradeType: "new", Side: side, Quantity: quantity, Price: price, HoldID: fmt.Sprintf("所有玉-%02d", index), DryRun: true}
		entry.ExecutionID = "約定/" + entry.ID
		entry.ClientOrderID = "意図/" + entry.ID
		gain := 2.0
		if index%3 == 0 {
			gain = -1
		}
		closePrice := price + gain
		if side == "sell" {
			closePrice = price - gain
		}
		pnl := gain * float64(quantity) * 100
		close := journal.Trade{ID: fmt.Sprintf("人工返済-%02d", index), At: entry.At.Add(time.Second),
			Session: entry.Session, Symbol: entry.Symbol, Product: entry.Product, Owner: "別の返済担当", MethodID: "NK225_人工返済",
			TradeType: "close", Side: closeSide, Quantity: quantity, Price: closePrice, PnL: pnl, DryRun: true,
			Closed: []controller.ClosePosition{{HoldID: entry.HoldID, Quantity: quantity}}}
		close.ExecutionID = "約定/" + close.ID
		close.ClientOrderID = "意図/" + close.ID
		trades = append(trades, entry, close)
		entries += quantity
		if pnl > 0 {
			profit += pnl
		} else {
			loss -= pnl
		}
	}
	return trades, profit, loss, entries
}

// TestWebStoredOutputsConcurrentWithOpenWriter は実際の商品保存先への保存とWeb派生出力を並行させます。
//
// 引数: t(*testing.T)は人工一時DBを管理します。
// 返り値: なし。40所有玉の保存回帰であり、一日実運転や実controller接続の検証ではありません。
// 売買worker・controller.Clientは生成せず、Webの出力処理だけを別goroutineから呼びます。
func TestWebStoredOutputsConcurrentWithOpenWriter(t *testing.T) {
	cfg := testConfig(t)
	var workerCalls atomic.Int32
	manager, err := NewManager(cfg, func(context.Context, config.Config, *runner.Control, func(runner.Snapshot)) error {
		workerCalls.Add(1)
		return fmt.Errorf("保存試験から売買workerを起動してはいけません")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	logRoot := cfg.Path(cfg.Runtime.LogDirectory)
	database := filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), "dryrun", "nk225mini", "NK225mini.sqlite")
	writer, err := journal.Open(database, "")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	end := time.Date(2026, 10, 28, 15, 45, 0, 0, time.FixedZone("日本時間", 9*60*60))
	trades, wantProfit, wantLoss, wantEntries := storageConcurrentTrades(end)
	if err := writer.Save(storageCheckpoint{Orders: []string{}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := writer.RegisterReportSession(trades[0].Session, true, "NK225mini", end, 100); err != nil {
		t.Fatal(err)
	}

	// ----------------------------------------
	// 各保存と同じ開始合図で出力を解放し、開いたwriterと別接続の出力を80回重ねます。
	type outputRound struct {
		at    time.Time
		start chan struct{}
		done  chan struct{}
	}
	rounds := make(chan outputRound)
	reporterDone := make(chan struct{})
	go func() {
		defer close(reporterDone)
		for round := range rounds {
			<-round.start
			manager.finalizeStoredSessionReports(round.at)
			close(round.done)
		}
	}()
	defer func() {
		close(rounds)
		<-reporterDone
	}()
	for count := 1; count <= len(trades); count++ {
		round := outputRound{at: end.Add(time.Duration(count) * 5 * time.Second), start: make(chan struct{}), done: make(chan struct{})}
		rounds <- round
		close(round.start)
		saveErr := writer.Save(storageCheckpoint{AckCount: count, LastTradeID: trades[count-1].ID, Orders: []string{}}, trades[:count])
		<-round.done
		if saveErr != nil {
			t.Fatalf("並行保存%dで運転側の保存が失敗しました: %v", count, saveErr)
		}
	}
	manager.finalizeStoredSessionReports(end.Add(time.Hour))
	if state := manager.State(); state.ReportError != "" {
		t.Fatalf("保存完了後もWebの保存待ち表示が残っています: %s", state.ReportError)
	}
	if workerCalls.Load() != 0 {
		t.Fatal("派生出力の確認が売買workerを起動しました")
	}

	// ----------------------------------------
	// 正本の状態と全約定本文を保存順のまま復元し、出力処理による欠落・改変を検出します。
	var checkpoint storageCheckpoint
	loaded, err := writer.Load(&checkpoint)
	actual, actualErr := json.Marshal(loaded)
	expected, expectedErr := json.Marshal(trades)
	if err != nil || actualErr != nil || expectedErr != nil || !bytes.Equal(actual, expected) || checkpoint.AckCount != len(trades) || checkpoint.LastTradeID != trades[len(trades)-1].ID {
		t.Fatalf("並行出力で正本の状態・約定順序が変わりました: 件数%d 状態%+v エラー%v/%v/%v", len(loaded), checkpoint, err, actualErr, expectedErr)
	}
	pl, err := journal.ReadSessionPL(logRoot)
	if err != nil || len(pl) != 1 || pl[0].Product != "NK225mini" || pl[0].Mode != "dryrun" || pl[0].Session != trades[0].Session || pl[0].EntryQty != wantEntries || pl[0].Profit != wantProfit || pl[0].Loss != wantLoss || pl[0].Net != wantProfit-wantLoss {
		t.Fatalf("終了CSVの件数・独立会計が不一致です: %+v / %v", pl, err)
	}
	csvPath := filepath.Join(logRoot, "NK225mini", trades[0].Session+"_dryrun.csv")
	csvBytes, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	csvRows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(csvBytes, []byte("\xef\xbb\xbf")))).ReadAll()
	if err != nil || len(csvRows) != len(trades)+1 {
		t.Fatalf("CSV全約定件数が不一致です: %d / %v", len(csvRows), err)
	}
	for index, row := range csvRows[1:] {
		if len(row) != 6 || row[3] != trades[index].At.Format(time.RFC3339Nano) || row[2] != strconv.Itoa(trades[index].Quantity) {
			t.Fatalf("CSVの約定順序・数量が不一致です: %d %+v", index, row)
		}
	}

	// ----------------------------------------
	// Viewerの全約定片を同じ所有玉へ対応づけ、入口担当以外の返済でも所有が崩れないか調べます。
	viewerPath := filepath.Join(logRoot, "NK225mini", trades[0].Session+"_dryrun.trades.jsonl")
	viewerBytes, err := os.ReadFile(viewerPath)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(viewerBytes))
	ids, positions := map[string]bool{}, map[string]int{}
	rows, realized := 0, 0.0
	for scanner.Scan() {
		var row storageViewerTrade
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if rows >= len(trades) {
			t.Fatal("Viewerに余分な約定片があります")
		}
		trade := trades[rows]
		owner := trades[rows-rows%2]
		at, err := time.Parse(time.RFC3339Nano, row.Timestamp)
		if err != nil || !at.Equal(trade.At) || row.ID == "" || ids[row.ID] || row.PositionID != owner.HoldID || row.Qty != trade.Quantity || row.Price != trade.Price {
			t.Fatalf("Viewerの所有ID・順序・重複が不一致です: %d %+v", rows, row)
		}
		ids[row.ID] = true
		wantType := "LongN"
		if owner.Side == "sell" {
			wantType = "ShortN"
		}
		if rows%2 == 0 {
			positions[row.PositionID] += row.Qty
		} else {
			wantType = "LongR"
			if owner.Side == "sell" {
				wantType = "ShortR"
			}
			if positions[row.PositionID] != row.Qty {
				t.Fatal("Viewerが別の玉又は未保有数量を返済しました")
			}
			positions[row.PositionID] -= row.Qty
			sign := 1.0
			if owner.Side == "sell" {
				sign = -1
			}
			realized += sign * (row.Price - owner.Price) * float64(row.Qty) * 100
		}
		if row.Type != wantType {
			t.Fatalf("Viewer売買種別が不一致です: %s / %s", row.Type, wantType)
		}
		rows++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if rows != len(trades) || len(positions) != 40 || math.Abs(realized-(wantProfit-wantLoss)) > 0.000001 {
		t.Fatalf("Viewer全約定・所有玉・独立会計が不一致です: 約定%d 所有玉%d 損益%g", rows, len(positions), realized)
	}
	for owner, remaining := range positions {
		if remaining != 0 {
			t.Fatalf("Viewerに閉じ残しがあります: %s / %d", owner, remaining)
		}
	}
	catalogPath := filepath.Join(logRoot, "sessionviewer.resources.json")
	catalogBytes, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Resources []struct {
			ID       string `json:"id"`
			Product  string `json:"product"`
			PNLPath  string `json:"pnl_path"`
			MethodID string `json:"method_id"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(catalogBytes, &catalog); err != nil {
		t.Fatal(err)
	}
	resourceIDs, paths, methods := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, resource := range catalog.Resources {
		if resourceIDs[resource.ID] || resource.Product != "NK225mini" {
			t.Fatal("Viewer列挙に重複又は別商品があります")
		}
		resourceIDs[resource.ID] = true
		paths[resource.PNLPath] = true
		methods[resource.MethodID] = true
		if filepath.Clean(filepath.Join(logRoot, filepath.FromSlash(resource.PNLPath))) != filepath.Clean(viewerPath) {
			t.Fatal("Viewer参照が共通約定ファイルを指していません", resource.PNLPath)
		}
	}
	if len(resourceIDs) != 3 || len(paths) != 1 || !reflect.DeepEqual(methods, map[string]bool{"*": true, "NK225_人工入口A": true, "NK225_人工入口B": true}) {
		t.Fatalf("全体・入口2担当の参照又は共通約定ファイルが不一致です: %+v", catalog)
	}

	// ----------------------------------------
	// 同じ確定事実を再確認してもCSV・全約定・列挙の件数と内容を増やしません。
	manager.finalizeStoredSessionReports(end.Add(2 * time.Hour))
	for path, before := range map[string][]byte{csvPath: csvBytes, viewerPath: viewerBytes, catalogPath: catalogBytes} {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("同じ事実の再出力で内容が変わりました: %s / %v", path, err)
		}
	}
	if again, err := journal.ReadSessionPL(logRoot); err != nil || !reflect.DeepEqual(again, pl) || manager.State().ReportError != "" {
		t.Fatal("再出力で場集計が重複又は保存待ちへ戻りました", again, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), "live")); !os.IsNotExist(err) {
		t.Fatal("保存回帰試験が実売買の保存先を生成しました", err)
	}
}
