package strategy

import (
	"errors"
	"fmt"
	"math"
)

// normalizeSelectedCloseGoals は既存の無群返済希望を群別の未返済量へ対応させます。
//
// 引数: lot(*SelectedLot)。返り値: なし。既存の元返済群を強制返済で上書きしません。
func normalizeSelectedCloseGoals(lot *SelectedLot) {
	total := 0
	for _, goal := range lot.GoalGroups {
		total += goal.Quantity
	}
	if lot.Goal > total {
		additional := lot.Goal - total
		for i := range lot.GoalGroups {
			if lot.GoalGroups[i].GroupID == "" {
				lot.GoalGroups[i].Quantity += additional
				lot.GoalGroups[i].Reason = lot.Reason
				return
			}
		}
		lot.GoalGroups = append(lot.GoalGroups, SelectedCloseGoal{Reason: lot.Reason, Quantity: additional})
	}
}

// reservedSelectedCloseGroup は当該実玉・返済群だけの未約定予約量を返します。
//
// 引数: actualID/groupID(string)。返り値: int。取消済み注文と既約定分は含めません。
func (p *Portfolio) reservedSelectedCloseGroup(actualID, groupID string) int {
	quantity := 0
	for id, order := range p.Orders {
		if order.Done || order.Signal.Action != "close" {
			continue
		}
		intent := p.Selected.Intents[id]
		if intent.ActualID == actualID && intent.CloseGroupID == groupID {
			quantity += order.Signal.Quantity - order.Filled
		}
	}
	return quantity
}

// queueSelectedSourceClose は既予約と既希望を控除した新しい返済量を通知群へ追加します。
//
// 引数: event(SelectedSourceEvent)。返り値: error。源通知は一度だけ、源残量更新後に渡します。
func (p *Portfolio) queueSelectedSourceClose(event SelectedSourceEvent) error {
	if event.Signal.Action != "close" {
		return errors.New("元返済以外を返済群へ渡しました")
	}
	r := p.Selected
	if r.CloseGroups == nil {
		r.CloseGroups = map[string]*SelectedCloseGroup{}
	}
	groupID := event.Signal.ID + "/" + event.Parent
	if r.CloseGroups[groupID] != nil {
		return errors.New("元返済通知群が重複しました")
	}
	reason := fmt.Sprintf("source_partial/%d", event.Signal.ReasonCode)
	if event.ParentRemainingMicro == 0 {
		reason = fmt.Sprintf("source_end/%d", event.Signal.ReasonCode)
	}
	group := &SelectedCloseGroup{Parent: event.Parent, Reason: reason}
	r.CloseGroups[groupID] = group
	snapshot := p.selectedSnapshot()
	for _, target := range event.Targets {
		if target.BeforeMicro <= 0 || target.QuantityMicro <= 0 || target.QuantityMicro > target.BeforeMicro || target.RemainingMicro != target.BeforeMicro-target.QuantityMicro {
			return errors.New("元返済群の前後数量が不正です")
		}
		if r.Night != nil {
			r.Night.sourceRemainingSelectedNight(event.Parent, target.SourceID, target.RemainingMicro)
		}
		for i := range r.Lots {
			lot := &r.Lots[i]
			if lot.Parent != event.Parent || lot.SourceID != target.SourceID {
				continue
			}
			normalizeSelectedCloseGoals(lot)
			already := max(lot.Goal, snapshot.ReservedClose[lot.ID])
			quantity := lot.Quantity * target.QuantityMicro / target.BeforeMicro
			if target.RemainingMicro == 0 {
				quantity = lot.Quantity
			}
			quantity = min(quantity, max(0, lot.Quantity-already))
			if quantity <= 0 {
				continue
			}
			lot.Goal += quantity
			lot.Reason = reason
			lot.GoalGroups = append(lot.GoalGroups, SelectedCloseGoal{GroupID: groupID, Reason: reason, Quantity: quantity})
			group.Expected += quantity
		}
	}
	return nil
}

// consumeSelectedCloseGoal は実約定した分だけ同じ返済群の希望を取り除きます。
//
// 引数: lot(*SelectedLot)、groupID(string)、quantity(int)。返り値: なし。取消だけでは消しません。
func consumeSelectedCloseGoal(lot *SelectedLot, groupID string, quantity int) {
	normalizeSelectedCloseGoals(lot)
	left := quantity
	for i := range lot.GoalGroups {
		goal := &lot.GoalGroups[i]
		if goal.GroupID != groupID || left <= 0 {
			continue
		}
		used := min(left, goal.Quantity)
		goal.Quantity -= used
		left -= used
	}
	lot.Goal = max(0, lot.Goal-quantity)
}

// validateSelectedCloseGroups は保存された返済群を実約定と未返済希望から独立に照合します。
//
// 引数: なし。返り値: error。数量・粗益・取消後の未返済量を検査し、保存値は変更しません。
func (p *Portfolio) validateSelectedCloseGroups() error {
	r := p.Selected
	waiting, filled := map[string]int{}, map[string]int{}
	gross := map[string]float64{}
	for _, lot := range r.Lots {
		total := 0
		seen := map[string]bool{}
		for _, goal := range lot.GoalGroups {
			if goal.Quantity < 0 || seen[goal.GroupID] {
				return errors.New("保存返済希望の数量又は群重複が不正です")
			}
			seen[goal.GroupID] = true
			total += goal.Quantity
			if goal.GroupID == "" {
				continue
			}
			group := r.CloseGroups[goal.GroupID]
			if group == nil || group.Parent != lot.Parent || group.Reason != goal.Reason {
				return errors.New("保存返済希望と元通知群が不一致です")
			}
			waiting[goal.GroupID] += goal.Quantity
			if p.reservedSelectedCloseGroup(lot.ID, goal.GroupID) > goal.Quantity {
				return errors.New("返済群の予約が未返済希望を超えています")
			}
		}
		if total > lot.Goal || lot.Goal > lot.Quantity {
			return errors.New("返済群の希望合計が実玉又は総希望を超えています")
		}
	}
	for _, fill := range p.Executions {
		intent := r.Intents[fill.SignalID]
		if intent.CloseGroupID == "" {
			continue
		}
		lot := p.selectedLot(intent.ActualID)
		group := r.CloseGroups[intent.CloseGroupID]
		if lot == nil || group == nil || group.Parent != lot.Parent {
			return errors.New("保存された返済群約定の実所有が不明です")
		}
		filled[intent.CloseGroupID] += fill.Quantity
		gross[intent.CloseGroupID] += float64(lot.Side*fill.Quantity) * (fill.Price - lot.Entry) * p.Config.PointValue
	}
	for id, group := range r.CloseGroups {
		if group == nil || group.Parent == "" || group.Expected < 0 || group.Filled < 0 || group.Filled > group.Expected || !finite(group.Gross) || group.Filled != filled[id] || math.Abs(group.Gross-gross[id]) > 1e-7*math.Max(1, math.Abs(group.Gross)) || group.Expected-group.Filled != waiting[id] {
			return errors.New("返済群の実約定・粗益・未返済量が不一致です")
		}
		if group.Qualified && (group.Expected == 0 || group.Expected != group.Filled || group.Gross <= 0) {
			return errors.New("未完了又は非正益の返済群に資格があります")
		}
	}
	for id, intent := range r.Intents {
		if intent.CloseGroupID != "" {
			group := r.CloseGroups[intent.CloseGroupID]
			order := p.Orders[id]
			if group == nil || group.Parent != intent.Parent || order == nil || order.Signal.Action != "close" {
				return errors.New("保存注文と返済群が不一致です")
			}
		}
	}
	return nil
}
