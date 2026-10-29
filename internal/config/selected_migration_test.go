package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// TestSelectedLoadKeepsModifiedLegacyRule は旧版の暗黙時間帯も改変式があればそのまま残します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。設定ファイルと実ルールを読み込む経路で検査します。
func TestSelectedLoadKeepsModifiedLegacyRule(t *testing.T) {
	for _, changedProfile := range []string{"d07", "p05", "s01"} {
		t.Run(changedProfile, func(t *testing.T) {
			body := strings.ReplaceAll(webConfiguration(), "TG04", "D07") + "\n[rule_engine]\ndirectory='conf/rules'\n"
			dir := writeConfiguration(t, body)
			if err := os.MkdirAll(filepath.Join(dir, "rules"), 0700); err != nil {
				t.Fatal(err)
			}
			for _, profile := range rules.ProfileNames() {
				data := rules.Content(profile)
				if profile == changedProfile {
					for _, spec := range rules.SpecsFor(profile) {
						if spec.Kind == "number" {
							data = []byte(strings.Replace(string(data), spec.Expression, "("+spec.Expression+") + 0.001", 1))
							break
						}
					}
				}
				if err := os.WriteFile(filepath.Join(dir, "rules", rules.FileName(profile)), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			c, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if c.Instruments[i].Day != "D07" || c.Instruments[i].Windows != nil {
					t.Fatal("改変式で暗黙の旧時間帯を変更しました", c.Instruments[i])
				}
			}
		})
	}
}

// selectedMigrationFixture は版1の旧標準を持つ独立設定を人工的に作ります。
//
// 引数: t(*testing.T)は試験です。返り値: Configは実通信を行わない5商品設定です。
func selectedMigrationFixture(t *testing.T) Config {
	t.Helper()
	c, err := Load(writeConfiguration(t, "settings_version=2\n"+webConfiguration()))
	if err != nil {
		t.Fatal(err)
	}
	c.SettingsVersion = 1
	c.RuleSets = map[string]*rules.Set{}
	for _, profile := range rules.ProfileNames() {
		c.RuleSets[profile], err = rules.DefaultFor(profile)
		if err != nil {
			t.Fatal(err)
		}
	}
	c.RuleSet = c.RuleSets["d07"]
	for i := 0; i < 3; i++ {
		item := &c.Instruments[i]
		item.Day, item.Night = "NK225_S01", "NK225_D07"
		item.Windows = StandardNK225Windows("S01")
		item.Mode = []string{"off", "dryrun", "live"}[i]
		item.MaxPerSide = 7 + i
		item.Windows[0].ID = "user_day"
		item.Windows[0], item.Windows[3] = item.Windows[3], item.Windows[0]
	}
	c.Controller.BaseURL = "http://private.example:18080"
	return c
}

// TestSelectedMigrationPreservesModesAndRollback は旧標準だけの移行と保存後の明示選択を確認します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。数量・モード・通信先・枠IDと順序を照合します。
func TestSelectedMigrationPreservesModesAndRollback(t *testing.T) {
	c := selectedMigrationFixture(t)
	prior := CloneInstruments(c.Instruments)
	if err := c.migrateSelectedMethods(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		got, old := c.Instruments[i], prior[i]
		if got.Day != "NK225_D164_15" || got.Night != "NK225_U165_03" || got.Mode != old.Mode || got.MaxPerSide != old.MaxPerSide {
			t.Fatal(got)
		}
		for j, window := range got.Windows {
			if window.ID != old.Windows[j].ID || window.Start != old.Windows[j].Start || window.End != old.Windows[j].End {
				t.Fatal("利用者の枠を変更しました")
			}
		}
	}
	if !reflect.DeepEqual(c.Instruments[3:], prior[3:]) || c.Controller.BaseURL != "http://private.example:18080" {
		t.Fatal("TOPIX又は接続先を変更しました")
	}
	c.Instruments[0].Day, c.Instruments[0].Night = "NK225_S01", "NK225_D07"
	c.Instruments[0].Windows = StandardNK225Windows("S01")
	if err := c.migrateSelectedMethods(); err != nil || c.Instruments[0].Day != "NK225_S01" {
		t.Fatal("版2の明示旧手法を再移行しました", err)
	}
}

// TestSelectedMigrationRetainsCustomWindowsAndRules は独自境界・変更式・版1の明示D07を保持します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。新旧全6profileの改変を個別に検査します。
func TestSelectedMigrationRetainsCustomWindowsAndRules(t *testing.T) {
	for _, profile := range []string{"s01", "p05", "d07", "d164_15", "p164_06", "u165_03"} {
		t.Run(profile, func(t *testing.T) {
			c := selectedMigrationFixture(t)
			expressions := c.RuleSets[profile].Expressions()
			for _, spec := range rules.SpecsFor(profile) {
				if spec.Kind == "bool" {
					expressions[spec.ID] = "!(" + spec.Expression + ")"
					break
				}
			}
			changed, err := rules.CompileFor(profile, expressions)
			if err != nil {
				t.Fatal(err)
			}
			c.RuleSets[profile] = changed
			if profile == "d07" {
				c.RuleSet = changed
			}
			before := CloneInstruments(c.Instruments)
			if err := c.migrateSelectedMethods(); err != nil || !reflect.DeepEqual(before, c.Instruments) {
				t.Fatal("独自式の利用者を移行しました", err)
			}
		})
	}
	c := selectedMigrationFixture(t)
	c.Instruments[0].Windows[3].Start = "09:00"
	c.Instruments[1].Day = "NK225_D07"
	c.Instruments[1].Windows = StandardNK225Windows("D07")
	prior := CloneInstruments(c.Instruments)
	if err := c.migrateSelectedMethods(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prior[:2], c.Instruments[:2]) || c.Instruments[2].Day != "NK225_D164_15" {
		t.Fatal("独自設定の範囲を識別できません")
	}
}

// TestSelectedMethodsNamesAndWindows は長いID、昼夜・前後境界・短IDの同値を確認します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。夏冬で固定JSTの誤配置も拒否します。
func TestSelectedMethodsNamesAndWindows(t *testing.T) {
	for _, id := range []string{"D164_15", "P164_06", "U165_03"} {
		for _, name := range []string{id, "NK225_" + id} {
			short, err := ShortMethodID("NK225", name)
			if err != nil || short != id || NameOfMethod("NK225mini", name) != "NK225_"+id {
				t.Fatal(name, short, err)
			}
		}
	}
	c := Defaults()
	c.Market = &SingleMarket{Product: "NK225mini", MaxPerSide: 10}
	c.Windows = SelectedNK225Windows()
	if err := c.ValidateMethodWindows(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at                 int
		start, end, method string
	}{
		{0, "SESSION_START", "SESSION_END", "NK225_P164_06"},
		{1, "SESSION_START", "US_OPEN-00:14", "NK225_P164_06"},
		{3, "US_OPEN+00:14", "SESSION_END", "NK225_U165_03"},
		{3, "23:00", "SESSION_END", "NK225_U165_03"},
		{1, "SESSION_START", "US_OPEN-00:15", "NK225_D164_15"},
	} {
		c.Windows = SelectedNK225Windows()
		c.Windows[tc.at].Start, c.Windows[tc.at].End, c.Windows[tc.at].Method = tc.start, tc.end, tc.method
		if err := c.ValidateMethodWindows(); err == nil {
			t.Fatal("対象外枠を受理しました", tc)
		}
	}
	c.Windows = nil
	c.Methods = Methods{Day: "NK225_D164_15", Night: "NK225_U165_03"}
	if err := c.ValidateMethodWindows(); err == nil {
		t.Fatal("夜間全体への後半専用手法を受理しました")
	}
}
