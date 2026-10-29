package strategy

import (
	"errors"
	"math"
)

// restoreSelected は所有・注文・元台帳の対応を検査して固定元手法を復元します。
//
// 引数: なし。返り値: error。アプリの新規起動はこの保存玉を引き継ぎません。
func (p *Portfolio) restoreSelected() error {
	r := p.Selected
	if r.Version != 1 || r.Shadow == nil || r.SourceLots == nil || r.Intents == nil || r.ForcedOwners == nil || p.Config.Reference != "NK225" {
		return errors.New("選定手法の保存版又は台帳が不正です")
	}
	day := p.Config.Parameters.Method == "D164_15"
	if day != (r.Day != nil) || day == (r.Night != nil) {
		return errors.New("選定手法の政策が不一致です")
	}
	base := "D07"
	if day {
		base = "S01"
	}
	if p.Config.Parameters.Method == "P164_06" {
		base = "P05"
	}
	if r.Shadow.Config.Parameters.Method != base || r.Shadow.Config.Cap != 10 || r.Shadow.Config.CoreQuantity != 5 || r.Shadow.Config.ExtraMaxQuantity != 5 || r.Shadow.Config.Product != "NK225mini" || r.Shadow.Session.ID != p.Session.ID {
		return errors.New("選定元手法の固定配分又はセッションが不一致です")
	}
	raw, err := r.Shadow.Dump()
	if err != nil {
		return err
	}
	r.Shadow, err = Restore(raw)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range r.SourceOrder {
		l := r.SourceLots[id]
		if seen[id] || l == nil || id != l.SourceID+"-玉" || l.Parent == "" || l.RemainingMicro < 0 || l.RemainingMicro > 100 || (l.Side != 1 && l.Side != -1) {
			return errors.New("選定元玉の識別子又は残量が不正です")
		}
		seen[id] = true
	}
	if len(seen) != len(r.SourceLots) {
		return errors.New("選定元玉の順序一覧が不一致です")
	}
	seen = map[string]bool{}
	for _, l := range r.Lots {
		order := p.Orders[l.EntryID]
		if l.ID == "" || seen[l.ID] || l.HoldID == "" || l.SourceID == "" || l.Parent == "" || l.Quantity < 0 || l.Quantity > l.InitialQuantity || l.Goal < 0 || l.Goal > l.Quantity || !finite(l.Entry) || l.Entry <= 0 || !finite(l.BestGain) || l.BestGain < 0 || order == nil || order.Signal.Action != "new" || order.Signal.Side != l.Side || order.Signal.Owner != l.Owner {
			return errors.New("選定実玉の所有又は残量が不正です")
		}
		seen[l.ID] = true
	}
	for id, order := range p.Orders {
		in, ok := r.Intents[id]
		if order == nil || !ok || order.Filled < 0 || order.Filled > order.Signal.Quantity || order.Signal.ID != id || in.Quantity != order.Signal.Quantity {
			return errors.New("選定注文と政策予約が不一致です")
		}
		if order.Signal.Action == "close" && !seen[in.ActualID] {
			return errors.New("選定返済対象が保存玉にありません")
		}
	}
	s := p.selectedSnapshot()
	for _, side := range []int{-1, 1} {
		if s.SideAvailable[side] < 0 {
			return errors.New("選定保存残高と新規予約が片方向上限を超えています")
		}
	}
	for _, l := range r.Lots {
		if s.ReservedClose[l.ID] > l.Quantity {
			return errors.New("選定返済予約が実残量を超えています")
		}
	}
	for _, sig := range r.ShadowPending {
		order := r.Shadow.Orders[sig.ID]
		if order == nil || order.Done || order.Filled != 0 || order.Signal.Quantity != sig.Quantity {
			return errors.New("選定元手法の未約定予約が不一致です")
		}
	}
	return p.validateSelectedCloseGroups()
}

// diagnosticsSelected は元通知と実玉保護を小容量で表示します。
//
// 引数: o(Observation)。返り値: []map[string]any。状態は更新しません。
func (p *Portfolio) diagnosticsSelected(o Observation) []map[string]any {
	var out []map[string]any
	for _, l := range p.Selected.Lots {
		if l.Quantity <= 0 {
			continue
		}
		v := map[string]any{"method": "NK225_" + p.Config.Parameters.Method, "owner": l.Owner, "actual_execution_id": l.ID, "hold_id": l.HoldID, "source_id": l.SourceID, "parent": l.Parent, "kind": l.Kind, "actual_quantity": l.Quantity, "side": l.Side, "entry_price": l.Entry, "best_gain_points": l.BestGain, "close_requested_quantity": l.Goal, "close_reason": l.Reason}
		if quoteValid(o) {
			v["reference_gain_points"] = float64(l.Side) * (liquidation(o, l.Side) - l.Entry)
		}
		if n := p.Selected.Night; n != nil {
			if state := n.Lots[l.ID]; state != nil {
				v["profit_protection_active"] = state.Activated
				v["residual_eligible"] = state.ResidualEligible
				v["floor_gain_points"] = math.Max(0, state.Floor)
			}
		}
		if d := p.Selected.Day; d != nil {
			if state := d.Protection[l.ID]; state != nil {
				v["residual_eligible"], v["profit_protection_active"] = state.Eligible, state.Active
				v["floor_gain_points"] = 0
			}
		}
		out = append(out, v)
	}
	return out
}

// SelectedState は現在の不変手法と実際の待機状態を表示へ返します。
//
// 引数: なし。返り値: string。注文や政策状態を更新しません。
func (p *Portfolio) SelectedState() string {
	if p == nil || p.Selected == nil {
		return ""
	}
	r := p.Selected
	prefix := "NK225_" + p.Config.Parameters.Method + "："
	if r.Forced != "" {
		return prefix + "時間枠終了・所有玉の返済を確認中"
	}
	if r.Day != nil {
		for _, parent := range r.Day.Parents {
			if parent.Deferred {
				return prefix + "初回失速後の再加速を確認中"
			}
		}
		return prefix + "支持量・初回短期確認・残玉保護を監視中"
	}
	if r.Night != nil {
		for _, parent := range r.Night.Parents {
			if parent.Deferred {
				return prefix + "反対板を確認・自方向の新価格帯を待機中"
			}
		}
		if p.Config.Parameters.Method == "P164_06" {
			return prefix + "米国開始前の新規・小口利益保護を監視中"
		}
		return prefix + "米国開始後の新規と板の価格帯を監視中"
	}
	return prefix + "判断を監視中"
}
