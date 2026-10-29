package rules

import "testing"

// TestSelectedProfilesKeepSourceDefinitions は新IDの依存式が旧IDの標準式をそのまま含むことを確認します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。旧ファイルの読み替えや同名上書きを行いません。
func TestSelectedProfilesKeepSourceDefinitions(t *testing.T) {
	for _, pair := range [][2]string{{"d164_15", "s01"}, {"p164_06", "p05"}, {"u165_03", "d07"}} {
		set, err := DefaultFor(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		expressions := set.Expressions()
		source := map[string]string{}
		for _, spec := range SpecsFor(pair[1]) {
			if expressions[spec.ID] != spec.Expression {
				t.Fatal("依存式を変更しました", pair, spec.ID)
			}
			source[spec.ID] = expressions[spec.ID]
		}
		compiled, err := CompileFor(pair[1], source)
		if err != nil {
			t.Fatal(err)
		}
		original, _ := DefaultFor(pair[1])
		if compiled.Hash() != original.Hash() {
			t.Fatal("源の指紋が一致しません")
		}
	}
}

// TestSelectedRulesKnownAndBoundary は未知・境界・売買対称を専用Exprで検査します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。全入力の型と数を実行時にも検査します。
func TestSelectedRulesKnownAndBoundary(t *testing.T) {
	d, err := NewRuntimeFor("d164_15", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, side := range []float64{-1, 1} {
		env := map[string]any{"short_known": true, "long_known": true, "side": side, "speed15": side * 2, "speed5": side}
		if !d.Bool("d164_15.delay_initial", env) {
			t.Fatal("半速の初回を待機しません")
		}
		env["short_known"] = false
		if d.Bool("d164_15.delay_initial", env) {
			t.Fatal("未知を失速扱いしました")
		}
	}
	if !d.Bool("d164_15.stop_add", map[string]any{"source_add": true, "gain": float64(-50)}) || d.Bool("d164_15.stop_add", map[string]any{"source_add": false, "gain": float64(-60)}) {
		t.Fatal("追撃専用の損切りが不正です")
	}
	u, err := NewRuntimeFor("u165_03", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u.Bool("selected.u165_03.timeout", map[string]any{"elapsed": float64(30)}) || !u.Bool("selected.u165_03.timeout", map[string]any{"elapsed": float64(31)}) {
		t.Fatal("30秒境界が不正です")
	}
	if u.Bool("selected.u165_03.release", map[string]any{"known": true, "same_active": true, "same_index": float64(2), "deferred_index": float64(2)}) {
		t.Fatal("待機同原行で再開しました")
	}
	p, err := NewRuntimeFor("p164_06", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Number("selected.p164_06.trail_floor", map[string]any{"best_gain": float64(10)}) != 0 || p.Number("selected.p164_06.trail_floor", map[string]any{"best_gain": float64(50)}) != 30 {
		t.Fatal("保護床が不正です")
	}
	for _, r := range []*Runtime{d, p, u} {
		if r.Err() != nil {
			t.Fatal(r.Err())
		}
	}
}
