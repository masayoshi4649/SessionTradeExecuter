package config

import (
	"fmt"
	"strings"
	"testing"
)

// webConfiguration は全五商品の独立設定を表す検査用TOMLを作ります。
//
// 引数: なし。返り値: stringは正常なWeb設定です。
func webConfiguration() string {
	text := "[web]\n[trading]\ndryrun=true\n"
	for _, product := range []string{"NK225", "NK225mini", "NK225micro", "TOPIX", "TOPIXmini"} {
		day, night := "TG04", "TG04"
		if Reference(product) == "TOPIX" {
			day, night = "T06", "T04"
		}
		text += fmt.Sprintf("\n[[instruments]]\nproduct=%q\nmax_per_side=10\nenabled=false\nday=%q\nnight=%q\n", product, day, night)
	}
	return text
}

// TestWebIndependentWorkers はポート既定値と全商品の台帳・手法・上限の独立性を確認します。
//
// 引数: t *testing.Tは試験。返り値: なし。
func TestWebIndependentWorkers(t *testing.T) {
	cfg, err := Load(writeConfiguration(t, webConfiguration()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.Host != "0.0.0.0" || cfg.Web.Port != 8090 {
		t.Fatalf("待受先の既定値が異なります: %+v", cfg.Web)
	}
	workers, err := cfg.WorkerConfigs()
	if err != nil || len(workers) != 5 {
		t.Fatal("全商品へ展開できません", err)
	}
	for i, worker := range workers {
		ref, market := worker.Selected()
		if market.Product != cfg.Instruments[i].Product || market.MaxPerSide != 10 || ref != Reference(market.Product) || worker.Web != nil || len(worker.Instruments) != 0 {
			t.Fatalf("商品別の設定が不正です: %+v", worker)
		}
		if worker.InstanceID() != strings.ToLower(market.Product) || worker.Root != cfg.Root || !worker.Trading.DryRun {
			t.Fatal("商品別の保存先または仮想モードが不正です")
		}
		if err := worker.Validate(); err != nil {
			t.Fatal("正規化後の再検査に失敗", err)
		}
	}
	workers[0].Products["NK225"] = Product{Tick: 99}
	workers[0].Markets["NK225"] = Market{Product: "壊した値"}
	if workers[1].Products["NK225"].Tick == 99 || cfg.Products["NK225"].Tick == 99 || workers[1].Markets["NK225"].Product != "NK225mini" {
		t.Fatal("商品の変更が別商品の設定へ漏れました")
	}
}

// TestWebRejectsAmbiguity は混在・重複・無効な待受先を起動前に拒否します。
//
// 引数: t *testing.Tは試験。返り値: なし。
func TestWebRejectsAmbiguity(t *testing.T) {
	base := webConfiguration()
	for _, body := range []string{
		strings.Replace(base, "[web]", "[web]\nport=0", 1),
		strings.Replace(base, "[web]", "[web]\nport=65536", 1),
		strings.Replace(base, "[web]", "[web]\nhost='http://localhost'", 1),
		strings.Replace(base, "[web]", "[web]\nhost=''", 1),
		strings.Replace(base, "[web]\n", "", 1),
		strings.Replace(base, "product=\"TOPIXmini\"", "product=\"TOPIX\"", 1),
		strings.Replace(base, "product=\"TOPIXmini\"", "product=\"OPTION\"", 1),
		strings.Replace(base, "max_per_side=10", "max_per_side=0", 1),
		strings.Replace(base, "night=\"TG04\"", "night=\"\"", 1),
		strings.Replace(base, "[trading]", "[trading]\ninstance_id=''", 1),
		base + "\n[methods]\n",
		base + "\n[market]\nproduct='TOPIXmini'\nmax_per_side=10\n",
		base + "\n[algorithm]\n",
		base + "\n[[instruments]]\nproduct='NK225'\nmax_per_side=10\nday='TG04'\nnight='TG04'\n",
	} {
		if _, err := Load(writeConfiguration(t, body)); err == nil {
			t.Fatalf("不正なWeb設定を受け入れました: %s", body)
		}
	}
	for _, host := range []string{"localhost", "0.0.0.0", "::1", "10.10.100.1"} {
		if _, err := Load(writeConfiguration(t, strings.Replace(base, "[web]", fmt.Sprintf("[web]\nhost=%q\nport=18090", host), 1))); err != nil {
			t.Fatal("正常な待受先を拒否しました", host, err)
		}
	}
}
