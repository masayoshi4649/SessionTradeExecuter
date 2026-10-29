package config

import (
	"reflect"
	"strings"
	"testing"
)

// windowTestConfig は外部通信せず既存五商品設定から日経miniのworkerを作ります。
//
// 引数: t(*testing.T)は試験。返り値: Configは検査済み単品設定です。
func windowTestConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := Load(writeConfiguration(t, webConfiguration()))
	if err != nil {
		t.Fatal(err)
	}
	workers, err := cfg.WorkerConfigs()
	if err != nil {
		t.Fatal(err)
	}
	return workers[1]
}

// TestWindowSyntaxAndSeasonalOverlap は銘柄混同・昼夜不適合・夏時間だけの重複を拒否します。
//
// 引数: t(*testing.T)は試験。返り値: なし。
func TestWindowSyntaxAndSeasonalOverlap(t *testing.T) {
	cfg := windowTestConfig(t)
	valid := []MethodWindow{{"day", "Day", "SESSION_START", "SESSION_END", "NK225_D07"}, {"pre", "Night", "SESSION_START", "US_OPEN-00:15", "NK225_D07"}, {"quiet", "Night", "US_OPEN-00:15", "US_OPEN+00:15", ""}, {"post", "Night", "US_OPEN+00:15", "SESSION_END", "NK225_PG04"}}
	cfg.Windows = valid
	if err := cfg.ValidateMethodWindows(); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]MethodWindow{
		"重複ID":      {{"one", "Day", "09:00", "10:00", "NK225_D07"}, {"one", "Day", "10:00", "11:00", "NK225_D07"}},
		"時計不正":      {{"one", "Day", "9:00", "10:00", "NK225_D07"}},
		"時計逆転":      {{"one", "Day", "10:00", "09:00", "NK225_D07"}},
		"銘柄違い":      {{"one", "Day", "09:00", "10:00", "TOPIX_T06"}},
		"昼夜違い":      {{"one", "Night", "17:00", "20:00", "NK225_T06"}},
		"日中米国":      {{"one", "Day", "US_OPEN", "SESSION_END", "NK225_D07"}},
		"夏時間だけ重複":   {{"one", "Night", "17:00", "22:30", "NK225_D07"}, {"two", "Night", "US_OPEN-00:15", "SESSION_END", "NK225_D07"}},
		"空白手法の取り違い": {{"one", "Night", "17:00", "20:00", " "}},
	}
	for name, windows := range cases {
		t.Run(name, func(t *testing.T) {
			cfg.Windows = windows
			if err := cfg.ValidateMethodWindows(); err == nil {
				t.Fatal("不正枠を許可しました")
			}
		})
	}
	for _, value := range []string{"US_OPEN+12:01", "US_OPEN+00:60", "US_OPEN-0:15", "24:00", "SESSION_END "} {
		if _, err := ParseWindowMoment(value); err == nil {
			t.Fatal("不正時計を許可しました", value)
		}
	}
}

// TestWindowPersistenceAndIndependentCopies は入れ子TOMLとREST用コピーの独立性を確認します。
//
// 引数: t(*testing.T)は試験。返り値: なし。
func TestWindowPersistenceAndIndependentCopies(t *testing.T) {
	dir := writeConfiguration(t, webConfiguration())
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := ReadWebSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	windows := []MethodWindow{{"day", "Day", "SESSION_START", "SESSION_END", "NK225_D07"}, {"pre", "Night", "SESSION_START", "US_OPEN-00:15", "NK225_D07"}, {"post", "Night", "US_OPEN+00:15", "SESSION_END", "NK225_PG04"}}
	settings.Instruments[1].Windows = windows
	saved, err := SaveWebSettings(cfg, settings, settings.Revision)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Instruments[1].Windows, windows) {
		t.Fatalf("保存再読込で変化しました: %+v", loaded.Instruments[1].Windows)
	}
	view, err := ReadWebSettings(saved)
	if err != nil {
		t.Fatal(err)
	}
	view.Instruments[1].Windows[0].Method = "NK225_PG04"
	if saved.Instruments[1].Windows[0].Method != "NK225_D07" {
		t.Fatal("REST用コピーが元設定を書き換えました")
	}
	workers, err := saved.WorkerConfigs()
	if err != nil {
		t.Fatal(err)
	}
	workers[1].Windows[0].Method = "NK225_TG04"
	if saved.Instruments[1].Windows[0].Method != "NK225_D07" {
		t.Fatal("workerコピーが元設定を書き換えました")
	}
}

// TestLegacyWindowsRemainImplicit は既存の昼夜手法を変えず独立した既定枠を返します。
//
// 引数: t(*testing.T)は試験。返り値: なし。
func TestLegacyWindowsRemainImplicit(t *testing.T) {
	cfg := windowTestConfig(t)
	before := cfg.Signature()
	windows := cfg.MethodWindows()
	if len(windows) != 2 || windows[0].ID != "Day" || windows[1].Method != cfg.MethodName("Night") {
		t.Fatal(windows)
	}
	windows[0].Method = "NK225_PG04"
	if len(cfg.Windows) != 0 || cfg.Signature() != before {
		t.Fatal("旧設定を暗黙変更しました")
	}
	cfg.Windows = cfg.MethodWindows()
	if cfg.Signature() == before {
		t.Fatal("明示時間帯が指紋へ反映されません")
	}
}

// TestExplicitEmptyWindowsNeverFallsBack は明示空配列を旧昼夜運転へ読み替えないことを検査します。
//
// 引数: t(*testing.T)は試験。返り値: なし。TOMLと全般設定更新の双方を確認します。
func TestExplicitEmptyWindowsNeverFallsBack(t *testing.T) {
	cloned := CloneInstruments([]Instrument{{Windows: []MethodWindow{}}})
	if cloned[0].Windows == nil {
		t.Fatal("複製で明示空配列が未指定へ変わりました")
	}
	body := strings.Replace(webConfiguration(), `product="NK225mini"`, `product="NK225mini"`+"\nwindows=[]", 1)
	if _, err := Load(writeConfiguration(t, body)); err == nil {
		t.Fatal("TOMLの明示空配列が旧昼夜へ戻りました")
	}
	worker := windowTestConfig(t)
	worker.Windows = []MethodWindow{}
	if err := worker.ValidateMethodWindows(); err == nil {
		t.Fatal("単品の明示空配列を許可しました")
	}
	cfg, err := Load(writeConfiguration(t, webConfiguration()))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := ReadWebSettings(cfg)
	if err != nil {
		t.Fatal(err)
	}
	settings.Instruments[1].Windows = []MethodWindow{}
	if _, err := ApplyWebSettings(cfg, settings); err == nil {
		t.Fatal("全般RESTの空配列を許可しました")
	}
	settings.Instruments[1].Windows = []MethodWindow{{ID: "pause", Session: "Night", Start: "SESSION_START", End: "SESSION_END", Method: ""}}
	next, err := ApplyWebSettings(cfg, settings)
	if err != nil {
		t.Fatal("明示静観を拒否しました", err)
	}
	view, err := ReadWebSettings(next)
	if err != nil {
		t.Fatal(err)
	}
	view.Instruments[1].Windows = nil
	if _, err := ApplyWebSettings(next, view); err == nil {
		t.Fatal("既存枠の省略で旧昼夜運転へ戻りました")
	}
}
