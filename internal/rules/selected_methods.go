package rules

// d16415Specs は日中D164_15の短期確認と実所有玉の保護条件を返します。
//
// 引数: なし。返り値: []SpecはS01共通条件へ追加する専用式です。
// 数量の整数配分・予約・所有ID・延期30秒の管理は戦略状態で検査します。
func d16415Specs() []Spec {
	return []Spec{
		{"d164_15.delay_initial", "初回の15秒方向が順行でも5秒速度が半分以下なら最大30秒待機。未知は元入口を維持します", "short_known && long_known && side * speed15 > 0 && side * speed5 <= 0.5 * side * speed15", "bool", map[string]any{"short_known": false, "long_known": false, "side": float64(0), "speed15": float64(0), "speed5": float64(0)}},
		{"d164_15.release_initial", "待機した初回は5秒が順行し15秒速度以上へ戻ったとき解除。元希望枚数と生存する源数量が上限です", "short_known && long_known && side * speed5 > 0 && side * speed15 >= 0 && side * speed5 >= side * speed15", "bool", map[string]any{"short_known": false, "long_known": false, "side": float64(0), "speed15": float64(0), "speed5": float64(0)}},
		{"d164_15.confirm_add", "源の追撃は5秒・15秒の価格変化が両方順行するときだけ採用。未知は追撃を見送ります", "short_known && long_known && side * move5 > 0 && side * move15 > 0", "bool", map[string]any{"short_known": false, "long_known": false, "side": float64(0), "move5": float64(0), "move15": float64(0)}},
		{"d164_15.arm_residual", "実約定で利益のある源部分返済後の残玉だけ、実建値から10円順行で保護を開始します", "residual_eligible && gain >= 10", "bool", map[string]any{"residual_eligible": false, "gain": float64(0)}},
		{"d164_15.exit_residual", "保護開始した残玉の返済側気配が実建値まで戻ったら所有IDで返済。次の約定利益は保証しません", "residual_active && gain <= 0", "bool", map[string]any{"residual_active": false, "gain": float64(0)}},
		{"d164_15.stop_add", "源追撃玉は実建値から50円逆行したら、その所有IDを返済します", "source_add && gain <= -50", "bool", map[string]any{"source_add": false, "gain": float64(0)}},
	}
}

// p16406Specs は米国前P164_06の少量初回・利益残玉・追撃の条件を返します。
//
// 引数: なし。返り値: []SpecはP05共通条件へ追加する専用式です。
// initial_microは片側100micro基準へ正規化した初回量、gainは実建値から返済気配までの円幅です。
func p16406Specs() []Spec {
	return []Spec{
		{"selected.p164_06.small_initial", "片側100micro基準で20micro以下の初回・延期初回を保護対象にします", "initial_micro <= 20 && is_initial", "bool", map[string]any{"initial_micro": float64(0), "is_initial": false}},
		{"selected.p164_06.activate", "対象の実所有玉が10円順行したら利益保護を開始します", "gain >= 10", "bool", map[string]any{"gain": float64(0)}},
		{"selected.p164_06.trail_floor", "最高順行幅から20円戻り、最低でも実建値を返済判断の床にします", "max(0.0, best_gain - 20.0)", "number", map[string]any{"best_gain": float64(0)}},
		{"selected.p164_06.add_stop", "源追撃玉は実建値から50円逆行したら所有IDで返済します", "source_add && gain <= -50", "bool", map[string]any{"source_add": false, "gain": float64(0)}},
	}
}

// u16503Specs は米国後U165_03の反対板による初回待機条件を返します。
//
// 引数: なし。返り値: []SpecはD07共通条件へ追加する専用式です。
// 板状態は日経ラージの原行順で確定し、数量の減少を約定又は取消とは断定しません。
func u16503Specs() []Spec {
	return []Spec{
		{"selected.u165_03.defer", "反対方向へ確定した価格帯に表示数量の支持があるとき、初回だけ待機します。未知は元入口を維持します", "known && opposite_active && opposite_quantity_support", "bool", map[string]any{"known": false, "opposite_active": false, "opposite_quantity_support": false}},
		{"selected.u165_03.timeout", "価格時計の待機が30秒を超えた親は取消。元の仮想源は独立して継続します", "elapsed > 30", "bool", map[string]any{"elapsed": float64(0)}},
		{"selected.u165_03.release", "待機開始後の原行で自方向の新しい価格帯が確定したら、生存する源数量の範囲で初回を再開します", "known && same_active && same_index > deferred_index", "bool", map[string]any{"known": false, "same_active": false, "same_index": float64(0), "deferred_index": float64(0)}},
	}
}
