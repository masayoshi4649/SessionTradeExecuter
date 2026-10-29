package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// TestS01LegacyStandardMigrationAndExplicitRollback は旧標準の一度だけの移行と利用者の再選択を確認します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。通信と実注文は行いません。
func TestS01LegacyStandardMigrationAndExplicitRollback(t *testing.T) {
	body := strings.ReplaceAll(webConfiguration(), "TG04", "D07")
	dir := writeConfiguration(t, body)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if cfg.Instruments[i].Day != "NK225_D164_15" || !reflect.DeepEqual(cfg.Instruments[i].Windows, SelectedNK225Windows()) {
			t.Fatal("日経3商品の旧標準を移行できません", cfg.Instruments[i])
		}
	}
	if cfg.Instruments[3].Day != "T06" || cfg.Instruments[3].Windows != nil || cfg.SettingsVersion != currentSettingsVersion {
		t.Fatal("TOPIX又は移行版が不正です")
	}
	before, err := os.ReadFile(filepath.Join(dir, "test.toml"))
	if err != nil || string(before) != body {
		t.Fatal("Loadが利用者の本文を書き換えました", err)
	}
	settings, err := ReadWebSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	settings.Instruments[1].Day = "NK225_D07"
	settings.Instruments[1].Windows = StandardNK225Windows("NK225_D07")
	settings.Instruments[1].Mode = "live"
	settings.Instruments[1].MaxPerSide = 11
	settings.Controller.BaseURL = "http://trader.example:18080"
	saved, err := SaveWebSettings(cfg, settings, settings.Revision)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(dir)
	if err != nil || !reflect.DeepEqual(webSettingsOf(saved), webSettingsOf(reloaded)) {
		t.Fatal("利用者が明示したD07へ再起動時にS01を再適用しました", err)
	}
	if reloaded.Instruments[1].Day != "NK225_D07" || reloaded.Instruments[1].Mode != "live" || reloaded.Instruments[1].MaxPerSide != 11 || reloaded.Controller.BaseURL != settings.Controller.BaseURL {
		t.Fatal("利用者の手法・モード・枚数・接続先が変化しました")
	}
}

// TestS01OldOverlayPreservesSettings は新配布本文より古い画面保存を優先して移行します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。設定読込だけで両ファイルを変更しません。
func TestS01OldOverlayPreservesSettings(t *testing.T) {
	dir := writeConfiguration(t, "settings_version=1\n"+webConfiguration())
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings := webSettingsOf(cfg)
	settings.SettingsVersion = 0
	settings.Controller.BaseURL = "http://private.example:19876"
	for i, mode := range []string{"off", "dryrun", "live"} {
		settings.Instruments[i].Day, settings.Instruments[i].Night = "NK225_D07", "NK225_D07"
		settings.Instruments[i].Mode = mode
		settings.Instruments[i].MaxPerSide = 7 + i
		settings.Instruments[i].Windows = StandardNK225Windows("D07")
		settings.Instruments[i].Windows[0].ID = "custom-name"
	}
	settings.Instruments[2].Windows[0].Start = "09:15"
	wantCustom := CloneInstruments(settings.Instruments)[2]
	encoded, err := encodeWebSettings(settings)
	if err != nil {
		t.Fatal(err)
	}
	// 旧版には版番号そのものが存在しない状態を再現します。
	encoded = []byte(strings.Replace(string(encoded), "settings_version = 0\n", "", 1))
	path := filepath.Join(dir, webOverlayName)
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, mode := range []string{"off", "dryrun"} {
		got := loaded.Instruments[i]
		if got.Day != "NK225_D164_15" || got.Mode != mode || got.MaxPerSide != 7+i || got.Windows[0].ID != "custom-name" {
			t.Fatal("標準枠の移行が他設定を変更しました", got)
		}
	}
	gotCustom := loaded.Instruments[2]
	gotCustom.Enabled, wantCustom.Enabled = false, false
	if !reflect.DeepEqual(gotCustom, wantCustom) || loaded.Controller.BaseURL != settings.Controller.BaseURL {
		t.Fatal("独自時間帯又は接続先を変更しました")
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(encoded) {
		t.Fatal("旧画面保存を直接書き換えました", err)
	}
}

// TestS01MigrationPreservesIndependentRules は利用者が変更したD07の意味を標準S01で置換しません。
//
// 引数: t(*testing.T)は試験です。返り値: なし。
func TestS01MigrationPreservesIndependentRules(t *testing.T) {
	base, err := Load(writeConfiguration(t, "settings_version=1\n[market]\nproduct='NK225mini'\nmax_per_side=10\n[methods]\nday='D07'\nnight='D07'\n"))
	if err != nil {
		t.Fatal(err)
	}
	base.SettingsVersion = 0
	custom := base.RuleSet.Expressions()
	// 条件名を固定せず、存在する数値式を一つ変更して独自条件を作ります。
	changed := false
	for _, spec := range rules.Specs() {
		if spec.Kind == "number" {
			custom[spec.ID] = "(" + spec.Expression + ") + 0.001"
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("数値式がありません")
	}
	set, err := rules.Compile(custom)
	if err != nil {
		t.Fatal(err)
	}
	base.RuleSet = set
	base.migrateStandardDayS01()
	if base.MethodID("Day") != "D07" || base.SettingsVersion != 1 {
		t.Fatal("独自D07をS01で置き換えました")
	}
}
