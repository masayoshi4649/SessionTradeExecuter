// Package rules は外部TOMLの売買条件をExprで型検査し、状態を変更せず評価します。
package rules

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"
)

// Spec は一つの条件の識別子、説明、型付き入力と標準式です。
type Spec struct {
	ID, Description, Expression, Kind string
	Env                               map[string]any
}

// File は保存するルール一式です。欠落・誤記を黙って標準値で埋めません。
type File struct {
	Version     int               `toml:"version"`
	Expressions map[string]string `toml:"expressions"`
	Profile     string            `toml:"profile" json:",omitempty"`
}

type compiled struct {
	spec    Spec
	program *vm.Program
}

// Set は起動時に固定されるコンパイル済み条件です。複数銘柄で共有できます。
type Set struct {
	profile     string
	items       map[string]compiled
	expressions map[string]string
	hash        string
}

// Runtime は一つの戦略が所有する評価エラーです。エラー後は売買依頼を外へ出しません。
type Runtime struct {
	Set *Set
	err error
}

var defaultOnce sync.Once
var defaultSet *Set
var defaultError error

// Specs は識別子順の条件定義を返します。
//
// 引数: なし。返り値: []Specは全条件の定義です。
func Specs() []Spec {
	items := append(addonSpecs(), coreSpecs()...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

// Default は検証済み標準式を一度だけコンパイルします。
//
// 引数: なし。返り値: *Setとerrorは標準定義の不整合です。
func Default() (*Set, error) {
	defaultOnce.Do(func() {
		expressions := map[string]string{}
		for _, spec := range Specs() {
			expressions[spec.ID] = spec.Expression
		}
		defaultSet, defaultError = Compile(expressions)
	})
	return defaultSet, defaultError
}

// Load は外部ルールを読み込み、未知キー・欠落・型を発注準備前に検査します。
//
// 引数: path(string)はTOMLのパス。返り値: *Setとerrorです。
func Load(path string) (*Set, error) {
	return LoadFor(path, "d07")
}

// LoadFor は指定手法のファイルを読み込み、別手法の混入も拒否します。
//
// 引数: path/profile(string)はファイルと手法名。返り値: *Setとerrorです。
func LoadFor(path, profile string) (*Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > 1024*1024 {
		return nil, fmt.Errorf("ルールファイルが1MBを超えています")
	}
	var file File
	meta, err := toml.Decode(strings.TrimPrefix(string(data), "\ufeff"), &file)
	if err != nil {
		return nil, fmt.Errorf("ルールTOML: %w", err)
	}
	if len(meta.Undecoded()) > 0 {
		return nil, fmt.Errorf("未知のルール設定: %v", meta.Undecoded())
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("ルールversionは1が必要です")
	}
	if file.Profile != profile && file.Profile != Name(profile) && !(file.Profile == "" && profile == "d07") {
		return nil, fmt.Errorf("ルールprofileが不一致: 指定%s、ファイル%s", profile, file.Profile)
	}
	return CompileFor(profile, file.Expressions)
}

// Compile は全条件を型検査し、不変の条件集を作ります。
//
// 引数: expressions(map[string]string)は全識別子に対応する式です。
// 返り値: *Setとerror。未指定の変数、条件不足、危険な構文は拒否します。
func Compile(expressions map[string]string) (*Set, error) {
	return CompileFor("d07", expressions)
}

// CompileFor は手法ごとに独立した条件集を型検査します。
//
// 引数: profile(string)、expressions(map[string]string)。返り値: *Setとerrorです。
func CompileFor(profile string, expressions map[string]string) (*Set, error) {
	specs := SpecsFor(profile)
	if len(specs) == 0 {
		return nil, fmt.Errorf("未対応のルールprofile: %s", profile)
	}
	if len(expressions) != len(specs) {
		return nil, fmt.Errorf("ルール件数が不正です: 必須%d件、指定%d件", len(specs), len(expressions))
	}
	s := &Set{profile: profile, items: map[string]compiled{}, expressions: map[string]string{}}
	for _, spec := range specs {
		code, ok := expressions[spec.ID]
		if !ok {
			return nil, fmt.Errorf("必須ルールがありません: %s", spec.ID)
		}
		if _, duplicate := s.items[spec.ID]; duplicate {
			return nil, fmt.Errorf("重複ルール: %s", spec.ID)
		}
		if err := validateSyntax(code); err != nil {
			return nil, fmt.Errorf("%s: %w", spec.ID, err)
		}
		opts := []expr.Option{expr.Env(spec.Env), expr.DisableAllBuiltins(), expr.EnableBuiltin("min"), expr.EnableBuiltin("max"), expr.EnableBuiltin("abs"), expr.MaxNodes(256)}
		if spec.Kind == "bool" {
			opts = append(opts, expr.AsBool())
		} else if spec.Kind == "number" {
			opts = append(opts, expr.AsFloat64())
		} else {
			return nil, fmt.Errorf("不正な結果型: %s", spec.ID)
		}
		opts = append(opts, expr.WarnOnAny())
		program, err := expr.Compile(code, opts...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec.ID, err)
		}
		s.items[spec.ID] = compiled{spec, program}
		s.expressions[spec.ID] = code
	}
	document := File{Version: 1, Expressions: s.expressions}
	if profile != "d07" {
		document.Profile = profile
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	s.hash = hex.EncodeToString(digest[:])
	return s, nil
}

// Hash は読み込んだ全条件のSHA256を返します。
//
// 引数: なし。返り値: stringは照合用の指紋です。
func (s *Set) Hash() string {
	if s == nil {
		return ""
	}
	return s.hash
}

// Expressions は監査・状態保存用に式を複製します。
//
// 引数: なし。返り値: map[string]stringは内部条件を書き換えられないコピーです。
func (s *Set) Expressions() map[string]string {
	if s == nil {
		return nil
	}
	out := make(map[string]string, len(s.expressions))
	for id, code := range s.expressions {
		out[id] = code
	}
	return out
}

// NewRuntime は標準式又は保存済み式から戦略専用の評価状態を作ります。
//
// 引数: expressions(map[string]string)はnilなら標準式。返り値: *Runtimeとerrorです。
func NewRuntime(expressions map[string]string) (*Runtime, error) {
	return NewRuntimeFor("d07", expressions)
}

// NewRuntimeFor は手法別に独立する評価器を作ります。
//
// 引数: profile(string)、expressions(map[string]string)はnilなら標準式。返り値: *Runtimeとerrorです。
func NewRuntimeFor(profile string, expressions map[string]string) (*Runtime, error) {
	var s *Set
	var err error
	if expressions == nil {
		s, err = DefaultFor(profile)
	} else {
		s, err = CompileFor(profile, expressions)
	}
	if err != nil {
		return nil, err
	}
	return &Runtime{Set: s}, nil
}

// Err は最初の評価エラーを返します。起動し直すまで解除しません。
//
// 引数: なし。返り値: errorは異常、正常又は標準直接評価ならnilです。
func (r *Runtime) Err() error {
	if r == nil {
		return nil
	}
	return r.err
}

// Bool は真偽条件を評価します。
//
// 引数: id(string)、env(map[string]any)は現在までに確定した数値・真偽値です。
// 返り値: boolは条件成立。異常はErrに保持し、戦略境界で全依頼を破棄します。
func (r *Runtime) Bool(id string, env map[string]any) bool {
	value := r.run(id, "bool", env)
	result, _ := value.(bool)
	return result
}

// Number は数値条件を評価します。
//
// 引数: id(string)、env(map[string]any)。返り値: float64。異常はErrへ保持します。
func (r *Runtime) Number(id string, env map[string]any) float64 {
	value := r.run(id, "number", env)
	result, _ := value.(float64)
	return result
}

// run は入力型を確認して式を評価し、最初の異常を保持します。
//
// 引数: id/kind(string)、env(map[string]any)。返り値: anyは評価値又は異常時nilです。
func (r *Runtime) run(id, kind string, env map[string]any) any {
	if r != nil && r.err != nil {
		return nil
	}
	set := (*Set)(nil)
	if r != nil {
		set = r.Set
	}
	if set == nil {
		var err error
		set, err = Default()
		if err != nil {
			return r.fail(err)
		}
	}
	c, ok := set.items[id]
	if !ok || c.spec.Kind != kind {
		return r.fail(fmt.Errorf("ルール識別子又は結果型が不正: %s", id))
	}
	if len(env) != len(c.spec.Env) {
		return r.fail(fmt.Errorf("%sの入力数が不正", id))
	}
	for name, prototype := range c.spec.Env {
		value, exists := env[name]
		if !exists {
			return r.fail(fmt.Errorf("%sの入力%sがありません", id, name))
		}
		switch prototype.(type) {
		case float64:
			n, good := value.(float64)
			if !good || math.IsNaN(n) || math.IsInf(n, 0) {
				return r.fail(fmt.Errorf("%sの入力%sは有限のfloat64が必要です", id, name))
			}
		case bool:
			if _, good := value.(bool); !good {
				return r.fail(fmt.Errorf("%sの入力%sはboolが必要です", id, name))
			}
		default:
			return r.fail(fmt.Errorf("%sの入力%sは許可されない型です", id, name))
		}
	}
	value, err := expr.Run(c.program, env)
	if err != nil {
		return r.fail(fmt.Errorf("ルール%sの評価失敗: %w", id, err))
	}
	if kind == "number" {
		n, good := value.(float64)
		if !good || math.IsNaN(n) || math.IsInf(n, 0) {
			return r.fail(fmt.Errorf("ルール%sの結果が有限値でありません", id))
		}
	}
	return value
}

// fail は評価エラーを停止まで保持します。
//
// 引数: err(error)。返り値: anyはnilです。単体の標準評価の異常はpanicで隠蔽を防ぎます。
func (r *Runtime) fail(err error) any {
	if r == nil {
		panic(err)
	}
	r.err = err
	return nil
}

// ----------------------------------------

type syntaxGuard struct{ err error }

// Visit は外部アクセスや配列処理を含まない有限長の式だけを許可します。
//
// 引数: node(*ast.Node)は構文木の要素。返り値: なし、異常をerrへ保持します。
func (v *syntaxGuard) Visit(node *ast.Node) {
	if v.err != nil {
		return
	}
	switch n := (*node).(type) {
	case *ast.IdentifierNode, *ast.BoolNode, *ast.IntegerNode, *ast.FloatNode, *ast.ConditionalNode:
	case *ast.UnaryNode:
		if n.Operator != "+" && n.Operator != "-" && n.Operator != "!" && n.Operator != "not" {
			v.err = fmt.Errorf("未対応の単項演算: %s", n.Operator)
		}
	case *ast.BinaryNode:
		switch n.Operator {
		case "+", "-", "*", "<", "<=", ">", ">=", "==", "!=", "&&", "||", "and", "or":
		default:
			v.err = fmt.Errorf("未対応の演算: %s", n.Operator)
		}
	case *ast.BuiltinNode:
		if n.Name != "min" && n.Name != "max" && n.Name != "abs" {
			v.err = fmt.Errorf("未対応の関数: %s", n.Name)
		}
	default:
		v.err = fmt.Errorf("条件には数値・真偽値・比較・論理演算・三項式・min/max/absだけ使用できます")
	}
}

// validateSyntax はコンパイル前に式の長さと使用構文を限定します。
//
// 引数: code(string)。返り値: errorは制限外の式です。
func validateSyntax(code string) error {
	if len(code) == 0 || len(code) > 4096 {
		return fmt.Errorf("条件式は1～4096バイトで指定してください")
	}
	tree, err := parser.Parse(code)
	if err != nil {
		return err
	}
	guard := &syntaxGuard{}
	ast.Walk(&tree.Node, guard)
	return guard.err
}

// Sample は説明付きの完全な標準TOMLを生成します。
//
// 引数: なし。返り値: []byteはUTF-8の設定例です。
func Sample() []byte {
	return Content("d07")
}

// Content は手法別の説明付き実ルールTOMLを生成します。
//
// 引数: profile(string)は対応手法名。返り値: []byteはUTF-8のルールです。
func Content(profile string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# %sの判断条件。変更は再起動時に適用します。\n# 設定形式の版。1のまま使用します。\nversion = 1\n# このファイルが適用される銘柄と手法名。変更しません。\nprofile = %s\n\n[expressions]\n", Name(profile), quoteTOML(Name(profile)))
	for _, spec := range SpecsFor(profile) {
		fmt.Fprintf(&b, "\n# %s\n", spec.Description)
		keys := []string{}
		for key := range spec.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			fmt.Fprintf(&b, "# 入力: %s\n", strings.Join(keys, ", "))
		}
		fmt.Fprintf(&b, "%s = %s\n", quoteTOML(spec.ID), quoteTOML(spec.Expression))
	}
	return []byte(b.String())
}

// quoteTOML は比較記号をそのまま読めるTOML文字列へ変換します。
//
// 引数: value(string)。返り値: stringは引用符付き文字列です。
func quoteTOML(value string) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSuffix(buffer.String(), "\n")
}
