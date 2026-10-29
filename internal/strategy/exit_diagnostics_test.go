package strategy

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestExitDiagnosticsReadOnly は利益があっても保護開始前という状態を小さく記録することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。仮想源や実玉の状態は変更しません。
func TestExitDiagnosticsReadOnly(t *testing.T) {
	p := researchTestPortfolio(t, "TG04", 2)
	s := p.sleeve("addon")
	s.Side, s.Campaign = 1, 0
	s.Lots = []ResearchLot{{ID: "実玉1", Ticket: 0, Quantity: 1, Price: 60000, At: 100}}
	s.Source.Active.Book = ResearchBook{Base: 60000, Side: 1, Risk: 300, Best: 300, Floor: -300, Held: [6]int{1}}
	before, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	d := p.ExitDiagnostics(researchTestObservation(10, 110, 60300))
	if len(d) != 2 || d[1]["actual_quantity"] != 1 || d[1]["reference_unrealized_yen"] != 30000. {
		t.Fatal(d)
	}
	a := d[1]["active_exit"].(map[string]any)
	// 開始条件は編集可能な真偽式なので、固定幅ではなく適用ルールIDを記録します。
	if a["profit_protection_active"] != false || a["trail_activation_rule"] != "research.active.trail_active" || a["trail_width_points"] != 225. {
		t.Fatal(a)
	}
	if _, fixed := a["trail_start_points"]; fixed {
		t.Fatal("可変式に固定の開始幅を表示しました")
	}
	if _, err = json.Marshal(d); err != nil {
		t.Fatal(err)
	}
	after, err := p.Dump()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("診断で売買状態が変化しました", err)
	}
	missing := p.ExitDiagnostics(researchTestObservation(11, 111, 0))
	if _, exists := missing[1]["reference_unrealized_yen"]; exists {
		t.Fatal("未知価格を含み損益にしました")
	}
}
