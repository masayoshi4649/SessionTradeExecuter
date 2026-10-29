package rules

// legacySpecs はT06・T04・U06で実際に使用する入口・継続・出口条件を返します。
//
// 引数: profile(string)はt06_topix/t06_nk225/t04/u06。返り値: []Specは型付き標準条件です。
// threshold等の入力は手法選択済みParametersから受け取り、銘柄別の標準差を維持します。
func legacySpecs(profile string) []Spec {
	common := []Spec{
		{ID: "trail.active", Kind: "bool", Expression: "peak >= arm * risk", Description: "実玉の最高利益が開始倍率×初回Rに届けば利益追尾開始。", Env: map[string]any{"peak": 0., "arm": 0., "risk": 0.}},
		{ID: "trail.floor", Kind: "number", Expression: "peak - width * risk", Description: "実玉の利益追尾下限。元の下限より緩めることは実行側で禁止。", Env: map[string]any{"peak": 0., "width": 0., "risk": 0.}},
		{ID: "trail.exit", Kind: "bool", Expression: "gain <= floor", Description: "現在利益幅が実玉の停止下限以下なら返済。", Env: map[string]any{"gain": 0., "floor": 0.}},
		{ID: "extra.risk", Kind: "number", Expression: "max(atr, max(band_fraction * band_width, minimum_ticks * tick))", Description: "主担当未参加の補完枠リスク幅。ATR、過去帯幅、最小呼値幅から計算。", Env: map[string]any{"atr": 0., "band_fraction": 0., "band_width": 0., "minimum_ticks": 0., "tick": 0.}},
		{ID: "extra.vote", Kind: "number", Expression: "style_zero ? long_a + long_b + 0.5 * (short_a + short_b) : mean_side + slope_side", Description: "補完の方向票。各入力は-1/0/1の確定票。票ゼロ時の補完順序はGo側で固定。", Env: map[string]any{"style_zero": false, "long_a": 0., "long_b": 0., "short_a": 0., "short_b": 0., "mean_side": 0., "slope_side": 0.}},
		{ID: "extra.quote", Kind: "bool", Expression: "spread <= spread_r * risk", Description: "補完枠の気配差が許容R倍率以内かを確認。", Env: map[string]any{"spread": 0., "spread_r": 0., "risk": 0.}},
	}
	if profile == "t06_topix" || profile == "t06_nk225" {
		return append(common, []Spec{
			{ID: "day.direction_agrees", Kind: "bool", Expression: "direction == gate", Description: "日中のE01方向と独立確認方向が一致した場合に入口準備へ進む。", Env: map[string]any{"direction": 0., "gate": 0.}},
			{ID: "day.entry_quote", Kind: "bool", Expression: "spread <= spread_atr * atr && signed_distance <= distance_atr * atr", Description: "日中の新規気配差と、直近価格から発注側気配への距離をATRで確認。", Env: map[string]any{"spread": 0., "spread_atr": 0., "atr": 0., "signed_distance": 0., "distance_atr": 0.}},
			{ID: "day.entry_support", Kind: "bool", Expression: "own30 >= threshold && peer30 >= threshold", Description: "新規方向への二市場30秒変化が弱い逆票の許容値以上かを確認。", Env: map[string]any{"own30": 0., "peer30": 0., "threshold": 0.}},
			{ID: "day.risk_fraction", Kind: "number", Expression: "max(minimum, min(1, ratio))", Description: "現在ATR÷初回ATRで損失予算を縮小。0〜1以内に制限し、以前より広げない。", Env: map[string]any{"minimum": 0., "ratio": 0.}},
			{ID: "day.loss", Kind: "bool", Expression: "equity <= -initial_quantity * risk * fraction", Description: "実現値幅と残玉評価値幅の合計が初回全枚数の損失予算以下なら返済。", Env: map[string]any{"equity": 0., "initial_quantity": 0., "risk": 0., "fraction": 0.}},
			{ID: "day.adverse", Kind: "bool", Expression: "own <= -threshold && peer <= -threshold", Description: "実約定後の固定アンカーから両市場が逆行したかを確認。", Env: map[string]any{"own": 0., "peer": 0., "threshold": 0.}},
			{ID: "day.adverse_confirm", Kind: "bool", Expression: "elapsed >= threshold", Description: "二市場の逆行が手法指定秒数以上持続したら返済。日経60秒、TOPIX120秒が標準。", Env: map[string]any{"elapsed": 0., "threshold": 0.}},
			{ID: "shadow.entry_distance", Kind: "number", Expression: "entry_atr * atr", Description: "日中の追跡新規。追跡極値から反発を待つ距離。標準0.25ATR。", Env: map[string]any{"entry_atr": 0., "atr": 0.}},
			{ID: "shadow.risk_factor", Kind: "number", Expression: "tight ? tight_risk : normal_risk", Description: "日中E01の組全体損失予算。通常影1ATR、早期出口影0.75ATRが標準。", Env: map[string]any{"tight": false, "tight_risk": 0., "normal_risk": 0.}},
			{ID: "shadow.trail_active", Kind: "bool", Expression: "gain >= arm_atr * atr", Description: "日中E01の最高順行幅が標準1.5ATRに到達したら追尾開始。", Env: map[string]any{"gain": 0., "arm_atr": 0., "atr": 0.}},
			{ID: "shadow.trail_width", Kind: "number", Expression: "tight && gain >= tight_arm * atr ? tight_width : normal_width", Description: "日中E01の追尾ATR倍率。標準3、早期出口影で3ATR順行後は0.75。", Env: map[string]any{"tight": false, "gain": 0., "tight_arm": 0., "atr": 0., "tight_width": 0., "normal_width": 0.}},
			{ID: "shadow.split_distance", Kind: "number", Expression: "split_atr * atr", Description: "日中E01の半数利確幅。初回ATRを固定し、標準2ATRに達した時の次観測で執行。", Env: map[string]any{"split_atr": 0., "atr": 0.}},
			{ID: "shadow.cooldown", Kind: "bool", Expression: "elapsed >= threshold", Description: "日中E01の全返済後の再準備待機。未返済・未約定の管理とは分離。", Env: map[string]any{"elapsed": 0., "threshold": 0.}},
			{ID: "shadow.warmup", Kind: "bool", Expression: "elapsed >= threshold", Description: "日中E01の開始後準備時間。標準1200秒。", Env: map[string]any{"elapsed": 0., "threshold": 0.}},
			{ID: "shadow.hold_expired", Kind: "bool", Expression: "elapsed >= threshold", Description: "日中E01の最大保有時間。場末強制返済はこの式にかかわらず優先。", Env: map[string]any{"elapsed": 0., "threshold": 0.}},
		}...)
	}
	if profile == "t04" || profile == "u06" {
		return append(common, []Spec{
			{ID: "night.entry_quote", Kind: "bool", Expression: "spread <= spread_r * risk && distance <= distance_r * risk", Description: "夜間の入口気配差・基準価格からの気配距離を固定Rで確認。", Env: map[string]any{"spread": 0., "spread_r": 0., "risk": 0., "distance": 0., "distance_r": 0.}},
			{ID: "night.efficiency", Kind: "bool", Expression: "efficiency >= threshold", Description: "夜間の突破準備に必要な方向効率。", Env: map[string]any{"efficiency": 0., "threshold": 0.}},
			{ID: "night.entry_direction", Kind: "number", Expression: "price > high && own > 0 && peer > 0 ? 1.0 : (price < low && own < 0 && peer < 0 ? -1.0 : 0.0)", Description: "過去の高値・安値突破と二市場の方向一致で候補を決める。-1/0/1以外は採用しない。", Env: map[string]any{"price": 0., "high": 0., "low": 0., "own": 0., "peer": 0.}},
			{ID: "night.support30", Kind: "bool", Expression: "own > 0 && peer > 0", Description: "候補方向への両市場30秒変化がともに正なら支持。", Env: map[string]any{"own": 0., "peer": 0.}},
			{ID: "night.progress", Kind: "bool", Expression: "elapsed >= confirm_seconds && advance >= progress_r * risk", Description: "T04は準備時間と固定境界からの順行幅を確認。", Env: map[string]any{"elapsed": 0., "confirm_seconds": 0., "advance": 0., "progress_r": 0., "risk": 0.}},
			{ID: "night.retest_progress", Kind: "bool", Expression: "advance >= progress_r * risk", Description: "U06は固定突破境界から標準0.75R順行した後に押し戻りを待つ。", Env: map[string]any{"advance": 0., "progress_r": 0., "risk": 0.}},
			{ID: "night.retest_return", Kind: "bool", Expression: "advance <= return_r * risk", Description: "U06は順行を確認した後、境界から標準0.25R以下へ戻るのを待つ。", Env: map[string]any{"advance": 0., "return_r": 0., "risk": 0.}},
			{ID: "night.retest_bounce", Kind: "bool", Expression: "elapsed >= confirm_seconds && rebound >= bounce_r * risk", Description: "U06は押し戻り極値から再反発してから新規。段階を同一行で飛ばさない。", Env: map[string]any{"elapsed": 0., "confirm_seconds": 0., "rebound": 0., "bounce_r": 0., "risk": 0.}},
			{ID: "night.support120", Kind: "bool", Expression: "own > threshold && peer > threshold", Description: "T04の入口直前に両市場120秒変化が支持閾値を超えることを確認。", Env: map[string]any{"own": 0., "peer": 0., "threshold": 0.}},
			{ID: "night.direction_still", Kind: "bool", Expression: "own > 0 && peer > 0", Description: "入口時にも過去帯の二市場方向が候補方向へ継続しているかを確認。", Env: map[string]any{"own": 0., "peer": 0.}},
			{ID: "night.opposition120", Kind: "bool", Expression: "own < -threshold && peer < -threshold", Description: "U06は両市場120秒変化が逆流閾値を超えた入口を取消す。", Env: map[string]any{"own": 0., "peer": 0., "threshold": 0.}},
			{ID: "night.boundary_exit", Kind: "bool", Expression: "distance < -break_r * risk && direction < 0", Description: "夜間の保有後、固定突破境界が破れ相手方向も逆行したら返済。", Env: map[string]any{"distance": 0., "break_r": 0., "risk": 0., "direction": 0.}},
		}...)
	}
	return nil
}
