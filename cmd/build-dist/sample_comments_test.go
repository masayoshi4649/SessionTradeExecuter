package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/cli"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// TestWebSamplesKeepExplanations はWeb版の全設定と式の説明が両OSへ届くことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。実設定ではなく配布用サンプルだけを読みます。
func TestWebSamplesKeepExplanations(t *testing.T) {
	root := fixture(t)
	source, err := os.ReadFile(filepath.Join(root, "web-conf", "settings.toml.sample"))
	if err != nil {
		t.Fatal(err)
	}
	if err := buildDistribution(root, fakeBuild); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"windows-amd64", "linux-amd64"} {
		directory := filepath.Join(root, "dist", platform, bundleDirectory, "conf")
		data, err := os.ReadFile(filepath.Join(directory, "settings.toml.sample"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != string(source) {
			t.Fatal("設定例の説明や値を変更しました", platform)
		}
		if err := validateWebSampleComments(data); err != nil {
			t.Fatal(err)
		}
		for _, profile := range rules.ProfileNames() {
			ruleData, err := os.ReadFile(filepath.Join(directory, "rules", rules.FileName(profile)))
			if err != nil {
				t.Fatal(err)
			}
			if err := validateWebSampleComments(ruleData); err != nil {
				t.Fatal(profile, err)
			}
		}
	}
}

// TestCommentRefreshPreservesUserValues は説明の再配布で利用者の設定値を上書きしないことを確認します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
func TestCommentRefreshPreservesUserValues(t *testing.T) {
	root := fixture(t)
	if err := buildDistribution(root, fakeBuild); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "dist", "windows-amd64", bundleDirectory, "conf")
	path := filepath.Join(directory, "settings.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	custom := strings.Replace(string(data), `mode = "off"`, `mode = "live"`, 1)
	custom = strings.Replace(custom, "max_per_side = 10", "max_per_side = 6", 1)
	write(t, path, custom)
	updated := strings.Replace(string(data), "# Web画面のポート番号", "# 更新したWeb画面のポート番号", 1)
	write(t, filepath.Join(root, "web-conf", "settings.toml.sample"), updated)
	if err = buildDistribution(root, fakeBuild); err != nil {
		t.Fatal(err)
	}
	retained, err := os.ReadFile(path)
	if err != nil || string(retained) != custom {
		t.Fatal("既存の実設定を変更しました", err)
	}
	cfg, err := config.Load(directory)
	if err != nil || len(cfg.Instruments) != 5 || config.ModeOf(cfg.Instruments[0], true) != "live" || cfg.Instruments[1].MaxPerSide != 6 {
		t.Fatalf("利用者が設定した有効値を保持していません: %+v %v", cfg, err)
	}
	sample, err := os.ReadFile(filepath.Join(directory, "settings.toml.sample"))
	if err != nil || !strings.Contains(string(sample), "# 更新したWeb画面のポート番号") {
		t.Fatal("新しい説明をsampleへ配布していません", err)
	}
}

// TestUndocumentedKeyStopsBeforeBuild は無説明の追加キーを配布前に検出します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
func TestUndocumentedKeyStopsBeforeBuild(t *testing.T) {
	root := fixture(t)
	path := filepath.Join(root, "web-conf", "settings.toml.sample")
	source, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	lines := []string{}
	for _, line := range strings.Split(string(source), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "# 商品別モード") {
			lines = append(lines, line)
		}
	}
	write(t, path, strings.Join(lines, "\n"))
	calls := 0
	err := buildDistribution(root, func(string, string, string) error {
		calls++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "instruments.mode") || calls != 0 {
		t.Fatalf("説明欠落をビルド前に検出できません: %d %v", calls, err)
	}
}

// TestQuotedAlgorithmOverrideKeepsValueAndExamples は旧手法の引用符付き実キーと無効化例を継承します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
func TestQuotedAlgorithmOverrideKeepsValueAndExamples(t *testing.T) {
	root := fixture(t)
	sample := sampleConfiguration + "\n[algorithm]\n# 共通上書きとして有効な損失幅の説明。\n\"day.risk_floor\" = 0.8\n# 幅の意味を説明した無効化例。\n# \"night.trail_width_r\" = 0.75\n"
	write(t, filepath.Join(root, "conf", "setting.toml.sample"), sample)
	stage := t.TempDir()
	if err := copyConfiguration(filepath.Join(root, "conf"), filepath.Join(stage, "conf")); err != nil {
		t.Fatal(err)
	}
	template, err := readSampleTemplate(filepath.Join(stage, "conf"))
	if err != nil {
		t.Fatal(err)
	}
	// 旧T06/U06の継承を検査します。新配布のTG04へ上書きを混ぜる場合は別試験で拒否します。
	annotated, err := replaceSampleScalars(template, []sampleReplacement{
		{"trading", "instance_id", "nk-micro"}, {"market", "product", "NK225micro"},
		{"market", "max_per_side", 20}, {"methods", "day", "T06"}, {"methods", "night", "U06"},
	})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(stage, "legacy", "conf")
	write(t, filepath.Join(directory, "settings.toml"), string(annotated))
	write(t, filepath.Join(directory, "settings.toml.sample"), string(annotated))
	cfg, err := config.Load(directory)
	if err != nil || cfg.MethodID("Day") != "T06" || cfg.MethodID("Night") != "U06" || len(cfg.Algorithm) != 1 || cfg.Algorithm["day.risk_floor"] != .8 {
		t.Fatalf("有効な上書き値を変更しました: %+v %v", cfg.Algorithm, err)
	}
	if err = cli.ValidateAlgorithms(cfg); err != nil {
		t.Fatalf("旧手法の有効な上書きが拒否されました: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "settings.toml.sample"))
	if err != nil || !strings.Contains(string(data), "# \"night.trail_width_r\" = 0.75") ||
		!strings.Contains(string(data), "# 共通上書きとして有効な損失幅の説明。") {
		t.Fatal("上書きの説明または無効化例が失われました", err)
	}
}

// TestFixedResearchMethodsRejectOverridesBeforeBuild は固定手法への上書き混入を配布前に拒否します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
// 旧TOPIX設定から商品別TG04を生成する経路と、TG04/PG04を直接指定する経路を検査します。
func TestFixedResearchMethodsRejectOverridesBeforeBuild(t *testing.T) {
	for _, tc := range []struct{ product, day, night string }{
		{"TOPIXmini", "T06", "T04"},
		{"NK225mini", "TG04", "TG04"}, {"NK225mini", "PG04", "PG04"},
		{"NK225mini", "TG04", "U06"}, {"NK225mini", "T06", "PG04"},
	} {
		t.Run(tc.product+"_"+tc.day+"_"+tc.night, func(t *testing.T) {
			root := fixture(t)
			sample, err := replaceSampleScalars([]byte(sampleConfiguration), []sampleReplacement{
				{"market", "product", tc.product}, {"methods", "day", tc.day}, {"methods", "night", tc.night},
			})
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(root, "conf", "setting.toml.sample"), string(sample)+"\n[algorithm]\n# 旧手法専用の損失幅上書きです。固定研究手法との併用は拒否します。\n\"day.risk_floor\" = 0.8\n")
			retainedPath := filepath.Join(root, "dist", "windows-amd64", "NK225mini", "conf", "settings.toml")
			retained := "# 利用者の既存実設定。\n[trading]\ndryrun = false\n"
			write(t, retainedPath, retained)
			stage := t.TempDir()
			if err := copyConfiguration(filepath.Join(root, "conf"), filepath.Join(stage, "conf")); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(filepath.Join(stage, "conf"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = prepareSingleConfigurations(stage, cfg)
			if err == nil || (!strings.Contains(err.Error(), "TG04") && !strings.Contains(err.Error(), "PG04") && !strings.Contains(err.Error(), "D07") && !strings.Contains(err.Error(), "S01")) {
				t.Fatalf("旧単品設定への固定手法と上書き混入を拒否できません: err=%v", err)
			}
			data, readErr := os.ReadFile(retainedPath)
			if readErr != nil || string(data) != retained {
				t.Fatal("拒否した配布処理が利用者の実設定を変更しました", readErr)
			}
		})
	}
}

// TestRepositorySamplesCoverGeneratedKeys は実際に配布するサンプルの全実キーを検査します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
// 利用者の*.tomlは読まず、版管理対象の*.toml.sampleだけを一時領域へコピーします。
func TestRepositorySamplesCoverGeneratedKeys(t *testing.T) {
	stage := t.TempDir()
	if err := prepareWebConfiguration(filepath.Join("..", ".."), stage); err != nil {
		t.Fatal(err)
	}
}

// TestScalarReplacementKeepsLiteralAndInlineComment は文字列と行末説明を混同しないことを確認します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
func TestScalarReplacementKeepsLiteralAndInlineComment(t *testing.T) {
	template := []byte("[market]\n# 発注商品の説明。\nproduct = 'TOPIX#mini' # 商品の行末説明。\n# 上限の説明。\nmax_per_side = 10\n")
	value := "引用符\"と改行\nを含む#文字列"
	data, err := replaceSampleScalars(template, []sampleReplacement{{"market", "product", value}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if _, err := toml.Decode(string(data), &decoded); err != nil {
		t.Fatal(err)
	}
	market := decoded["market"].(map[string]any)
	if len(market) != 2 || market["product"] != value || market["max_per_side"] != int64(10) ||
		!strings.Contains(string(data), "# 商品の行末説明。") {
		t.Fatal("文字列の内容、別項目または行末説明が変わりました")
	}
	if _, err := replaceSampleScalars(template, []sampleReplacement{{"market", "unknown", 20}}); err == nil {
		t.Fatal("存在しない項目を差替えで追加しました")
	}
}

// TestUnknownMethodStopsBeforeBuild は未知の手法IDを含む配布元を出荷しないことを確認します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
func TestUnknownMethodStopsBeforeBuild(t *testing.T) {
	root := fixture(t)
	path := filepath.Join(root, "web-conf", "settings.toml.sample")
	source, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	changed := strings.Replace(string(source), `method = "NK225_D164_15"`, `method = "NK225_T99"`, 1)
	if changed == string(source) {
		t.Fatal("未知の手法を仕込む対象が設定例にありません")
	}
	write(t, path, changed)
	calls := 0
	err := buildDistribution(root, func(string, string, string) error {
		calls++
		return nil
	})
	if err == nil || calls != 0 || !strings.Contains(err.Error(), "T99") {
		t.Fatalf("未知の手法IDをビルド前に拒否できません: %d %v", calls, err)
	}
}
