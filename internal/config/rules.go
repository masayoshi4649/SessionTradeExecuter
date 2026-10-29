package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// RuleEngine は外部条件のファイルを指定します。銘柄規格や注文処理は含めません。
type RuleEngine struct {
	File      string `toml:"file"`
	Directory string `toml:"directory"`
}

// loadRules は指定済み外部条件を起動時に一度だけ読み込みます。
//
// 引数: なし。返り値: errorは構文・型・条件不足の不整合です。
func (c *Config) loadRules() error {
	if c.RuleEngine.File != "" && c.RuleEngine.Directory != "" {
		return fmt.Errorf("rule_engineはdirectoryか旧fileの一方だけを指定してください")
	}
	if c.RuleEngine.Directory != "" {
		directory := c.RuleEngine.Directory
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(c.Root, directory)
		}
		sets := map[string]*rules.Set{}
		for _, profile := range rules.ProfileNames() {
			set, err := rules.LoadFor(filepath.Join(directory, rules.FileName(profile)), profile)
			if err != nil {
				return fmt.Errorf("ルール%sの読込: %w", profile, err)
			}
			sets[profile] = set
		}
		c.RuleSets, c.RuleSet = sets, sets["d07"]
		return nil
	}
	if c.RuleEngine.File == "" {
		return c.bindDefaultRules()
	}
	path := c.RuleEngine.File
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.Root, path)
	}
	set, err := rules.Load(path)
	if err != nil {
		return fmt.Errorf("売買ルール %s: %w", path, err)
	}
	c.RuleSet = set
	return nil
}

// RuleHash は起動時に固定した条件の指紋を返します。
//
// 引数: なし。返り値: stringはSHA256、D07も外部指定もない場合は空文字です。
func (c Config) RuleHash() string {
	if c.RuleSets == nil {
		return c.RuleSet.Hash()
	}
	values := map[string]string{}
	for profile, set := range c.RuleSets {
		values[rules.Name(profile)] = set.Hash()
	}
	data, _ := json.Marshal(values)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// RuleExpressions は戦略へ渡す条件を複製します。
//
// 引数: なし。返り値: map[string]stringは条件、D07も外部指定もない場合はnilです。
func (c Config) RuleExpressions() map[string]string { return c.RuleSet.Expressions() }

// RuleExpressionsFor は現在の銘柄・昼夜の手法に対応する条件だけを渡します。
//
// 引数: reference/kind(string)。返り値: map[string]string、旧設定で指定なしならnilです。
func (c Config) RuleExpressionsFor(reference, kind string) map[string]string {
	return c.RuleExpressionsForMethod(reference, kind, c.MethodID(kind))
}

// RuleExpressionsForMethod は選択した時間帯の手法に対応する式を独立コピーで返します。
//
// 引数: reference/kind/method(string)は判断市場・昼夜・手法IDです。
// 返り値: map[string]stringは外部条件です。静観又は未対応ならnilです。
func (c Config) RuleExpressionsForMethod(reference, kind, method string) map[string]string {
	profile, err := rules.SelectProfile(reference, kind, method)
	if err != nil {
		return nil
	}
	if c.RuleSets != nil {
		return c.RuleSets[profile].Expressions()
	}
	if profile == "d07" {
		return c.RuleExpressions()
	}
	return nil
}

// RuleDocuments は起動時に読み込んだ各手法の条件を監査記録用に複製します。
//
// 引数: なし。返り値: map[string]map[string]stringは手法ごとの全式です。
func (c Config) RuleDocuments() map[string]map[string]string {
	out := map[string]map[string]string{}
	for profile, set := range c.RuleSets {
		out[rules.Name(profile)] = set.Expressions()
	}
	if len(out) == 0 && c.RuleSet != nil {
		out["NK225_D07"] = c.RuleSet.Expressions()
	}
	return out
}

// bindDefaultRules は外部指定がない選択手法の実行条件を監査対象にします。
//
// 引数: なし。返り値: errorは標準式の不整合です。既存の読み込み済み式は変更しません。
func (c *Config) bindDefaultRules() error {
	needed := map[string]bool{}
	add := func(reference, kind, method string) {
		profile, err := rules.SelectProfile(reference, kind, method)
		if err == nil && (profile == "d07" || profile == "s01" || profile == "p05" || profile == "d164_15" || profile == "p164_06" || profile == "u165_03") {
			needed[profile] = true
		}
	}
	reference, _ := c.Selected()
	if reference == "" && c.Market != nil {
		reference = Reference(c.Market.Product)
	}
	if reference == "" && (c.MethodID("Day") == "D07" || c.MethodID("Night") == "D07") {
		needed["d07"] = true
	}
	for _, kind := range []string{"Day", "Night"} {
		add(reference, kind, c.MethodID(kind))
	}
	for _, window := range c.Windows {
		add(reference, window.Session, window.Method)
	}
	for _, item := range c.Instruments {
		for _, part := range instrumentMethods(item) {
			add(Reference(item.Product), part.Session, part.Method)
		}
	}
	if len(needed) == 0 {
		return nil
	}
	sets := make(map[string]*rules.Set, len(c.RuleSets)+len(needed))
	for profile, set := range c.RuleSets {
		sets[profile] = set
	}
	for profile := range needed {
		if sets[profile] != nil {
			continue
		}
		if profile == "d07" && c.RuleSet != nil {
			sets[profile] = c.RuleSet
			continue
		}
		set, err := rules.DefaultFor(profile)
		if err != nil {
			return err
		}
		sets[profile] = set
	}
	// 旧D07監査窓口を残し、新手法では必ず専用profileを経由します。
	if c.RuleSet == nil {
		var err error
		c.RuleSet, err = rules.Default()
		if err != nil {
			return err
		}
	}
	sets["d07"] = c.RuleSet
	c.RuleSets = sets
	return nil
}
