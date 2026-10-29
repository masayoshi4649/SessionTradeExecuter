package strategy

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/e01"
)

// testPortfolio は外部商品規格を与えた検査器を作ります。
//
// 引数: t(*testing.T)、symbol/kind(string)。返り値: *Portfolio です。
func testPortfolio(t *testing.T, symbol, kind string) *Portfolio {
	t.Helper()
	q, cap, tick, value := 5, 10, .5, 1000.0
	if symbol == "NK225" {
		q = 10
		cap = 20
		tick = 10
		value = 10
	}
	p, err := New(Config{Reference: symbol, Product: symbol, CoreQuantity: q, ExtraMaxQuantity: q, Cap: cap, LargeTick: tick, PointValue: value, Parameters: DefaultParameters(symbol, kind)},
		Session{ID: "人工_" + kind, Kind: kind, Start: 0, ForceClose: 20000, End: 21000, USOpen: -2000, ExtraAt: 19000})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// observation は未来列を含めない人工観測を作ります。
//
// 引数: p(*Portfolio)、i(int64)、at/price(float64)。返り値: Observation です。
func observation(p *Portfolio, i int64, at, price float64) Observation {
	m := Market{Own: price, Peer: 100, OwnATR: 10, PeerATR: 10, Own30: .1, Own120: .1, Peer30: .1, Peer120: .1, PeerTime: at - .1, Known: true, Own30Known: true, Own120Known: true, Peer30Known: true, Peer120Known: true}
	h := History{Known: true, Values: [15]float64{99, 98, 2, 100, 90, .5, 2, 4, 95, at - 60, 2, 4, .5, 5, at - 120}}
	return Observation{Reference: p.Config.Reference, Time: at, Received: at, Index: i, Last: price, Bid: price - .25, Ask: price + .25, ATR: 10, QuoteOK: true, E01: e01.Observation{Direction: 1, AdverseSource: -1}, GateDirection: 1, DayMarket: m, LastMarket: m, MidMarket: m, LastHistory: h, MidHistory: h}
}

// observe は人工観測の不正を検査失敗にします。
//
// 引数: t(*testing.T)、p(*Portfolio)、o(Observation)、allow(bool)。返り値: []Signal です。
func observe(t *testing.T, p *Portfolio, o Observation, allow bool) []Signal {
	t.Helper()
	signals, err := p.Observe(o, allow)
	if err != nil {
		t.Fatal(err)
	}
	return signals
}

// fillAll は発注判断後の実約定を明示的に反映します。
//
// 引数: t(*testing.T)、p(*Portfolio)、s(Signal)、price/at(float64)。返り値: なし。
func fillAll(t *testing.T, p *Portfolio, s Signal, price, at float64) {
	t.Helper()
	if err := p.OnFill(Fill{SignalID: s.ID, ExecutionID: s.ID + "-約定", Quantity: s.Quantity, Price: price, Time: at}); err != nil {
		t.Fatal(err)
	}
	if err := p.OnOrderDone(s.ID); err != nil {
		t.Fatal(err)
	}
}

// TestStrictWindowsAndPeer は窓端の < と <=、相手同秒除外を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestStrictWindowsAndPeer(t *testing.T) {
	rows := []rawRow{{Time: 0, Last: 100, Mid: 100, ATR: 10, ValidLast: true, ValidMid: true}, {Time: 30, Last: 102, Mid: 102, ATR: 10, ValidLast: true, ValidMid: true}, {Time: 60, Last: 104, Mid: 104, ATR: 10, ValidLast: true, ValidMid: true}}
	v, at, ok := change(rows, 2, 30, false, false)
	if !ok || v != .2 || at != 30 {
		t.Fatal(v, at, ok)
	}
	v, at, ok = change(rows, 2, 30, false, true)
	if !ok || v != .4 || at != 0 {
		t.Fatal(v, at, ok)
	}
	f := Features{Rows: map[string][]rawRow{"TOPIX": rows, "NK225": {{Time: 59, Last: 200, ATR: 20, ValidLast: true}, {Time: 60, Last: 9999, ATR: 20, ValidLast: true}}}}
	m := f.market("TOPIX", false, true)
	if m.Peer != 200 || m.PeerTime != 59 {
		t.Fatalf("同秒相手を参照: %+v", m)
	}
}

// TestHistoryReleaseAndPrefix は履歴の公開待機と未来改変不変を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestHistoryReleaseAndPrefix(t *testing.T) {
	f := Features{Session: Session{Start: 0}, Rows: map[string][]rawRow{}}
	for _, s := range []string{"TOPIX", "NK225"} {
		for i := 0; i < 25; i++ {
			f.Rows[s] = append(f.Rows[s], rawRow{Time: float64(i)*60 + 1, Last: 100 + float64(i), Mid: 100 + float64(i), ValidLast: true, ValidMid: true})
		}
	}
	if f.history("TOPIX", 1379, false).Known {
		t.Fatal("相手追加遅延より前に公開")
	}
	before := f.history("TOPIX", 1380, false)
	if !before.Known || before.Values[9] != 1320 || before.Values[14] != 1260 {
		t.Fatal(before)
	}
	f.Rows["TOPIX"] = append(f.Rows["TOPIX"], rawRow{Time: 9999, Last: 999999, ValidLast: true})
	if after := f.history("TOPIX", 1380, false); after != before {
		t.Fatal("未来行で接頭履歴が変わりました")
	}
}

// TestNightContinuationAndRetest は二つの選定入口と上下対称を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestNightContinuationAndRetest(t *testing.T) {
	for _, symbol := range []string{"TOPIX", "NK225"} {
		for _, side := range []int{1, -1} {
			p := testPortfolio(t, symbol, "Night")
			p.Config.LargeTick = .5
			prices := []float64{101, 103}
			times := []float64{1400, 1461}
			if symbol == "NK225" {
				prices = []float64{108, 102, 105}
				times = []float64{1400, 1401, 1461}
			}
			var signals []Signal
			for i, price := range prices {
				o := observation(p, int64(i), times[i], price)
				if side < 0 {
					o = mirror(o, 200)
				}
				signals = observe(t, p, o, true)
				if i < len(prices)-1 && len(signals) != 0 {
					t.Fatal("段階確認前に新規")
				}
			}
			if len(signals) != 1 || signals[0].Side != side || signals[0].Action != "new" {
				t.Fatalf("%s/%d %+v", symbol, side, signals)
			}
			if p.Status().Positions[0].Quantity != 0 {
				t.Fatal("発注判断を実約定として扱いました")
			}
		}
	}
}

// mirror は全価格と方向を対称にする人工変換です。
//
// 引数: o(Observation)、center(float64)。返り値: Observation です。
func mirror(o Observation, center float64) Observation {
	o.Last = center - o.Last
	o.Bid, o.Ask = center-o.Ask, center-o.Bid
	o.GateDirection = -o.GateDirection
	o.E01.Direction = -o.E01.Direction
	o.E01.Adverse = -o.E01.Adverse
	for _, m := range []*Market{&o.DayMarket, &o.LastMarket, &o.MidMarket} {
		m.Own = center - m.Own
		m.Peer = center - m.Peer
		m.Own30 = -m.Own30
		m.Own120 = -m.Own120
		m.Peer30 = -m.Peer30
		m.Peer120 = -m.Peer120
	}
	for _, h := range []*History{&o.LastHistory, &o.MidHistory} {
		v := &h.Values
		v[0] = center - v[0]
		v[1] = center - v[1]
		v[3], v[4] = center-v[4], center-v[3]
		v[6] = -v[6]
		v[7] = -v[7]
		v[8] = center - v[8]
		v[10] = -v[10]
		v[11] = -v[11]
		v[13] = -v[13]
	}
	return o
}

// TestRetestOppositionCancelsAndUnknownAllows は二分反対で全段階をやり直す条件を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestRetestOppositionCancelsAndUnknownAllows(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		p := testPortfolio(t, "NK225", "Night")
		p.Config.LargeTick = .5
		observe(t, p, observation(p, 0, 1400, 108), true)
		observe(t, p, observation(p, 1, 1401, 102), true)
		o := observation(p, 2, 1461, 105)
		o.MidMarket.Own120 = -.3
		o.MidMarket.Peer120 = -.3
		o.MidMarket.Own120Known = !unknown
		s := observe(t, p, o, true)
		if unknown && len(s) != 1 {
			t.Fatal("未知を反対と誤認")
		}
		if !unknown && (len(s) != 0 || p.Core.Arm.Side != 0) {
			t.Fatal("既知の両反対で準備を取消していない")
		}
	}
}

// TestPartialFillsRestoreAndRetries は実約定、重複、復元、返済再試行を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestPartialFillsRestoreAndRetries(t *testing.T) {
	p := testPortfolio(t, "TOPIX", "Night")
	observe(t, p, observation(p, 0, 1400, 101), true)
	s := observe(t, p, observation(p, 1, 1461, 103), true)[0]
	f := Fill{SignalID: s.ID, ExecutionID: "実約定1", Quantity: 2, Price: 104, Time: 1462, Fee: 83}
	if err := p.OnFill(f); err != nil {
		t.Fatal(err)
	}
	if err := p.OnFill(f); err != nil {
		t.Fatal(err)
	}
	b, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	copyP, err := Restore(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []*Portfolio{p, copyP} {
		if err := q.OnFill(Fill{SignalID: s.ID, ExecutionID: "実約定2", Quantity: 1, Price: 105, Time: 1463, Fee: 42}); err != nil {
			t.Fatal(err)
		}
		if err := q.OnOrderDone(s.ID); err != nil {
			t.Fatal(err)
		}
		if q.Core.Position.Quantity != 3 || q.Core.Position.InitialQuantity != 3 {
			t.Fatal(q.Core.Position)
		}
	}
	o := observation(p, 2, 1464, 93)
	a := observe(t, p, o, true)
	bSignals := observe(t, copyP, o, true)
	if !reflect.DeepEqual(a, bSignals) || len(a) != 1 || a[0].Quantity != 3 {
		t.Fatal(a, bSignals)
	}
	if err := p.OnOrderRejected(a[0].ID); err != nil {
		t.Fatal(err)
	}
	retry := observe(t, p, observation(p, 3, 1465, 104), true)
	if len(retry) != 1 || retry[0].Action != "close" {
		t.Fatal("価格が戻った後に停止要求が消えました", retry)
	}
	bad := f
	bad.Quantity = 1
	if p.OnFill(bad) == nil {
		t.Fatal("約定 ID 内容変更を受理")
	}
}

// TestExtraSizingCapsAndFutureCore は数量計算と後からの主参加で取消さないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestExtraSizingCapsAndFutureCore(t *testing.T) {
	p := testPortfolio(t, "NK225", "Night")
	p.Session.ExtraAt = 100
	o := observation(p, 0, 200, 105)
	o.ATR = 300
	s := observe(t, p, o, true)
	if len(s) != 1 || s[0].Owner != "extra" || s[0].Quantity != 4 {
		t.Fatal(s)
	}
	p.CoreEntered = true
	fillAll(t, p, s[0], 106, 201)
	if p.Extra.Position.Quantity != 4 {
		t.Fatal("未来の主参加で追加を取消")
	}
	p2 := testPortfolio(t, "TOPIX", "Night")
	p2.Config.Cap = 1
	p2.Config.CoreQuantity = 1
	p2.Config.ExtraMaxQuantity = 1
	p2.Session.ExtraAt = 100
	if err := p2.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	new1 := p2.emit(&p2.Core, observation(p2, 0, 100, 105), "new", 1, 1, 1, 10, 100, 0)
	if new1 == nil || p2.available(1) != 0 || p2.available(-1) != 1 {
		t.Fatal("長短別の予約上限が不正")
	}
	if s := p2.emit(&p2.Extra, observation(p2, 1, 101, 105), "new", 1, 1, 82, 10, 0, 1); s != nil {
		t.Fatal("片方向上限超過")
	}
}

// TestRecoveryLatchesWithoutFakeFills は通信断の過去返済を架空約定にしないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestRecoveryLatchesWithoutFakeFills(t *testing.T) {
	p := testPortfolio(t, "TOPIX", "Night")
	observe(t, p, observation(p, 0, 1400, 101), true)
	s := observe(t, p, observation(p, 1, 1461, 103), true)[0]
	fillAll(t, p, s, 104, 1462)
	if err := p.Advance(observation(p, 2, 1463, 90)); err != nil {
		t.Fatal(err)
	}
	if p.Core.Position.Quantity != 5 || p.Core.Pending != "" || p.Core.CloseTarget != 0 {
		t.Fatal(p.Status())
	}
	out := observe(t, p, observation(p, 3, 1464, 110), false)
	if len(out) != 1 || out[0].Action != "close" {
		t.Fatal("復旧後に返済条件を失いました")
	}
}

// TestDayPersistenceAndNoReentry は実玉の二市場継続撤退と同じ影の入り直し禁止を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestDayPersistenceAndNoReentry(t *testing.T) {
	p := testPortfolio(t, "TOPIX", "Day")
	p.Config.Parameters.Day.PersistenceSeconds = 60
	observe(t, p, observation(p, 0, 1200, 100), true)
	news := observe(t, p, observation(p, 1, 1201, 103), true)
	if len(news) != 1 {
		t.Fatal(news)
	}
	fillAll(t, p, news[0], 103.25, 1202)
	observe(t, p, observation(p, 2, 1202, 103), true)
	var closing []Signal
	for i := 0; i <= 6; i++ {
		o := observation(p, int64(3+i), 1203+float64(i)*10, 100)
		o.DayMarket.Peer = 97
		s := observe(t, p, o, true)
		if i < 6 && len(s) > 0 {
			t.Fatal("持続確認前に撤退", i, s)
		}
		closing = append(closing, s...)
	}
	if len(closing) != 1 || closing[0].ReasonCode != 75 {
		t.Fatal(closing)
	}
	fillAll(t, p, closing[0], 99.75, 1264)
	out := observe(t, p, observation(p, 10, 1265, 106), true)
	if len(out) != 0 {
		t.Fatal("旧影存続中に再参加", out)
	}
}

// TestParameterOverrides は未知設定と非整数の受理を防ぎます。
//
// 引数: t(*testing.T)。返り値: なし。
func TestParameterOverrides(t *testing.T) {
	p := DefaultParameters("TOPIX", "Day")
	if err := ApplyOverrides(&p, map[string]float64{"day.risk_floor": .8, "night.retest": 1}); err != nil {
		t.Fatal(err)
	}
	if p.Day.RiskFloor != .8 || !p.Night.Retest {
		t.Fatal(p)
	}
	for key, value := range map[string]float64{"day.unknown": 1, "features.ema_fast": 1.1, "night.retest": 2, "extra.style": math.NaN()} {
		if ApplyOverrides(&p, map[string]float64{key: value}) == nil {
			t.Fatal("不正設定受理", key)
		}
	}
	if _, err := json.Marshal(p); err != nil {
		t.Fatal(err)
	}
}

// TestFeaturePruningRestore は原板を刈り込んでも始値と連番が変わらないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestFeaturePruningRestore(t *testing.T) {
	session := Session{ID: "特徴_Day", Kind: "Day", Start: 0, ForceClose: 20000, End: 21000}
	f, err := NewFeatures(session, map[string]float64{"TOPIX": .5, "NK225": 10}, e01.DefaultFeatureConfig())
	if err != nil {
		t.Fatal(err)
	}
	var duplicate *Features
	for i := 0; i <= 1800; i++ {
		for _, symbol := range []string{"NK225", "TOPIX"} {
			px := 100 + float64(i)/100
			b := Board{Reference: symbol, Time: float64(i), Received: float64(i), Last: px, Bid: px - .25, Ask: px + .25, BidQuantity: 10, AskQuantity: 10}
			o, err := f.OnBoard(b)
			if err != nil {
				t.Fatal(err)
			}
			if o.Index != int64(i) {
				t.Fatal("刈込後の連番不整合", o.Index, i)
			}
			if i >= 1501 && o.MidHistory.Known && o.MidHistory.Values[8] != 100 {
				t.Fatal("始値が保存窓の先頭へ変わりました")
			}
			if duplicate != nil {
				other, err := duplicate.OnBoard(b)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(o, other) {
					t.Fatalf("特徴復元後に不一致 %d/%s", i, symbol)
				}
			}
		}
		if i == 1600 {
			raw, err := f.Dump()
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err = RestoreFeatures(raw)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(f.Rows["TOPIX"]) > 1501 {
		t.Fatal("保存窓の刈込がありません")
	}
}

// TestFreshWarmupIndex は過去特徴だけ暖機した後の初回連番を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestFreshWarmupIndex(t *testing.T) {
	p := testPortfolio(t, "TOPIX", "Day")
	o := observation(p, 90000, 5000, 100)
	o.E01.Adverse = -1
	o.E01.AdverseSource = 89999
	observe(t, p, o, true)
	o = observation(p, 90001, 5001, 103)
	o.E01.AdverseSource = 90000
	signals := observe(t, p, o, true)
	if len(signals) != 1 || signals[0].SourceIndex != 90001 {
		t.Fatal(signals)
	}
}

// TestPartialNewCancellationRebasesPaperExit は新規残取消後の紙上部分返済比率を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestPartialNewCancellationRebasesPaperExit(t *testing.T) {
	p := testPortfolio(t, "TOPIX", "Day")
	o := observation(p, 0, 1200, 100)
	s := p.emit(&p.Core, o, "new", 1, 5, 20, 10, 0, 0)
	p.Core.PaperCampaign = 0
	p.Core.PaperRemaining = 3
	if err := p.OnFill(Fill{SignalID: s.ID, ExecutionID: "部分約定", Quantity: 2, Price: 100, Time: 1201}); err != nil {
		t.Fatal(err)
	}
	requestClose(&p.Core, 3, 7)
	if err := p.OnOrderDone(s.ID); err != nil {
		t.Fatal(err)
	}
	if p.Core.CloseTarget != 1 {
		t.Fatal("未約定分を基準に残数量を保持", p.Core.CloseTarget)
	}
	close := p.flushClose(&p.Core, observation(p, 1, 1202, 105))
	if close == nil || close.Quantity != 1 {
		t.Fatal(close)
	}
}

// TestPaperSourceMappingSurvivesGaps は無効価格・欠番をまたいだ根拠行の写像を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestPaperSourceMappingSurvivesGaps(t *testing.T) {
	p := testPortfolio(t, "TOPIX", "Day")
	o := observation(p, 90000, 1200, 100)
	observe(t, p, o, true)
	o = observation(p, 90001, 1201, 0)
	o.E01.Adverse = -1
	o.E01.AdverseSource = 90000
	observe(t, p, o, true)
	o = observation(p, 90007, 1202, 103)
	o.E01.Adverse = -1
	o.E01.AdverseSource = 90000
	news := observe(t, p, o, true)
	if len(news) != 1 || p.Paper.Row != 1 || p.PaperRows[90000] != 0 || p.PaperRows[90007] != 1 {
		t.Fatal("欠番後の根拠写像が不正", p.PaperRows, p.Paper.Row)
	}
	o = observation(p, 90008, 1203, 104)
	o.E01.AdverseSource = 90001
	o.E01.Adverse = -1
	observe(t, p, o, true)
	if _, ok := p.PaperRows[90001]; ok {
		t.Fatal("無効価格行を影の有効根拠に写像")
	}
}

// TestRecoveryLargeHistoryCreatesNoOrders は十万行の復元中に架空の発注履歴が増えないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestRecoveryLargeHistoryCreatesNoOrders(t *testing.T) {
	p := testPortfolio(t, "TOPIX", "Night")
	observe(t, p, observation(p, 0, 1400, 101), true)
	s := observe(t, p, observation(p, 1, 1461, 103), true)[0]
	fillAll(t, p, s, 104, 1462)
	beforeOrders, beforeSequence := len(p.Orders), p.Sequence
	for i := 0; i < 100000; i++ {
		o := observation(p, int64(i+2), 1463+float64(i)/100, 90)
		if err := p.Advance(o); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.Orders) != beforeOrders || p.Sequence != beforeSequence || p.Core.Pending != "" || p.Core.Position.Quantity != 5 || p.Core.CloseTarget != 0 {
		t.Fatalf("復元中に架空依頼を生成: orders=%d sequence=%d state=%+v", len(p.Orders), p.Sequence, p.Status())
	}
	orders := observe(t, p, observation(p, 100002, 2464, 110), false)
	if len(orders) != 1 || orders[0].Action != "close" || len(p.Orders) != beforeOrders+1 {
		t.Fatal("現在へ戻った後の一度の返済依頼が不正", orders)
	}
}

// TestEntryBlockCancelsInvalidPreparation は新規停止中の破綻・寿命切れと不等号を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestEntryBlockCancelsInvalidPreparation(t *testing.T) {
	for _, symbol := range []string{"TOPIX", "NK225"} {
		for _, boundary := range []float64{97.5, 97} {
			p := testPortfolio(t, symbol, "Night")
			p.Config.LargeTick = .5
			p.Core.Arm = armState{Side: 1, Stage: 2, Level: 100, Risk: 10, At: 1400, ReturnExtreme: 98, ReturnIndex: 0}
			observe(t, p, observation(p, 1, 1461, boundary), false)
			shouldCancel := symbol == "NK225" || boundary < 97.5
			if (p.Core.Arm.Side == 0) != shouldCancel {
				t.Fatalf("%s/%g 取消し不一致: %+v", symbol, boundary, p.Core.Arm)
			}
			if shouldCancel {
				out := observe(t, p, observation(p, 2, 1462, 103), true)
				if len(out) != 0 {
					t.Fatal("破綻前の段階から発注", out)
				}
			}
		}
		p := testPortfolio(t, symbol, "Night")
		p.Core.Arm = armState{Side: 1, Level: 100, Risk: 10, At: 100}
		if err := p.Advance(observation(p, 0, 1901, 103)); err != nil {
			t.Fatal(err)
		}
		if p.Core.Arm.Side != 0 || len(p.Orders) != 0 {
			t.Fatal("復元中の寿命切れ準備を保持", p.Core.Arm)
		}
	}
}
