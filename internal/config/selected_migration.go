package config

import "github.com/masayoshi4649/SessionTradeExecuter/internal/rules"

// SelectedNK225Windows は日中・米国前・米国後の新しい標準4枠を作ります。
//
// 引数: なし。返り値: []MethodWindowは独立した配列です。旧標準関数は旧手法の意味のまま残します。
func SelectedNK225Windows() []MethodWindow {
	windows := StandardNK225Windows("NK225_D164_15")
	windows[1].Method = "NK225_P164_06"
	windows[3].Method = "NK225_U165_03"
	return windows
}

// migrateSelectedMethods は未移行の旧標準4枠だけを新しい3手法へ一度移行します。
//
// 引数: なし。受け手Configは全ルールの読込後です。
// 返り値: errorは移行後の標準式の準備失敗です。枚数・モード・接続先・枠ID・順序・実ファイルは変更しません。
// 版1で明示したD07、独自時間帯、独自の旧式又は移行先の式はそのまま保持します。
func (c *Config) migrateSelectedMethods() error {
	if c.SettingsVersion >= currentSettingsVersion {
		return nil
	}
	c.SettingsVersion = currentSettingsVersion
	if c.RuleEngine.File != "" || len(c.Algorithm) > 0 {
		return nil
	}
	for _, profile := range []string{"s01", "p05", "d07", "d164_15", "p164_06", "u165_03"} {
		standard, err := rules.DefaultFor(profile)
		if err != nil {
			return err
		}
		actual := c.RuleSets[profile]
		if profile == "d07" && actual == nil {
			actual = c.RuleSet
		}
		// 外部フォルダでは必要式の欠落を標準扱いしません。
		if (actual == nil && c.RuleEngine.Directory != "") || (actual != nil && actual.Hash() != standard.Hash()) {
			return nil
		}
	}
	if c.Web != nil {
		c.Instruments = CloneInstruments(c.Instruments)
		for i := range c.Instruments {
			item := &c.Instruments[i]
			if Reference(item.Product) != "NK225" || NameOfMethod("NK225", item.Day) != "NK225_S01" || NameOfMethod("NK225", item.Night) != "NK225_D07" || !isPreviousStandardWindows(item.Windows) {
				continue
			}
			item.Day, item.Night = "NK225_D164_15", "NK225_U165_03"
			replaceSelectedWindows(item.Windows)
		}
	} else {
		reference, _ := c.Selected()
		if reference == "NK225" && c.MethodID("Day") == "S01" && c.MethodID("Night") == "D07" && isPreviousStandardWindows(c.Windows) {
			c.Methods = Methods{Day: "NK225_D164_15", Night: "NK225_U165_03"}
			c.Windows = append([]MethodWindow(nil), c.Windows...)
			replaceSelectedWindows(c.Windows)
		}
	}
	if c.RuleEngine.Directory == "" {
		return c.bindDefaultRules()
	}
	return nil
}

// isPreviousStandardWindows はS01/P05/D07の旧標準境界だけを照合します。
//
// 引数: windows([]MethodWindow)は現在の時間帯です。返り値: boolは境界・手法が旧標準4枠と同じ場合だけtrueです。
func isPreviousStandardWindows(windows []MethodWindow) bool {
	copy := append([]MethodWindow(nil), windows...)
	for i := range copy {
		if copy[i].Session == "Day" && NameOfMethod("NK225", copy[i].Method) == "NK225_S01" {
			copy[i].Method = "NK225_D07"
		} else if copy[i].Session == "Day" {
			return false
		}
	}
	return isStandardNK225D07Windows(copy)
}

// replaceSelectedWindows は標準と照合済みの枠の手法だけを置き換えます。
//
// 引数: windows([]MethodWindow)は呼出側が独立コピーした標準枠です。返り値: なし。
func replaceSelectedWindows(windows []MethodWindow) {
	for i := range windows {
		switch NameOfMethod("NK225", windows[i].Method) {
		case "NK225_S01":
			windows[i].Method = "NK225_D164_15"
		case "NK225_P05":
			windows[i].Method = "NK225_P164_06"
		case "NK225_D07":
			windows[i].Method = "NK225_U165_03"
		}
	}
}
