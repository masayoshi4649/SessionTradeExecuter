package strategy

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

// selectedSnapshot は当起動の所有と予約をコピーし、政策へ渡します。
//
// 引数: なし。返り値: SelectedSnapshot。数量は明記した正規化値以外は実商品枚数です。
func (p *Portfolio) selectedSnapshot() SelectedSnapshot {
	r := p.Selected
	s := SelectedSnapshot{Cap: p.Config.Cap, Lots: append([]SelectedLot(nil), r.Lots...), ReservedClose: map[string]int{}, PendingParents: map[string]bool{}, ClosingParents: map[string]bool{}, SourceRemaining: map[string]int{}, ParentRemaining: map[string]int{}, SideAvailable: map[int]int{1: p.Config.Cap, -1: p.Config.Cap}}
	for _, l := range r.Lots {
		s.SideAvailable[l.Side] -= l.Quantity
	}
	for id, ord := range p.Orders {
		if ord.Done || ord.Filled >= ord.Signal.Quantity {
			continue
		}
		in := r.Intents[id]
		left := ord.Signal.Quantity - ord.Filled
		s.PendingParents[in.Parent] = true
		if ord.Signal.Action == "new" {
			s.SideAvailable[ord.Signal.Side] -= left
		} else {
			s.ClosingParents[in.Parent] = true
			s.ReservedClose[in.ActualID] += left
		}
	}
	for _, id := range r.SourceOrder {
		l := r.SourceLots[id]
		s.SourceRemaining[l.SourceID] = l.RemainingMicro
		s.ParentRemaining[l.Parent] += l.RemainingMicro
	}
	return s
}

// selectedLot は実約定IDで当起動の玉を特定します。
//
// 引数: id(string)。返り値: *SelectedLot。対象外はnilです。
func (p *Portfolio) selectedLot(id string) *SelectedLot {
	for i := range p.Selected.Lots {
		if p.Selected.Lots[i].ID == id {
			return &p.Selected.Lots[i]
		}
	}
	return nil
}

// emitSelected は実商品整数枚へ変換し、所有IDと片方向予約上限を検査します。
//
// 引数: o(Observation)、in(SelectedIntent)。返り値: *Signalとerror。0枚希望は発注しません。
func (p *Portfolio) emitSelected(o Observation, in SelectedIntent) (*Signal, error) {
	snapshot := p.selectedSnapshot()
	quantity := in.Quantity
	action := "new"
	var targets []LotTarget
	if in.ActualID != "" {
		action = "close"
		l := p.selectedLot(in.ActualID)
		if l == nil {
			return nil, errors.New("選定返済の実約定IDがありません")
		}
		if in.CloseGroupID != "" {
			g := p.Selected.CloseGroups[in.CloseGroupID]
			if g == nil || g.Parent != l.Parent {
				return nil, errors.New("返済群と実玉の親が不一致です")
			}
		}
		if quantity <= 0 {
			return nil, nil
		}
		quantity = min(quantity, l.Quantity-snapshot.ReservedClose[l.ID])
		in.Parent, in.Owner, in.SourceID, in.Side, in.Campaign = l.Parent, l.Owner, l.SourceID, -l.Side, l.Campaign
		if quantity > 0 {
			targets = []LotTarget{{ID: l.HoldID, Quantity: quantity}}
		}
	} else {
		if in.Side != 1 && in.Side != -1 {
			return nil, errors.New("選定新規方向が不正です")
		}
		if p.Selected.Forced != "" || p.Selected.ForcedOwners[in.Owner] != "" || o.Time >= p.selectedDeadline() {
			return nil, nil
		}
		if quantity == 0 {
			quantity = int(math.Floor(float64(in.QuantityMicro)*float64(p.Config.Cap)/100 + 1e-10))
		}
		quantity = min(quantity, snapshot.SideAvailable[in.Side])
	}
	if quantity <= 0 {
		return nil, nil
	}
	p.Sequence++
	id := fmt.Sprintf("%s-%s-%s-%06d", p.Session.ID, p.Config.Product, p.Config.Parameters.Method, p.Sequence)
	if p.Config.Namespace != "" {
		id = p.Config.Namespace + "-" + id
	}
	in.Quantity = quantity
	evidence, err := json.Marshal(map[string]any{"method": "NK225_" + p.Config.Parameters.Method, "parent": in.Parent, "source_id": in.SourceID, "policy_id": in.PolicyID, "close_group_id": in.CloseGroupID, "actual_execution_id": in.ActualID, "kind": in.Kind, "quantity_micro": in.QuantityMicro, "actual_quantity": quantity, "cap": p.Config.Cap, "large_bid": o.Bid, "large_ask": o.Ask})
	if err != nil {
		return nil, err
	}
	s := Signal{ID: id, Owner: in.Owner, Action: action, Side: in.Side, Quantity: quantity, Time: o.Time, SourceIndex: o.Index, Reason: in.Reason, ReasonCode: 166, Campaign: in.Campaign, Evidence: evidence, Targets: targets}
	p.Orders[id] = &orderState{Signal: s}
	p.Selected.Intents[id] = in
	return &s, nil
}

// fillSelected は証券会社の確認済み約定を個別所有玉へ反映します。
//
// 引数: f(Fill)、order(*orderState)。返り値: error。全所有検査を更新前に完了します。
func (p *Portfolio) fillSelected(f Fill, order *orderState) error {
	r := p.Selected
	in, ok := r.Intents[f.SignalID]
	if !ok {
		return errors.New("選定注文の政策対応がありません")
	}
	sig := order.Signal
	gross := 0.0
	lotID := f.ExecutionID
	if sig.Action == "new" {
		if f.HoldID == "" || len(f.Closed) > 0 {
			return errors.New("選定新規の建玉ID又は返済内訳が不正です")
		}
		if p.selectedLot(lotID) != nil {
			return errors.New("選定実約定IDが重複しました")
		}
		l := SelectedLot{ID: lotID, HoldID: f.HoldID, EntryID: f.SignalID, Parent: in.Parent, SourceID: in.SourceID, Owner: sig.Owner, Kind: in.Kind, Campaign: sig.Campaign, Side: sig.Side, Quantity: f.Quantity, InitialQuantity: f.Quantity, Entry: f.Price, At: f.Time, Fees: f.Fee}
		if r.Forced != "" {
			l.Goal = l.Quantity
			l.Reason = r.Forced
		} else if reason := r.ForcedOwners[l.Owner]; reason != "" {
			l.Goal = l.Quantity
			l.Reason = reason
		} else if source := r.SourceLots[in.SourceID+"-玉"]; source != nil && source.RemainingMicro == 0 {
			l.Goal = l.Quantity
			l.Reason = "元手法終了後の遅着約定"
		}
		r.Lots = append(r.Lots, l)
		if sig.Owner == "core" {
			p.CoreEntered = true
		} else {
			p.ExtraAttempted = true
		}
	} else {
		l := p.selectedLot(in.ActualID)
		lotID = in.ActualID
		if l == nil || l.Side != -sig.Side || l.Owner != sig.Owner || l.Quantity < f.Quantity {
			return errors.New("選定返済の所有・方向・残量が不一致です")
		}
		if len(sig.Targets) != 1 || sig.Targets[0].ID != l.HoldID {
			return errors.New("選定返済の発注先建玉IDが不一致です")
		}
		q := 0
		for _, target := range f.Closed {
			if target.ID != l.HoldID || target.Quantity <= 0 {
				return errors.New("選定返済に他の建玉IDがあります")
			}
			q += target.Quantity
		}
		if len(f.Closed) > 0 && q != f.Quantity {
			return errors.New("選定返済内訳の枚数が不一致です")
		}
		if in.CloseGroupID != "" {
			g := r.CloseGroups[in.CloseGroupID]
			available := 0
			for _, goal := range l.GoalGroups {
				if goal.GroupID == in.CloseGroupID {
					available += goal.Quantity
				}
			}
			if g == nil || g.Parent != l.Parent || g.Filled+f.Quantity > g.Expected || available < f.Quantity {
				return errors.New("返済群の約定数量が未完了量を超えました")
			}
		}
		gross = float64(l.Side*f.Quantity) * (f.Price - l.Entry) * p.Config.PointValue
		consumeSelectedCloseGoal(l, in.CloseGroupID, f.Quantity)
		l.Realized += gross
		l.Fees += f.Fee
		l.Quantity -= f.Quantity
	}
	order.Filled += f.Quantity
	p.Executions[f.ExecutionID] = f
	e := SelectedFillEvent{ExecutionID: f.ExecutionID, Intent: in, LotID: lotID, Quantity: f.Quantity, Gross: gross, Time: f.Time}
	o := r.Last
	o.Time = f.Time
	if in.CloseGroupID != "" {
		group := r.CloseGroups[in.CloseGroupID]
		group.Filled += f.Quantity
		group.Gross += gross
		if r.Night != nil && group.Expected > 0 && group.Filled == group.Expected && !group.Qualified {
			qualified := r.Night.qualifySelectedNight(group.Parent, group.Reason, group.Gross, p.selectedParentRemaining(group.Parent), o, p.selectedNightLots())
			group.Qualified = len(qualified) > 0
		}
	}
	if r.Day != nil {
		return r.Day.FillSelectedDay(e, o, p.selectedSnapshot())
	}
	return nil
}

// doneSelected は取消済み未約定量の政策予約を解放します。
//
// 引数: order(*orderState)。返り値: error。
func (p *Portfolio) doneSelected(order *orderState) error {
	if p.Selected.Day != nil && p.Selected.Intents[order.Signal.ID].PolicyID != "" {
		return p.Selected.Day.DoneSelectedDay(p.Selected.Intents[order.Signal.ID].PolicyID, p.selectedSnapshot())
	}
	return nil
}

// flattenSelected は当起動の実玉だけに永続する全返済要求を記録します。
//
// 引数: owner(string、空は全担当)、at(float64)、reason(string)。返り値: []Signal。
func (p *Portfolio) flattenSelected(owner string, at float64, reason string) []Signal {
	if owner == "" {
		p.Selected.Forced = reason
	} else {
		p.Selected.ForcedOwners[owner] = reason
	}
	for i := range p.Selected.Lots {
		l := &p.Selected.Lots[i]
		if owner == "" || owner == l.Owner {
			l.Goal = l.Quantity
			l.Reason = reason
		}
	}
	o := Observation{Reference: p.Config.Reference, Time: at, Index: p.LastIndex}
	out, _ := p.flushSelectedGoals(o)
	return out
}

// flushSelectedGoals は取消や欠測を挟んでも確定済み返済希望を再送します。
//
// 引数: o(Observation)。返り値: []Signalとerror。
func (p *Portfolio) flushSelectedGoals(o Observation) ([]Signal, error) {
	var out []Signal
	for i := range p.Selected.Lots {
		l := &p.Selected.Lots[i]
		if l.Goal <= 0 {
			continue
		}
		normalizeSelectedCloseGoals(l)
		for _, goal := range l.GoalGroups {
			quantity := goal.Quantity - p.reservedSelectedCloseGroup(l.ID, goal.GroupID)
			if quantity <= 0 {
				continue
			}
			kind := "forced_close"
			if goal.GroupID != "" {
				kind = "source_close"
			}
			s, err := p.emitSelected(o, SelectedIntent{ActualID: l.ID, Quantity: min(quantity, l.Quantity), CloseGroupID: goal.GroupID, Kind: kind, Reason: goal.Reason})
			if err != nil {
				return nil, err
			}
			if s != nil {
				out = append(out, *s)
			}
		}
	}
	return out, nil
}

// statusSelected は所有実玉だけの損益と未約定依頼を表示へ返します。
//
// 引数: なし。返り値: Status。
func (p *Portfolio) statusSelected() Status {
	s := Status{CoreEntered: p.CoreEntered, ExtraAttempted: p.ExtraAttempted}
	for _, l := range p.Selected.Lots {
		side := l.Side
		if l.Quantity == 0 {
			side = 0
		}
		s.Positions = append(s.Positions, Position{Owner: l.Owner, Side: side, Quantity: l.Quantity, InitialQuantity: l.InitialQuantity, Average: l.Entry, EntryTime: l.At, Realized: l.Realized, Fees: l.Fees, Peak: l.BestGain, Campaign: l.Campaign})
	}
	for _, order := range p.Orders {
		if !order.Done {
			s.Pending = append(s.Pending, order.Signal)
		}
	}
	sort.Slice(s.Pending, func(i, j int) bool { return s.Pending[i].ID < s.Pending[j].ID })
	return s
}
