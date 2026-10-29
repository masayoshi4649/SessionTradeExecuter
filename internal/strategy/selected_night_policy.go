package strategy

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// selectedNightRules は共通のExpr実行器を夜間の純判断へ渡す最小契約です。
type selectedNightRules interface {
	Bool(string, map[string]any) bool
	Number(string, map[string]any) float64
	Err() error
}

// selectedNightParent は影Sourceの親と初回待機を実所有とは別に保持します。
type selectedNightParent struct {
	Side                int
	InitialSource       string
	InitialMicro        int
	Deferred, Cancelled bool
	DeferredAt          float64
	DeferredIndex       int64
}

// selectedNightLotView は共通実帳簿から判断時点に取得する所有玉の読取専用写像です。
// 新規の基準はmicro換算、返済はRemainingActualを優先します。非整除Capで実玉を丸め消しません。
type selectedNightLotView struct {
	ID, Parent, SourceID, Kind string
	Side, RemainingMicro       int
	RemainingActual, Cap       int
	Entry, BestGain            float64
}

// selectedNightLotProtection は実玉ごとの資格と単調な利益床です。
type selectedNightLotProtection struct {
	ResidualEligible, Activated        bool
	ResidualTime, ActivatedTime, Floor float64
	ResidualIndex, ActivatedIndex      int64
}

// selectedNightIntent は共通発注器へ渡す希望量です。実約定や予約を仮定しません。
type selectedNightIntent struct {
	Parent, SourceID, ActualID, Kind, Reason string
	Side, QuantityMicro                      int
	QuantityActual                           int
	Gain, Floor                              float64
}

// selectedNightPolicy はP164_06の実玉保護とU165_03の初回待機を独立管理します。
// Source残量・実玉・予約の正本は共通帳簿です。この型から影Sourceへ実利益を返しません。
type selectedNightPolicy struct {
	Method    string
	Parents   map[string]*selectedNightParent
	Lots      map[string]*selectedNightLotProtection
	View      selectedNightStateView
	LastIndex int64
	LastTime  float64
}

// newSelectedNightPolicy は正本IDだけを受け付けて夜間状態を生成します。
//
// 引数: method(string)。返り値: *selectedNightPolicyとerror。時間割と発注商品は共通側で検査します。
func newSelectedNightPolicy(method string) (*selectedNightPolicy, error) {
	if method == "P164_06" || method == "U165_03" {
		method = "NK225_" + method
	}
	if method != "NK225_P164_06" && method != "NK225_U165_03" {
		return nil, errors.New("選定夜間の不変手法IDではありません")
	}
	return &selectedNightPolicy{Method: method, Parents: map[string]*selectedNightParent{}, Lots: map[string]*selectedNightLotProtection{}, LastIndex: -1}, nil
}

// injectSelectedNightView は共通Featuresで準備した当原行の状態だけを受け取ります。
//
// 引数: o(Observation)。返り値: error。過去又は未来の板状態を当原行へ流用しません。
func (n *selectedNightPolicy) injectSelectedNightView(o Observation) error {
	if !o.Selected.NightAvailable || o.Reference != "NK225" || !finite(o.Time) || o.Selected.NightTime != o.Time || o.Selected.NightIndex != o.Index || o.Index <= n.LastIndex || o.Time < n.LastTime {
		return errors.New("夜間状態と当観測の市場・時計・原行順が不一致です")
	}
	n.LastIndex, n.LastTime, n.View = o.Index, o.Time, o.Selected.Night
	return nil
}

// remainingSelectedNight は非整除Capでも実玉の有無を丸め消さずに判定します。
//
// 引数: lot(selectedNightLotView)。返り値: intは実枚数、又は人工・研究のmicro枚数です。
func remainingSelectedNight(lot selectedNightLotView) int {
	if lot.Cap > 0 {
		return lot.RemainingActual
	}
	return lot.RemainingMicro
}

// lotSelectedNight は実約定ごとの保護状態を取得します。
//
// 引数: id(string)。返り値: *selectedNightLotProtection。後続約定は別IDとし旧床を継承しません。
func (n *selectedNightPolicy) lotSelectedNight(id string) *selectedNightLotProtection {
	if n.Lots[id] == nil {
		n.Lots[id] = &selectedNightLotProtection{}
	}
	return n.Lots[id]
}

// sourceNewSelectedNight は元の初回と追加を識別し、初回待機の希望を返します。
//
// 引数: parent/source(string)、side/quantityMicro(int)、o(Observation)、rules(selectedNightRules)。
// 返り値: selectedNightIntentとerror。元通知は一度だけ渡し、Source残量は呼出し前に共通側で更新します。
// 返り値を実注文へ変える時はfresh・時間枠・予約込み上限を共通側で検査します。
func (n *selectedNightPolicy) sourceNewSelectedNight(parent, source string, side, quantityMicro int, o Observation, rules selectedNightRules) (selectedNightIntent, error) {
	if parent == "" || source == "" || (side != 1 && side != -1) || quantityMicro <= 0 || rules == nil {
		return selectedNightIntent{}, errors.New("選定夜間の元新規通知が不正です")
	}
	p := n.Parents[parent]
	initial := p == nil
	if initial {
		p = &selectedNightParent{Side: side, InitialSource: source, InitialMicro: quantityMicro}
		n.Parents[parent] = p
	} else if p.Side != side {
		return selectedNightIntent{}, fmt.Errorf("同じ夜間親の方向が変化しました: %s", parent)
	}
	kind := "source_add"
	if initial {
		kind = "initial"
	}
	intent := selectedNightIntent{Parent: parent, SourceID: source, Kind: kind, Side: side, QuantityMicro: quantityMicro, Reason: kind}
	if initial && n.Method == "NK225_U165_03" {
		opposite := n.View.Sell
		if side < 0 {
			opposite = n.View.Buy
		}
		deferEntry := rules.Bool("selected.u165_03.defer", map[string]any{"known": n.View.Known, "opposite_active": opposite.Active, "opposite_quantity_support": opposite.QuantitySupport})
		if err := rules.Err(); err != nil {
			return selectedNightIntent{}, err
		}
		if deferEntry {
			p.Deferred, p.DeferredAt, p.DeferredIndex = true, o.Time, o.Index
		}
	}
	if p.Deferred || p.Cancelled {
		intent.QuantityMicro = 0
		intent.Reason = "初回待機又は取消済み"
	}
	return intent, rules.Err()
}

// sourceRemainingSelectedNight は待機元IDの消滅を先に確定します。
//
// 引数: parent/source(string)、remainingMicro(int)。返り値: なし。新しい別親の権利は変更しません。
func (n *selectedNightPolicy) sourceRemainingSelectedNight(parent, source string, remainingMicro int) {
	p := n.Parents[parent]
	if p != nil && p.Deferred && p.InitialSource == source && remainingMicro <= 0 {
		p.Deferred, p.Cancelled = false, true
	}
}

// releaseSelectedNight は新しい同方向帯ができた時だけ元量以内の解除希望を返します。
//
// 引数: parent(string)、sourceRemainingMicro(int)、pending/fresh(bool)、end(float64)、o(Observation)、rules(selectedNightRules)。
// 返り値: selectedNightIntentとerror。呼出し側は発注を試みた直後にfinishReleaseSelectedNightを一度呼びます。
func (n *selectedNightPolicy) releaseSelectedNight(parent string, sourceRemainingMicro int, pending, fresh bool, end float64, o Observation, rules selectedNightRules) (selectedNightIntent, error) {
	p := n.Parents[parent]
	if p == nil || !p.Deferred {
		return selectedNightIntent{}, nil
	}
	if rules == nil {
		return selectedNightIntent{}, errors.New("夜間待機のルール実行器がありません")
	}
	expired := rules.Bool("selected.u165_03.timeout", map[string]any{"elapsed": o.Time - p.DeferredAt})
	if err := rules.Err(); err != nil {
		return selectedNightIntent{}, err
	}
	if expired || sourceRemainingMicro <= 0 || o.Time >= end {
		p.Deferred, p.Cancelled = false, true
		return selectedNightIntent{}, nil
	}
	if !fresh || !quoteValid(o) || pending || o.Index <= p.DeferredIndex {
		return selectedNightIntent{}, nil
	}
	hold := n.View.Buy
	if p.Side < 0 {
		hold = n.View.Sell
	}
	release := rules.Bool("selected.u165_03.release", map[string]any{"known": n.View.Known, "same_active": hold.Active, "same_index": float64(hold.Index), "deferred_index": float64(p.DeferredIndex)})
	if err := rules.Err(); err != nil {
		return selectedNightIntent{}, err
	}
	if !release {
		return selectedNightIntent{}, nil
	}
	return selectedNightIntent{Parent: parent, SourceID: p.InitialSource, Kind: "delayed_initial", Reason: "新しい同方向価格帯", Side: p.Side, QuantityMicro: min(p.InitialMicro, sourceRemainingMicro)}, nil
}

// finishReleaseSelectedNight は解除発注を試みた結果を一度だけ反映します。
//
// 引数: parent(string)、acceptedMicro(int)。返り値: なし。枠不足0なら後続追加で初回を復活させません。
func (n *selectedNightPolicy) finishReleaseSelectedNight(parent string, acceptedMicro int) {
	if p := n.Parents[parent]; p != nil && p.Deferred {
		p.Deferred = false
		p.Cancelled = acceptedMicro <= 0
	}
}

// qualifySelectedNight は黒字の元部分返済実約定時点の残玉へだけ資格を与えます。
//
// 引数: parent/reason(string)、gross(float64)、sourceRemainingMicro(int)、o(Observation)、remaining([]selectedNightLotView)。
// 返り値: []stringは新たに資格を得た実玉ID。部分約定は一通知ずつ、実帳簿へ反映した直後の残玉を渡します。
func (n *selectedNightPolicy) qualifySelectedNight(parent, reason string, gross float64, sourceRemainingMicro int, o Observation, remaining []selectedNightLotView) []string {
	if n.Method != "NK225_P164_06" || !strings.HasPrefix(reason, "source_partial/") || gross <= 0 || sourceRemainingMicro <= 0 {
		return nil
	}
	qualified := []string{}
	for _, lot := range remaining {
		if lot.Parent != parent || remainingSelectedNight(lot) <= 0 {
			continue
		}
		state := n.lotSelectedNight(lot.ID)
		if !state.ResidualEligible {
			state.ResidualEligible, state.ResidualTime, state.ResidualIndex = true, o.Time, o.Index
			qualified = append(qualified, lot.ID)
		}
	}
	return qualified
}

// protectSelectedNight は既存実玉だけへP164_06の局所保護を判断します。
//
// 引数: lot(selectedNightLotView)、o(Observation)、parentClosePending/fresh(bool)、rules(selectedNightRules)。
// 返り値: selectedNightIntentとerror。新規予約だけでは遮断せず、元返済予約のある親は次回へ送ります。
// fresh=falseでは起動・床・新しい返済希望を作りません。確定済み返済の再送管理は共通側の責務です。
func (n *selectedNightPolicy) protectSelectedNight(lot selectedNightLotView, o Observation, parentClosePending, fresh bool, rules selectedNightRules) (selectedNightIntent, error) {
	if n.Method != "NK225_P164_06" || !fresh || !quoteValid(o) || parentClosePending || remainingSelectedNight(lot) <= 0 {
		return selectedNightIntent{}, nil
	}
	p := n.Parents[lot.Parent]
	if p == nil || lot.ID == "" || !finite(lot.Entry) || lot.Entry <= 0 || !finite(lot.BestGain) || lot.Side != p.Side || rules == nil || lot.Cap < 0 || (lot.Cap > 0 && lot.RemainingActual > lot.Cap) {
		return selectedNightIntent{}, errors.New("夜間利益保護の所有又は実建値が不正です")
	}
	mark := o.Bid
	if lot.Side < 0 {
		mark = o.Ask
	}
	gain := float64(lot.Side) * (mark - lot.Entry)
	state := n.lotSelectedNight(lot.ID)
	tight := rules.Bool("selected.p164_06.small_initial", map[string]any{"initial_micro": float64(p.InitialMicro), "is_initial": lot.Kind == "initial" || lot.Kind == "delayed_initial"})
	if (tight || state.ResidualEligible) && !state.Activated && rules.Bool("selected.p164_06.activate", map[string]any{"gain": gain}) {
		state.Activated, state.ActivatedTime, state.ActivatedIndex = true, o.Time, o.Index
	}
	if err := rules.Err(); err != nil {
		return selectedNightIntent{}, err
	}
	reason := ""
	if state.Activated {
		floor := 0.0
		reason = "protect_residual_floor"
		if tight {
			floor = rules.Number("selected.p164_06.trail_floor", map[string]any{"best_gain": lot.BestGain})
			reason = "protect_10trail20"
		}
		if err := rules.Err(); err != nil {
			return selectedNightIntent{}, err
		}
		if !finite(floor) || floor < 0 || floor > math.Max(0, lot.BestGain) {
			return selectedNightIntent{}, errors.New("夜間利益床の式が有効な順行範囲外です")
		}
		state.Floor = math.Max(state.Floor, floor)
		if gain > state.Floor {
			reason = ""
		}
	}
	if reason == "" && rules.Bool("selected.p164_06.add_stop", map[string]any{"source_add": lot.Kind == "source_add", "gain": gain}) {
		reason = "source_add_stop50"
	}
	if err := rules.Err(); err != nil {
		return selectedNightIntent{}, err
	}
	if reason == "" {
		return selectedNightIntent{}, nil
	}
	return selectedNightIntent{Parent: lot.Parent, SourceID: lot.SourceID, ActualID: lot.ID, Kind: "policy_close", Reason: reason, Side: -lot.Side, QuantityMicro: lot.RemainingMicro, QuantityActual: lot.RemainingActual, Gain: gain, Floor: state.Floor}, nil
}
