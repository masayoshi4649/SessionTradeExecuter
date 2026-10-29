package strategy

import (
	"math"
	"sort"
)

// selectedNightLots は実商品残量を小量判定用の正規化量と分離して渡します。
//
// 引数: なし。返り値: []selectedNightLotView。
func (p *Portfolio) selectedNightLots() []selectedNightLotView {
	var out []selectedNightLotView
	for _, l := range p.Selected.Lots {
		out = append(out, selectedNightLotView{ID: l.ID, Parent: l.Parent, SourceID: l.SourceID, Kind: l.Kind, Side: l.Side, RemainingActual: l.Quantity, Cap: p.Config.Cap, Entry: l.Entry, BestGain: l.BestGain})
	}
	return out
}

// observeSelected は固定元手法、当時の政策、実会計の順に一原行を処理します。
//
// 引数: o(Observation)、allow/emit/fresh(bool)。返り値: []Signalとerror。
func (p *Portfolio) observeSelected(o Observation, allow, emit, fresh bool) ([]Signal, error) {
	r := p.Selected
	r.Last = o
	events, err := p.stepSelectedSource(o)
	if err != nil {
		return nil, err
	}
	if fresh && quoteValid(o) {
		for i := range r.Lots {
			l := &r.Lots[i]
			if l.Quantity > 0 {
				l.BestGain = math.Max(l.BestGain, float64(l.Side)*(liquidation(o, l.Side)-l.Entry))
			}
		}
	}
	if o.Time >= p.selectedDeadline() {
		r.Forced = "時間枠終了の全返済"
	}
	if r.Forced != "" {
		for i := range r.Lots {
			l := &r.Lots[i]
			l.Goal = l.Quantity
			l.Reason = r.Forced
		}
	}
	if r.Day != nil {
		return p.observeSelectedDay(o, events, allow, emit, fresh)
	}
	if err := r.Night.injectSelectedNightView(o); err != nil {
		return nil, err
	}
	var result []Signal
	// ----------------------------------------
	// 元返済は通知時点の元ID比率で実残玉へ投影します。未完了返済を二重に作りません。
	for _, e := range events {
		if e.Signal.Action != "close" {
			continue
		}
		if err := p.queueSelectedSourceClose(e); err != nil {
			return nil, err
		}
	}
	if emit {
		out, err := p.flushSelectedGoals(o)
		if err != nil {
			return nil, err
		}
		result = append(result, out...)
	}
	for _, e := range events {
		if e.Signal.Action != "new" {
			continue
		}
		in, err := r.Night.sourceNewSelectedNight(e.Parent, e.SourceID, e.Signal.Side, e.QuantityMicro, o, p.Rules)
		if err != nil {
			return nil, err
		}
		if !allow || !emit || !fresh || !quoteValid(o) || in.QuantityMicro <= 0 {
			continue
		}
		s, err := p.emitSelected(o, SelectedIntent{Parent: in.Parent, SourceID: in.SourceID, Owner: e.Signal.Owner, Campaign: e.Signal.Campaign, Kind: in.Kind, Reason: in.Reason, Side: in.Side, QuantityMicro: in.QuantityMicro})
		if err != nil {
			return nil, err
		}
		if s != nil {
			result = append(result, *s)
		}
	}
	// ----------------------------------------
	// 待機解除は新しい自方向帯が確定した原行だけです。残量も予約も現在値を使います。
	keys := make([]string, 0, len(r.Night.Parents))
	for key := range r.Night.Parents {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		snap := p.selectedSnapshot()
		parent := r.Night.Parents[key]
		in, err := r.Night.releaseSelectedNight(key, snap.SourceRemaining[parent.InitialSource], snap.PendingParents[key], fresh && emit && allow, p.selectedDeadline(), o, p.Rules)
		if err != nil {
			return nil, err
		}
		if in.QuantityMicro <= 0 {
			continue
		}
		source := r.SourceLots[in.SourceID+"-玉"]
		s, err := p.emitSelected(o, SelectedIntent{Parent: key, SourceID: in.SourceID, Owner: source.Owner, Campaign: source.Campaign, Kind: in.Kind, Reason: in.Reason, Side: in.Side, QuantityMicro: in.QuantityMicro})
		if err != nil {
			return nil, err
		}
		accepted := 0
		if s != nil {
			result = append(result, *s)
			accepted = s.Quantity
		}
		r.Night.finishReleaseSelectedNight(key, accepted)
	}
	protectionSnapshot := p.selectedSnapshot()
	for _, l := range p.selectedNightLots() {
		in, err := r.Night.protectSelectedNight(l, o, protectionSnapshot.ClosingParents[l.Parent], fresh && emit, p.Rules)
		if err != nil {
			return nil, err
		}
		if in.QuantityActual <= 0 {
			continue
		}
		lot := p.selectedLot(in.ActualID)
		lot.Goal = max(lot.Goal, in.QuantityActual)
		lot.Reason = in.Reason
		s, err := p.emitSelected(o, SelectedIntent{ActualID: in.ActualID, Kind: in.Kind, Reason: in.Reason, Quantity: in.QuantityActual})
		if err != nil {
			return nil, err
		}
		if s != nil {
			result = append(result, *s)
		}
	}
	return result, p.Rules.Err()
}
