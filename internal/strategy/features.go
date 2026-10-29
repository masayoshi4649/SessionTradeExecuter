package strategy

import (
	"encoding/json"
	"errors"
	"math"
	"sort"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/e01"
)

// Candle は確定足の共通型です。
type Candle = e01.Candle

// Board は参照ラージの一件の板です。Time は原価格時計、Received は受信時計です。
type Board struct {
	Reference                                                string
	Time, Received, Last, Bid, Ask, BidQuantity, AskQuantity float64
	BidLevels, AskLevels                                     [10]ResearchLevel
}

type rawRow struct {
	Time, Last, Mid, ATR                                   float64
	ValidLast, ValidMid                                    bool
	Bid, Ask, BidQuantity, AskQuantity                     float64
	ShortATR                                               float64
	ShortATRKnown, BookQuantityKnown, OFIKnown, DepthKnown bool
	OFI, Depth, OFISum, DepthSum                           float64
	OFICount, DepthCount                                   int64
}

// Features は確定足系と厳密な原行窓を別々に保持します。
type Features struct {
	SelectedDay       SelectedDay164Features
	SelectedNight     selectedNightBookFeatures
	SelectedRepricing selectedNightRepricing
	Version           int                       `json:"version"`
	Session           Session                   `json:"session"`
	E01               *e01.Features             `json:"e01"`
	Rows              map[string][]rawRow       `json:"rows"`
	Counts            map[string]int64          `json:"counts"`
	OpeningLast       map[string]float64        `json:"opening_last"`
	OpeningMid        map[string]float64        `json:"opening_mid"`
	ResearchEnabled   bool                      `json:"research_enabled"`
	ResearchState     map[string]*researchState `json:"research_state,omitempty"`
}

// NewFeatures は二市場の因果的な特徴量器を作ります。
//
// 引数: session(Session)、ticks(map[string]float64) はラージ規格、cfg(e01.FeatureConfig) は公開条件。
// 返り値: *Features と設定検査の error です。
func NewFeatures(session Session, ticks map[string]float64, cfg e01.FeatureConfig) (*Features, error) {
	base, err := e01.NewFeatures(session.ID, session.Start, ticks, cfg)
	if err != nil {
		return nil, err
	}
	return &Features{Version: 1, Session: session, E01: base, Rows: map[string][]rawRow{"TOPIX": {}, "NK225": {}}, Counts: map[string]int64{}, OpeningLast: map[string]float64{}, OpeningMid: map[string]float64{}}, nil
}

// AddCandles は当時取得できた確定足だけを追加します。
//
// 引数: reference(string)、interval(int) は秒、rows([]Candle)、received(float64) は受信秒。
// 返り値: int は追加数、error は未来足や修正足の検出です。
func (f *Features) AddCandles(reference string, interval int, rows []Candle, received float64) (int, error) {
	return f.E01.AddCandles(reference, interval, rows, received)
}

// OnBoard は原板を追加し、同時刻相手行を参照しない観測を返します。
//
// 引数: b(Board) はラージ板。返り値: Observation と時計・市場不正の error です。
func (f *Features) OnBoard(b Board) (Observation, error) {
	rows, ok := f.Rows[b.Reference]
	if !ok || !finite(b.Time) || !finite(b.Received) || b.Time < f.Session.Start || b.Received < b.Time {
		return Observation{}, errors.New("原板の市場又は時計が不正です")
	}
	if len(rows) > 0 && b.Time < rows[len(rows)-1].Time {
		return Observation{}, errors.New("原板の価格時計が逆行しました")
	}
	base, err := f.E01.OnBook(b.Reference, e01.Book{QuoteTime: b.Time, Price: b.Last, Bid: b.Bid, Ask: b.Ask, BidQty: b.BidQuantity, AskQty: b.AskQuantity}, b.Received)
	if err != nil {
		return Observation{}, err
	}
	good := finite(b.Bid) && finite(b.Ask) && b.Bid > 0 && b.Ask > b.Bid
	r := rawRow{Time: b.Time, ATR: base.ATR, ValidLast: finite(b.Last) && b.Last > 0, ValidMid: good}
	if r.ValidLast {
		r.Last = b.Last
		if f.OpeningLast[b.Reference] == 0 {
			f.OpeningLast[b.Reference] = b.Last
		}
	}
	if good {
		r.Mid = (b.Bid + b.Ask) * .5
		if f.OpeningMid[b.Reference] == 0 {
			f.OpeningMid[b.Reference] = r.Mid
		}
	}
	if f.ResearchEnabled {
		f.updateResearchRow(b, &r, rows)
	}
	f.Rows[b.Reference] = append(rows, r)
	o := Observation{Reference: b.Reference, Time: b.Time, Received: b.Received, Index: f.Counts[b.Reference], Last: r.Last, Bid: b.Bid, Ask: b.Ask, ATR: base.ATR, QuoteOK: good, E01: base, GateDirection: f.E01.RearmDirection(b.Reference, b.Received)}
	f.Counts[b.Reference]++
	// ----------------------------------------
	// 旧手法は25分、00143は40分帯・公開待機・欠測猶予を含む45分を保存します。
	retention := 1500.0
	if f.ResearchEnabled {
		retention = 2700
	}
	keep := sort.Search(len(f.Rows[b.Reference]), func(i int) bool { return f.Rows[b.Reference][i].Time >= b.Time-retention })
	if keep > 0 {
		f.Rows[b.Reference] = f.Rows[b.Reference][keep:]
	}
	if !finite(o.Bid) {
		o.Bid = 0
	}
	if !finite(o.Ask) {
		o.Ask = 0
	}
	o.DayMarket = f.market(b.Reference, false, false)
	o.LastMarket = f.market(b.Reference, false, true)
	o.MidMarket = f.market(b.Reference, true, true)
	o.LastHistory = f.history(b.Reference, b.Time, false)
	o.MidHistory = f.history(b.Reference, b.Time, true)
	if f.ResearchEnabled {
		o.Research = f.researchFeatures(b.Reference)
		f.SelectedDay.OnBoard(b)
		if b.Reference == "NK225" {
			o.Selected.DayBuy = f.SelectedDay.Support(b.Time, 1)
			o.Selected.DaySell = f.SelectedDay.Support(b.Time, -1)
			o.Selected.DayShort = f.SelectedDay.Short(b.Time)
			feature := f.SelectedNight.addSelectedNight(b, o.Index, o.Index)
			o.Selected.Night = f.SelectedRepricing.stepSelectedNight(feature, b)
			o.Selected.NightAvailable, o.Selected.NightIndex, o.Selected.NightTime = true, o.Index, o.Time
		}
	}
	return o, nil
}

// rowPrice は指定経路の価格と有効性を返します。
//
// 引数: r(rawRow)、mid(bool)。返り値: float64 は価格、bool は有効性です。
func rowPrice(r rawRow, mid bool) (float64, bool) {
	if mid {
		return r.Mid, r.ValidMid
	}
	return r.Last, r.ValidLast
}

// change は窓端の等値採否を保って過去変化を計算します。
//
// 引数: rows([]rawRow)、i(int)、seconds(float64)、mid/strict(bool)。
// 返り値: 変化・基準時計(float64) と有効性(bool) です。
func change(rows []rawRow, i int, seconds float64, mid, strict bool) (float64, float64, bool) {
	if i < 0 || rows[i].ATR <= 0 {
		return 0, 0, false
	}
	now := rows[i]
	px, ok := rowPrice(now, mid)
	if !ok {
		return 0, 0, false
	}
	bound := now.Time - seconds
	j := sort.Search(i+1, func(k int) bool {
		if strict {
			return rows[k].Time >= bound
		}
		return rows[k].Time > bound
	}) - 1
	if j < 0 || bound-rows[j].Time > 30 {
		return 0, 0, false
	}
	old, valid := rowPrice(rows[j], mid)
	if !valid {
		return 0, 0, false
	}
	return (px - old) / now.ATR, rows[j].Time, true
}

// market は厳密過去の相手原行だけを組み合わせます。
//
// 引数: reference(string)、mid/strict(bool)。返り値: Market は正規化変化です。
func (f *Features) market(reference string, mid, strict bool) Market {
	peer := "NK225"
	if reference == "NK225" {
		peer = "TOPIX"
	}
	own, other := f.Rows[reference], f.Rows[peer]
	i := len(own) - 1
	if i < 0 {
		return Market{}
	}
	j := sort.Search(len(other), func(k int) bool { return other[k].Time >= own[i].Time }) - 1
	m := Market{OwnATR: own[i].ATR}
	var a, b bool
	m.Own, a = rowPrice(own[i], mid)
	m.Own30, m.Own30Time, m.Own30Known = change(own, i, 30, mid, strict)
	m.Own120, _, m.Own120Known = change(own, i, 120, mid, strict)
	if j < 0 {
		return m
	}
	m.Peer, b = rowPrice(other[j], mid)
	m.PeerATR = other[j].ATR
	m.PeerTime = other[j].Time
	m.Known = a && b && m.OwnATR > 0 && m.PeerATR > 0 && own[i].Time-other[j].Time <= 15
	m.Peer30, m.Peer30Time, m.Peer30Known = change(other, j, 30, mid, strict)
	m.Peer120, _, m.Peer120Known = change(other, j, 120, mid, strict)
	return m
}

// minute は公開境界までの有限な 21 終値から履歴を作ります。
//
// 引数: rows([]rawRow)、boundary(float64)、mid(bool)。
// 返り値: [11]float64 は履歴、bool は全窓が既知かです。
func (f *Features) minute(rows []rawRow, boundary float64, mid bool) ([11]float64, bool) {
	var result [11]float64
	if boundary < f.Session.Start+21*60 {
		return result, false
	}
	var closes [21]float64
	for k := 0; k < 21; k++ {
		b := boundary - float64(20-k)*60
		i := sort.Search(len(rows), func(i int) bool { return rows[i].Time >= b }) - 1
		for i >= 0 {
			value, valid := rowPrice(rows[i], mid)
			if valid {
				if b-rows[i].Time > 90 {
					return result, false
				}
				closes[k] = value
				break
			}
			i--
		}
		if i < 0 {
			return result, false
		}
	}
	open := 0.0
	for _, r := range rows {
		v, valid := rowPrice(r, mid)
		if valid {
			open = v
			break
		}
	}
	if open <= 0 {
		return result, false
	}
	var mean5, mean20, path, variance float64
	high, low := closes[0], closes[0]
	for i, v := range closes {
		if i >= 16 {
			mean5 += v / 5
		}
		if i >= 1 {
			mean20 += v / 20
			path += math.Abs(v - closes[i-1])
		}
		high = math.Max(high, v)
		low = math.Min(low, v)
	}
	for _, v := range closes[1:] {
		variance += (v - mean20) * (v - mean20) / 20
	}
	eff := 0.0
	if path > 0 {
		eff = math.Abs(closes[20]-closes[0]) / path
	}
	result = [11]float64{mean5, mean20, math.Sqrt(variance), high, low, eff, closes[20] - closes[15], closes[20] - closes[0], open, boundary, closes[20] - open}
	return result, true
}

// history は自己 60 秒、相手追加 60 秒の公開待機を適用します。
//
// 引数: reference(string)、at(float64)、mid(bool)。返り値: History は既知フラグ付き履歴です。
func (f *Features) history(reference string, at float64, mid bool) History {
	peer := "NK225"
	if reference == "NK225" {
		peer = "TOPIX"
	}
	b := f.Session.Start + math.Floor((at-f.Session.Start-60)/60)*60
	own, ok := f.minute(f.Rows[reference], b, mid)
	other, pk := f.minute(f.Rows[peer], b-60, mid)
	ownOpening, peerOpening := f.OpeningLast[reference], f.OpeningLast[peer]
	if mid {
		ownOpening = f.OpeningMid[reference]
		peerOpening = f.OpeningMid[peer]
	}
	if ownOpening > 0 {
		own[10] += own[8] - ownOpening
		own[8] = ownOpening
	}
	if peerOpening > 0 {
		other[10] += other[8] - peerOpening
		other[8] = peerOpening
	}
	h := History{Known: ok && pk && at-own[9] < 120 && at-other[9] < 180}
	if !h.Known {
		return h
	}
	copy(h.Values[:10], own[:10])
	copy(h.Values[10:], []float64{other[6], other[7], other[5], other[10], other[9]})
	return h
}

// Dump は特徴の観測接頭と公開時計を保存します。
//
// 引数: なし。返り値: []byte は JSON、error は保存失敗です。
func (f *Features) Dump() ([]byte, error) { return json.Marshal(f) }

// RestoreFeatures はモデルキャッシュを再計算できる特徴状態を復元します。
//
// 引数: raw([]byte)。返り値: *Features と復元検査の error です。
func RestoreFeatures(raw []byte) (*Features, error) {
	var f Features
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	if f.Version != 1 || f.E01 == nil || f.Counts == nil || f.OpeningLast == nil || f.OpeningMid == nil {
		return nil, errors.New("特徴状態の版が不正です")
	}
	b, err := json.Marshal(f.E01)
	if err != nil {
		return nil, err
	}
	f.E01, err = e01.RestoreFeatures(b)
	return &f, err
}

// Evidence は非有限値を含まない監査 JSON を返します。
//
// 引数: なし。返り値: json.RawMessage と変換の error です。
func (o Observation) Evidence() (json.RawMessage, error) { return json.Marshal(o) }
