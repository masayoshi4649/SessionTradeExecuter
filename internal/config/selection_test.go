package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestMinimalProductsAndMethods は商品規格を省いた設定で5商品の規格と昼夜のIDを補います。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。旧設定が明示した商品表は追加補完しないことも検査します。
func TestMinimalProductsAndMethods(t *testing.T) {
	expected := map[string]Product{
		"TOPIX": {Tick: .5, PointValue: 10000}, "TOPIXmini": {Tick: .25, PointValue: 1000},
		"NK225": {Tick: 10, PointValue: 1000}, "NK225mini": {Tick: 5, PointValue: 100},
		"NK225micro": {Tick: 5, PointValue: 10},
	}
	for _, product := range []string{"TOPIX", "TOPIXmini", "NK225", "NK225mini", "NK225micro"} {
		t.Run(product, func(t *testing.T) {
			cfg, err := Load(writeConfiguration(t, "[market]\nproduct='"+product+"'\nmax_per_side=10\n"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Products, expected) {
				t.Fatalf("省略した商品規格の補完が不正です: %#v", cfg.Products)
			}
			night := "T04"
			if Reference(product) == "NK225" {
				night = "U06"
			}
			if cfg.Methods != (Methods{Day: "T06", Night: night}) || cfg.MethodID("Day") != "T06" || cfg.MethodID("Night") != night {
				t.Fatalf("標準の昼夜IDが不正です: %#v", cfg.Methods)
			}
			before := cfg.Signature()
			if err := cfg.Validate(); err != nil || cfg.Signature() != before {
				t.Fatalf("再検証で標準設定が変化しました: %v", err)
			}
		})
	}
	legacy, err := Load(writeConfiguration(t, minimumConfig))
	if err != nil || len(legacy.Products) != 3 {
		t.Fatalf("旧設定の商品表を勝手に補完しました: %#v / %v", legacy.Products, err)
	}
	if _, err := Load(writeConfiguration(t, "[market]\nproduct='TOPIXmini'\nmax_per_side=10\n[products]\n")); err == nil {
		t.Fatal("明示した空の商品規格表を省略扱いで補完しました")
	}
	fullOld := minimumConfig + "\n[products.NK225mini]\ntick=5\npoint_value=100\n[products.NK225micro]\ntick=5\npoint_value=10\n"
	old, err := Load(writeConfiguration(t, fullOld))
	if err != nil {
		t.Fatal(err)
	}
	minimal, err := Load(writeConfiguration(t, "[market]\nproduct='TOPIXmini'\nmax_per_side=10\n"))
	if err != nil || minimal.Signature() != old.Signature() {
		t.Fatalf("全標準規格の旧設定と省略設定で指紋が変化しました: %v", err)
	}
}

// TestMethodSignatureCompatibility は標準IDで旧指紋を維持し、手法変更を区別します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。旧形式の固定SHA256を比較し、新型の構造体だけに依存した検査を避けます。
func TestMethodSignatureCompatibility(t *testing.T) {
	const oldSignature = "c61a8734cc55d789bc8c757f98d1eaaa783c343fbc60663f1d18901e6a6e55b0"
	for _, suffix := range []string{"", "\n[methods]\nday='T06'\nnight='T04'\n", "\n[methods]\n"} {
		cfg, err := Load(writeConfiguration(t, minimumConfig+suffix))
		if err != nil || cfg.Signature() != oldSignature {
			t.Fatalf("標準IDの追加で旧指紋が変化しました: %v / %s", err, cfg.Signature())
		}
	}
	signatures := map[string]bool{oldSignature: true}
	for _, fields := range []string{"day='T01'", "night='T06'", "day='T01'\nnight='T06'"} {
		cfg, err := Load(writeConfiguration(t, minimumConfig+"\n[methods]\n"+fields+"\n"))
		if err != nil {
			t.Fatal(err)
		}
		signature := cfg.Signature()
		if signatures[signature] {
			t.Fatalf("異なる手法の指紋が同一です: %s", fields)
		}
		signatures[signature] = true
	}
	for _, invalid := range []string{"day=' T06'", "day=''", "night=''", "day=' '", "night='  '"} {
		if _, err := Load(writeConfiguration(t, minimumConfig+"\n[methods]\n"+invalid+"\n")); err == nil {
			t.Fatalf("手法IDの明示空欄または前後の空白を受け付けました: %s", invalid)
		}
	}
	manual := Defaults()
	manual.Markets["NK225"] = Market{Enabled: true, Product: "NK225micro"}
	if manual.MethodID("Day") != "T06" || manual.MethodID("Night") != "U06" || manual.MethodID("不明") != "" {
		t.Fatal("手作り設定の未指定IDを既定へ補完できません")
	}
}

// ----------------------------------------

// TestJSTClockNormalization は新旧時刻表記を同じ内部時計・指紋へ正規化します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。入力専用の別名を残さず、旧台帳移行時の構造体比較も維持します。
func TestJSTClockNormalization(t *testing.T) {
	for _, clocks := range []struct{ old, current string }{
		{"day_extra='12:30'\nus_open='09:30'\nlondon_extra='16:30'", "day_extra_jst='12:30'\nus_open_jst='23:30'\nlondon_extra_jst='01:30'"},
		{"day_extra='12:45'\nus_open='09:45'\nlondon_extra='16:15'", "day_extra_jst='12:45'\nus_open_jst='23:45'\nlondon_extra_jst='01:15'"},
	} {
		old, err := Load(writeConfiguration(t, minimumConfig+"\n[schedule]\n"+clocks.old+"\n"))
		if err != nil {
			t.Fatal(err)
		}
		current, err := Load(writeConfiguration(t, minimumConfig+"\n[schedule]\n"+clocks.current+"\n"))
		if err != nil || !reflect.DeepEqual(old.Schedule, current.Schedule) || current.Signature() != old.Signature() {
			t.Fatalf("日本時間表記で時刻か旧指紋が変化しました: %v / %#v / %#v", err, old.Schedule, current.Schedule)
		}
		if current.Schedule.DayExtraJST != "" || current.Schedule.USOpenJST != "" || current.Schedule.LondonExtraJST != "" {
			t.Fatal("正規化後に入力用時刻が残っています")
		}
		before := current.Signature()
		if err := current.Validate(); err != nil || current.Signature() != before {
			t.Fatalf("再検査で日本時間を二重変換しました: %v", err)
		}
	}
	data, err := json.Marshal(Schedule{DayExtraJST: "12:30", USOpenJST: "23:30", LondonExtraJST: "01:30"})
	if err != nil || strings.Contains(string(data), "JST") {
		t.Fatalf("入力用フィールドが保存状態のJSONへ混入しました: %s / %v", data, err)
	}
}

// TestAmbiguousAndInvalidJST は明示した新旧時計の混在と不正な日本時間を拒否します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。旧値と新値が同じ時刻を指す場合も二重指定は拒否します。
func TestAmbiguousAndInvalidJST(t *testing.T) {
	for _, fields := range []string{
		"day_extra='12:30'\nday_extra_jst='12:30'",
		"us_open='09:30'\nus_open_jst='23:30'",
		"london_extra='16:30'\nlondon_extra_jst='01:30'",
		"us_open=''\nus_open_jst='23:30'",
		"day_extra_jst=''", "us_open_jst=''", "london_extra_jst=''",
		"us_open_jst='24:00'", "london_extra_jst='01:60'", "day_extra_jst='9:30'",
		"us_open_jst=' 23:30'", "us_open_jst='23:30:00'",
	} {
		if _, err := Load(writeConfiguration(t, minimumConfig+"\n[schedule]\n"+fields+"\n")); err == nil {
			t.Fatalf("曖昧または不正な日本時間設定を受け付けました: %s", fields)
		}
	}
}

// TestLegacyFrozenSignatureAndNewKeys は移行専用読取の旧指紋を固定し、新設定の誤使用を拒否します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。旧二商品版の元資料だけを移行の根拠に使えることを検査します。
func TestLegacyFrozenSignatureAndNewKeys(t *testing.T) {
	body := minimumConfig + "\n[markets.NK225]\nenabled=true\nproduct='NK225'\nmax_per_side=2\n"
	_, signature, err := ReadLegacyForMigration(writeConfiguration(t, body))
	if err != nil || signature != "17313f77f968f172abfeec48380e7f9f4e897d30fd14f820e625b15b65f1ce63" {
		t.Fatalf("旧二商品版の指紋が変化しました: %s / %v", signature, err)
	}
	for _, suffix := range []string{
		"\n[methods]\nday='T06'\nnight='T04'",
		"\n[schedule]\nday_extra_jst='12:30'", "\n[schedule]\nus_open_jst='23:30'",
		"\n[schedule]\nlondon_extra_jst='01:30'",
	} {
		if _, _, err := ReadLegacyForMigration(writeConfiguration(t, body+suffix+"\n")); err == nil {
			t.Fatalf("新しいキーを旧設定の移行へ受け付けました: %s", suffix)
		}
	}
}
