package rules

// addonSpecs はD07の昼V05・夜V16と共通入口の判断式を定義します。
//
// 引数: なし。返り値: []Specは式、日本語説明、入力型の一覧です。
func addonSpecs() []Spec {
	return []Spec{
		{ID: "addon.exit_unit", Description: "初回判断時の利益単位U。ATR未取得なら元のRを使い、取得済みなら8ティック以上・元R以下でATRの1.5倍。", Kind: "number", Expression: "atr_known ? max(8 * tick, min(risk, 1.5 * atr)) : risk", Env: map[string]any{"atr_known": false, "tick": float64(0), "risk": float64(0), "atr": float64(0)}},
		{ID: "addon.cooldown", Description: "全返済後の再準備秒数。夜の利益返済60秒、連敗2回以上900秒、それ以外120秒。", Kind: "number", Expression: "night && profitable ? 60.0 : (losses >= 2 ? 900.0 : 120.0)", Env: map[string]any{"night": false, "profitable": false, "losses": float64(0)}},
		{ID: "addon.trail_active", Description: "最高順行幅が初回固定Uへ届いたら利益追尾を開始。", Kind: "bool", Expression: "best >= unit", Env: map[string]any{"best": float64(0), "unit": float64(0)}},
		{ID: "addon.trail_width", Description: "利益追尾の戻り許容幅。昼は1.5U、夜は0.75U。停止価格を緩める処理は実行側で禁止。", Kind: "number", Expression: "(night ? 0.75 : 1.5) * unit", Env: map[string]any{"night": false, "unit": float64(0)}},
		{ID: "addon.exit_trigger", Description: "現在順行幅が停止幅以下なら返済。初期損切りと利益追尾を同じ停止幅で判定。", Kind: "bool", Expression: "gain <= floor", Env: map[string]any{"gain": float64(0), "floor": float64(0)}},
		{ID: "addon.adverse", Description: "保有方向に対して両市場の30秒変化が-0.1R以下、120秒変化も負なら悪化監視。", Kind: "bool", Expression: "own30 <= -0.1 && peer30 <= -0.1 && own120 < 0 && peer120 < 0", Env: map[string]any{"own30": float64(0), "peer30": float64(0), "own120": float64(0), "peer120": float64(0)}},
		{ID: "addon.adverse_confirm", Description: "二市場の逆行が連続15秒以上続いたか。データ中断で連続時計を初期化。", Kind: "bool", Expression: "elapsed >= 15", Env: map[string]any{"elapsed": float64(0)}},
		{ID: "addon.opposite", Description: "両市場の5分・20分・40分がすべて保有方向と逆なら全返済。", Kind: "bool", Expression: "own5 < 0 && peer5 < 0 && own20 < 0 && peer20 < 0 && own40 < 0 && peer40 < 0", Env: map[string]any{"own5": float64(0), "peer5": float64(0), "own20": float64(0), "peer20": float64(0), "own40": float64(0), "peer40": float64(0)}},
		{ID: "addon.support", Description: "20分・40分それぞれで両市場が逆行せず、少なくとも片方が順行なら方向支持を残す。", Kind: "bool", Expression: "own20 >= 0 && peer20 >= 0 && (own20 > 0 || peer20 > 0) && own40 >= 0 && peer40 >= 0 && (own40 > 0 || peer40 > 0)", Env: map[string]any{"own20": float64(0), "peer20": float64(0), "own40": float64(0), "peer40": float64(0)}},
		{ID: "addon.partial_profit", Description: "昼の最初の半数利確、又は回転玉1枚の利確は順行幅1Uで開始。利益がある所有玉だけを返済。", Kind: "bool", Expression: "gain >= unit", Env: map[string]any{"gain": float64(0), "unit": float64(0)}},
		{ID: "addon.pullback", Description: "前回の利確価格から0.75U以上押し戻されたら、再参加の反発待ちへ移る。", Kind: "bool", Expression: "distance >= 0.75 * unit", Env: map[string]any{"distance": float64(0), "unit": float64(0)}},
		{ID: "addon.rebound", Description: "押し戻り極値から0.25U以上反発し、前回利確価格より不利でなければ再参加候補。", Kind: "bool", Expression: "rebound >= 0.25 * unit && price_change <= 0", Env: map[string]any{"rebound": float64(0), "unit": float64(0), "price_change": float64(0)}},
		{ID: "addon.self_support", Description: "再参加方向に自銘柄の30秒・120秒変化がともに正かを確認。", Kind: "bool", Expression: "own30 > 0 && own120 > 0", Env: map[string]any{"own30": float64(0), "own120": float64(0)}},
		{ID: "addon.rotation_ready", Description: "昼の再参加条件が15秒連続し、元の損失予算以内かつ気配差0.25U以内なら回転玉を追加。", Kind: "bool", Expression: "elapsed >= 15 && exposure <= budget && spread <= 0.25 * unit", Env: map[string]any{"elapsed": float64(0), "exposure": float64(0), "budget": float64(0), "spread": float64(0), "unit": float64(0)}},
		{ID: "addon.add_trigger", Description: "通常追撃は自銘柄の30秒が正・相手が非負で、追加段数に応じ0.5Rずつ進み、前回追加後30秒以上。", Kind: "bool", Expression: "own30 > 0 && peer30 >= 0 && gain >= 0.5 * next_ticket * risk && elapsed >= 30", Env: map[string]any{"own30": float64(0), "peer30": float64(0), "gain": float64(0), "next_ticket": float64(0), "risk": float64(0), "elapsed": float64(0)}},
		{ID: "addon.add_ready", Description: "通常追撃の損失予算と気配差を確認。夜は利益保護後だけを実行側でも要求。", Kind: "bool", Expression: "exposure <= budget && spread <= 0.25 * risk", Env: map[string]any{"exposure": float64(0), "budget": float64(0), "spread": float64(0), "risk": float64(0)}},
		{ID: "addon.flip_direction", Description: "夜のドテン候補は自銘柄の5分・120秒・30秒・40分が新方向に正で、価格も20分平均の新方向側。", Kind: "bool", Expression: "own5 > 0 && own120 > 0 && own30 > 0 && price_ma > 0 && own40 > 0", Env: map[string]any{"own5": float64(0), "own120": float64(0), "own30": float64(0), "price_ma": float64(0), "own40": float64(0)}},
		{ID: "addon.reentry_ready", Description: "夜の再参加又はドテンが15秒連続し、気配差が0.25R以内なら新規候補を確定。", Kind: "bool", Expression: "elapsed >= 15 && spread <= 0.25 * risk", Env: map[string]any{"elapsed": float64(0), "spread": float64(0), "risk": float64(0)}},
		// ----------------------------------------
		{ID: "addon.entry.spread_reject", Description: "通常入口は気配差が0.25Rを超えたら準備を解除。", Kind: "bool", Expression: "spread > 0.25 * risk", Env: map[string]any{"spread": float64(0), "risk": float64(0)}},
		{ID: "addon.entry.mode", Description: "40分の方向効率が0.35以上なら突破型1、0.2以下なら往復型4、それ以外は準備しない-1。", Kind: "number", Expression: "efficiency >= 0.35 ? 1.0 : (efficiency <= 0.2 ? 4.0 : -1.0)", Env: map[string]any{"efficiency": float64(0)}},
		{ID: "addon.entry.expired", Description: "通常入口の準備が20分を超えた、又は選択型が変わった場合に準備を破棄。", Kind: "bool", Expression: "elapsed > 1200 || changed", Env: map[string]any{"elapsed": float64(0), "changed": false}},
		{ID: "addon.entry.breakout_side", Description: "突破型の方向候補。方向効率0.25以上かつ40分高値を0.05R超えて上抜けなら買い、安値を下抜けなら売り。", Kind: "number", Expression: "efficiency >= 0.25 ? (mid > high + 0.05 * risk ? 1.0 : (mid < low - 0.05 * risk ? -1.0 : 0.0)) : 0.0", Env: map[string]any{"efficiency": float64(0), "mid": float64(0), "high": float64(0), "low": float64(0), "risk": float64(0)}},
		{ID: "addon.entry.range_side", Description: "往復型の方向候補。方向効率0.3以下かつ40分安値の0.1R以内で買い、高値の0.1R以内で売り。", Kind: "number", Expression: "efficiency <= 0.3 ? (mid <= low + 0.1 * risk ? 1.0 : (mid >= high - 0.1 * risk ? -1.0 : 0.0)) : 0.0", Env: map[string]any{"efficiency": float64(0), "mid": float64(0), "high": float64(0), "low": float64(0), "risk": float64(0)}},
		{ID: "addon.entry.support30", Description: "通常入口の30秒方向支持。自銘柄と相手市場が候補方向に逆行していないこと。", Kind: "bool", Expression: "own30 >= 0 && peer30 >= 0", Env: map[string]any{"own30": float64(0), "peer30": float64(0)}},
		{ID: "addon.entry.breakout_failed", Description: "突破境界から0.5Rを超えて逆行したら突破型準備を解除。", Kind: "bool", Expression: "distance < -0.5 * risk", Env: map[string]any{"distance": float64(0), "risk": float64(0)}},
		{ID: "addon.entry.breakout_advanced", Description: "突破境界から0.5R以上順行したら押し戻りを待つ。", Kind: "bool", Expression: "distance >= 0.5 * risk", Env: map[string]any{"distance": float64(0), "risk": float64(0)}},
		{ID: "addon.entry.breakout_retested", Description: "突破境界から0.25R以下へ戻ったら再反発の確認へ移る。", Kind: "bool", Expression: "distance <= 0.25 * risk", Env: map[string]any{"distance": float64(0), "risk": float64(0)}},
		{ID: "addon.entry.rebound", Description: "入口の極値から0.25R反発したことを確認。", Kind: "bool", Expression: "distance >= 0.25 * risk", Env: map[string]any{"distance": float64(0), "risk": float64(0)}},
		{ID: "addon.entry.range_pullback", Description: "往復型の第一反発後、頂点から0.125Rの押し戻りを確認。", Kind: "bool", Expression: "distance >= 0.125 * risk", Env: map[string]any{"distance": float64(0), "risk": float64(0)}},
		{ID: "addon.entry.higher_low_failed", Description: "往復型の二回目極値が最初の極値より2ティック以上有利でなければ第一段階へ戻す。", Kind: "bool", Expression: "distance < 2 * tick", Env: map[string]any{"distance": float64(0), "tick": float64(0)}},
		{ID: "addon.entry.range_rebound", Description: "往復型の二回目極値から0.125R反発し、最初の境界の候補方向側なら入口候補。", Kind: "bool", Expression: "rebound >= 0.125 * risk && boundary_distance >= 0", Env: map[string]any{"rebound": float64(0), "risk": float64(0), "boundary_distance": float64(0)}},
		{ID: "addon.entry.support120", Description: "往復型は両市場の120秒変化も候補方向へ逆行していないことを要求。", Kind: "bool", Expression: "own120 >= 0 && peer120 >= 0", Env: map[string]any{"own120": float64(0), "peer120": float64(0)}},
		{ID: "addon.entry.range_failed", Description: "往復型は境界から0.75Rを超えて逆行したら準備を解除。", Kind: "bool", Expression: "distance < -0.75 * risk", Env: map[string]any{"distance": float64(0), "risk": float64(0)}},
		{ID: "addon.entry.confirmed", Description: "通常入口の価格条件と二市場支持が30秒連続してから初回注文。", Kind: "bool", Expression: "elapsed >= 30", Env: map[string]any{"elapsed": float64(0)}},
	}
}
