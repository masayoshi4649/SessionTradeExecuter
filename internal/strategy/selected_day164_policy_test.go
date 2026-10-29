package strategy

import (
	"fmt"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
	"math"
	"testing"
)

// selectedDayPolicyRules は実運用と同じExprの人工試験器を生成します。
//
// 引数: t(*testing.T)。返り値: *rules.Runtime。
func selectedDayPolicyRules(t *testing.T) *rules.Runtime {
	t.Helper()
	r, e := rules.NewRuntimeFor("d164_15", nil)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

// selectedDayPolicyObservation は円幅と速度を指定できる人工の当時観測です。
//
// 引数: at,bid(float64)、wanted(int)。返り値: Observation。
func selectedDayPolicyObservation(at, bid float64, wanted int) Observation {
	s := SelectedDay164Support{Known: true, M: 1, T: float64(wanted-10) / 90}
	return Observation{Reference: "NK225", Time: at, Received: at, Index: int64(at), Bid: bid, Ask: bid + 10, QuoteOK: true, Selected: SelectedObservation{DayBuy: s, DaySell: s, DayShort: SelectedDay164Short{Five: SelectedDay164ShortWindow{Known: true, Move: 10, Speed: 2}, Fifteen: SelectedDay164ShortWindow{Known: true, Move: 15, Speed: 1}}}}
}

// selectedDayPolicyNew は固定源mini量を100基準の通知へ変換した人工例です。
//
// 引数: id,parent(string)、side,quantity,remaining(int)。返り値: SelectedSourceEvent。
func selectedDayPolicyNew(id, parent string, side, quantity, remaining int) SelectedSourceEvent {
	return SelectedSourceEvent{Signal: Signal{ID: id, Owner: "core", Campaign: 1, Action: "new", Side: side, Quantity: quantity / 10}, Parent: parent, SourceID: id, QuantityMicro: quantity, ParentRemainingMicro: remaining}
}

// selectedDayPolicyStep は人工判断の失敗を即座に報告します。
//
// 引数: t(*testing.T)、d(*SelectedDay164Policy)、o(Observation)、e([]SelectedSourceEvent)、s(SelectedSnapshot)、r(*rules.Runtime)。返り値: []SelectedIntent。
func selectedDayPolicyStep(t *testing.T, d *SelectedDay164Policy, o Observation, e []SelectedSourceEvent, s SelectedSnapshot, r *rules.Runtime) []SelectedIntent {
	t.Helper()
	v, err := d.StepSelectedDay(o, e, s, r, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// selectedDayPolicyFill は人工実約定を共通帳簿と政策へ同じ順で反映します。
//
// 引数: t(*testing.T)、d(*SelectedDay164Policy)、in(SelectedIntent)、s(*SelectedSnapshot)、quantity(int)、price,gross,at(float64)。返り値: 実玉ID(string)。
func selectedDayPolicyFill(t *testing.T, d *SelectedDay164Policy, in SelectedIntent, s *SelectedSnapshot, quantity int, price, gross, at float64) string {
	t.Helper()
	id := in.ActualID
	if id == "" {
		id = fmt.Sprintf("玉-%s-%d", in.PolicyID, len(s.Lots))
		s.Lots = append(s.Lots, SelectedLot{ID: id, EntryID: in.PolicyID, Parent: in.Parent, SourceID: in.SourceID, Owner: in.Owner, Campaign: in.Campaign, Kind: in.Kind, Side: in.Side, Quantity: quantity, InitialQuantity: quantity, Entry: price, At: at})
	} else {
		found := false
		for i := range s.Lots {
			if s.Lots[i].ID == id {
				s.Lots[i].Quantity -= quantity
				found = true
				break
			}
		}
		if !found {
			t.Fatal("人工実玉なし")
		}
	}
	e := SelectedFillEvent{Intent: in, ExecutionID: fmt.Sprintf("実行-%s-%.3f-%d", in.PolicyID, at, quantity), LotID: id, Quantity: quantity, Gross: gross, Time: at}
	if err := d.FillSelectedDay(e, selectedDayPolicyObservation(at, price, 50), *s); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestSelectedDay164DelayKeepsOriginalQuantity は25→88の支持増加を待機量の増幅へ使わないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164DelayKeepsOriginalQuantity(t *testing.T) {
	d := newSelectedDay164Policy(100)
	s := SelectedSnapshot{Cap: 100}
	r := selectedDayPolicyRules(t)
	o := selectedDayPolicyObservation(100, 10000, 25)
	o.Selected.DayShort.Five.Speed = .5
	if v := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("A", "親", 1, 50, 50)}, s, r); len(v) != 0 {
		t.Fatal("失速で即新規")
	}
	o.Time = 110
	o.Selected.DayBuy.T = float64(88-10) / 90
	selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("B", "親", 1, 50, 100)}, s, r)
	close := SelectedSourceEvent{Signal: Signal{ID: "返済A", Action: "close"}, Parent: "親", QuantityMicro: 20, ParentRemainingMicro: 80, Targets: []SelectedSourceTarget{{SourceID: "A", BeforeMicro: 50, QuantityMicro: 20, RemainingMicro: 30}}}
	o.Time = 120
	selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{close}, s, r)
	o.Time = 130
	o.Selected.DayShort.Five.Speed = 2
	v := selectedDayPolicyStep(t, d, o, nil, s, r)
	n := 0
	for _, in := range v {
		n += in.Quantity
		selectedDayPolicyFill(t, d, in, &s, in.Quantity, 10010, 0, 131)
	}
	if n != 20 || d.Parents["親"].Factor != .25 {
		t.Fatalf("延期中の追加/部分返済で上限が増幅: %d %+v", n, d.Parents["親"])
	}
	o.Time = 140
	v = selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("C", "親", 1, 20, 100)}, s, r)
	if len(v) != 1 || v[0].Quantity != 5 {
		t.Fatalf("後続係数が初回上限を超える: %+v", v)
	}
}

// TestSelectedDay164UnknownAndTimeout は未知の初回は維持し追撃は拒否、30秒境界と親終了を確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164UnknownAndTimeout(t *testing.T) {
	r := selectedDayPolicyRules(t)
	d := newSelectedDay164Policy(100)
	s := SelectedSnapshot{Cap: 100}
	o := selectedDayPolicyObservation(100, 10000, 10)
	o.Selected = SelectedObservation{}
	v := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("A", "親", 1, 50, 50)}, s, r)
	if len(v) != 1 || v[0].Quantity != 10 {
		t.Fatalf("未知初回を勝手に拒否: %+v", v)
	}
	selectedDayPolicyFill(t, d, v[0], &s, 10, 10010, 0, 101)
	o.Time = 102
	v = selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("B", "親", 1, 50, 100)}, s, r)
	if len(v) != 0 || d.Parents["親"].SourceQuantity != 100 {
		t.Fatal("未知追撃又は源残量が不正")
	}
	for _, elapsed := range []float64{30, 30.001} {
		d2 := newSelectedDay164Policy(100)
		o = selectedDayPolicyObservation(100, 10000, 25)
		o.Selected.DayShort.Five.Speed = .5
		selectedDayPolicyStep(t, d2, o, []SelectedSourceEvent{selectedDayPolicyNew("Z", "待機", 1, 50, 50)}, SelectedSnapshot{Cap: 100}, r)
		o.Time += elapsed
		o.Selected.DayShort.Five.Speed = 2
		v = selectedDayPolicyStep(t, d2, o, nil, SelectedSnapshot{Cap: 100}, r)
		if (len(v) > 0) != (elapsed == 30) {
			t.Fatalf("30秒境界: %v %+v", elapsed, v)
		}
	}
}

// TestSelectedDay164SourceFIFOAndResidual は源ID返済を実玉FIFOへ写し、実益の既存残玉だけを建値保護します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164SourceFIFOAndResidual(t *testing.T) {
	d := newSelectedDay164Policy(100)
	s := SelectedSnapshot{Cap: 100}
	r := selectedDayPolicyRules(t)
	o := selectedDayPolicyObservation(100, 10000, 50)
	a := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("A", "親", 1, 50, 50)}, s, r)[0]
	aID := selectedDayPolicyFill(t, d, a, &s, 50, 10010, 0, 101)
	o.Time = 102
	b := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("B", "親", 1, 50, 100)}, s, r)[0]
	bID := selectedDayPolicyFill(t, d, b, &s, 50, 10030, 0, 103)
	o.Time = 104
	o.Bid = 10020
	o.Ask = 10030
	e := SelectedSourceEvent{Signal: Signal{ID: "B部分", Action: "close"}, Parent: "親", QuantityMicro: 20, ParentRemainingMicro: 80, Targets: []SelectedSourceTarget{{SourceID: "B", BeforeMicro: 50, QuantityMicro: 20, RemainingMicro: 30}}}
	v := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{e}, s, r)
	if len(v) != 1 || v[0].ActualID != aID || v[0].Quantity != 20 {
		t.Fatalf("Source BをActual B直接返済にしている: %+v", v)
	}
	selectedDayPolicyFill(t, d, v[0], &s, 20, 10020, 200, 105)
	if !d.Protection[aID].Eligible || !d.Protection[bID].Eligible || d.Canonical[0].Quantity != 50 || d.Canonical[1].Quantity != 30 {
		t.Fatal("実資格又はCanonical SourceID按分が不一致")
	}
	o.Time = 106
	selectedDayPolicyStep(t, d, o, nil, s, r)
	if !d.Protection[aID].Active || d.Protection[bID].Active {
		t.Fatal("資格前最高益又は別建値で保護を起動")
	}
	o.Time = 107
	o.Bid = 10010
	o.Ask = 10020
	v = selectedDayPolicyStep(t, d, o, nil, s, r)
	if len(v) != 1 || v[0].ActualID != aID || v[0].Quantity != 30 || v[0].Reason != "protect_residual_floor/D164_15" {
		t.Fatalf("実建値床が不一致: %+v", v)
	}
	selectedDayPolicyFill(t, d, v[0], &s, 30, 10010, 0, 108)
	if d.Canonical[0].Quantity != 20 || d.Canonical[1].Quantity != 30 {
		t.Fatal("独自実玉返済のCanonical同期が不正")
	}
	o.Time = 109
	v = selectedDayPolicyStep(t, d, o, nil, s, r)
	if len(v) != 0 {
		t.Fatalf("保護後に幽霊新規: %+v", v)
	}
}

// TestSelectedDay164ActualCostAndAddStop は初回玉を短く止めず、追加の実建値から50円だけを停止します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164ActualCostAndAddStop(t *testing.T) {
	d := newSelectedDay164Policy(100)
	s := SelectedSnapshot{Cap: 100}
	r := selectedDayPolicyRules(t)
	o := selectedDayPolicyObservation(100, 10000, 50)
	a := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("A", "親", 1, 50, 50)}, s, r)[0]
	selectedDayPolicyFill(t, d, a, &s, 50, 10010, 0, 101)
	o.Time = 102
	b := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("B", "親", 1, 50, 100)}, s, r)[0]
	bID := selectedDayPolicyFill(t, d, b, &s, 50, 10100, 0, 103)
	o.Time = 104
	o.Bid = 10050
	o.Ask = 10060
	v := selectedDayPolicyStep(t, d, o, nil, s, r)
	if len(v) != 1 || v[0].ActualID != bID || v[0].Reason != "tactical_stop50/D164_15" {
		t.Fatalf("追加実建値ではなく元価格又は初回まで停止: %+v", v)
	}
}

// TestSelectedDay164SplitFillGrossAndCancellation は部分約定の一時黒字を注文完了黒字と混同しないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164SplitFillGrossAndCancellation(t *testing.T) {
	d := newSelectedDay164Policy(100)
	s := SelectedSnapshot{Cap: 100}
	r := selectedDayPolicyRules(t)
	o := selectedDayPolicyObservation(100, 10000, 50)
	a := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("A", "親", 1, 50, 50)}, s, r)[0]
	id := selectedDayPolicyFill(t, d, a, &s, 50, 10010, 0, 101)
	e := SelectedSourceEvent{Signal: Signal{ID: "部分", Action: "close"}, Parent: "親", QuantityMicro: 20, ParentRemainingMicro: 30, Targets: []SelectedSourceTarget{{SourceID: "A", BeforeMicro: 50, QuantityMicro: 20, RemainingMicro: 30}}}
	o.Time = 102
	in := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{e}, s, r)[0]
	selectedDayPolicyFill(t, d, in, &s, 10, 10020, 100, 103)
	if st := d.Protection[id]; st != nil && st.Eligible {
		t.Fatal("注文未完了の一部黒字で資格付与")
	}
	selectedDayPolicyFill(t, d, in, &s, 10, 9990, -200, 104)
	if st := d.Protection[id]; st != nil && st.Eligible {
		t.Fatal("注文合計赤字で資格付与")
	}
	// ----------------------------------------
	// 未完了の源返済取消でも残り10枚の返済目的を失いません。
	e.Signal.ID = "部分2"
	e.ParentRemainingMicro = 10
	e.Targets[0] = SelectedSourceTarget{SourceID: "A", BeforeMicro: 30, QuantityMicro: 20, RemainingMicro: 10}
	o.Time = 105
	in = selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{e}, s, r)[0]
	selectedDayPolicyFill(t, d, in, &s, 10, 10020, 100, 106)
	if err := d.DoneSelectedDay(in.PolicyID, s); err != nil {
		t.Fatal(err)
	}
	if state := d.Protection[id]; state != nil && state.Eligible {
		t.Fatal("返済途中の取消で先行黒字を完了黒字にした")
	}
	o.Time = 107
	v := selectedDayPolicyStep(t, d, o, nil, s, r)
	if len(v) != 1 || v[0].Quantity != 10 {
		t.Fatalf("取消で源返済を忘れた: %+v", v)
	}
	selectedDayPolicyFill(t, d, v[0], &s, 10, 9990, -200, 108)
	if state := d.Protection[id]; state != nil && state.Eligible {
		t.Fatal("再送を含む元返済群が赤字なのに資格付与")
	}
	if math.Abs(d.Canonical[0].Quantity-10) > 1e-9 {
		t.Fatal("取消後の予約二重消費")
	}
}

// TestSelectedDay164OddCapAndLateFill は11枚上限の1枚をゼロにせず、取消後遅着も同期します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164OddCapAndLateFill(t *testing.T) {
	d := newSelectedDay164Policy(11)
	s := SelectedSnapshot{Cap: 11}
	r := selectedDayPolicyRules(t)
	o := selectedDayPolicyObservation(100, 10000, 25)
	in := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("A", "親", 1, 50, 50)}, s, r)[0]
	if in.Quantity != 2 {
		t.Fatal("25microを11上限へ切上げた")
	}
	selectedDayPolicyFill(t, d, in, &s, 1, 10010, 0, 101)
	if err := d.DoneSelectedDay(in.PolicyID, s); err != nil {
		t.Fatal(err)
	}
	selectedDayPolicyFill(t, d, in, &s, 1, 10020, 0, 102)
	if math.Abs(d.Canonical[0].Quantity-200./11) > 1e-8 || d.Canonical[0].PendingNew != 0 {
		t.Fatal("非整除枚数又は取消後遅着が不整合")
	}
	e := SelectedSourceEvent{Signal: Signal{ID: "全返済", Action: "close"}, Parent: "親", QuantityMicro: 50, ParentRemainingMicro: 0, Targets: []SelectedSourceTarget{{SourceID: "A", BeforeMicro: 50, QuantityMicro: 50, RemainingMicro: 0}}}
	o.Time = 103
	v := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{e}, s, r)
	total := 0
	for _, close := range v {
		total += close.Quantity
		selectedDayPolicyFill(t, d, close, &s, close.Quantity, 10000, -1, 104)
	}
	if total != 2 || d.Canonical[0].Quantity > 1e-8 {
		t.Fatalf("1枚の返済消失: %d %+v", total, d.Canonical[0])
	}
}

// TestSelectedDay164StaleBookKeepsSourceExit は古い受信中も源終了を失わず新鮮な原行で一度だけ返済します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164StaleBookKeepsSourceExit(t *testing.T) {
	d := newSelectedDay164Policy(100)
	s := SelectedSnapshot{Cap: 100}
	r := selectedDayPolicyRules(t)
	o := selectedDayPolicyObservation(100, 10000, 10)
	in := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("A", "親", 1, 50, 50)}, s, r)[0]
	selectedDayPolicyFill(t, d, in, &s, 10, 10010, 0, 101)
	e := SelectedSourceEvent{Signal: Signal{ID: "全返済", Action: "close"}, Parent: "親", QuantityMicro: 50, ParentRemainingMicro: 0, Targets: []SelectedSourceTarget{{SourceID: "A", BeforeMicro: 50, QuantityMicro: 50, RemainingMicro: 0}}}
	o.Time = 102
	v, err := d.StepSelectedDay(o, []SelectedSourceEvent{e}, s, r, false, true, false)
	if err != nil || len(v) != 0 || !d.Parents["親"].Ended {
		t.Fatal("古い原行で発注又は源終了欠落")
	}
	o.Time = 103
	v = selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{e}, s, r)
	if len(v) != 1 || v[0].Quantity != 10 {
		t.Fatalf("次の新鮮原行で返済しない又は重複: %+v", v)
	}
}

// TestSelectedDay164BatchBudgetAndExternalClose は同原行比例配分と共通強制返済の同期を確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164BatchBudgetAndExternalClose(t *testing.T) {
	d := newSelectedDay164Policy(100)
	s := SelectedSnapshot{Cap: 100}
	r := selectedDayPolicyRules(t)
	o := selectedDayPolicyObservation(100, 10000, 88)
	v := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("A", "親A", 1, 50, 50), selectedDayPolicyNew("B", "親B", 1, 50, 50)}, s, r)
	if len(v) != 2 || v[0].Quantity != 50 || v[1].Quantity != 50 {
		t.Fatalf("同原行の配分偏り: %+v", v)
	}
	id := selectedDayPolicyFill(t, d, v[0], &s, 50, 10010, 0, 101)
	s.Lots[0].Quantity = 0
	e := SelectedFillEvent{Intent: SelectedIntent{Parent: "親A", ActualID: id}, ExecutionID: "強制", LotID: id, Quantity: 50, Time: 102}
	if err := d.FillSelectedDay(e, o, s); err != nil {
		t.Fatal(err)
	}
	if err := d.FillSelectedDay(e, o, s); err != nil {
		t.Fatal(err)
	}
	if d.Canonical[0].Quantity != 0 {
		t.Fatal("外部返済又は重複Executionを誤消費")
	}
}

// TestSelectedDay164FreshCrossedQuoteKeepsSourceDecision は新鮮な交差板で元返済の判断を一原行遅らせないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。値は2026-10-26の差分原因を再現する人工経路です。
// 不正気配へ約定させる試験ではなく、約定は共通の次正常原行規則へ委ねます。
func TestSelectedDay164FreshCrossedQuoteKeepsSourceDecision(t *testing.T) {
	d := newSelectedDay164Policy(100)
	s := SelectedSnapshot{Cap: 100}
	r := selectedDayPolicyRules(t)
	o := selectedDayPolicyObservation(100, 62350, 10)
	in := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("A", "core/2", -1, 50, 50)}, s, r)[0]
	selectedDayPolicyFill(t, d, in, &s, 10, 62350, 0, 101)
	o = selectedDayPolicyObservation(1792983403, 62460, 10)
	o.Index = 87642
	o.Ask = 62440
	o.Last = 62470
	o.QuoteOK = false
	o.Selected.DayShort = SelectedDay164Short{}
	e := SelectedSourceEvent{Signal: Signal{ID: "全返済", Action: "close", SourceIndex: 87642}, Parent: "core/2", QuantityMicro: 50, ParentRemainingMicro: 0, Targets: []SelectedSourceTarget{{SourceID: "A", BeforeMicro: 50, QuantityMicro: 50, RemainingMicro: 0}}}
	v := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{e}, s, r)
	if len(v) != 1 || v[0].Quantity != 10 || v[0].Reason != "source_end/D164_15" || len(d.DeferredIntents) != 0 {
		t.Fatalf("交差板の元返済を次の正常板まで延期した: %+v", v)
	}
	// ----------------------------------------
	// 同じ時刻でも次の正常原行で初めて実約定を受けます。
	selectedDayPolicyFill(t, d, v[0], &s, 10, 62470, -12000, 1792983403)
	if d.Canonical[0].Quantity != 0 {
		t.Fatal("次正常約定の元返済が同期されない")
	}
}

// TestSelectedDay164CrossedQuoteOnlyBlocksIndependentManagement は元通知と独自気配判断の条件を分離します。
//
// 引数: t(*testing.T)。返り値: なし。元の新規通知も二重の気配条件で変更しません。
func TestSelectedDay164CrossedQuoteOnlyBlocksIndependentManagement(t *testing.T) {
	d := newSelectedDay164Policy(100)
	s := SelectedSnapshot{Cap: 100}
	r := selectedDayPolicyRules(t)
	o := selectedDayPolicyObservation(100, 10000, 50)
	a := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("A", "親", 1, 50, 50)}, s, r)[0]
	selectedDayPolicyFill(t, d, a, &s, 50, 10010, 0, 101)
	o.Time = 102
	b := selectedDayPolicyStep(t, d, o, []SelectedSourceEvent{selectedDayPolicyNew("B", "親", 1, 50, 100)}, s, r)[0]
	selectedDayPolicyFill(t, d, b, &s, 50, 10100, 0, 103)
	o.Time = 104
	o.Bid = 10040
	o.Ask = 10020
	o.QuoteOK = false
	if v := selectedDayPolicyStep(t, d, o, nil, s, r); len(v) != 0 {
		t.Fatalf("交差板を追加50円停止の判断に使用: %+v", v)
	}
	o.Time = 105
	o.Ask = 10050
	o.QuoteOK = true
	if v := selectedDayPolicyStep(t, d, o, nil, s, r); len(v) != 1 || v[0].Reason != "tactical_stop50/D164_15" {
		t.Fatalf("正常板での追加停止まで失われた: %+v", v)
	}
	// ----------------------------------------
	// 元Source自体が確定した新規通知は、独自manageの正常気配条件を二重適用しません。
	d2 := newSelectedDay164Policy(100)
	o.QuoteOK = false
	o.Ask = o.Bid - 10
	o.Selected.DayShort = SelectedDay164Short{}
	if v := selectedDayPolicyStep(t, d2, o, []SelectedSourceEvent{selectedDayPolicyNew("C", "別親", 1, 50, 50)}, SelectedSnapshot{Cap: 100}, r); len(v) != 1 {
		t.Fatalf("元新規通知へ新しい気配条件を追加: %+v", v)
	}
}
