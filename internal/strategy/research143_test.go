package strategy

import (
	"reflect"
	"testing"
)

// researchTestPortfolio は実APIへ接続しない独立枠の試験器です。
//
// 引数: t(*testing.T)、method(string)、cap(int)。返り値: *Portfolioです。
func researchTestPortfolio(t *testing.T, method string, cap int) *Portfolio {
	t.Helper()
	parameters, err := ParametersForMethod("NK225", "Day", method)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{Reference: "NK225", Product: "NK225mini", CoreQuantity: 1, ExtraMaxQuantity: 1, Cap: cap, LargeTick: 10, PointValue: 100, Parameters: parameters}, Session{ID: "人工_Day", Kind: "Day", Start: 0, ForceClose: 20000, End: 21000})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// researchTestObservation は有限な当時値だけを渡します。
//
// 引数: index(int64)、at/bid(float64)。返り値: Observationです。
func researchTestObservation(index int64, at, bid float64) Observation {
	return Observation{Reference: "NK225", Index: index, Time: at, Received: at, Bid: bid, Ask: bid + 10, Last: bid, ATR: 20, QuoteOK: true}
}

// researchTestFill は対象建玉を指定して約定・注文終了を確認します。
//
// 引数: t(*testing.T)、p(*Portfolio)、s(Signal)、price/fee(float64)。返り値: なし。
func researchTestFill(t *testing.T, p *Portfolio, s Signal, price, fee float64) {
	t.Helper()
	f := Fill{SignalID: s.ID, ExecutionID: s.ID + "約定", HoldID: s.ID + "玉", Closed: s.Targets, Quantity: s.Quantity, Price: price, Time: s.Time + 1, Fee: fee}
	if err := p.OnFill(f); err != nil {
		t.Fatal(err)
	}
	if err := p.OnOrderDone(s.ID); err != nil {
		t.Fatal(err)
	}
}

// TestResearch143Allocation は端数・少枚数・大枚数で上限を増やさないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearch143Allocation(t *testing.T) {
	for _, method := range []string{"TG04", "PG04"} {
		for _, cap := range []int{1, 2, 3, 10, 11, 20, 100, 1000000, int(^uint(0) >> 1)} {
			a := ResearchAllocation(method, cap)
			sum := 0
			for _, n := range a {
				sum += n
				if n < 0 {
					t.Fatal(a)
				}
			}
			if sum != cap {
				t.Fatal(method, cap, a)
			}
		}
	}
	if !reflect.DeepEqual(ResearchAllocation("TG04", 11), map[string]int{"core": 6, "addon": 3, "episode": 2}) {
		t.Fatal("11枚の端数")
	}
	for _, cap := range []int{1, 2, 3, 10, 20, 100} {
		p := researchTestPortfolio(t, "TG04", cap)
		if p.Research == nil {
			t.Fatal(cap)
		}
	}
}

// TestResearch143RestoreMissingEngine は新手法の本体欠落を旧手法として復元しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearch143RestoreMissingEngine(t *testing.T) {
	for _, method := range []string{"TG04", "PG04"} {
		for _, kind := range []string{"Day", "Night"} {
			p := researchTestPortfolio(t, method, 10)
			p.Session.Kind = kind
			p.Research = nil
			raw, err := p.Dump()
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Restore(raw); err == nil {
				t.Fatalf("%s/%sで戦略本体欠落を受理", method, kind)
			}
		}
		p := researchTestPortfolio(t, method, 10)
		p.Config.Parameters.Method = ""
		raw, err := p.Dump()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Restore(raw); err == nil {
			t.Fatal("新戦略本体を旧手法として受理")
		}
	}
}

// TestResearch143RealizedBudgetAndRestore は不正板・予算・復元・後続親見送りを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearch143RealizedBudgetAndRestore(t *testing.T) {
	p := researchTestPortfolio(t, "TG04", 10)
	s := p.sleeve("episode")
	o := researchTestObservation(0, 100, 990)
	entry := p.nativeResearch(s, o, []ResearchEvent{{Action: "new", Side: 1, Quantity: 2, Campaign: 0, Ticket: 0, Reason: 1431}}, true, true)
	if len(entry) != 1 {
		t.Fatal(entry)
	}
	researchTestFill(t, p, entry[0], 1000, 77)
	o = researchTestObservation(2, 102, 1200)
	p.nativeResearch(s, o, []ResearchEvent{{Action: "close", Side: -1, Quantity: 2, Campaign: 0, Ticket: 0, Reason: 1437}}, true, true)
	closes := p.flushResearchGoals(s, o)
	if len(closes) != 1 || closes[0].Quantity != 1 || s.Stage != 1 {
		t.Fatal(closes, s.Stage)
	}
	// 未約定の返済を保持したまま再起動する。
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	p, err = Restore(raw)
	if err != nil {
		t.Fatal(err)
	}
	s = p.sleeve("episode")
	researchTestFill(t, p, closes[0], 1200, 38)
	bad := researchTestObservation(3, 103, 0)
	bad.QuoteOK = false
	p.researchManage(s, bad)
	if s.Stage != 1 {
		t.Fatal("不正板で残玉の高値を初期化")
	}
	o = researchTestObservation(4, 104, 1201)
	p.researchManage(s, o)
	if s.Stage != 2 || s.Budget != 19885 || s.Best != 201 || s.FixedR != 80 {
		t.Fatalf("実現費用予算が不正: %+v", s)
	}
	// 後続親は初回通知時に拒否し、後から途中参加しない。
	if v := p.nativeResearch(s, o, []ResearchEvent{{Action: "new", Side: -1, Quantity: 2, Campaign: 1, Ticket: 0}}, true, true); len(v) != 0 {
		t.Fatal("残玉中に新親")
	}
	o = researchTestObservation(5, 105, 960)
	p.researchManage(s, o)
	closes = p.flushResearchGoals(s, o)
	if len(closes) != 1 || closes[0].ReasonCode != 14380 || closes[0].Targets[0].ID != s.Retained {
		t.Fatal(closes)
	}
	researchTestFill(t, p, closes[0], 950, 38)
	if v := p.nativeResearch(s, o, []ResearchEvent{{Action: "new", Side: -1, Quantity: 1, Campaign: 1, Ticket: 1}}, true, true); len(v) != 0 {
		t.Fatal("拒否済み親へ途中参加")
	}
}

// TestResearch143LateEntryFillMustClose は返済判断後に届いた新規残約定を裸で残さないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearch143LateEntryFillMustClose(t *testing.T) {
	p := researchTestPortfolio(t, "TG04", 10)
	s := p.sleeve("episode")
	o := researchTestObservation(0, 100, 990)
	entry := p.nativeResearch(s, o, []ResearchEvent{{Action: "new", Side: 1, Quantity: 2, Campaign: 0, Ticket: 0}}, true, true)[0]
	if err := p.OnFill(Fill{SignalID: entry.ID, ExecutionID: "前半", HoldID: "玉A", Quantity: 1, Price: 1000, Time: 101, Fee: 38}); err != nil {
		t.Fatal(err)
	}
	p.nativeResearch(s, researchTestObservation(1, 102, 900), []ResearchEvent{{Action: "close", Side: -1, Quantity: 2, Campaign: 0, Ticket: 0, Reason: 1436}}, true, true)
	if err := p.OnFill(Fill{SignalID: entry.ID, ExecutionID: "後半", HoldID: "玉B", Quantity: 1, Price: 900, Time: 103, Fee: 39}); err != nil {
		t.Fatal(err)
	}
	if err := p.OnOrderDone(entry.ID); err != nil {
		t.Fatal(err)
	}
	closes := p.flushResearchGoals(s, researchTestObservation(2, 104, 890))
	if len(closes) != 1 || closes[0].Quantity != 2 || len(closes[0].Targets) != 2 {
		t.Fatal("遅延新規を残しました", closes, s.Goals)
	}
}

// TestResearch143CloseTargetCumulative は同じ指定玉の二重返済を分割約定でも拒否します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearch143CloseTargetCumulative(t *testing.T) {
	p := researchTestPortfolio(t, "PG04", 10)
	s := p.sleeve("episode")
	o := researchTestObservation(0, 100, 990)
	entry := p.nativeResearch(s, o, []ResearchEvent{{Action: "new", Side: 1, Quantity: 3, Campaign: 0, Ticket: 0}}, true, true)[0]
	if err := p.OnFill(Fill{SignalID: entry.ID, ExecutionID: "A", HoldID: "A", Quantity: 2, Price: 1000, Time: 101}); err != nil {
		t.Fatal(err)
	}
	if err := p.OnFill(Fill{SignalID: entry.ID, ExecutionID: "B", HoldID: "B", Quantity: 1, Price: 1000, Time: 102}); err != nil {
		t.Fatal(err)
	}
	if err := p.OnOrderDone(entry.ID); err != nil {
		t.Fatal(err)
	}
	close := p.emitResearch(s, o, ResearchEvent{Action: "close", Side: -1, Quantity: 2, Campaign: 0}, []LotTarget{{"A", 1}, {"B", 1}})
	f := Fill{SignalID: close.ID, ExecutionID: "返済A1", Quantity: 1, Price: 1010, Time: 103, Closed: []LotTarget{{"A", 1}}}
	if err := p.OnFill(f); err != nil {
		t.Fatal(err)
	}
	before, _ := p.Dump()
	f.ExecutionID = "返済A2"
	if err := p.OnFill(f); err == nil {
		t.Fatal("指定A1をA2枚へ拡大しました")
	}
	after, _ := p.Dump()
	if string(before) != string(after) {
		t.Fatal("拒否した約定が状態を変更しました")
	}
	if err := p.OnFill(Fill{SignalID: close.ID, ExecutionID: "返済B", Quantity: 1, Price: 1010, Time: 104, Closed: []LotTarget{{"B", 1}}}); err != nil {
		t.Fatal(err)
	}
}

// TestResearch143InitialReject は初回が全数拒否された親へ追撃だけで参加しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearch143InitialReject(t *testing.T) {
	p := researchTestPortfolio(t, "PG04", 10)
	s := p.sleeve("episode")
	o := researchTestObservation(0, 100, 990)
	entry := p.nativeResearch(s, o, []ResearchEvent{{Action: "new", Side: 1, Quantity: 2, Campaign: 0}}, true, true)[0]
	if err := p.OnOrderRejected(entry.ID); err != nil {
		t.Fatal(err)
	}
	if got := p.nativeResearch(s, o, []ResearchEvent{{Action: "new", Side: 1, Quantity: 1, Campaign: 0, Ticket: 1}}, true, true); len(got) != 0 {
		t.Fatal(got)
	}
}
