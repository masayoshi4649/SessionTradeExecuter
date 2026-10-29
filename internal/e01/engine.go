// Package strategy は、観測済みデータだけで E01 の判断と仮想建玉を更新します。
package e01

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// Config は外部から指定した商品の呼値・倍率と、E01 の運転変数です。
type Config struct {
	Symbol             string  `json:"symbol"`
	InitialQuantity    int     `json:"initial_quantity"`
	MaxQuantity        int     `json:"max_quantity"`
	Tick               float64 `json:"tick"`
	PointValue         float64 `json:"point_value"`
	FillMode           string  `json:"fill_mode"`
	WarmupSeconds      float64 `json:"warmup_seconds"`
	EntryCutoffSeconds float64 `json:"entry_cutoff_seconds"`
	CooldownSeconds    float64 `json:"cooldown_seconds"`
	PendingSeconds     float64 `json:"pending_seconds"`
	MaxHoldSeconds     float64 `json:"max_hold_seconds"`
	EntryATR           float64 `json:"entry_atr"`
	NormalRiskATR      float64 `json:"normal_risk_atr"`
	TightRiskATR       float64 `json:"tight_risk_atr"`
	TrailArmATR        float64 `json:"trail_arm_atr"`
	NormalTrailATR     float64 `json:"normal_trail_atr"`
	TightArmATR        float64 `json:"tight_arm_atr"`
	TightTrailATR      float64 `json:"tight_trail_atr"`
	SplitATR           float64 `json:"split_atr"`
	ActualRiskATR      float64 `json:"actual_risk_atr"`
}

// DefaultConfig は既存 E01 と同じ変数を返します。
//
// 引数: symbol(string) は取引商品、quantity/cap(int) は初回枚数と上限、tick/value(float64) は外部指定の呼値と倍率。
// 返り値: Config は既定条件です。商品情報を内部で定義しません。
func DefaultConfig(symbol string, quantity, cap int, tick, value float64) Config {
	return Config{Symbol: symbol, InitialQuantity: quantity, MaxQuantity: cap, Tick: tick, PointValue: value, FillMode: "observed",
		WarmupSeconds: 1200, EntryCutoffSeconds: 300, CooldownSeconds: 300, PendingSeconds: 300, MaxHoldSeconds: 21600,
		EntryATR: 0.25, NormalRiskATR: 1, TightRiskATR: 0.75, TrailArmATR: 1.5, NormalTrailATR: 3, TightArmATR: 3, TightTrailATR: 0.75, SplitATR: 2, ActualRiskATR: 1}
}

// Session は controller から得た、開始・強制返済判断・終場の UNIX 秒です。
type Session struct {
	ID         string  `json:"id"`
	Start      float64 `json:"start"`
	ForceClose float64 `json:"force_close"`
	Close      float64 `json:"close"`
}

// Tick は未来情報を含まない一件の価格と判断根拠です。InputIndex=-1 は連番照合を省きます。
type Tick struct {
	Time             float64
	Price            float64
	ATR              float64
	Direction        int
	Adverse          int
	AdverseSource    int64
	AllowEntry       bool
	InputIndex       int64
	ObservedRecovery bool
}

// Event は一件の新規・返済の判断記録です。注文方向は Side() で取得します。
type Event struct {
	SessionID         string  `json:"session_id"`
	Symbol            string  `json:"symbol"`
	Strategy          string  `json:"strategy"`
	Event             string  `json:"event"`
	Direction         int     `json:"direction"`
	Quantity          int     `json:"quantity"`
	Price             float64 `json:"price"`
	Reason            string  `json:"reason"`
	ReasonCode        int     `json:"reason_code"`
	Campaign          int     `json:"campaign"`
	Row               int64   `json:"row"`
	SourceRow         int64   `json:"source_row"`
	Timestamp         float64 `json:"timestamp"`
	InitialATR        float64 `json:"initial_atr"`
	EntryPrice        float64 `json:"entry_price"`
	RealizedPoints    float64 `json:"realized_points"`
	RealizedYen       float64 `json:"realized_yen"`
	PositionAfter     int     `json:"position_after"`
	PositionDirection int     `json:"position_direction"`
	FillMode          string  `json:"fill_mode"`
}

// Side は新規・返済を考慮した注文方向を返します。
//
// 引数: なし。返り値: int は買い=1、売り=-1です。
func (e Event) Side() int {
	if e.Event == "返済" {
		return -e.Direction
	}
	return e.Direction
}

type pendingEntry struct {
	Direction                  int
	ATR, Level, Extreme, Until float64
	Row, PrepareRow            int64
	PrepareID                  int
}
type level struct {
	Reason    int
	Price     *float64
	Triggered bool
}
type exitPlan struct {
	Action    int
	ActionRow int64
	Levels    []level
}
type shadow struct {
	Direction, Campaign, Quantity             int
	Anchor, ATR, Extreme, Realized, EntryTime float64
	EntryRow                                  int64
	LastExit                                  *float64
	SplitDone                                 bool
	TrailFloor                                *float64
	Plan                                      *exitPlan
	Pending                                   *pendingEntry
	PrepareCount                              int
	GateArm                                   int64
}
type reduction struct {
	Slots         []int
	Row, Evidence int64
}
type actual struct {
	Direction, Campaign int
	Anchor, ATR         float64
	Slots, Alive        []bool
	Realized            float64
	Reductions          int
	Barrier             int64
	Pending             *reduction
	Budget              *int64
	EntryRow            int64
	EntryTime           float64
}
type shadowAction struct {
	Reason              int
	Price               float64
	Source              int64
	Direction, Campaign int
	ATR, Anchor         float64
}

// Engine は JSON 保存可能な E01 状態です。通常影と早期出口影を独立に進めます。
type Engine struct {
	Rules             *rules.Runtime `json:"-"`
	Config            Config         `json:"config"`
	Session           Session        `json:"session"`
	Version           int            `json:"version"`
	Row               int64          `json:"row"`
	LastTime          *float64       `json:"last_time"`
	LastPrice         float64        `json:"last_price"`
	RealizedYen       float64        `json:"realized_yen"`
	Normal            shadow         `json:"normal"`
	Exit              shadow         `json:"exit"`
	Actual            actual         `json:"actual"`
	Halted            bool           `json:"halted"`
	LastEventBefore   *actual        `json:"last_event_before,omitempty"`
	LastEventSelected []int          `json:"last_event_selected,omitempty"`
	LastEventRetire   bool           `json:"last_event_retire"`
	LastEventRealized float64        `json:"last_event_realized"`
	observedRecovery  bool
}

// Status は実運転側が照合する現在の E01 仮想建玉です。
type Status struct {
	Quantity, Direction                 int
	EntryPrice, InitialATR, RealizedYen float64
	Campaign                            int
	Row                                 int64
	PendingEntry                        bool
	GateArmed                           bool
	Halted                              bool
}

var reasons = map[int]string{1: "組の損切り", 2: "追跡返済", 7: "2ATRの部分利確", 8: "保有時間上限", 9: "セッション内返済", 20: "追跡新規", 30: "順行後の悪化で半減", 32: "実建玉の損失予算", 90: "運転上の返済"}

// finite は NaN と無限値を拒否します。
//
// 引数: v(float64) は検査値。返り値: bool は有限値なら真です。
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// pointer は有限数値のコピーを指すポインターを返します。
//
// 引数: v(float64) は値。返り値: *float64 は独立した保存値です。
func pointer(v float64) *float64 { return &v }

// rounded は注文方向に応じて呼値へ丸めます。
//
// 引数: value/tick(float64) は価格と呼値、side(int) は売買方向。返り値: float64 は丸めた価格です。
func rounded(value, tick float64, side int) float64 {
	if side > 0 {
		return math.Ceil(value/tick-1e-9) * tick
	}
	return math.Floor(value/tick+1e-9) * tick
}

// newShadow は影運転の初期状態を作ります。
//
// 引数: なし。返り値: shadow は新しい状態です。
func newShadow() shadow { return shadow{Campaign: -1, EntryRow: -1, GateArm: -1} }

// newActual は実仮想建玉の初期状態を作ります。
//
// 引数: なし。返り値: actual は空の状態です。
func newActual() actual { return actual{Campaign: -1, Barrier: -1, EntryRow: -1} }

// NewEngine は E01 のセッション状態を初期化します。
//
// 引数: cfg(Config) は取引条件、session(Session) は API 由来の期間。
// 返り値: *Engine は新しい注文器、error は不正な条件です。
func NewEngine(cfg Config, session Session) (*Engine, error) {
	if cfg.InitialQuantity < 1 || cfg.InitialQuantity > cfg.MaxQuantity || cfg.MaxQuantity < 1 || cfg.Symbol == "" {
		return nil, errors.New("初回枚数・片方向上限・商品が不正です")
	}
	if cfg.FillMode != "specified" && cfg.FillMode != "observed" {
		return nil, errors.New("約定方式が不正です")
	}
	for _, v := range []float64{cfg.Tick, cfg.PointValue, cfg.PendingSeconds, cfg.MaxHoldSeconds, cfg.EntryATR, cfg.NormalRiskATR, cfg.TightRiskATR, cfg.TrailArmATR, cfg.NormalTrailATR, cfg.TightArmATR, cfg.TightTrailATR, cfg.SplitATR, cfg.ActualRiskATR} {
		if !finite(v) || v <= 0 {
			return nil, errors.New("戦略の呼値・倍率・幅は正の有限値が必要です")
		}
	}
	for _, v := range []float64{cfg.WarmupSeconds, cfg.EntryCutoffSeconds, cfg.CooldownSeconds} {
		if !finite(v) || v < 0 {
			return nil, errors.New("戦略の待機時間は非負の有限値が必要です")
		}
	}
	if !finite(session.Start) || !finite(session.ForceClose) || !finite(session.Close) || session.ID == "" || session.Start >= session.ForceClose || session.ForceClose >= session.Close {
		return nil, errors.New("セッションの開始・返済判断・終場が不正です")
	}
	return &Engine{Config: cfg, Session: session, Version: 1, Row: -1, Normal: newShadow(), Exit: newShadow(), Actual: newActual()}, nil
}

// Snapshot は再起動用に状態を JSON 化します。
//
// 引数: なし。返り値: []byte は保存データ、error は符号化エラーです。
func (e *Engine) Snapshot() ([]byte, error) { return json.Marshal(e) }

// RestoreEngine は保存データから状態を復元し、枚数と条件を検証します。
//
// 引数: data([]byte) は Snapshot の結果。返り値: *Engine は復元した状態、error は不整合です。
func RestoreEngine(data []byte) (*Engine, error) {
	var e Engine
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	if e.Version != 1 {
		return nil, errors.New("戦略保存形式が非対応です")
	}
	if _, err := NewEngine(e.Config, e.Session); err != nil {
		return nil, err
	}
	if err := e.checkPositions(); err != nil {
		return nil, err
	}
	return &e, nil
}

// Clone は注文送信前の予測処理に使う独立した状態を作ります。
//
// 引数: なし。返り値: *Engine は複製、error は保存・復元エラーです。
func (e *Engine) Clone() (*Engine, error) {
	b, err := e.Snapshot()
	if err != nil {
		return nil, err
	}
	return RestoreEngine(b)
}

// count は生存フラグの数を数えます。
//
// 引数: values([]bool) は元枠。返り値: int は真の数です。
func count(values []bool) int {
	n := 0
	for _, v := range values {
		if v {
			n++
		}
	}
	return n
}

// indexes はゼロから指定個数までの元枠番号を作ります。
//
// 引数: n(int) は個数。返り値: []int は番号列です。
func indexes(n int) []int {
	result := make([]int, n)
	for i := range result {
		result[i] = i
	}
	return result
}

// Status は現在の片方向建玉と新規準備の状態を返します。
//
// 引数: なし。返り値: Status は現在状態です。
func (e *Engine) Status() Status {
	a := e.Actual
	return Status{Quantity: count(a.Slots), Direction: a.Direction, EntryPrice: a.Anchor, InitialATR: a.ATR, RealizedYen: e.RealizedYen, Campaign: a.Campaign, Row: e.Row, PendingEntry: e.Normal.Pending != nil, GateArmed: e.Normal.GateArm >= 0, Halted: e.Halted}
}

// CancelEntry は新規準備を取り消し、既存建玉の返済条件を残します。
//
// 引数: なし。返り値: なし。
func (e *Engine) CancelEntry() { e.Normal.Pending = nil; e.Exit.Pending = nil }

// checkPositions は元枠会計と片方向枚数上限を検査します。
//
// 引数: なし。返り値: error は不整合、正常時 nil です。
func (e *Engine) checkPositions() error {
	a := e.Actual
	n := len(a.Slots)
	if n != len(a.Alive) || (n != 0 && n != e.Config.InitialQuantity) || count(a.Slots) > e.Config.MaxQuantity {
		return errors.New("元枠又は片方向枚数上限が不正です")
	}
	for i, v := range a.Slots {
		if v && !a.Alive[i] {
			return errors.New("廃止元枠に建玉があります")
		}
	}
	if a.Direction < -1 || a.Direction > 1 || (count(a.Slots) > 0) != (a.Direction != 0) {
		return errors.New("建玉枚数と方向が不整合です")
	}
	return nil
}

// shadowTick は前観測で作った影の条件を執行し、次観測用条件を作ります。
//
// 引数: sh(*shadow) は影、t/p/atr(float64) は時刻・価格・ATR、direction(int) は許可方向、allowPrepare/tight(bool) は準備許可と早期出口。
// 返り値: *shadowAction は約定、bool は今回新規準備した場合に真です。
func (e *Engine) shadowTick(sh *shadow, t, p, atr float64, direction int, allowPrepare, tight bool) (*shadowAction, bool) {
	row, q0, tick := e.Row, e.Config.InitialQuantity, e.Config.Tick
	var action *shadowAction
	prepared := false
	plan := sh.Plan
	if sh.Direction != 0 && plan != nil {
		if plan.Action != 0 {
			action = &shadowAction{Reason: plan.Action, Price: p, Source: plan.ActionRow}
		} else {
			best := math.Inf(1)
			for _, v := range plan.Levels {
				side := -sh.Direction
				if v.Reason == 7 {
					side = sh.Direction
				}
				if v.Price == nil || (!v.Triggered && float64(side)*(p-*v.Price) < -1e-8) {
					continue
				}
				distance := 0.0
				if !v.Triggered {
					distance = math.Max(0, float64(side)*(*v.Price-e.LastPrice))
				}
				if distance < best-1e-9 {
					best = distance
					fill := *v.Price
					if v.Triggered || e.observedRecovery || e.Config.FillMode == "observed" {
						fill = p
					}
					action = &shadowAction{Reason: v.Reason, Price: fill, Source: row - 1}
				}
			}
		}
	} else if sh.Direction == 0 && sh.Pending != nil {
		pend := sh.Pending
		if t < pend.Until && t < e.Session.ForceClose-e.Config.EntryCutoffSeconds && float64(pend.Direction)*(p-pend.Level) >= -1e-8 {
			fill := pend.Level
			if e.Config.FillMode == "observed" {
				fill = p
			}
			sh.Direction = pend.Direction
			sh.Campaign++
			sh.Quantity = q0
			sh.Anchor = fill
			sh.ATR = pend.ATR
			sh.Extreme = fill
			sh.Realized = 0
			sh.EntryTime = t
			sh.EntryRow = row
			sh.SplitDone = false
			sh.TrailFloor = nil
			sh.GateArm = -1
			action = &shadowAction{Reason: 20, Price: fill, Source: pend.Row}
			sh.Pending = nil
		}
	}
	if action != nil {
		action.Direction = sh.Direction
		action.Campaign = sh.Campaign
		action.ATR = sh.ATR
		action.Anchor = sh.Anchor
		if action.Reason != 20 {
			quantity := sh.Quantity
			if action.Reason == 7 {
				quantity = max(1, sh.Quantity/2)
			}
			sh.Realized += float64(quantity*sh.Direction) * (action.Price - sh.Anchor)
			sh.Quantity -= quantity
			if action.Reason == 7 {
				sh.SplitDone = true
			}
			if sh.Quantity == 0 {
				sh.Direction = 0
				sh.LastExit = pointer(t)
				sh.Pending = nil
				sh.GateArm = -1
			}
		}
	}
	// ----------------------------------------
	// 現在観測で作成した条件は次の観測まで執行しません。
	sh.Plan = nil
	if sh.Direction != 0 {
		d, a, anchor := sh.Direction, sh.ATR, sh.Anchor
		if d > 0 {
			sh.Extreme = math.Max(sh.Extreme, p)
		} else {
			sh.Extreme = math.Min(sh.Extreme, p)
		}
		riskFactor := e.Config.NormalRiskATR
		if tight {
			riskFactor = e.Config.TightRiskATR
		}
		riskFactor = e.ruleNumber("shadow.risk_factor", riskFactor, map[string]any{"tight": tight, "tight_risk": e.Config.TightRiskATR, "normal_risk": e.Config.NormalRiskATR})
		floor := -float64(q0) * a * riskFactor
		stop := rounded(anchor+(floor-sh.Realized)/float64(d*sh.Quantity), tick, -d)
		favorable := float64(d) * (sh.Extreme - anchor)
		if e.ruleBool("shadow.trail_active", favorable >= e.Config.TrailArmATR*a, map[string]any{"gain": favorable, "arm_atr": e.Config.TrailArmATR, "atr": a}) {
			width := e.RuleTrailWidth(tight, favorable, a)
			candidate := rounded(sh.Extreme-float64(d)*width*a, tick, -d)
			if sh.TrailFloor == nil || float64(d)*(candidate-*sh.TrailFloor) > 0 {
				sh.TrailFloor = pointer(candidate)
			}
			if sh.GateArm < 0 {
				sh.GateArm = row
			}
		}
		var split *float64
		if !sh.SplitDone && sh.Quantity >= 2 {
			split = pointer(rounded(anchor+float64(d)*e.RuleSplitDistance(a), tick, d))
		}
		levels := []level{{Reason: 1, Price: pointer(stop)}, {Reason: 2, Price: sh.TrailFloor}, {Reason: 7, Price: split}}
		pendingAction := 0
		if t >= e.Session.ForceClose {
			pendingAction = 9
		} else if sh.Realized+float64(sh.Quantity*d)*(p-anchor) <= floor-1e-8 {
			pendingAction = 1
		} else if e.ruleBool("shadow.hold_expired", t-sh.EntryTime >= e.Config.MaxHoldSeconds, map[string]any{"elapsed": t - sh.EntryTime, "threshold": e.Config.MaxHoldSeconds}) {
			pendingAction = 8
		}
		for i := range levels {
			side := -d
			if levels[i].Reason == 7 {
				side = d
			}
			levels[i].Triggered = levels[i].Price != nil && float64(side)*(p-*levels[i].Price) >= -1e-8
		}
		sh.Plan = &exitPlan{Action: pendingAction, ActionRow: row, Levels: levels}
	} else {
		pend := sh.Pending
		if pend != nil && (t >= pend.Until || direction != pend.Direction) {
			sh.Pending = nil
			pend = nil
		}
		if pend != nil {
			d := pend.Direction
			if d > 0 {
				pend.Extreme = math.Min(pend.Extreme, p)
			} else {
				pend.Extreme = math.Max(pend.Extreme, p)
			}
			price := rounded(pend.Extreme+float64(d)*e.ruleEntryDistance(pend.ATR), tick, d)
			if price != pend.Level {
				pend.Level = price
				pend.Row = row
			}
		}
		cooldown := sh.LastExit == nil || e.ruleBool("shadow.cooldown", t >= *sh.LastExit+e.Config.CooldownSeconds, map[string]any{"elapsed": t - *sh.LastExit, "threshold": e.Config.CooldownSeconds})
		if allowPrepare && sh.Pending == nil && direction != 0 && cooldown && e.ruleBool("shadow.warmup", t >= e.Session.Start+e.Config.WarmupSeconds, map[string]any{"elapsed": t - e.Session.Start, "threshold": e.Config.WarmupSeconds}) && t < e.Session.ForceClose-e.Config.EntryCutoffSeconds && finite(atr) && atr > 0 {
			a := math.Max(atr, tick*2)
			sh.Pending = &pendingEntry{Direction: direction, ATR: a, Level: rounded(p+float64(direction)*e.ruleEntryDistance(a), tick, direction), Extreme: p, Until: t + e.Config.PendingSeconds, Row: row, PrepareRow: row, PrepareID: sh.PrepareCount}
			sh.PrepareCount++
			prepared = true
		}
	}
	return action, prepared
}

// event は現在状態を含む一件の約定記録を作ります。
//
// 引数: kind/customReason(string)、quantity/reason/direction(int)、source(int64)、price/t/pnl(float64)。
// 返り値: Event は記録です。
func (e *Engine) event(kind string, quantity int, price float64, reason int, source int64, t float64, direction int, pnl float64, customReason string) Event {
	a := e.Actual
	description := reasons[reason]
	if customReason != "" {
		description = customReason
	}
	mode := e.Config.FillMode
	if reason == 90 {
		mode = "observed"
	}
	if e.observedRecovery {
		mode = "observed_recovery"
	}
	return Event{SessionID: e.Session.ID, Symbol: e.Config.Symbol, Strategy: "E01", Event: kind, Direction: direction, Quantity: quantity, Price: price, Reason: description, ReasonCode: reason, Campaign: a.Campaign, Row: e.Row, SourceRow: source, Timestamp: t, InitialATR: a.ATR, EntryPrice: a.Anchor, RealizedPoints: pnl, RealizedYen: pnl * e.Config.PointValue, PositionAfter: count(a.Slots), PositionDirection: a.Direction, FillMode: mode}
}

// closeSlots は指定元枠を返済し、確定損益を反映します。
//
// 引数: selected([]int)、price/t(float64)、reason(int)、source(int64)、retire(bool)、customReason(string)。
// 返り値: []Event は実際に保有していた枚数の記録です。
func (e *Engine) closeSlots(selected []int, price float64, reason int, source int64, t float64, retire bool, customReason string) []Event {
	before := copyActual(e.Actual)
	e.LastEventBefore = &before
	e.LastEventSelected = append([]int(nil), selected...)
	e.LastEventRetire = retire
	e.LastEventRealized = e.RealizedYen
	a := &e.Actual
	quantity := 0
	d := a.Direction
	for _, i := range selected {
		if a.Slots[i] {
			quantity++
		}
		a.Slots[i] = false
		if retire {
			a.Alive[i] = false
		}
	}
	pnl := float64(quantity*d) * (price - a.Anchor)
	a.Realized += pnl
	e.RealizedYen += pnl * e.Config.PointValue
	a.Barrier = e.Row
	if count(a.Slots) == 0 {
		a.Direction = 0
		a.Pending = nil
		a.Budget = nil
	}
	if quantity == 0 {
		return nil
	}
	return []Event{e.event("返済", quantity, price, reason, source, t, d, pnl, customReason)}
}

// OnTick は一つの価格観測を進め、既設条件の判断記録を返します。
//
// 引数: in(Tick) は現在までの観測。ATR=0 は暖機未完了です。
// 返り値: []Event は新規・返済、error は逆行時計・連番・終場越え等の異常です。
func (e *Engine) OnTick(in Tick) (result []Event, err error) {
	if err := e.Rules.Err(); err != nil {
		return nil, err
	}
	defer func() {
		if failure := e.Rules.Err(); failure != nil {
			result, err = nil, failure
		}
	}()
	t, p := in.Time, in.Price
	e.observedRecovery = in.ObservedRecovery
	if !finite(t) || !finite(p) || p <= 0 || in.Direction < -1 || in.Direction > 1 || in.Adverse < -1 || in.Adverse > 1 {
		return nil, errors.New("価格観測又は方向が不正です")
	}
	if e.LastTime != nil && t < *e.LastTime {
		return nil, errors.New("価格観測時刻が逆行しました")
	}
	row := e.Row + 1
	if in.InputIndex >= 0 && in.InputIndex != row {
		return nil, fmt.Errorf("特徴量と注文器の連番が不一致です: 特徴量=%d 注文器=%d", in.InputIndex, row)
	}
	source := in.AdverseSource
	if source < -1 || source > row || (in.Adverse != 0 && source < 0) {
		return nil, errors.New("悪化根拠の観測連番が不正です")
	}
	if t >= e.Session.Close && count(e.Actual.Slots) > 0 {
		return nil, errors.New("終場を越えた未返済があります。過去価格で返済できません")
	}
	e.Row = row
	allowed := in.AllowEntry && !e.Halted && t < e.Session.Close
	if !allowed {
		e.CancelEntry()
	}
	direction := in.Direction
	if !allowed {
		direction = 0
	}
	_, prepared := e.shadowTick(&e.Normal, t, p, in.ATR, direction, allowed, false)
	action, _ := e.shadowTick(&e.Exit, t, p, in.ATR, direction, prepared, true)
	events := []Event{}
	acted := false
	if action != nil && action.Reason == 20 {
		if e.Actual.Direction != 0 {
			return nil, errors.New("未返済のまま新しい組を開始しました")
		}
		before := copyActual(e.Actual)
		e.LastEventBefore = &before
		e.LastEventSelected = nil
		e.LastEventRetire = false
		e.LastEventRealized = e.RealizedYen
		a := newActual()
		a.Direction = action.Direction
		a.Campaign = action.Campaign
		a.Anchor = action.Price
		a.ATR = action.ATR
		a.Slots = make([]bool, e.Config.InitialQuantity)
		a.Alive = make([]bool, e.Config.InitialQuantity)
		for i := range a.Slots {
			a.Slots[i] = true
			a.Alive[i] = true
		}
		a.Barrier = row
		a.EntryRow = row
		a.EntryTime = t
		e.Actual = a
		events = append(events, e.event("新規", e.Config.InitialQuantity, action.Price, 20, action.Source, t, a.Direction, 0, ""))
		acted = true
	} else if action != nil && e.Actual.Direction != 0 {
		e.Actual.Pending = nil
		n := e.Config.InitialQuantity
		if action.Reason == 7 {
			n /= 2
		}
		events = append(events, e.closeSlots(indexes(n), action.Price, action.Reason, action.Source, t, true, "")...)
		acted = true
	} else if e.Actual.Direction != 0 && e.Actual.Budget != nil {
		events = append(events, e.closeSlots(indexes(e.Config.InitialQuantity), p, 32, *e.Actual.Budget, t, true, "")...)
		acted = true
	} else if e.Actual.Direction != 0 && e.Actual.Pending != nil {
		pending := e.Actual.Pending
		e.Actual.Pending = nil
		events = append(events, e.closeSlots(pending.Slots, p, 30, pending.Row, t, false, "")...)
		e.Actual.Reductions++
		acted = true
	}
	// ----------------------------------------
	// 通常影の順行到達より後に得た、新しい逆行情報だけを次観測の減量に使います。
	a := &e.Actual
	if a.Direction != 0 {
		equity := a.Realized + float64(count(a.Slots)*a.Direction)*(p-a.Anchor)
		if equity <= -float64(e.Config.InitialQuantity)*a.ATR*e.Config.ActualRiskATR && a.Budget == nil {
			a.Budget = &row
			a.Pending = nil
		}
		normal := e.Normal
		if !acted && a.Pending == nil && a.Budget == nil && a.Reductions < 1 && normal.Direction != 0 && normal.Campaign == a.Campaign && normal.GateArm >= 0 && source > normal.GateArm && in.Adverse == -a.Direction && source > a.Barrier {
			n := min(count(a.Alive)-1, max(1, count(a.Alive)/2))
			selected := []int{}
			for i, v := range a.Slots {
				if v && a.Alive[i] && len(selected) < n {
					selected = append(selected, i)
				}
			}
			if len(selected) > 0 {
				a.Pending = &reduction{Slots: selected, Row: row, Evidence: source}
			}
		}
	}
	e.LastTime = pointer(t)
	e.LastPrice = p
	if err := e.checkPositions(); err != nil {
		return nil, err
	}
	return events, nil
}

// ForceFlatten は実際に観測した現在価格で全建玉を返済します。
//
// 引数: t/price(float64) は観測時刻・価格、reason(string) は運転上の理由。
// 返り値: []Event は返済記録、error は終場後の遡及返済などの異常です。
func (e *Engine) ForceFlatten(t, price float64, reason string) ([]Event, error) {
	if !finite(t) || !finite(price) || price <= 0 {
		return nil, errors.New("返済用観測が不正です")
	}
	if t >= e.Session.Close && count(e.Actual.Slots) > 0 {
		return nil, errors.New("終場後に前セッション建玉を遡及返済できません")
	}
	if e.LastTime != nil && t < *e.LastTime {
		return nil, errors.New("返済観測が過去です")
	}
	events := e.closeSlots(indexes(len(e.Actual.Slots)), price, 90, e.Row, t, true, reason)
	for _, sh := range []*shadow{&e.Normal, &e.Exit} {
		fresh := newShadow()
		fresh.Campaign = sh.Campaign
		fresh.LastExit = pointer(t)
		fresh.PrepareCount = sh.PrepareCount
		*sh = fresh
	}
	e.LastTime = pointer(t)
	e.LastPrice = price
	return events, e.checkPositions()
}

// copyActual は元枠配列を共有しない建玉状態の複製を作ります。
//
// 引数: source(actual) は元状態。返り値: actual は複製です。
func copyActual(source actual) actual {
	source.Slots = append([]bool(nil), source.Slots...)
	source.Alive = append([]bool(nil), source.Alive...)
	if source.Pending != nil {
		p := *source.Pending
		p.Slots = append([]int(nil), p.Slots...)
		source.Pending = &p
	}
	return source
}

// SyncActual は直前の発注意図に対し、実約定台帳の数量と平均単価を反映します。
//
// 引数: quantity/direction(int) は所有実建玉、averageEntryPrice/realizedYen(float64) は実平均取得価格とセッション累計確定損益、origin(Event) は直前意図。
// 返り値: error は不正な枚数・方向・元枠対応です。影の判断系列は変えません。
// 未約定中は OnTick を止め、同じ origin に累積約定を繰返し同期してください。
// 全返済意図に残玉がある場合は、実行層が残数量を返済するまで通常判断を再開してはいけません。
func (e *Engine) SyncActual(quantity, direction int, averageEntryPrice, realizedYen float64, origin Event) error {
	if quantity < 0 || quantity > e.Config.InitialQuantity || quantity > e.Config.MaxQuantity || direction < -1 || direction > 1 || (quantity > 0) != (direction != 0) || !finite(realizedYen) || (quantity > 0 && (!finite(averageEntryPrice) || averageEntryPrice <= 0)) {
		return errors.New("実約定台帳の枚数・方向・単価が不正です")
	}
	if origin.Campaign != e.Actual.Campaign || origin.Symbol != e.Config.Symbol || origin.Row != e.Row || e.LastEventBefore == nil {
		return errors.New("実約定同期の発注意図が現在の戦略と一致しません")
	}
	if quantity > 0 && direction != origin.Direction {
		return errors.New("実建玉が発注意図と逆方向です")
	}
	if origin.Event == "新規" {
		a := &e.Actual
		a.Slots = make([]bool, e.Config.InitialQuantity)
		a.Alive = make([]bool, e.Config.InitialQuantity)
		for i := 0; i < quantity; i++ {
			a.Slots[i] = true
			a.Alive[i] = true
		}
		a.Direction = direction
		a.Anchor = averageEntryPrice
		if quantity == 0 {
			a.Anchor = origin.EntryPrice
		}
		a.Realized = 0
		a.Pending = nil
		a.Budget = nil
	} else if origin.Event == "返済" {
		before := copyActual(*e.LastEventBefore)
		closed := count(before.Slots) - quantity
		if closed < 0 || closed > origin.Quantity {
			return errors.New("実返済数量が発注意図の範囲外です")
		}
		remaining := closed
		for _, i := range e.LastEventSelected {
			if remaining <= 0 {
				break
			}
			if before.Slots[i] {
				before.Slots[i] = false
				if e.LastEventRetire {
					before.Alive[i] = false
				}
				remaining--
			}
		}
		if remaining != 0 {
			return errors.New("実返済を対応させる元枠がありません")
		}
		before.Direction = direction
		if quantity > 0 {
			before.Anchor = averageEntryPrice
		}
		before.Realized += (realizedYen - e.LastEventRealized) / e.Config.PointValue
		before.Barrier = e.Row
		before.Pending = nil
		before.Budget = nil
		if origin.ReasonCode == 30 && closed > 0 {
			before.Reductions++
		}
		e.Actual = before
	} else {
		return errors.New("実約定同期の種別が不明です")
	}
	e.RealizedYen = realizedYen
	return e.checkPositions()
}
