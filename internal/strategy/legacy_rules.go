package strategy

import "math"

// legacyBool は従来手法の条件を外部式へ渡し、未接続の人工試験では元条件を使います。
//
// 引数: id(string)、fallback(bool)、env(map[string]any)。返り値: boolは条件の成立です。
func (p *Portfolio) legacyBool(id string, fallback bool, env map[string]any) bool {
	if p.Rules == nil {
		return fallback
	}
	return p.Rules.Bool(id, env)
}

// legacyNumber は従来手法の幅を外部式で計算します。
//
// 引数: id(string)、fallback(float64)、env(map[string]any)。返り値: float64は計算値です。
func (p *Portfolio) legacyNumber(id string, fallback float64, env map[string]any) float64 {
	if p.Rules == nil {
		return fallback
	}
	return p.Rules.Number(id, env)
}

// legacyTrail は実玉の追尾開始・追尾下限・返済を外部条件で判定します。
//
// 引数: pos(*Position)、o(Observation)、arm/width/stop(float64)はR倍率。
// 返り値: intは返済理由。正常板だけを使い、既存下限は緩めません。
func (p *Portfolio) legacyTrail(pos *Position, o Observation, arm, width, stop float64) int {
	if pos.Quantity == 0 || !quoteValid(o) {
		return 0
	}
	gain := float64(pos.Side) * (liquidation(o, pos.Side) - pos.Average)
	pos.Peak = math.Max(pos.Peak, gain)
	pos.Floor = math.Max(pos.Floor, -stop*pos.Risk)
	if p.legacyBool("trail.active", pos.Peak >= arm*pos.Risk, map[string]any{"peak": pos.Peak, "arm": arm, "risk": pos.Risk}) {
		floor := p.legacyNumber("trail.floor", pos.Peak-width*pos.Risk, map[string]any{"peak": pos.Peak, "width": width, "risk": pos.Risk})
		pos.Floor = math.Max(pos.Floor, floor)
	}
	if p.legacyBool("trail.exit", gain <= pos.Floor, map[string]any{"gain": gain, "floor": pos.Floor}) {
		if pos.Floor < 0 {
			return 32
		}
		return 70
	}
	return 0
}

// legacyExtraDirection は補完枠の最初の方向票だけを外部化し、同票時の順序を保ちます。
//
// 引数: h(History)、mid(float64)、style(int)。返り値: intは-1/0/1の方向です。
func (p *Portfolio) legacyExtraDirection(h History, mid float64, style int) int {
	v := h.Values
	score := sign(v[7]) + sign(v[11]) + .5*(sign(v[6])+sign(v[10]))
	if style != 0 {
		score = sign(mid-v[8]) + sign(v[13])
	}
	score = p.legacyNumber("extra.vote", score, map[string]any{"style_zero": style == 0, "long_a": sign(v[7]), "long_b": sign(v[11]), "short_a": sign(v[6]), "short_b": sign(v[10]), "mean_side": sign(mid - v[8]), "slope_side": sign(v[13])})
	if style != 0 && score == 0 {
		score = sign(v[7]) + sign(v[11])
	}
	if score == 0 {
		score = sign(v[7])
	}
	if score == 0 {
		score = sign(mid - v[8])
	}
	return int(sign(score))
}
