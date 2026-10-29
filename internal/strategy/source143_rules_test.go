package strategy

import (
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// TestResearchSourceChildRuleLatch は独立した主担当・補完担当の異常で候補生成前に停止することを確認します。
//
// 引数: t(*testing.T)。返り値: なし。人工状態だけを使い、原データや注文APIには接続しません。
func TestResearchSourceChildRuleLatch(t *testing.T) {
	for _, child := range []string{"主担当", "補完担当"} {
		t.Run(child, func(t *testing.T) {
			p := researchTestPortfolio(t, "D07", 10)
			s := p.sleeve("core").Source
			runtime := s.Legacy.Rules
			if child == "補完担当" {
				runtime = s.Supplement.Rules
			}
			if runtime == nil || runtime == s.Rules {
				t.Fatal("独立した子評価器がありません")
			}
			runtime.Bool("人工的な未定義条件", map[string]any{})
			for i := int64(0); i < 2; i++ {
				events, err := s.Step(researchTestObservation(i, 100+float64(i), 1000))
				if err == nil || len(events) != 0 || !strings.Contains(err.Error(), child) || !strings.Contains(err.Error(), "人工的な未定義条件") {
					t.Fatal("子評価器の異常が候補生成を止めていません", events, err)
				}
				if s.LastIndex != -1 || len(s.PendingLegacy) != 0 {
					t.Fatal("異常保持中に仮想状態を進めました")
				}
			}
			if s.Rules.Err() != nil {
				t.Fatal("別評価器への誤った依存で停止を判定しています")
			}
		})
	}
}

// TestResearchSourceChildRuleFailureDuringStep はnightの直接呼出中の数値異常も返却時に通知破棄することを確認します。
//
// 引数: t(*testing.T)。返り値: なし。人工保有の追尾条件で非有限値を発生させます。
func TestResearchSourceChildRuleFailureDuringStep(t *testing.T) {
	parameters, err := ParametersForMethod("NK225", "Night", "D07")
	if err != nil {
		t.Fatal(err)
	}
	session := activeSession145("Night")
	s, err := newResearchSource(Config{Reference: "NK225", Product: "NK225mini", Cap: 10, LargeTick: 10, PointValue: 100, Parameters: parameters}, session, "core", 5)
	if err != nil {
		t.Fatal(err)
	}
	expressions := s.Legacy.Rules.Set.Expressions()
	expressions["trail.floor"] = "1e308 * 1e308"
	s.Legacy.Rules, err = rules.NewRuntimeFor("u06", expressions)
	if err != nil {
		t.Fatal(err)
	}
	s.Legacy.Core.Position = Position{Owner: "core", Side: 1, Quantity: 1, Average: 1000, Risk: 80, Floor: -80}
	events, err := s.Step(researchTestObservation(0, 100, 1100))
	if err == nil || len(events) != 0 || s.Legacy.Rules.Err() == nil || !strings.Contains(err.Error(), "trail.floor") {
		t.Fatal("直接night評価中の異常を返却していません", events, err)
	}
	if s.Rules.Err() != nil {
		t.Fatal("親評価器の異常に依存しています")
	}
}
