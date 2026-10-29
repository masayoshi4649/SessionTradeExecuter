package config

import (
	"fmt"
	"testing"
)

// TestExplicitPortfolioKeepsLegacyDefaults は新手法の明示指定だけが復元指紋を変えることを確認します。
//
// 引数: t(*testing.T)は試験です。
// 返り値: なし。省略時の旧ID、運用設定と整数の上限値を維持します。
func TestExplicitPortfolioKeepsLegacyDefaults(t *testing.T) {
	for _, product := range []string{"NK225", "NK225mini", "NK225micro"} {
		for _, cap := range []int{1, 2, 3, 10, 11, 20} {
			t.Run(fmt.Sprintf("%s/%d", product, cap), func(t *testing.T) {
				body := fmt.Sprintf("[market]\nproduct=%q\nmax_per_side=%d\n[trading]\ndryrun=false\n[runtime]\nstatus_seconds=47\ndata_directory='独自保存先'\n", product, cap)
				legacy, err := Load(writeConfiguration(t, body))
				if err != nil {
					t.Fatal(err)
				}
				if legacy.Methods != (Methods{Day: "T06", Night: "U06"}) {
					t.Fatal("省略時の手法を新手法へ変更しました", legacy.Methods)
				}
				seen := map[string]bool{legacy.Signature(): true}
				for _, method := range []string{"TG04", "PG04", "D07"} {
					cfg, err := Load(writeConfiguration(t, body+fmt.Sprintf("\n[methods]\nday=%q\nnight=%q\n", method, method)))
					if err != nil {
						t.Fatal(err)
					}
					_, selected := cfg.Selected()
					if cfg.Methods != (Methods{Day: method, Night: method}) || selected.MaxPerSide != cap || cfg.Trading.DryRun || cfg.Runtime.StatusSeconds != 47 || cfg.Runtime.DataDirectory != "独自保存先" {
						t.Fatal("明示IDまたは運用設定を変更しました", cfg)
					}
					if signature := cfg.Signature(); seen[signature] {
						t.Fatal("異なる新手法を同じ復元指紋にしました", method)
					} else {
						seen[signature] = true
					}
				}
			})
		}
	}
}
