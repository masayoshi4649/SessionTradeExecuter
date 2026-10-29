package rules

// s01Specs は日中D07の波担当へ加える親利益保護と仮想基準の占有判定です。
//
// 引数: なし。
// 返り値: []SpecはD07共通式へ追加する条件です。主担当・追加担当の出口は変更しません。
// parent_valueは同じ親の確定損益と返済側気配による含み損益の合計です。
func s01Specs() []Spec {
	return []Spec{
		{"s01.guard_active", "波担当の親総時価利益が初回枚数×固定R×0.5に到達すると保護を開始。部分返済・追加で初期化しません", "parent_value >= 0.5 * risk * initial_quantity * point_value", "bool", map[string]any{"parent_value": float64(0), "risk": float64(0), "initial_quantity": float64(0), "point_value": float64(0)}},
		{"s01.guard_floor", "保護開始後の親総時価利益の最高値の50%を返済判断の床にします。次の実約定で同額を保証する式ではありません", "best_value * 0.5", "number", map[string]any{"best_value": float64(0)}},
		{"s01.entry_allowed", "独立して動く元D07の波担当が別の親を保有・予約中なら新しい波親を採用しません。一度拒否した親は後追いしません", "!baseline_occupied || same_campaign", "bool", map[string]any{"baseline_occupied": false, "same_campaign": false}},
	}
}
