package strategy

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// s01TestEntry は人工の別入口通知を通常の予約経路へ渡します。
//
// 引数: t(*testing.T)、p(*Portfolio)、side/campaign/quantity(int)。返り値: Signalは初回予約です。
func s01TestEntry(t *testing.T, p *Portfolio, side, campaign, quantity int) Signal {
	t.Helper()
	out := p.nativeResearch(p.sleeve("episode"), researchTestObservation(0, 100, 990), []ResearchEvent{{Action: "new", Side: side, Quantity: quantity, Campaign: campaign, Ticket: 0, Risk: 80}}, true, true)
	if len(out) != 1 {
		t.Fatalf("新規予約: %+v", out)
	}
	return out[0]
}

// s01TestOpen は元D07と同じ配分で初回2枚を全量約定します。
//
// 引数: t(*testing.T)、side(int)。返り値: *Portfolioは実2枚を持つS01です。
func s01TestOpen(t *testing.T, side int) *Portfolio {
	t.Helper()
	p := researchTestPortfolio(t, "S01", 10)
	sig := s01TestEntry(t, p, side, 0, 2)
	researchTestFill(t, p, sig, 1000, 400)
	return p
}

// TestS01PartialInitialOrder は初回部分約定の累積・取消確定・過去ピーク破棄を確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestS01PartialInitialOrder(t *testing.T) {
	p := researchTestPortfolio(t, "S01", 10)
	sig := s01TestEntry(t, p, 1, 0, 2)
	first := Fill{SignalID: sig.ID, ExecutionID: "一部1", HoldID: "同一玉", Quantity: 1, Price: 1000, Time: 101, Fee: 100}
	if err := p.OnFill(first); err != nil {
		t.Fatal(err)
	}
	p.manageS01Guard(p.sleeve("episode"), researchTestObservation(1, 102, 1300), true)
	if p.S01.Guard.Active || p.S01.Guard.Best != 0 {
		t.Fatal("初回未確定の過去ピークを使用")
	}
	if err := p.OnFill(first); err != nil {
		t.Fatal(err)
	}
	if p.S01.Guard.InitialQuantity != 1 {
		t.Fatal("重複約定を累積")
	}
	second := Fill{SignalID: sig.ID, ExecutionID: "一部2", HoldID: "同一玉", Quantity: 1, Price: 1020, Time: 103, Fee: 100}
	if err := p.OnFill(second); err != nil {
		t.Fatal(err)
	}
	if err := p.OnOrderDone(sig.ID); err != nil {
		t.Fatal(err)
	}
	s := p.sleeve("episode")
	if p.S01.Guard.InitialQuantity != 2 || !p.S01.Guard.InitialDone || len(s.Lots) != 1 || s.Lots[0].Price != 1010 {
		t.Fatal(s, p.S01.Guard)
	}
	p.manageS01Guard(s, researchTestObservation(2, 104, 1045), true)
	if p.S01.Guard.Active {
		t.Fatal("初回未確定時の高値を遡及使用")
	}
	p.manageS01Guard(s, researchTestObservation(3, 105, 1050), true)
	if !p.S01.Guard.Active || p.S01.Guard.Best != 8000 || p.S01.Guard.Floor != 4000 {
		t.Fatal(p.S01.Guard)
	}
	q := researchTestPortfolio(t, "S01", 10)
	pending := s01TestEntry(t, q, 1, 0, 2)
	first.SignalID = pending.ID
	first.ExecutionID = "取消前1"
	if err := q.OnFill(first); err != nil {
		t.Fatal(err)
	}
	if err := q.OnOrderDone(pending.ID); err != nil {
		t.Fatal(err)
	}
	if q.S01.Guard.InitialQuantity != 1 || !q.S01.Guard.InitialDone {
		t.Fatal("取消までの実枚数を固定していない")
	}
}

// TestS01FreshnessAndOriginalExit は古い床割れを発注せず、新鮮な判断と元退出の優先を確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestS01FreshnessAndOriginalExit(t *testing.T) {
	for _, advance := range []bool{false, true} {
		p := s01TestOpen(t, 1)
		s := p.sleeve("episode")
		p.manageS01Guard(s, researchTestObservation(1, 102, 1080), true)
		var err error
		if advance {
			err = p.Advance(researchTestObservation(2, 103, 1030))
		} else {
			_, err = p.ObserveWithFreshness(researchTestObservation(2, 103, 1030), false, false)
		}
		if err != nil {
			t.Fatal(err)
		}
		if s.Blocked || len(s.Goals) != 0 {
			t.Fatal("古い床割れから返済目標を生成")
		}
		out, err := p.ObserveWithFreshness(researchTestObservation(3, 104, 1070), false, true)
		if err != nil || len(out) != 0 {
			t.Fatal(err, out)
		}
		out, err = p.ObserveWithFreshness(researchTestObservation(4, 105, 1030), false, true)
		if err != nil || len(out) != 1 || out[0].Quantity != 2 || out[0].ReasonCode != s01GuardReason {
			t.Fatal(err, out)
		}
		if _, err = p.ObserveWithFreshness(researchTestObservation(5, 106, 1100), false, false); err != nil {
			t.Fatal(err)
		}
		if !s.Blocked || len(s.Goals) == 0 {
			t.Fatal("既決定の返済目標を取消")
		}
	}
	p := s01TestOpen(t, 1)
	s := p.sleeve("episode")
	p.manageS01Guard(s, researchTestObservation(1, 102, 1080), true)
	s.setGoal(s.Lots[0].ID, 0, 1436)
	p.manageS01Guard(s, researchTestObservation(2, 103, 1030), true)
	if s.Goals[s.Lots[0].ID].Reason != 1436 {
		t.Fatal("元退出を上書き")
	}
	if s01EntryNotClosing(s, false) {
		t.Fatal("元退出中に追加を許可")
	}
}

// TestS01ParentGrossValue は部分利確後も親全体を評価し、費用や追加で基準を変えません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestS01ParentGrossValue(t *testing.T) {
	for _, side := range []int{1, -1} {
		p := s01TestOpen(t, side)
		s := p.sleeve("episode")
		sig := p.emitResearch(s, researchTestObservation(1, 102, 1090), ResearchEvent{Action: "close", Side: -side, Quantity: 1, Campaign: 0, Ticket: 0}, []LotTarget{{ID: s.Lots[0].ID, Quantity: 1}})
		researchTestFill(t, p, sig, 1000+float64(side)*100, 250)
		bid := 1040.
		if side < 0 {
			bid = 950
		}
		p.manageS01Guard(s, researchTestObservation(2, 104, bid), true)
		if p.S01.Guard.Best != 14000 || p.S01.Guard.Floor != 7000 || p.S01.Guard.InitialQuantity != 2 || p.S01.Guard.Risk != 80 {
			t.Fatal(side, p.S01.Guard, s.Cash, s.Fees)
		}
		bid = 960
		if side < 0 {
			bid = 1030
		}
		p.manageS01Guard(s, researchTestObservation(3, 105, bid), true)
		if !s.Blocked || s.ForceReason != s01GuardReason {
			t.Fatal(side, s)
		}
	}
}

// TestS01ShadowPrivateNextQuote は影の次正常気配約定・費用0・外部注文への非混入を確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestS01ShadowPrivateNextQuote(t *testing.T) {
	p := researchTestPortfolio(t, "S01", 10)
	shadow := p.S01.Shadow
	s := shadow.sleeve("episode")
	sigs := shadow.nativeResearch(s, researchTestObservation(0, 100, 990), []ResearchEvent{{Action: "new", Side: 1, Quantity: 2, Campaign: 0, Ticket: 0}}, true, true)
	p.S01.ShadowPending = append(p.S01.ShadowPending, sigs...)
	out, err := p.ObserveWithFreshness(researchTestObservation(0, 100, 1000), false, false)
	if err != nil || len(out) > 0 || s.held() != 0 {
		t.Fatal(err, out, s)
	}
	invalid := researchTestObservation(1, 101, 1000)
	invalid.QuoteOK = false
	if _, err = p.ObserveWithFreshness(invalid, false, false); err != nil {
		t.Fatal(err)
	}
	if s.held() != 0 {
		t.Fatal("異常板で影約定")
	}
	if _, err = p.ObserveWithFreshness(researchTestObservation(2, 102, 1090), false, false); err != nil {
		t.Fatal(err)
	}
	if s.held() != 2 || s.Lots[0].Price != 1100 || s.Fees != 0 || len(p.Orders) != 0 || p.sleeve("episode").held() != 0 {
		t.Fatal("影と実口座の混入", s, p.Orders)
	}
	if p.S01.Shadow.Config.Namespace == p.Config.Namespace {
		t.Fatal("影ID空間未分離")
	}
}

// TestS01ShadowAdmission は旧親占有時の新親拒否を固定し、同じ親のBlockedを解除しません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestS01ShadowAdmission(t *testing.T) {
	p := s01TestOpen(t, 1)
	base := p.S01.Shadow.sleeve("episode")
	base.Campaign = 10
	base.Side = 1
	base.Lots = []ResearchLot{{ID: "影旧玉", Quantity: 1, Price: 1000}}
	s := p.sleeve("episode")
	s.Lots = nil
	event := ResearchEvent{Action: "new", Side: 1, Quantity: 2, Campaign: 11, Ticket: 0}
	if out := p.nativeResearch(s, researchTestObservation(1, 102, 1000), []ResearchEvent{event}, true, true); len(out) != 0 {
		t.Fatal(out)
	}
	if p.S01.RejectedParents != 1 || s.Accepted[11] {
		t.Fatal(p.S01.LastRejection)
	}
	base.Lots = nil
	if out := p.nativeResearch(s, researchTestObservation(2, 103, 1000), []ResearchEvent{event}, true, true); len(out) != 0 {
		t.Fatal("拒否済親を復活")
	}
	event.Campaign = 12
	out := p.nativeResearch(s, researchTestObservation(3, 104, 1000), []ResearchEvent{event}, true, true)
	if len(out) != 1 || p.S01.Guard.Campaign != 12 || p.S01.Guard.InitialQuantity != 0 {
		t.Fatal(out, p.S01.Guard)
	}
	researchTestFill(t, p, out[0], 1000, 0)
	s.Blocked = true
	event.Ticket = 1
	if out = p.nativeResearch(s, researchTestObservation(4, 106, 1100), []ResearchEvent{event}, true, true); len(out) != 0 || !s.Blocked {
		t.Fatal("同親許可が保護停止を解除")
	}
}

// TestS01RestoreAndDiagnostics は途中の初回と利益保護・影予約を保存し、診断を読取専用に保ちます。
//
// 引数: t(*testing.T)。返り値: なし。
func TestS01RestoreAndDiagnostics(t *testing.T) {
	p := researchTestPortfolio(t, "S01", 10)
	sig := s01TestEntry(t, p, 1, 0, 2)
	if err := p.OnFill(Fill{SignalID: sig.ID, ExecutionID: "初回部分", HoldID: "初回玉", Quantity: 1, Price: 1000, Time: 101}); err != nil {
		t.Fatal(err)
	}
	before, _ := p.Dump()
	restored, err := Restore(before)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := restored.Dump()
	if string(before) != string(after) {
		t.Fatal("初回部分保存の差")
	}
	if err = restored.OnOrderDone(sig.ID); err != nil {
		t.Fatal(err)
	}
	restored.manageS01Guard(restored.sleeve("episode"), researchTestObservation(1, 102, 1080), true)
	before, _ = restored.Dump()
	diag := restored.ExitDiagnostics(researchTestObservation(2, 103, 1080))
	after, _ = restored.Dump()
	if string(before) != string(after) || len(diag) != 3 || diag[2]["method"] != "S01" {
		t.Fatal("診断による変更", diag)
	}
	restored, err = Restore(before)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.S01.Guard.Active || restored.S01.Guard.InitialQuantity != 1 {
		t.Fatal(restored.S01.Guard)
	}
	restored.S01.Guard = nil
	bad, _ := restored.Dump()
	if _, err = Restore(bad); err == nil {
		t.Fatal("実玉の保護状態欠落を許可")
	}
}

// TestS01RulesInvalidFloor は外部床の不正値で返却注文を破棄し、継続運転も止めます。
//
// 引数: t(*testing.T)。返り値: なし。
func TestS01RulesInvalidFloor(t *testing.T) {
	for _, formula := range []string{"-1.0", "best_value*2.0"} {
		p := s01TestOpen(t, 1)
		set, err := rules.DefaultFor("s01")
		if err != nil {
			t.Fatal(err)
		}
		p.Config.RuleExpressions = set.Expressions()
		p.Config.RuleExpressions["s01.guard_floor"] = formula
		if err = p.bindRules(); err != nil {
			t.Fatal(err)
		}
		out, err := p.ObserveWithFreshness(researchTestObservation(1, 102, 1080), true, true)
		if err == nil || len(out) != 0 || p.S01.Failure == "" {
			t.Fatal(formula, err, out)
		}
		if _, err = p.Observe(researchTestObservation(2, 103, 1090), true); err == nil {
			t.Fatal("不正床後に運転継続")
		}
	}
}

// TestS01SmallCapsAndCommonConfiguration は少枚数・D07共通式・夜間拒否を確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestS01SmallCapsAndCommonConfiguration(t *testing.T) {
	for _, cap := range []int{1, 2, 3, 10, 11, 20} {
		p := researchTestPortfolio(t, "S01", cap)
		d := researchTestPortfolio(t, "D07", cap)
		if !reflect.DeepEqual(ResearchAllocation("D07", cap), ResearchAllocation(p.Research.Method, cap)) {
			t.Fatal(cap)
		}
		if p.S01.Shadow.Config.Cap != cap || p.S01.Shadow.Research.Method != "D07" {
			t.Fatal(cap)
		}
		for i, s := range p.Research.Sleeves {
			a, _ := json.Marshal(s.Source)
			b, _ := json.Marshal(d.Research.Sleeves[i].Source)
			if string(a) != string(b) {
				t.Fatal("共通源の初期値差", cap, s.Name)
			}
		}
		cfg := p.Config
		session := p.Session
		session.Kind = "Night"
		if _, err := New(cfg, session); err == nil {
			t.Fatal("夜S01を許可")
		}
	}
}

// TestS01BoundaryPendingAndCloseRetry は境界直前の予約約定を所有IDだけで返し、取消後も返済を維持します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestS01BoundaryPendingAndCloseRetry(t *testing.T) {
	p := researchTestPortfolio(t, "S01", 10)
	entry := s01TestEntry(t, p, 1, 0, 2)
	if out := p.Flatten(p.Session.ForceClose, "時間枠終了"); len(out) != 0 {
		t.Fatal("未約定玉を架空返済", out)
	}
	researchTestFill(t, p, entry, 1000, 0)
	out, err := p.ObserveWithFreshness(researchTestObservation(1, p.Session.ForceClose+1, 990), false, true)
	if err != nil || len(out) != 1 || out[0].ReasonCode != 93 || out[0].Quantity != 2 {
		t.Fatal(err, out)
	}
	if len(out[0].Targets) != 1 || out[0].Targets[0].ID != entry.ID+"玉" {
		t.Fatal("外部所有ID混入", out)
	}
	if err = p.OnOrderRejected(out[0].ID); err != nil {
		t.Fatal(err)
	}
	out, err = p.ObserveWithFreshness(researchTestObservation(2, p.Session.ForceClose+2, 980), false, true)
	if err != nil || len(out) != 1 || out[0].Quantity != 2 || out[0].ReasonCode != 93 {
		t.Fatal(err, out)
	}
	if err = p.OnFill(Fill{SignalID: out[0].ID, ExecutionID: "外部玉混入", Quantity: 1, Price: 980, Time: p.Session.ForceClose + 3, Closed: []LotTarget{{ID: "手動所有玉", Quantity: 1}}}); err == nil {
		t.Fatal("未所有玉の返済を許可")
	}
	researchTestFill(t, p, out[0], 980, 0)
	if p.sleeve("episode").held() != 0 {
		t.Fatal("境界後の実玉が残った")
	}
}

// TestS01ShadowReservationRestore は影の未約定予約を復元後も次正常板で一度だけ約定させます。
//
// 引数: t(*testing.T)。返り値: なし。
func TestS01ShadowReservationRestore(t *testing.T) {
	p := researchTestPortfolio(t, "S01", 10)
	shadow := p.S01.Shadow
	signals := shadow.nativeResearch(shadow.sleeve("episode"), researchTestObservation(0, 100, 990), []ResearchEvent{{Action: "new", Side: -1, Quantity: 2, Campaign: 0, Ticket: 0}}, true, true)
	p.S01.ShadowPending = append(p.S01.ShadowPending, signals...)
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	p, err = Restore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.ObserveWithFreshness(researchTestObservation(1, 101, 990), false, true); err != nil {
		t.Fatal(err)
	}
	if p.S01.Shadow.sleeve("episode").held() != 2 || len(p.S01.ShadowPending) != 0 || len(p.Orders) != 0 {
		t.Fatal("影予約の復元差")
	}
	if _, err = p.ObserveWithFreshness(researchTestObservation(2, 102, 990), false, true); err != nil {
		t.Fatal(err)
	}
	if p.S01.Shadow.sleeve("episode").held() != 2 {
		t.Fatal("影予約の二重約定")
	}
	p.S01.ShadowPending = signals
	raw, _ = p.Dump()
	if _, err = Restore(raw); err == nil {
		t.Fatal("済み注文を影予約として復元")
	}
}
