package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// TestProductModeOverridesLegacy はモードの混在と旧設定の互換性を確認します。
//
// 引数: t *testing.Tは試験。返り値: なし。
func TestProductModeOverridesLegacy(t *testing.T) {
	cfg, err := Load(writeConfiguration(t, strings.Replace(webConfiguration(), "dryrun=true", "dryrun=false", 1)))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Instruments[0].Mode, cfg.Instruments[0].Enabled = "off", true
	cfg.Instruments[1].Mode, cfg.Instruments[1].Enabled = "dryrun", false
	cfg.Instruments[2].Mode, cfg.Instruments[2].Enabled = "live", false
	cfg.Instruments[3].Enabled = true
	workers, err := cfg.WorkerConfigs()
	if err != nil {
		t.Fatal(err)
	}
	for i, expected := range []bool{true, true, false, false, true} {
		if workers[i].Trading.DryRun != expected {
			t.Fatalf("商品%dのモードが混線しました: %t", i, workers[i].Trading.DryRun)
		}
	}
	if ModeOf(cfg.Instruments[0], false) != "off" || ModeOf(cfg.Instruments[3], false) != "live" {
		t.Fatal("明示モードと旧形式を読み替えられません")
	}
	cfg.Instruments[0].Mode = "ON"
	if err := cfg.Validate(); err == nil {
		t.Fatal("不正なモードを実注文に読み替えました")
	}
}

// TestWebSettingsRoundTripKeepsOriginalFiles は全項目と隠れた設定を保持して原子的に永続化します。
//
// 引数: t *testing.Tは試験。返り値: なし。
func TestWebSettingsRoundTripKeepsOriginalFiles(t *testing.T) {
	body := "# 利用者の説明を残す\n" + webConfiguration() + "\n[products.NK225]\ntick=10\npoint_value=1000\n[products.TOPIX]\ntick=0.5\npoint_value=10000\n[products.NK225mini]\ntick=5\npoint_value=100\n[products.NK225micro]\ntick=5\npoint_value=10\n[products.TOPIXmini]\ntick=0.25\npoint_value=1000\n"
	dir := writeConfiguration(t, body)
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := ReadWebSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Schedule.USOpenJST != "23:30" || settings.Schedule.LondonExtraJST != "01:30" || len(settings.Revision) != 64 {
		t.Fatal("JST表示か保存版が不正です", settings)
	}
	settings.Web.Port = 18091
	settings.Controller.AccountPollSeconds = 3
	settings.Schedule.USOpenJST = "23:35"
	settings.Runtime.QuoteMaxAgeSeconds = 45
	settings.Instruments[1].Mode = "live"
	settings.Instruments[1].MaxPerSide = 11
	settings.Instruments[1].Day = "NK225_D07"
	saved, err := SaveWebSettings(cfg, settings, settings.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if saved.WebRevision == cfg.WebRevision || cfg.Web.Port == saved.Web.Port || cfg.Instruments[1].Mode != "" {
		t.Fatal("保存版が未更新、又は元cfgを変更しました")
	}
	original, err := os.ReadFile(filepath.Join(dir, "test.toml"))
	if err != nil || string(original) != body {
		t.Fatal("既存の設定・コメントを書き換えました", err)
	}
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadWebSettings(reloaded)
	want, wantErr := ReadWebSettings(saved)
	if err != nil || wantErr != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("保存後の再起動で設定が変わりました: %v/%v\n%+v\n%+v", err, wantErr, got, want)
	}
	workers, err := reloaded.WorkerConfigs()
	if err != nil || workers[1].Trading.DryRun || workers[1].MethodID("Day") != "D07" || workers[1].Markets["NK225"].MaxPerSide != 11 {
		t.Fatal("保存した商品設定が運転へ反映されません", err)
	}
	if reloaded.Products["NK225mini"].PointValue != 100 || reloaded.Startup.Action != cfg.Startup.Action {
		t.Fatal("非公開の既存設定が失われました")
	}
	overlay, err := os.ReadFile(filepath.Join(dir, webOverlayName))
	if err != nil || strings.Contains(string(overlay), "enabled =") || strings.Contains(string(overlay), "dryrun =") {
		t.Fatal("旧共通モードを新モードへ混入しました", err)
	}
	lines := strings.Split(string(overlay), "\n")
	for i, line := range lines {
		if strings.Contains(line, " = ") && !strings.HasPrefix(strings.TrimSpace(line), "#") && (i == 0 || !strings.HasPrefix(lines[i-1], "# ")) {
			t.Fatal("説明のない設定項目があります", line)
		}
	}
}

// TestSettingsConflictAndWriteFailure は外部編集・並列保存・保存不能で採用済み設定を壊さないことを確認します。
//
// 引数: t *testing.Tは試験。返り値: なし。
func TestSettingsConflictAndWriteFailure(t *testing.T) {
	dir := writeConfiguration(t, webConfiguration())
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := ReadWebSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "external.toml"), []byte("# 外部編集\n"), 0600); err != nil {
		t.Fatal(err)
	}
	still, err := ReadWebSettings(cfg)
	if err != nil || still.Revision != settings.Revision {
		t.Fatal("古い設定値へ外部の新保存版を付け替えました", err)
	}
	returned, err := SaveWebSettings(cfg, settings, settings.Revision)
	if !errors.Is(err, ErrSettingsConflict) || returned.WebRevision != cfg.WebRevision {
		t.Fatal("外部編集を上書きしました", err)
	}
	cfg, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, _ = ReadWebSettings(cfg)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := SaveWebSettings(cfg, settings, settings.Revision)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrSettingsConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("並列保存を直列化できません", success, conflict)
	}
	failedDir := writeConfiguration(t, webConfiguration())
	failedCfg, err := Load(failedDir)
	if err != nil {
		t.Fatal(err)
	}
	failSettings, _ := ReadWebSettings(failedCfg)
	if err := os.Mkdir(filepath.Join(failedDir, webOverlayName), 0700); err != nil {
		t.Fatal(err)
	}
	failSettings.Web.Port = 19000
	failed, err := SaveWebSettings(failedCfg, failSettings, failSettings.Revision)
	if err == nil || failed.Web.Port == 19000 {
		t.Fatal("保存不能なのに新設定を採用しました")
	}
}

// TestSettingsRejectsInvalidEditsAndCatalog は不正値・未実装手法・壊れたルールを選択不可にします。
//
// 引数: t *testing.Tは試験。返り値: なし。
func TestSettingsRejectsInvalidEditsAndCatalog(t *testing.T) {
	cfg, err := Load(writeConfiguration(t, webConfiguration()))
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := ReadWebSettings(cfg)
	for _, change := range []func(*WebSettings){
		func(s *WebSettings) { s.Web.Port = 0 },
		func(s *WebSettings) { s.Schedule.USOpenJST = "25:00" },
		func(s *WebSettings) { s.Instruments[0].Mode = "" },
		func(s *WebSettings) { s.Instruments[0].Day = "TOPIX_T06" },
		func(s *WebSettings) { s.Instruments[0].Day = "NK225_UNKNOWN" },
		func(s *WebSettings) { s.Instruments[0].Night = "NK225_T06" },
	} {
		copy := settings
		copy.Instruments = append([]Instrument(nil), settings.Instruments...)
		change(&copy)
		if _, err := ApplyWebSettings(cfg, copy); err == nil {
			t.Fatal("不正設定を受け入れました", copy)
		}
	}
	directory := filepath.Join(cfg.Root, "conf", "rules")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, profile := range rules.ProfileNames() {
		if err := os.WriteFile(filepath.Join(directory, rules.FileName(profile)), rules.Content(profile), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "NK225_CUSTOM.toml"), rules.Content("d07"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "NK225_TG04.toml"), rules.Content("d07"), 0600); err != nil {
		t.Fatal(err)
	}
	choices, err := ListRuleChoices(cfg)
	if err != nil || len(choices) != len(rules.ProfileNames())+1 {
		t.Fatal("実ファイルから候補を取得できません", len(choices), err)
	}
	for _, choice := range choices {
		switch choice.Name {
		case "NK225_CUSTOM", "NK225_TG04":
			if choice.Supported || choice.Reason == "" {
				t.Fatal("未知又は内容不整合のルールを選択可能にしました", choice)
			}
		case "NK225_D07":
			if !choice.Supported || !choice.Day || !choice.Night {
				t.Fatal("昼夜共通手法の選択区分が不正です", choice)
			}
		case "TOPIX_T04":
			if !choice.Supported || choice.Day || !choice.Night {
				t.Fatal("夜間専用の手法を日中へ出しました", choice)
			}
		}
	}
}

// TestOffSaveDoesNotReloadUnrelatedBrokenRule は外部の式編集で停止保存を妨げないことを確認します。
//
// 引数: t *testing.Tは試験。返り値: なし。手法変更先が壊れている場合は引き続き拒否します。
func TestOffSaveDoesNotReloadUnrelatedBrokenRule(t *testing.T) {
	dir := writeConfiguration(t, webConfiguration()+"\n[rule_engine]\ndirectory='conf/rules'\n")
	ruleDir := filepath.Join(dir, "rules")
	if err := os.MkdirAll(ruleDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, profile := range rules.ProfileNames() {
		if err := os.WriteFile(filepath.Join(ruleDir, rules.FileName(profile)), rules.Content(profile), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	originalHash := cfg.RuleHash()
	settings, err := ReadWebSettings(cfg)
	if err != nil || cfg.RuleHash() != originalHash {
		t.Fatal("設定取得で採用中のルールが変わりました", err)
	}
	if err := os.WriteFile(filepath.Join(ruleDir, rules.FileName("tg04")), []byte("不正なルール"), 0600); err != nil {
		t.Fatal(err)
	}
	settings.Instruments[1].Mode = "off"
	saved, err := SaveWebSettings(cfg, settings, settings.Revision)
	if err != nil || saved.RuleHash() != originalHash {
		t.Fatal("無関係な外部編集でOFF保存を止めるか、実行中条件を変更しました", err)
	}
	settings, _ = ReadWebSettings(saved)
	settings.Instruments[1].Day = "NK225_PG04"
	if err := os.WriteFile(filepath.Join(ruleDir, rules.FileName("pg04")), []byte("不正な変更先"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyWebSettings(saved, settings); err == nil {
		t.Fatal("壊れた変更先の手法を採用しました")
	}
}
