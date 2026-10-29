package config

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// MethodWindow は一商品の時間帯と手法です。空のMethodは意図的な静観です。
type MethodWindow struct {
	ID      string `toml:"id" json:"id"`
	Session string `toml:"session" json:"session"`
	Start   string `toml:"start" json:"start"`
	End     string `toml:"end" json:"end"`
	Method  string `toml:"method" json:"method"`
}

// WindowMoment は設定時計を解釈した値です。分数はJST時計又は米国開始からの差です。
type WindowMoment struct {
	Anchor  string
	Minutes int
}

// ParseWindowMoment は日本時間と米国開始相対の文字列を解釈します。
//
// 引数: value(string)はHH:MM、SESSION_START/END、US_OPEN又はUS_OPEN±HH:MMです。
// 返り値: WindowMomentは時計の種類と分数、errorは表記不正です。
func ParseWindowMoment(value string) (WindowMoment, error) {
	if value == "SESSION_START" || value == "SESSION_END" || value == "US_OPEN" {
		return WindowMoment{Anchor: value}, nil
	}
	if regexp.MustCompile(`^[0-9]{2}:[0-9]{2}$`).MatchString(value) {
		clock, err := time.Parse("15:04", value)
		if err == nil {
			return WindowMoment{Anchor: "JST", Minutes: clock.Hour()*60 + clock.Minute()}, nil
		}
	}
	if regexp.MustCompile(`^US_OPEN[+-][0-9]{2}:[0-9]{2}$`).MatchString(value) {
		hour, _ := strconv.Atoi(value[8:10])
		minute, _ := strconv.Atoi(value[11:13])
		if hour <= 12 && minute < 60 && hour*60+minute <= 720 {
			difference := hour*60 + minute
			if value[7] == '-' {
				difference = -difference
			}
			return WindowMoment{Anchor: "US_OPEN", Minutes: difference}, nil
		}
	}
	return WindowMoment{}, fmt.Errorf("時間帯の時計%qは日本時間HH:MM、SESSION_START、SESSION_END、US_OPEN又はUS_OPEN±HH:MMで指定してください", value)
}

// MethodWindows は明示枠を複製し、旧設定なら昼夜全体の2枠を返します。
//
// 引数: なし。受け手Configの選択商品と手法を使用します。
// 返り値: []MethodWindowは独立した配列です。空白時間へ旧手法を暗黙補完しません。
func (c Config) MethodWindows() []MethodWindow {
	if len(c.Windows) > 0 {
		return append([]MethodWindow(nil), c.Windows...)
	}
	return []MethodWindow{
		{ID: "Day", Session: "Day", Start: "SESSION_START", End: "SESSION_END", Method: c.MethodName("Day")},
		{ID: "Night", Session: "Night", Start: "SESSION_START", End: "SESSION_END", Method: c.MethodName("Night")},
	}
}

// ShortMethodID は公開手法名を内部IDへ変換し、判断銘柄の不一致を拒否します。
//
// 引数: reference/method(string)は判断銘柄と公開又は内部ID。
// 返り値: stringは短ID、errorは銘柄又は表記の不一致です。
func ShortMethodID(reference, method string) (string, error) { return methodShortID(reference, method) }

// CloneInstruments は時間帯配列まで独立した商品一覧を返します。
//
// 引数: values([]Instrument)は複製元。返り値: []Instrumentは元の時間帯を書き換えないコピーです。
func CloneInstruments(values []Instrument) []Instrument {
	out := append([]Instrument(nil), values...)
	for i := range out {
		if values[i].Windows != nil {
			out[i].Windows = append([]MethodWindow{}, values[i].Windows...)
		}
	}
	return out
}

// instrumentMethods は明示時間帯又は互換昼夜設定から実際に選択した手法を取り出します。
//
// 引数: item(Instrument)は商品設定。返り値: []MethodWindowは独立した選択一覧です。
func instrumentMethods(item Instrument) []MethodWindow {
	if len(item.Windows) > 0 {
		return append([]MethodWindow(nil), item.Windows...)
	}
	return []MethodWindow{{Session: "Day", Method: item.Day}, {Session: "Night", Method: item.Night}}
}

// ValidateMethodWindows は明示枠の構文・銘柄・昼夜と夏冬の重複を検査します。
//
// 引数: なし。受け手Configの時間帯・米国開始設定・選択商品を使用します。
// 返り値: errorは不正又は重複です。実立会の範囲はschedule.ResolveSlotがcontroller時計で最終検査します。
func (c Config) ValidateMethodWindows() error {
	if len(c.Windows) == 0 {
		if c.Windows != nil {
			return fmt.Errorf("windowsを明示する場合は最低1枠が必要です。全て静観する場合もmethodが空文字の静観枠を指定してください")
		}
		for _, method := range []string{c.MethodID("Day"), c.MethodID("Night")} {
			if IsWindowOnlyMethod(method) {
				return fmt.Errorf("NK225_%sは時間帯専用です。windowsで米国開始に対する範囲を指定してください", method)
			}
		}
		return nil
	}
	if len(c.Windows) > 64 {
		return fmt.Errorf("時間帯は商品ごとに64枠以内にしてください")
	}
	reference, _ := c.Selected()
	if reference == "" && c.Market != nil {
		reference = Reference(c.Market.Product)
	}
	seen := map[string]bool{}
	for _, window := range c.Windows {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`).MatchString(window.ID) || seen[window.ID] {
			return fmt.Errorf("時間帯IDは重複しない英数・_・-の1〜64文字にしてください: %q", window.ID)
		}
		seen[window.ID] = true
		if window.Session != "Day" && window.Session != "Night" {
			return fmt.Errorf("時間帯%sのsessionはDay又はNightです", window.ID)
		}
		for _, clock := range []string{window.Start, window.End} {
			moment, err := ParseWindowMoment(clock)
			if err != nil {
				return fmt.Errorf("時間帯%s: %w", window.ID, err)
			}
			if moment.Anchor == "US_OPEN" && window.Session != "Night" {
				return fmt.Errorf("時間帯%s: 米国開始相対はNightだけに指定できます", window.ID)
			}
		}
		if window.Method != "" {
			if strings.TrimSpace(window.Method) != window.Method {
				return fmt.Errorf("時間帯%sの手法IDに前後の空白を付けないでください", window.ID)
			}
			if _, err := rules.SelectProfile(reference, window.Session, window.Method); err != nil {
				return fmt.Errorf("時間帯%s: %w", window.ID, err)
			}
			method, _ := methodShortID(reference, window.Method)
			if (IsWindowOnlyMethod(method) || method == "D164_15") && c.RuleEngine.File != "" {
				return fmt.Errorf("時間帯%s: %sはrule_engine.directory又は標準式で指定してください。旧単一ファイルでは手法別の式を使用できません", window.ID, method)
			}
		}
	}
	schedule := c.Schedule
	if err := schedule.NormalizeJST(); err != nil {
		return err
	}
	clock, err := time.Parse("15:04", schedule.USOpen)
	if err != nil {
		return fmt.Errorf("米国開始の設定が不正です: %w", err)
	}
	winter := (clock.Hour()*60 + clock.Minute() + 14*60) % (24 * 60)
	for _, offset := range []int{0, -60} {
		for _, kind := range []string{"Day", "Night"} {
			if err := validateWindowOverlap(c.Windows, kind, winter+offset); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateWindowOverlap は当場日付上の相対分数に揃え、夏冬どちらでも重複を拒否します。
//
// 引数: windows([]MethodWindow)、kind(string)、usMinute(int)は当日の米国開始JST分数。
// 返り値: errorは逆転・重複です。立会の端は無限の番兵とし、API時計の再定義を避けます。
func validateWindowOverlap(windows []MethodWindow, kind string, usMinute int) error {
	type interval struct {
		id         string
		start, end int
	}
	var items []interval
	for _, window := range windows {
		if window.Session != kind {
			continue
		}
		moments := []int{}
		for _, input := range []string{window.Start, window.End} {
			m, _ := ParseWindowMoment(input)
			value := m.Minutes
			switch m.Anchor {
			case "SESSION_START":
				value = -100000
			case "SESSION_END":
				value = 100000
			case "US_OPEN":
				anchor := usMinute
				if kind == "Night" && anchor < 12*60 {
					anchor += 24 * 60
				}
				value = anchor + m.Minutes
			case "JST":
				if kind == "Night" && value < 12*60 {
					value += 24 * 60
				}
			}
			moments = append(moments, value)
		}
		if moments[0] >= moments[1] {
			return fmt.Errorf("時間帯%sの終了は開始より後にしてください（米国夏冬も検査します）", window.ID)
		}
		method, _ := methodShortID("NK225", window.Method)
		if IsWindowOnlyMethod(method) {
			anchor := usMinute
			if anchor < 12*60 {
				anchor += 24 * 60
			}
			if kind != "Night" || (method != "U165_03" && moments[1] > anchor-15) || (method == "U165_03" && moments[0] < anchor+15) {
				return fmt.Errorf("時間帯%s: %sの米国開始に対する範囲が不正です（前担当は15分前まで、後担当は15分後から。米国夏冬も検査します）", window.ID, window.Method)
			}
		}
		items = append(items, interval{window.ID, moments[0], moments[1]})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].start < items[j].start })
	for i := 1; i < len(items); i++ {
		if items[i].start < items[i-1].end {
			return fmt.Errorf("時間帯%sと%sが重複します（米国夏冬も検査します）", items[i-1].id, items[i].id)
		}
	}
	return nil
}

// IsWindowOnlyMethod は米国開始の前後へ用途を限定した手法かを返します。
//
// 引数: method(string)は内部短IDです。返り値: boolは時間帯明示を必須にする場合にtrueです。
func IsWindowOnlyMethod(method string) bool {
	return method == "P05" || method == "P164_06" || method == "U165_03"
}
