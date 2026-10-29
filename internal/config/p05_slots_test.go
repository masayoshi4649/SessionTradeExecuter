package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// TestP05LegacyWindowMigrationKeepsExplicitChoices は標準の旧夜間だけを移行し独自条件を保持します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。
func TestP05LegacyWindowMigrationKeepsExplicitChoices(t *testing.T) {
	custom := []MethodWindow{{ID: "custom", Session: "Night", Start: "SESSION_START", End: "SESSION_END", Method: "NK225_D07"}}
	for _, tc := range []struct {
		name, product, night, ruleFile string
		windows                        []MethodWindow
		algorithm                      map[string]float64
		migrated                       bool
	}{
		{name: "標準", product: "NK225mini", night: "NK225_D07", migrated: true},
		{name: "短ID", product: "NK225micro", night: "D07", migrated: true},
		{name: "明示枠", product: "NK225mini", night: "D07", windows: custom},
		{name: "空配列不正を隠さない", product: "NK225mini", night: "D07", windows: []MethodWindow{}},
		{name: "他手法", product: "NK225mini", night: "NK225_U06"},
		{name: "別銘柄", product: "TOPIXmini", night: "TOPIX_T04"},
		{name: "旧一枚式", product: "NK225mini", night: "D07", ruleFile: "custom.toml"},
		{name: "独自係数", product: "NK225mini", night: "D07", algorithm: map[string]float64{"custom": 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Web = &Web{Host: "0.0.0.0", Port: 8090}
			cfg.RuleEngine.File, cfg.Algorithm = tc.ruleFile, tc.algorithm
			cfg.Instruments = []Instrument{{Product: tc.product, Mode: "live", MaxPerSide: 11, Day: "NK225_TG04", Night: tc.night, Windows: tc.windows}}
			original := CloneInstruments(cfg.Instruments)
			cfg.migrateLegacyD07Windows(false)
			got := cfg.Instruments[0]
			if got.Mode != original[0].Mode || got.MaxPerSide != 11 || got.Day != original[0].Day || got.Night != tc.night {
				t.Fatal("枚数・モード・昼夜IDを変更しました", got)
			}
			if tc.migrated {
				if !reflect.DeepEqual(got.Windows, StandardNK225Windows(original[0].Day)) || original[0].Windows != nil {
					t.Fatal("標準枠が異なるか元設定を変更しました", got)
				}
			} else if !reflect.DeepEqual(got, original[0]) {
				t.Fatal("利用者指定又は不正な空配列を読み替えました", got)
			}
		})
	}
}

// TestP05OldWebOverlayMigrationRoundTrip は旧画面保存を優先してから移行し再保存を同値に保ちます。
//
// 引数: t(*testing.T)は試験です。返り値: なし。起動時の元TOML書換えも検査します。
func TestP05OldWebOverlayMigrationRoundTrip(t *testing.T) {
	body := strings.ReplaceAll(webConfiguration(), `night="TG04"`, `night="D07"`)
	dir := writeConfiguration(t, body)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	legacy := webSettingsOf(cfg)
	legacy.Instruments[0].Windows = []MethodWindow{{ID: "custom", Session: "Night", Start: "18:00", End: "21:00", Method: "NK225_D07"}}
	legacy.Instruments[1].Windows, legacy.Instruments[2].Windows = nil, nil
	legacy.Instruments[1].Mode, legacy.Instruments[1].MaxPerSide = "live", 11
	legacy.Instruments[2].Night = "NK225_U06"
	encoded, err := encodeWebSettings(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, webOverlayName)
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Instruments[0].Windows, legacy.Instruments[0].Windows) || loaded.Instruments[2].Windows != nil {
		t.Fatal("明示枠又は別手法を変更しました")
	}
	item := loaded.Instruments[1]
	if item.Mode != "live" || item.MaxPerSide != 11 || !reflect.DeepEqual(item.Windows, StandardNK225Windows("NK225_TG04")) {
		t.Fatal("旧画面保存の枚数・モード・日中手法・標準夜間が不正です", item)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(encoded) {
		t.Fatal("読込だけで旧画面保存を書き換えました", err)
	}
	if after, err := os.ReadFile(filepath.Join(dir, "test.toml")); err != nil || string(after) != body {
		t.Fatal("読込だけで元設定を書き換えました", err)
	}
	settings, err := ReadWebSettings(loaded)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SaveWebSettings(loaded, settings, settings.Revision)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(webSettingsOf(saved), webSettingsOf(reloaded)) {
		t.Fatal("移行済み時間帯を再保存・再起動すると変化しました")
	}
}

// TestP05LegacySelectionSaveMatchesReload は旧形式から標準夜間を選んだ保存直後と再起動を一致させます。
//
// 引数: t(*testing.T)は試験です。返り値: なし。
func TestP05LegacySelectionSaveMatchesReload(t *testing.T) {
	dir := writeConfiguration(t, webConfiguration())
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := ReadWebSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	settings.Instruments[1].Night = "NK225_D07"
	saved, err := SaveWebSettings(cfg, settings, settings.Revision)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil || !reflect.DeepEqual(webSettingsOf(saved), webSettingsOf(loaded)) {
		t.Fatal("旧形式選択の保存直後と再起動で条件が変わりました", err)
	}
	if !reflect.DeepEqual(saved.Instruments[1].Windows, StandardNK225Windows("NK225_TG04")) || cfg.Instruments[1].Windows != nil {
		t.Fatal("保存先だけに標準時間帯を設定できませんでした")
	}
}

// TestP05LegacyWholeSessionWindowsDetection は旧標準2枠と独自の時間分割を区別します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。枠IDと配列順には依存しません。
func TestP05LegacyWholeSessionWindowsDetection(t *testing.T) {
	base := []MethodWindow{
		{ID: "user-night-79", Session: "Night", Start: "SESSION_START", End: "SESSION_END", Method: "NK225_D07"},
		{ID: "user-day-82", Session: "Day", Start: "SESSION_START", End: "SESSION_END", Method: "NK225_TG04"},
	}
	for _, tc := range []struct {
		name string
		edit func([]MethodWindow) []MethodWindow
		want bool
	}{
		{"旧画面の2枠", func(w []MethodWindow) []MethodWindow { return w }, true},
		{"短IDも同一", func(w []MethodWindow) []MethodWindow { w[0].Method, w[1].Method = "D07", "TG04"; return w }, true},
		{"日中の選択が異なる", func(w []MethodWindow) []MethodWindow { w[1].Method = "NK225_D07"; return w }, false},
		{"夜間別手法", func(w []MethodWindow) []MethodWindow { w[0].Method = "NK225_PG04"; return w }, false},
		{"独自の開始", func(w []MethodWindow) []MethodWindow { w[0].Start = "18:00"; return w }, false},
		{"独自の終了", func(w []MethodWindow) []MethodWindow { w[0].End = "03:00"; return w }, false},
		{"夜間のみ1枠", func(w []MethodWindow) []MethodWindow { return w[:1] }, false},
		{"三つに分割", func(w []MethodWindow) []MethodWindow { return append(w, MethodWindow{ID: "other", Session: "Night"}) }, false},
		{"重複区分", func(w []MethodWindow) []MethodWindow { w[1].Session = "Night"; return w }, false},
		{"重複IDを隠さない", func(w []MethodWindow) []MethodWindow { w[1].ID = w[0].ID; return w }, false},
		{"別銘柄を隠さない", func(w []MethodWindow) []MethodWindow { w[0].Method = "TOPIX_D07"; return w }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			windows := tc.edit(append([]MethodWindow(nil), base...))
			if got := isLegacyD07FullSessionWindows(windows, "TG04"); got != tc.want {
				t.Fatal("旧標準の識別が不正です", got, windows)
			}
		})
	}
}

// TestP05OldWholeSessionWindowsLoadMigration は旧画面保存の2枠だけをLoad時に移行します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。RESTで明示保存した夜間1枠D07は再起動後も維持します。
func TestP05OldWholeSessionWindowsLoadMigration(t *testing.T) {
	dir := writeConfiguration(t, webConfiguration())
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := ReadWebSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldWindows := []MethodWindow{
		{ID: "original-night", Session: "Night", Start: "SESSION_START", End: "SESSION_END", Method: "NK225_D07"},
		{ID: "original-day", Session: "Day", Start: "SESSION_START", End: "SESSION_END", Method: "NK225_TG04"},
	}
	settings.Instruments[1].Windows = oldWindows
	settings.Instruments[1].Mode, settings.Instruments[1].MaxPerSide = "live", 11
	saved, err := SaveWebSettings(cfg, settings, settings.Revision)
	if err != nil || !reflect.DeepEqual(saved.Instruments[1].Windows, oldWindows) {
		t.Fatal("明示REST保存中に2枠を変更しました", err)
	}
	path := filepath.Join(dir, webOverlayName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Instruments[1]
	if !reflect.DeepEqual(got.Windows, StandardNK225Windows("NK225_TG04")) || got.Mode != "live" || got.MaxPerSide != 11 {
		t.Fatal("旧2枠からの標準化又はモード・枚数保持が不正です", got)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("Loadだけで旧設定を書き換えました", err)
	}
	settings, err = ReadWebSettings(loaded)
	if err != nil {
		t.Fatal(err)
	}
	settings.Instruments[1].Windows = oldWindows[:1]
	saved, err = SaveWebSettings(loaded, settings, settings.Revision)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(dir)
	if err != nil || !reflect.DeepEqual(reloaded.Instruments[1].Windows, oldWindows[:1]) || !reflect.DeepEqual(webSettingsOf(saved), webSettingsOf(reloaded)) {
		t.Fatal("明示した夜間1枠D07が再起動後P05へ変化しました", err)
	}
}

// TestP05WindowsRejectsLateAndLegacyAssignments は前半専用の時間制限を夏冬とも検査します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。
func TestP05WindowsRejectsLateAndLegacyAssignments(t *testing.T) {
	cfg := windowTestConfig(t)
	cfg.Windows = StandardNK225Windows("NK225_D07")
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.RuleExpressionsForMethod("NK225", "Night", "NK225_P05")) == 0 || cfg.RuleDocuments()["NK225_P05"] == nil {
		t.Fatal("標準P05のルールと指紋が監査対象にありません")
	}
	for _, end := range []string{"US_OPEN-00:14", "US_OPEN+00:15", "SESSION_END", "22:16"} {
		cfg.Windows = []MethodWindow{{ID: "pre", Session: "Night", Start: "SESSION_START", End: end, Method: "NK225_P05"}}
		if err := cfg.ValidateMethodWindows(); err == nil {
			t.Fatal("米国前の期限を越えたP05を許可しました", end)
		}
	}
	for _, end := range []string{"US_OPEN-00:15", "US_OPEN-00:30", "21:00"} {
		cfg.Windows = []MethodWindow{{ID: "pre", Session: "Night", Start: "SESSION_START", End: end, Method: "NK225_P05"}}
		if err := cfg.ValidateMethodWindows(); err != nil {
			t.Fatal("前半の正しいP05枠を拒否しました", end, err)
		}
	}
	cfg.Windows = nil
	cfg.Methods.Night = "NK225_P05"
	if err := cfg.ValidateMethodWindows(); err == nil {
		t.Fatal("夜間全体へP05を割り当てました")
	}
}

// TestP05CatalogRequiresNightWindow は銘柄接頭辞・昼夜区分・時間帯専用の公開情報を確認します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。
func TestP05CatalogRequiresNightWindow(t *testing.T) {
	cfg := Defaults()
	cfg.Root = t.TempDir()
	cfg.RuleEngine.Directory = "rules"
	dir := filepath.Join(cfg.Root, "rules")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rules.FileName("p05")), rules.Content("p05"), 0600); err != nil {
		t.Fatal(err)
	}
	choices, err := ListRuleChoices(cfg)
	if err != nil || len(choices) != 1 {
		t.Fatal(choices, err)
	}
	got := choices[0]
	if got.Name != "NK225_P05" || got.Reference != "NK225" || !got.Supported || got.Day || !got.Night || !got.WindowOnly || got.WindowHint == "" {
		t.Fatal("P05の選択範囲が不正です", got)
	}
}
