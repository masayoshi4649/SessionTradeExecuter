package rules

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEveryProfileRoundTrip は全手法の標準ルールを実TOML経由で型検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestEveryProfileRoundTrip(t *testing.T) {
	for _, profile := range ProfileNames() {
		t.Run(profile, func(t *testing.T) {
			set, err := DefaultFor(profile)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), profile+".toml")
			if err := os.WriteFile(path, Content(profile), 0600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadFor(path, profile)
			if err != nil || loaded.Hash() != set.Hash() || loaded.Profile() != profile {
				t.Fatalf("条件を保存・再読込できません: %v", err)
			}
			other := "d07"
			if profile == other {
				other = "tg04"
			}
			if _, err = LoadFor(path, other); err == nil {
				t.Fatal("他手法のファイルを誤って採用しました")
			}
		})
	}
}

// TestProfileSelection は銘柄・昼夜ごとの手法選択と不正な組合せを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestProfileSelection(t *testing.T) {
	for _, row := range [][4]string{{"NK225", "Day", "D07", "d07"}, {"NK225", "Night", "TG04", "tg04"}, {"NK225", "Day", "PG04", "pg04"}, {"NK225", "Day", "T06", "t06_nk225"}, {"TOPIX", "Day", "T06", "t06_topix"}, {"TOPIX", "Night", "T04", "t04"}, {"NK225", "Night", "U06", "u06"}} {
		got, err := SelectProfile(row[0], row[1], row[2])
		if err != nil || got != row[3] {
			t.Fatal(row, got, err)
		}
	}
	if _, err := SelectProfile("TOPIX", "Night", "D07"); err == nil {
		t.Fatal("対象外の組合せを受理しました")
	}
}
