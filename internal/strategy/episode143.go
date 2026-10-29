package strategy

import (
	"math"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// ResearchEpisode は公開20分帯の突破失敗を追う日中担当の保存状態です。
type ResearchEpisode struct {
	Rules                                                                 *rules.Runtime `json:"-" toml:"-"`
	ResearchRules                                                         bool
	Book                                                                  ResearchBook
	Initial, Maximum                                                      int
	Locked                                                                [2]bool
	LockEdge, LockExtreme, LockPublic                                     [2]float64
	BandWidth, Edge, Center, EpisodeExtreme, InitialBudget                float64
	Arm                                                                   int
	ArmEdge, ArmCenter, ArmWidth, ArmExtreme, ArmTime                     float64
	Since                                                                 float64
	SinceKnown                                                            bool
	PendingStop, PendingWidth, PendingEdge, PendingCenter, PendingExtreme float64
	LastAdd, LastAddGain                                                  float64
	PartialDone                                                           bool
	CampaignLow, CampaignHigh                                             float64
}

// episodeBool はTG04/PG04の日中別入口だけ外部条件を評価します。
//
// 引数: name(string)、legacy(bool)、env(map[string]any)。返り値: boolは条件成立です。
func (s *ResearchEpisode) episodeBool(name string, legacy bool, env map[string]any) bool {
	if s.ResearchRules {
		return s.Rules.Bool("research.episode."+name, env)
	}
	return legacy
}

// episodeNumber はTG04/PG04の日中別入口の数値式を評価します。
//
// 引数: name(string)、legacy(float64)、env(map[string]any)。返り値: float64は判定値です。
func (s *ResearchEpisode) episodeNumber(name string, legacy float64, env map[string]any) float64 {
	if s.ResearchRules {
		return s.Rules.Number("research.episode."+name, env)
	}
	return legacy
}

// step は固定した帯の外側への失敗と内側回復を確認して注文を判断します。
//
// 引数: o(Observation)、session(Session)、tick(float64)、gap(bool)。
// 返り値: []ResearchEvent は現在原行の通知です。最終値や将来足は参照しません。
func (s *ResearchEpisode) step(o Observation, session Session, tick float64, gap bool) []ResearchEvent {
	b := &s.Book
	roundTrip := .77
	if b.FeeFree {
		roundTrip = 0
	}
	valid := quoteValid(o)
	h := o.Research.Band20.Values
	known := valid && o.Research.Band20.Known && h[0] > h[1]
	for _, v := range h {
		known = known && finite(v)
	}
	terminal := o.Time >= session.ForceClose
	mid := (o.Bid + o.Ask) * .5
	if gap {
		s.SinceKnown = false
	}
	fill := b.filled(o)
	if fill == 1 {
		b.Floor = float64(b.Side) * (s.PendingStop - b.Base)
		b.Risk = s.episodeNumber("fixed_r", math.Max(8*tick, -b.Floor), map[string]any{"tick": tick, "distance": -b.Floor})
		s.InitialBudget = float64(b.total()) * math.Max(tick, -b.Floor)
		s.BandWidth, s.Edge, s.Center, s.EpisodeExtreme = s.PendingWidth, s.PendingEdge, s.PendingCenter, s.PendingExtreme
		s.PartialDone = false
		s.CampaignLow, s.CampaignHigh = mid, mid
		s.LastAddGain = 0
		s.LastAdd = o.Time
	} else if fill == 3 {
		s.LastAdd = o.Time
	} else if fill == 4 {
		s.PartialDone = true
		b.Floor = math.Max(b.Floor, s.episodeNumber("partial_floor", roundTrip+tick, map[string]any{"round_trip": roundTrip, "tick": tick}))
	} else if fill == 2 {
		k := 0
		if b.Side < 0 {
			k = 1
		}
		s.Locked[k] = true
		s.LockEdge[k] = s.Edge
		if known {
			if b.Side > 0 {
				s.LockEdge[k] = h[1]
			} else {
				s.LockEdge[k] = h[0]
			}
		}
		if b.Side > 0 {
			s.LockExtreme[k] = math.Min(s.CampaignLow, mid)
		} else {
			s.LockExtreme[k] = math.Max(s.CampaignHigh, mid)
		}
		s.LockPublic[k] = o.Time
		if known {
			s.LockPublic[k] = h[8]
		}
		s.Arm = 0
		s.SinceKnown = false
	}
	total := b.total()
	if total > 0 && valid {
		s.CampaignLow = math.Min(s.CampaignLow, mid)
		s.CampaignHigh = math.Max(s.CampaignHigh, mid)
	}
	// ----------------------------------------
	// 固定停止は新しい玉から返済し、中心到達では半分だけ利益を確定します。
	if total > 0 && len(b.Pending) == 0 {
		if terminal {
			return b.queueClose(o, 2, total, 93, true)
		}
		if valid {
			d := float64(b.Side)
			mark := liquidation(o, b.Side)
			gain := d * (mark - b.Base)
			b.Best = math.Max(b.Best, gain)
			if s.episodeBool("trail_active", b.Best >= b.Risk, map[string]any{"best": b.Best, "risk": b.Risk}) {
				width := s.episodeNumber("trail_width", math.Max(8*tick, .5*s.BandWidth), map[string]any{"tick": tick, "band_width": s.BandWidth})
				b.Floor = math.Max(b.Floor, b.Best-width)
			}
			if s.episodeBool("exit", gain <= b.Floor, map[string]any{"gain": gain, "floor": b.Floor}) {
				reason := 1436
				if b.Floor > 0 {
					reason = 1437
				}
				return b.queueClose(o, 2, total, reason, true)
			}
			if !s.PartialDone && total >= 2 && s.episodeBool("partial", d*(mark-s.Center) >= 0 && gain > roundTrip+tick, map[string]any{"center_distance": d * (mark - s.Center), "gain": gain, "round_trip": roundTrip, "tick": tick}) {
				return b.queueClose(o, 4, total/2, 1435, true)
			}
		}
	}
	if total > 0 && valid && len(b.Pending) == 0 && total < s.Maximum && b.UID < 5 && o.Time < session.ForceClose-300 {
		d := float64(b.Side)
		gain := d * (liquidation(o, b.Side) - b.Base)
		if s.episodeBool("add_trigger", b.Floor >= roundTrip && o.Time-s.LastAdd >= 60 && gain >= s.LastAddGain+.5*b.Risk, map[string]any{"floor": b.Floor, "round_trip": roundTrip, "elapsed": o.Time - s.LastAdd, "gain": gain, "last_gain": s.LastAddGain, "risk": b.Risk}) {
			price := liquidation(o, -b.Side)
			stop := b.Base + d*b.Floor
			exposed := 0.
			for u, q := range b.Held {
				exposed += float64(q) * math.Max(0, d*(b.Entries[u]-stop))
			}
			if s.episodeBool("add_ready", exposed+math.Max(tick, d*(price-stop)) <= s.InitialBudget && o.Ask-o.Bid <= .1*s.BandWidth, map[string]any{"exposure": exposed + math.Max(tick, d*(price-stop)), "budget": s.InitialBudget, "spread": o.Ask - o.Bid, "band_width": s.BandWidth}) {
				s.LastAddGain = gain
				return b.queueNew(o, 3, b.Side, 1, 1434, b.Risk, stop)
			}
		}
	}
	// ----------------------------------------
	// 同方向の再参加は新境界又は新極値を観測した後だけ許可します。
	if total == 0 && len(b.Pending) == 0 && known && o.Time >= session.Start && o.Time < session.ForceClose-300 {
		high, low := h[0], h[1]
		width := high - low
		for k := 0; k < 2; k++ {
			if s.Locked[k] {
				d := 1.
				boundary := low
				if k == 1 {
					d = -1
					boundary = high
				}
				fresh := h[8] > s.LockPublic[k] && d*(s.LockEdge[k]-boundary) >= 2*tick
				extreme := d*(s.LockExtreme[k]-mid) >= 2*tick
				if s.episodeBool("unlock", fresh || extreme, map[string]any{"new_public": h[8] > s.LockPublic[k], "edge_change": d * (s.LockEdge[k] - boundary), "extreme_change": d * (s.LockExtreme[k] - mid), "tick": tick}) {
					s.Locked[k] = false
				}
			}
		}
		qualified := s.episodeBool("qualified", width >= 16*tick && o.Ask-o.Bid <= .1*width, map[string]any{"band_width": width, "tick": tick, "spread": o.Ask - o.Bid})
		if s.Arm != 0 && s.episodeBool("expired", o.Time-s.ArmTime > 1200 || !qualified, map[string]any{"elapsed": o.Time - s.ArmTime, "qualified": qualified}) {
			s.Arm = 0
			s.SinceKnown = false
		}
		if s.Arm == 0 && qualified {
			candidate := 0
			boundary := 0.
			if mid <= low-2*tick {
				candidate = 1
				boundary = low
			} else if mid >= high+2*tick {
				candidate = -1
				boundary = high
			}
			if s.ResearchRules {
				direction := s.episodeNumber("side", float64(candidate), map[string]any{"mid": mid, "low": low, "high": high, "tick": tick})
				candidate = 0
				if direction == 1 {
					candidate, boundary = 1, low
				} else if direction == -1 {
					candidate, boundary = -1, high
				}
			}
			k := 0
			if candidate < 0 {
				k = 1
			}
			if candidate != 0 && !s.Locked[k] {
				s.Arm = candidate
				s.ArmEdge = boundary
				s.ArmCenter = (high + low) * .5
				s.ArmWidth = width
				s.ArmExtreme = mid
				s.ArmTime = o.Time
				s.SinceKnown = false
			}
		}
		if s.Arm != 0 {
			d := float64(s.Arm)
			signed := d * (mid - s.ArmEdge)
			s.ArmExtreme = adverseExtreme(s.ArmExtreme, mid, s.Arm)
			offset := s.episodeNumber("stop_offset", math.Max(2*tick, .1*s.ArmWidth), map[string]any{"tick": tick, "band_width": s.ArmWidth})
			stop := s.ArmExtreme - d*offset
			if s.episodeBool("recovered", signed >= 2*tick, map[string]any{"distance": signed, "tick": tick}) {
				price := liquidation(o, -s.Arm)
				distance := d * (price - stop)
				reward := d * (s.ArmCenter - price)
				acceptable := s.episodeBool("acceptable", distance > 0 && distance <= .75*s.ArmWidth && reward >= math.Max(4*tick, 2*(o.Ask-o.Bid)), map[string]any{"distance": distance, "band_width": s.ArmWidth, "reward": reward, "tick": tick, "spread": o.Ask - o.Bid})
				if acceptable {
					if !s.SinceKnown {
						s.SinceKnown = true
						s.Since = o.Time
					}
					if s.episodeBool("confirmed", o.Time-s.Since >= 15, map[string]any{"elapsed": o.Time - s.Since}) {
						s.PendingStop, s.PendingWidth, s.PendingEdge, s.PendingCenter, s.PendingExtreme = stop, s.ArmWidth, s.ArmEdge, s.ArmCenter, s.ArmExtreme
						side := s.Arm
						s.Arm = 0
						s.SinceKnown = false
						risk := s.episodeNumber("fixed_r", math.Max(8*tick, distance), map[string]any{"tick": tick, "distance": distance})
						return b.queueNew(o, 1, side, s.Initial, 1431, risk, stop)
					}
				} else {
					s.SinceKnown = false
				}
			} else {
				s.SinceKnown = false
			}
		}
	} else if total == 0 && (!known || terminal) {
		s.Arm = 0
		s.SinceKnown = false
	}
	return nil
}
