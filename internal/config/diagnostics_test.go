package config

import "testing"

// TestDiagnosticStartupDoesNotChangeSignature は診断操作を売買状態の指紋へ混ぜないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestDiagnosticStartupDoesNotChangeSignature(t *testing.T) {
	base, err := Load(writeConfiguration(t, minimumConfig))
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"action='diagnostics'", "action='diagnostics'\ndiagnostic_session='2026-10-26_Day'", "action='diagnostics'\ndiagnostic_session='2026-10-25_Night'"} {
		cfg, err := Load(writeConfiguration(t, minimumConfig+"\n[startup]\n"+action+"\n"))
		if err != nil || cfg.Signature() != base.Signature() {
			t.Fatal(action, err)
		}
	}
	for _, action := range []string{"action='run'\ndiagnostic_session='2026-10-26_Day'", "action='diagnostics'\ndiagnostic_session='../data'", "action='diagnostics'\ndiagnostic_session='2026-02-30_Day'", "action='diagnostics'\nduration_seconds=1"} {
		if _, err := Load(writeConfiguration(t, minimumConfig+"\n[startup]\n"+action+"\n")); err == nil {
			t.Fatal("不正な診断設定を受理", action)
		}
	}
}
