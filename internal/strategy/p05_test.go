package strategy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// p05TestPortfolio はネットワークと原データを使わず独立P05を作ります。
//
// 引数: t(*testing.T)、cap(int)。返り値: *Portfolioは人工検査器です。
func p05TestPortfolio(t *testing.T, cap int) *Portfolio {
	t.Helper()
	parameters, _ := ParametersForMethod("NK225", "Night", "P05")
	cfg := Config{Reference: "NK225", Product: "NK225mini", Namespace: "人工運転", CoreQuantity: 1, ExtraMaxQuantity: 1, Cap: cap, LargeTick: 10, PointValue: 100, Parameters: parameters}
	p, err := New(cfg, Session{ID: "人工_Night", Kind: "Night", Start: 1000, USOpen: 20000, ForceClose: 47000, End: 48000, ExtraAt: 21800})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// p05TestObservation は指定返済気配の正常板を生成します。
//
// 引数: index(int64)、at/bid/ask(float64)。返り値: Observationは未知特徴を持つ人工板です。
func p05TestObservation(index int64, at, bid, ask float64) Observation {
	return Observation{Reference: "NK225", Index: index, Time: at, Received: at, Last: (bid + ask) / 2, Bid: bid, Ask: ask, QuoteOK: true, ATR: 100}
}

// p05TestEntry は独立した仮想通知を実注文予約に渡します。
//
// 引数: t(*testing.T)、p(*Portfolio)、side/qty(int)。返り値: Signalは実新規依頼です。
func p05TestEntry(t *testing.T, p *Portfolio, side, qty int) Signal {
	t.Helper()
	s := p.emitP05(p05TestObservation(0, 1100, 1000, 1010), Signal{ID: fmt.Sprintf("仮想-%d", p.Sequence), Owner: "core", Action: "new", Side: side, Quantity: qty}, nil)
	if s == nil {
		t.Fatal("人工入口が余枠不足です")
	}
	return *s
}

// p05TestFill は実約定を明示し、終了通知を任意に与えます。
//
// 引数: t(*testing.T)、p(*Portfolio)、s(Signal)、id/hold(string)、qty(int)、price(float64)、done(bool)。返り値: なし。
func p05TestFill(t *testing.T, p *Portfolio, s Signal, id, hold string, qty int, price float64, done bool) {
	t.Helper()
	if err := p.OnFill(Fill{SignalID: s.ID, ExecutionID: id, HoldID: hold, Quantity: qty, Price: price, Time: 1101, Fee: 7}); err != nil {
		t.Fatal(err)
	}
	if done {
		if err := p.OnOrderDone(s.ID); err != nil {
			t.Fatal(err)
		}
	}
}

// TestP05Configuration はB04の入口、補完、費用、標準外部式を本番生成経路で確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestP05Configuration(t *testing.T) {
	p := p05TestPortfolio(t, 10)
	core := p.P05.Shadow.sleeve("core").Source
	n := core.Legacy.Config.Parameters.Night
	if n.Efficiency != .25 || n.RetestReturnR != .4 || n.BounceR != .125 || core.Legacy.Session.NightCoreStart != 1000 || core.Session.ExtraAt != 21800 || p.P05.Shadow.researchPolicyFee() != 0 || p.P05.Shadow.Session.ForceClose != 19100 {
		t.Fatal("B04とP05の固定条件が未接続")
	}
	set, err := rules.LoadFor("../../conf/rules/NK225_P05.toml", "p05")
	if err != nil {
		t.Fatal(err)
	}
	cfg := p.Config
	cfg.RuleExpressions = set.Expressions()
	cfg.Product = "NK225micro"
	cfg.PointValue = 10
	micro, err := New(cfg, p.Session)
	if err != nil {
		t.Fatal(err)
	}
	if micro.P05.Shadow.Config.PointValue != 100 || micro.Config.PointValue != 10 {
		t.Fatal("実規格が影へ混入")
	}
	if _, err = ParametersForMethod("NK225", "Day", "P05"); err == nil {
		t.Fatal("日中P05を許可")
	}
	if _, err = rules.SelectProfile("TOPIX", "Night", "P05"); err == nil {
		t.Fatal("TOPIXにP05を許可")
	}
}

// TestP05PartialExecutionsAndPositiveTrail は同一HoldIDの部分約定を別価格で追尾します。
//
// 引数: t(*testing.T)。返り値: なし。買いと売りを鏡像で検査します。
func TestP05PartialExecutionsAndPositiveTrail(t *testing.T) {
	for _, side := range []int{1, -1} {
		p := p05TestPortfolio(t, 10)
		s := p05TestEntry(t, p, side, 5)
		p05TestFill(t, p, s, "約定1", "同じ保有ID", 2, 1000, false)
		p05TestFill(t, p, s, "約定2", "同じ保有ID", 3, 1000+float64(side)*20, true)
		quote := func(i int64, gain float64) Observation {
			mark := 1000 + float64(side)*gain
			if side > 0 {
				return p05TestObservation(i, 1200+float64(i), mark, mark+10)
			}
			return p05TestObservation(i, 1200+float64(i), mark-10, mark)
		}
		out, err := p.Observe(quote(1, 40), true)
		if err != nil || len(out) != 0 {
			t.Fatal(out, err)
		}
		if !p.P05.Lots[0].Activated || p.P05.Lots[1].Activated {
			t.Fatal("部分約定の建値が平均化されています")
		}
		out, err = p.Observe(quote(2, -100), true)
		if err != nil || len(out) != 0 {
			t.Fatal("損失玉を通常停止しました", out, err)
		}
		out, err = p.Observe(quote(3, 20), true)
		if err != nil || len(out) != 1 || out[0].Quantity != 2 || out[0].Targets[0].ID != "同じ保有ID" {
			t.Fatal("利益玉だけの追尾が不一致", out, err)
		}
		if err = p.OnFill(Fill{SignalID: out[0].ID, ExecutionID: "赤字返済", Quantity: 2, Price: 1000 - float64(side)*10, Time: 1204, Closed: []LotTarget{{"同じ保有ID", 2}}}); err != nil {
			t.Fatal(err)
		}
		if p.P05.Lots[0].Realized != -2000 || p.P05.Lots[1].Quantity != 3 {
			t.Fatal("次約定の損失又は対象玉が不正")
		}
	}
}

// TestP05ReservationHedgeCancelAndRestore は両側上限・予約・取消後の再返済と保存復旧を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestP05ReservationHedgeCancelAndRestore(t *testing.T) {
	p := p05TestPortfolio(t, 3)
	buy := p05TestEntry(t, p, 1, 3)
	sell := p05TestEntry(t, p, -1, 3)
	if p.p05Available(1) != 0 || p.p05Available(-1) != 0 {
		t.Fatal("新規予約が枚数へ未計上")
	}
	p05TestFill(t, p, buy, "買約定", "買保有", 2, 1000, false)
	if p.p05Available(1) != 0 {
		t.Fatal("未約定残を空き枠扱い")
	}
	if err := p.OnOrderDone(buy.ID); err != nil {
		t.Fatal(err)
	}
	if p.p05Available(1) != 1 {
		t.Fatal("取消残を解放していない")
	}
	p05TestFill(t, p, sell, "売約定", "売保有", 3, 1000, true)
	out := p.FlattenOwner("core", 1200, "人工返済")
	if len(out) != 2 || p.p05Available(-1) != 0 {
		t.Fatal("返済予約を空き枠扱い")
	}
	close := out[0]
	if err := p.OnFill(Fill{SignalID: close.ID, ExecutionID: "返済一部", Quantity: 1, Price: 1010, Time: 1201, Closed: []LotTarget{{close.Targets[0].ID, 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := p.OnOrderDone(close.ID); err != nil {
		t.Fatal(err)
	}
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	p, err = Restore(raw)
	if err != nil {
		t.Fatal(err)
	}
	more, err := p.Observe(p05TestObservation(1, 1202, 1000, 1010), false)
	if err != nil || len(more) != 1 || more[0].Quantity != close.Quantity-1 {
		t.Fatal("取消後の残玉返済が復旧しません", more, err)
	}
	if more[0].Targets[0].ID != close.Targets[0].ID {
		t.Fatal("外部又は別玉へ対象が変化")
	}
	bad := Fill{SignalID: more[0].ID, ExecutionID: "対象外", Quantity: 1, Price: 1010, Time: 1203, Closed: []LotTarget{{"手動の玉", 1}}}
	if err = p.OnFill(bad); err == nil {
		t.Fatal("外部保有の返済を許可")
	}
}

// TestP05ActualFillsNeverChangeShadow は実約定価格・手数料・取消を独立影へ混ぜないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestP05ActualFillsNeverChangeShadow(t *testing.T) {
	p := p05TestPortfolio(t, 10)
	before, _ := p.P05.Shadow.Dump()
	s := p05TestEntry(t, p, 1, 5)
	p05TestFill(t, p, s, "実約定", "実保有", 5, 50000, true)
	after, _ := p.P05.Shadow.Dump()
	if !bytes.Equal(before, after) {
		t.Fatal("実玉が影の会計へ混入")
	}
	if p.P05.Lots[0].Fees != 7 {
		t.Fatal("実費用を記録していない")
	}
	for _, v := range p.Status().Positions {
		if v.Quantity > 0 && v.Average != 50000 {
			t.Fatal("表示に仮想価格を混入")
		}
	}
}

// TestP05BoundaryLateFillAndAdvance は境界後の遅延約定と再準備中の返済要求を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestP05BoundaryLateFillAndAdvance(t *testing.T) {
	p := p05TestPortfolio(t, 10)
	s := p05TestEntry(t, p, 1, 5)
	if err := p.Advance(p05TestObservation(1, 19100, 1000, 1010)); err != nil {
		t.Fatal(err)
	}
	if len(p.Orders) != 1 {
		t.Fatal("履歴Advanceで注文を作成")
	}
	if err := p.OnFill(Fill{SignalID: s.ID, ExecutionID: "境界後", HoldID: "当運転玉", Quantity: 5, Price: 1010, Time: 19101}); err != nil {
		t.Fatal(err)
	}
	_ = p.OnOrderDone(s.ID)
	out, err := p.Observe(p05TestObservation(2, 19102, 1000, 1010), true)
	if err != nil || len(out) != 1 || out[0].Action != "close" || out[0].Targets[0].ID != "当運転玉" {
		t.Fatal("境界後玉を返済していない", out, err)
	}
	if p.P05.SourceNotifications != 0 {
		t.Fatal("人工未知特徴から影入口を生成")
	}
}

// TestP05ShadowPendingUsesNextValidQuote は仮想注文の当行約定・異常板約定を禁止します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestP05ShadowPendingUsesNextValidQuote(t *testing.T) {
	p := p05TestPortfolio(t, 10)
	shadow := p.P05.Shadow
	sleeve := shadow.sleeve("core")
	sleeve.Campaign = 1
	sleeve.Side = 1
	sleeve.Requested[0] = 5
	sleeve.Accepted[1] = true
	s := shadow.emitResearch(sleeve, p05TestObservation(1, 1200, 1000, 1010), ResearchEvent{Action: "new", Side: 1, Quantity: 5, Campaign: 1}, nil)
	p.P05.ShadowPending = []Signal{s}
	if _, err := p.stepP05Shadow(p05TestObservation(1, 1200, 1000, 1010)); err != nil {
		t.Fatal(err)
	}
	if sleeve.held() != 0 {
		t.Fatal("判断行で仮想約定")
	}
	bad := p05TestObservation(2, 1201, 2000, 1990)
	if _, err := p.stepP05Shadow(bad); err != nil {
		t.Fatal(err)
	}
	if sleeve.held() != 0 {
		t.Fatal("異常気配で仮想約定")
	}
	if _, err := p.stepP05Shadow(p05TestObservation(3, 1202, 1100, 1110)); err != nil {
		t.Fatal(err)
	}
	if sleeve.held() != 5 || sleeve.Lots[0].Price != 1110 || sleeve.Fees != 0 || len(p.P05.Lots) != 0 {
		t.Fatal("仮想約定価格又は実玉との分離が不正")
	}
}

// TestP05SnapshotRetainsOrdersAndRules は保存中の注文と条件を復旧し、未知の玉への改竄を拒否します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestP05SnapshotRetainsOrdersAndRules(t *testing.T) {
	p := p05TestPortfolio(t, 1)
	s := p05TestEntry(t, p, 1, 1)
	p05TestFill(t, p, s, "約定", "所有", 1, 1000, true)
	p.Flatten(1200, "保存前返済")
	raw, _ := p.Dump()
	q, err := Restore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Status().Pending) != 1 || q.Status().Pending[0].ID != p.Status().Pending[0].ID {
		t.Fatal("返済IDが復旧で変化")
	}
	q.Orders[q.Status().Pending[0].ID].P05LotID = "他運転の玉"
	bad, _ := json.Marshal(q)
	if _, err = Restore(bad); err == nil {
		t.Fatal("破損した対象を受理")
	}
}

// TestP05AdvanceDoesNotSendPastProfit は未送信の過去利確を現在の損失返済へ変換しません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestP05AdvanceDoesNotSendPastProfit(t *testing.T) {
	p := p05TestPortfolio(t, 10)
	s := p05TestEntry(t, p, 1, 5)
	p05TestFill(t, p, s, "現在玉", "現在保有", 5, 1000, true)
	if err := p.Advance(p05TestObservation(1, 1200, 1040, 1050)); err != nil {
		t.Fatal(err)
	}
	if err := p.Advance(p05TestObservation(2, 1201, 1030, 1040)); err != nil {
		t.Fatal(err)
	}
	if p.P05.Lots[0].Goal || !p.P05.Lots[0].Activated {
		t.Fatal("過去利確を発注要求にした又は最高益を忘れた")
	}
	out, err := p.Observe(p05TestObservation(3, 1202, 990, 1000), true)
	if err != nil || len(out) != 0 {
		t.Fatal("復帰時の赤字玉を過去利確で返済", out, err)
	}
	out, err = p.Observe(p05TestObservation(4, 1203, 1020, 1030), true)
	if err != nil || len(out) != 1 {
		t.Fatal("復帰後の現在利益を再確認していない", out, err)
	}
}

// TestP05SameHoldClosesSequentially は証券会社の同一保有IDへ重複返済を予約しません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestP05SameHoldClosesSequentially(t *testing.T) {
	p := p05TestPortfolio(t, 10)
	s := p05TestEntry(t, p, 1, 5)
	p05TestFill(t, p, s, "別約定A", "共有保有", 2, 1000, false)
	p05TestFill(t, p, s, "別約定B", "共有保有", 3, 1020, true)
	out := p.Flatten(1200, "全返済")
	if len(out) != 1 || out[0].Quantity != 2 {
		t.Fatal("同一保有へ同時返済", out)
	}
	p05TestFill(t, p, out[0], "返済A", "", 2, 1030, true)
	out, err := p.Observe(p05TestObservation(1, 1201, 1030, 1040), false)
	if err != nil || len(out) != 1 || out[0].Quantity != 3 {
		t.Fatal("同一保有の残玉返済が進まない", out, err)
	}
}
