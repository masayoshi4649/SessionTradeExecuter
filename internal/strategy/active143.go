package strategy

import (
	"math"
	"strings"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// ResearchActive はA34及びA36の準備・注文別保有・連敗停止を保存します。
type ResearchActive struct {
	Rules                                                *rules.Runtime `json:"-" toml:"-"`
	D07Rules                                             bool
	ResearchRules                                        bool
	Book                                                 ResearchBook
	Initial, Maximum                                     int
	Guard, Management                                    bool
	NextReady                                            float64
	Arm, Stage, ArmMode                                  int
	ArmTime, ArmRisk, ArmLevel, Extreme, FirstLow, Pivot float64
	Since, AdverseSince                                  float64
	SinceKnown, AdverseKnown                             bool
	Lost                                                 int
	LastAddTime, ActualInitialRisk                       float64
	PartialDone                                          bool
}

// entryBool はD07又はTG04/PG04の入口式を個別に評価し、未接続の独立状態は従来条件を返します。
//
// 引数: id(string)、legacy(bool)、env(map[string]any)。返り値: boolは入口条件の成立です。
func (s *ResearchActive) entryBool(id string, legacy bool, env map[string]any) bool {
	if s.D07Rules {
		return s.Rules.Bool(id, env)
	}
	if s.ResearchRules {
		return s.Rules.Bool(strings.Replace(id, "addon.entry.", "research.entry.", 1), env)
	}
	return legacy
}

// entryNumber は共通入口の数値式を手法ごとの独立した名前空間で評価します。
//
// 引数: id(string)、env(map[string]any)。返り値: float64は入口選択値です。
func (s *ResearchActive) entryNumber(id string, env map[string]any) float64 {
	if s.D07Rules {
		return s.Rules.Number(id, env)
	}
	return s.Rules.Number(strings.Replace(id, "addon.entry.", "research.entry.", 1), env)
}

// activeBool はTG04/PG04だけ継続・出口式を評価し、D07独立夜間枠は従来条件を維持します。
//
// 引数: name(string)、legacy(bool)、env(map[string]any)。返り値: boolは条件成立です。
func (s *ResearchActive) activeBool(name string, legacy bool, env map[string]any) bool {
	if s.ResearchRules {
		return s.Rules.Bool("research.active."+name, env)
	}
	return legacy
}

// activeNumber はTG04/PG04の数値式を評価し、それ以外では既存値を返します。
//
// 引数: name(string)、legacy(float64)、env(map[string]any)。返り値: float64は判定値です。
func (s *ResearchActive) activeNumber(name string, legacy float64, env map[string]any) float64 {
	if s.ResearchRules {
		return s.Rules.Number("research.active."+name, env)
	}
	return legacy
}

// step は準備済み40分帯から反転又は突破後の戻りを確認します。
//
// 引数: o(Observation)、session(Session)、tick(float64)、gap(bool)。
// 返り値: []ResearchEvent は現在行で判断した発注通知です。
func (s *ResearchActive) step(o Observation, session Session, tick float64, gap bool) []ResearchEvent {
	b := &s.Book
	f := o.Research
	a := f.Values
	valid := quoteValid(o)
	terminal := o.Time >= session.ForceClose
	quiet := session.Kind == "Night" && o.Time >= session.USOpen-900 && o.Time < session.USOpen+900
	if gap {
		s.SinceKnown = false
		s.AdverseKnown = false
	}
	fill := b.filled(o)
	if fill == 1 {
		s.ActualInitialRisk = float64(b.total()) * b.Risk
		s.PartialDone = false
		s.AdverseKnown = false
		s.LastAddTime = o.Time
	}
	if fill == 3 {
		s.LastAddTime = o.Time
	}
	if fill == 2 {
		if b.Cash-b.PriorCash < 0 {
			s.Lost++
		} else {
			s.Lost = 0
		}
		wait := 120.
		if s.Lost >= 2 {
			wait = 900
		}
		wait = s.activeNumber("cooldown", wait, map[string]any{"losses": float64(s.Lost)})
		s.NextReady = o.Time + wait
		s.Arm = 0
		s.SinceKnown = false
	}
	if fill == 4 {
		s.PartialDone = true
	}
	total := b.total()
	if !valid {
		s.AdverseKnown = false
	}
	// ----------------------------------------
	// 予約済み注文を先に約定させ、元の停止・静観・場末を追加判定より優先します。
	if total > 0 && len(b.Pending) == 0 {
		if terminal || quiet {
			reason := 94
			if terminal {
				reason = 93
			}
			return b.queueClose(o, 2, total, reason, false)
		}
		if valid {
			d := float64(b.Side)
			gain := d * (liquidation(o, b.Side) - b.Base)
			b.Best = math.Max(b.Best, gain)
			if s.activeBool("trail_active", b.Best >= 1.25*b.Risk, map[string]any{"best": b.Best, "risk": b.Risk}) {
				width := s.activeNumber("trail_width", .75*b.Risk, map[string]any{"risk": b.Risk})
				b.Floor = math.Max(b.Floor, b.Best-width)
			}
			if s.activeBool("exit", gain <= b.Floor, map[string]any{"gain": gain, "floor": b.Floor}) {
				reason := 32
				if b.Floor > 0 {
					reason = 70
				}
				return b.queueClose(o, 2, total, reason, false)
			}
			adverse := featureKnown(f, 12, 13, 20, 21) && s.activeBool("adverse", d*a[12] <= -.1 && d*a[20] <= -.1 && d*a[13] < 0 && d*a[21] < 0, map[string]any{"own30": d * a[12], "peer30": d * a[20], "own120": d * a[13], "peer120": d * a[21]})
			if s.Management && adverse {
				if !s.AdverseKnown {
					s.AdverseKnown = true
					s.AdverseSince = o.Time
				}
				if s.activeBool("adverse_confirm", o.Time-s.AdverseSince >= 15, map[string]any{"elapsed": o.Time - s.AdverseSince}) {
					opposite := featureKnown(f, 3, 14, 28, 29, 4, 15) && s.activeBool("opposite", d*a[3] < 0 && d*a[14] < 0 && d*a[28] < 0 && d*a[29] < 0 && d*a[4] < 0 && d*a[15] < 0, map[string]any{"own5": d * a[3], "peer5": d * a[14], "own20": d * a[28], "peer20": d * a[29], "own40": d * a[4], "peer40": d * a[15]})
					support := featureKnown(f, 28, 29, 4, 15) && s.activeBool("support", d*a[28] >= 0 && d*a[29] >= 0 && (d*a[28] > 0 || d*a[29] > 0) && d*a[4] >= 0 && d*a[15] >= 0 && (d*a[4] > 0 || d*a[15] > 0), map[string]any{"own20": d * a[28], "peer20": d * a[29], "own40": d * a[4], "peer40": d * a[15]})
					if opposite {
						return b.queueClose(o, 2, total, 1421, false)
					}
					if !support && !s.PartialDone && total >= 2 {
						return b.queueClose(o, 4, max(1, total/2), 1431, true)
					}
				}
			} else {
				s.AdverseKnown = false
			}
		}
	}
	if total > 0 && valid && len(b.Pending) == 0 && !s.PartialDone && total < s.Maximum && b.UID < 5 && o.Time < session.ForceClose-600 && !quiet {
		d := float64(b.Side)
		gain := d * (liquidation(o, b.Side) - b.Base)
		if featureKnown(f, 12, 20) && s.activeBool("add_trigger", d*a[12] > 0 && d*a[20] >= 0 && gain >= .5*float64(b.UID+1)*b.Risk && o.Time-s.LastAddTime >= 30, map[string]any{"own30": d * a[12], "peer30": d * a[20], "gain": gain, "next_ticket": float64(b.UID + 1), "risk": b.Risk, "elapsed": o.Time - s.LastAddTime}) {
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
			if s.activeBool("add_ready", exposed+math.Max(tick, d*(px-stop)) <= budget && o.Ask-o.Bid <= .25*b.Risk, map[string]any{"exposure": exposed + math.Max(tick, d*(px-stop)), "budget": budget, "spread": o.Ask - o.Bid, "risk": b.Risk}) {
				return b.queueNew(o, 3, b.Side, 1, 142, b.Risk, stop)
			}
		}
	}
	return s.entry(o, session, tick, total, valid, terminal, quiet)
}

// entry は旧A34/A36とD07が共通に使う通常入口だけを更新します。
//
// 引数: o(Observation)、session(Session)、tick(float64)、total(int)、valid/terminal/quiet(bool)。
// 返り値: []ResearchEvent は準備と方向確認を経た新規通知です。
func (s *ResearchActive) entry(o Observation, session Session, tick float64, total int, valid, terminal, quiet bool) []ResearchEvent {
	b := &s.Book
	f := o.Research
	a := f.Values
	// ----------------------------------------
	// 指標の未知は支持票にせず、最初の準備と30秒の継続確認を分離します。
	known := valid
	for c := 0; c < 24; c++ {
		known = known && f.Known[c] && finite(a[c])
	}
	if total == 0 && len(b.Pending) == 0 && known && !terminal && !quiet && o.Time >= s.NextReady && o.Time < session.ForceClose-600 {
		r, mid := a[9], a[10]
		if s.entryBool("addon.entry.spread_reject", o.Ask-o.Bid > .25*r, map[string]any{"spread": o.Ask - o.Bid, "risk": r}) {
			s.Arm = 0
			return nil
		}
		peerDirection := int(sign(a[15]))
		chosen := 4
		if s.Guard {
			chosen = -1
			if a[2] >= .35 {
				chosen = 1
			} else if a[2] <= .2 {
				chosen = 4
			}
			if s.D07Rules || s.ResearchRules {
				mode := s.entryNumber("addon.entry.mode", map[string]any{"efficiency": a[2]})
				chosen = -1
				if mode == 1 || mode == 4 {
					chosen = int(mode)
				}
			}
		}
		if s.Arm != 0 && s.entryBool("addon.entry.expired", o.Time-s.ArmTime > 1200 || chosen != s.ArmMode, map[string]any{"elapsed": o.Time - s.ArmTime, "changed": chosen != s.ArmMode}) {
			s.Arm = 0
		}
		if s.Arm == 0 {
			candidate := 0
			boundary := 0.
			if chosen == 1 && a[2] >= .25 {
				if mid > a[0]+.05*r {
					candidate = 1
					boundary = a[0]
				} else if mid < a[1]-.05*r {
					candidate = -1
					boundary = a[1]
				}
			}
			if chosen == 4 && a[2] <= .3 {
				if mid <= a[1]+.1*r {
					candidate = 1
					boundary = a[1]
				} else if mid >= a[0]-.1*r {
					candidate = -1
					boundary = a[0]
				}
			}
			if (s.D07Rules || s.ResearchRules) && (chosen == 1 || chosen == 4) {
				id := "addon.entry.breakout_side"
				if chosen == 4 {
					id = "addon.entry.range_side"
				}
				direction := s.entryNumber(id, map[string]any{"efficiency": a[2], "mid": mid, "high": a[0], "low": a[1], "risk": r})
				// 外部式が方向以外の数値を返しても、任意枚数・不正方向へ変換しません。
				candidate = 0
				if direction == -1 || direction == 1 {
					candidate = int(direction)
				}
				if (chosen == 1 && candidate > 0) || (chosen == 4 && candidate < 0) {
					boundary = a[0]
				} else if candidate != 0 {
					boundary = a[1]
				}
			}
			if candidate != 0 && (chosen == 4 || candidate == peerDirection) {
				s.Arm = candidate
				s.ArmLevel = boundary
				s.ArmRisk = r
				s.ArmTime = o.Time
				s.Extreme = mid
				s.Stage = 0
				s.ArmMode = chosen
				s.SinceKnown = false
			}
		}
		if s.Arm != 0 {
			d := float64(s.Arm)
			signed := d * (mid - s.ArmLevel)
			trigger := false
			support := s.entryBool("addon.entry.support30", d*a[12] >= 0 && d*a[20] >= 0, map[string]any{"own30": d * a[12], "peer30": d * a[20]})
			if chosen != 4 {
				support = support && s.Arm == peerDirection
			}
			if chosen == 1 {
				if s.entryBool("addon.entry.breakout_failed", signed < -.5*s.ArmRisk, map[string]any{"distance": signed, "risk": s.ArmRisk}) {
					s.Arm = 0
				} else if s.Stage == 0 && s.entryBool("addon.entry.breakout_advanced", signed >= .5*s.ArmRisk, map[string]any{"distance": signed, "risk": s.ArmRisk}) {
					s.Stage = 1
				} else if s.Stage == 1 && s.entryBool("addon.entry.breakout_retested", signed <= .25*s.ArmRisk, map[string]any{"distance": signed, "risk": s.ArmRisk}) {
					s.Stage = 2
					s.Extreme = mid
				} else if s.Stage == 2 {
					s.Extreme = adverseExtreme(s.Extreme, mid, s.Arm)
					trigger = s.entryBool("addon.entry.rebound", d*(mid-s.Extreme) >= .25*s.ArmRisk, map[string]any{"distance": d * (mid - s.Extreme), "risk": s.ArmRisk})
				}
			} else if chosen == 4 {
				s.Extreme = adverseExtreme(s.Extreme, mid, s.Arm)
				trigger = d*(mid-s.Extreme) >= .25*s.ArmRisk && signed >= 0
				if s.ResearchRules {
					trigger = s.Rules.Bool("research.entry.range_initial", map[string]any{"rebound": d * (mid - s.Extreme), "risk": s.ArmRisk, "boundary_distance": signed})
				}
				if s.Guard {
					trigger = false
					if s.Stage == 0 && s.entryBool("addon.entry.rebound", d*(mid-s.Extreme) >= .25*s.ArmRisk, map[string]any{"distance": d * (mid - s.Extreme), "risk": s.ArmRisk}) {
						s.Stage = 1
						s.FirstLow = s.Extreme
						s.Pivot = mid
					} else if s.Stage == 1 {
						s.Pivot = adverseExtreme(s.Pivot, mid, -s.Arm)
						if s.entryBool("addon.entry.range_pullback", d*(s.Pivot-mid) >= .125*s.ArmRisk, map[string]any{"distance": d * (s.Pivot - mid), "risk": s.ArmRisk}) {
							s.Stage = 2
							s.Extreme = mid
						}
					} else if s.Stage == 2 {
						s.Extreme = adverseExtreme(s.Extreme, mid, s.Arm)
						if s.entryBool("addon.entry.higher_low_failed", d*(s.Extreme-s.FirstLow) < 2*tick, map[string]any{"distance": d * (s.Extreme - s.FirstLow), "tick": tick}) {
							s.Stage = 0
							s.Extreme = adverseExtreme(s.FirstLow, mid, s.Arm)
						} else {
							trigger = s.entryBool("addon.entry.range_rebound", d*(mid-s.Extreme) >= .125*s.ArmRisk && signed >= 0, map[string]any{"rebound": d * (mid - s.Extreme), "risk": s.ArmRisk, "boundary_distance": signed})
						}
					}
					support = support && s.entryBool("addon.entry.support120", d*a[13] >= 0 && d*a[21] >= 0, map[string]any{"own120": d * a[13], "peer120": d * a[21]})
				}
				if s.entryBool("addon.entry.range_failed", signed < -.75*s.ArmRisk, map[string]any{"distance": signed, "risk": s.ArmRisk}) {
					s.Arm = 0
				}
			}
			if s.Arm != 0 && trigger && support {
				if !s.SinceKnown {
					s.SinceKnown = true
					s.Since = o.Time
				}
				if s.entryBool("addon.entry.confirmed", o.Time-s.Since >= 30, map[string]any{"elapsed": o.Time - s.Since}) {
					qty := min(s.Initial, int(float64(s.Initial)*40*tick/s.ArmRisk))
					side := s.Arm
					risk := s.ArmRisk
					boundary := s.ArmLevel
					s.Arm = 0
					s.SinceKnown = false
					if qty >= 1 {
						return b.queueNew(o, 1, side, qty, 140+chosen, risk, boundary)
					}
				}
			} else {
				s.SinceKnown = false
			}
		}
	} else if total == 0 {
		s.SinceKnown = false
		if !known || quiet || terminal {
			s.Arm = 0
		}
	}
	return nil
}

// adverseExtreme は方向に対して不利な側の極値を返します。
//
// 引数: prior/current(float64)、side(int)。返り値: float64 は買いなら安値、売りなら高値です。
func adverseExtreme(prior, current float64, side int) float64 {
	if side > 0 {
		return math.Min(prior, current)
	}
	return math.Max(prior, current)
}
