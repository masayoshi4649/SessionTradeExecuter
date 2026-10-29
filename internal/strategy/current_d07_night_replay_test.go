package strategy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// idleD07Case は指標のみを共有し、起動前の売買状態を持ち越さない独立再生です。
type idleD07Case struct {
	Start                    float64
	Label                    string
	Portfolio                *Portfolio
	Sources                  map[string]*ResearchSource
	Pending                  []Signal
	Encoder                  *json.Encoder
	Previous                 map[string]string
	ReasonRows               map[string]map[string]int
	FirstArm, FirstConfirmed map[string]string
	MaxConfirmation          map[string]float64
	Signals, Fills           []map[string]any
	SourceEvents             []map[string]any
	Rows                     int
	LastSample               float64
	FirstKnown               string
}

// idleD07Second は日本時間等の明示時計をUNIX小数秒へ変換します。
//
// 引数: at(time.Time)。返り値: float64はUNIX秒です。
func idleD07Second(at time.Time) float64 { return float64(at.UnixNano()) / 1e9 }

// idleD07Clock は内部時計を日本時間の監査表記へ変換します。
//
// 引数: at(float64)はUNIX秒。返り値: stringは日本時間、未定義は空文字です。
func idleD07Clock(at float64) string {
	if at <= 0 || !finite(at) {
		return ""
	}
	return time.Unix(0, int64(at*1e9)).In(time.FixedZone("JST", 9*3600)).Format(time.RFC3339Nano)
}

// idleD07Write は因果的な診断と約定仮定をJSONLへ保存します。
//
// 引数: t(*testing.T)、enc(*json.Encoder)、value(any)。返り値: なし。
func idleD07Write(t *testing.T, enc *json.Encoder, value any) {
	t.Helper()
	if err := enc.Encode(value); err != nil {
		t.Fatal(err)
	}
}

// idleD07State は売買状態を変更せず、入口の準備段階と未成立条件を抽出します。
//
// 引数: p(*Portfolio)、s(*ResearchSleeve)、o(Observation)。
// 返り値: map[string]anyは詳細、stringは変化検出用署名、stringは集計用の待機理由です。
func idleD07State(p *Portfolio, s *ResearchSleeve, o Observation) (map[string]any, string, string) {
	f := o.Research
	missing := []int{}
	for i := 0; i < 24; i++ {
		if !f.Known[i] || !finite(f.Values[i]) {
			missing = append(missing, i)
		}
	}
	d := map[string]any{"owner": s.Name, "capacity": s.Capacity, "actual_quantity": s.held(),
		"features_known": len(missing) == 0, "unknown_columns": missing, "blocked": s.Blocked,
		"quote_valid": quoteValid(o), "last": o.Last, "bid": o.Bid, "ask": o.Ask,
		"feature_values": f.Values, "feature_known_flags": f.Known}
	reason := "入口条件待ち"
	if s.Source.Legacy != nil {
		l := s.Source.Legacy.Core
		ready := p.Session.USOpen + s.Source.Legacy.Config.Parameters.Night.PostOpenPrepareSeconds
		d["arm"], d["stage"], d["arm_time"] = l.Arm.Side, l.Arm.Stage, idleD07Clock(l.Arm.At)
		d["first_entry_allowed_at"] = idleD07Clock(ready)
		d["fallback_allowed_at"] = idleD07Clock(p.Session.ExtraAt)
		d["virtual_quantity"] = l.Position.Quantity
		if o.Time < ready {
			reason = "米国開始後22分まで主担当新規禁止"
		} else if l.Arm.Side != 0 {
			reason = "主担当の突破後再試験待ち"
		}
		if s.held() > 0 {
			reason = "保有中"
		}
		d["reason"] = reason
		return d, fmt.Sprintf("%s/%d/%d/%d", reason, l.Arm.Side, l.Arm.Stage, s.held()), reason
	}
	a := s.Source.Active
	if s.Source.Active145 != nil {
		a = &s.Source.Active145.ResearchActive
	}
	if a == nil {
		d["reason"] = "対象外の担当"
		return d, "対象外", "対象外"
	}
	chosen := 4
	if a.Guard {
		chosen = -1
		if f.Values[2] >= .35 {
			chosen = 1
		} else if f.Values[2] <= .2 {
			chosen = 4
		}
		if a.D07Rules || a.ResearchRules {
			chosen = int(a.entryNumber("addon.entry.mode", map[string]any{"efficiency": f.Values[2]}))
		}
	}
	d["selected_mode"], d["arm"], d["stage"], d["arm_mode"] = chosen, a.Arm, a.Stage, a.ArmMode
	d["arm_time"], d["arm_level"], d["arm_risk"] = idleD07Clock(a.ArmTime), a.ArmLevel, a.ArmRisk
	d["since_known"], d["since"], d["next_ready"] = a.SinceKnown, idleD07Clock(a.Since), idleD07Clock(a.NextReady)
	d["extreme"], d["first_low"], d["pivot"] = a.Extreme, a.FirstLow, a.Pivot
	d["virtual_quantity"], d["virtual_pending"] = a.Book.total(), len(a.Book.Pending)
	d["initial_quantity"] = a.Initial
	d["direction_efficiency_40m"], d["high_40m"], d["low_40m"] = f.Values[2], f.Values[0], f.Values[1]
	d["current_risk_points"] = f.Values[9]
	d["own_30s_r"], d["own_120s_r"], d["peer_30s_r"], d["peer_120s_r"] = f.Values[12], f.Values[13], f.Values[20], f.Values[21]
	d["confirmation_elapsed_seconds"] = 0.0
	if a.SinceKnown {
		d["confirmation_elapsed_seconds"] = o.Time - a.Since
	}
	if a.Arm != 0 {
		direction := float64(a.Arm)
		mid := (o.Bid + o.Ask) * .5
		d["signed_boundary_distance"] = direction * (mid - a.ArmLevel)
		d["signed_rebound_distance"] = direction * (mid - a.Extreme)
		d["signed_pullback_distance"] = direction * (a.Pivot - mid)
		d["second_extreme_improvement"] = direction * (a.Extreme - a.FirstLow)
		d["support_30s_now"] = a.entryBool("addon.entry.support30", direction*f.Values[12] >= 0 && direction*f.Values[20] >= 0,
			map[string]any{"own30": direction * f.Values[12], "peer30": direction * f.Values[20]})
		d["support_120s_now"] = a.entryBool("addon.entry.support120", direction*f.Values[13] >= 0 && direction*f.Values[21] >= 0,
			map[string]any{"own120": direction * f.Values[13], "peer120": direction * f.Values[21]})
		d["peer_40m_support_now"] = a.Arm == int(sign(f.Values[15]))
		d["standard_rule_levels"] = map[string]any{"breakout_progress": a.ArmLevel + direction*.5*a.ArmRisk,
			"breakout_retest": a.ArmLevel + direction*.25*a.ArmRisk, "rebound_quarter_r": a.Extreme + direction*.25*a.ArmRisk,
			"range_pullback": a.Pivot - direction*.125*a.ArmRisk, "range_second_rebound": a.Extreme + direction*.125*a.ArmRisk}
	}
	switch {
	case !quoteValid(o):
		reason = "正常気配待ち"
	case len(missing) > 0:
		reason = "二市場の40分帯と短期指標の準備待ち"
	case s.held() > 0 || a.Book.total() > 0:
		reason = "保有中"
	case len(a.Book.Pending) > 0:
		reason = "仮想候補の次行約定待ち"
	case o.Time < a.NextReady:
		reason = "返済後の再準備待ち"
	case o.Time >= p.Session.USOpen-900 && o.Time < p.Session.USOpen+900:
		reason = "米国開始前後15分の静観"
	case o.Ask-o.Bid > .25*f.Values[9]:
		reason = "気配差が許容幅超過"
	case chosen == -1:
		reason = "方向効率が往復型と突破型の中間"
	case a.Arm == 0:
		reason = "40分帯の入口候補待ち"
	case a.SinceKnown:
		reason = "入口成立の30秒連続確認中"
	case a.Stage == 0:
		reason = "第一反発または突破後進行待ち"
	case a.Stage == 1:
		reason = "第一反発または突破後の押し戻り待ち"
	case a.Stage == 2:
		reason = "再反発と二市場支持の成立待ち"
	}
	d["reason"] = reason
	key := fmt.Sprintf("%s/%v/%d/%d/%d/%t/%d/%d/%d", reason, missing, chosen, a.Arm, a.Stage, a.SinceKnown, a.Book.total(), len(a.Book.Pending), s.held())
	return d, key, reason
}

// TestCurrentD07NightReplay は取得済み二市場原板をD07へ順送りし、途中起動別の発注有無を確認します。
//
// 引数: t(*testing.T)。SESSIONTRADER_CURRENT_D07_NIGHTは6入力のフォルダです。
// SESSIONTRADER_D07_RULESは実配布ルール、SESSIONTRADER_CURRENT_D07_CUTOFFは排他的終端です。
// 返り値: なし。通信はせず、ラージの次正常気配による仮想約定と手数料0を明示します。
func TestCurrentD07NightReplay(t *testing.T) {
	dir := os.Getenv("SESSIONTRADER_CURRENT_D07_NIGHT")
	if dir == "" {
		t.Skip("取得済み夜間原板と実ルールを明示した場合だけ再生します")
	}
	rulePath := os.Getenv("SESSIONTRADER_D07_RULES")
	if rulePath == "" {
		t.Fatal("SESSIONTRADER_D07_RULESに実配布NK225_D07.tomlを指定してください")
	}
	loaded, err := rules.Load(rulePath)
	if err != nil {
		t.Fatal(err)
	}
	cutoffText := os.Getenv("SESSIONTRADER_CURRENT_D07_CUTOFF")
	if cutoffText == "" {
		cutoffText = "2026-10-26T18:37:11+09:00"
	}
	cutoff, err := time.Parse(time.RFC3339Nano, cutoffText)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse(time.RFC3339, "2026-10-26T17:00:00+09:00")
	end := start.Add(13 * time.Hour)
	session := Session{ID: "2026-10-26_Night", Kind: "Night", Start: idleD07Second(start), End: idleD07Second(end),
		ForceClose: idleD07Second(end.Add(-10 * time.Minute)), USOpen: idleD07Second(start.Add(330 * time.Minute)), ExtraAt: idleD07Second(start.Add(360 * time.Minute))}
	params, err := ParametersForMethod("NK225", "Night", "D07")
	if err != nil {
		t.Fatal(err)
	}
	features, err := NewFeatures(session, map[string]float64{"NK225": 10, "TOPIX": .5}, params.Features)
	if err != nil {
		t.Fatal(err)
	}
	features.EnableResearch()
	inputs := []Board{}
	counts := map[string]map[string]int{}
	for _, reference := range []string{"TOPIX", "NK225"} {
		counts[reference] = map[string]int{}
		for _, interval := range []int{60, 300} {
			name := "1min"
			if interval == 300 {
				name = "5min"
			}
			raw, readErr := os.ReadFile(filepath.Join(dir, reference+"_candles_"+name+".json"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var data controller.Candles
			if err = json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			if data.Snapshot.SessionID != session.ID {
				t.Fatal("足のセッションが異なります", reference, data.Snapshot.SessionID)
			}
			sort.SliceStable(data.Data, func(i, j int) bool { return data.Data[i].Time.Before(data.Data[j].Time) })
			for _, bar := range data.Data {
				barEnd := bar.Time.Add(time.Duration(interval) * time.Second)
				if !bar.Closed || bar.Time.Before(start) || barEnd.After(cutoff) || barEnd.After(data.Snapshot.AsOf) {
					continue
				}
				candle := Candle{Time: idleD07Second(bar.Time), Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Closed: true}
				if _, err = features.AddCandles(reference, interval, []Candle{candle}, idleD07Second(barEnd)+params.Features.CandleDelay); err != nil {
					t.Fatal(err)
				}
				counts[reference][name]++
			}
		}
		file, openErr := os.Open(filepath.Join(dir, reference+"_book.jsonl"))
		if openErr != nil {
			t.Fatal(openErr)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 65536), 4*1024*1024)
		previous := time.Time{}
		for scanner.Scan() {
			counts[reference]["raw"]++
			var b controller.Board
			if err = json.Unmarshal(scanner.Bytes(), &b); err != nil {
				t.Fatal(err)
			}
			at, parseErr := time.Parse(time.RFC3339Nano, b.CurrentPriceTime)
			if parseErr != nil {
				counts[reference]["invalid_clock"]++
				continue
			}
			if at.Before(start) || !at.Before(end) || !at.Before(cutoff) {
				counts[reference]["outside"]++
				continue
			}
			if !previous.IsZero() && at.Before(previous) {
				counts[reference]["reversed"]++
				continue
			}
			if !finite(b.CurrentPrice) || b.CurrentPrice <= 0 {
				counts[reference]["invalid_price"]++
				continue
			}
			if at.Equal(previous) {
				counts[reference]["same_clock"]++
			}
			previous = at
			board := Board{Reference: reference, Time: idleD07Second(at), Received: idleD07Second(at), Last: b.CurrentPrice,
				Bid: b.Buy1.Price, Ask: b.Sell1.Price, BidQuantity: b.Buy1.Qty, AskQuantity: b.Sell1.Qty}
			buys := []controller.Level{b.Buy1, b.Buy2, b.Buy3, b.Buy4, b.Buy5, b.Buy6, b.Buy7, b.Buy8, b.Buy9, b.Buy10}
			sells := []controller.Level{b.Sell1, b.Sell2, b.Sell3, b.Sell4, b.Sell5, b.Sell6, b.Sell7, b.Sell8, b.Sell9, b.Sell10}
			for k := range buys {
				board.BidLevels[k] = ResearchLevel{Price: buys[k].Price, Quantity: buys[k].Qty}
				board.AskLevels[k] = ResearchLevel{Price: sells[k].Price, Quantity: sells[k].Qty}
			}
			inputs = append(inputs, board)
			counts[reference]["accepted"]++
		}
		scanErr := scanner.Err()
		file.Close()
		if scanErr != nil {
			t.Fatal(scanErr)
		}
	}
	sort.SliceStable(inputs, func(i, j int) bool { return inputs[i].Time < inputs[j].Time })
	// ----------------------------------------
	// 全ケースは同じ公開済み特徴を読むが、売買状態は指定起動時点で初めて動かします。
	outputDir := filepath.Join(filepath.Dir(dir), "D07夜間再生")
	if err = os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}
	starts := []string{"17:00", "17:40", "18:00", "18:10", "18:20", "18:30"}
	if supplied := os.Getenv("SESSIONTRADER_CURRENT_D07_STARTS"); supplied != "" {
		starts = strings.Split(supplied, ",")
	}
	cases := []*idleD07Case{}
	for _, label := range starts {
		caseStart, parseErr := time.Parse(time.RFC3339, "2026-10-26T"+strings.TrimSpace(label)+":00+09:00")
		if parseErr != nil || caseStart.Before(start) || !caseStart.Before(cutoff) {
			t.Fatal("起動時刻が範囲外です", label, parseErr)
		}
		portfolio, createErr := New(Config{Reference: "NK225", Product: "NK225mini", CoreQuantity: 5, ExtraMaxQuantity: 5,
			Cap: 10, LargeTick: 10, PointValue: 100, Parameters: params, RuleExpressions: loaded.Expressions()}, session)
		if createErr != nil {
			t.Fatal(createErr)
		}
		file, createErr := os.Create(filepath.Join(outputDir, "起動"+strings.ReplaceAll(label, ":", "")+"_判断推移.jsonl"))
		if createErr != nil {
			t.Fatal(createErr)
		}
		defer file.Close()
		c := &idleD07Case{Start: idleD07Second(caseStart), Label: label, Portfolio: portfolio, Encoder: json.NewEncoder(file),
			Previous: map[string]string{}, ReasonRows: map[string]map[string]int{}, FirstArm: map[string]string{}, FirstConfirmed: map[string]string{},
			Signals: []map[string]any{}, Fills: []map[string]any{}, Sources: map[string]*ResearchSource{}, SourceEvents: []map[string]any{}, MaxConfirmation: map[string]float64{}}
		for _, sleeve := range portfolio.Research.Sleeves {
			c.Sources[sleeve.Name], err = newResearchSource(portfolio.Config, session, sleeve.Name, sleeve.Capacity)
			if err != nil {
				t.Fatal(err)
			}
		}
		cases = append(cases, c)
		idleD07Write(t, c.Encoder, map[string]any{"kind": "再生条件", "session": session, "start": caseStart, "cutoff_exclusive": cutoff,
			"rule_file": rulePath, "capacity": 10, "allocation": ResearchAllocation("D07", 10),
			"fill_assumption": "売買シグナルの次の正常ラージ板気配で仮想約定。miniの実約定・受信遅延・通信・口座を再現しない。手数料0。途中終端の強制清算なし。",
			"warmup":          "起動前は指標のみ。当時足の名目公開遅延を適用。過去Arm・仮想建玉・注文・利益予算は引き継がない。"})
	}
	var last Observation
	firstKnown := ""
	knownRows := 0
	for _, board := range inputs {
		o, observeErr := features.OnBoard(board)
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		if o.Reference != "NK225" {
			continue
		}
		last = o
		known := quoteValid(o)
		for k := 0; k < 24; k++ {
			known = known && o.Research.Known[k] && finite(o.Research.Values[k])
		}
		if known {
			knownRows++
			if firstKnown == "" {
				firstKnown = idleD07Clock(o.Time)
			}
		}
		for _, c := range cases {
			if o.Time < c.Start {
				continue
			}
			c.Rows++
			if known && c.FirstKnown == "" {
				c.FirstKnown = idleD07Clock(o.Time)
			}
			p := c.Portfolio
			// 実発注採用の前段にある独立源も同じ板だけで進め、外側ガードとの混同を防ぎます。
			for _, owner := range []string{"core", "addon", "episode"} {
				events, sourceErr := c.Sources[owner].Step(o)
				if sourceErr != nil {
					t.Fatal(c.Label, owner, sourceErr)
				}
				for _, sourceEvent := range events {
					event := map[string]any{"kind": "独立源の候補", "time_jst": idleD07Clock(o.Time), "time": o.Time,
						"owner": owner, "action": sourceEvent.Action, "event": sourceEvent}
					c.SourceEvents = append(c.SourceEvents, event)
					idleD07Write(t, c.Encoder, event)
				}
			}
			if quoteValid(o) {
				for _, sig := range c.Pending {
					if o.Index <= sig.SourceIndex {
						t.Fatal("判断した行で約定しています")
					}
					fill := Fill{SignalID: sig.ID, ExecutionID: sig.ID + "-仮想約定", HoldID: sig.ID + "-仮想玉", Closed: sig.Targets,
						Quantity: sig.Quantity, Price: liquidation(o, -sig.Side), Time: o.Time, Fee: 0}
					if err = p.OnFill(fill); err != nil {
						t.Fatal(c.Label, err)
					}
					if err = p.OnOrderDone(sig.ID); err != nil {
						t.Fatal(c.Label, err)
					}
					event := map[string]any{"kind": "仮想約定", "time_jst": idleD07Clock(o.Time), "owner": sig.Owner, "action": sig.Action, "signal": sig, "fill": fill}
					c.Fills = append(c.Fills, event)
					idleD07Write(t, c.Encoder, event)
				}
				c.Pending = nil
			}
			signals, observeErr := p.Observe(o, true)
			if observeErr != nil {
				t.Fatal(c.Label, observeErr)
			}
			c.Pending = append(c.Pending, signals...)
			for _, sig := range signals {
				event := map[string]any{"kind": "発注判断", "time_jst": idleD07Clock(o.Time), "owner": sig.Owner, "action": sig.Action, "signal": sig}
				c.Signals = append(c.Signals, event)
				idleD07Write(t, c.Encoder, event)
			}
			periodic := o.Time-c.LastSample >= 60
			for _, sleeve := range p.Research.Sleeves {
				detail, key, reason := idleD07State(p, sleeve, o)
				if c.ReasonRows[sleeve.Name] == nil {
					c.ReasonRows[sleeve.Name] = map[string]int{}
				}
				c.ReasonRows[sleeve.Name][reason]++
				if arm, ok := detail["arm"].(int); ok && arm != 0 && c.FirstArm[sleeve.Name] == "" {
					c.FirstArm[sleeve.Name] = idleD07Clock(o.Time)
				}
				if confirming, _ := detail["since_known"].(bool); confirming && c.FirstConfirmed[sleeve.Name] == "" {
					c.FirstConfirmed[sleeve.Name] = idleD07Clock(o.Time)
				}
				if elapsed, ok := detail["confirmation_elapsed_seconds"].(float64); ok {
					c.MaxConfirmation[sleeve.Name] = math.Max(c.MaxConfirmation[sleeve.Name], elapsed)
				}
				if key != c.Previous[sleeve.Name] || periodic || len(signals) > 0 {
					idleD07Write(t, c.Encoder, map[string]any{"kind": "入口状態", "time_jst": idleD07Clock(o.Time), "index": o.Index, "detail": detail})
					c.Previous[sleeve.Name] = key
				}
				if sleeve.held() > sleeve.Capacity {
					t.Fatal("担当上限違反", c.Label, sleeve.Name)
				}
			}
			if periodic {
				c.LastSample = o.Time
			}
		}
	}
	// ----------------------------------------
	// 最終状態と18時33分09秒時点の発注小計を、全約定と分けて保存します。
	results := []map[string]any{}
	stateAt, _ := time.Parse(time.RFC3339, "2026-10-26T18:33:09+09:00")
	for _, c := range cases {
		byOwner := map[string]map[string]int{}
		beforeState := map[string]int{}
		sourceByOwner := map[string]map[string]int{}
		sourceBeforeState := map[string]int{}
		for _, event := range c.SourceEvents {
			owner, action := event["owner"].(string), event["action"].(string)
			if sourceByOwner[owner] == nil {
				sourceByOwner[owner] = map[string]int{}
			}
			sourceByOwner[owner][action]++
			if event["time"].(float64) <= idleD07Second(stateAt) {
				sourceBeforeState[owner+"_"+action]++
			}
		}
		for _, event := range c.Signals {
			sig := event["signal"].(Signal)
			if byOwner[sig.Owner] == nil {
				byOwner[sig.Owner] = map[string]int{}
			}
			byOwner[sig.Owner][sig.Action]++
			if sig.Time <= idleD07Second(stateAt) {
				beforeState[sig.Owner+"_"+sig.Action]++
			}
		}
		finalDetails := []map[string]any{}
		for _, s := range c.Portfolio.Research.Sleeves {
			d, _, _ := idleD07State(c.Portfolio, s, last)
			finalDetails = append(finalDetails, d)
		}
		result := map[string]any{"start": idleD07Clock(c.Start), "processed_nk_rows": c.Rows,
			"first_features_known": c.FirstKnown, "first_arm": c.FirstArm, "first_confirmation": c.FirstConfirmed,
			"max_confirmation_seconds": c.MaxConfirmation,
			"signal_counts":            byOwner, "signals_through_183309": beforeState, "signals": c.Signals, "fills": c.Fills,
			"source_event_counts": sourceByOwner, "source_events_through_183309": sourceBeforeState, "source_events": c.SourceEvents,
			"reason_row_counts": c.ReasonRows, "final_status": c.Portfolio.Status(), "final_entry_details": finalDetails,
			"pending_count": len(c.Pending), "last_time": idleD07Clock(last.Time)}
		results = append(results, result)
		idleD07Write(t, c.Encoder, map[string]any{"kind": "途中終端", "result": result})
		t.Logf("起動%s 発注%d 約定%d 最初の指標成立%s", c.Label, len(c.Signals), len(c.Fills), c.FirstKnown)
	}
	if math.IsNaN(last.Time) || len(inputs) == 0 || firstKnown == "" {
		t.Fatal("検証に必要な有効入力がありません")
	}
	summary := map[string]any{"session": session.ID, "cutoff_exclusive": cutoff, "last_nk_time": idleD07Clock(last.Time),
		"input_counts": counts, "first_features_known": firstKnown, "fully_known_nk_rows": knownRows,
		"rule_file": rulePath, "results": results, "limitations": "約定はラージ次正常気配の仮想値。生PUSH受信順・mini実約定・実口座・当時の通信障害は再現していない。時刻同値は取得元順、二市場同値はTOPIXから保存順で処理し、同時計相手情報は使用しない。"}
	raw, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(outputDir, "再生結果.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
