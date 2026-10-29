package rules

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestSampleRoundTrip は配布TOMLと標準式の完全一致を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSampleRoundTrip(t *testing.T) {
	set, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "d07.toml")
	if err = os.WriteFile(path, Sample(), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Hash() != set.Hash() {
		t.Fatalf("配布条件が標準式と一致しません: %v", err)
	}
	copy := loaded.Expressions()
	for key := range copy {
		copy[key] = "false"
		break
	}
	if loaded.Hash() != set.Hash() {
		t.Fatal("外部から条件を書き換えられました")
	}
}

// TestInvalidExpressions は誤記・不足・型違い・外部アクセスを起動前に拒否します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestInvalidExpressions(t *testing.T) {
	set, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, spec := range Specs() {
		if spec.Kind == "bool" {
			id = spec.ID
			break
		}
	}
	for _, code := range []string{"unknown_name > 0", "42", "now() != nil", "all([1, 2], # > 0)", "1 / 0 > 0", "true &&"} {
		values := set.Expressions()
		values[id] = code
		if _, err = Compile(values); err == nil {
			t.Fatalf("不正な式を受理しました: %s", code)
		}
	}
	values := set.Expressions()
	delete(values, id)
	if _, err = Compile(values); err == nil {
		t.Fatal("欠落を受理しました")
	}
	values["typo"] = "true"
	if _, err = Compile(values); err == nil {
		t.Fatal("誤記を受理しました")
	}
}

// TestRuntimeErrorLatches は欠損・非有限入力の後に正常な式へ進まないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestRuntimeErrorLatches(t *testing.T) {
	set, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range Specs() {
		if len(spec.Env) == 0 {
			continue
		}
		r := &Runtime{Set: set}
		r.run(spec.ID, spec.Kind, nil)
		if r.Err() == nil {
			t.Fatal("入力欠落を検出していません")
		}
		if r.run(spec.ID, spec.Kind, spec.Env) != nil {
			t.Fatal("異常後に評価を継続しました")
		}
		break
	}
	for _, spec := range Specs() {
		for name, prototype := range spec.Env {
			if _, ok := prototype.(float64); !ok {
				continue
			}
			env := map[string]any{}
			for k, v := range spec.Env {
				env[k] = v
			}
			env[name] = math.NaN()
			r := &Runtime{Set: set}
			r.run(spec.ID, spec.Kind, env)
			if r.Err() == nil {
				t.Fatal("NaNを検出していません")
			}
			return
		}
	}
	t.Fatal("数値条件がありません")
}
