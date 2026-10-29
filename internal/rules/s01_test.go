package rules

import (
	"path/filepath"
	"testing"
)

// TestS01ExternalRulesAndScope はS01の3条件と適用範囲を実ファイルから検査します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。
func TestS01ExternalRulesAndScope(t *testing.T) {
	path := filepath.Join("..", "..", "conf", "rules", "NK225_S01.toml")
	set, err := LoadFor(path, "s01")
	if err != nil {
		t.Fatal(err)
	}
	standard, err := DefaultFor("s01")
	if err != nil || set.Hash() != standard.Hash() {
		t.Fatal("配布するS01式が標準定義と一致しません", err)
	}
	runtime := &Runtime{Set: set}
	values := map[string]any{"parent_value": float64(9999), "risk": float64(100), "initial_quantity": float64(2), "point_value": float64(100)}
	if runtime.Bool("s01.guard_active", values) {
		t.Fatal("初回固定額の半R未満で保護を開始しました")
	}
	values["parent_value"] = float64(10000)
	if !runtime.Bool("s01.guard_active", values) || runtime.Number("s01.guard_floor", map[string]any{"best_value": float64(30000)}) != 15000 {
		t.Fatal("保護開始又は最高益の半分の床が不正です", runtime.Err())
	}
	for _, item := range []struct{ occupied, same, want bool }{{false, false, true}, {true, false, false}, {true, true, true}} {
		if got := runtime.Bool("s01.entry_allowed", map[string]any{"baseline_occupied": item.occupied, "same_campaign": item.same}); got != item.want {
			t.Fatal("仮想D07の異親占有を誤判定しました", item)
		}
	}
	if runtime.Err() != nil {
		t.Fatal(runtime.Err())
	}
	for _, row := range [][3]string{{"NK225", "Night", "NK225_S01"}, {"TOPIX", "Day", "S01"}} {
		if _, err := SelectProfile(row[0], row[1], row[2]); err == nil {
			t.Fatal("S01を対象外の昼夜・市場へ適用しました", row)
		}
	}
	if profile, err := SelectProfile("NK225", "Day", "NK225_S01"); err != nil || profile != "s01" {
		t.Fatal("日経日中のS01を選べません", err)
	}
}
