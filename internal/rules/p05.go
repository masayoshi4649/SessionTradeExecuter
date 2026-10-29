package rules

// p05Specs は独立B04入口と実玉専用の利益追尾を定義します。
//
// 引数: なし。返り値: []Spec はD07共通式へ追加する条件です。
func p05Specs() []Spec {
	return []Spec{
		{"p05.core_efficiency", "仮想主担当の方向効率の下限。勝率ではありません", "0.25", "number", map[string]any{}},
		{"p05.core_return_r", "仮想主担当の突破後に許容する戻り幅のR倍率", "0.40", "number", map[string]any{}},
		{"p05.core_bounce_r", "仮想主担当の押戻り後に要求する反発幅のR倍率", "0.125", "number", map[string]any{}},
		{"p05.profit_active", "実玉の返済側気配で40pt以上の最高益に達したら追尾を開始", "best >= 40", "bool", map[string]any{"best": float64(0)}},
		{"p05.profit_exit", "最高益から10pt戻り、判断時にも利益がある実玉だけ全返済。次約定の損失は除外しません", "gain > 0 && best - gain >= 10", "bool", map[string]any{"gain": float64(0), "best": float64(0)}},
	}
}
