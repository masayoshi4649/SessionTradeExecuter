package config

import (
	"fmt"
	"strings"
	"testing"
)

// TestQualifiedMethodNames は短IDと銘柄付きIDを同じ内部条件へ正規化します。
//
// 引数: t(*testing.T)。返り値: なし。商品規格違いで参照市場の名前を変えないことも検査します。
func TestQualifiedMethodNames(t *testing.T) {
	for _, tc := range []struct{ product, day, night string }{
		{"TOPIX", "T06", "T04"}, {"TOPIXmini", "T06", "T04"},
		{"NK225", "D07", "D07"}, {"NK225mini", "TG04", "PG04"}, {"NK225micro", "T06", "U06"},
	} {
		reference := Reference(tc.product)
		body := fmt.Sprintf("[market]\nproduct=%q\nmax_per_side=10\n[methods]\nday=%q\nnight=%q\n", tc.product, tc.day, tc.night)
		short, err := Load(writeConfiguration(t, body))
		if err != nil {
			t.Fatal(err)
		}
		body = fmt.Sprintf("[market]\nproduct=%q\nmax_per_side=10\n[methods]\nday=%q\nnight=%q\n", tc.product, reference+"_"+tc.day, reference+"_"+tc.night)
		qualified, err := Load(writeConfiguration(t, body))
		if err != nil {
			t.Fatal(err)
		}
		if qualified.Methods != short.Methods || qualified.Signature() != short.Signature() || qualified.MethodID("Day") != tc.day || qualified.MethodID("Night") != tc.night {
			t.Fatal("名前表記で内部手法または設定指紋が変わりました", tc, qualified.Methods, short.Methods)
		}
		if qualified.MethodName("Day") != reference+"_"+tc.day || qualified.MethodName("Night") != reference+"_"+tc.night {
			t.Fatal("銘柄付き表示が不正です", tc)
		}
		if NameOfMethod(tc.product, tc.day) != reference+"_"+tc.day || NameOfMethod(tc.product, reference+"_"+tc.day) != reference+"_"+tc.day {
			t.Fatal("mini/micro又は二重prefixの表示が不正です", tc)
		}
	}
}

// TestQualifiedMethodRejectsOtherMarket は別銘柄・空ID・重複prefixを起動前に拒否します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestQualifiedMethodRejectsOtherMarket(t *testing.T) {
	for _, method := range []string{"TOPIX_D07", "NK225mini_D07", "NK225_", "NK225_NK225_D07", "NK225_D07 ", " NK225_D07"} {
		body := fmt.Sprintf("[market]\nproduct='NK225mini'\nmax_per_side=10\n[methods]\nday=%q\nnight='NK225_D07'\n", method)
		if _, err := Load(writeConfiguration(t, body)); err == nil {
			t.Fatal("不正な銘柄付きIDを受け入れました", method)
		}
	}
	body := strings.Replace(webConfiguration(), "day=\"TG04\"", "day=\"TOPIX_TG04\"", 1)
	if _, err := Load(writeConfiguration(t, body)); err == nil {
		t.Fatal("Web商品一覧で別銘柄の手法を受け入れました")
	}
}

// TestQualifiedWebMethods はWeb設定の表示名を各workerの短IDへ正規化します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestQualifiedWebMethods(t *testing.T) {
	body := strings.ReplaceAll(webConfiguration(), "\"TG04\"", "\"NK225_TG04\"")
	body = strings.ReplaceAll(body, "\"T06\"", "\"TOPIX_T06\"")
	body = strings.ReplaceAll(body, "\"T04\"", "\"TOPIX_T04\"")
	cfg, err := Load(writeConfiguration(t, body))
	if err != nil {
		t.Fatal(err)
	}
	workers, err := cfg.WorkerConfigs()
	if err != nil {
		t.Fatal(err)
	}
	for _, worker := range workers {
		if strings.Contains(worker.MethodID("Day"), "_") || !strings.Contains(worker.MethodName("Day"), "_") {
			t.Fatal("内部IDと表示名が分離されていません")
		}
	}
}
