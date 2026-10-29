package strategy

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/e01"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

type orderState struct {
	Signal        Signal
	Filled        int
	Done          bool
	CloseConsumed map[string]int `json:",omitempty"`
	P05LotID      string         `json:",omitempty"`
}
type armState struct {
	Side, Stage                    int
	Level, Risk, At, ReturnExtreme float64
	ProgressIndex, ReturnIndex     int64
}
type paperMapping struct {
	Index, Row int64
	Time       float64
}
type legState struct {
	Position        Position
	Pending         string
	CloseTarget     int
	CloseReason     int
	ReadyAfter      float64
	Arm             armState
	PaperCampaign   int
	PaperRemaining  int
	PlannedQuantity int
}

// Portfolio は主担当・追加担当の実建玉と注文予約を独立管理します。
type Portfolio struct {
	Selected                    *SelectedPortfolio `json:",omitempty"`
	S01                         *S01Portfolio      `json:",omitempty"`
	s01Fresh                    bool
	Rules                       *rules.Runtime     `json:"-"`
	Research                    *ResearchPortfolio `json:",omitempty"`
	P05                         *P05Portfolio      `json:",omitempty"`
	Version                     int
	Config                      Config
	Session                     Session
	Paper                       *e01.Engine
	Core, Extra                 legState
	Orders                      map[string]*orderState
	Executions                  map[string]Fill
	Sequence                    int64
	LastIndex                   int64
	LastTime                    float64
	PaperInputOrigin            int64
	PaperRows                   map[int64]int64
	PaperRowQueue               []paperMapping
	CoreEntered, ExtraAttempted bool
}

// New は選定済み担当を生成します。
//
// 引数: cfg(Config)、session(Session)。返り値: *Portfolio と設定不整合の error です。
func New(cfg Config, session Session) (*Portfolio, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if session.ID == "" || (session.Kind != "Day" && session.Kind != "Night") || session.Start >= session.ForceClose || session.ForceClose > session.End {
		return nil, errors.New("セッション時刻が不正です")
	}
	p := &Portfolio{Version: 1, Config: cfg, Session: session, Orders: map[string]*orderState{}, Executions: map[string]Fill{}, LastIndex: -1, PaperInputOrigin: -1, PaperRows: map[int64]int64{}}
	p.Core = legState{Position: Position{Owner: "core"}, CloseTarget: -1, PaperCampaign: -1}
	p.Extra = legState{Position: Position{Owner: "extra"}, CloseTarget: -1, PaperCampaign: -1}
	if selectedMethod(cfg.Parameters.Method) {
		if err := p.bindRules(); err != nil {
			return nil, err
		}
		var err error
		p.Selected, err = p.newSelected()
		return p, err
	}
	if cfg.Parameters.Method == "P05" {
		if err := p.bindRules(); err != nil {
			return nil, err
		}
		var err error
		p.P05, err = p.newP05()
		return p, err
	}
	if cfg.Parameters.Method != "" {
		var err error
		base := cfg
		if cfg.Parameters.Method == "S01" {
			if cfg.Reference != "NK225" || session.Kind != "Day" {
				return nil, errors.New("S01は日経の日中専用です")
			}
			base = s01BaseConfig(cfg)
		}
		p.Research, err = newResearchPortfolio(base, session)
		if err == nil {
			err = p.bindRules()
		}
		if err == nil && cfg.Parameters.Method == "S01" {
			p.S01, err = p.newS01()
		}
		return p, err
	}
	if session.Kind == "Day" {
		d := cfg.Parameters.Day
		c := e01.DefaultConfig(cfg.Reference, cfg.CoreQuantity, cfg.Cap, cfg.LargeTick, cfg.PointValue)
		c.WarmupSeconds = d.WarmupSeconds
		c.CooldownSeconds = d.CooldownSeconds
		c.PendingSeconds = d.PendingSeconds
		c.EntryCutoffSeconds = d.EntryCutoffSeconds
		c.MaxHoldSeconds = d.MaxHoldSeconds
		c.EntryATR = d.EntryATR
		c.NormalRiskATR = d.NormalRiskATR
		c.TightRiskATR = d.TightRiskATR
		c.TrailArmATR = d.TrailArmATR
		c.NormalTrailATR = d.NormalTrailATR
		c.TightArmATR = d.TightArmATR
		c.TightTrailATR = d.TightTrailATR
		c.SplitATR = d.SplitATR
		var err error
		p.Paper, err = e01.NewEngine(c, e01.Session{ID: session.ID, Start: session.Start, ForceClose: session.ForceClose, Close: session.End})
		if err != nil {
			return nil, err
		}
	}
	if err := p.bindRules(); err != nil {
		return nil, err
	}
	return p, nil
}

// leg は担当文字列を内部状態へ対応します。
//
// 引数: owner(string)。返り値: *legState、未知なら nil です。
func (p *Portfolio) leg(owner string) *legState {
	if owner == "core" {
		return &p.Core
	}
	if owner == "extra" {
		return &p.Extra
	}
	return nil
}

// available は同方向の実玉と新規注文の未約定予約を控除します。
//
// 引数: side(int)。返り値: int は新規へ配分できる枚数です。
func (p *Portfolio) available(side int) int {
	n := p.Config.Cap
	for _, l := range []*legState{&p.Core, &p.Extra} {
		if l.Position.Side == side {
			n -= l.Position.Quantity
		}
	}
	for _, o := range p.Orders {
		if !o.Done && o.Signal.Action == "new" && o.Signal.Side == side {
			n -= o.Signal.Quantity - o.Filled
		}
	}
	if n < 0 {
		return 0
	}
	return n
}

// emit は実約定を仮定せず発注依頼だけを予約します。
//
// 引数: l(*legState)、o(Observation)、action(string)、side/qty/reason(int)、risk/boundary(float64)、campaign(int)。
// 返り値: *Signal は依頼、枠不足又は注文中なら nil です。
func (p *Portfolio) emit(l *legState, o Observation, action string, side, qty, reason int, risk, boundary float64, campaign int) *Signal {
	if l.Pending != "" || qty <= 0 {
		return nil
	}
	if action == "new" {
		qty = min(qty, p.available(side))
	} else {
		qty = min(qty, l.Position.Quantity)
	}
	if qty <= 0 {
		return nil
	}
	p.Sequence++
	evidence, _ := o.Evidence()
	id := fmt.Sprintf("%s-%s-%s-%06d", p.Session.ID, p.Config.Product, l.Position.Owner, p.Sequence)
	if p.Config.Namespace != "" {
		id = p.Config.Namespace + "-" + id
	}
	s := Signal{ID: id, Owner: l.Position.Owner, Action: action, Side: side, Quantity: qty, Time: o.Time, SourceIndex: o.Index, Reason: reasonText(p.Session.Kind, action, reason), ReasonCode: reason, Risk: risk, Boundary: boundary, Campaign: campaign, Evidence: evidence}
	p.Orders[s.ID] = &orderState{Signal: s}
	l.Pending = s.ID
	if action == "new" {
		l.PlannedQuantity = qty
	}
	return &s
}

// requestClose は返済条件を注文失敗や欠測をまたいで保持します。
//
// 引数: l(*legState)、target/reason(int)。返り値: なし。
func requestClose(l *legState, target, reason int) {
	if l.CloseTarget < 0 || target < l.CloseTarget {
		l.CloseTarget = target
		l.CloseReason = reason
	}
}

// flushClose は未発注の返済要求を現在観測で発注依頼へ変換します。
//
// 引数: l(*legState)、o(Observation)。返り値: *Signal、不要なら nil です。
func (p *Portfolio) flushClose(l *legState, o Observation) *Signal {
	if l.CloseTarget < 0 || l.Pending != "" || l.Position.Quantity <= l.CloseTarget {
		return nil
	}
	return p.emit(l, o, "close", -l.Position.Side, l.Position.Quantity-l.CloseTarget, l.CloseReason, l.Position.Risk, l.Position.Boundary, l.Position.Campaign)
}

// Observe は過去と現在の観測だけで新規・返済を判断します。
//
// 引数: o(Observation)、allowEntry(bool) は新規を外部で許可するか。
// 返り値: []Signal は発注依頼、error は時計や参照市場の不正です。
func (p *Portfolio) Observe(o Observation, allowEntry bool) ([]Signal, error) {
	return p.observe(o, allowEntry, true, true)
}

// ObserveWithFreshness は実受信の鮮度を追加の利益保護判断へ渡します。
//
// 引数: o(Observation)、allowEntry/fresh(bool)。返り値: []Signalとerrorです。
// S01の未送信利益返済だけは新鮮な行で確定します。元の損切り・場末返済は停止しません。
func (p *Portfolio) ObserveWithFreshness(o Observation, allowEntry, fresh bool) ([]Signal, error) {
	return p.observe(o, allowEntry, true, fresh)
}

// observe は判断更新と外部発注依頼の生成を分離します。
//
// 引数: o(Observation)、allowEntry(bool)、emitOrders(bool) は現在の発注依頼を生成するか。
// 返り値: []Signal は現在の依頼、error は観測の不整合です。
func (p *Portfolio) observe(o Observation, allowEntry, emitOrders, fresh bool) (signals []Signal, err error) {
	if p.S01 != nil && p.S01.Failure != "" {
		return nil, errors.New(p.S01.Failure)
	}
	if err := p.Rules.Err(); err != nil {
		return nil, err
	}
	defer func() {
		if p.S01 != nil && p.S01.Failure != "" {
			signals, err = nil, errors.New(p.S01.Failure)
		}
		if failure := p.Rules.Err(); failure != nil {
			signals, err = nil, failure
		}
	}()
	if o.Reference != p.Config.Reference || !finite(o.Time) || o.Time < p.LastTime || o.Index <= p.LastIndex {
		return nil, errors.New("観測の市場・時計・連番が不正です")
	}
	p.LastTime = o.Time
	p.LastIndex = o.Index
	p.s01Fresh = fresh && emitOrders
	if p.Selected != nil {
		return p.observeSelected(o, allowEntry, emitOrders, fresh)
	}
	if p.P05 != nil {
		return p.observeP05(o, allowEntry, emitOrders)
	}
	if p.Research != nil {
		if p.S01 != nil {
			if err := p.stepS01Shadow(o); err != nil {
				return nil, err
			}
		}
		return p.observeResearch(o, allowEntry, emitOrders)
	}
	out := []Signal{}
	terminal := o.Time >= p.Session.ForceClose
	if p.Session.Kind == "Day" {
		if err := p.day(o, allowEntry && !terminal, &out); err != nil {
			return nil, err
		}
	} else {
		p.night(o, allowEntry && !terminal, &out)
	}
	p.extra(o, allowEntry && !terminal, &out)
	if terminal {
		requestClose(&p.Core, 0, 93)
		requestClose(&p.Extra, 0, 93)
	}
	if emitOrders {
		for _, l := range []*legState{&p.Core, &p.Extra} {
			if s := p.flushClose(l, o); s != nil {
				out = append(out, *s)
			}
		}
	}
	return out, nil
}

// OnFill は確認済み約定のみを実玉へ反映します。
//
// 引数: f(Fill)。返り値: error は二重 ID の内容矛盾又は数量不整合です。
func (p *Portfolio) OnFill(f Fill) (err error) {
	defer func() {
		if failure := p.Rules.Err(); failure != nil {
			err = failure
		}
	}()
	if f.ExecutionID == "" || f.SignalID == "" || f.Quantity <= 0 || f.Price <= 0 || !finite(f.Price) || !finite(f.Fee) || f.Fee < 0 || !finite(f.Time) {
		return errors.New("約定情報が不正です")
	}
	if prior, ok := p.Executions[f.ExecutionID]; ok {
		if !reflect.DeepEqual(prior, f) {
			return errors.New("同じ約定 ID の内容が変わりました")
		}
		return nil
	}
	order, ok := p.Orders[f.SignalID]
	if !ok {
		return errors.New("約定に対応する発注依頼がありません")
	}
	if order.Filled+f.Quantity > order.Signal.Quantity {
		return errors.New("約定枚数が注文枚数を超えました")
	}
	s := order.Signal
	if p.Selected != nil {
		return p.fillSelected(f, order)
	}
	if p.P05 != nil {
		return p.fillP05(f, order)
	}
	if p.Research != nil {
		return p.fillResearch(f, order)
	}
	l := p.leg(s.Owner)
	pos := &l.Position
	if s.Action == "new" {
		if pos.Quantity > 0 && pos.Side != s.Side {
			return errors.New("同一担当で反対玉が混在しました")
		}
		if pos.Quantity == 0 {
			owner := pos.Owner
			*pos = Position{Owner: owner, Side: s.Side, Risk: s.Risk, Boundary: s.Boundary, Campaign: s.Campaign, EntryTime: f.Time, EntryIndex: s.SourceIndex, RiskFraction: 1, Floor: -s.Risk}
			if owner == "extra" {
				pos.Floor = -p.Config.Parameters.Extra.StopR * s.Risk
			} else if p.Session.Kind == "Night" {
				pos.Floor = -p.Config.Parameters.Night.StopR * s.Risk
			}
			// ----------------------------------------
			// 入口アンカーは発注判断ではなく、約定後最初の観測で採取します。
		}
		pos.Average = (pos.Average*float64(pos.Quantity) + f.Price*float64(f.Quantity)) / float64(pos.Quantity+f.Quantity)
		pos.Quantity += f.Quantity
		pos.InitialQuantity += f.Quantity
		if s.Owner == "core" {
			p.CoreEntered = true
		}
	} else {
		if pos.Side != -s.Side || f.Quantity > pos.Quantity {
			return errors.New("返済方向又は枚数が実玉と一致しません")
		}
		pos.Realized += float64(pos.Side) * (f.Price - pos.Average) * float64(f.Quantity) * p.Config.PointValue
		pos.Quantity -= f.Quantity
		if pos.Quantity == 0 {
			pos.Side = 0
			l.CloseTarget = -1
			l.Arm = armState{}
			l.ReadyAfter = f.Time + p.Config.Parameters.Night.CooldownSeconds
		}
	}
	pos.Fees += f.Fee
	order.Filled += f.Quantity
	p.Executions[f.ExecutionID] = f
	return nil
}

// OnOrderDone は取消済み残数量も含めて一件の注文終了を確定します。
//
// 引数: signalID(string)。返り値: error は未知 ID です。
func (p *Portfolio) OnOrderDone(signalID string) error {
	o, ok := p.Orders[signalID]
	if !ok {
		return errors.New("終了した注文 ID が不明です")
	}
	o.Done = true
	if p.Selected != nil {
		return p.doneSelected(o)
	}
	if p.S01 != nil {
		p.finishS01Initial(o)
	}
	if p.P05 != nil {
		return nil
	}
	if p.Research != nil {
		if o.Signal.Action == "new" && o.Filled == 0 {
			s := p.sleeve(o.Signal.Owner)
			if s != nil && s.Campaign == o.Signal.Campaign && s.held() == 0 {
				s.Accepted[s.Campaign] = false
			}
		}
		return nil
	}
	l := p.leg(o.Signal.Owner)
	if l.Pending == signalID {
		l.Pending = ""
	}
	if o.Signal.Action == "new" {
		l.PlannedQuantity = o.Filled
		// ----------------------------------------
		// 新規の未約定取消までに影が部分返済した場合、実約定枚数で残目標を引き直します。
		if o.Signal.Owner == "core" && p.Session.Kind == "Day" && l.PaperCampaign == o.Signal.Campaign && l.CloseTarget >= 0 {
			target := int(math.Floor(float64(o.Filled*l.PaperRemaining)/float64(p.Config.CoreQuantity) + .5))
			requestClose(l, target, l.CloseReason)
		}
	}
	if o.Signal.Action == "close" && l.CloseTarget >= 0 && l.Position.Quantity <= l.CloseTarget {
		l.CloseTarget = -1
	}
	return nil
}

// OnOrderRejected は新規を架空約定にせず、返済要求を保持したまま再試行可能にします。
//
// 引数: signalID(string)。返り値: error は未知 ID です。
func (p *Portfolio) OnOrderRejected(signalID string) error { return p.OnOrderDone(signalID) }

// Advance は再起動時の欠測期間を、架空約定せずに進めます。
//
// 引数: o(Observation) は未処理だった過去行です。
// 返り値: error は観測の不整合です。返済要求は残し、過去発注依頼は外部へ出しません。
func (p *Portfolio) Advance(o Observation) error {
	_, err := p.observe(o, false, false, false)
	return err
}

// Flatten は価格観測が途切れても実玉の返済依頼を保持します。
//
// 引数: at(float64) は現在時計、reason(string) は運用上の理由。
// 返り値: []Signal は返済依頼です。既に注文中なら終了通知後に再判断します。
func (p *Portfolio) Flatten(at float64, reason string) []Signal {
	if p.Selected != nil {
		return p.flattenSelected("", at, reason)
	}
	if p.P05 != nil {
		return p.flattenP05("", at, reason)
	}
	if p.Research != nil {
		return p.flattenResearch("", at, reason)
	}
	o := Observation{Reference: p.Config.Reference, Time: at, Index: p.LastIndex}
	out := []Signal{}
	for _, l := range []*legState{&p.Core, &p.Extra} {
		requestClose(l, 0, 93)
		if s := p.flushClose(l, o); s != nil {
			s.Reason = reason
			p.Orders[s.ID].Signal = *s
			out = append(out, *s)
		}
	}
	return out
}

// FlattenOwner は指定した担当だけを価格観測なしでも返済待ちにします。
//
// 引数: owner(string) は core/extra、at(float64)、reason(string)。
// 返り値: []Signal は現在発注できる返済依頼です。
func (p *Portfolio) FlattenOwner(owner string, at float64, reason string) []Signal {
	if p.Selected != nil {
		return p.flattenSelected(owner, at, reason)
	}
	if p.P05 != nil {
		return p.flattenP05(owner, at, reason)
	}
	if p.Research != nil {
		return p.flattenResearch(owner, at, reason)
	}
	l := p.leg(owner)
	if l == nil {
		return nil
	}
	requestClose(l, 0, 93)
	o := Observation{Reference: p.Config.Reference, Time: at, Index: p.LastIndex}
	if s := p.flushClose(l, o); s != nil {
		s.Reason = reason
		p.Orders[s.ID].Signal = *s
		return []Signal{*s}
	}
	return nil
}

// Status は実保有と注文予約を返します。
//
// 引数: なし。返り値: Status は表示・照合用状態です。
func (p *Portfolio) Status() Status {
	if p.Selected != nil {
		return p.statusSelected()
	}
	if p.P05 != nil {
		return p.statusP05()
	}
	if p.Research != nil {
		return p.researchStatus()
	}
	s := Status{Positions: []Position{p.Core.Position, p.Extra.Position}, CoreEntered: p.CoreEntered, ExtraAttempted: p.ExtraAttempted}
	for _, id := range []string{p.Core.Pending, p.Extra.Pending} {
		if id != "" {
			s.Pending = append(s.Pending, p.Orders[id].Signal)
		}
	}
	return s
}

// Dump は実玉・仮想影・準備・未約定依頼をまとめて保存します。
//
// 引数: なし。返り値: []byte は JSON、error は保存失敗です。
func (p *Portfolio) Dump() ([]byte, error) { return json.Marshal(p) }

// Restore は保存状態から同じ注文 ID と実玉を復元します。
//
// 引数: data([]byte)。返り値: *Portfolio と整合検査の error です。
func Restore(data []byte) (*Portfolio, error) {
	var p Portfolio
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if p.Version != 1 || p.Orders == nil || p.Executions == nil {
		return nil, errors.New("戦略状態の版又は注文台帳が不正です")
	}
	if p.PaperRows == nil {
		p.PaperRows = map[int64]int64{}
	}
	if err := p.Config.Validate(); err != nil {
		return nil, err
	}
	if selectedMethod(p.Config.Parameters.Method) {
		if p.Selected == nil || p.P05 != nil || p.Research != nil || p.S01 != nil {
			return nil, errors.New("選定手法の保存本体が不一致です")
		}
		if err := p.bindRules(); err != nil {
			return nil, err
		}
		if err := p.restoreSelected(); err != nil {
			return nil, err
		}
		return &p, nil
	}
	if p.Selected != nil {
		return nil, errors.New("対象外手法に選定手法状態があります")
	}
	if p.Config.Parameters.Method == "P05" {
		if p.P05 == nil || p.Research != nil {
			return nil, errors.New("P05の保存本体が一致しません")
		}
		if err := p.bindRules(); err != nil {
			return nil, err
		}
		if err := p.restoreP05(); err != nil {
			return nil, err
		}
		return &p, nil
	}
	if p.P05 != nil {
		return nil, errors.New("対象外手法にP05状態があります")
	}
	if (p.Config.Parameters.Method == "S01") != (p.S01 != nil) {
		return nil, errors.New("S01の手法と保存状態が一致しません")
	}
	if (p.Config.Parameters.Method != "") != (p.Research != nil) {
		return nil, errors.New("研究手法IDと保存された戦略本体が一致しません")
	}
	if p.Research != nil {
		if err := p.restoreResearch(); err != nil {
			return nil, err
		}
		if err := p.bindRules(); err != nil {
			return nil, err
		}
		if p.S01 != nil {
			if err := p.restoreS01(); err != nil {
				return nil, err
			}
		}
		return &p, nil
	}
	if p.Session.Kind == "Day" {
		if p.Paper == nil {
			return nil, errors.New("日中の仮想影がありません")
		}
		raw, err := json.Marshal(p.Paper)
		if err != nil {
			return nil, err
		}
		p.Paper, err = e01.RestoreEngine(raw)
		if err != nil {
			return nil, err
		}
	}
	if err := p.bindRules(); err != nil {
		return nil, err
	}
	return &p, nil
}

// reasonText は昼夜と新規・返済を区別して研究理由コードを短い日本語へ変換します。
//
// 引数: kind(string) は Day/Night、action(string) は new/close、reason(int) は研究理由コードです。
// 返り値: string は注文理由です。日中返済の 1 と夜間新規の 1 は別の理由です。
func reasonText(kind, action string, reason int) string {
	if action == "new" {
		if reason == 82 || reason == 83 {
			return "未参加時の追加"
		}
		return "主担当の新規"
	}
	if kind == "Day" {
		switch reason {
		case 1:
			return "組の損切り"
		case 2:
			return "追跡返済"
		case 7:
			return "順行幅到達による部分利確"
		case 8:
			return "保有時間上限"
		case 9:
			return "セッション内返済"
		case 30:
			return "順行後の悪化で半減"
		}
	}
	switch reason {
	case 32:
		return "損失幅へ到達"
	case 70:
		return "利益保護の戻り"
	case 72:
		return "境界破綻と相手逆行"
	case 75:
		return "二市場の逆行が継続"
	case 93:
		return "セッション内返済"
	case 94:
		return "米国現物開始の静観"
	}
	return "元の返済条件"
}

// sign は価格単位に依存しない方向票を返します。
//
// 引数: v(float64)。返り値: float64 は -1/0/1 です。
func sign(v float64) float64 {
	if v > 0 {
		return 1
	}
	if v < 0 {
		return -1
	}
	return 0
}

// quoteValid は発注判断に使える正常気配を検査します。
//
// 引数: o(Observation)。返り値: bool は正の非交差気配です。
func quoteValid(o Observation) bool {
	return o.QuoteOK && finite(o.Bid) && finite(o.Ask) && o.Bid > 0 && o.Ask > o.Bid
}

// liquidation は保有を決済できる側の気配を返します。
//
// 引数: o(Observation)、side(int)。返り値: float64 は買い玉なら Bid、売り玉なら Ask です。
func liquidation(o Observation, side int) float64 {
	if side > 0 {
		return o.Bid
	}
	return o.Ask
}

// trail は実玉平均価格からの順行と保護床を更新します。
//
// 引数: pos(*Position)、o(Observation)、arm/width/stop(float64) は R 倍率。
// 返り値: int は返済理由、未成立なら 0 です。
func trail(pos *Position, o Observation, arm, width, stop float64) int {
	if pos.Quantity == 0 || !quoteValid(o) {
		return 0
	}
	gain := float64(pos.Side) * (liquidation(o, pos.Side) - pos.Average)
	pos.Peak = math.Max(pos.Peak, gain)
	pos.Floor = math.Max(pos.Floor, -stop*pos.Risk)
	if pos.Peak >= arm*pos.Risk {
		pos.Floor = math.Max(pos.Floor, pos.Peak-width*pos.Risk)
	}
	if gain <= pos.Floor {
		if pos.Floor < 0 {
			return 32
		}
		return 70
	}
	return 0
}
