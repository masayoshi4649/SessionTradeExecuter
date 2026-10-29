package strategy

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
	"math"
)

// ResearchSource は実注文から独立した研究担当の仮想状態です。
// 全状態を公開フィールドで保存し、再起動で準備・発注予約を復元します。
type ResearchSource struct {
	Rules                *rules.Runtime `json:"-"`
	Name                 string
	Capacity             int
	Config               Config
	Session              Session
	Legacy               *Portfolio
	Supplement           *Portfolio
	CoreAccepted         map[int]bool
	CoreActive           bool
	CoreEverEntered      bool
	CoreRemaining        map[int]int
	CoreOutputHeld       map[int]int
	Active145            *ResearchActive145
	Active               *ResearchActive
	Episode              *ResearchEpisode
	PendingLegacy        []Signal
	LastIndex            int64
	LastTime             float64
	FallbackFailureSince float64
	FallbackFailureKnown bool
}

// ResearchBook は注文別の仮想建玉と未約定通知を保持します。
type ResearchBook struct {
	FeeFree                                  bool
	Held                                     [6]int
	Entries                                  [6]float64
	Side, Parent, UID                        int
	Risk, Base, Floor, Best, Cash, PriorCash float64
	Pending                                  []ResearchEvent
	PendingIndex                             int64
	PendingKind                              int
}

// newResearchSource は当時の公開情報だけを消費する担当を生成します。
//
// 引数: cfg(Config)、session(Session)、name(string)、capacity(int) は担当上限です。
// 返り値: *ResearchSource と設定不整合の error です。
func newResearchSource(cfg Config, session Session, name string, capacity int) (*ResearchSource, error) {
	if cfg.Reference != "NK225" || capacity < 1 {
		return nil, errors.New("研究担当は日経、上限1枚以上で指定してください")
	}
	s := &ResearchSource{Name: name, Capacity: capacity, Config: cfg, Session: session, LastIndex: -1}
	switch name {
	case "core":
		c := cfg
		// 主担当の基礎候補は標準の旧手法を使い、親の別profileの式は流用しません。
		c.RuleExpressions = nil
		c.Parameters = DefaultParameters("NK225", session.Kind)
		c.CoreQuantity, c.ExtraMaxQuantity, c.Cap = 2*capacity, 2*capacity, 2*capacity
		c.PointValue = 100
		var err error
		s.Legacy, err = New(c, session)
		if err != nil {
			return nil, err
		}
		c.CoreQuantity, c.ExtraMaxQuantity, c.Cap = capacity, capacity, capacity
		s.Supplement, err = New(c, session)
		if err != nil {
			return nil, err
		}
		s.CoreAccepted = map[int]bool{}
		s.CoreRemaining = map[int]int{}
		s.CoreOutputHeld = map[int]int{}
	case "addon":
		if cfg.Parameters.Method == "D07" {
			s.Active145 = newResearchActive145(session, capacity)
			break
		}
		s.Active = &ResearchActive{Book: ResearchBook{Parent: -1}, Initial: min(2, capacity), Maximum: capacity, Guard: true, Management: session.Kind == "Night", NextReady: session.Start}
	case "episode":
		if session.Kind == "Day" {
			s.Episode = &ResearchEpisode{Book: ResearchBook{Parent: -1}, Initial: min(2, capacity), Maximum: capacity}
		} else {
			initial := 2
			if capacity <= 2 {
				initial = 1
			}
			s.Active = &ResearchActive{Book: ResearchBook{Parent: -1}, Initial: min(initial, capacity), Maximum: capacity, NextReady: session.Start}
		}
	default:
		return nil, errors.New("未知の研究担当: " + name)
	}
	if cfg.Parameters.Method == "D07" {
		if s.Active != nil {
			s.Active.Book.FeeFree = true
		}
		if s.Episode != nil {
			s.Episode.Book.FeeFree = true
		}
	}
	if err := s.bindRules(); err != nil {
		return nil, err
	}
	return s, nil
}

// Restore はJSON復元後に旧E01内の時系列状態も復元します。
//
// 引数: なし。返り値: error は保存状態の不整合です。
func (s *ResearchSource) Restore() error {
	if s.Capacity < 1 {
		return errors.New("研究担当の上限が不正です")
	}
	if s.Legacy != nil {
		raw, err := json.Marshal(s.Legacy)
		if err != nil {
			return err
		}
		s.Legacy, err = Restore(raw)
		if err != nil {
			return err
		}
		if s.Supplement == nil || s.CoreAccepted == nil || s.CoreRemaining == nil || s.CoreOutputHeld == nil {
			return errors.New("主担当と補完の独立状態がありません")
		}
		raw, err = json.Marshal(s.Supplement)
		if err != nil {
			return err
		}
		s.Supplement, err = Restore(raw)
		if err != nil {
			return err
		}
		return s.bindRules()
	}
	if s.Active == nil && s.Active145 == nil && s.Episode == nil {
		return errors.New("研究担当の保存状態がありません")
	}
	return s.bindRules()
}

// Step は前行までの予約を次正常気配で約定させ、現在行の新規通知を返します。
//
// 引数: o(Observation) は原行順に到着した因果的観測です。
// 返り値: []ResearchEvent は現在行で判断した通知、error は時系列不整合です。
func (s *ResearchSource) Step(o Observation) (events []ResearchEvent, err error) {
	if err := s.ruleError(); err != nil {
		return nil, err
	}
	defer func() {
		if failure := s.ruleError(); failure != nil {
			events, err = nil, failure
		}
	}()
	if o.Index <= s.LastIndex || (s.LastIndex >= 0 && o.Time < s.LastTime) || !finite(o.Time) {
		return nil, errors.New("研究源の時計又は原行順が不正です")
	}
	gap := s.LastIndex >= 0 && o.Time-s.LastTime > 15
	s.LastIndex, s.LastTime = o.Index, o.Time
	if s.Legacy != nil {
		return s.stepLegacy(o)
	}
	if s.Active145 != nil {
		return s.Active145.step(o, s.Session, s.Config.LargeTick, gap), nil
	}
	if s.Active != nil {
		return s.Active.step(o, s.Session, s.Config.LargeTick, gap), nil
	}
	return s.Episode.step(o, s.Session, s.Config.LargeTick, gap), nil
}

// ruleError は親と独立した仮想主担当・補完担当の評価異常も確認します。
//
// 引数: なし。返り値: errorは保持中の最初の異常、全担当が正常ならnilです。
// 直接呼ぶnight・extraでも異常後の通知を外へ出さず、次の観測でも停止を維持します。
func (s *ResearchSource) ruleError() error {
	if err := s.Rules.Err(); err != nil {
		return err
	}
	if s.Legacy != nil {
		if err := s.Legacy.Rules.Err(); err != nil {
			return fmt.Errorf("仮想主担当のルール評価異常: %w", err)
		}
	}
	if s.Supplement != nil {
		if err := s.Supplement.Rules.Err(); err != nil {
			return fmt.Errorf("仮想補完担当のルール評価異常: %w", err)
		}
	}
	return nil
}

// total は担当の仮想保有枚数を合計します。
//
// 引数: なし。返り値: int は保有枚数です。
func (b *ResearchBook) total() int {
	n := 0
	for _, q := range b.Held {
		n += q
	}
	return n
}

// filled は現在気配で約定可能な予約を実行します。
//
// 引数: o(Observation)。返り値: int は約定した予約種類、未約定なら0です。
func (b *ResearchBook) filled(o Observation) int {
	if len(b.Pending) == 0 || o.Index <= b.PendingIndex || !quoteValid(o) {
		return 0
	}
	kind := b.PendingKind
	for _, e := range b.Pending {
		px := liquidation(o, -e.Side)
		fee := math.Floor(float64(e.Quantity) * 38.5)
		if b.FeeFree {
			fee = 0
		}
		if e.Action == "new" {
			if kind == 1 {
				b.Parent = e.Campaign
				b.Side = e.Side
				b.UID = 0
				b.Base = px
				b.Risk = e.Risk
				b.Floor = -e.Risk
				b.Best = 0
				b.PriorCash = b.Cash
			}
			b.UID = e.Ticket
			b.Held[e.Ticket] += e.Quantity
			b.Entries[e.Ticket] = px
			b.Cash -= float64(e.Side*e.Quantity)*px*100 + fee
		} else {
			b.Held[e.Ticket] -= e.Quantity
			b.Cash -= float64(e.Side*e.Quantity)*px*100 + fee
		}
	}
	b.Pending = nil
	b.PendingKind = 0
	return kind
}

// queueNew は新規又は追撃を次原行の約定待ちへ登録します。
//
// 引数: o(Observation)、kind/side/qty/reason(int)、risk/boundary(float64)。
// 返り値: []ResearchEvent は現在発行した一通知です。
func (b *ResearchBook) queueNew(o Observation, kind, side, qty, reason int, risk, boundary float64) []ResearchEvent {
	parent, uid := b.Parent, b.UID+1
	if kind == 1 {
		parent++
		uid = 0
	}
	b.Pending = []ResearchEvent{{Action: "new", Side: side, Quantity: qty, Reason: reason, Campaign: parent, Ticket: uid, Risk: risk, Boundary: boundary}}
	b.PendingIndex, b.PendingKind = o.Index, kind
	return append([]ResearchEvent(nil), b.Pending...)
}

// queueClose は注文別返済順を保持して次原行の約定を予約します。
//
// 引数: o(Observation)、kind/qty/reason(int)、descending(bool) は新しい玉から返す指定です。
// 返り値: []ResearchEvent は現在発行した返済通知です。
func (b *ResearchBook) queueClose(o Observation, kind, qty, reason int, descending bool) []ResearchEvent {
	out := []ResearchEvent{}
	for k := 0; k < 6 && qty > 0; k++ {
		u := k
		if descending {
			u = 5 - k
		}
		q := min(qty, b.Held[u])
		if q <= 0 {
			continue
		}
		out = append(out, ResearchEvent{Action: "close", Side: -b.Side, Quantity: q, Reason: reason, Campaign: b.Parent, Ticket: u, Risk: b.Risk, Boundary: b.Base + float64(b.Side)*b.Floor})
		qty -= q
	}
	b.Pending = out
	b.PendingIndex, b.PendingKind = o.Index, kind
	return append([]ResearchEvent(nil), out...)
}

// featureKnown は要求列の公開・有限性を確認します。
//
// 引数: f(ResearchFeatures)、cols(...int)。返り値: bool は全列既知です。
func featureKnown(f ResearchFeatures, cols ...int) bool {
	for _, c := range cols {
		if !f.Known[c] || !finite(f.Values[c]) {
			return false
		}
	}
	return true
}

// stepLegacy は旧主担当の独立仮想口座とZ04補完を更新します。
//
// 引数: o(Observation)。返り値: []ResearchEvent は現在の判断、error は仮想会計の不整合です。
func (s *ResearchSource) stepLegacy(o Observation) ([]ResearchEvent, error) {
	p := s.Legacy
	if quoteValid(o) {
		pending := s.PendingLegacy
		s.PendingLegacy = nil
		for _, v := range pending {
			if o.Index <= v.SourceIndex {
				s.PendingLegacy = append(s.PendingLegacy, v)
				continue
			}
			account := p
			if v.Owner == "extra" {
				account = s.Supplement
			}
			fee := math.Floor(float64(v.Quantity) * 38.5)
			if s.Config.Parameters.Method == "D07" {
				fee = 0
			}
			if err := account.OnFill(Fill{SignalID: v.ID, ExecutionID: v.ID + "-仮想", Quantity: v.Quantity, Price: liquidation(o, -v.Side), Time: o.Time, Fee: fee}); err != nil {
				return nil, err
			}
			if err := account.OnOrderDone(v.ID); err != nil {
				return nil, err
			}
			if v.Owner == "core" && s.CoreAccepted[v.Campaign] {
				if v.Action == "new" {
					s.CoreEverEntered = true
				} else if p.Core.Position.Quantity == 0 {
					s.CoreActive = false
				}
			}
		}
	}
	out := []Signal{}
	terminal := o.Time >= s.Session.ForceClose
	if s.Session.Kind == "Day" {
		if err := p.day(o, !terminal, &out); err != nil {
			return nil, err
		}
	} else {
		current := o
		// 既存Goの全15列Knownは入口用。研究の保有中境界退出は相手20分差だけを必要とします。
		if p.Core.Position.Quantity > 0 {
			current.MidHistory.Known = o.Research.Known[29] && finite(o.Research.Values[29])
			current.MidHistory.Values[11] = o.Research.Values[29]
		}
		p.night(current, !terminal, &out)
	}
	// ----------------------------------------
	// 影の親は不採用でも最後まで進める。次の候補を早出しするため途中で消しません。
	for _, v := range out {
		if v.Action == "new" {
			allowed := !s.CoreActive && s.Supplement.Extra.Position.Quantity == 0 && s.Supplement.Extra.Pending == ""
			s.CoreAccepted[v.Campaign] = allowed
			if allowed {
				s.CoreActive = true
			}
		}
	}
	s.Supplement.CoreEntered = s.CoreEverEntered
	if s.Session.Kind == "Day" {
		before := len(out)
		s.Supplement.extra(o, !terminal && !s.CoreActive, &out)
		for i := before; i < len(out); i++ {
			if out[i].Action == "new" {
				out[i].Campaign = 10000
				s.Supplement.Orders[out[i].ID].Signal.Campaign = 10000
			}
		}
	} else {
		s.z04(o, !terminal && !s.CoreActive, &out)
	}
	if terminal {
		requestClose(&p.Core, 0, 93)
		requestClose(&s.Supplement.Extra, 0, 93)
	}
	if v := p.flushClose(&p.Core, o); v != nil {
		out = append(out, *v)
	}
	if v := s.Supplement.flushClose(&s.Supplement.Extra, o); v != nil {
		out = append(out, *v)
	}
	events := make([]ResearchEvent, 0, len(out))
	for _, v := range out {
		s.PendingLegacy = append(s.PendingLegacy, v)
		if v.Owner == "core" && !s.CoreAccepted[v.Campaign] {
			continue
		}
		quantity := v.Quantity
		if v.Owner == "core" {
			if v.Action == "new" {
				s.CoreRemaining[v.Campaign] = v.Quantity
				s.CoreOutputHeld[v.Campaign] = s.Capacity
				quantity = s.Capacity
			} else {
				s.CoreRemaining[v.Campaign] -= v.Quantity
				left := int(math.Floor(float64(s.CoreRemaining[v.Campaign])*.5 + .5))
				quantity = s.CoreOutputHeld[v.Campaign] - left
				s.CoreOutputHeld[v.Campaign] = left
			}
		}
		if quantity <= 0 {
			continue
		}
		reason := v.ReasonCode
		if s.Session.Kind == "Day" && v.Owner == "core" && v.Action == "new" {
			reason = 90
		}
		ticket := 0
		if v.Owner == "extra" {
			ticket = 999
		}
		events = append(events, ResearchEvent{Action: v.Action, Side: v.Side, Quantity: quantity, Reason: reason, Campaign: v.Campaign, Ticket: ticket, Risk: v.Risk, Boundary: v.Boundary})
	}
	return events, nil
}

// z04 は始値方向を基本とし、明確な二市場反証時のみ補完方向を置換します。
//
// 引数: o(Observation)、allow(bool)、out(*[]Signal)。返り値: なし。
func (s *ResearchSource) z04(o Observation, allow bool, out *[]Signal) {
	p := s.Supplement
	l := &p.Extra
	m := o.MidMarket
	h := o.MidHistory
	good := quoteValid(o)
	if l.Position.Quantity > 0 {
		pos := &l.Position
		if reason := trail(pos, o, .75, .5, 1); reason != 0 {
			requestClose(l, 0, reason)
		}
		fresh := m.Known && m.Own30Known && m.Peer30Known && m.Own120Known && m.Peer120Known && h.Known
		d := float64(pos.Side)
		adverse := good && fresh && d*m.Own30 < 0 && d*m.Peer30 < 0 && d*m.Own120 < 0 && d*m.Peer120 < 0 && d*h.Values[7] < 0 && d*h.Values[11] < 0
		if !good || !fresh || (pos.LastValidTime > 0 && o.Time-pos.LastValidTime > 15) {
			s.FallbackFailureKnown = false
		}
		if adverse {
			if !s.FallbackFailureKnown {
				s.FallbackFailureSince = o.Time
				s.FallbackFailureKnown = true
			}
			if o.Time-s.FallbackFailureSince >= 15 {
				requestClose(l, 0, 1434)
			}
		} else {
			s.FallbackFailureKnown = false
		}
		if good {
			pos.LastValidTime = o.Time
		}
		return
	}
	activation := s.Session.ExtraAt
	if activation <= 0 {
		activation = s.Session.USOpen + 1800
	}
	if !allow || p.CoreEntered || p.ExtraAttempted || l.Pending != "" || !good || !h.Known || !finite(o.ATR) || o.ATR <= 0 || o.Time < activation || o.Time >= s.Session.ForceClose-600 {
		return
	}
	r := math.Max(o.ATR, math.Max(.5*(h.Values[3]-h.Values[4]), 8*s.Config.LargeTick))
	if o.Ask-o.Bid > .5*r {
		return
	}
	side := extraDirection(h, (o.Ask+o.Bid)*.5, 1)
	if side == 0 {
		return
	}
	d := -float64(side)
	if m.Known && m.Own120Known && m.Peer120Known && d*h.Values[7] > 0 && d*h.Values[11] > 0 && d*h.Values[6] > 0 && d*h.Values[10] > 0 && d*m.Own120 > 0 && d*m.Peer120 > 0 {
		side = -side
	}
	// ----------------------------------------
	// 旧micro10枚の縮小を先に行い、研究mini5枚へ切捨て配分した式を維持します。
	native := max(1, min(2*s.Capacity, int(float64(2*s.Capacity)*12*s.Config.LargeTick/r)))
	qty := max(1, native/2)
	if v := p.emit(l, o, "new", side, qty, 1424, r, 0, 4000000); v != nil {
		p.ExtraAttempted = true
		l.CloseTarget = -1
		*out = append(*out, *v)
	}
}

// String は診断時に担当名と枚数枠を返します。
//
// 引数: なし。返り値: string は短い担当表示です。
func (s *ResearchSource) String() string { return fmt.Sprintf("%s/%d枚", s.Name, s.Capacity) }
