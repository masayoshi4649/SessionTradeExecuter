package strategy

import "fmt"

// ParametersForMethod は銘柄・昼夜に対応する研究手法IDから設定一式を選びます。
//
// 引数: reference(string) はラージ参照市場、kind(string) は Day/Night、id(string) は研究手法IDです。
// 返り値: Parameters は主担当と未参加補完を含む設定一式、error は未実装または用途が異なるIDです。
func ParametersForMethod(reference, kind, id string) (Parameters, error) {
	if reference == "NK225" && ((kind == "Day" && id == "D164_15") || (kind == "Night" && (id == "P164_06" || id == "U165_03"))) {
		p := DefaultParameters(reference, kind)
		p.Method = id
		return p, nil
	}
	if reference == "NK225" && kind == "Day" && id == "S01" {
		p := DefaultParameters(reference, kind)
		p.Method = id
		return p, nil
	}
	if reference == "NK225" && kind == "Night" && id == "P05" {
		p := DefaultParameters(reference, kind)
		p.Method = id
		return p, nil
	}
	if reference == "NK225" && (kind == "Day" || kind == "Night") && (id == "TG04" || id == "PG04" || id == "D07") {
		p := DefaultParameters(reference, kind)
		p.Method = id
		return p, nil
	}
	switch reference + "/" + kind + "/" + id {
	case "TOPIX/Day/T06", "NK225/Day/T06":
		// 00131: TOPIX は DV07、日経は DV05 の主担当に T06 の未参加補完を組み合わせます。
		return DefaultParameters(reference, kind), nil
	case "TOPIX/Night/T04":
		// 00131: NV01 の主担当と T04 の未参加補完です。
		return DefaultParameters(reference, kind), nil
	case "NK225/Night/U06":
		// 00131: NV25 の主担当と、枚数予算を持つ U06 の未参加補完です。
		return DefaultParameters(reference, kind), nil
	default:
		return Parameters{}, fmt.Errorf("手法ID %q は %s/%s では使用できません。対応は日中T06、TOPIX夜間T04、日経夜間U06、日経の昼夜D07/TG04/PG04です", id, reference, kind)
	}
}
