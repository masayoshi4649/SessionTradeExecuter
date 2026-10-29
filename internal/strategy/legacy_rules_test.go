package strategy

import (
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// legacyRulePortfolio は標準一式の指定条件だけを変え、本番と同じNew経路で構築します。
//
// 引数: t(*testing.T)、symbol/kind/profile(string)、changes(map[string]string)。
// 返り値: *Portfolioは外部条件を読んだ検査器です。
func legacyRulePortfolio(t *testing.T, symbol, kind, profile string, changes map[string]string) *Portfolio {
	t.Helper()
	base := testPortfolio(t, symbol, kind)
	standard, err := rules.DefaultFor(profile)
	if err != nil {
		t.Fatal(err)
	}
	cfg := base.Config
	cfg.RuleExpressions = standard.Expressions()
	for id, expression := range changes {
		cfg.RuleExpressions[id] = expression
	}
	p, err := New(cfg, base.Session)
	if err != nil {
		t.Fatal(err)
	}
	if p.Rules == nil || (kind == "Day" && p.Paper.Rules != p.Rules) {
		t.Fatal("注文経路とE01影へ条件が接続されていません")
	}
	return p
}

// TestLegacyRulesDayEntryChanges は日中の新規追跡幅変更が本番判断経路に届くことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。TOPIX・日経の両T06を確認します。
func TestLegacyRulesDayEntryChanges(t *testing.T) {
	for _, tc := range []struct{ symbol, profile string }{{"TOPIX", "t06_topix"}, {"NK225", "t06_nk225"}} {
		p := legacyRulePortfolio(t, tc.symbol, "Day", tc.profile, map[string]string{"shadow.entry_distance": "2 * atr"})
		p.Paper.Config.Tick = .5
		observe(t, p, observation(p, 0, 1200, 100), true)
		if orders := observe(t, p, observation(p, 1, 1201, 103), true); len(orders) != 0 {
			t.Fatal("2ATRへの反発幅変更が使われていません", orders)
		}
		if p.Paper.Exit.Pending == nil || p.Paper.Exit.Pending.Level != 120 {
			t.Fatal("影の新規幅が変更されていません")
		}
	}
}

// TestLegacyRulesDayPartialExitChanges は部分利確幅の変更が実玉の返済依頼に届くことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestLegacyRulesDayPartialExitChanges(t *testing.T) {
	p := legacyRulePortfolio(t, "TOPIX", "Day", "t06_topix", map[string]string{"shadow.split_distance": "0.5 * atr"})
	observe(t, p, observation(p, 0, 1200, 100), true)
	orders := observe(t, p, observation(p, 1, 1201, 103), true)
	if len(orders) != 1 {
		t.Fatal("人工入口が成立しません", orders)
	}
	fillAll(t, p, orders[0], 103.25, 1201.5)
	orders = observe(t, p, observation(p, 2, 1202, 110), true)
	if len(orders) != 1 || orders[0].Action != "close" || orders[0].Quantity != 2 || orders[0].ReasonCode != 7 {
		t.Fatal("外部式の部分利確条件が返済へ届きません", orders)
	}
}

// TestLegacyRulesNightEntryChanges は夜間T04/U06の方向支持変更が新規を止めることを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestLegacyRulesNightEntryChanges(t *testing.T) {
	for _, tc := range []struct{ symbol, profile string }{{"TOPIX", "t04"}, {"NK225", "u06"}} {
		p := legacyRulePortfolio(t, tc.symbol, "Night", tc.profile, map[string]string{"night.support30": "false"})
		p.Config.LargeTick = .5
		prices, times := []float64{101, 103}, []float64{1400, 1461}
		if tc.symbol == "NK225" {
			prices, times = []float64{108, 102, 105}, []float64{1400, 1401, 1461}
		}
		for i, price := range prices {
			if orders := observe(t, p, observation(p, int64(i), times[i], price), true); len(orders) != 0 {
				t.Fatal("外部支持条件を使わずに新規しました", tc, orders)
			}
		}
	}
}

// TestLegacyRulesExitAndRestore は変更した追尾をJSON復元後も使用し、下限を緩めないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestLegacyRulesExitAndRestore(t *testing.T) {
	p := legacyRulePortfolio(t, "TOPIX", "Night", "t04", map[string]string{"trail.active": "peak >= 5", "trail.floor": "peak - 2"})
	observe(t, p, observation(p, 0, 1400, 101), true)
	orders := observe(t, p, observation(p, 1, 1461, 103), true)
	fillAll(t, p, orders[0], 103.25, 1462)
	observe(t, p, observation(p, 2, 1463, 110), true)
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	p, err = Restore(raw)
	if err != nil {
		t.Fatal(err)
	}
	orders = observe(t, p, observation(p, 3, 1464, 107), true)
	if len(orders) != 1 || orders[0].Action != "close" || orders[0].ReasonCode != 70 {
		t.Fatal("復元後に外部追尾幅を失いました", orders)
	}
}

// TestLegacyRulesFailureSuppressesOrders は外部式の非有限結果を発注前に停止することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestLegacyRulesFailureSuppressesOrders(t *testing.T) {
	p := legacyRulePortfolio(t, "TOPIX", "Day", "t06_topix", map[string]string{"shadow.entry_distance": "1e308 * atr"})
	orders, err := p.Observe(observation(p, 0, 1200, 100), true)
	if err == nil || len(orders) != 0 || p.Rules.Err() == nil {
		t.Fatal("影の幅計算エラーを発注前に止めません", orders, err)
	}
}

// TestLegacyRulesDirectionRejectsNonEnum は方向式で-1/0/1以外を指定しても建玉を作らないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestLegacyRulesDirectionRejectsNonEnum(t *testing.T) {
	p := legacyRulePortfolio(t, "TOPIX", "Night", "t04", map[string]string{"night.entry_direction": "1.5"})
	observe(t, p, observation(p, 0, 1400, 101), true)
	if p.Core.Arm.Side != 0 {
		t.Fatal("小数の方向を整数へ切り捨てました")
	}
}
