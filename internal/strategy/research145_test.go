package strategy

import (
	"math"
	"testing"
)

// research145Portfolio はD07の管理層を検査する人工試験器を作ります。
//
// 引数: t(*testing.T)、kind(string)。返り値: *Portfolio。源の独立再生とは分けた管理層の検査です。
func research145Portfolio(t *testing.T, kind string) *Portfolio {
	p := researchTestPortfolio(t, "D07", 10)
	p.Session.Kind = kind
	return p
}

// TestD07PartialProtectionUsesActualProfitableFill は実際の部分利確後だけ最高利益を測ることを検査します。
//
// 引数: t(*testing.T)。返り値: なし。未約定の希望価格や、部分利確前の高値を使用しません。
func TestD07PartialProtectionUsesActualProfitableFill(t *testing.T) {
	p := research145Portfolio(t, "Day")
	s := p.sleeve("core")
	o := researchTestObservation(0, 100, 990)
	entry := p.nativeResearch(s, o, []ResearchEvent{{Action: "new", Side: 1, Quantity: 5, Campaign: 0, Ticket: 0}}, true, true)[0]
	researchTestFill(t, p, entry, 1000, 192)
	p.managePartialProtection145(s, researchTestObservation(1, 102, 1600))
	o = researchTestObservation(2, 103, 1200)
	p.nativeResearch(s, o, []ResearchEvent{{Action: "close", Side: -1, Quantity: 2, Campaign: 0, Ticket: 0, Reason: 7}}, true, true)
	closes := p.flushResearchGoals(s, o)
	if len(closes) != 1 || s.PartialProtection != nil {
		t.Fatal("未約定で保護を開始しました")
	}
	researchTestFill(t, p, closes[0], 1200, 77)
	if s.PartialProtection == nil || s.PartialProtection.Anchor != 1000 || s.PartialProtection.Best != 200 {
		t.Fatalf("開始値が不正です: %+v", s.PartialProtection)
	}
	p.managePartialProtection145(s, researchTestObservation(3, 105, 1300))
	bad := researchTestObservation(4, 106, 9999)
	bad.QuoteOK = false
	p.managePartialProtection145(s, bad)
	p.managePartialProtection145(s, researchTestObservation(5, 107, 1151))
	if s.Blocked || s.PartialProtection.Best != 300 {
		t.Fatal("不正板又は半減前で返済しました")
	}
	p.managePartialProtection145(s, researchTestObservation(6, 108, 1150))
	closes = p.flushResearchGoals(s, researchTestObservation(6, 108, 1150))
	if len(closes) != 1 || closes[0].Quantity != 3 || closes[0].ReasonCode != 14531 || !s.Blocked {
		t.Fatal(closes)
	}
	researchTestFill(t, p, closes[0], 1140, 115)
	if extra := p.nativeResearch(s, researchTestObservation(7, 110, 1150), []ResearchEvent{{Action: "new", Side: 1, Quantity: 1, Campaign: 0, Ticket: 1}}, true, true); len(extra) != 0 {
		t.Fatal("強制返済済みの親に追撃しました")
	}
	entry = p.nativeResearch(s, researchTestObservation(8, 111, 1150), []ResearchEvent{{Action: "new", Side: 1, Quantity: 5, Campaign: 1, Ticket: 0}}, true, true)[0]
	if entry.Campaign != 1 || s.PartialProtection != nil {
		t.Fatal("次の親に前親の保護値を引継ぎました")
	}
}

// TestD07PartialProtectionExcludesLossAndFullClose は損失返済と全返済がP01を開始しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07PartialProtectionExcludesLossAndFullClose(t *testing.T) {
	for _, test := range []struct {
		reason, qty int
		price       float64
	}{{7, 2, 990}, {32, 2, 1200}, {7, 5, 1200}} {
		p := research145Portfolio(t, "Day")
		s := p.sleeve("core")
		o := researchTestObservation(0, 100, 990)
		entry := p.nativeResearch(s, o, []ResearchEvent{{Action: "new", Side: 1, Quantity: 5, Campaign: 0, Ticket: 0}}, true, true)[0]
		researchTestFill(t, p, entry, 1000, 192)
		o = researchTestObservation(1, 102, test.price)
		p.nativeResearch(s, o, []ResearchEvent{{Action: "close", Side: -1, Quantity: test.qty, Campaign: 0, Ticket: 0, Reason: test.reason}}, true, true)
		closes := p.flushResearchGoals(s, o)
		if len(closes) != 1 {
			t.Fatal(closes)
		}
		researchTestFill(t, p, closes[0], test.price, 77)
		if s.PartialProtection != nil {
			t.Fatal("対象外の返済で保護を開始しました", test)
		}
	}
}

// TestD07NightResidualCapIsCoreOnly は160pt上限を基本の残り1枚だけへ適用することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07NightResidualCapIsCoreOnly(t *testing.T) {
	for _, owner := range []string{"core", "episode"} {
		p := research145Portfolio(t, "Night")
		s := p.sleeve(owner)
		s.Campaign, s.Side, s.Stage, s.FixedR, s.Best, s.Budget = 0, 1, 2, 100, 300, 100000
		s.Retained = "残玉"
		s.Lots = []ResearchLot{{ID: "残玉", Ticket: 0, Quantity: 1, Price: 1000}}
		p.researchManage(s, researchTestObservation(0, 100, 1141))
		if s.Blocked {
			t.Fatal("幅の手前で返済しました")
		}
		p.researchManage(s, researchTestObservation(1, 101, 1140))
		if owner == "core" && (!s.Blocked || s.ForceReason != 14504) {
			t.Fatal("基本の160pt上限が効きません")
		}
		if owner == "episode" && s.Blocked {
			t.Fatal("別入口まで狭めました")
		}
	}
}

// TestD07AccountingKeepsActualFeesButIgnoresThemInPolicy は表示会計と研究判断の費用を混同しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07AccountingKeepsActualFeesButIgnoresThemInPolicy(t *testing.T) {
	p := research145Portfolio(t, "Night")
	s := p.sleeve("core")
	s.Side, s.Stage, s.FixedR = 1, 1, 100
	s.Retained = "残玉"
	s.Cash, s.Fees = -5, 100
	s.Lots = []ResearchLot{{ID: "残玉", Ticket: 0, Quantity: 1, Price: 1000}}
	p.researchManage(s, researchTestObservation(0, 100, 1100))
	if s.Budget != 95 || s.Blocked || s.Cash != -5 || s.Fees != 100 {
		t.Fatalf("費用方針又は実会計が不正です: %+v", s)
	}
	status := p.researchStatus()
	if status.Positions[0].Realized != 95 || status.Positions[0].Fees != 100 {
		t.Fatal("表示の実手数料を消しました")
	}
}

// TestD07PartialProtectionIsSymmetric はショートでも同じ利益返し率で返済することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07PartialProtectionIsSymmetric(t *testing.T) {
	p := research145Portfolio(t, "Day")
	s := p.sleeve("core")
	s.Side, s.Campaign = -1, 0
	s.Lots = []ResearchLot{{ID: "残玉", Ticket: 0, Quantity: 3, Price: 1000}}
	p.armPartialProtection145(s, Fill{Price: 800, Time: 100}, Signal{Action: "close", ReasonCode: 7}, 40000)
	p.managePartialProtection145(s, researchTestObservation(1, 101, 690))
	p.managePartialProtection145(s, researchTestObservation(2, 102, 840))
	if s.ForceReason != 14531 || s.PartialProtection.Best != 300 {
		t.Fatal("ショートの50%戻りが非対称です")
	}
}

// TestD07ProtectionDiagnostics は運転ログがP01とR05の実際の閾値を表示することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07ProtectionDiagnostics(t *testing.T) {
	p := research145Portfolio(t, "Day")
	s := p.sleeve("core")
	s.Side = 1
	s.Lots = []ResearchLot{{ID: "残玉", Ticket: 0, Quantity: 3, Price: 1000}}
	s.PartialProtection = &ResearchPartial145{Anchor: 1000, Best: 300, StartedAt: 100}
	d := p.ExitDiagnostics(researchTestObservation(1, 101, 1200))[0]["partial_profit_protection"].(map[string]any)
	if d["stop_price"] != 1150. || d["floor_gain_points"] != 150. {
		t.Fatal(d)
	}
	p.Session.Kind = "Night"
	s.PartialProtection = nil
	s.Stage = 2
	s.FixedR = 100
	s.Retained = "残玉"
	s.Best = 300
	s.Budget = 100000
	d = p.ExitDiagnostics(researchTestObservation(2, 102, 1200))[0]["residual_exit"].(map[string]any)
	if d["trail_width_points"] != 160. || d["stop_price"] != 1140. {
		t.Fatal(d)
	}
}

// TestD07FixedRFallsBackWhenATRIsUnknown はATR欠測時に研究原本と同じ8呼値を固定することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。旧TG04の数値扱いは変更しません。
func TestD07FixedRFallsBackWhenATRIsUnknown(t *testing.T) {
	for _, atr := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, -10, 90} {
		p := research145Portfolio(t, "Day")
		s := p.sleeve("core")
		o := researchTestObservation(0, 100, 990)
		o.ATR = atr
		entry := p.nativeResearch(s, o, []ResearchEvent{{Action: "new", Side: 1, Quantity: 5, Campaign: 0, Ticket: 0}}, true, true)
		want := 80.
		if atr == 90 {
			want = 90
		}
		if len(entry) != 1 || s.FixedR != want {
			t.Fatalf("ATR=%v、固定R=%v、期待=%v", atr, s.FixedR, want)
		}
	}
}

// TestD07PartialPeakContinuesWhileOrderIsPending は部分返済の照合中も利益返しを記憶することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。追加返済は未完了注文の終了を待ってから送ります。
func TestD07PartialPeakContinuesWhileOrderIsPending(t *testing.T) {
	p := research145Portfolio(t, "Day")
	s := p.sleeve("core")
	o := researchTestObservation(0, 100, 990)
	entry := p.nativeResearch(s, o, []ResearchEvent{{Action: "new", Side: 1, Quantity: 5, Campaign: 0, Ticket: 0}}, true, true)[0]
	researchTestFill(t, p, entry, 1000, 0)
	o = researchTestObservation(1, 102, 1200)
	p.nativeResearch(s, o, []ResearchEvent{{Action: "close", Side: -1, Quantity: 2, Campaign: 0, Ticket: 0, Reason: 7}}, true, true)
	partial := p.flushResearchGoals(s, o)[0]
	if err := p.OnFill(Fill{SignalID: partial.ID, ExecutionID: "部分約定", Quantity: 1, Price: 1200, Time: 103}); err != nil {
		t.Fatal(err)
	}
	p.managePartialProtection145(s, researchTestObservation(2, 104, 1300))
	p.managePartialProtection145(s, researchTestObservation(3, 105, 1140))
	if !s.Blocked || s.PartialProtection.Best != 300 {
		t.Fatal("照合中の利益返しを失いました")
	}
	if v := p.flushResearchGoals(s, researchTestObservation(3, 105, 1140)); len(v) > 0 {
		t.Fatal("未完了注文の照合前に返済しました")
	}
	if err := p.OnOrderDone(partial.ID); err != nil {
		t.Fatal(err)
	}
	closes := p.flushResearchGoals(s, researchTestObservation(4, 106, 1140))
	if len(closes) != 1 || closes[0].Quantity != 4 || closes[0].ReasonCode != 14531 {
		t.Fatal("照合後に全残玉を返済しません", closes)
	}
}
