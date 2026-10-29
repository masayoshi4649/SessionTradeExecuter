package rules

// coreSpecs はD07の基本枠と残玉管理に使う条件を定義します。
//
// 引数: なし。返り値: []Specは標準式、説明、入力の型を持つ条件です。
// 約定所有権、数量上限、観測の有効性、セッション終端はGo側で管理します。
func coreSpecs() []Spec {
	return []Spec{
		{ID: "core.fixed_r", Kind: "number", Expression: "atr_known ? max(8 * tick, atr) : 8 * tick", Description: "初回の固定R。tickはラージ呼値、atrは直前までのATR。最小呼値未満にはしません。", Env: map[string]any{"atr_known": false, "tick": 0., "atr": 0.}},
		{ID: "core.day_gate", Kind: "bool", Expression: "!own_known || signed_move_r >= -0.25", Description: "昼の新規採否。自市場120秒変化を新規方向へ符号付けしたR比が-0.25以上。", Env: map[string]any{"own_known": false, "signed_move_r": 0.}},
		{ID: "core.peer_fresh", Kind: "bool", Expression: "age_seconds >= 0 && age_seconds <= 15", Description: "夜の他市場情報の経過秒。未来時刻はGo側でも拒否します。", Env: map[string]any{"age_seconds": 0.}},
		{ID: "core.night_gate", Kind: "bool", Expression: "!(own_known && peer_known && own_signed_move < 0 && peer_signed_move < 0)", Description: "夜の新規採否。両市場の120秒変化がともに新規方向へ逆流なら見送ります。", Env: map[string]any{"own_known": false, "peer_known": false, "own_signed_move": 0., "peer_signed_move": 0.}},
		{ID: "core.day_adverse", Kind: "bool", Expression: "signed_120 < 0 && signed_20m < 0 && signed_ma_gap <= -0.1 * fixed_r", Description: "昼の入口反証。120秒と20分が逆行し、現在MAが入口固定MAを0.1R以上下回る場合。売りは上下対称。", Env: map[string]any{"signed_120": 0., "signed_20m": 0., "signed_ma_gap": 0., "fixed_r": 0.}},
		{ID: "core.adverse_hold_seconds", Kind: "number", Expression: "15", Description: "昼の入口反証が連続する必要秒数。無効板と15秒超の受信断ではGo側で連続判定をリセットします。", Env: map[string]any{}},
		{ID: "partial.start", Kind: "bool", Expression: "closed_gain > 0 && (reason == 7 || reason == 30 || reason == 1435)", Description: "P01の開始条件。昼基本枠の実部分返済利益と返済理由から判定。約定前や全返済では開始しません。", Env: map[string]any{"closed_gain": 0., "reason": 0.}},
		{ID: "partial.floor", Kind: "number", Expression: "0.5 * best", Description: "P01の利益下限。実部分利確後に残玉固定平均建値から測った最高利益幅の半分。", Env: map[string]any{"best": 0.}},
		{ID: "partial.exit", Kind: "bool", Expression: "best > 0 && gain <= floor", Description: "P01の返済判定。部分利確後に最高利益があり、現在利益幅が下限以下になれば残玉を返済。", Env: map[string]any{"best": 0., "gain": 0., "floor": 0.}},
		{ID: "residual.width", Kind: "number", Expression: "is_night && is_core ? min(3 * fixed_r, 16 * tick) : 3 * fixed_r", Description: "残り1枚の追尾幅。夜基本枠R05だけ3Rを16ラージ呼値（日経160pt）で上限化。", Env: map[string]any{"is_night": false, "is_core": false, "fixed_r": 0., "tick": 0.}},
		{ID: "residual.eligible_reason", Kind: "bool", Expression: "reason == 2 || reason == 70 || reason == 1437", Description: "残玉化を検討する元の全返済理由。最古の利益玉1枚を候補とします。", Env: map[string]any{"reason": 0.}},
		{ID: "residual.profitable_lot", Kind: "bool", Expression: "gain_yen > 0", Description: "残玉候補1枚の費用前利益が正かを判定。D07の判断用費用は0円です。", Env: map[string]any{"gain_yen": 0.}},
		{ID: "residual.profitable_budget", Kind: "bool", Expression: "cash_yen > 0", Description: "残玉以外の返済見込益、またはその実約定後利益が正かを判定。", Env: map[string]any{"cash_yen": 0.}},
		{ID: "residual.budget", Kind: "number", Expression: "max(0, cash_yen)", Description: "他玉の実返済後に固定する残玉の損失予算。費用前の実現収支を用います。", Env: map[string]any{"cash_yen": 0.}},
		{ID: "residual.exit", Kind: "bool", Expression: "gain <= max(budget_floor, trail_floor)", Description: "残り1枚の返済。実現利益予算による下限と最高利益からの追尾下限を比較します。", Env: map[string]any{"gain": 0., "budget_floor": 0., "trail_floor": 0.}},
		{ID: "residual.quiet_before_seconds", Kind: "number", Expression: "900", Description: "残玉を維持しない米国寄付前の秒数。米国寄付時刻はセッション設定から受け取ります。", Env: map[string]any{}},
		{ID: "residual.quiet_after_seconds", Kind: "number", Expression: "900", Description: "残玉を維持しない米国寄付後の秒数。", Env: map[string]any{}},
	}
}
