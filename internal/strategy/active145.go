package strategy

import (
	"math"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// ResearchActive145 はD07追加担当の利益単位・部分回転・全返済後再参加を保持します。
// Bookの6枠は物理的な空き枠、LotUIDは親内で再利用しない注文識別子です。
type ResearchActive145 struct {
	ResearchActive
	ExitRisk, PendingExitRisk                             float64
	LotUID                                                [6]int
	PendingSlot                                           int
	Rotation                                              bool
	RotationAnchor, RotationExtreme, RotationSince        float64
	RotationStage                                         int
	RotationKnown                                         bool
	ClosedContext, ClosedProfit                           bool
	LastClosedSide                                        int
	ClosedPrice, ClosedUnit, ReentryExtreme, ReentrySince float64
	ReentryStage, ReentryCandidate                        int
	ReentryKnown                                          bool
}

// newResearchActive145 は昼V05・夜V16の共通状態を初期化します。
//
// 引数: session(Session)、capacity(int)。返り値: *ResearchActive145。
func newResearchActive145(session Session, capacity int) *ResearchActive145 {
	s := &ResearchActive145{ResearchActive: ResearchActive{Book: ResearchBook{Parent: -1, FeeFree: true}, Initial: min(2, capacity), Maximum: capacity, Guard: true, Management: session.Kind == "Night", NextReady: session.Start, D07Rules: true}}
	for i := range s.LotUID {
		s.LotUID[i] = i
	}
	return s
}

// exitUnit145 は初回判断に公開済みのATRだけで利益追尾単位を固定します。
//
// 引数: risk/atr/tick(float64)。返り値: float64。ATR欠測時は元のRです。
func exitUnit145(risk, atr, tick float64) float64 {
	return exitUnitWithRules145(nil, risk, atr, tick)
}

// exitUnitWithRules145 は利益単位を指定ルールで決め、ATR欠測を既知値と区別します。
//
// 引数: runtime(*rules.Runtime)、risk/atr/tick(float64)。返り値: float64は初回固定Uです。
func exitUnitWithRules145(runtime *rules.Runtime, risk, atr, tick float64) float64 {
	known := finite(atr) && atr > 0
	if !known {
		atr = 0
	}
	return runtime.Number("addon.exit_unit", map[string]any{"atr_known": known, "tick": tick, "risk": risk, "atr": atr})
}

// queueInitial は通常又は確認後再参加の初回注文と利益単位を同時に固定します。
//
// 引数: o(Observation)、side/qty/reason(int)、risk/boundary/tick(float64)。返り値: []ResearchEvent。
func (s *ResearchActive145) queueInitial(o Observation, side, qty, reason int, risk, boundary, tick float64) []ResearchEvent {
	s.PendingExitRisk = exitUnitWithRules145(s.Rules, risk, o.ATR, tick)
	return s.Book.queueNew(o, 1, side, qty, reason, risk, boundary)
}

// queueClose145 は物理枠と一意の玉番号を対応させ、指定分の返済を予約します。
//
// 引数: o(Observation)、kind/reason(int)、quantities([6]int)、descending(bool)。返り値: []ResearchEvent。
func (s *ResearchActive145) queueClose145(o Observation, kind, reason int, quantities [6]int, descending bool) []ResearchEvent {
	b := &s.Book
	out := []ResearchEvent{}
	for k := 0; k < 6; k++ {
		u := k
		if descending {
			u = 5 - k
		}
		q := min(quantities[u], b.Held[u])
		if q <= 0 {
			continue
		}
		out = append(out, ResearchEvent{Action: "close", Side: -b.Side, Quantity: q, Reason: reason, Campaign: b.Parent, Ticket: s.LotUID[u], Risk: b.Risk, Boundary: b.Base + float64(b.Side)*b.Floor})
	}
	b.Pending = out
	b.PendingIndex = o.Index
	b.PendingKind = kind
	return append([]ResearchEvent(nil), out...)
}

// closeAll145 は現在の仮想玉全ての返済を次正常気配へ予約します。
//
// 引数: o(Observation)、reason(int)。返り値: []ResearchEvent。
func (s *ResearchActive145) closeAll145(o Observation, reason int) []ResearchEvent {
	return s.queueClose145(o, 2, reason, s.Book.Held, false)
}

// filled145 は次の正常原行で予約を反映し、実際の全返済後だけ再参加時計を開始します。
//
// 引数: o(Observation)、night(bool)。返り値: なし。源会計の費用は00145と同じ0です。
func (s *ResearchActive145) filled145(o Observation, night bool) {
	b := &s.Book
	if len(b.Pending) == 0 || o.Index <= b.PendingIndex || !quoteValid(o) {
		return
	}
	kind := b.PendingKind
	closePrice := liquidation(o, b.Side)
	for _, e := range b.Pending {
		price := liquidation(o, -e.Side)
		if e.Action == "new" {
			if kind == 1 {
				b.Parent = e.Campaign
				b.Side = e.Side
				b.UID = 0
				b.Risk = e.Risk
				b.Base = price
				b.Floor = -e.Risk
				b.Best = 0
				b.PriorCash = b.Cash
				s.ActualInitialRisk = float64(e.Quantity) * e.Risk
				s.PartialDone = false
				s.AdverseKnown = false
				s.ExitRisk = s.PendingExitRisk
				s.Rotation = false
				s.ClosedContext = false
				s.RotationKnown = false
				s.ReentryKnown = false
				for i := range s.LotUID {
					s.LotUID[i] = i
				}
			} else {
				b.UID++
			}
			slot := b.UID
			if kind == 6 {
				slot = s.PendingSlot
			}
			b.Held[slot] = e.Quantity
			b.Entries[slot] = price
			s.LotUID[slot] = b.UID
			b.Cash -= float64(e.Side*e.Quantity) * price * 100
			s.LastAddTime = o.Time
			if kind == 6 {
				s.Rotation = false
				s.RotationKnown = false
			}
		} else {
			for u := range s.LotUID {
				if s.LotUID[u] == e.Ticket && b.Held[u] > 0 {
					b.Held[u] -= e.Quantity
					break
				}
			}
			b.Cash -= float64(e.Side*e.Quantity) * price * 100
		}
	}
	if kind == 2 {
		profit := b.Cash - b.PriorCash
		if profit < 0 {
			s.Lost++
		} else {
			s.Lost = 0
		}
		s.NextReady = o.Time + s.Rules.Number("addon.cooldown", map[string]any{"night": night, "profitable": profit > 0, "losses": float64(s.Lost)})
		s.LastClosedSide = b.Side
		s.ClosedContext = night
		s.ClosedProfit = profit > 0
		s.ClosedPrice = closePrice
		s.ReentryExtreme = closePrice
		s.ClosedUnit = s.ExitRisk
		s.ReentryStage = 0
		s.ReentryKnown = false
		s.ReentryCandidate = 0
		s.Arm = 0
		s.SinceKnown = false
	} else if kind == 4 || kind == 5 {
		s.PartialDone = true
		if kind == 5 && !night {
			s.Rotation = true
			s.RotationAnchor = closePrice
			s.RotationExtreme = closePrice
			s.RotationStage = 0
			s.RotationKnown = false
		}
	}
	b.Pending = nil
	b.PendingKind = 0
}

// step は元停止・静観・場末を優先してから、昼回転又は夜再参加を判定します。
//
// 引数: o(Observation)、session(Session)、tick(float64)、gap(bool)。返り値: []ResearchEvent。
func (s *ResearchActive145) step(o Observation, session Session, tick float64, gap bool) []ResearchEvent {
	b := &s.Book
	f := o.Research
	a := f.Values
	night := session.Kind == "Night"
	valid := quoteValid(o)
	terminal := o.Time >= session.ForceClose
	quiet := night && o.Time >= session.USOpen-900 && o.Time < session.USOpen+900
	if gap {
		s.SinceKnown = false
		s.AdverseKnown = false
		s.RotationKnown = false
		s.ReentryKnown = false
	}
	s.filled145(o, night)
	total := b.total()
	if !valid {
		s.AdverseKnown = false
		s.RotationKnown = false
		s.ReentryKnown = false
	}
	// ----------------------------------------
	if total > 0 && len(b.Pending) == 0 {
		if terminal || quiet {
			reason := 94
			if terminal {
				reason = 93
			}
			return s.closeAll145(o, reason)
		}
		if valid {
			d := float64(b.Side)
			gain := d * (liquidation(o, b.Side) - b.Base)
			b.Best = math.Max(b.Best, gain)
			if s.Rules.Bool("addon.trail_active", map[string]any{"best": b.Best, "unit": s.ExitRisk}) {
				trail := s.Rules.Number("addon.trail_width", map[string]any{"night": night, "unit": s.ExitRisk})
				b.Floor = math.Max(b.Floor, b.Best-trail)
			}
			if s.Rules.Bool("addon.exit_trigger", map[string]any{"gain": gain, "floor": b.Floor}) {
				reason := 32
				if b.Floor > 0 {
					reason = 70
				}
				return s.closeAll145(o, reason)
			}
			adverse := featureKnown(f, 12, 13, 20, 21) && s.Rules.Bool("addon.adverse", map[string]any{"own30": d * a[12], "peer30": d * a[20], "own120": d * a[13], "peer120": d * a[21]})
			if s.Management && adverse {
				if !s.AdverseKnown {
					s.AdverseKnown = true
					s.AdverseSince = o.Time
				}
				if s.Rules.Bool("addon.adverse_confirm", map[string]any{"elapsed": o.Time - s.AdverseSince}) {
					opposite := featureKnown(f, 3, 14, 28, 29, 4, 15) && s.Rules.Bool("addon.opposite", map[string]any{"own5": d * a[3], "peer5": d * a[14], "own20": d * a[28], "peer20": d * a[29], "own40": d * a[4], "peer40": d * a[15]})
					support := featureKnown(f, 28, 29, 4, 15) && s.Rules.Bool("addon.support", map[string]any{"own20": d * a[28], "peer20": d * a[29], "own40": d * a[4], "peer40": d * a[15]})
					if opposite {
						return s.closeAll145(o, 1421)
					}
					if !support && !s.PartialDone && total >= 2 {
						var amounts [6]int
						remain := max(1, total/2)
						for u := 5; u >= 0; u-- {
							amounts[u] = min(remain, b.Held[u])
							remain -= amounts[u]
						}
						return s.queueClose145(o, 4, 1431, amounts, true)
					}
				}
			} else {
				s.AdverseKnown = false
			}
		}
	}
	// ----------------------------------------
	// 昼は部分利確と、親内で一意な玉番号を持つ回転玉の個別利確を行います。
	if !night && total >= 2 && valid && len(b.Pending) == 0 && !terminal && !quiet {
		d := float64(b.Side)
		mark := liquidation(o, b.Side)
		var amounts [6]int
		if !s.PartialDone && s.Rules.Bool("addon.partial_profit", map[string]any{"gain": d * (mark - b.Base), "unit": s.ExitRisk}) {
			remain := total / 2
			for u := 5; u >= 0; u-- {
				if b.Held[u] > 0 && d*(mark-b.Entries[u]) > 0 {
					amounts[u] = min(b.Held[u], remain)
					remain -= amounts[u]
				}
			}
		} else if s.PartialDone && !s.Rotation {
			newest := -1
			for u := range b.Held {
				if b.Held[u] > 0 && (newest < 0 || s.LotUID[u] > s.LotUID[newest]) {
					newest = u
				}
			}
			if newest >= 0 && s.Rules.Bool("addon.partial_profit", map[string]any{"gain": d * (mark - b.Entries[newest]), "unit": s.ExitRisk}) {
				amounts[newest] = 1
			}
		}
		if sum145(amounts) > 0 {
			return s.queueClose145(o, 5, 14510, amounts, true)
		}
	}
	if total > 0 && total < s.Maximum && s.Rotation && valid && len(b.Pending) == 0 && o.Time < session.ForceClose-600 && !quiet {
		d := float64(b.Side)
		mid := (o.Bid + o.Ask) * .5
		s.RotationExtreme = adverseExtreme(s.RotationExtreme, mid, b.Side)
		if s.Rules.Bool("addon.pullback", map[string]any{"distance": d * (s.RotationAnchor - s.RotationExtreme), "unit": s.ExitRisk}) {
			s.RotationStage = 1
		}
		support := featureKnown(f, 12, 13) && s.Rules.Bool("addon.self_support", map[string]any{"own30": d * a[12], "own120": d * a[13]})
		price := liquidation(o, -b.Side)
		trigger := s.RotationStage == 1 && s.Rules.Bool("addon.rebound", map[string]any{"rebound": d * (mid - s.RotationExtreme), "unit": s.ExitRisk, "price_change": d * (price - s.RotationAnchor)})
		if trigger && support {
			if !s.RotationKnown {
				s.RotationKnown = true
				s.RotationSince = o.Time
			}
			stop := b.Base + d*b.Floor
			risk := math.Max(tick, d*(price-stop))
			for u, q := range b.Held {
				risk += float64(q) * math.Max(0, d*(b.Entries[u]-stop))
			}
			if s.Rules.Bool("addon.rotation_ready", map[string]any{"elapsed": o.Time - s.RotationSince, "exposure": risk, "budget": s.ActualInitialRisk, "spread": o.Ask - o.Bid, "unit": s.ExitRisk}) {
				for u, q := range b.Held {
					if q == 0 {
						s.PendingSlot = u
						break
					}
				}
				b.Pending = []ResearchEvent{{Action: "new", Side: b.Side, Quantity: 1, Reason: 14511, Campaign: b.Parent, Ticket: b.UID + 1, Risk: b.Risk, Boundary: stop}}
				b.PendingIndex = o.Index
				b.PendingKind = 6
				return append([]ResearchEvent(nil), b.Pending...)
			}
		} else {
			s.RotationKnown = false
		}
	}
	// ----------------------------------------
	// 部分利確前だけ、TG04元来のリスク予算付き追撃を維持します。
	if total > 0 && valid && len(b.Pending) == 0 && !s.PartialDone && total < s.Maximum && b.UID < 5 && o.Time < session.ForceClose-600 && !quiet {
		d := float64(b.Side)
		gain := d * (liquidation(o, b.Side) - b.Base)
		if featureKnown(f, 12, 20) && s.Rules.Bool("addon.add_trigger", map[string]any{"own30": d * a[12], "peer30": d * a[20], "gain": gain, "next_ticket": float64(b.UID + 1), "risk": b.Risk, "elapsed": o.Time - s.LastAddTime}) {
			px := liquidation(o, -b.Side)
			stop := b.Base + d*b.Floor
			exposed := 0.
			for u, q := range b.Held {
				exposed += float64(q) * math.Max(0, d*(b.Entries[u]-stop))
			}
			budget := float64(s.Initial) * 40 * tick
			if s.Management {
				budget = s.ActualInitialRisk
				if b.Floor <= 0 {
					budget = -1
				}
			}
			if s.Rules.Bool("addon.add_ready", map[string]any{"exposure": exposed + math.Max(tick, d*(px-stop)), "budget": budget, "spread": o.Ask - o.Bid, "risk": b.Risk}) {
				return b.queueNew(o, 3, b.Side, 1, 142, b.Risk, stop)
			}
		}
	}
	known := valid
	for c := 0; c < 24; c++ {
		known = known && featureKnown(f, c)
	}
	if !known {
		s.ReentryKnown = false
		s.ReentryCandidate = 0
	}
	if quiet || terminal {
		s.ClosedContext = false
		s.Rotation = false
	}
	// ----------------------------------------
	// 夜は全返済が確認された後にだけ、押戻り再参加と40分方向付きドテンを検討します。
	if total == 0 && len(b.Pending) == 0 && known && s.ClosedContext && !terminal && !quiet && o.Time >= s.NextReady && o.Time < session.ForceClose-600 {
		mid := a[10]
		candidate := 0
		d := float64(s.LastClosedSide)
		if s.ClosedProfit {
			s.ReentryExtreme = adverseExtreme(s.ReentryExtreme, mid, s.LastClosedSide)
			if s.Rules.Bool("addon.pullback", map[string]any{"distance": d * (s.ClosedPrice - s.ReentryExtreme), "unit": s.ClosedUnit}) {
				s.ReentryStage = 1
			}
			price := liquidation(o, -s.LastClosedSide)
			if s.ReentryStage == 1 && s.Rules.Bool("addon.rebound", map[string]any{"rebound": d * (mid - s.ReentryExtreme), "unit": s.ClosedUnit, "price_change": d * (price - s.ClosedPrice)}) && s.Rules.Bool("addon.self_support", map[string]any{"own30": d * a[12], "own120": d * a[13]}) {
				candidate = s.LastClosedSide
			}
		}
		opposite := -s.LastClosedSide
		od := float64(opposite)
		if f.MA20Known && finite(f.MA20) && featureKnown(f, 4) && s.Rules.Bool("addon.flip_direction", map[string]any{"own5": od * a[3], "own120": od * a[13], "own30": od * a[12], "price_ma": od * (mid - f.MA20), "own40": od * a[4]}) {
			candidate = opposite
		}
		if candidate != 0 {
			if candidate != s.ReentryCandidate || !s.ReentryKnown {
				s.ReentryCandidate = candidate
				s.ReentrySince = o.Time
				s.ReentryKnown = true
			}
			if s.Rules.Bool("addon.reentry_ready", map[string]any{"elapsed": o.Time - s.ReentrySince, "spread": o.Ask - o.Bid, "risk": a[9]}) {
				qty := min(s.Initial, int(float64(s.Initial)*40*tick/a[9]))
				if qty >= 1 {
					reason := 14512
					if candidate != s.LastClosedSide {
						reason = 14513
					}
					s.ClosedContext = false
					s.Arm = 0
					s.SinceKnown = false
					return s.queueInitial(o, candidate, qty, reason, a[9], 0, tick)
				}
			}
		} else {
			s.ReentryKnown = false
			s.ReentryCandidate = 0
		}
	}
	out := s.entry(o, session, tick, total, valid, terminal, quiet)
	if len(out) > 0 && b.PendingKind == 1 {
		s.PendingExitRisk = exitUnitWithRules145(s.Rules, out[0].Risk, o.ATR, tick)
	}
	return out
}

// sum145 は物理枠ごとの返済予定枚数を合計します。
//
// 引数: values([6]int)。返り値: intは枚数合計です。
func sum145(values [6]int) int {
	n := 0
	for _, q := range values {
		n += q
	}
	return n
}
