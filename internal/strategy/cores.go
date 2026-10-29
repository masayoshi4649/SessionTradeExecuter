package strategy

import (
	"math"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/e01"
)

// day は G/Q と弱い逆票拒否を旧 E01 の影へ接続します。
//
// 引数: o(Observation)、allow(bool)、out(*[]Signal)。
// 返り値: error は旧影の不整合です。
func (p *Portfolio) day(o Observation, allow bool, out *[]Signal) error {
	l := &p.Core
	d := p.Config.Parameters.Day
	direction := o.E01.Direction
	if !p.legacyBool("day.direction_agrees", direction == o.GateDirection, map[string]any{"direction": float64(direction), "gate": float64(o.GateDirection)}) {
		direction = 0
	}
	if finite(o.Last) && o.Last > 0 {
		if p.PaperInputOrigin < 0 {
			p.PaperInputOrigin = o.Index
		}
		p.PaperRows[o.Index] = p.Paper.Row + 1
		p.PaperRowQueue = append(p.PaperRowQueue, paperMapping{o.Index, p.Paper.Row + 1, o.Time})
		for len(p.PaperRowQueue) > 0 && p.PaperRowQueue[0].Time < o.Time-300 {
			delete(p.PaperRows, p.PaperRowQueue[0].Index)
			p.PaperRowQueue = p.PaperRowQueue[1:]
		}
		source, knownSource := p.PaperRows[o.E01.AdverseSource]
		adverse := o.E01.Adverse
		if !knownSource {
			source = -1
			adverse = 0
		}
		events, err := p.Paper.OnTick(e01.Tick{Time: o.Time, Price: o.Last, ATR: o.ATR, Direction: direction, Adverse: adverse, AdverseSource: source, AllowEntry: true, InputIndex: -1})
		if err != nil {
			return err
		}
		for _, event := range events {
			if event.Event == "新規" {
				m := o.DayMarket
				side := float64(event.Direction)
				quote := liquidation(o, -event.Direction)
				quality := quoteValid(o) && event.InitialATR > 0 && p.legacyBool("day.entry_quote", o.Ask-o.Bid <= d.QuoteSpreadATR*event.InitialATR && side*(quote-o.Last) <= d.QuoteDistanceATR*event.InitialATR, map[string]any{"spread": o.Ask - o.Bid, "spread_atr": d.QuoteSpreadATR, "atr": event.InitialATR, "signed_distance": side * (quote - o.Last), "distance_atr": d.QuoteDistanceATR})
				support := m.Known && m.Own30Known && m.Peer30Known && p.legacyBool("day.entry_support", side*m.Own30 >= d.WeakSupport && side*m.Peer30 >= d.WeakSupport, map[string]any{"own30": side * m.Own30, "peer30": side * m.Peer30, "threshold": d.WeakSupport})
				if allow && quality && support && l.Position.Quantity == 0 && l.Pending == "" {
					if s := p.emit(l, o, "new", event.Direction, p.Config.CoreQuantity, event.ReasonCode, event.InitialATR, 0, event.Campaign); s != nil {
						l.PaperCampaign = event.Campaign
						l.PaperRemaining = event.Quantity
						l.CloseTarget = -1
						*out = append(*out, *s)
					}
				}
			} else if event.Event == "返済" && event.Campaign == l.PaperCampaign {
				l.PaperRemaining = max(0, l.PaperRemaining-event.Quantity)
				target := int(math.Floor(float64(l.PlannedQuantity*l.PaperRemaining)/float64(p.Config.CoreQuantity) + .5))
				requestClose(l, target, event.ReasonCode)
			}
		}
	}
	// ----------------------------------------
	// 実玉の停止幅は縮めるだけとし、未知 ATR で元へ戻しません。
	pos := &l.Position
	good := quoteValid(o)
	m := o.DayMarket
	if pos.Quantity == 0 {
		return nil
	}
	if !good || !m.Known || (pos.LastValidTime > 0 && o.Time-pos.LastValidTime > d.MaximumGapSeconds) {
		pos.FailureSince = 0
	}
	if !good {
		return nil
	}
	if !pos.AnchorInitialized && o.Received >= pos.EntryTime {
		// 約定後初観測で相手が未知なら、この親の二市場アンカーは利用しません。
		if m.Known && m.OwnATR > 0 && m.PeerATR > 0 {
			pos.AnchorOwn = m.Own
			pos.AnchorPeer = m.Peer
			pos.AnchorPeerATR = m.PeerATR
			pos.AnchorPeerTime = m.PeerTime
			pos.AnchorKnown = true
		}
		pos.EntryIndex = o.Index
		pos.AnchorInitialized = true
	}
	if o.ATR > 0 && finite(o.ATR) {
		fraction := p.legacyNumber("day.risk_fraction", math.Max(d.RiskFloor, math.Min(1, o.ATR/pos.Risk)), map[string]any{"minimum": d.RiskFloor, "ratio": o.ATR / pos.Risk})
		pos.RiskFraction = math.Min(pos.RiskFraction, math.Max(0, math.Min(1, fraction)))
	}
	equity := pos.Realized/p.Config.PointValue + float64(pos.Quantity*pos.Side)*(liquidation(o, pos.Side)-pos.Average)
	if p.legacyBool("day.loss", equity <= -float64(pos.InitialQuantity)*pos.Risk*pos.RiskFraction, map[string]any{"equity": equity, "initial_quantity": float64(pos.InitialQuantity), "risk": pos.Risk, "fraction": pos.RiskFraction}) {
		requestClose(l, 0, 32)
	}
	adverse := false
	if pos.AnchorKnown && m.Known && o.Index > pos.EntryIndex && m.PeerTime > pos.AnchorPeerTime {
		own := float64(pos.Side) * (m.Own - pos.AnchorOwn) / pos.Risk
		peer := float64(pos.Side) * (m.Peer - pos.AnchorPeer) / pos.AnchorPeerATR
		adverse = p.legacyBool("day.adverse", own <= -d.WithdrawalATR && peer <= -d.WithdrawalATR, map[string]any{"own": own, "peer": peer, "threshold": d.WithdrawalATR})
	}
	if adverse {
		if pos.FailureSince == 0 {
			pos.FailureSince = o.Time
		}
		if p.legacyBool("day.adverse_confirm", o.Time-pos.FailureSince >= d.PersistenceSeconds, map[string]any{"elapsed": o.Time - pos.FailureSince, "threshold": d.PersistenceSeconds}) {
			requestClose(l, 0, 75)
		}
	} else {
		pos.FailureSince = 0
	}
	pos.LastValidTime = o.Time
	return nil
}

// night は二市場の進行確認又は固定境界の再試験を実行します。
//
// 引数: o(Observation)、allow(bool)、out(*[]Signal)。返り値: なし。
func (p *Portfolio) night(o Observation, allow bool, out *[]Signal) {
	l := &p.Core
	n := p.Config.Parameters.Night
	price, m, h := o.Last, o.LastMarket, o.LastHistory
	if n.Retest {
		price = (o.Bid + o.Ask) * .5
		m = o.MidMarket
		h = o.MidHistory
	}
	good := quoteValid(o)
	quiet := o.Time >= p.Session.USOpen-n.QuietSeconds && o.Time < p.Session.USOpen+n.QuietSeconds
	terminal := o.Time >= p.Session.ForceClose
	if quiet || terminal {
		l.Arm = armState{}
	}
	if l.Position.Quantity > 0 {
		reason := 0
		if terminal {
			reason = 93
		} else if quiet {
			reason = 94
		} else {
			reason = p.legacyTrail(&l.Position, o, n.TrailStartR, n.TrailWidthR, n.StopR)
			if reason == 0 && good && m.Known && h.Known && p.legacyBool("night.boundary_exit", float64(l.Position.Side)*(price-l.Position.Boundary) < -n.BoundaryBreakR*l.Position.Risk && float64(l.Position.Side)*h.Values[11] < 0, map[string]any{"distance": float64(l.Position.Side) * (price - l.Position.Boundary), "break_r": n.BoundaryBreakR, "risk": l.Position.Risk, "direction": float64(l.Position.Side) * h.Values[11]}) {
				reason = 72
			}
		}
		if reason != 0 {
			requestClose(l, 0, reason)
		}
		return
	}
	// ----------------------------------------
	// 新規許可の停止中も、既に観測した境界破綻と寿命切れは準備へ反映します。
	// 通常許可中の段階進行・取消し順序は元の数値核のまま維持します。
	if !allow && l.Arm.Side != 0 {
		expired := o.Time-l.Arm.At > n.ArmLifetimeSeconds
		broken := false
		if good && finite(price) {
			advance := float64(l.Arm.Side) * (price - l.Arm.Level)
			if n.Retest {
				broken = advance <= -n.BoundaryBreakR*l.Arm.Risk
			} else {
				broken = advance < -n.BoundaryBreakR*l.Arm.Risk
			}
		}
		if expired || broken {
			l.Arm = armState{}
		}
	}
	if l.Pending != "" || !allow || !good || quiet || terminal || o.Time < l.ReadyAfter {
		return
	}
	a := &l.Arm
	if n.Retest && a.Side != 0 && float64(a.Side)*(price-a.Level) <= -n.BoundaryBreakR*a.Risk {
		*a = armState{}
	}
	known := price > 0 && finite(price) && o.ATR > 0 && finite(o.ATR) && h.Known && m.Known && m.Own30Known && m.Peer30Known
	if !n.Retest {
		known = known && m.Own120Known && m.Peer120Known
	}
	coreStart := p.Session.USOpen + n.PostOpenPrepareSeconds
	if p.Session.NightCoreStart > 0 {
		coreStart = p.Session.NightCoreStart
	}
	if !known || o.Time < coreStart {
		return
	}
	w := math.Max(o.ATR, n.MinimumTicks*p.Config.LargeTick)
	quality := p.legacyBool("night.entry_quote", o.Ask-o.Bid <= n.QuoteSpreadR*w && math.Max(o.Ask-price, price-o.Bid) <= n.QuoteDistanceR*w, map[string]any{"spread": o.Ask - o.Bid, "spread_r": n.QuoteSpreadR, "risk": w, "distance": math.Max(o.Ask-price, price-o.Bid), "distance_r": n.QuoteDistanceR})
	if a.Side != 0 && o.Time-a.At > n.ArmLifetimeSeconds {
		*a = armState{}
	}
	if a.Side == 0 {
		if !quality || !p.legacyBool("night.efficiency", h.Values[5] >= n.Efficiency, map[string]any{"efficiency": h.Values[5], "threshold": n.Efficiency}) {
			return
		}
		side := 0
		line := 0.0
		if price > h.Values[3] && h.Values[10] > 0 && h.Values[11] > 0 {
			side = 1
		} else if price < h.Values[4] && h.Values[10] < 0 && h.Values[11] < 0 {
			side = -1
		}
		direction := p.legacyNumber("night.entry_direction", float64(side), map[string]any{"price": price, "high": h.Values[3], "low": h.Values[4], "own": h.Values[10], "peer": h.Values[11]})
		side = 0
		if direction == 1 {
			side, line = 1, h.Values[3]
		} else if direction == -1 {
			side, line = -1, h.Values[4]
		}
		if side != 0 {
			*a = armState{Side: side, Level: line, Risk: w, At: o.Time, ReturnExtreme: price, ProgressIndex: -1, ReturnIndex: -1}
			if n.Retest && p.legacyBool("night.retest_progress", float64(side)*(price-line) >= n.RetestProgressR*w, map[string]any{"advance": float64(side) * (price - line), "progress_r": n.RetestProgressR, "risk": w}) {
				a.Stage = 1
				a.ProgressIndex = o.Index
			}
		}
		return
	}
	d := float64(a.Side)
	advance := d * (price - a.Level)
	support := p.legacyBool("night.support30", d*m.Own30 > 0 && d*m.Peer30 > 0, map[string]any{"own": d * m.Own30, "peer": d * m.Peer30})
	trigger := false
	if !n.Retest {
		if advance < -n.BoundaryBreakR*a.Risk {
			*a = armState{}
			return
		}
		trigger = support && p.legacyBool("night.progress", o.Time-a.At >= n.ConfirmSeconds && advance >= n.ProgressR*a.Risk, map[string]any{"elapsed": o.Time - a.At, "confirm_seconds": n.ConfirmSeconds, "advance": advance, "progress_r": n.ProgressR, "risk": a.Risk})
	} else {
		if advance <= -n.BoundaryBreakR*a.Risk {
			*a = armState{}
			return
		}
		switch a.Stage {
		case 0:
			if p.legacyBool("night.retest_progress", advance >= n.RetestProgressR*a.Risk, map[string]any{"advance": advance, "progress_r": n.RetestProgressR, "risk": a.Risk}) {
				a.Stage = 1
				a.ProgressIndex = o.Index
			}
		case 1:
			if o.Index > a.ProgressIndex && p.legacyBool("night.retest_return", advance <= n.RetestReturnR*a.Risk, map[string]any{"advance": advance, "return_r": n.RetestReturnR, "risk": a.Risk}) {
				a.Stage = 2
				a.ReturnIndex = o.Index
				a.ReturnExtreme = price
			}
		default:
			if d*(price-a.ReturnExtreme) < 0 {
				a.ReturnExtreme = price
			}
			trigger = o.Index > a.ReturnIndex && support && p.legacyBool("night.retest_bounce", o.Time-a.At >= n.ConfirmSeconds && d*(price-a.ReturnExtreme) >= n.BounceR*a.Risk, map[string]any{"elapsed": o.Time - a.At, "confirm_seconds": n.ConfirmSeconds, "rebound": d * (price - a.ReturnExtreme), "bounce_r": n.BounceR, "risk": a.Risk})
		}
	}
	if !trigger {
		return
	}
	if !n.Retest {
		trigger = p.legacyBool("night.support120", d*m.Own120 > n.LongSupportR && d*m.Peer120 > n.LongSupportR, map[string]any{"own": d * m.Own120, "peer": d * m.Peer120, "threshold": n.LongSupportR})
	}
	trigger = trigger && p.legacyBool("night.direction_still", d*h.Values[10] > 0 && d*h.Values[11] > 0, map[string]any{"own": d * h.Values[10], "peer": d * h.Values[11]})
	if n.Retest && m.Own120Known && m.Peer120Known && p.legacyBool("night.opposition120", d*m.Own120 < -n.LongOppositionR && d*m.Peer120 < -n.LongOppositionR, map[string]any{"own": d * m.Own120, "peer": d * m.Peer120, "threshold": n.LongOppositionR}) {
		if trigger && quality {
			*a = armState{}
		}
		return
	}
	if trigger && quality {
		if s := p.emit(l, o, "new", a.Side, p.Config.CoreQuantity, 1, a.Risk, a.Level, int(p.Sequence+1)); s != nil {
			l.CloseTarget = -1
			*out = append(*out, *s)
		}
	}
}

// extraDirection は二市場の方向票を合成します。
//
// 引数: h(History)、mid(float64)、style(int)。返り値: int は方向です。
func extraDirection(h History, mid float64, style int) int {
	v := h.Values
	score := 0.0
	if style == 0 {
		score = sign(v[7]) + sign(v[11]) + .5*(sign(v[6])+sign(v[10]))
	} else {
		score = sign(mid-v[8]) + sign(v[13])
		if score == 0 {
			score = sign(v[7]) + sign(v[11])
		}
	}
	if score == 0 {
		score = sign(v[7])
	}
	if score == 0 {
		score = sign(mid - v[8])
	}
	return int(sign(score))
}

// extra は過去に主担当がまだ一度も建てていない場合だけ一親を補います。
//
// 引数: o(Observation)、allow(bool)、out(*[]Signal)。返り値: なし。
func (p *Portfolio) extra(o Observation, allow bool, out *[]Signal) {
	l := &p.Extra
	x := p.Config.Parameters.Extra
	if l.Position.Quantity > 0 {
		if reason := p.legacyTrail(&l.Position, o, x.TrailStartR, x.TrailWidthR, x.StopR); reason != 0 {
			requestClose(l, 0, reason)
		}
		return
	}
	// 未約定の主注文を未来の参加と数えません。二担当の同時予約も片方向上限で拘束します。
	if !allow || p.ExtraAttempted || p.CoreEntered || l.Pending != "" || !quoteValid(o) || !o.MidHistory.Known || o.ATR <= 0 {
		return
	}
	if o.Time < p.Session.ExtraAt || o.Time >= p.Session.ForceClose-x.EntryCutoffSeconds {
		return
	}
	h := o.MidHistory
	r := math.Max(o.ATR, math.Max(x.BandFraction*(h.Values[3]-h.Values[4]), x.MinimumTicks*p.Config.LargeTick))
	r = math.Max(p.Config.LargeTick, p.legacyNumber("extra.risk", r, map[string]any{"atr": o.ATR, "band_fraction": x.BandFraction, "band_width": h.Values[3] - h.Values[4], "minimum_ticks": x.MinimumTicks, "tick": p.Config.LargeTick}))
	side := p.legacyExtraDirection(h, (o.Bid+o.Ask)*.5, x.Style)
	if side == 0 || !p.legacyBool("extra.quote", o.Ask-o.Bid <= x.QuoteSpreadR*r, map[string]any{"spread": o.Ask - o.Bid, "spread_r": x.QuoteSpreadR, "risk": r}) {
		return
	}
	q := p.Config.ExtraMaxQuantity
	if x.BudgetTicks > 0 {
		q = max(1, min(q, int(float64(q)*x.BudgetTicks*p.Config.LargeTick/r)))
	}
	if s := p.emit(l, o, "new", side, q, 82+x.Style, r, 0, int(p.Sequence+1)); s != nil {
		p.ExtraAttempted = true
		l.CloseTarget = -1
		*out = append(*out, *s)
	}
}
