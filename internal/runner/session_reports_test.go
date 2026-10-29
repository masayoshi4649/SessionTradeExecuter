package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/schedule"
)

// TestDecisionMethodPreservesOldSlot は切替返済を次の手法へ誤帰属させないことを検査します。
//
// 引数: t(*testing.T)は人工試験です。
// 返り値: なし。待ち列の保存後に現在の枠が変わっても元のIDを維持します。
func TestDecisionMethodPreservesOldSlot(t *testing.T) {
	a, m, _, _ := integrationApp(t, "NK225", true)
	m.Saved.ActiveSlot = &schedule.Slot{Enabled: true, Method: "P164_06"}
	m.Saved.SlotSwitch = &slotTransition{Target: schedule.Slot{Enabled: true, Method: "U165_03"}}
	method := a.decisionMethod(m)
	if method != "NK225_P164_06" {
		t.Fatal("返済を元の枠へ帰属できません", method)
	}
	raw, err := json.Marshal(map[string]string{"method_id": method, "rules_profile": "旧形式"})
	if err != nil {
		t.Fatal(err)
	}
	m.Saved.ActiveSlot.Method = "U165_03"
	if queuedMethod(raw) != "NK225_P164_06" || a.decisionMethod(m) != "NK225_U165_03" {
		t.Fatal("保存済みの判断が現在の手法に変わりました")
	}
	if queuedMethod(json.RawMessage(`{"rules_profile":"NK225_D07"}`)) != "NK225_D07" || queuedMethod(json.RawMessage(`{}`)) != "" {
		t.Fatal("旧判断のIDを現設定から捏造しています")
	}
	if queuedMethod(json.RawMessage(`{"rules_profile":"d07"}`)) != "NK225_D07" {
		t.Fatal("旧版の内部名を公開手法IDへ変換できません")
	}
}

// TestSessionReportWaitsForActualSessionEnd は早期返済完了時に終了CSVを作らないことを検査します。
//
// 引数: t(*testing.T)は人工試験です。
// 返り値: なし。終了時計に届いたときだけ登録済みの場が一行になります。
func TestSessionReportWaitsForActualSessionEnd(t *testing.T) {
	a, m, _, _ := integrationApp(t, "TOPIX", true)
	p := nextSessionFixture(t, a, m, "Day")
	if err := a.activate(p); err != nil {
		t.Fatal(err)
	}
	directSessionOrder(t, a, m, p.Plan.Start.Add(time.Hour), "new", "report-new")
	if err := m.Manager.Checkpoint(json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	directSessionOrder(t, a, m, p.Plan.Start.Add(2*time.Hour), "close", "report-close")
	if err := m.Manager.Checkpoint(json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	a.finalizeSessionReports(p.Plan.ForceClose)
	root := a.cfg.Path(a.cfg.Runtime.LogDirectory)
	if _, err := os.Stat(filepath.Join(root, "sessionPL.csv")); !os.IsNotExist(err) {
		t.Fatal("立会終了前にレポートを確定しました", err)
	}
	a.finalizeSessionReports(p.Plan.End)
	rows, err := journal.ReadSessionPL(root)
	if err != nil || len(rows) != 1 {
		t.Fatal("立会終了でレポートを作成できません", rows, err)
	}
	if rows[0].Profit != 2000 || rows[0].Loss != 0 || rows[0].Net != 2000 || rows[0].EntryQty != 1 {
		t.Fatal("確定約定からの集計が一致しません", rows)
	}
	a.finalizeSessionReports(p.Plan.End.Add(time.Minute))
	rows, err = journal.ReadSessionPL(root)
	if err != nil || len(rows) != 1 {
		t.Fatal("同じセッションの終了が重複しています", rows, err)
	}
}
