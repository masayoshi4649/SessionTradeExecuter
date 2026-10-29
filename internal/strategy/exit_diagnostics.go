package strategy

import "math"

// ExitDiagnostics は実玉と仮想候補を区別した小型の出口診断を返します。
//
// 引数: o(Observation)は最後に採用したラージ板です。
// 返り値: []map[string]any は担当別の既知閾値・実枚数です。注文や状態を変更しません。
// reference_unrealized_yen はラージ気配による参考値で、発注商品の評価損益と区別します。
func (p *Portfolio) ExitDiagnostics(o Observation) []map[string]any {
	if p != nil && p.Selected != nil {
		return p.diagnosticsSelected(o)
	}
	if p != nil && p.P05 != nil {
		return p.diagnosticsP05(o)
	}
	if p == nil || p.Research == nil {
		return nil
	}
	var out []map[string]any
	for _, s := range p.Research.Sleeves {
		d := map[string]any{"method": p.Research.Method, "owner": s.Name, "capacity": s.Capacity,
			"campaign": s.Campaign, "side": s.Side, "actual_quantity": s.held(), "fixed_r": s.FixedR,
			"realized_net_yen": s.Cash, "residual_stage": s.Stage, "profit_budget_yen": s.Budget,
			"retained_hold_id": s.Retained, "blocked": s.Blocked, "force_reason": s.ForceReason,
			"reference_time": o.Time, "reference_quote_valid": quoteValid(o)}
		d["strategy_cash_yen"] = p.researchCash(s)
		if p.S01 != nil {
			d["method"] = "S01"
			d["s01"] = p.s01Diagnostics()
		}
		d["strategy_fee_per_contract_yen"] = p.researchPolicyFee()
		if s.PartialProtection != nil {
			protect := s.PartialProtection
			floor := p.partialFloor145(protect.Best)
			d["partial_profit_protection"] = map[string]any{"started_at": protect.StartedAt,
				"anchor": protect.Anchor, "best_gain_points": protect.Best, "floor_gain_points": floor,
				"stop_price": protect.Anchor + float64(s.Side)*floor, "active": s.Stage == 0 && s.held() > 0}
		}
		if quoteValid(o) {
			mark, unrealized := liquidation(o, s.Side), 0.
			for _, l := range s.Lots {
				unrealized += float64(s.Side*l.Quantity) * (mark - l.Price) * p.Config.PointValue
			}
			d["reference_unrealized_yen"] = unrealized
		}
		if s.Source != nil && s.Source.Legacy != nil {
			legacy := s.Source.Legacy
			d["virtual_quantity"] = legacy.Core.Position.Quantity
			if legacy.Paper != nil {
				shadow := legacy.Paper.Exit
				gain := float64(shadow.Direction) * (shadow.Extreme - shadow.Anchor)
				width := legacy.Paper.Config.NormalTrailATR
				if gain >= legacy.Paper.Config.TightArmATR*shadow.ATR {
					width = legacy.Paper.Config.TightTrailATR
				}
				levels := map[string]any{"anchor": shadow.Anchor, "atr": shadow.ATR,
					"virtual_quantity": shadow.Quantity, "virtual_realized_points": shadow.Realized,
					"best_gain_points": gain, "trail_start_points": legacy.Paper.Config.TrailArmATR * shadow.ATR,
					"trail_width_points": width * shadow.ATR, "tight_start_points": legacy.Paper.Config.TightArmATR * shadow.ATR,
					"partial_profit_points": legacy.Paper.Config.SplitATR * shadow.ATR, "partial_done_virtual": shadow.SplitDone,
					"trail_price": shadow.TrailFloor}
				if shadow.Plan != nil {
					for _, level := range shadow.Plan.Levels {
						if level.Reason == 1 {
							levels["stop_price"] = level.Price
						}
					}
				}
				d["core_exit_shadow"] = levels
			}
		}
		if s.Source != nil && s.Source.Active != nil {
			active := s.Source.Active
			b := active.Book
			width := active.activeNumber("trail_width", .75*b.Risk, map[string]any{"risk": b.Risk})
			protecting := b.total() > 0 && active.activeBool("trail_active", b.Best >= 1.25*b.Risk, map[string]any{"best": b.Best, "risk": b.Risk})
			levels := map[string]any{"virtual_quantity": b.total(), "anchor": b.Base, "risk_points": b.Risk,
				"best_gain_points": b.Best, "trail_start_points": 1.25 * b.Risk, "trail_width_points": .75 * b.Risk,
				"profit_protection_active": protecting,
				"stop_price":               b.Base + float64(b.Side)*b.Floor, "floor_gain_points": b.Floor,
				"adverse_management": active.Management}
			levels["trail_width_points"] = width
			if active.ResearchRules {
				delete(levels, "trail_start_points")
				levels["trail_activation_rule"] = "research.active.trail_active"
			}
			d["active_exit"] = levels
		}
		if s.Source != nil && s.Source.Active145 != nil {
			active := s.Source.Active145
			b := active.Book
			width := active.Rules.Number("addon.trail_width", map[string]any{"night": p.Session.Kind == "Night", "unit": active.ExitRisk})
			protecting := b.total() > 0 && active.Rules.Bool("addon.trail_active", map[string]any{"best": b.Best, "unit": active.ExitRisk})
			d["active_exit"] = map[string]any{"virtual_quantity": b.total(), "anchor": b.Base, "risk_points": b.Risk,
				"profit_unit_points": active.ExitRisk, "best_gain_points": b.Best, "trail_activation_rule": "addon.trail_active",
				"trail_width_points": width, "profit_protection_active": protecting,
				"stop_price": b.Base + float64(b.Side)*b.Floor, "floor_gain_points": b.Floor,
				"partial_done_virtual": active.PartialDone, "rotation_waiting": active.Rotation,
				"rotation_anchor": active.RotationAnchor, "rotation_extreme": active.RotationExtreme,
				"reentry_ready_at": active.NextReady, "adverse_management": active.Management}
		}
		if s.Source != nil && s.Source.Episode != nil {
			e := s.Source.Episode
			b := e.Book
			width := e.episodeNumber("trail_width", math.Max(8*p.Config.LargeTick, .5*e.BandWidth), map[string]any{"tick": p.Config.LargeTick, "band_width": e.BandWidth})
			levels := map[string]any{"virtual_quantity": b.total(), "anchor": b.Base, "risk_points": b.Risk,
				"center": e.Center, "partial_done": e.PartialDone, "best_gain_points": b.Best,
				"trail_start_points": b.Risk, "trail_width_points": width,
				"stop_price": b.Base + float64(b.Side)*b.Floor}
			if e.ResearchRules {
				delete(levels, "trail_start_points")
				levels["trail_activation_rule"] = "research.episode.trail_active"
				levels["profit_protection_active"] = b.total() > 0 && e.episodeBool("trail_active", b.Best >= b.Risk, map[string]any{"best": b.Best, "risk": b.Risk})
			}
			d["episode_exit"] = levels
		}
		if s.Stage == 2 {
			for _, l := range s.Lots {
				if l.ID == s.Retained && l.Quantity > 0 {
					width, capped := p.researchResidualWidth(s)
					floor := math.Max((math.Floor(p.researchPolicyFee())-s.Budget)/p.Config.PointValue, s.Best-width)
					d["residual_exit"] = map[string]any{"best_gain_points": s.Best, "trail_width_points": width, "width_capped": capped,
						"floor_gain_points": floor, "stop_price": l.Price + float64(s.Side)*floor}
				}
			}
		}
		out = append(out, d)
	}
	return out
}
