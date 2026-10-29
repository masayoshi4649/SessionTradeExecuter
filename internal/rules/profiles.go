package rules

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

var profileDefaults sync.Map

// ProfileNames は配布する既存手法の一覧を返します。
//
// 引数: なし。返り値: []stringは互換用の内部名です。利用者向けにはName/FileNameを使用します。
func ProfileNames() []string {
	return []string{"d164_15", "p164_06", "u165_03", "s01", "p05", "d07", "tg04", "pg04", "t06_topix", "t06_nk225", "t04", "u06"}
}

// Name は銘柄を含む公開手法名を返します。
//
// 引数: profile(string)は内部名。返り値: stringは銘柄_ID、未知なら空文字です。
func Name(profile string) string {
	switch profile {
	case "d164_15":
		return "NK225_D164_15"
	case "p164_06":
		return "NK225_P164_06"
	case "u165_03":
		return "NK225_U165_03"
	case "s01":
		return "NK225_S01"
	case "p05":
		return "NK225_P05"
	case "d07":
		return "NK225_D07"
	case "tg04":
		return "NK225_TG04"
	case "pg04":
		return "NK225_PG04"
	case "t06_nk225":
		return "NK225_T06"
	case "t06_topix":
		return "TOPIX_T06"
	case "t04":
		return "TOPIX_T04"
	case "u06":
		return "NK225_U06"
	}
	return ""
}

// FileName は公開手法名に対応する実ルールのファイル名です。
//
// 引数: profile(string)は内部名。返り値: stringはTOML名、未知なら空文字です。
func FileName(profile string) string {
	if name := Name(profile); name != "" {
		return name + ".toml"
	}
	return ""
}

// Name は読み込んだ条件の公開手法名を返します。
//
// 引数: なし。返り値: stringは銘柄_IDです。
func (s *Set) Name() string {
	if s == nil {
		return ""
	}
	return Name(s.profile)
}

// SpecsFor は手法の変数名と標準式を返します。
//
// 引数: profile(string)。返り値: []Spec、未知名ではnilです。
func SpecsFor(profile string) []Spec {
	var items []Spec
	switch profile {
	case "d164_15":
		items = append(append(Specs(), s01Specs()...), d16415Specs()...)
	case "p164_06":
		items = append(append(Specs(), p05Specs()...), p16406Specs()...)
	case "u165_03":
		items = append(Specs(), u16503Specs()...)
	case "s01":
		items = append(Specs(), s01Specs()...)
	case "p05":
		items = append(Specs(), p05Specs()...)
	case "d07":
		return Specs()
	case "tg04", "pg04":
		items = researchSpecs(profile)
	case "t06_topix", "t06_nk225", "t04", "u06":
		items = legacySpecs(profile)
	default:
		return nil
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

// DefaultFor は標準ルールを手法ごとに一度だけ構成して共有します。
//
// 引数: profile(string)。返り値: *Setとerrorです。
func DefaultFor(profile string) (*Set, error) {
	if profile == "d07" {
		return Default()
	}
	if prior, ok := profileDefaults.Load(profile); ok {
		return prior.(*Set), nil
	}
	expressions := map[string]string{}
	for _, spec := range SpecsFor(profile) {
		expressions[spec.ID] = spec.Expression
	}
	set, err := CompileFor(profile, expressions)
	if err != nil {
		return nil, err
	}
	stored, _ := profileDefaults.LoadOrStore(profile, set)
	return stored.(*Set), nil
}

// Profile は同じ式でも銘柄・昼夜の混用を防ぐ手法名を返します。
//
// 引数: なし。返り値: stringは手法名です。
func (s *Set) Profile() string {
	if s == nil {
		return ""
	}
	return s.profile
}

// SelectProfile は銘柄と昼夜手法IDに対応するルールファイル名を選びます。
//
// 引数: reference/kind/method(string)。返り値: stringとerrorです。
func SelectProfile(reference, kind, method string) (string, error) {
	if strings.HasPrefix(method, "NK225_") || strings.HasPrefix(method, "TOPIX_") {
		prefix, id, _ := strings.Cut(method, "_")
		if prefix != reference {
			return "", fmt.Errorf("ルール銘柄%sを%sへ適用できません", prefix, reference)
		}
		method = id
	}
	if reference == "NK225" && (kind == "Day" || kind == "Night") {
		if method == "D07" || method == "TG04" || method == "PG04" {
			return strings.ToLower(method), nil
		}
	}
	switch reference + "/" + kind + "/" + method {
	case "NK225/Day/D164_15":
		return "d164_15", nil
	case "NK225/Night/P164_06":
		return "p164_06", nil
	case "NK225/Night/U165_03":
		return "u165_03", nil
	case "NK225/Day/S01":
		return "s01", nil
	case "TOPIX/Day/T06":
		return "t06_topix", nil
	case "NK225/Day/T06":
		return "t06_nk225", nil
	case "TOPIX/Night/T04":
		return "t04", nil
	case "NK225/Night/P05":
		return "p05", nil
	case "NK225/Night/U06":
		return "u06", nil
	}
	return "", fmt.Errorf("%s/%s/%sのルールはありません", reference, kind, method)
}
