package strategy

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// P05Lot は実約定ごとの独立した利益追尾です。同じ証券会社建玉IDでも建値を平均化しません。
type P05Lot struct {
	ID, HoldID, EntryID, SourceID, Owner string
	Side, Quantity, InitialQuantity      int
	Price, At, Peak, Realized, Fees      float64
	Activated, Goal                      bool
	Reason                               string
}

// P05Portfolio はB04の独立仮想売買と、損切りしない実玉の台帳を分離します。
type P05Portfolio struct {
	Version             int
	Shadow              *Portfolio
	ShadowPending       []Signal
	Lots                []P05Lot
	ForcedOwners        map[string]string
	Forced, ForceReason string
	SourceNotifications int
}

// newP05 は実玉に影響されないB04の仮想口座を準備します。
//
// 引数: なし。返り値: *P05Portfolio と設定不整合のerrorです。
func (p *Portfolio) newP05() (*P05Portfolio, error) {
	if p.Config.Reference != "NK225" || p.Session.Kind != "Night" || researchFee(p.Config.Product) == 0 || p.Session.USOpen <= p.Session.Start+900 {
		return nil, errors.New("P05は米国開始15分前までの日経夜間専用です")
	}
	cfg := p.Config
	cfg.Parameters.Method, cfg.Product, cfg.PointValue = "D07", "NK225mini", 100
	cfg.Namespace += "-P05仮想"
	cfg.RuleExpressions = map[string]string{}
	for _, spec := range rules.Specs() {
		cfg.RuleExpressions[spec.ID] = p.Rules.Set.Expressions()[spec.ID]
	}
	session := p.Session
	session.ForceClose = p.p05Deadline()
	session.ExtraAt = session.USOpen + 1800
	shadow, err := New(cfg, session)
	if err != nil {
		return nil, err
	}
	eff := p.Rules.Number("p05.core_efficiency", map[string]any{})
	ret := p.Rules.Number("p05.core_return_r", map[string]any{})
	bounce := p.Rules.Number("p05.core_bounce_r", map[string]any{})
	if err := p.Rules.Err(); err != nil {
		return nil, err
	}
	if eff <= 0 || eff > 1 || ret <= 0 || ret >= .75 || bounce <= 0 {
		return nil, errors.New("P05の主担当入口係数が不正です")
	}
	for _, sleeve := range shadow.Research.Sleeves {
		if sleeve.Name == "core" {
			legacy := sleeve.Source.Legacy
			legacy.Session.NightCoreStart = p.Session.Start
			legacy.Config.Parameters.Night.Efficiency = eff
			legacy.Config.Parameters.Night.RetestReturnR = ret
			legacy.Config.Parameters.Night.BounceR = bounce
		}
	}
	return &P05Portfolio{Version: 1, Shadow: shadow, ForcedOwners: map[string]string{}}, nil
}

// p05Deadline は早い方の全返済判断時刻を返します。
//
// 引数: なし。返り値: float64はUNIX秒です。
func (p *Portfolio) p05Deadline() float64 {
	return math.Min(p.Session.ForceClose, p.Session.USOpen-900)
}

// stepP05Shadow は仮想注文だけを次の正常ラージ板で費用0約定させます。
//
// 引数: o(Observation)。返り値: []Signalは今回の仮想通知、errorは内部会計の不整合です。
func (p *Portfolio) stepP05Shadow(o Observation) ([]Signal, error) {
	state := p.P05
	remaining := []Signal{}
	for _, s := range state.ShadowPending {
		if !quoteValid(o) || o.Index <= s.SourceIndex {
			remaining = append(remaining, s)
			continue
		}
		f := Fill{SignalID: s.ID, ExecutionID: s.ID + "-仮想約定", Quantity: s.Quantity, Price: liquidation(o, -s.Side), Time: o.Time, HoldID: s.ID + "-仮想玉"}
		if s.Action == "close" {
			f.Closed = append([]LotTarget(nil), s.Targets...)
		}
		if err := state.Shadow.OnFill(f); err != nil {
			return nil, err
		}
		if err := state.Shadow.OnOrderDone(s.ID); err != nil {
			return nil, err
		}
	}
	state.ShadowPending = remaining
	signals, err := state.Shadow.Observe(o, o.Time < p.p05Deadline())
	if err != nil {
		return nil, err
	}
	state.ShadowPending = append(state.ShadowPending, signals...)
	return signals, nil
}

// p05Available は同方向の実玉と未約定新規予約を差し引きます。
//
// 引数: side(int)。返り値: intは空き枚数。返済注文中の玉も保有に含めます。
func (p *Portfolio) p05Available(side int) int {
	n := p.Config.Cap
	for _, l := range p.P05.Lots {
		if l.Side == side {
			n -= l.Quantity
		}
	}
	for _, s := range p.Orders {
		if !s.Done && s.Signal.Action == "new" && s.Signal.Side == side {
			n -= s.Signal.Quantity - s.Filled
		}
	}
	return max(0, n)
}

// emitP05 は仮想通知又は特定の実約定玉を、外部注文の予約へ変換します。
//
// 引数: o(Observation)、source(Signal)、lot(*P05Lot)は新規ならnilです。
// 返り値: *Signalは依頼、余枠なし又は対象返済中ならnilです。
func (p *Portfolio) emitP05(o Observation, source Signal, lot *P05Lot) *Signal {
	s := source
	lotID := ""
	if lot == nil {
		s.Quantity = min(s.Quantity, p.p05Available(s.Side))
		s.Reason = "P05 仮想B04の新規通知"
		s.Targets = nil
	} else {
		for _, order := range p.Orders {
			if !order.Done && order.Signal.Action == "close" {
				for _, target := range order.Signal.Targets {
					if target.ID == lot.HoldID {
						return nil
					}
				}
			}
		}
		s = Signal{Owner: lot.Owner, Action: "close", Side: -lot.Side, Quantity: lot.Quantity, Reason: lot.Reason, ReasonCode: 151, Targets: []LotTarget{{ID: lot.HoldID, Quantity: lot.Quantity}}}
		lotID = lot.ID
	}
	if s.Quantity <= 0 {
		return nil
	}
	p.Sequence++
	s.ID = fmt.Sprintf("%s-%s-P05-%06d", p.Session.ID, p.Config.Product, p.Sequence)
	if p.Config.Namespace != "" {
		s.ID = p.Config.Namespace + "-" + s.ID
	}
	s.Time, s.SourceIndex = o.Time, o.Index
	evidence, _ := o.Evidence()
	s.Evidence, _ = json.Marshal(map[string]any{"observation": json.RawMessage(evidence), "virtual_source_id": source.ID, "actual_execution_id": lotID})
	p.Orders[s.ID] = &orderState{Signal: s, P05LotID: lotID}
	return &s
}

// observeP05 は影の入口と実玉の利益追尾を独立に進めます。
//
// 引数: o(Observation)、allow/emit(bool)は実注文の許可です。
// 返り値: []Signalとerror。履歴Advanceでも影は進めますが実注文は作りません。
func (p *Portfolio) observeP05(o Observation, allow, emit bool) ([]Signal, error) {
	shadow, err := p.stepP05Shadow(o)
	if err != nil {
		return nil, err
	}
	if o.Time >= p.p05Deadline() {
		p.P05.Forced = "全返済"
		p.P05.ForceReason = "P05 米国開始15分前の全返済"
	}
	var result []Signal
	for i := range p.P05.Lots {
		lot := &p.P05.Lots[i]
		if lot.Quantity == 0 {
			continue
		}
		if p.P05.Forced != "" {
			lot.Goal = true
			lot.Reason = p.P05.ForceReason
		} else if reason := p.P05.ForcedOwners[lot.Owner]; reason != "" {
			lot.Goal = true
			lot.Reason = reason
		} else if quoteValid(o) {
			gain := float64(lot.Side) * (liquidation(o, lot.Side) - lot.Price)
			lot.Peak = math.Max(lot.Peak, gain)
			if p.Rules.Bool("p05.profit_active", map[string]any{"best": lot.Peak}) {
				lot.Activated = true
			}
			// 履歴の追尾水準だけは継続しますが、未送信の過去利確は現在の注文へ変換しません。
			if emit && lot.Activated && p.Rules.Bool("p05.profit_exit", map[string]any{"best": lot.Peak, "gain": gain}) {
				lot.Goal = true
				lot.Reason = "P05 利益開始後の利益玉追尾返済"
			}
		}
		if lot.Goal && emit {
			if s := p.emitP05(o, Signal{}, lot); s != nil {
				result = append(result, *s)
			}
		}
	}
	for _, source := range shadow {
		if source.Action != "new" {
			continue
		}
		p.P05.SourceNotifications++
		if !allow || !emit || p.P05.Forced != "" || p.P05.ForcedOwners[source.Owner] != "" || o.Time >= p.p05Deadline() {
			continue
		}
		if s := p.emitP05(o, source, nil); s != nil {
			result = append(result, *s)
		}
	}
	return result, p.Rules.Err()
}

// fillP05 は実約定だけを個別玉へ反映し、仮想B04の会計へは渡しません。
//
// 引数: f(Fill)、order(*orderState)。返り値: errorは所有ID又は枚数の不整合です。
func (p *Portfolio) fillP05(f Fill, order *orderState) error {
	s := order.Signal
	if s.Action == "new" {
		n := f.Quantity
		for _, l := range p.P05.Lots {
			if l.Side == s.Side {
				n += l.Quantity
			}
		}
		if n > p.Config.Cap {
			return errors.New("P05の実約定が片側上限を超えました")
		}
		hold := f.HoldID
		if hold == "" {
			hold = f.ExecutionID
		}
		reason := p.P05.ForcedOwners[s.Owner]
		if p.P05.Forced != "" {
			reason = p.P05.ForceReason
		}
		if f.Time >= p.p05Deadline() {
			p.P05.Forced = "全返済"
			p.P05.ForceReason = "P05 境界後の遅延新規を全返済"
			reason = p.P05.ForceReason
		}
		sourceID := ""
		var evidence struct {
			Source string `json:"virtual_source_id"`
		}
		_ = json.Unmarshal(s.Evidence, &evidence)
		sourceID = evidence.Source
		p.P05.Lots = append(p.P05.Lots, P05Lot{ID: f.ExecutionID, HoldID: hold, EntryID: s.ID, SourceID: sourceID, Owner: s.Owner, Side: s.Side, Quantity: f.Quantity, InitialQuantity: f.Quantity, Price: f.Price, At: f.Time, Fees: f.Fee, Goal: reason != "", Reason: reason})
		if s.Owner == "core" {
			p.CoreEntered = true
		} else {
			p.ExtraAttempted = true
		}
	} else {
		var lot *P05Lot
		for i := range p.P05.Lots {
			if p.P05.Lots[i].ID == order.P05LotID {
				lot = &p.P05.Lots[i]
				break
			}
		}
		if lot == nil || lot.Side != -s.Side || lot.Owner != s.Owner || lot.Quantity < f.Quantity {
			return errors.New("P05の返済対象実約定玉が一致しません")
		}
		quantity := 0
		for _, target := range f.Closed {
			if target.ID != lot.HoldID || target.Quantity <= 0 {
				return errors.New("P05返済に指定外の建玉IDがあります")
			}
			quantity += target.Quantity
		}
		if len(f.Closed) > 0 && quantity != f.Quantity {
			return errors.New("P05返済内訳の数量が一致しません")
		}
		lot.Realized += float64(lot.Side*f.Quantity) * (f.Price - lot.Price) * p.Config.PointValue
		lot.Fees += f.Fee
		lot.Quantity -= f.Quantity
	}
	order.Filled += f.Quantity
	p.Executions[f.ExecutionID] = f
	return nil
}

// flattenP05 は価格がなくても所有する実玉だけに返済要求を残します。
//
// 引数: owner(string)は空なら全担当、at(float64)、reason(string)。返り値: []Signalは現在送れる依頼です。
func (p *Portfolio) flattenP05(owner string, at float64, reason string) []Signal {
	if owner == "" {
		p.P05.Forced = "全返済"
		p.P05.ForceReason = reason
	} else {
		p.P05.ForcedOwners[owner] = reason
	}
	o := Observation{Reference: p.Config.Reference, Time: at, Index: p.LastIndex}
	var out []Signal
	for i := range p.P05.Lots {
		l := &p.P05.Lots[i]
		if owner != "" && l.Owner != owner {
			continue
		}
		l.Goal = true
		l.Reason = reason
		if s := p.emitP05(o, Signal{}, l); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// statusP05 は実約定玉だけを表示へ渡します。
//
// 引数: なし。返り値: Statusは仮想玉を含まず、各部分約定の建値を保持します。
func (p *Portfolio) statusP05() Status {
	out := Status{CoreEntered: p.CoreEntered, ExtraAttempted: p.ExtraAttempted}
	for _, l := range p.P05.Lots {
		side := l.Side
		if l.Quantity == 0 {
			side = 0
		}
		out.Positions = append(out.Positions, Position{Owner: l.Owner, Side: side, Quantity: l.Quantity, InitialQuantity: l.InitialQuantity, Average: l.Price, EntryTime: l.At, Realized: l.Realized, Fees: l.Fees, Peak: l.Peak})
	}
	for _, o := range p.Orders {
		if !o.Done {
			out.Pending = append(out.Pending, o.Signal)
		}
	}
	sort.Slice(out.Pending, func(i, j int) bool { return out.Pending[i].ID < out.Pending[j].ID })
	return out
}

// restoreP05 は実玉と独立影の保存台帳を復元し、破損した所有関係を拒否します。
//
// 引数: なし。返り値: errorは保存状態の不整合です。
func (p *Portfolio) restoreP05() error {
	r := p.P05
	if r.Version != 1 || r.Shadow == nil || r.ForcedOwners == nil || p.Config.Reference != "NK225" || p.Session.Kind != "Night" || r.Shadow.Config.Parameters.Method != "D07" || r.Shadow.Config.Cap != p.Config.Cap || r.Shadow.Session.ForceClose != p.p05Deadline() {
		return errors.New("P05の保存版・仮想口座・時刻が不正です")
	}
	data, err := r.Shadow.Dump()
	if err != nil {
		return err
	}
	r.Shadow, err = Restore(data)
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, l := range r.Lots {
		order := p.Orders[l.EntryID]
		if l.ID == "" || ids[l.ID] || l.HoldID == "" || l.Quantity < 0 || l.Quantity > l.InitialQuantity || !finite(l.Price) || l.Price <= 0 || !finite(l.Peak) || l.Peak < 0 || (l.Side != 1 && l.Side != -1) || order == nil || order.Signal.Action != "new" || order.Signal.Side != l.Side || order.Signal.Owner != l.Owner {
			return errors.New("P05の個別玉の保存状態が不正です")
		}
		ids[l.ID] = true
	}
	for _, o := range p.Orders {
		if o == nil || o.Filled < 0 || o.Filled > o.Signal.Quantity {
			return errors.New("P05注文数量が不正です")
		}
		if o.Signal.Action == "close" && !ids[o.P05LotID] {
			return errors.New("P05返済対象が保存玉にありません")
		}
	}
	for _, side := range []int{-1, 1} {
		quantity := 0
		for _, l := range r.Lots {
			if l.Side == side {
				quantity += l.Quantity
			}
		}
		for _, o := range p.Orders {
			if !o.Done && o.Signal.Action == "new" && o.Signal.Side == side {
				quantity += o.Signal.Quantity - o.Filled
			}
		}
		if quantity > p.Config.Cap {
			return errors.New("P05保存残高と新規予約が上限を超えています")
		}
	}
	for _, s := range r.ShadowPending {
		o := r.Shadow.Orders[s.ID]
		if o == nil || o.Done || o.Filled != 0 || o.Signal.Quantity != s.Quantity {
			return errors.New("P05仮想の未約定依頼が一致しません")
		}
	}
	return nil
}

// diagnosticsP05 は実玉の追尾水準と独立影の通知数を小容量で示します。
//
// 引数: o(Observation)。返り値: []map[string]anyは実玉別の読取診断です。
func (p *Portfolio) diagnosticsP05(o Observation) []map[string]any {
	var out []map[string]any
	for _, l := range p.P05.Lots {
		if l.Quantity <= 0 {
			continue
		}
		v := map[string]any{"method": "P05", "owner": l.Owner, "actual_execution_id": l.ID, "hold_id": l.HoldID, "actual_quantity": l.Quantity, "side": l.Side, "entry_price": l.Price, "best_gain_points": l.Peak, "profit_protection_active": l.Activated, "close_requested": l.Goal, "force_reason": l.Reason, "virtual_new_notifications": p.P05.SourceNotifications, "reference_quote_valid": quoteValid(o)}
		if quoteValid(o) {
			v["reference_gain_points"] = float64(l.Side) * (liquidation(o, l.Side) - l.Price)
		}
		out = append(out, v)
	}
	return out
}
