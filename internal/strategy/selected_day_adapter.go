package strategy

import (
	"errors"
	"sort"
)

// observeSelectedDay は日中の正規化政策予約を実発注依頼へ接続します。
//
// 引数: o(Observation)、events([]SelectedSourceEvent)、allow/emit/fresh(bool)。返り値: []Signalとerror。
func (p *Portfolio) observeSelectedDay(o Observation, events []SelectedSourceEvent, allow, emit, fresh bool) ([]Signal, error) {
	r := p.Selected
	intents, err := r.Day.StepSelectedDay(o, events, p.selectedSnapshot(), p.Rules, fresh && emit, allow && emit, r.Forced != "")
	if err != nil {
		return nil, err
	}
	var out []Signal
	for _, in := range intents {
		if in.CancelIntentID != "" {
			continue
		}
		if !emit {
			if err := r.Day.DoneSelectedDay(in.PolicyID, p.selectedSnapshot()); err != nil {
				return nil, err
			}
			continue
		}
		// 研究配分を共通発注器が再縮小して政策会計とずらさないため、受付不能は全量取消します。
		snap := p.selectedSnapshot()
		available := snap.SideAvailable[in.Side]
		if in.ActualID != "" {
			l := p.selectedLot(in.ActualID)
			if l == nil {
				return nil, errors.New("日中政策の返済対象がありません")
			}
			available = l.Quantity - snap.ReservedClose[l.ID]
		}
		if in.Quantity > available {
			if err := r.Day.DoneSelectedDay(in.PolicyID, snap); err != nil {
				return nil, err
			}
			continue
		}
		s, err := p.emitSelected(o, in)
		if err != nil {
			return nil, err
		}
		if s == nil {
			if err := r.Day.DoneSelectedDay(in.PolicyID, p.selectedSnapshot()); err != nil {
				return nil, err
			}
			continue
		}
		out = append(out, *s)
	}
	if emit {
		goals, err := p.flushSelectedGoals(o)
		if err != nil {
			return nil, err
		}
		out = append(out, goals...)
	}
	return out, nil
}

// SelectedCancellationIDs は元終了等で不要になった当起動の新規依頼IDだけを返します。
//
// 引数: なし。返り値: []string。取消受付は完了とせず、OnOrderDoneまで繰り返し照合対象に残します。
func (p *Portfolio) SelectedCancellationIDs() []string {
	if p == nil || p.Selected == nil || p.Selected.Day == nil {
		return nil
	}
	var out []string
	for id, in := range p.Selected.Intents {
		order := p.Orders[id]
		if order == nil || order.Done || order.Signal.Action != "new" {
			continue
		}
		policy := p.Selected.Day.Pending[in.PolicyID]
		if policy != nil && policy.CancelRequested {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
