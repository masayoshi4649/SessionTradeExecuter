package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// TestWebDistributionPreservesSettings はWeb版の全商品設定と再配布時の利用者データ保持を確認します。
//
// 引数: t *testing.Tは試験。返り値: なし。
func TestWebDistributionPreservesSettings(t *testing.T) {
	root := fixture(t)
	if err := buildDistribution(root, fakeBuild); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"windows-amd64", "linux-amd64"} {
		path := filepath.Join(root, "dist", platform, bundleDirectory)
		cfg, err := config.Load(filepath.Join(path, "conf"))
		if err != nil || cfg.Web == nil || cfg.Web.Port != 8090 || !cfg.Trading.DryRun {
			t.Fatal("Web版の設定が不正です", err)
		}
		workers, err := cfg.WorkerConfigs()
		if err != nil || len(workers) != 5 {
			t.Fatal("全商品の設定がありません", err)
		}
		for _, product := range cfg.Instruments {
			if config.ModeOf(product, cfg.Trading.DryRun) != "off" {
				t.Fatal("配布直後に商品がONになっています")
			}
		}
		expected := map[string]struct {
			cap              int
			day, night       string
			tick, pointValue float64
		}{
			"NK225":      {2, "D164_15", "U165_03", 10, 1000},
			"NK225mini":  {10, "D164_15", "U165_03", 5, 100},
			"NK225micro": {20, "D164_15", "U165_03", 5, 10},
			"TOPIX":      {2, "T06", "T04", .5, 10000},
			"TOPIXmini":  {10, "T06", "T04", .25, 1000},
		}
		for _, worker := range workers {
			_, market := worker.Selected()
			want, ok := expected[market.Product]
			if !ok || market.MaxPerSide != want.cap || worker.MethodID("Day") != want.day || worker.MethodID("Night") != want.night || worker.Products[market.Product].Tick != want.tick || worker.Products[market.Product].PointValue != want.pointValue {
				t.Fatal("Web商品の手法・上限・規格が不正です", market)
			}
			if config.Reference(market.Product) == "NK225" && !reflect.DeepEqual(worker.Windows, config.SelectedNK225Windows()) {
				t.Fatal("日経の日中D164_15・米国前P164_06・静観・後半U165_03が配布されません", worker.Windows)
			}
			if config.Reference(market.Product) == "TOPIX" && worker.Windows != nil {
				t.Fatal("TOPIXの時間帯を変更しました", worker.Windows)
			}
			delete(expected, market.Product)
		}
		if len(expected) != 0 {
			t.Fatal("配布に不足商品があります", expected)
		}
		ruleDirectory := filepath.Join(path, "conf", "rules")
		entries, err := os.ReadDir(ruleDirectory)
		if err != nil || len(entries) != len(rules.ProfileNames()) {
			t.Fatal("全手法の実ルールだけが必要です", entries, err)
		}
		for _, profile := range rules.ProfileNames() {
			rulePath := filepath.Join(ruleDirectory, rules.FileName(profile))
			data, err := os.ReadFile(rulePath)
			if err != nil || string(data) != string(rules.Content(profile)) {
				t.Fatal("初回の標準式が不正です", profile, err)
			}
			if _, err := rules.LoadFor(rulePath, profile); err != nil {
				t.Fatal(profile, err)
			}
			write(t, rulePath, "利用者が変更した条件式:"+profile)
		}
		if cfg.RuleHash() == "" {
			t.Fatal("配布した条件の指紋がありません")
		}
		readme, err := os.ReadFile(filepath.Join(path, "README.md"))
		if err != nil || string(readme) != string(distributionWebReadme) {
			t.Fatal("Web版の説明書がありません", err)
		}
		write(t, filepath.Join(path, "conf", "settings.toml"), "利用者が変更したWeb設定")
		write(t, filepath.Join(path, "data", "dryrun", "nk225mini", "NK225mini.sqlite"), "台帳")
	}
	if err := buildDistribution(root, fakeBuild); err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"windows-amd64", "linux-amd64"} {
		path := filepath.Join(root, "dist", platform, bundleDirectory)
		for file, want := range map[string]string{"conf/settings.toml": "利用者が変更したWeb設定", "data/dryrun/nk225mini/NK225mini.sqlite": "台帳"} {
			got, err := os.ReadFile(filepath.Join(path, filepath.FromSlash(file)))
			if err != nil || string(got) != want {
				t.Fatal("再配布でWeb運転の設定または台帳を変更しました", file, err)
			}
		}
		for _, profile := range rules.ProfileNames() {
			data, err := os.ReadFile(filepath.Join(path, "conf", "rules", rules.FileName(profile)))
			if err != nil || string(data) != "利用者が変更した条件式:"+profile {
				t.Fatal("再配布で既存ルールを上書きしました", profile, err)
			}
		}
		if matches, err := filepath.Glob(filepath.Join(path, "conf", "rules", "*.sample")); err != nil || len(matches) != 0 {
			t.Fatal("ルールsampleを生成しました", matches, err)
		}
	}
}

// TestRuleDistributionAddsOnlyMissingFiles は新規手法の追加時にも既存の利用者条件を保持します。
//
// 引数: t(*testing.T)。返り値: なし。試験専用の一時ディレクトリだけを変更します。
func TestRuleDistributionAddsOnlyMissingFiles(t *testing.T) {
	root := fixture(t)
	if err := buildDistribution(root, fakeBuild); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "dist", "windows-amd64", bundleDirectory, "conf", "rules")
	write(t, filepath.Join(directory, rules.FileName("d07")), "変更済みの実D07")
	if err := os.Remove(filepath.Join(directory, rules.FileName("u06"))); err != nil {
		t.Fatal(err)
	}
	if err := buildDistribution(root, fakeBuild); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, rules.FileName("d07")))
	if err != nil || string(data) != "変更済みの実D07" {
		t.Fatal("存在するルールを上書きしました", err)
	}
	data, err = os.ReadFile(filepath.Join(directory, rules.FileName("u06")))
	if err != nil || string(data) != string(rules.Content("u06")) {
		t.Fatal("不足した標準ルールを追加できません", err)
	}
}

// TestRuleCanonicalNamePreservesLegacyContent は旧名の実条件を標準式で置き換えないことを確かめます。
//
// 引数: t(*testing.T)。返り値: なし。旧ファイルの削除は配布処理に含めません。
func TestRuleCanonicalNamePreservesLegacyContent(t *testing.T) {
	root := fixture(t)
	directory := filepath.Join(root, "dist", "windows-amd64", bundleDirectory, "conf", "rules")
	write(t, filepath.Join(directory, "d07.toml"), "利用者の旧D07条件")
	write(t, filepath.Join(directory, "u06.toml"), "利用者の旧U06条件")
	write(t, filepath.Join(directory, rules.FileName("u06")), "既に移行済みのU06条件")
	if err := buildDistribution(root, fakeBuild); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		rules.FileName("d07"): "利用者の旧D07条件",
		rules.FileName("u06"): "既に移行済みのU06条件",
		"d07.toml":            "利用者の旧D07条件",
	} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(data) != want {
			t.Fatal("旧条件又は移行済み条件を変更しました", name, err)
		}
	}
}
