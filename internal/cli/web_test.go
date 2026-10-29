package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
)

// webConfiguration は設定動作を差し替えた五商品のTOMLを作ります。
//
// 引数: action(string)は起動動作。返り値: stringは試験用TOML。
func webConfiguration(action string) string {
	text := "[startup]\naction='" + action + "'\n[web]\nport=8099\n[trading]\ndryrun=true\n"
	for _, product := range []string{"NK225", "NK225mini", "NK225micro", "TOPIX", "TOPIXmini"} {
		day, night := "TG04", "TG04"
		if config.Reference(product) == "TOPIX" {
			day, night = "T06", "T04"
		}
		text += fmt.Sprintf("[[instruments]]\nproduct='%s'\nmax_per_side=10\nday='%s'\nnight='%s'\nenabled=false\n", product, day, night)
	}
	return text
}

// TestWebDispatchAndCheck は画面版の起動が旧単品実行に流れず、全商品の手法を検査することを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestWebDispatchAndCheck(t *testing.T) {
	webCalls, singleCalls := 0, 0
	operations := Operations{Run: func(context.Context, config.Config) error { singleCalls++; return nil },
		Web: func(_ context.Context, c config.Config) error {
			webCalls++
			if c.Web.Port != 8099 {
				t.Fatal("ポートが変わりました")
			}
			return nil
		}}
	var out bytes.Buffer
	for _, action := range []string{"check", "run"} {
		if err := Run(context.Background(), configuration(t, webConfiguration(action)), nil, &out, &out, operations); err != nil {
			t.Fatal(err)
		}
	}
	if webCalls != 1 || singleCalls != 0 || !strings.Contains(out.String(), "TOPIXmini") {
		t.Fatal("Webと単品の起動が混在しました", out.String())
	}
	invalid := strings.Replace(webConfiguration("run"), "night='TG04'", "night='存在しないID'", 1)
	if Run(context.Background(), configuration(t, invalid), nil, &out, &out, operations) == nil {
		t.Fatal("未対応手法を許可しました")
	}
	if webCalls != 1 {
		t.Fatal("設定エラーなのに画面を起動しました")
	}
}
