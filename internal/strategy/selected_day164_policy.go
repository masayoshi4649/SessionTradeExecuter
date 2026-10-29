package strategy

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// SelectedDay164Policy はD164_15の正規化会計と実所有IDを分離して保持します。
// 元S01の通知だけが源数量を更新し、実損益を元手法へ戻しません。
type SelectedDay164Policy struct {
	Cap                         int
	Parents                     map[string]*selectedDay164Parent
	Canonical                   []*selectedDay164Canonical
	Pending                     map[string]*selectedDay164Pending
	Protection                  map[string]*selectedDay164Protection
	ActualEntries               map[string]string
	CloseGroups                 map[string]*selectedDay164CloseGroup
	SeenSources, SeenExecutions map[string]bool
	Retry                       []selectedDay164Retry
	DeferredIntents             []SelectedIntent
	Sequence                    int64
	Stopped                     bool
}

type selectedDay164Parent struct {
	Key, Owner                                                                 string
	Side, Campaign, NativeInitial, SourceQuantity, SourcePeak, OriginalDesired int
	SourceHolds                                                                map[string]int
	Factor, Started, DeferredAt                                                float64
	Deferred, Cancelled, Ended, InitialSeen                                    bool
	Rejected                                                                   string
}

type selectedDay164Canonical struct {
	ID, Parent, SourceID, Kind          string
	Side                                int
	Quantity, PendingNew, ReservedClose float64
}

type selectedDay164Allocation struct {
	ID       string
	Quantity float64
}

type selectedDay164Pending struct {
	Intent                               SelectedIntent
	CanonicalID                          string
	Allocations                          []selectedDay164Allocation
	ActualRequested, ActualFilled        int
	Gross                                float64
	Qualified                            bool
	QualificationID                      string
	CancelRequested, CloseLateFill, Done bool
}

type selectedDay164Protection struct {
	Eligible, Active     bool
	EligibleAt, ActiveAt float64
}

type selectedDay164Retry struct {
	Parent, ActualID, Reason string
	Allocations              []selectedDay164Allocation
	Origin                   string
	QualificationID          string
}

type selectedDay164CloseGroup struct {
	Parent           string
	Expected, Filled int
	Gross            float64
	Qualified        bool
}

type selectedDay164Request struct {
	Parent         *selectedDay164Parent
	SourceID, Kind string
	Quantity       int
	Initial        bool
}

// newSelectedDay164Policy は商品枚数上限を正規化100へ対応させます。
//
// 引数: cap(int)は片方向の実商品上限。返り値: *SelectedDay164Policy。
// 研究の100microを実capへ切捨て変換し、逆換算はfloat64で保有を失いません。
func newSelectedDay164Policy(cap int) *SelectedDay164Policy {
	return &SelectedDay164Policy{Cap: cap, Parents: map[string]*selectedDay164Parent{}, Pending: map[string]*selectedDay164Pending{}, Protection: map[string]*selectedDay164Protection{}, ActualEntries: map[string]string{}, CloseGroups: map[string]*selectedDay164CloseGroup{}, SeenSources: map[string]bool{}, SeenExecutions: map[string]bool{}}
}

// selectedDay164Desired はJ02の支持量を校正確率とせず希望数量へ写します。
//
// 引数: s(SelectedDay164Support)。返り値: intは100基準の10～100です。
func selectedDay164Desired(s SelectedDay164Support) int {
	v := 0.
	if s.Known && finite(s.M) && finite(s.T) {
		v = math.Max(0, math.Min(1, s.M*s.T))
	}
	return int(math.Floor(10 + 90*v + 1e-10))
}

// supportSelectedDay は方向に対応する原行時点の支持を返します。
//
// 引数: o(Observation)、side(int)。返り値: SelectedDay164Support。
func supportSelectedDay(o Observation, side int) SelectedDay164Support {
	if side < 0 {
		return o.Selected.DaySell
	}
	return o.Selected.DayBuy
}

// shortSelectedDay は短期窓の未知をゼロの速度と混同せず返します。
//
// 引数: o(Observation)。返り値: SelectedDay164Short。
func shortSelectedDay(o Observation) SelectedDay164Short {
	return o.Selected.DayShort
}

// conditionSelectedDay は専用Exprへ必要な型付き変数だけを渡します。
//
// 引数: r(selectedNightRules)、name(string)、s(SelectedDay164Short)、side(int)。返り値: bool。
func conditionSelectedDay(r selectedNightRules, name string, s SelectedDay164Short, side int) bool {
	env := map[string]any{"short_known": s.Five.Known, "long_known": s.Fifteen.Known, "side": float64(side)}
	if name == "confirm_add" {
		env["move5"] = s.Five.Move
		env["move15"] = s.Fifteen.Move
	} else {
		env["speed5"] = s.Five.Speed
		env["speed15"] = s.Fifteen.Speed
	}
	return r.Bool("d164_15."+name, env)
}

// microSelectedDay は実枚数を100基準へ逆換算します。
//
// 引数: quantity(int)。返り値: float64。非整除上限でも実1枚をゼロにしません。
func (d *SelectedDay164Policy) microSelectedDay(quantity int) float64 {
	return float64(quantity) * 100 / float64(d.Cap)
}

// actualSelectedDay は研究数量を実商品枚数へ切り捨てます。
//
// 引数: micro(float64)。返り値: int。最低1枚への切上げは行いません。
func (d *SelectedDay164Policy) actualSelectedDay(micro float64) int {
	return max(0, int(math.Floor(micro*float64(d.Cap)/100+1e-9)))
}

// nextSelectedDay は起動内で衝突しない政策予約IDを発行します。
//
// 引数: なし。返り値: string。
func (d *SelectedDay164Policy) nextSelectedDay() string {
	d.Sequence++
	return fmt.Sprintf("D16415-%09d", d.Sequence)
}

// canonicalSelectedDay は正規化玉を識別子で検索します。
//
// 引数: id(string)。返り値: *selectedDay164Canonical。未存在時nil。
func (d *SelectedDay164Policy) canonicalSelectedDay(id string) *selectedDay164Canonical {
	for _, c := range d.Canonical {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// pendingSelectedDay は同親の未完了注文を判定します。
//
// 引数: parent(string)、s(SelectedSnapshot)。返り値: bool。
func (d *SelectedDay164Policy) pendingSelectedDay(parent string, s SelectedSnapshot) bool {
	if s.PendingParents[parent] || s.ClosingParents[parent] {
		return true
	}
	for _, p := range d.Pending {
		if !p.Done && p.Intent.Parent == parent {
			return true
		}
	}
	return false
}

// reservedSelectedDay は共通側へ未反映の同呼出し内予約も含めます。
//
// 引数: id(string)、s(SelectedSnapshot)。返り値: int。
func (d *SelectedDay164Policy) reservedSelectedDay(id string, s SelectedSnapshot) int {
	n := 0
	for _, p := range d.Pending {
		if !p.Done && p.Intent.ActualID == id {
			n += p.ActualRequested - p.ActualFilled
		}
	}
	return max(n, s.ReservedClose[id])
}

// lotOrderSelectedDay は実約定の親FIFO順を安定化します。
//
// 引数: s(SelectedSnapshot)、parent(string)。返り値: []SelectedLot。元スライスを変更しません。
func lotOrderSelectedDay(s SelectedSnapshot, parent string) []SelectedLot {
	lots := []SelectedLot{}
	for _, l := range s.Lots {
		if l.Parent == parent && l.Quantity > 0 {
			lots = append(lots, l)
		}
	}
	// 共通帳簿の原行・部分約定追加順が同時計のFIFO順です。
	sort.SliceStable(lots, func(i, j int) bool { return lots[i].At < lots[j].At })
	return lots
}

// openSelectedDay は実商品丸め後の量だけをCanonicalへ予約します。
//
// 引数: p(*selectedDay164Parent)、source/kind(string)、want(int)。返り値: SelectedIntent、採用正規化量(float64)。
func (d *SelectedDay164Policy) openSelectedDay(p *selectedDay164Parent, source, kind string, want int) (SelectedIntent, float64) {
	actual := d.actualSelectedDay(float64(want))
	if actual <= 0 {
		return SelectedIntent{}, 0
	}
	id := d.nextSelectedDay()
	quantity := d.microSelectedDay(actual)
	in := SelectedIntent{PolicyID: id, Parent: p.Key, SourceID: source, Owner: p.Owner, Campaign: p.Campaign, Side: p.Side, Kind: kind, Reason: "D164_15_" + kind, QuantityMicro: want, Quantity: actual}
	c := &selectedDay164Canonical{ID: id, Parent: p.Key, SourceID: source, Kind: kind, Side: p.Side, PendingNew: quantity}
	d.Canonical = append(d.Canonical, c)
	d.Pending[id] = &selectedDay164Pending{Intent: in, CanonicalID: id, ActualRequested: actual}
	return in, quantity
}

// closeSelectedDay はCanonical予約を実玉FIFO又は指定IDへ投影します。
//
// 引数: parent/actualID/reason(string)、want([]selectedDay164Allocation)、s(SelectedSnapshot)。
// 返り値: []SelectedIntent。返済量は実整数へ切捨て、予約は同じ正規化実量だけです。
func (d *SelectedDay164Policy) closeSelectedDay(parent, actualID, reason string, want []selectedDay164Allocation, s SelectedSnapshot) []SelectedIntent {
	free := []selectedDay164Allocation{}
	total := 0.
	for _, a := range want {
		c := d.canonicalSelectedDay(a.ID)
		if c == nil {
			continue
		}
		q := math.Min(a.Quantity, math.Max(0, c.Quantity-c.ReservedClose))
		if q > 1e-8 {
			free = append(free, selectedDay164Allocation{a.ID, q})
			total += q
		}
	}
	left := d.actualSelectedDay(total)
	if left <= 0 {
		return nil
	}
	out := []SelectedIntent{}
	at := 0
	for _, l := range lotOrderSelectedDay(s, parent) {
		if actualID != "" && l.ID != actualID {
			continue
		}
		q := min(left, max(0, l.Quantity-d.reservedSelectedDay(l.ID, s)))
		if q <= 0 {
			continue
		}
		p := d.Parents[parent]
		id := d.nextSelectedDay()
		in := SelectedIntent{PolicyID: id, Parent: parent, SourceID: l.SourceID, ActualID: l.ID, Owner: l.Owner, Campaign: l.Campaign, Side: -l.Side, Quantity: q, Kind: l.Kind, Reason: reason}
		if p != nil {
			in.Owner = p.Owner
			in.Campaign = p.Campaign
		}
		pending := &selectedDay164Pending{Intent: in, ActualRequested: q}
		if reason == "source_partial/D164_15" {
			pending.QualificationID = id
			d.CloseGroups[id] = &selectedDay164CloseGroup{Parent: parent, Expected: q}
		}
		need := d.microSelectedDay(q)
		for need > 1e-7 && at < len(free) {
			a := &free[at]
			v := math.Min(need, a.Quantity)
			pending.Allocations = append(pending.Allocations, selectedDay164Allocation{a.ID, v})
			d.canonicalSelectedDay(a.ID).ReservedClose += v
			a.Quantity -= v
			need -= v
			if a.Quantity < 1e-8 {
				at++
			}
		}
		d.Pending[id] = pending
		out = append(out, in)
		left -= q
		if left == 0 {
			break
		}
	}
	return out
}

// protectionWantSelectedDay は指定実玉に対応するCanonicalを先に消費します。
//
// 引数: l(SelectedLot)。返り値: []selectedDay164Allocation。同SourceIDかつ役割優先、残りは親FIFOです。
func (d *SelectedDay164Policy) protectionWantSelectedDay(l SelectedLot) []selectedDay164Allocation {
	cs := append([]*selectedDay164Canonical(nil), d.Canonical...)
	sort.SliceStable(cs, func(i, j int) bool {
		a := cs[i].SourceID == l.SourceID && cs[i].Kind == l.Kind
		b := cs[j].SourceID == l.SourceID && cs[j].Kind == l.Kind
		return a && !b
	})
	out := []selectedDay164Allocation{}
	left := d.microSelectedDay(l.Quantity)
	for _, c := range cs {
		if c.Parent != l.Parent || c.Side != l.Side {
			continue
		}
		q := math.Min(left, math.Max(0, c.Quantity-c.ReservedClose))
		if q > 1e-8 {
			out = append(out, selectedDay164Allocation{c.ID, q})
			left -= q
		}
		if left < 1e-8 {
			break
		}
	}
	return out
}

// sourceCloseSelectedDay は元返済のSourceID比例を実所有FIFOへ写します。
//
// 引数: e(SelectedSourceEvent)、s(SelectedSnapshot)。返り値: []SelectedIntent。
func (d *SelectedDay164Policy) sourceCloseSelectedDay(e SelectedSourceEvent, s SelectedSnapshot) []SelectedIntent {
	p := d.Parents[e.Parent]
	if p == nil {
		return nil
	}
	p.SourceQuantity = max(0, e.ParentRemainingMicro)
	for _, t := range e.Targets {
		p.SourceHolds[t.SourceID] = max(0, t.RemainingMicro)
	}
	if p.SourceQuantity == 0 {
		p.Ended = true
		p.Cancelled = true
		p.Deferred = false
	}
	want := []selectedDay164Allocation{}
	out := []SelectedIntent{}
	for _, c := range d.Canonical {
		if c.Parent != p.Key {
			continue
		}
		ratio := 0.
		for _, t := range e.Targets {
			if t.SourceID == c.SourceID && t.BeforeMicro > 0 {
				ratio = math.Min(1, float64(t.QuantityMicro)/float64(t.BeforeMicro))
				break
			}
		}
		if p.Ended {
			ratio = 1
		}
		if ratio <= 0 {
			continue
		}
		// 発注済新規は数量変更できないため取消を要求。遅着した分も次判断で返済します。
		if pending := d.Pending[c.ID]; pending != nil && !pending.Done && c.PendingNew > 1e-8 {
			pending.CloseLateFill = true
			if !pending.CancelRequested {
				pending.CancelRequested = true
				out = append(out, SelectedIntent{Parent: p.Key, PolicyID: d.nextSelectedDay(), CancelIntentID: pending.Intent.PolicyID, Reason: "D164_15_源返済による未約定取消"})
			}
		}
		q := math.Floor(c.Quantity*ratio + 1e-9)
		if p.Ended {
			q = c.Quantity
		}
		if q > 0 {
			want = append(want, selectedDay164Allocation{c.ID, q})
		}
	}
	reason := "source_partial/D164_15"
	if p.Ended {
		reason = "source_end/D164_15"
	}
	out = append(out, d.closeSelectedDay(p.Key, "", reason, want, s)...)
	return out
}

// manageProtectionSelectedDay は実約定建値だけから保護・追加限定停止を判断します。
//
// 引数: o(Observation)、s(SelectedSnapshot)、rules(selectedNightRules)。返り値: []SelectedIntent。
func (d *SelectedDay164Policy) manageProtectionSelectedDay(o Observation, s SelectedSnapshot, rules selectedNightRules) []SelectedIntent {
	out := []SelectedIntent{}
	for _, l := range s.Lots {
		if l.Quantity <= 0 || d.reservedSelectedDay(l.ID, s) > 0 {
			continue
		}
		p := d.Parents[l.Parent]
		if p == nil {
			continue
		}
		reason := ""
		if p.Ended || d.Stopped {
			reason = "source_end/D164_15"
		} else if pending := d.Pending[d.ActualEntries[l.ID]]; pending != nil && pending.CloseLateFill {
			reason = "source_changed_late_fill/D164_15"
		}
		state := d.Protection[l.ID]
		if state == nil {
			state = &selectedDay164Protection{}
			d.Protection[l.ID] = state
		}
		mark := o.Bid
		if l.Side < 0 {
			mark = o.Ask
		}
		gain := float64(l.Side) * (mark - l.Entry)
		if rules.Bool("d164_15.arm_residual", map[string]any{"residual_eligible": state.Eligible, "gain": gain}) && !state.Active {
			state.Active = true
			state.ActiveAt = o.Time
		}
		if reason == "" && rules.Bool("d164_15.exit_residual", map[string]any{"residual_active": state.Active, "gain": gain}) {
			reason = "protect_residual_floor/D164_15"
		}
		if reason == "" && rules.Bool("d164_15.stop_add", map[string]any{"source_add": l.Kind == "source_add", "gain": gain}) {
			reason = "tactical_stop50/D164_15"
		}
		if reason != "" {
			out = append(out, d.closeSelectedDay(l.Parent, l.ID, reason, d.protectionWantSelectedDay(l), s)...)
		}
	}
	return out
}

// requestsSelectedDay はSource通知を全て論理会計へ反映し、採用希望だけを返します。
//
// 引数: o(Observation)、events([]SelectedSourceEvent)、s(SelectedSnapshot)、rules(selectedNightRules)、allow(bool)。
// 返り値: []selectedDay164Request。拒否した追撃もSource残量には含めます。
func (d *SelectedDay164Policy) requestsSelectedDay(o Observation, events []SelectedSourceEvent, s SelectedSnapshot, rules selectedNightRules, allow bool) []selectedDay164Request {
	out := []selectedDay164Request{}
	short := shortSelectedDay(o)
	for _, e := range events {
		if e.Signal.Action != "new" {
			continue
		}
		p := d.Parents[e.Parent]
		if p == nil {
			p = &selectedDay164Parent{Key: e.Parent, Owner: e.Signal.Owner, Campaign: e.Signal.Campaign, Side: e.Signal.Side, Started: o.Time, SourceHolds: map[string]int{}}
			d.Parents[e.Parent] = p
		}
		if p.Ended {
			continue
		}
		initial := !p.InitialSeen
		p.SourceQuantity = max(0, e.ParentRemainingMicro)
		p.SourcePeak = max(p.SourcePeak, p.SourceQuantity)
		p.SourceHolds[e.SourceID] += e.QuantityMicro
		if initial {
			p.InitialSeen = true
			p.NativeInitial = e.QuantityMicro
			p.OriginalDesired = selectedDay164Desired(supportSelectedDay(o, p.Side))
			if !allow {
				p.Rejected = "新規停止時の源初回"
				continue
			}
			if conditionSelectedDay(rules, "delay_initial", short, p.Side) {
				p.Deferred = true
				p.DeferredAt = o.Time
				continue
			}
			out = append(out, selectedDay164Request{p, e.SourceID, "initial", p.OriginalDesired, true})
			continue
		}
		if !allow || p.Rejected != "" || p.Cancelled || p.Deferred || d.pendingSelectedDay(p.Key, s) || !conditionSelectedDay(rules, "confirm_add", short, p.Side) {
			continue
		}
		want := max(0, int(math.Floor(float64(e.QuantityMicro)*p.Factor+1e-10)))
		out = append(out, selectedDay164Request{p, e.SourceID, "source_add", want, false})
	}
	return out
}

// allocateSelectedDay は同原行希望をJ02と同じ比例・最大剰余で配分します。
//
// 引数: requests([]selectedDay164Request)、s(SelectedSnapshot)。返り値: []SelectedIntent。
func (d *SelectedDay164Policy) allocateSelectedDay(requests []selectedDay164Request, s SelectedSnapshot) []SelectedIntent {
	out := []SelectedIntent{}
	for _, side := range []int{-1, 1} {
		indexes := []int{}
		sum := 0
		used := 0.
		for _, c := range d.Canonical {
			if c.Side == side {
				used += c.Quantity + c.PendingNew
			}
		}
		available := max(0, int(math.Floor(100-used+1e-8)))
		if s.SideAvailable != nil {
			available = min(available, int(math.Floor(d.microSelectedDay(s.SideAvailable[side])+1e-8)))
		}
		for i, r := range requests {
			if r.Parent.Side == side {
				indexes = append(indexes, i)
				sum += r.Quantity
			}
		}
		allocated := map[int]int{}
		left := available
		for _, i := range indexes {
			q := requests[i].Quantity
			if sum > available {
				q = int(math.Floor(float64(q*available)/float64(sum) + 1e-10))
			}
			allocated[i] = q
			left -= q
		}
		if sum > available {
			sort.SliceStable(indexes, func(i, j int) bool {
				a, b := requests[indexes[i]], requests[indexes[j]]
				ra := float64(a.Quantity*available)/float64(sum) - float64(allocated[indexes[i]])
				rb := float64(b.Quantity*available)/float64(sum) - float64(allocated[indexes[j]])
				if ra != rb {
					return ra > rb
				}
				if a.Parent.Started != b.Parent.Started {
					return a.Parent.Started < b.Parent.Started
				}
				return a.Parent.Key < b.Parent.Key
			})
			for _, i := range indexes {
				if left < 1 {
					break
				}
				allocated[i]++
				left--
			}
		}
		for _, i := range indexes {
			r := requests[i]
			q := allocated[i]
			if q <= 0 || d.pendingSelectedDay(r.Parent.Key, s) {
				if r.Initial && q <= 0 {
					r.Parent.Rejected = "枠不足"
				}
				continue
			}
			in, got := d.openSelectedDay(r.Parent, r.SourceID, r.Kind, q)
			if got == 0 {
				if r.Initial {
					r.Parent.Rejected = "商品枚数への丸め0"
				}
				continue
			}
			if r.Initial {
				r.Parent.Factor = got / float64(max(1, r.Parent.NativeInitial))
			}
			out = append(out, in)
		}
	}
	return out
}

// releaseSelectedDay は延期時点の希望を上限として生存Sourceへ配分します。
//
// 引数: o(Observation)、s(SelectedSnapshot)、rules(selectedNightRules)、allow(bool)。返り値: []SelectedIntent。
func (d *SelectedDay164Policy) releaseSelectedDay(o Observation, s SelectedSnapshot, rules selectedNightRules, allow bool) []SelectedIntent {
	out := []SelectedIntent{}
	keys := []string{}
	for k := range d.Parents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p := d.Parents[key]
		if !p.Deferred || p.Ended || p.Cancelled || p.Rejected != "" {
			continue
		}
		if o.Time-p.DeferredAt > 30 {
			p.Deferred = false
			p.Cancelled = true
			p.Rejected = "初回再加速待機30秒超"
			continue
		}
		if !allow || d.pendingSelectedDay(key, s) || !conditionSelectedDay(rules, "release_initial", shortSelectedDay(o), p.Side) {
			continue
		}
		desired := min(p.OriginalDesired, selectedDay164Desired(supportSelectedDay(o, p.Side)))
		want := int(math.Floor(float64(desired)*math.Min(1, float64(p.SourceQuantity)/float64(max(1, p.SourcePeak))) + 1e-10))
		used := 0.
		for _, c := range d.Canonical {
			if c.Side == p.Side {
				used += c.Quantity + c.PendingNew
			}
		}
		want = min(want, max(0, int(math.Floor(100-used+1e-8))))
		if s.SideAvailable != nil {
			want = min(want, int(math.Floor(d.microSelectedDay(s.SideAvailable[p.Side])+1e-8)))
		}
		if want <= 0 {
			continue
		}
		type share struct {
			source   string
			quantity int
			fraction float64
		}
		shares := []share{}
		left := want
		for source, n := range p.SourceHolds {
			if n <= 0 {
				continue
			}
			exact := float64(want*n) / float64(max(1, p.SourceQuantity))
			q := int(math.Floor(exact + 1e-10))
			shares = append(shares, share{source, q, exact - float64(q)})
			left -= q
		}
		sort.Slice(shares, func(i, j int) bool {
			if shares[i].fraction != shares[j].fraction {
				return shares[i].fraction > shares[j].fraction
			}
			return shares[i].source < shares[j].source
		})
		for i := 0; i < len(shares) && left > 0; i++ {
			shares[i].quantity++
			left--
		}
		sort.Slice(shares, func(i, j int) bool { return shares[i].source < shares[j].source })
		got := 0.
		for _, part := range shares {
			in, q := d.openSelectedDay(p, part.source, "delayed_initial", part.quantity)
			if q > 0 {
				got += q
				out = append(out, in)
			}
		}
		if got > 0 {
			p.Deferred = false
			p.Factor = math.Min(got/float64(max(1, p.SourceQuantity)), float64(p.OriginalDesired)/float64(max(1, p.NativeInitial)))
		}
	}
	return out
}

// StepSelectedDay は源返済・実玉保護・初回と追撃・延期解除の順で判定します。
//
// 引数: o(Observation)、events([]SelectedSourceEvent)、snapshot(SelectedSnapshot)、rules(selectedNightRules)、fresh/allowEntry/forceClose(bool)。
// 返り値: []SelectedIntentとerror。古い受信では源数量だけ更新して発注を待ちます。
// 新鮮な交差板でも元手法の確定通知は当行で受け取り、独自保護・延期解除だけ正常気配を必須にします。
func (d *SelectedDay164Policy) StepSelectedDay(o Observation, events []SelectedSourceEvent, snapshot SelectedSnapshot, rules selectedNightRules, fresh, allowEntry, forceClose bool) ([]SelectedIntent, error) {
	if d.Cap <= 0 || snapshot.Cap != d.Cap {
		return nil, errors.New("D164_15の実商品枚数上限が不正又は起動中に変更されました")
	}
	if rules == nil {
		return nil, errors.New("D164_15のルールがありません")
	}
	d.Stopped = d.Stopped || forceClose
	unique := []SelectedSourceEvent{}
	for _, e := range events {
		if e.Signal.ID == "" || e.Parent == "" {
			return nil, errors.New("D164_15の源識別子が空です")
		}
		key := e.Signal.ID + "|" + e.Parent
		if !d.SeenSources[key] {
			d.SeenSources[key] = true
			unique = append(unique, e)
		}
	}
	if !fresh || !finite(o.Time) {
		for _, e := range unique {
			if e.Signal.Action == "close" {
				d.DeferredIntents = append(d.DeferredIntents, d.sourceCloseSelectedDay(e, snapshot)...)
			}
		}
		d.requestsSelectedDay(o, unique, snapshot, rules, false)
		return nil, rules.Err()
	}
	out := d.DeferredIntents
	d.DeferredIntents = nil
	retry := d.Retry
	d.Retry = nil
	for _, r := range retry {
		v := d.closeSelectedDay(r.Parent, r.ActualID, r.Reason, r.Allocations, snapshot)
		if r.QualificationID != "" {
			for _, in := range v {
				p := d.Pending[in.PolicyID]
				delete(d.CloseGroups, p.QualificationID)
				p.QualificationID = r.QualificationID
			}
		}
		out = append(out, v...)
		if len(v) == 0 {
			d.Retry = append(d.Retry, r)
		}
	}
	for _, e := range unique {
		if e.Signal.Action == "close" {
			out = append(out, d.sourceCloseSelectedDay(e, snapshot)...)
		}
	}
	if d.Stopped {
		for _, p := range d.Parents {
			p.Deferred = false
			p.Cancelled = true
		}
		for _, p := range d.Pending {
			if !p.Done && p.Intent.ActualID == "" && !p.CancelRequested {
				p.CancelRequested = true
				p.CloseLateFill = true
				out = append(out, SelectedIntent{PolicyID: d.nextSelectedDay(), Parent: p.Intent.Parent, CancelIntentID: p.Intent.PolicyID, Reason: "D164_15_終了時新規取消"})
			}
		}
	}
	normal := o.QuoteOK && finite(o.Bid) && finite(o.Ask) && o.Bid > 0 && o.Ask > o.Bid
	if normal {
		out = append(out, d.manageProtectionSelectedDay(o, snapshot, rules)...)
	}
	requests := d.requestsSelectedDay(o, unique, snapshot, rules, allowEntry && !d.Stopped)
	out = append(out, d.allocateSelectedDay(requests, snapshot)...)
	if normal {
		out = append(out, d.releaseSelectedDay(o, snapshot, rules, allowEntry && !d.Stopped)...)
	}
	if err := rules.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// FillSelectedDay は実約定量だけをCanonicalへ反映し、利益部分返済の資格を付けます。
//
// 引数: e(SelectedFillEvent)、o(Observation)、snapshot(SelectedSnapshot)。返り値: error。
// snapshotは当該一約定反映直後とし、後で建った玉へ利益保護資格を遡及しません。
func (d *SelectedDay164Policy) FillSelectedDay(e SelectedFillEvent, o Observation, snapshot SelectedSnapshot) error {
	if e.Intent.PolicyID == "" {
		return d.ExternalFillSelectedDay(e, o, snapshot)
	}
	if e.ExecutionID == "" {
		return errors.New("D164_15の実約定識別子が空です")
	}
	if d.SeenExecutions[e.ExecutionID] {
		return nil
	}
	p := d.Pending[e.Intent.PolicyID]
	if p == nil {
		return errors.New("D164_15の実約定に対応する政策予約がありません")
	}
	if e.Quantity <= 0 || p.ActualFilled+e.Quantity > p.ActualRequested {
		return errors.New("D164_15の実約定量が予約を超えました")
	}
	amount := d.microSelectedDay(e.Quantity)
	if p.Done && p.Intent.ActualID != "" {
		// 取消完了後、再送前に判明した実約定は解放済み予約を一度だけ取り戻します。
		found := -1
		for i := range d.Retry {
			if d.Retry[i].Origin == p.Intent.PolicyID {
				found = i
				break
			}
		}
		if found < 0 {
			return errors.New("D164_15の取消後返済約定は再送と競合しています")
		}
		need := amount
		for _, a := range p.Allocations {
			q := math.Min(need, a.Quantity)
			if q <= 0 {
				continue
			}
			c := d.canonicalSelectedDay(a.ID)
			if c == nil || c.Quantity-c.ReservedClose+1e-7 < q {
				return errors.New("D164_15の取消後返済量が自由残量を超えています")
			}
			need -= q
			if need < 1e-7 {
				break
			}
		}
		if need > 1e-7 {
			return errors.New("D164_15の取消後返済予約が不足しています")
		}
		need = amount
		for i := range d.Retry[found].Allocations {
			a := &d.Retry[found].Allocations[i]
			q := math.Min(need, a.Quantity)
			if q <= 0 {
				continue
			}
			d.canonicalSelectedDay(a.ID).ReservedClose += q
			a.Quantity -= q
			need -= q
			if need < 1e-7 {
				break
			}
		}
	}
	if p.Intent.ActualID == "" {
		c := d.canonicalSelectedDay(p.CanonicalID)
		if c == nil {
			return errors.New("D164_15の新規Canonicalがありません")
		}
		c.Quantity += amount
		c.PendingNew = math.Max(0, c.PendingNew-amount)
		if d.ActualEntries == nil {
			d.ActualEntries = map[string]string{}
		}
		d.ActualEntries[e.LotID] = p.Intent.PolicyID
	} else {
		left := amount
		for i := range p.Allocations {
			a := &p.Allocations[i]
			q := math.Min(left, a.Quantity)
			if q <= 0 {
				continue
			}
			c := d.canonicalSelectedDay(a.ID)
			if c == nil || c.Quantity+1e-7 < q || c.ReservedClose+1e-7 < q {
				return errors.New("D164_15の返済同期数量が不整合です")
			}
			c.Quantity = math.Max(0, c.Quantity-q)
			c.ReservedClose = math.Max(0, c.ReservedClose-q)
			a.Quantity -= q
			left -= q
			if left < 1e-7 {
				break
			}
		}
		if left > 1e-7 {
			return errors.New("D164_15の返済同期が不足しました")
		}
	}
	p.ActualFilled += e.Quantity
	p.Gross += e.Gross
	if p.QualificationID != "" {
		g := d.CloseGroups[p.QualificationID]
		if g == nil || g.Filled+e.Quantity > g.Expected {
			return errors.New("D164_15の元返済群の数量が不整合です")
		}
		g.Filled += e.Quantity
		g.Gross += e.Gross
	}
	d.SeenExecutions[e.ExecutionID] = true
	if p.ActualFilled == p.ActualRequested {
		p.Done = true
	}
	d.qualifySelectedDay(p, e.Time, snapshot)
	return nil
}

// ExternalFillSelectedDay は場末等の共通強制返済を実所有IDから同期します。
//
// 引数: e(SelectedFillEvent)、o(Observation)、snapshot(SelectedSnapshot)。返り値: error。
// 実新規を勝手に発生させず、既予約返済と競合する強制返済は不整合として停止します。
func (d *SelectedDay164Policy) ExternalFillSelectedDay(e SelectedFillEvent, o Observation, snapshot SelectedSnapshot) error {
	if e.ExecutionID == "" || e.Quantity <= 0 || e.Intent.ActualID == "" {
		return errors.New("D164_15の強制返済通知が不正です")
	}
	if d.SeenExecutions[e.ExecutionID] {
		return nil
	}
	var lot *SelectedLot
	for i := range snapshot.Lots {
		if snapshot.Lots[i].ID == e.Intent.ActualID {
			lot = &snapshot.Lots[i]
			break
		}
	}
	if lot == nil {
		return errors.New("D164_15の強制返済対象IDがありません")
	}
	view := *lot
	view.Quantity = e.Quantity
	allocations := d.protectionWantSelectedDay(view)
	total := 0.
	for _, a := range allocations {
		total += a.Quantity
	}
	need := d.microSelectedDay(e.Quantity)
	if total+1e-7 < need {
		return errors.New("D164_15の強制返済がCanonical自由量を超えました")
	}
	for _, a := range allocations {
		q := math.Min(need, a.Quantity)
		d.canonicalSelectedDay(a.ID).Quantity -= q
		need -= q
		if need < 1e-7 {
			break
		}
	}
	d.SeenExecutions[e.ExecutionID] = true
	return nil
}

// DoneSelectedDay は取消・失効の未約定予約だけを解放します。
//
// 引数: policyID(string)、snapshot(SelectedSnapshot)。返り値: error。
// 返済の未完了分は再判断待ちに残し、部分利確条件が消えても必要返済を忘れません。
func (d *SelectedDay164Policy) DoneSelectedDay(policyID string, snapshot SelectedSnapshot) error {
	p := d.Pending[policyID]
	if p == nil {
		return errors.New("D164_15の完了予約がありません")
	}
	if p.Done {
		return nil
	}
	if p.Intent.ActualID == "" {
		c := d.canonicalSelectedDay(p.CanonicalID)
		if c == nil {
			return errors.New("D164_15の取消Canonicalがありません")
		}
		c.PendingNew = 0
		if p.ActualFilled == 0 && (p.Intent.Kind == "initial" || p.Intent.Kind == "delayed_initial") {
			if parent := d.Parents[p.Intent.Parent]; parent != nil {
				parent.Rejected = "新規予約の未約定終了"
			}
		}
	} else {
		remaining := []selectedDay164Allocation{}
		for _, a := range p.Allocations {
			if a.Quantity <= 1e-8 {
				continue
			}
			c := d.canonicalSelectedDay(a.ID)
			if c == nil || c.ReservedClose+1e-7 < a.Quantity {
				return errors.New("D164_15の取消返済予約が不整合です")
			}
			c.ReservedClose = math.Max(0, c.ReservedClose-a.Quantity)
			remaining = append(remaining, a)
		}
		if len(remaining) > 0 {
			pin := p.Intent.ActualID
			if p.Intent.Reason == "source_partial/D164_15" || p.Intent.Reason == "source_end/D164_15" {
				pin = ""
			}
			d.Retry = append(d.Retry, selectedDay164Retry{p.Intent.Parent, pin, p.Intent.Reason, remaining, p.Intent.PolicyID, p.QualificationID})
		}
	}
	p.Done = true
	return nil
}

// qualifySelectedDay は一つの実玉返済注文の完了粗益で残玉資格を確定します。
//
// 引数: p(*selectedDay164Pending)、at(float64)、s(SelectedSnapshot)。返り値: なし。
// 正本はActualID別注文です。別ActualIDの赤字注文と合算せず、同注文内の分割約定だけ合算します。
func (d *SelectedDay164Policy) qualifySelectedDay(p *selectedDay164Pending, at float64, s SelectedSnapshot) {
	parent := d.Parents[p.Intent.Parent]
	g := d.CloseGroups[p.QualificationID]
	if g == nil || g.Qualified || g.Filled != g.Expected || g.Expected <= 0 || g.Gross <= 0 || parent == nil || parent.SourceQuantity <= 0 {
		return
	}
	g.Qualified = true
	for _, l := range s.Lots {
		if l.Parent != parent.Key || l.Quantity <= 0 {
			continue
		}
		st := d.Protection[l.ID]
		if st == nil {
			st = &selectedDay164Protection{}
			d.Protection[l.ID] = st
		}
		if !st.Eligible {
			st.Eligible = true
			st.EligibleAt = at
		}
	}
}
