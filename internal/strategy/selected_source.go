package strategy

import (
	"errors"
	"fmt"
	"math"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// newSelected は固定mini10枚の元手法と実商品会計を分離して初期化します。
//
// 引数: なし。返り値: *SelectedPortfolioとerror。
func (p *Portfolio) newSelected() (*SelectedPortfolio, error) {
	method := p.Config.Parameters.Method
	if p.Config.Reference != "NK225" || (method == "D164_15") != (p.Session.Kind == "Day") {
		return nil, errors.New("選定三手法の参照市場・時間枠が不一致です")
	}
	base := "D07"
	if method == "D164_15" {
		base = "S01"
	}
	if method == "P164_06" {
		base = "P05"
	}
	cfg := p.Config
	cfg.Parameters = DefaultParameters("NK225", p.Session.Kind)
	cfg.Parameters.Method = base
	cfg.Product, cfg.PointValue = "NK225mini", 100
	cfg.CoreQuantity, cfg.ExtraMaxQuantity, cfg.Cap = 5, 5, 10
	cfg.Namespace += "-選定源"
	profile, err := rules.SelectProfile("NK225", p.Session.Kind, base)
	if err != nil {
		return nil, err
	}
	cfg.RuleExpressions = map[string]string{}
	all := p.Rules.Set.Expressions()
	for _, spec := range rules.SpecsFor(profile) {
		cfg.RuleExpressions[spec.ID] = all[spec.ID]
	}
	session := p.Session
	if method == "P164_06" {
		session.ForceClose = math.Min(session.ForceClose, session.USOpen-900)
	}
	shadow, err := New(cfg, session)
	if err != nil {
		return nil, err
	}
	r := &SelectedPortfolio{Version: 1, Shadow: shadow, SourceLots: map[string]*selectedSourceLot{}, Intents: map[string]SelectedIntent{}, ForcedOwners: map[string]string{}}
	if method == "D164_15" {
		r.Day = newSelectedDay164Policy(p.Config.Cap)
	} else {
		r.Night, err = newSelectedNightPolicy("NK225_" + method)
	}
	return r, err
}

// stepSelectedSource は元手法のみを次正常ラージ板で仮想約定し、当行の元通知を返します。
//
// 引数: o(Observation)。返り値: []SelectedSourceEventとerror。実約定は一切生成しません。
func (p *Portfolio) stepSelectedSource(o Observation) ([]SelectedSourceEvent, error) {
	r := p.Selected
	waiting := []Signal{}
	for _, sig := range r.ShadowPending {
		if !quoteValid(o) || o.Index <= sig.SourceIndex {
			waiting = append(waiting, sig)
			continue
		}
		f := Fill{SignalID: sig.ID, ExecutionID: sig.ID + "-源約定", Quantity: sig.Quantity, Price: liquidation(o, -sig.Side), Time: o.Time, HoldID: sig.ID + "-玉"}
		if sig.Action == "close" {
			f.Closed = append([]LotTarget(nil), sig.Targets...)
		}
		if err := r.Shadow.OnFill(f); err != nil {
			return nil, fmt.Errorf("元手法約定: %w", err)
		}
		if err := r.Shadow.OnOrderDone(sig.ID); err != nil {
			return nil, err
		}
	}
	r.ShadowPending = waiting
	signals, err := r.Shadow.Observe(o, o.Time < p.selectedDeadline())
	if err != nil {
		return nil, err
	}
	r.ShadowPending = append(r.ShadowPending, signals...)
	var events []SelectedSourceEvent
	// ----------------------------------------
	// 元返済を先に更新します。P05の返済Campaign=0は対象の元建玉IDから復元します。
	for _, sig := range signals {
		if sig.Action != "close" {
			continue
		}
		groups := map[string]*SelectedSourceEvent{}
		var keys []string
		total := 0
		for _, target := range sig.Targets {
			lot := r.SourceLots[target.ID]
			q := target.Quantity * 10
			if lot == nil || q <= 0 || q > lot.RemainingMicro {
				return nil, fmt.Errorf("元返済対象の不整合: %s", target.ID)
			}
			e := groups[lot.Parent]
			if e == nil {
				s := sig
				s.Owner, s.Campaign = lot.Owner, lot.Campaign
				e = &SelectedSourceEvent{Signal: s, Parent: lot.Parent, SourceID: sig.ID}
				groups[lot.Parent] = e
				keys = append(keys, lot.Parent)
			}
			e.Targets = append(e.Targets, SelectedSourceTarget{lot.SourceID, lot.RemainingMicro, q, lot.RemainingMicro - q})
			lot.RemainingMicro -= q
			e.QuantityMicro += q
			total += q
		}
		if total != sig.Quantity*10 {
			return nil, errors.New("元返済数量と所有ID内訳が不一致です")
		}
		for _, key := range keys {
			e := groups[key]
			e.ParentRemainingMicro = p.selectedParentRemaining(key)
			events = append(events, *e)
		}
	}
	for _, sig := range signals {
		if sig.Action != "new" {
			continue
		}
		id := sig.ID + "-玉"
		if r.SourceLots[id] != nil {
			return nil, errors.New("元新規IDが重複しました")
		}
		parent := fmt.Sprintf("%s/%d", sig.Owner, sig.Campaign)
		q := sig.Quantity * 10
		r.SourceLots[id] = &selectedSourceLot{sig.ID, parent, sig.Owner, sig.Campaign, sig.Side, q}
		r.SourceOrder = append(r.SourceOrder, id)
		events = append(events, SelectedSourceEvent{Signal: sig, Parent: parent, SourceID: sig.ID, QuantityMicro: q, ParentRemainingMicro: p.selectedParentRemaining(parent)})
	}
	return events, nil
}

// selectedParentRemaining は元通知時点の親残量を返します。
//
// 引数: parent(string)。返り値: intは100基準の量です。
func (p *Portfolio) selectedParentRemaining(parent string) int {
	q := 0
	for _, id := range p.Selected.SourceOrder {
		l := p.Selected.SourceLots[id]
		if l.Parent == parent {
			q += l.RemainingMicro
		}
	}
	return q
}

// selectedDeadline は時間枠の強制返済時計を返します。
//
// 引数: なし。返り値: float64は日本時間設定から解決されたUNIX秒です。
func (p *Portfolio) selectedDeadline() float64 {
	if p.Config.Parameters.Method == "P164_06" {
		return math.Min(p.Session.ForceClose, p.Session.USOpen-900)
	}
	return p.Session.ForceClose
}
