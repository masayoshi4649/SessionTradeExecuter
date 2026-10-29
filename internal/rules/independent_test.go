package rules

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// independentSpec は契約試験に必要な結果型の条件を選びます。
//
// 引数: t(*testing.T)、kind(string)。返り値: Specは標準条件です。
func independentSpec(t *testing.T, kind string) Spec {
	t.Helper()
	for _, spec := range Specs() {
		if spec.Kind == kind {
			return spec
		}
	}
	t.Fatal("指定された型の標準条件がありません", kind)
	return Spec{}
}

// independentEnv は標準条件の型付き人工入力を複製します。
//
// 引数: spec(Spec)。返り値: map[string]anyは真偽値又は有限数値だけの入力です。
func independentEnv(spec Spec) map[string]any {
	out := map[string]any{}
	for name, value := range spec.Env {
		out[name] = value
	}
	return out
}

// TestIndependentRuleContracts は許可していない構文・不正型・欠落を独立に反証します。
//
// 引数: t(*testing.T)。返り値: なし。金融データ・発注APIは使用しません。
func TestIndependentRuleContracts(t *testing.T) {
	standard, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	boolean := independentSpec(t, "bool")
	for _, code := range []string{"unknownInput > 0", "1", "nil", "[1, 2]", "now()", "map([1], #)", "1 / 0 > 0", "let x = 1; x > 0", "{x: 1}.x > 0", "\"a\" == \"a\""} {
		t.Run(code, func(t *testing.T) {
			expressions := standard.Expressions()
			expressions[boolean.ID] = code
			if _, err := Compile(expressions); err == nil {
				t.Fatal("禁止構文又は誤った型を受け入れました", code)
			}
		})
	}
	expressions := standard.Expressions()
	delete(expressions, boolean.ID)
	if _, err := Compile(expressions); err == nil {
		t.Fatal("必須条件の欠落を受け入れました")
	}
	expressions["未知条件"] = "true"
	if _, err := Compile(expressions); err == nil {
		t.Fatal("同じ条件数の未知名を受け入れました")
	}
}

// TestIndependentRuleRuntimeLatch は入力不正・結果非有限の後も停止状態が継続することを確かめます。
//
// 引数: t(*testing.T)。返り値: なし。次の正常値でエラーを自動解除してはいけません。
func TestIndependentRuleRuntimeLatch(t *testing.T) {
	standard, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	numeric := independentSpec(t, "number")
	expressions := standard.Expressions()
	expressions[numeric.ID] = "1e308 * 1e308"
	runtime, err := NewRuntime(expressions)
	if err != nil {
		t.Fatal(err)
	}
	if value := runtime.Number(numeric.ID, independentEnv(numeric)); value != 0 || runtime.Err() == nil {
		t.Fatal("数値結果の非有限を検出できません", value, runtime.Err())
	}
	first := runtime.Err()
	boolean := independentSpec(t, "bool")
	if runtime.Bool(boolean.ID, independentEnv(boolean)) || runtime.Err() != first {
		t.Fatal("最初の異常を保持せず処理を継続しました")
	}
	for _, malformed := range []any{math.NaN(), math.Inf(1), 1, "1", nil} {
		var chosen Spec
		key := ""
		for _, spec := range Specs() {
			for name, value := range spec.Env {
				if _, ok := value.(float64); ok {
					chosen, key = spec, name
					break
				}
			}
			if key != "" {
				break
			}
		}
		if key == "" {
			t.Fatal("有限数値の契約がありません")
		}
		runtime, err = NewRuntime(nil)
		if err != nil {
			t.Fatal(err)
		}
		env := independentEnv(chosen)
		env[key] = malformed
		if chosen.Kind == "bool" {
			runtime.Bool(chosen.ID, env)
		} else {
			runtime.Number(chosen.ID, env)
		}
		if runtime.Err() == nil {
			t.Fatalf("不正な入力型又は値を受け入れました: %#v", malformed)
		}
	}
}

// TestIndependentRuleHashAndSample は実TOMLの経路と式指紋の不変性を検査します。
//
// 引数: t(*testing.T)。返り値: なし。複製した式の変更で標準を破壊しないことも確認します。
func TestIndependentRuleHashAndSample(t *testing.T) {
	standard, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "d07.toml")
	if err = os.WriteFile(path, Sample(), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.Hash() != standard.Hash() {
		t.Fatal("標準とTOMLの判断式が一致しません", err)
	}
	before := standard.Hash()
	expressions := standard.Expressions()
	spec := independentSpec(t, "bool")
	expressions[spec.ID] = "false"
	if expressions[spec.ID] == standard.Expressions()[spec.ID] {
		expressions[spec.ID] = "true"
	}
	modified, err := Compile(expressions)
	if err != nil || modified.Hash() == before || standard.Hash() != before {
		t.Fatal("式の変更又は不変の標準を指紋で区別できません", err)
	}
}

// TestIndependentRulePublishedSample は配布処理と同じ標準TOMLを外部読込照合用へ保存します。
//
// 引数: t(*testing.T)、環境変数SESSIONTRADER_D07_SAMPLE_OUTPUT(string)は出力先です。
// 返り値: なし。指定なしでは何も保存しません。
func TestIndependentRulePublishedSample(t *testing.T) {
	path := os.Getenv("SESSIONTRADER_D07_SAMPLE_OUTPUT")
	if path == "" {
		t.Skip("外部読込照合の保存先指定なし")
	}
	if err := os.WriteFile(path, Sample(), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	standard, err := Default()
	if err != nil || loaded.Hash() != standard.Hash() {
		t.Fatal("配布する標準TOMLが組込条件と一致しません", err)
	}
}
