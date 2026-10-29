package strategy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// replayInput146 は当時の価格時計を持つ一件の保存原板です。
type replayInput146 struct {
	board Board
}

// TestCurrent146Replay は指定済み当日履歴を因果的な特徴と売買へ接続します。
//
// 引数: t(*testing.T)。環境変数SESSIONTRADER_CURRENT146は取得済みデータのフォルダです。
// 返り値: なし。既定ではスキップし、実API・未来約定・途中データの強制清算は使いません。
func TestCurrent146Replay(t *testing.T) {
	dir := os.Getenv("SESSIONTRADER_CURRENT146")
	if dir == "" {
		t.Skip("当日再生は入力と事前計画を指定して実行します")
	}
	start, _ := time.Parse(time.RFC3339, "2026-10-26T08:45:00+09:00")
	end, _ := time.Parse(time.RFC3339, "2026-10-26T15:45:00+09:00")
	sec := func(at time.Time) float64 { return float64(at.UnixNano()) / 1e9 }
	session := Session{ID: "2026-10-26_Day", Kind: "Day", Start: sec(start), End: sec(end), ForceClose: sec(end.Add(-10 * time.Minute)), ExtraAt: sec(start.Add(225 * time.Minute))}
	params, err := ParametersForMethod("NK225", "Day", "TG04")
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewFeatures(session, map[string]float64{"NK225": 10, "TOPIX": .5}, params.Features)
	if err != nil {
		t.Fatal(err)
	}
	f.EnableResearch()
	inputs := []replayInput146{}
	for _, ref := range []string{"TOPIX", "NK225"} {
		for _, interval := range []int{60, 300} {
			name := "1min"
			if interval == 300 {
				name = "5min"
			}
			raw, readErr := os.ReadFile(filepath.Join(dir, ref+"_candles_"+name+".json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var data controller.Candles
			if err = json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			for _, b := range data.Data {
				if !b.Closed {
					continue
				}
				c := Candle{Time: sec(b.Time), Open: b.Open, High: b.High, Low: b.Low, Close: b.Close, Closed: true}
				if _, err = f.AddCandles(ref, interval, []Candle{c}, sec(b.Time)+float64(interval)+params.Features.CandleDelay); err != nil {
					t.Fatal(err)
				}
			}
		}
		file, openErr := os.Open(filepath.Join(dir, ref+"_book.jsonl"))
		if openErr != nil {
			t.Fatal(openErr)
		}
		scan := bufio.NewScanner(file)
		scan.Buffer(make([]byte, 65536), 4*1024*1024)
		previous := session.Start
		for scan.Scan() {
			var b controller.Board
			if err = json.Unmarshal(scan.Bytes(), &b); err != nil {
				t.Fatal(err)
			}
			at, parseErr := time.Parse(time.RFC3339Nano, b.CurrentPriceTime)
			if parseErr != nil || b.CurrentPrice <= 0 || sec(at) < previous || at.Before(start) || !at.Before(end) {
				continue
			}
			previous = sec(at)
			row := Board{Reference: ref, Time: previous, Received: previous, Last: b.CurrentPrice, Bid: b.Buy1.Price, Ask: b.Sell1.Price, BidQuantity: b.Buy1.Qty, AskQuantity: b.Sell1.Qty}
			buy := []controller.Level{b.Buy1, b.Buy2, b.Buy3, b.Buy4, b.Buy5, b.Buy6, b.Buy7, b.Buy8, b.Buy9, b.Buy10}
			sell := []controller.Level{b.Sell1, b.Sell2, b.Sell3, b.Sell4, b.Sell5, b.Sell6, b.Sell7, b.Sell8, b.Sell9, b.Sell10}
			for k := range buy {
				row.BidLevels[k] = ResearchLevel{Price: buy[k].Price, Quantity: buy[k].Qty}
				row.AskLevels[k] = ResearchLevel{Price: sell[k].Price, Quantity: sell[k].Qty}
			}
			inputs = append(inputs, replayInput146{row})
		}
		if err = scan.Err(); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	sort.SliceStable(inputs, func(i, j int) bool { return inputs[i].board.Time < inputs[j].board.Time })
	portfolios := map[int]*Portfolio{}
	pending := map[int][]Signal{}
	outputs := map[int]*json.Encoder{}
	files := []*os.File{}
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	for _, cap := range []int{2, 10} {
		p, createErr := New(Config{Reference: "NK225", Product: "NK225mini", Cap: cap, CoreQuantity: max(1, cap/2), ExtraMaxQuantity: max(1, cap/2), LargeTick: 10, PointValue: 100, Parameters: params}, session)
		if createErr != nil {
			t.Fatal(createErr)
		}
		portfolios[cap] = p
		file, createErr := os.Create(filepath.Join(dir, fmt.Sprintf("再生TG04_%d枚.jsonl", cap)))
		if createErr != nil {
			t.Fatal(createErr)
		}
		files = append(files, file)
		outputs[cap] = json.NewEncoder(file)
	}
	lastSample := map[int]float64{}
	peaks := map[int]map[string]float64{2: {}, 10: {}}
	var last Observation
	for _, row := range inputs {
		o, observeErr := f.OnBoard(row.board)
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		if o.Reference != "NK225" {
			continue
		}
		last = o
		for _, cap := range []int{2, 10} {
			p := portfolios[cap]
			if quoteValid(o) {
				for _, sig := range pending[cap] {
					fill := Fill{SignalID: sig.ID, ExecutionID: sig.ID + "約定", HoldID: sig.ID + "玉", Closed: sig.Targets, Quantity: sig.Quantity, Price: liquidation(o, -sig.Side), Time: o.Time, Fee: math.Floor(float64(sig.Quantity) * 38.5)}
					if err = p.OnFill(fill); err != nil {
						t.Fatal(err)
					}
					if err = p.OnOrderDone(sig.ID); err != nil {
						t.Fatal(err)
					}
					if err = outputs[cap].Encode(map[string]any{"kind": "約定", "signal": sig, "fill": fill}); err != nil {
						t.Fatal(err)
					}
				}
				pending[cap] = nil
			}
			signals, observeErr := p.Observe(o, true)
			if observeErr != nil {
				t.Fatal(observeErr)
			}
			pending[cap] = append(pending[cap], signals...)
			newPeak := false
			for _, s := range p.Research.Sleeves {
				if s.held() > s.Capacity {
					t.Fatal("担当上限違反")
				}
				if !quoteValid(o) || s.held() == 0 {
					continue
				}
				amount := 0.
				for _, l := range s.Lots {
					amount += float64(s.Side*l.Quantity) * (liquidation(o, s.Side) - l.Price) * 100
				}
				key := fmt.Sprintf("%s/%d", s.Name, s.Campaign)
				if amount > peaks[cap][key] {
					peaks[cap][key], newPeak = amount, true
				}
			}
			if newPeak || len(signals) > 0 || o.Time-lastSample[cap] >= 15 {
				if err = outputs[cap].Encode(map[string]any{"kind": "出口状態", "time": o.Time, "bid": o.Bid, "ask": o.Ask, "diagnostics": p.ExitDiagnostics(o)}); err != nil {
					t.Fatal(err)
				}
				lastSample[cap] = o.Time
			}
		}
	}
	for _, cap := range []int{2, 10} {
		if err = outputs[cap].Encode(map[string]any{"kind": "途中終端", "time": last.Time, "peaks": peaks[cap], "status": portfolios[cap].Status(), "diagnostics": portfolios[cap].ExitDiagnostics(last)}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("原板%d行、最終価格時計%s。途中終端の建玉は清算していません", len(inputs), time.Unix(int64(last.Time), 0).In(time.FixedZone("JST", 9*3600)).Format(time.RFC3339))
}
