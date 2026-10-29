package config

import (
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
	"regexp"
)

// StandardNK225Windows は日中手法を保ち、米国前P05・静観・米国後D07の標準枠を作ります。
//
// 引数: day(string)は既存の日中手法IDです。
// 返り値: []MethodWindowは独立した4枠です。米国相対時計は実行日の夏時間へ追従します。
func StandardNK225Windows(day string) []MethodWindow {
	return []MethodWindow{
		{ID: "Day", Session: "Day", Start: "SESSION_START", End: "SESSION_END", Method: NameOfMethod("NK225", day)},
		{ID: "NightPreUS", Session: "Night", Start: "SESSION_START", End: "US_OPEN-00:15", Method: "NK225_P05"},
		{ID: "NightUSQuiet", Session: "Night", Start: "US_OPEN-00:15", End: "US_OPEN+00:15", Method: ""},
		{ID: "NightPostUS", Session: "Night", Start: "US_OPEN+00:15", End: "SESSION_END", Method: "NK225_D07"},
	}
}

// migrateLegacyD07Windows は標準D07夜間だけを新しい標準枠へ正規化します。
//
// 引数: includeLegacyWindows(bool)は旧画面が生成した昼夜全体の2枠も移行する指定です。Load時だけtrueです。
// 返り値: なし。独自分割・他手法・数量・モード・既存ファイルを変更しません。
// 単一外部式又は旧係数の上書きがある設定は独自条件として保持します。
func (c *Config) migrateLegacyD07Windows(includeLegacyWindows bool) {
	if c.RuleEngine.File != "" || len(c.Algorithm) > 0 {
		return
	}
	for _, profile := range []string{"d07", "p05", "s01"} {
		standard, err := rules.DefaultFor(profile)
		actual := c.RuleSets[profile]
		if profile == "d07" && actual == nil {
			actual = c.RuleSet
		}
		if err != nil || (actual != nil && actual.Hash() != standard.Hash()) {
			return
		}
	}
	if c.Web != nil {
		c.Instruments = CloneInstruments(c.Instruments)
		for i := range c.Instruments {
			item := &c.Instruments[i]
			night, err := methodShortID(Reference(item.Product), item.Night)
			implicit := item.Windows == nil && err == nil && night == "D07"
			legacyWindows := includeLegacyWindows && isLegacyD07FullSessionWindows(item.Windows, item.Day)
			if Reference(item.Product) == "NK225" && item.Day != "" && (implicit || legacyWindows) {
				item.Windows = StandardNK225Windows(item.Day)
			}
		}
		return
	}
	reference, _ := c.Selected()
	if reference == "" && c.Market != nil {
		reference = Reference(c.Market.Product)
	}
	implicit := c.Windows == nil && c.MethodID("Night") == "D07"
	legacyWindows := includeLegacyWindows && isLegacyD07FullSessionWindows(c.Windows, c.MethodID("Day"))
	if reference == "NK225" && (implicit || legacyWindows) {
		c.Windows = StandardNK225Windows(c.MethodID("Day"))
	}
}

// isLegacyD07FullSessionWindows は旧画面の昼夜全体2枠が既存の標準選択を表すか判定します。
//
// 引数: windows([]MethodWindow)は明示枠、day(string)は互換欄の既存日中手法です。
// 返り値: boolはDay/Night各1枠・立会全体・日中同一・夜間D07の場合だけtrueです。
// 枠IDの値や配列順は問いませんが、不正なID・重複を移行で隠しません。
func isLegacyD07FullSessionWindows(windows []MethodWindow, day string) bool {
	if len(windows) != 2 || windows[0].ID == windows[1].ID {
		return false
	}
	dayID, err := methodShortID("NK225", day)
	if err != nil || dayID == "" {
		return false
	}
	seen := map[string]bool{}
	for _, window := range windows {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`).MatchString(window.ID) ||
			(window.Session != "Day" && window.Session != "Night") || seen[window.Session] ||
			window.Start != "SESSION_START" || window.End != "SESSION_END" {
			return false
		}
		seen[window.Session] = true
		method, err := methodShortID("NK225", window.Method)
		if err != nil || (window.Session == "Day" && method != dayID) || (window.Session == "Night" && method != "D07") {
			return false
		}
	}
	return true
}
