package strategy

import (
	"encoding/json"
	"errors"
	"reflect"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

const s01GuardReason = 15410

// S01Guard は別入口の一連の実売買を、粗実現損益と清算含みの合計で保護します。
type S01Guard struct {
	Campaign            int
	Risk                float64
	InitialOrderID      string
	InitialQuantity     int
	InitialDone, Active bool
	Best, Floor         float64
}

// S01Rejection は新規を見送った時点の基準親占有を小容量で記録します。
type S01Rejection struct {
	Time                       float64
	Index                      int64
	Campaign, BaselineCampaign int
	BaselineOccupied           bool
}

// S01Portfolio は実玉と独立した基準D07の仮想運転、親利益床、直近の抑止理由を保存します。
type S01Portfolio struct {
	Version         int
	Shadow          *Portfolio
	ShadowPending   []Signal
	Guard           *S01Guard
	RejectedParents int
	LastRejection   *S01Rejection
	Failure         string
}

// s01BaseConfig はS01の共通部分を変更せずD07の独立設定へ写します。
//
// 引数: cfg(Config)。返り値: ConfigはD07専用キーだけを持つ独立コピーです。
func s01BaseConfig(cfg Config) Config {
	cfg.Parameters.Method = "D07"
	if cfg.RuleExpressions != nil {
		expressions := map[string]string{}
		for _, spec := range rules.Specs() {
			if value, ok := cfg.RuleExpressions[spec.ID]; ok {
				expressions[spec.ID] = value
			}
		}
		cfg.RuleExpressions = expressions
	}
	return cfg
}

// newS01 は元D07の実玉運転を独立した仮想口座として生成します。
//
// 引数: なし。返り値: *S01Portfolioとerrorです。
// 基準は同じ枚数上限のmini・費用0で、実商品の約定や手数料では変更されません。
func (p *Portfolio) newS01() (*S01Portfolio, error) {
	cfg := s01BaseConfig(p.Config)
	cfg.Product, cfg.PointValue = "NK225mini", 100
	cfg.Namespace += "-S01基準仮想"
	shadow, err := New(cfg, p.Session)
	if err != nil {
		return nil, err
	}
	return &S01Portfolio{Version: 1, Shadow: shadow}, nil
}

// stepS01Shadow は基準だけを次の正常原板で全量約定させ、その当行判断まで先行します。
//
// 引数: o(Observation)。返り値: errorは仮想台帳の不整合です。
// 実側の新規許可とは独立し、仮想注文を返り値や実注文台帳へ混ぜません。
func (p *Portfolio) stepS01Shadow(o Observation) error {
	r := p.S01
	remaining := []Signal{}
	for _, sig := range r.ShadowPending {
		if !quoteValid(o) || o.Index <= sig.SourceIndex {
			remaining = append(remaining, sig)
			continue
		}
		f := Fill{SignalID: sig.ID, ExecutionID: sig.ID + "-仮想約定", HoldID: sig.ID + "-仮想玉", Quantity: sig.Quantity, Price: liquidation(o, -sig.Side), Time: o.Time}
		if sig.Action == "close" {
			f.Closed = append([]LotTarget(nil), sig.Targets...)
		}
		if err := r.Shadow.OnFill(f); err != nil {
			return err
		}
		if err := r.Shadow.OnOrderDone(sig.ID); err != nil {
			return err
		}
	}
	r.ShadowPending = remaining
	signals, err := r.Shadow.Observe(o, o.Time < p.Session.ForceClose)
	if err != nil {
		return err
	}
	r.ShadowPending = append(r.ShadowPending, signals...)
	return nil
}

// s01Baseline は現在までに確定した基準側の別入口親と占有を返します。
//
// 引数: なし。返り値: intは基準親、boolは実保有又は未確定注文の有無です。
func (p *Portfolio) s01Baseline() (int, bool) {
	if p.S01 == nil || p.S01.Shadow == nil {
		return -1, false
	}
	s := p.S01.Shadow.sleeve("episode")
	if s == nil {
		return -1, false
	}
	pending, _ := p.S01.Shadow.pendingResearch("episode")
	return s.Campaign, s.held() > 0 || pending
}

// s01EntryAllowed は基準旧親が占有する間の別親への乗り換えだけを止めます。
//
// 引数: s(*ResearchSleeve)、o(Observation)、campaign(int)。返り値: boolは当親の受付可否です。
func (p *Portfolio) s01EntryAllowed(s *ResearchSleeve, o Observation, campaign int) bool {
	if s.Name != "episode" {
		return true
	}
	old, occupied := p.s01Baseline()
	allowed := p.Rules.Bool("s01.entry_allowed", map[string]any{"baseline_occupied": occupied, "same_campaign": old == campaign})
	if !allowed {
		p.S01.RejectedParents++
		p.S01.LastRejection = &S01Rejection{Time: o.Time, Index: o.Index, Campaign: campaign, BaselineCampaign: old, BaselineOccupied: occupied}
	}
	return allowed
}

// s01EntryNotClosing は成立済みの元退出又は保護床の返済を追加注文より優先します。
//
// 引数: s(*ResearchSleeve)、sourceClose(bool)。返り値: boolは当行の新規許可です。
func s01EntryNotClosing(s *ResearchSleeve, sourceClose bool) bool {
	if sourceClose || s.held() > 0 && (s.Blocked || s.ForceReason != 0) {
		return false
	}
	for _, l := range s.Lots {
		if goal, ok := s.Goals[l.ID]; ok && goal.Remaining < l.Quantity {
			return false
		}
	}
	return true
}

// recordS01Initial は初回注文の実約定だけを累積し、追加玉では初回基準を変えません。
//
// 引数: s(*ResearchSleeve)、f(Fill)、sig(Signal)。返り値: なし。
func (p *Portfolio) recordS01Initial(s *ResearchSleeve, f Fill, sig Signal) {
	if p.S01 == nil || s.Name != "episode" || p.S01.Guard == nil {
		return
	}
	g := p.S01.Guard
	if g.InitialOrderID == "" {
		g.InitialOrderID = sig.ID
	}
	if sig.ID == g.InitialOrderID {
		g.InitialQuantity += f.Quantity
	}
}

// finishS01Initial は初回の全約定又は取消照合完了時に起動用実枚数を確定します。
//
// 引数: order(*orderState)。返り値: なし。
// 初回注文が残る間の価格ピークは保持せず、確定後の観測から利益保護を測ります。
func (p *Portfolio) finishS01Initial(order *orderState) {
	g := p.S01.Guard
	if g == nil || g.InitialOrderID != order.Signal.ID || order.Signal.Action != "new" {
		return
	}
	g.InitialQuantity = order.Filled
	g.InitialDone = true
}

// manageS01Guard はepisode親の粗実現と清算含みの合計の最高益半分を保護します。
//
// 引数: s(*ResearchSleeve)、o(Observation)、fresh(bool)。返り値: なし。
// 履歴や古い受信では水準だけ更新し、新しい返済目標を作りません。既決定目標は取消しません。
func (p *Portfolio) manageS01Guard(s *ResearchSleeve, o Observation, fresh bool) {
	if p.S01 == nil || s.Name != "episode" {
		return
	}
	g := p.S01.Guard
	if g == nil || !g.InitialDone || g.InitialQuantity <= 0 || s.held() == 0 || !quoteValid(o) {
		return
	}
	value := p.researchCash(s)
	mark := liquidation(o, s.Side)
	for _, lot := range s.Lots {
		value += float64(s.Side*lot.Quantity) * (mark - lot.Price) * p.Config.PointValue
	}
	if !g.Active && p.Rules.Bool("s01.guard_active", map[string]any{"parent_value": value, "risk": g.Risk, "initial_quantity": float64(g.InitialQuantity), "point_value": p.Config.PointValue}) {
		if value <= 0 {
			p.S01.Failure = "S01の保護起動時の親利益は正の値が必要です"
			return
		}
		g.Active = true
		g.Best = value
	}
	if !g.Active {
		return
	}
	if value > g.Best {
		g.Best = value
	}
	floor := p.Rules.Number("s01.guard_floor", map[string]any{"best_value": g.Best})
	if !finite(floor) || floor < 0 || floor > g.Best {
		p.S01.Failure = "S01の利益床は0以上かつ最高親利益以下が必要です"
		return
	}
	if floor > g.Floor {
		g.Floor = floor
	}
	if !fresh || value > g.Floor || s.ForceReason != 0 {
		return
	}
	if pending, _ := p.pendingResearch(s.Name); pending {
		return
	}
	for _, lot := range s.Lots {
		if goal, ok := s.Goals[lot.ID]; ok && goal.Remaining < lot.Quantity {
			return
		}
	}
	closeAllResearch(s, s01GuardReason)
}

// restoreS01 は影の予約と実親保護を復元し、別口座の玉を混入させません。
//
// 引数: なし。返り値: errorは保存された親・枚数・初回注文の矛盾です。
func (p *Portfolio) restoreS01() error {
	r := p.S01
	if r.Failure != "" {
		return errors.New(r.Failure)
	}
	if r.Version != 1 || r.Shadow == nil || p.Config.Reference != "NK225" || p.Session.Kind != "Day" || r.Shadow.Config.Parameters.Method != "D07" || r.Shadow.S01 != nil || r.Shadow.P05 != nil || r.Shadow.Config.Cap != p.Config.Cap || r.Shadow.Config.Product != "NK225mini" || r.Shadow.Config.PointValue != 100 || !reflect.DeepEqual(r.Shadow.Session, p.Session) {
		return errors.New("S01の独立基準口座が不正です")
	}
	raw, err := json.Marshal(r.Shadow)
	if err != nil {
		return err
	}
	r.Shadow, err = Restore(raw)
	if err != nil {
		return err
	}
	pending := map[string]Signal{}
	for _, sig := range r.ShadowPending {
		order := r.Shadow.Orders[sig.ID]
		if _, exists := pending[sig.ID]; exists || order == nil || order.Done || order.Filled != 0 || !reflect.DeepEqual(order.Signal, sig) {
			return errors.New("S01基準の仮想予約が不整合です")
		}
		pending[sig.ID] = sig
	}
	for _, order := range r.Shadow.Orders {
		if !order.Done {
			if _, ok := pending[order.Signal.ID]; !ok {
				return errors.New("S01基準の仮想予約が欠落しています")
			}
		}
	}
	if s := p.sleeve("episode"); s != nil && s.held() > 0 && r.Guard == nil {
		return errors.New("S01の実玉に対応する親利益保護がありません")
	}
	if g := r.Guard; g != nil {
		s := p.sleeve("episode")
		if s == nil || s.Campaign != g.Campaign || !finite(g.Risk) || g.Risk <= 0 || g.Risk != s.FixedR || g.InitialQuantity < 0 || g.InitialQuantity > s.Capacity || !finite(g.Best) || !finite(g.Floor) || g.Floor < 0 || g.Best < g.Floor {
			return errors.New("S01の親利益保護状態が不正です")
		}
		if g.InitialOrderID != "" {
			order := p.Orders[g.InitialOrderID]
			if order == nil || order.Signal.Owner != "episode" || order.Signal.Campaign != g.Campaign || order.Signal.Action != "new" || order.Filled != g.InitialQuantity || order.Done != g.InitialDone {
				return errors.New("S01の初回実約定枚数が不正です")
			}
		} else if g.InitialQuantity != 0 || g.InitialDone || g.Active {
			return errors.New("S01の未約定初回状態が不正です")
		}
		if g.Active && (!g.InitialDone || g.InitialQuantity <= 0 || g.Best <= 0) {
			return errors.New("S01保護の起動条件が不正です")
		}
	}
	return nil
}

// s01Diagnostics は実保護と基準側の占有を区別した小容量の監査項目を返します。
//
// 引数: なし。返り値: map[string]anyは親最高・床・q0・固定R・最後の抑止理由です。
func (p *Portfolio) s01Diagnostics() map[string]any {
	parent, occupied := p.s01Baseline()
	d := map[string]any{"baseline_campaign": parent, "baseline_occupied": occupied, "rejected_parents": p.S01.RejectedParents, "last_rejection": p.S01.LastRejection, "failure": p.S01.Failure}
	if g := p.S01.Guard; g != nil {
		d["guard"] = map[string]any{"campaign": g.Campaign, "initial_order_id": g.InitialOrderID, "initial_quantity": g.InitialQuantity, "initial_done": g.InitialDone, "fixed_r": g.Risk, "active": g.Active, "best_value_yen": g.Best, "floor_value_yen": g.Floor}
	}
	return d
}

// s01Reason は専用の利益保護理由を日本語で表示します。
//
// 引数: reason(int)。返り値: stringは既知理由、未知なら空文字です。
func s01Reason(reason int) string {
	if reason == s01GuardReason {
		return "S01 別入口の親利益50%保護"
	}
	return ""
}

// StrategyError はルールと専用保護の停止理由を統一して取得します。
//
// 引数: なし。返り値: errorは送信を止める異常、正常時はnilです。
// 既に待機中の依頼にも同じ検査を適用でき、状態や注文を変更しません。
func (p *Portfolio) StrategyError() error {
	if p == nil {
		return nil
	}
	if err := p.Rules.Err(); err != nil {
		return err
	}
	if p.Selected != nil && p.Selected.Shadow != nil {
		if err := p.Selected.Shadow.StrategyError(); err != nil {
			return err
		}
	}
	if p.S01 != nil && p.S01.Failure != "" {
		return errors.New(p.S01.Failure)
	}
	if p.S01 != nil && p.S01.Shadow != nil {
		if err := p.S01.Shadow.StrategyError(); err != nil {
			return err
		}
	}
	return nil
}
