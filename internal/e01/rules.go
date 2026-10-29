package e01

// ruleBool は接続済み従来手法プロファイルの真偽条件を評価します。
//
// 引数: id(string)、fallback(bool)、env(map[string]any)。返り値: boolは条件成立です。
// E01単独利用ではRules=nilのまま、元の保存条件を使用します。
func (e *Engine) ruleBool(id string, fallback bool, env map[string]any) bool {
	if e.Rules == nil {
		return fallback
	}
	return e.Rules.Bool(id, env)
}

// ruleNumber は接続済み従来手法プロファイルの幅を計算します。
//
// 引数: id(string)、fallback(float64)、env(map[string]any)。返り値: float64は計算値です。
func (e *Engine) ruleNumber(id string, fallback float64, env map[string]any) float64 {
	if e.Rules == nil {
		return fallback
	}
	return e.Rules.Number(id, env)
}

// ruleEntryDistance は追跡極値から新規を待つ反発幅を計算します。
//
// 引数: atr(float64)は準備開始時に固定したATR。返り値: float64は価格幅です。
func (e *Engine) ruleEntryDistance(atr float64) float64 {
	return e.ruleNumber("shadow.entry_distance", e.Config.EntryATR*atr, map[string]any{"entry_atr": e.Config.EntryATR, "atr": atr})
}

// RuleTrailWidth は出口影と出口診断が使うATR倍率を計算します。
//
// 引数: tight(bool)は早期出口影、gain/atr(float64)は最高順行幅と初回ATR。
// 返り値: float64は追尾幅のATR倍率です。
func (e *Engine) RuleTrailWidth(tight bool, gain, atr float64) float64 {
	width := e.Config.NormalTrailATR
	if tight && gain >= e.Config.TightArmATR*atr {
		width = e.Config.TightTrailATR
	}
	return e.ruleNumber("shadow.trail_width", width, map[string]any{"tight": tight, "gain": gain, "tight_arm": e.Config.TightArmATR, "atr": atr, "tight_width": e.Config.TightTrailATR, "normal_width": e.Config.NormalTrailATR})
}

// RuleSplitDistance は出口影と出口診断が使う部分利確幅を計算します。
//
// 引数: atr(float64)は初回ATR。返り値: float64は平均建値からの価格幅です。
func (e *Engine) RuleSplitDistance(atr float64) float64 {
	return e.ruleNumber("shadow.split_distance", e.Config.SplitATR*atr, map[string]any{"split_atr": e.Config.SplitATR, "atr": atr})
}
