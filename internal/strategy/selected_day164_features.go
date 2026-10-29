package strategy

import (
	"math"
	"sort"
)

type selectedDay164Row struct {
	Time, Mid, OFISum float64
	Bad, OFICount     int64
	Valid             bool
	OFIValid          bool
}
type selectedDay164Market struct {
	Rows          []selectedDay164Row
	Last          Board
	Seen          bool
	Bad, OFICount int64
	OFISum        float64
}
type SelectedDay164Features struct {
	Markets map[string]*selectedDay164Market
}
type SelectedDay164Support struct {
	Known                                                  bool
	Reason                                                 string
	Own5, Own15, Own120, Own300, Peer120, OFI30, M, T, Opp float64
	OwnAt, PeerAt                                          float64
	Components                                             *SelectedDay164Components `json:"components,omitempty"`
}

type SelectedDay164Window struct {
	Known                                                   bool
	Reason                                                  string
	CurrentAt, EndpointAt, Elapsed, Delay, RawReturn, Value float64
	IntermediateBad                                         int64
}
type SelectedDay164Components struct {
	OwnKnown, PeerKnown, OFIKnown    bool
	OwnReason, PeerReason, OFIReason string
	Windows                          map[string]SelectedDay164Window
}

// selectedDay164Clip は符号を維持した観測を有限区間へ制限します。
//
// 引数: x(float64)。返り値: float64は−1～1です。
func selectedDay164Clip(x float64) float64 { return math.Max(-1, math.Min(1, x)) }

// OnBoard は原板から実経過時間を確認できる短期速度用履歴を追加します。
//
// 引数: b(Board)。返り値: なし。同時計は順番だけを維持し未来相手へ使用しません。
func (f *SelectedDay164Features) OnBoard(b Board) {
	if f.Markets == nil {
		f.Markets = map[string]*selectedDay164Market{}
	}
	s := f.Markets[b.Reference]
	if s == nil {
		s = &selectedDay164Market{}
		f.Markets[b.Reference] = s
	}
	valid := b.Bid > 0 && b.Ask > b.Bid && !math.IsInf(b.Ask, 0) && !math.IsNaN(b.Bid)
	if !valid || (s.Seen && b.Time-s.Last.Time > 15) {
		s.Bad++
	}
	finiteQty := func(x float64) bool { return x > 0 && !math.IsNaN(x) && !math.IsInf(x, 0) }
	qty := valid && finiteQty(b.BidQuantity) && finiteQty(b.AskQuantity)
	p := s.Last
	ofiValid := false
	if qty && s.Seen && p.Bid > 0 && p.Ask > p.Bid && finiteQty(p.BidQuantity) && finiteQty(p.AskQuantity) && b.Time-p.Time <= 15 {
		x := 0.
		if b.Bid >= p.Bid {
			x += b.BidQuantity
		}
		if b.Bid <= p.Bid {
			x -= p.BidQuantity
		}
		if b.Ask <= p.Ask {
			x -= b.AskQuantity
		}
		if b.Ask >= p.Ask {
			x += p.AskQuantity
		}
		den := b.BidQuantity + p.BidQuantity + b.AskQuantity + p.AskQuantity
		v := x / den
		if !math.IsInf(x, 0) && !math.IsInf(den, 0) && !math.IsNaN(v) && !math.IsInf(v, 0) && !math.IsInf(s.OFISum+v, 0) {
			s.OFISum += v
			s.OFICount++
			ofiValid = true
		}
	}
	r := selectedDay164Row{Time: b.Time, Bad: s.Bad, Valid: valid, OFIValid: ofiValid, OFISum: s.OFISum, OFICount: s.OFICount}
	if valid {
		r.Mid = b.Bid + (b.Ask-b.Bid)/2
	}
	s.Rows = append(s.Rows, r)
	s.Last = b
	s.Seen = true
	keep := sort.Search(len(s.Rows), func(i int) bool { return s.Rows[i].Time >= b.Time-340 })
	if keep > 0 {
		s.Rows = s.Rows[keep:]
	}
}

// selectedDay164Return は厳密過去の窓端だけから価格比を計算します。
//
// 引数: rows([]selectedDay164Row)、i(int)、seconds/tolerance(float64)。返り値: bp変化(float64)、既知(bool)。
func selectedDay164Return(rows []selectedDay164Row, i int, seconds, tolerance float64) (float64, bool) {
	if i < 0 || !rows[i].Valid {
		return 0, false
	}
	r := rows[i]
	bound := r.Time - seconds
	j := sort.Search(i+1, func(j int) bool { return rows[j].Time > bound }) - 1
	if j < 0 || !rows[j].Valid || bound-rows[j].Time > tolerance || r.Bad != rows[j].Bad {
		return 0, false
	}
	v := 10000 * (r.Mid/rows[j].Mid - 1)
	return v, !math.IsNaN(v) && !math.IsInf(v, 0)
}

// elapsedSelectedDay164Window は現在と許容内の最後の正常過去端から速度又は価格比を計算します。
//
// 引数: rows([]selectedDay164Row)、i(int)、seconds(float64)、normalize(bool)。
// 返り値: SelectedDay164Window。途中の異常価格は採らず、異常の存在だけで両端比を無効にしません。
func elapsedSelectedDay164Window(rows []selectedDay164Row, i int, seconds float64, normalize bool) SelectedDay164Window {
	w := SelectedDay164Window{Reason: "現在値不明"}
	if i < 0 || i >= len(rows) {
		return w
	}
	r := rows[i]
	w.CurrentAt = r.Time
	if !r.Valid || !(r.Mid > 0) || math.IsInf(r.Mid, 0) || math.IsNaN(r.Mid) {
		return w
	}
	bound := r.Time - seconds
	j := sort.Search(i+1, func(j int) bool { return rows[j].Time > bound }) - 1
	for j >= 0 && !rows[j].Valid {
		j--
	}
	if j < 0 {
		w.Reason = "過去端履歴不足"
		return w
	}
	w.EndpointAt = rows[j].Time
	w.Elapsed = r.Time - rows[j].Time
	w.Delay = bound - rows[j].Time
	w.IntermediateBad = r.Bad - rows[j].Bad
	if w.Delay > 15 || w.Elapsed <= 0 {
		w.Reason = "正常過去端が許容15秒より古い"
		return w
	}
	if !(rows[j].Mid > 0) || math.IsNaN(rows[j].Mid) || math.IsInf(rows[j].Mid, 0) {
		w.Reason = "過去端価格非有限"
		return w
	}
	w.RawReturn = 10000 * (r.Mid/rows[j].Mid - 1)
	if math.IsNaN(w.RawReturn) || math.IsInf(w.RawReturn, 0) {
		w.RawReturn = 0
		w.Reason = "価格比非有限"
		return w
	}
	w.Value = w.RawReturn
	if normalize {
		w.Value *= seconds / w.Elapsed
	}
	w.Known = true
	w.Reason = ""
	return w
}

// Support は自己価格を必須、相手価格とOFIを個別補助として未校正支持を返します。
//
// 引数: at(float64)、side(int)。返り値: SelectedDay164Support。補助未知は0寄与で重みを再正規化しません。
func (f *SelectedDay164Features) Support(at float64, side int) SelectedDay164Support {
	c := &SelectedDay164Components{OwnReason: "自己履歴不足", PeerReason: "相手履歴不足", OFIReason: "OFI履歴不足", Windows: map[string]SelectedDay164Window{}}
	o := SelectedDay164Support{Reason: "自己価格未知", Components: c}
	own, peer := f.Markets["NK225"], f.Markets["TOPIX"]
	if own == nil || len(own.Rows) == 0 {
		return o
	}
	i := len(own.Rows) - 1
	r := own.Rows[i]
	o.OwnAt = r.Time
	if r.Time != at || !r.Valid {
		c.OwnReason = "自己現在時計又は気配が不正"
		return o
	}
	// ----------------------------------------
	// 価格窓は正常な両端を使い、短期5/15だけ実経過時間で速度へ換算します。
	c.OwnKnown = true
	c.OwnReason = ""
	for _, x := range []struct {
		name    string
		seconds float64
		target  *float64
	}{{"own5", 5, &o.Own5}, {"own15", 15, &o.Own15}, {"own120", 120, &o.Own120}, {"own300", 300, &o.Own300}} {
		w := elapsedSelectedDay164Window(own.Rows, i, x.seconds, x.seconds <= 15)
		c.Windows[x.name] = w
		if w.Known {
			*x.target = w.Value
		} else {
			c.OwnKnown = false
			if c.OwnReason == "" {
				c.OwnReason = x.name + ":" + w.Reason
			}
		}
	}
	if peer != nil {
		j := sort.Search(len(peer.Rows), func(j int) bool { return peer.Rows[j].Time >= at }) - 1
		if j >= 0 {
			o.PeerAt = peer.Rows[j].Time
			if at-o.PeerAt <= 15 {
				w := elapsedSelectedDay164Window(peer.Rows, j, 120, false)
				c.Windows["peer120"] = w
				c.PeerKnown = w.Known
				c.PeerReason = w.Reason
				if w.Known {
					o.Peer120 = w.Value
				}
			} else {
				c.PeerReason = "相手現在値が15秒より古い"
			}
		}
	}
	k := sort.Search(len(own.Rows), func(k int) bool { return own.Rows[k].Time >= at-30 }) - 1
	if !r.OFIValid {
		c.OFIReason = "現在OFI差分不成立"
	} else if k < 0 {
		c.OFIReason = "OFI30秒履歴不足"
	} else if r.Bad != own.Rows[k].Bad {
		c.OFIReason = "OFI窓内異常又は空白"
	} else if r.OFICount-own.Rows[k].OFICount < 2 {
		c.OFIReason = "OFI差分2件未満"
	} else {
		v := (r.OFISum - own.Rows[k].OFISum) / float64(r.OFICount-own.Rows[k].OFICount)
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			o.OFI30 = v
			c.OFIKnown = true
			c.OFIReason = ""
		} else {
			c.OFIReason = "OFI非有限"
		}
	}
	if !c.OwnKnown {
		return o
	}
	d := float64(side)
	o.M = math.Max(0, .45*selectedDay164Clip(d*o.Own120/8)+.35*selectedDay164Clip(d*o.Own300/16)+.20*selectedDay164Clip(d*o.Peer120/8))
	z := .45*selectedDay164Clip(d*o.Own15/3) + .35*selectedDay164Clip(d*o.Own5/1.5) + .20*selectedDay164Clip(d*o.OFI30/.05)
	o.T = math.Max(0, z)
	o.Opp = math.Max(0, -z)
	o.Known = true
	o.Reason = ""
	return o
}

// ----------------------------------------
// 日中改修の失速・回復はbp幅と区別した円毎秒で判定する。

type SelectedDay164ShortWindow struct {
	Known                                 bool
	Move, Speed, Elapsed, Endpoint, Delay float64
	Reason                                string
}
type SelectedDay164Short struct {
	Five, Fifteen SelectedDay164ShortWindow
	Time, Mid     float64
}

// Short は既に受信した正常価格端点から円幅と円毎秒を分けて計算します。
//
// 引数: at(float64)。返り値: SelectedDay164Short。長い空白・異常板をまたぐ窓は未知です。
func (f *SelectedDay164Features) Short(at float64) SelectedDay164Short {
	out := SelectedDay164Short{Time: at}
	m := f.Markets["NK225"]
	if m == nil || len(m.Rows) == 0 {
		return out
	}
	i := len(m.Rows) - 1
	cur := m.Rows[i]
	if !cur.Valid || cur.Time != at {
		return out
	}
	out.Mid = cur.Mid
	get := func(h float64) SelectedDay164ShortWindow {
		w := SelectedDay164ShortWindow{Reason: "過去端不足"}
		j := sort.Search(i+1, func(j int) bool { return m.Rows[j].Time > at-h }) - 1
		for j >= 0 && !m.Rows[j].Valid {
			j--
		}
		if j < 0 {
			return w
		}
		old := m.Rows[j]
		w.Endpoint = old.Time
		w.Delay = at - h - old.Time
		w.Elapsed = at - old.Time
		if w.Delay > 15 || w.Elapsed <= 0 || cur.Bad != old.Bad {
			w.Reason = "窓内欠測又は価格時計空白"
			return w
		}
		w.Move = cur.Mid - old.Mid
		w.Speed = w.Move / w.Elapsed
		w.Known = true
		w.Reason = "既知"
		return w
	}
	out.Five = get(5)
	out.Fifteen = get(15)
	return out
}

// selectedDay164Decelerating は短期速度が長期速度の半分以下に弱まった順行を判定します。
//
// 引数: s(SelectedDay164Short)、side(int)。返り値: bool。bp幅ではなく実経過秒で割った速度です。
func selectedDay164Decelerating(s SelectedDay164Short, side int) bool {
	return s.Five.Known && s.Fifteen.Known && float64(side)*s.Fifteen.Speed > 0 && float64(side)*s.Five.Speed <= .5*float64(side)*s.Fifteen.Speed
}

// selectedDay164Recovered は初回延期の解除を当時の再加速だけで判定します。
//
// 引数: s(SelectedDay164Short)、side(int)。返り値: bool。
func selectedDay164Recovered(s SelectedDay164Short, side int) bool {
	return s.Five.Known && s.Fifteen.Known && float64(side)*s.Five.Speed > 0 && float64(side)*s.Fifteen.Speed >= 0 && float64(side)*s.Five.Speed >= float64(side)*s.Fifteen.Speed
}

// selectedDay164Forward は追加と再参加の短期方向一致を判定します。
//
// 引数: s(SelectedDay164Short)、side(int)。返り値: bool。未知を成立と扱いません。
func selectedDay164Forward(s SelectedDay164Short, side int) bool {
	return s.Five.Known && s.Fifteen.Known && float64(side)*s.Five.Move > 0 && float64(side)*s.Fifteen.Move > 0
}
