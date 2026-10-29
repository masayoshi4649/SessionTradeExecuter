package config

import (
	"regexp"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

const currentSettingsVersion = 2

// migrateStandardDayS01 は旧標準の日経日中D07だけを一度S01へ移行します。
//
// 引数: なし。受け手Configは外部ルール読込後の設定です。
// 返り値: なし。枚数・運転モード・接続先・独自時間帯・独自式を変更しません。
// 設定ファイル自体は書き換えず、次回の通常保存で移行版を永続化します。
func (c *Config) migrateStandardDayS01() {
	if c.SettingsVersion != 0 {
		return
	}
	c.SettingsVersion = 1
	if c.RuleEngine.File != "" || len(c.Algorithm) > 0 {
		return
	}
	standard, err := rules.Default()
	if err != nil || c.RuleSet == nil || c.RuleSet.Hash() != standard.Hash() {
		return
	}
	for _, profile := range []string{"p05", "s01"} {
		standard, err := rules.DefaultFor(profile)
		actual := c.RuleSets[profile]
		if err != nil || (actual != nil && actual.Hash() != standard.Hash()) {
			return
		}
	}
	if c.Web != nil {
		c.Instruments = CloneInstruments(c.Instruments)
		for i := range c.Instruments {
			item := &c.Instruments[i]
			day, err := methodShortID(Reference(item.Product), item.Day)
			if Reference(item.Product) != "NK225" || err != nil || day != "D07" || !isStandardNK225D07Windows(item.Windows) {
				continue
			}
			item.Day = "NK225_S01"
			for j := range item.Windows {
				if item.Windows[j].Session == "Day" {
					item.Windows[j].Method = "NK225_S01"
				}
			}
		}
		return
	}
	reference, _ := c.Selected()
	if reference == "NK225" && c.MethodID("Day") == "D07" && isStandardNK225D07Windows(c.Windows) {
		c.Methods.Day = "NK225_S01"
		for i := range c.Windows {
			if c.Windows[i].Session == "Day" {
				c.Windows[i].Method = "NK225_S01"
			}
		}
	}
}

// isStandardNK225D07Windows は独自の時間境界を上書きしないため標準4枠を照合します。
//
// 引数: windows([]MethodWindow)は利用者の枠です。
// 返り値: boolは標準と同じ昼夜・時計・手法で、識別名に不正や重複がない場合だけtrueです。
// 識別名と配列順序は保持し、同じ時刻範囲でも分割された独自枠は対象にしません。
func isStandardNK225D07Windows(windows []MethodWindow) bool {
	standard := StandardNK225Windows("NK225_D07")
	if len(windows) != len(standard) {
		return false
	}
	ids, matched := map[string]bool{}, map[int]bool{}
	for _, window := range windows {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`).MatchString(window.ID) || ids[window.ID] {
			return false
		}
		ids[window.ID] = true
		found := false
		for i, expected := range standard {
			if !matched[i] && window.Session == expected.Session && window.Start == expected.Start && window.End == expected.End && NameOfMethod("NK225", window.Method) == expected.Method {
				matched[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
