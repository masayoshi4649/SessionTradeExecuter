package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// TestRuleFileAndSignature は外部式の変更が設定指紋へ反映され、読込後は固定されることを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestRuleFileAndSignature(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "d07.toml")
	if err := os.WriteFile(path, rules.Sample(), 0600); err != nil {
		t.Fatal(err)
	}
	c := Defaults()
	c.Root = directory
	c.RuleEngine.File = "d07.toml"
	if err := c.loadRules(); err != nil {
		t.Fatal(err)
	}
	before := c.Signature()
	expressions := c.RuleExpressions()
	expressions["partial.floor"] = "0.75 * best"
	set, err := rules.Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}
	changed := c
	changed.RuleSet = set
	if changed.Signature() == before || changed.RuleHash() == c.RuleHash() {
		t.Fatal("条件変更の指紋を識別できません")
	}
	if err := os.WriteFile(path, []byte("壊れたTOML"), 0600); err != nil {
		t.Fatal(err)
	}
	if c.Signature() != before {
		t.Fatal("読込後に式が変更されました")
	}
	if err := changed.loadRules(); err == nil {
		t.Fatal("不正な条件ファイルを受理しました")
	}
}

// TestLegacyD07RecordsDefaultRules は外部指定のない旧D07設定にも標準式の監査指紋を付けます。
//
// 引数: t(*testing.T)。返り値: なし。
func TestLegacyD07RecordsDefaultRules(t *testing.T) {
	c := Defaults()
	c.Methods = Methods{Day: "D07", Night: "D07"}
	if err := c.loadRules(); err != nil {
		t.Fatal(err)
	}
	if c.RuleHash() == "" || len(c.RuleExpressions()) == 0 {
		t.Fatal("標準式を使った記録が空です")
	}
}
