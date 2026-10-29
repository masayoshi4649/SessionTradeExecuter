package strategy

import (
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// TestIndependentD07InvalidRuleStopsConstruction は契約違反の条件で戦略を開始しないことを確かめます。
//
// 引数: t(*testing.T)。返り値: なし。価格データも注文APIも使用しません。
func TestIndependentD07InvalidRuleStopsConstruction(t *testing.T) {
	standard, err := rules.Default()
	if err != nil {
		t.Fatal(err)
	}
	p := researchTestPortfolio(t, "D07", 10)
	cfg := p.Config
	cfg.RuleExpressions = standard.Expressions()
	cfg.RuleExpressions["core.day_gate"] = "123"
	if _, err := New(cfg, p.Session); err == nil {
		t.Fatal("真偽条件に数値を指定しても戦略を開始しました")
	}
}

// TestIndependentD07RuleFailureSuppressesSignals は途中の評価失敗で全発注依頼を外へ出さないことを確かめます。
//
// 引数: t(*testing.T)。返り値: なし。人工建玉だけを使い、実発注や収益性の再生は行いません。
func TestIndependentD07RuleFailureSuppressesSignals(t *testing.T) {
	standard, err := rules.Default()
	if err != nil {
		t.Fatal(err)
	}
	base := researchTestPortfolio(t, "D07", 10)
	cfg := base.Config
	cfg.RuleExpressions = standard.Expressions()
	cfg.RuleExpressions["partial.floor"] = "1e308 * 1e308"
	p, err := New(cfg, base.Session)
	if err != nil {
		t.Fatal(err)
	}
	for _, sleeve := range p.Research.Sleeves {
		if sleeve.Source.Rules != p.Rules {
			t.Fatal("担当間でエラー状態を共有していません", sleeve.Name)
		}
	}
	core := p.sleeve("core")
	core.Side, core.Campaign, core.FixedR = 1, 0, 80
	core.Lots = []ResearchLot{{ID: "人工保有", Ticket: 0, Quantity: 3, Price: 1000}}
	core.PartialProtection = &ResearchPartial145{Anchor: 1000, Best: 300, StartedAt: 90}
	signals, err := p.Observe(researchTestObservation(0, 100, 1100), true)
	if err == nil || len(signals) != 0 || p.Rules.Err() == nil || !strings.Contains(err.Error(), "partial.floor") {
		t.Fatal("途中の非有限値で停止できません", signals, err)
	}
	first := err.Error()
	signals, err = p.Observe(researchTestObservation(1, 101, 1300), true)
	if err == nil || err.Error() != first || len(signals) != 0 {
		t.Fatal("次の正常観測でエラーを解除しました", signals, err)
	}
	if err = p.OnFill(Fill{SignalID: "人工", ExecutionID: "人工約定", Quantity: 1, Price: 1100, Time: 101}); err == nil || err.Error() != first {
		t.Fatal("約定境界が最初のルール異常を保持していません", err)
	}
}
