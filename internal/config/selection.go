package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Methods は選択商品の昼夜それぞれに適用する公開手法IDです。
// 手法の実在と商品・昼夜の組合せは、戦略生成時に検査します。
type Methods struct {
	Day   string `toml:"day"`
	Night string `toml:"night"`
}

// MethodID は指定した昼夜の手法IDを返し、未指定なら現行の標準手法を補います。
//
// 引数: kind string は Day または Night です。受け手 Config の選択商品を参照します。
// 返り値: string は手法IDです。未知の区分は空文字で、売買を許可するIDにはしません。
func (c Config) MethodID(kind string) string {
	reference, _ := c.Selected()
	if reference == "" && c.Market != nil {
		reference = Reference(c.Market.Product)
	}
	defaults := defaultMethods(reference)
	switch kind {
	case "Day":
		if c.Methods.Day != "" {
			id, err := methodShortID(reference, c.Methods.Day)
			if err != nil {
				return c.Methods.Day
			}
			return id
		}
		return defaults.Day
	case "Night":
		if c.Methods.Night != "" {
			id, err := methodShortID(reference, c.Methods.Night)
			if err != nil {
				return c.Methods.Night
			}
			return id
		}
		return defaults.Night
	}
	return ""
}

// methodShortID は銘柄付き表記を内部IDへ変換し、別銘柄への誤適用を拒否します。
//
// 引数: reference(string)は判断用銘柄、method(string)は入力表記。
// 返り値: stringは旧互換の短ID、errorは銘柄不一致・表記不正です。手法の実在は戦略選択時に検査します。
func methodShortID(reference, method string) (string, error) {
	if method != strings.TrimSpace(method) {
		return "", fmt.Errorf("手法IDの先頭・末尾に空白を付けないでください")
	}
	// 年次調査番号を含む短IDも公開IDと同じ意味で受け取ります。
	if method == "D164_15" || method == "P164_06" || method == "U165_03" {
		return method, nil
	}
	prefix, id, hasPrefix := strings.Cut(method, "_")
	if !hasPrefix {
		return method, nil
	}
	if prefix != reference || (prefix != "NK225" && prefix != "TOPIX") {
		return "", fmt.Errorf("手法ID %q の銘柄は判断用銘柄 %s と一致しません", method, reference)
	}
	if id == "" || (strings.Contains(id, "_") && id != "D164_15" && id != "P164_06" && id != "U165_03") {
		return "", fmt.Errorf("手法IDは %s_手法ID の形式で指定してください: %s", reference, method)
	}
	return id, nil
}

// NameOfMethod は人向け表示用に判断銘柄と手法IDを結合します。
//
// 引数: reference(string)は判断用銘柄又は発注商品、method(string)は短ID又は銘柄付きID。
// 返り値: stringはNK225_D07等の表記。不正な入力は隠さず元の表記を返します。
func NameOfMethod(reference, method string) string {
	if value := Reference(reference); value != "" {
		reference = value
	}
	if method == "" || (reference != "NK225" && reference != "TOPIX") {
		return method
	}
	id, err := methodShortID(reference, method)
	if err != nil {
		return method
	}
	return reference + "_" + id
}

// MethodName は指定した昼夜手法を銘柄付きの名前で返します。
//
// 引数: kind(string)はDay又はNight。返り値: stringは表示・ログ用の名前です。
func (c Config) MethodName(kind string) string {
	reference, _ := c.Selected()
	if reference == "" && c.Market != nil {
		reference = Reference(c.Market.Product)
	}
	return NameOfMethod(reference, c.MethodID(kind))
}

// defaultMethods は既存の運転条件に対応する標準IDを返します。
//
// 引数: reference string は判断用ラージの商品名です。
// 返り値: Methods は昼夜の標準IDです。未知商品では空値です。
func defaultMethods(reference string) Methods {
	switch reference {
	case "TOPIX":
		return Methods{Day: "T06", Night: "T04"}
	case "NK225":
		return Methods{Day: "T06", Night: "U06"}
	}
	return Methods{}
}

// standardProducts はAPIにない価格刻み・円換算倍率の標準値を毎回新しい表で返します。
//
// 引数: なし。
// 返り値: map[string]Product は5商品の規格です。明示した旧設定への追加補完には使用しません。
func standardProducts() map[string]Product {
	return map[string]Product{
		"TOPIX": {Tick: .5, PointValue: 10000}, "TOPIXmini": {Tick: .25, PointValue: 1000},
		"NK225": {Tick: 10, PointValue: 1000}, "NK225mini": {Tick: 5, PointValue: 100},
		"NK225micro": {Tick: 5, PointValue: 10},
	}
}

// ----------------------------------------

// normalizeInput は明示キーを確認し、省略した商品規格と日本時間の入力を正規化します。
//
// 引数: meta toml.MetaData は入力に実在したキーの情報です。受け手 Config を更新します。
// 返り値: error は明示した手法IDの空欄、旧時刻との二重指定または時刻書式の不正です。
func (c *Config) normalizeInput(meta toml.MetaData) error {
	if c.Web != nil {
		for _, name := range []string{"market", "markets", "methods", "windows", "algorithm"} {
			if meta.IsDefined(name) {
				return fmt.Errorf("Web版では[%s]を指定せず、instruments内で商品と手法を設定してください", name)
			}
		}
		if meta.IsDefined("trading", "instance_id") {
			return fmt.Errorf("Web版の実行IDは商品名から自動生成するためtrading.instance_idは不要です")
		}
		if !meta.IsDefined("web", "host") {
			c.Web.Host = "0.0.0.0"
		}
		if !meta.IsDefined("web", "port") {
			c.Web.Port = 8090
		}
	}
	for _, field := range []struct{ name, value string }{{"day", c.Methods.Day}, {"night", c.Methods.Night}} {
		if meta.IsDefined("methods", field.name) && strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("methods.%sを明示する場合は空欄にせず手法IDを指定してください", field.name)
		}
	}
	for _, field := range []struct{ old, current, value string }{
		{"day_extra", "day_extra_jst", c.Schedule.DayExtraJST},
		{"us_open", "us_open_jst", c.Schedule.USOpenJST},
		{"london_extra", "london_extra_jst", c.Schedule.LondonExtraJST},
	} {
		if meta.IsDefined("schedule", field.current) {
			if meta.IsDefined("schedule", field.old) {
				return fmt.Errorf("schedule.%sと%sは同時に指定できません", field.old, field.current)
			}
			if field.value == "" {
				return fmt.Errorf("schedule.%sは日本時間のHH:MMを指定してください", field.current)
			}
		}
	}
	if !meta.IsDefined("products") {
		c.Products = standardProducts()
	}
	return c.Schedule.NormalizeJST()
}

// NormalizeJST は冬の日本時間表記を既存の地域時計へ変換し、入力用の別名を消します。
//
// 引数: なし。受け手 Schedule のJST入力を正規化します。旧キーとの重複はLoadで検査します。
// 返り値: error はHH:MMの不正です。正規化後の再呼出しは値を変えません。
// 米国・英国の実際の夏時間と日付は、schedule.Buildが地域別に適用します。
func (s *Schedule) NormalizeJST() error {
	for _, field := range []struct {
		name           string
		input, target  *string
		differenceHour int
	}{
		{"day_extra_jst", &s.DayExtraJST, &s.DayExtra, 0},
		{"us_open_jst", &s.USOpenJST, &s.USOpen, 14},
		{"london_extra_jst", &s.LondonExtraJST, &s.LondonExtra, 9},
	} {
		if *field.input == "" {
			continue
		}
		parsed, err := time.Parse("15:04", *field.input)
		if err != nil || !regexp.MustCompile(`^[0-9]{2}:[0-9]{2}$`).MatchString(*field.input) {
			return fmt.Errorf("schedule.%sは日本時間のHH:MMを指定してください: %s", field.name, *field.input)
		}
		*field.target = parsed.Add(-time.Duration(field.differenceHour) * time.Hour).Format("15:04")
		*field.input = ""
	}
	return nil
}
