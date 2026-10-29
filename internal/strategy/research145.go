package strategy

import "math"

// ResearchPartial145 は実際の黒字部分返済後だけ開始する残玉の利益保護です。
// Anchorは開始時の残玉平均建値で固定し、それ以前の高値をBestへ持ち込みません。
type ResearchPartial145 struct {
	Anchor    float64
	Best      float64
	StartedAt float64
}

// researchPolicyFee は手法の判断用費用と、約定台帳で記録する実費用を区別します。
//
// 引数: なし。返り値: float64は判断上の片道1枚費用。D07は研究00145の0円条件です。
func (p *Portfolio) researchPolicyFee() float64 {
	if p.Research.Method == "D07" {
		return 0
	}
	return researchFee(p.Config.Product)
}

// researchCash は利益予算へ使う会計を手法ごとに取り出します。
//
// 引数: s(*ResearchSleeve)は実約定会計。返り値: float64はD07だけ費用控除前、それ以外は費用後です。
// CashとFees自体は変更せず、運転画面や約定記録では実際の費用を保持します。
func (p *Portfolio) researchCash(s *ResearchSleeve) float64 {
	if p.Research.Method == "D07" {
		return s.Cash + s.Fees
	}
	return s.Cash
}

// researchResidualWidth は利益予算で残した1枚の追尾幅を返します。
//
// 引数: s(*ResearchSleeve)は担当状態。返り値: float64は幅、boolはD07夜間基本の上限適用です。
func (p *Portfolio) researchResidualWidth(s *ResearchSleeve) (float64, bool) {
	width := 3 * s.FixedR
	if p.Research.Method == "D07" {
		adjusted := p.Rules.Number("residual.width", map[string]any{"is_night": p.Session.Kind == "Night", "is_core": s.Name == "core", "fixed_r": s.FixedR, "tick": p.Config.LargeTick})
		return adjusted, adjusted < width
	}
	return p.researchRuleNumber("residual.width", width, map[string]any{"risk": s.FixedR}), false
}

// researchFixedR145 は初回リスク幅を外部式で計算し、呼値未満の分母を防ぎます。
//
// 引数: o(Observation)は現在までの指標。返り値: float64は入口で固定する正の幅です。
func (p *Portfolio) researchFixedR145(o Observation) float64 {
	known := finite(o.ATR) && o.ATR > 0
	atr := 0.
	if known {
		atr = o.ATR
	}
	return math.Max(p.Config.LargeTick, p.Rules.Number("core.fixed_r", map[string]any{"atr_known": known, "atr": atr, "tick": p.Config.LargeTick}))
}

// researchGate145 は昼夜別の入口確認を現在までに既知の変化だけで行います。
//
// 引数: o(Observation)、side(int)、r(float64)。返り値: boolは基本枠の採用可否です。
// 未知の値は0と既知フラグを渡し、未知値を方向の根拠にしません。
func (p *Portfolio) researchGate145(o Observation, side int, r float64) bool {
	f := o.Research
	own := f.Known[13] && f.Known[9] && f.Values[9] > 0
	ownMove, moveR := 0., 0.
	if own {
		ownMove = float64(side) * f.Values[13]
		moveR = ownMove * f.Values[9] / r
	}
	if p.Session.Kind == "Day" {
		return p.Rules.Bool("core.day_gate", map[string]any{"own_known": own, "signed_move_r": moveR})
	}
	peer, peerMove := false, 0.
	if f.Known[21] && f.Known[22] {
		age := o.Time - f.Values[22]
		peer = age >= 0 && p.Rules.Bool("core.peer_fresh", map[string]any{"age_seconds": age})
		if peer {
			peerMove = float64(side) * f.Values[21]
		}
	}
	return p.Rules.Bool("core.night_gate", map[string]any{"own_known": own, "peer_known": peer, "own_signed_move": ownMove, "peer_signed_move": peerMove})
}

// partialFloor145 はP01の利益下限を判断と画面表示で共用します。
//
// 引数: best(float64)は保護開始後の最高利益幅。返り値: float64は現在の返済下限です。
func (p *Portfolio) partialFloor145(best float64) float64 {
	return p.Rules.Number("partial.floor", map[string]any{"best": best})
}

// ----------------------------------------

// armPartialProtection145 は対象となる部分利確の実約定で保護を開始します。
//
// 引数: s(*ResearchSleeve)、f(Fill)、signal(Signal)、closedGain(float64)は費用前の今回返済利益です。
// 返り値: なし。未約定通知、損失返済、全返済、残玉専用返済では開始しません。
func (p *Portfolio) armPartialProtection145(s *ResearchSleeve, f Fill, signal Signal, closedGain float64) {
	if p.Research.Method != "D07" || p.Session.Kind != "Day" || s.Name != "core" || s.PartialProtection != nil || s.Stage != 0 || signal.Action != "close" || s.held() <= 0 {
		return
	}
	if !p.Rules.Bool("partial.start", map[string]any{"closed_gain": closedGain, "reason": float64(signal.ReasonCode)}) {
		return
	}
	anchor := 0.
	for _, l := range s.Lots {
		anchor += l.Price * float64(l.Quantity)
	}
	anchor /= float64(s.held())
	s.PartialProtection = &ResearchPartial145{Anchor: anchor, Best: math.Max(0, float64(s.Side)*(f.Price-anchor)), StartedAt: f.Time}
}

// managePartialProtection145 は開始後の現在気配だけで残玉の利益返しを検知します。
//
// 引数: s(*ResearchSleeve)、o(Observation)。返り値: なし。返済は既存目標キュー経由で次の約定機会へ送ります。
func (p *Portfolio) managePartialProtection145(s *ResearchSleeve, o Observation) {
	if p.Research.Method != "D07" || p.Session.Kind != "Day" || s.Name != "core" || s.PartialProtection == nil || s.Stage != 0 || s.held() <= 0 || !quoteValid(o) {
		return
	}
	protection := s.PartialProtection
	if o.Time < protection.StartedAt {
		return
	}
	gain := float64(s.Side) * (liquidation(o, s.Side) - protection.Anchor)
	protection.Best = math.Max(protection.Best, gain)
	if p.Rules.Bool("partial.exit", map[string]any{"best": protection.Best, "gain": gain, "floor": p.partialFloor145(protection.Best)}) {
		// 元の損切り・場末などの返済目標があれば、その理由と優先順位は置き換えません。
		if s.ForceReason == 0 {
			closeAllResearch(s, 14531)
		}
	}
}

// researchReason145 は00145の追加理由を日本語に変換します。
//
// 引数: reason(int)は研究理由コード。返り値: stringは理由、boolは登録済みかです。
func researchReason145(reason int) (string, bool) {
	values := map[int]string{
		14504: "残り1枚の追尾幅上限", 14531: "部分利確後の最高含み益から半減",
		14510: "利益単位へ到達して部分利確", 14511: "有利な押し戻りから取り直し",
		14512: "利確後の再参加", 14513: "長短方向を確認して反転",
	}
	name, ok := values[reason]
	return name, ok
}
