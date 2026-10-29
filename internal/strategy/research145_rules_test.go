package strategy

import (
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// research145Rules は試験対象の式だけを標準一式から変更します。
//
// 引数: t(*testing.T)、p(*Portfolio)、changes(map[string]string)。返り値: なし。
func research145Rules(t *testing.T, p *Portfolio, changes map[string]string) {
	t.Helper()
	set, err := rules.Default()
	if err != nil {
		t.Fatal(err)
	}
	expressions := set.Expressions()
	for key, expression := range changes {
		expressions[key] = expression
	}
	p.Rules, err = rules.NewRuntime(expressions)
	if err != nil {
		t.Fatal(err)
	}
}

// TestD07ExternalPartialFloor は変更した式を返済判定と診断表示の両方が使用することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07ExternalPartialFloor(t *testing.T) {
	p := research145Portfolio(t, "Day")
	research145Rules(t, p, map[string]string{"partial.floor": "0.8 * best"})
	s := p.sleeve("core")
	s.Side = 1
	s.Lots = []ResearchLot{{ID: "試験玉", Quantity: 3, Price: 1000}}
	s.PartialProtection = &ResearchPartial145{Anchor: 1000, Best: 100, StartedAt: 90}
	p.managePartialProtection145(s, researchTestObservation(0, 100, 1081))
	if s.Blocked {
		t.Fatal("外部下限に届く前に返済しました")
	}
	for _, d := range p.ExitDiagnostics(researchTestObservation(0, 100, 1081)) {
		if d["owner"] == "core" {
			protection := d["partial_profit_protection"].(map[string]any)
			if protection["floor_gain_points"] != 80. || protection["stop_price"] != 1080. {
				t.Fatal("表示が外部式に追従していません", protection)
			}
		}
	}
	p.managePartialProtection145(s, researchTestObservation(1, 101, 1080))
	if !s.Blocked || s.ForceReason != 14531 || p.Rules.Err() != nil {
		t.Fatal("外部下限で返済されません", p.Rules.Err())
	}
}

// TestD07ExternalEntryGates は昼夜の条件と未知値の扱いを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07ExternalEntryGates(t *testing.T) {
	p := research145Portfolio(t, "Day")
	research145Rules(t, p, map[string]string{"core.fixed_r": "max(4 * tick, atr)", "core.day_gate": "!own_known || signed_move_r >= -0.1", "core.peer_fresh": "age_seconds <= 5"})
	o := researchTestObservation(0, 100, 1000)
	if r := p.researchFixedR145(o); r != 40 {
		t.Fatal("入口幅の式が反映されません", r)
	}
	if !p.researchGate(o, 1, 40) {
		t.Fatal("未知の変化を逆行扱いしました")
	}
	o.Research.Known[9], o.Research.Known[13] = true, true
	o.Research.Values[9], o.Research.Values[13] = 100, -.05
	if p.researchGate(o, 1, 40) || !p.researchGate(o, -1, 40) {
		t.Fatal("昼の変更閾値が上下対称に反映されません")
	}
	p.Session.Kind = "Night"
	o.Research.Known[21], o.Research.Known[22] = true, true
	o.Research.Values[21], o.Research.Values[22] = -.01, 90
	if !p.researchGate(o, 1, 40) {
		t.Fatal("外部の鮮度条件を超えた板を使いました")
	}
	o.Research.Values[22] = 99
	if p.researchGate(o, 1, 40) {
		t.Fatal("両市場の逆行を見逃しました")
	}
	if p.Rules.Err() != nil {
		t.Fatal(p.Rules.Err())
	}
}

// TestD07ExternalResidualBudget は残玉の実約定予算と追尾幅を外部式から計算することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07ExternalResidualBudget(t *testing.T) {
	p := research145Portfolio(t, "Night")
	research145Rules(t, p, map[string]string{"residual.width": "fixed_r", "residual.budget": "max(0, cash_yen * 0.5)"})
	s := p.sleeve("core")
	s.Side, s.Stage, s.FixedR, s.Cash = 1, 1, 100, 10000
	s.Retained = "残玉"
	s.Lots = []ResearchLot{{ID: "残玉", Quantity: 1, Price: 1000}}
	p.researchManage(s, researchTestObservation(0, 100, 1100))
	if s.Budget != 5000 || s.Stage != 2 || s.Blocked {
		t.Fatal("残玉予算の外部式が反映されません", s)
	}
	p.researchManage(s, researchTestObservation(1, 101, 1000))
	if !s.Blocked || s.ForceReason != 14504 {
		t.Fatal("変更した追尾幅で返済しません")
	}
}

// TestD07ExternalAdverseHold は反証の持続時間を変更しても有効板の連続性を維持することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07ExternalAdverseHold(t *testing.T) {
	p := research145Portfolio(t, "Day")
	research145Rules(t, p, map[string]string{"core.adverse_hold_seconds": "30"})
	s := p.sleeve("core")
	s.Side, s.MAKnown, s.FixedMA, s.FixedR = 1, true, 1000, 100
	s.Lots = []ResearchLot{{ID: "試験玉", Quantity: 5, Price: 1000}}
	o := researchTestObservation(0, 100, 990)
	for _, i := range []int{9, 10, 13, 28} {
		o.Research.Known[i] = true
	}
	o.Research.Values[9], o.Research.Values[10], o.Research.Values[13], o.Research.Values[28] = 100, 989, -.02, -.03
	for _, at := range []float64{100, 110, 120} {
		o.Time = at
		p.researchManage(s, o)
		if s.Blocked {
			t.Fatal("変更した持続時間の前に返済しました")
		}
	}
	o.Time = 130
	p.researchManage(s, o)
	if !s.Blocked || s.ForceReason != 14312 {
		t.Fatal("変更した持続時間で返済しません")
	}
}

// TestD07ExternalRulesLeaveTG04Unchanged は旧手法がD07専用の外部条件へ引き込まれないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestD07ExternalRulesLeaveTG04Unchanged(t *testing.T) {
	p := researchTestPortfolio(t, "TG04", 10)
	d07 := research145Portfolio(t, "Day")
	research145Rules(t, d07, map[string]string{"core.day_gate": "false", "residual.width": "1", "residual.quiet_before_seconds": "1000"})
	s := p.sleeve("core")
	s.FixedR = 100
	width, capped := p.researchResidualWidth(s)
	if width != 300 || capped || !p.researchGate(researchTestObservation(0, 100, 1000), 1, 100) {
		t.Fatal("TG04の標準条件を変更しました")
	}
	p.Session.USOpen = 2000
	if p.researchQuiet(1050) {
		t.Fatal("TG04の静観時間を変更しました")
	}
}
