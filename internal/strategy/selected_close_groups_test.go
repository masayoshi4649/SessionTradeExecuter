package strategy

import (
	"fmt"
	"testing"
)

// selectedTestSourceClose は共通Source写像を更新した後の人工部分返済通知を作ります。
//
// 引数: p(*Portfolio)、id/source(string)、before/quantity(int)。返り値: SelectedSourceEvent。
func selectedTestSourceClose(p *Portfolio, id, source string, before, quantity int) SelectedSourceEvent {
	l := p.Selected.SourceLots[source+"-玉"]
	l.RemainingMicro = before - quantity
	return SelectedSourceEvent{Signal: Signal{ID: id, Action: "close", Side: -l.Side, ReasonCode: 70}, Parent: l.Parent, SourceID: id, QuantityMicro: quantity, ParentRemainingMicro: l.RemainingMicro, Targets: []SelectedSourceTarget{{SourceID: source, BeforeMicro: before, QuantityMicro: quantity, RemainingMicro: before - quantity}}}
}

// TestSelectedCloseGroupProfitWaitsForAllActualPieces は黒字片だけで資格を先に渡さないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。同じ証券会社HoldIDでも実約定ごとの異なる建値を保ちます。
func TestSelectedCloseGroupProfitWaitsForAllActualPieces(t *testing.T) {
	for _, secondEntry := range []float64{1100, 1200} {
		t.Run(fmt.Sprintf("後続建値%.0f", secondEntry), func(t *testing.T) {
			p := selectedTestPortfolio(t, "P164_06", 100)
			order := selectedTestNew(t, p, 1, 50, "源")
			for _, f := range []Fill{
				{SignalID: order.ID, ExecutionID: "先玉", HoldID: "同一口座玉", Quantity: 30, Price: 1000, Time: 1101},
				{SignalID: order.ID, ExecutionID: "後玉", HoldID: "同一口座玉", Quantity: 20, Price: secondEntry, Time: 1102},
			} {
				if err := p.OnFill(f); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.OnOrderDone(order.ID); err != nil {
				t.Fatal(err)
			}
			event := selectedTestSourceClose(p, "元部分", "源", 50, 20)
			if err := p.queueSelectedSourceClose(event); err != nil {
				t.Fatal(err)
			}
			orders, err := p.flushSelectedGoals(p05TestObservation(1, 1103, 1050, 1060))
			if err != nil || len(orders) != 2 || orders[0].Quantity != 12 || orders[1].Quantity != 8 {
				t.Fatal("元2/5の所有別数量", orders, err)
			}
			groupID := p.Selected.Intents[orders[0].ID].CloseGroupID
			group := p.Selected.CloseGroups[groupID]
			if group.Expected != 20 {
				t.Fatal("返済群の期待数量", group)
			}
			// ----------------------------------------
			// 黒字側を半約定して取消し、同じ群の残量だけ再送します。
			if err := p.OnFill(Fill{SignalID: orders[0].ID, ExecutionID: "半返済", Quantity: 6, Price: 1050, Time: 1104, Closed: []LotTarget{{ID: "同一口座玉", Quantity: 6}}}); err != nil {
				t.Fatal(err)
			}
			if group.Filled != 6 || group.Qualified || len(p.Selected.Night.Lots) != 0 {
				t.Fatal("黒字片で早期資格", group)
			}
			if err := p.OnOrderDone(orders[0].ID); err != nil {
				t.Fatal(err)
			}
			retry, err := p.flushSelectedGoals(p05TestObservation(2, 1105, 1050, 1060))
			if err != nil || len(retry) != 1 || retry[0].Quantity != 6 || p.Selected.Intents[retry[0].ID].CloseGroupID != groupID {
				t.Fatal("取消後の群残量再送", retry, err)
			}
			if err := p.validateSelectedCloseGroups(); err != nil {
				t.Fatal("取消・再送中の返済群保存照合", err)
			}
			if err := p.OnFill(Fill{SignalID: retry[0].ID, ExecutionID: "黒字側完了", Quantity: 6, Price: 1050, Time: 1106, Closed: []LotTarget{{ID: "同一口座玉", Quantity: 6}}}); err != nil {
				t.Fatal(err)
			}
			if err := p.OnOrderDone(retry[0].ID); err != nil {
				t.Fatal(err)
			}
			if group.Filled != 12 || group.Qualified || len(p.Selected.Night.Lots) != 0 {
				t.Fatal("群の一方だけで資格", group)
			}
			if err := p.OnFill(Fill{SignalID: orders[1].ID, ExecutionID: "群完了", Quantity: 8, Price: 1050, Time: 1107, Closed: []LotTarget{{ID: "同一口座玉", Quantity: 8}}}); err != nil {
				t.Fatal(err)
			}
			if err := p.OnOrderDone(orders[1].ID); err != nil {
				t.Fatal(err)
			}
			wantGross := 12*50*100 + 8*(1050-secondEntry)*100
			if group.Filled != 20 || group.Gross != wantGross || group.Qualified != (wantGross > 0) {
				t.Fatal("群完了の粗益", group)
			}
			if wantGross > 0 {
				if !p.Selected.Night.Lots["先玉"].ResidualEligible || !p.Selected.Night.Lots["後玉"].ResidualEligible {
					t.Fatal("群完了時の残玉資格")
				}
			} else if len(p.Selected.Night.Lots) != 0 {
				t.Fatal("赤字合計の群へ資格")
			}
			if p.selectedLot("先玉").Quantity != 18 || p.selectedLot("後玉").Quantity != 12 {
				t.Fatal("同HoldIDを混ぜた返済")
			}
			if out, err := p.flushSelectedGoals(p05TestObservation(3, 1108, 1050, 1060)); err != nil || len(out) != 0 {
				t.Fatal("完済返済群の二重送信", out, err)
			}
			if err := p.validateSelectedCloseGroups(); err != nil {
				t.Fatal("完了返済群の保存照合", err)
			}
			group.Gross++
			if err := p.validateSelectedCloseGroups(); err == nil {
				t.Fatal("返済群の粗益破損を受理しました")
			}
			group.Gross--
		})
	}
}

// TestSelectedCloseGroupsAccumulateWhilePending は実返済遅延中の別Source返済を二重予約せず積みます。
//
// 引数: t(*testing.T)。返り値: なし。現実残量比例と既予約控除を通知ごとに適用します。
func TestSelectedCloseGroupsAccumulateWhilePending(t *testing.T) {
	p := selectedTestPortfolio(t, "P164_06", 100)
	order := selectedTestNew(t, p, 1, 50, "源")
	if err := p.OnFill(Fill{SignalID: order.ID, ExecutionID: "実玉", HoldID: "口座玉", Quantity: 50, Price: 1000, Time: 1101}); err != nil {
		t.Fatal(err)
	}
	if err := p.OnOrderDone(order.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.queueSelectedSourceClose(selectedTestSourceClose(p, "部分1", "源", 50, 20)); err != nil {
		t.Fatal(err)
	}
	first, err := p.flushSelectedGoals(p05TestObservation(1, 1102, 1010, 1020))
	if err != nil || len(first) != 1 || first[0].Quantity != 20 {
		t.Fatal(first, err)
	}
	if err := p.queueSelectedSourceClose(selectedTestSourceClose(p, "部分2", "源", 30, 20)); err != nil {
		t.Fatal(err)
	}
	second, err := p.flushSelectedGoals(p05TestObservation(2, 1103, 1010, 1020))
	if err != nil || len(second) != 1 || second[0].Quantity != 30 || p.selectedLot("実玉").Goal != 50 {
		t.Fatal("既予約に別通知分を加算", second, err)
	}
	if err := p.queueSelectedSourceClose(selectedTestSourceClose(p, "最終", "源", 10, 10)); err != nil {
		t.Fatal(err)
	}
	if orders, err := p.flushSelectedGoals(p05TestObservation(3, 1104, 1010, 1020)); err != nil || len(orders) != 0 {
		t.Fatal("予約済み全量を重複", orders, err)
	}
	for i, s := range []Signal{first[0], second[0]} {
		if err := p.OnFill(Fill{SignalID: s.ID, ExecutionID: fmt.Sprintf("返済%d", i), Quantity: s.Quantity, Price: 1010, Time: 1105 + float64(i), Closed: []LotTarget{{ID: "口座玉", Quantity: s.Quantity}}}); err != nil {
			t.Fatal(err)
		}
		if err := p.OnOrderDone(s.ID); err != nil {
			t.Fatal(err)
		}
	}
	if p.selectedLot("実玉").Quantity != 0 || p.selectedLot("実玉").Goal != 0 || len(p.selectedSnapshot().ReservedClose) != 0 {
		t.Fatal("予約又は希望が残りました")
	}
	if err := p.validateSelectedCloseGroups(); err != nil {
		t.Fatal("複数返済群の保存照合", err)
	}
}

// TestSelectedNightSamePassProtectsAllOwnedLots は同じ判断群の先行独自返済で後続玉を遮断しません。
//
// 引数: t(*testing.T)。返り値: なし。元返済予約による遮断とは区別します。
func TestSelectedNightSamePassProtectsAllOwnedLots(t *testing.T) {
	p := selectedTestPortfolio(t, "P164_06", 100)
	s := selectedTestNew(t, p, 1, 20, "源")
	for i := 0; i < 2; i++ {
		if err := p.OnFill(Fill{SignalID: s.ID, ExecutionID: fmt.Sprintf("分割%d", i), HoldID: "同じ口座玉", Quantity: 10, Price: 1000, Time: 1101}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.OnOrderDone(s.ID); err != nil {
		t.Fatal(err)
	}
	o := p05TestObservation(1, 1102, 1030, 1040)
	_, err := p.Selected.Night.sourceNewSelectedNight("core/源", "源", 1, 20, o, p.Rules)
	if err != nil {
		t.Fatal(err)
	}
	o.Selected.NightAvailable = true
	o.Selected.NightTime = o.Time
	o.Selected.NightIndex = o.Index
	if out, err := p.ObserveWithFreshness(o, true, true); err != nil || len(out) != 0 {
		t.Fatal(out, err)
	}
	o = p05TestObservation(2, 1103, 1010, 1020)
	o.Selected.NightAvailable = true
	o.Selected.NightTime = o.Time
	o.Selected.NightIndex = o.Index
	out, err := p.ObserveWithFreshness(o, true, true)
	if err != nil || len(out) != 2 || out[0].Quantity+out[1].Quantity != 20 {
		t.Fatal("同じ判断群で後続実玉を保護しない", out, err)
	}
}
