package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// TestRuleDirectorySelection は手法ファイルの選択・独立性・読み込み後の固定を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestRuleDirectorySelection(t *testing.T) {
	directory := t.TempDir()
	for _, profile := range rules.ProfileNames() {
		if err := os.WriteFile(filepath.Join(directory, rules.FileName(profile)), rules.Content(profile), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := Defaults()
	c.Root = directory
	c.RuleEngine.Directory = "."
	if err := c.loadRules(); err != nil {
		t.Fatal(err)
	}
	if len(c.RuleDocuments()) != len(rules.ProfileNames()) {
		t.Fatal("全手法がありません")
	}
	for _, row := range [][4]string{{"NK225", "D07", "PG04", "d07"}, {"NK225", "D07", "P05", "d07"}, {"TOPIX", "T06", "T04", "t06_topix"}, {"NK225", "T06", "U06", "t06_nk225"}} {
		c.Methods = Methods{Day: row[1], Night: row[2]}
		if !reflect.DeepEqual(c.RuleExpressionsFor(row[0], "Day"), c.RuleSets[row[3]].Expressions()) {
			t.Fatal("銘柄別の式を選べません", row)
		}
		profile, _ := rules.SelectProfile(row[0], "Night", row[2])
		if !reflect.DeepEqual(c.RuleExpressionsFor(row[0], "Night"), c.RuleSets[profile].Expressions()) {
			t.Fatal("夜間の式を選べません", row)
		}
	}
	hash := c.RuleHash()
	if err := os.WriteFile(filepath.Join(directory, "NK225_TG04.toml"), []byte("壊れた内容"), 0600); err != nil {
		t.Fatal(err)
	}
	if c.RuleHash() != hash {
		t.Fatal("読込後に条件が変わりました")
	}
	if err := c.loadRules(); err == nil {
		t.Fatal("不正な実ルールを受理しました")
	}
}
