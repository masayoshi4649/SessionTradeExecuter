package strategy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// addonRuntime145 は人工値試験用に指定した式だけを変更します。
//
// 引数: t(*testing.T)、changed(map[string]string)。返り値: *rules.Runtimeです。
func addonRuntime145(t *testing.T, changed map[string]string) *rules.Runtime {
	t.Helper()
	expressions := map[string]string{}
	for _, spec := range rules.Specs() {
		expressions[spec.ID] = spec.Expression
	}
	for id, code := range changed {
		expressions[id] = code
	}
	runtime, err := rules.NewRuntime(expressions)
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

// TestD07AddonExternalRules は人工板でU・追尾・再準備が読み込んだ式に従うことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。保存市場データの再生は行いません。
func TestD07AddonExternalRules(t *testing.T) {
	runtime := addonRuntime145(t, map[string]string{"addon.exit_unit": "90.0", "addon.trail_width": "20.0", "addon.cooldown": "45.0"})
	session := activeSession145("Day")
	s := newResearchActive145(session, 3)
	s.Rules = runtime
	o := researchTestObservation(1, 1, 1000)
	o.ATR = 100
	s.queueInitial(o, 1, 1, 144, 200, 1000, 10)
	o.Index = 2
	o.Time = 2
	s.step(o, session, 10, false)
	if s.ExitRisk != 90 {
		t.Fatal("外部の利益単位が反映されていません", s.ExitRisk)
	}
	o = researchTestObservation(3, 3, 1110)
	s.step(o, session, 10, false)
	if s.Book.Best != 100 || s.Book.Floor != 80 {
		t.Fatal("外部の追尾幅が反映されていません", s.Book.Best, s.Book.Floor)
	}
	o = researchTestObservation(4, 4, 1080)
	events := s.step(o, session, 10, false)
	if len(events) != 1 || events[0].Action != "close" {
		t.Fatal("停止到達で返済されません", events)
	}
	o.Index = 5
	o.Time = 5
	s.step(o, session, 10, false)
	if s.NextReady != 50 || runtime.Err() != nil {
		t.Fatal("外部の再準備秒数が反映されていません", s.NextReady, runtime.Err())
	}
}

// TestD07EntryExternalRulesIsolation は旧手法が外部D07入口式を参照しないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。条件だけの人工値検査です。
func TestD07EntryExternalRulesIsolation(t *testing.T) {
	runtime := addonRuntime145(t, map[string]string{"addon.entry.confirmed": "elapsed >= 60"})
	legacy := ResearchActive{Rules: runtime}
	d07 := ResearchActive{Rules: runtime, D07Rules: true}
	env := map[string]any{"elapsed": float64(30)}
	if !legacy.entryBool("addon.entry.confirmed", true, env) || d07.entryBool("addon.entry.confirmed", true, env) {
		t.Fatal("旧手法又はD07への条件適用が不正です")
	}
	data, err := json.Marshal(d07)
	if err != nil || strings.Contains(string(data), `"Rules"`) {
		t.Fatal("ルール実行器が売買状態JSONへ混入しました", err)
	}
}
