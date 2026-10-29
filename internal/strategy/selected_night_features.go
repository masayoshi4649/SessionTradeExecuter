// 00165凍結正本から識別子と同一パッケージ参照だけを移した夜間板判断です。
package strategy

import (
	"math"
	"sort"
)

type selectedNightFrame struct {
	Index, Line                                              int64
	Time, Bid, Ask                                           float64
	PriceEvents, QuantityEvents, BadQuantity                 int
	Up, Down, BidOnlyUp, AskOnlyDown, AskOnlyUp, BidOnlyDown float64
	BidRefill, AskRefill, BidDeplete, AskDeplete             float64
	CommonBidUp, CommonAskUp, CommonBidDown, CommonAskDown   float64
	CommonDepthEvents                                        int
}

type selectedNightBand struct {
	Known, QuantityKnown                                                       bool
	StartIndex, Index, StartLine, Line                                         int64
	StartTime, Time, Elapsed                                                   float64
	Events, QuantityEvents, BadQuantity, CommonDepthEvents                     int
	BidMove, AskMove, MidMove, Spread, SpreadChange                            float64
	UpFraction, GrossPriceMove, BidOnlyUp, AskOnlyDown, AskOnlyUp, BidOnlyDown float64
	BidRefill, AskRefill, BidDeplete, AskDeplete                               float64
	CommonBidUp, CommonAskUp, CommonBidDown, CommonAskDown                     float64
}

type selectedNightFeature struct {
	Current                       selectedNightFrame
	Changed                       bool
	Event8, Event32, Clock15      selectedNightBand
	Resets, InvalidRows, LongGaps int
}

type selectedNightBookFeatures struct {
	Frames                        []selectedNightFrame
	PricePoints                   []int
	Previous                      Board
	HasPrevious                   bool
	Resets, InvalidRows, LongGaps int
}

// selectedNightFinite は有限数かを返します。
//
// 引数: x(float64)。返り値: bool。NaNや無限大を既知の数量へ変換しません。
func selectedNightFinite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// selectedNightValidBook は正常な価格気配かを検査します。
//
// 引数: b(Board)。返り値: bool。数量欠測とは分離します。
func selectedNightValidBook(b Board) bool {
	return selectedNightFinite(b.Bid) && selectedNightFinite(b.Ask) && b.Bid > 0 && b.Ask > b.Bid
}

// selectedNightValidQuantity は最良両側数量の既知性を検査します。
//
// 引数: b(Board)。返り値: bool。表示0と欠測を勝手に有効化しません。
func selectedNightValidQuantity(b Board) bool {
	return selectedNightFinite(b.BidQuantity) && selectedNightFinite(b.AskQuantity) && b.BidQuantity > 0 && b.AskQuantity > 0
}

// selectedNightCommonDepth は共通して見える同価格だけの正負数量差を算出します。
//
// 引数: old/current([10]ResearchLevel)。返り値: 正増分、負増分(float64)、既知(bool)。
// 段外へ出た価格の数量を0にせず、段番号の移動を補充に数えません。
func selectedNightCommonDepth(old, current [10]ResearchLevel) (float64, float64, bool) {
	m := map[float64]float64{}
	for _, x := range old {
		if !selectedNightFinite(x.Price) || !selectedNightFinite(x.Quantity) || x.Price <= 0 || x.Quantity < 0 {
			return 0, 0, false
		}
		if _, exists := m[x.Price]; exists {
			return 0, 0, false
		}
		m[x.Price] = x.Quantity
	}
	up, down, denom := 0.0, 0.0, 0.0
	seen := map[float64]bool{}
	for _, x := range current {
		if !selectedNightFinite(x.Price) || !selectedNightFinite(x.Quantity) || x.Price <= 0 || x.Quantity < 0 || seen[x.Price] {
			return 0, 0, false
		}
		seen[x.Price] = true
		if q, exists := m[x.Price]; exists {
			d := x.Quantity - q
			up += math.Max(0, d)
			down += math.Max(0, -d)
			denom += q + x.Quantity
		}
	}
	if denom <= 0 {
		return 0, 0, false
	}
	return up / denom, down / denom, true
}

// addSelectedNight は現在の物理原行だけから価格イベントと同価格数量を更新します。
//
// 引数: b(Board)、index/line(int64)。返り値: selectedNightFeature。
// 同時計の価格更新を処理し、不正板又は60秒超の時計空白では接続を切ります。
func (h *selectedNightBookFeatures) addSelectedNight(b Board, index, line int64) selectedNightFeature {
	if !selectedNightValidBook(b) {
		h.InvalidRows++
		h.Resets++
		h.HasPrevious = false
		h.Frames = nil
		h.PricePoints = nil
		return selectedNightFeature{Resets: h.Resets, InvalidRows: h.InvalidRows, LongGaps: h.LongGaps}
	}
	if h.HasPrevious && (b.Time < h.Previous.Time || b.Time-h.Previous.Time > 60) {
		h.LongGaps++
		h.Resets++
		h.HasPrevious = false
		h.Frames = nil
		h.PricePoints = nil
	}
	f := selectedNightFrame{Index: index, Line: line, Time: b.Time, Bid: b.Bid, Ask: b.Ask}
	changed := false
	if h.HasPrevious {
		f = h.Frames[len(h.Frames)-1]
		f.Index = index
		f.Line = line
		f.Time = b.Time
		f.Bid = b.Bid
		f.Ask = b.Ask
		p := h.Previous
		db, da := b.Bid-p.Bid, b.Ask-p.Ask
		changed = db != 0 || da != 0
		if changed {
			f.PriceEvents++
			f.Up += math.Max(db, 0) + math.Max(da, 0)
			f.Down += math.Max(-db, 0) + math.Max(-da, 0)
		}
		if db > 0 && da <= 0 {
			f.BidOnlyUp++
		}
		if da < 0 && db >= 0 {
			f.AskOnlyDown++
		}
		if da > 0 && db <= 0 {
			f.AskOnlyUp++
		}
		if db < 0 && da >= 0 {
			f.BidOnlyDown++
		}
		if selectedNightValidQuantity(b) && selectedNightValidQuantity(p) {
			if b.Bid != p.Bid || b.Ask != p.Ask || b.BidQuantity != p.BidQuantity || b.AskQuantity != p.AskQuantity {
				f.QuantityEvents++
				if b.Bid == p.Bid {
					d := (b.BidQuantity - p.BidQuantity) / (b.BidQuantity + p.BidQuantity)
					f.BidRefill += math.Max(d, 0)
					f.BidDeplete += math.Max(-d, 0)
				}
				if b.Ask == p.Ask {
					d := (b.AskQuantity - p.AskQuantity) / (b.AskQuantity + p.AskQuantity)
					f.AskRefill += math.Max(d, 0)
					f.AskDeplete += math.Max(-d, 0)
				}
			}
		} else {
			f.BadQuantity++
		}
		if p.BidLevels != b.BidLevels || p.AskLevels != b.AskLevels {
			bu, bd, bk := selectedNightCommonDepth(p.BidLevels, b.BidLevels)
			au, ad, ak := selectedNightCommonDepth(p.AskLevels, b.AskLevels)
			if bk && ak && (bu+bd+au+ad > 0) {
				f.CommonDepthEvents++
				f.CommonBidUp += bu
				f.CommonBidDown += bd
				f.CommonAskUp += au
				f.CommonAskDown += ad
			}
		}
	}
	h.Frames = append(h.Frames, f)
	if !h.HasPrevious || changed {
		h.PricePoints = append(h.PricePoints, len(h.Frames)-1)
	}
	h.Previous = b
	h.HasPrevious = true
	return selectedNightFeature{Current: f, Changed: changed, Event8: h.selectedNightEvent(8), Event32: h.selectedNightEvent(32), Clock15: h.selectedNightClock(15), Resets: h.Resets, InvalidRows: h.InvalidRows, LongGaps: h.LongGaps}
}

// selectedNightBetween は既知の二原行の差から窓内の価格応答を返します。
//
// 引数: a/z(selectedNightFrame)。返り値: selectedNightBand。量の平均は最良価格又は数量が変化した有効更新を分母にします。
func selectedNightBetween(a, z selectedNightFrame) selectedNightBand {
	b := selectedNightBand{Known: true, StartIndex: a.Index, Index: z.Index, StartLine: a.Line, Line: z.Line, StartTime: a.Time, Time: z.Time, Elapsed: z.Time - a.Time,
		Events: z.PriceEvents - a.PriceEvents, QuantityEvents: z.QuantityEvents - a.QuantityEvents, BadQuantity: z.BadQuantity - a.BadQuantity, CommonDepthEvents: z.CommonDepthEvents - a.CommonDepthEvents,
		BidMove: z.Bid - a.Bid, AskMove: z.Ask - a.Ask, MidMove: (z.Bid + z.Ask - a.Bid - a.Ask) / 2, Spread: z.Ask - z.Bid, SpreadChange: z.Ask - z.Bid - a.Ask + a.Bid,
		GrossPriceMove: z.Up + z.Down - a.Up - a.Down, BidOnlyUp: z.BidOnlyUp - a.BidOnlyUp, AskOnlyDown: z.AskOnlyDown - a.AskOnlyDown, AskOnlyUp: z.AskOnlyUp - a.AskOnlyUp, BidOnlyDown: z.BidOnlyDown - a.BidOnlyDown}
	if b.GrossPriceMove > 0 {
		b.UpFraction = (z.Up - a.Up) / b.GrossPriceMove
	}
	b.QuantityKnown = b.BadQuantity == 0
	if b.QuantityEvents > 0 {
		n := float64(b.QuantityEvents)
		b.BidRefill = (z.BidRefill - a.BidRefill) / n
		b.AskRefill = (z.AskRefill - a.AskRefill) / n
		b.BidDeplete = (z.BidDeplete - a.BidDeplete) / n
		b.AskDeplete = (z.AskDeplete - a.AskDeplete) / n
	}
	if b.CommonDepthEvents > 0 {
		n := float64(b.CommonDepthEvents)
		b.CommonBidUp = (z.CommonBidUp - a.CommonBidUp) / n
		b.CommonAskUp = (z.CommonAskUp - a.CommonAskUp) / n
		b.CommonBidDown = (z.CommonBidDown - a.CommonBidDown) / n
		b.CommonAskDown = (z.CommonAskDown - a.CommonAskDown) / n
	}
	return b
}

// selectedNightEvent は現在までの指定価格遷移数を持つ窓を返します。
//
// 引数: n(int)。返り値: selectedNightBand。60秒を越えた古い窓は未知です。
func (h *selectedNightBookFeatures) selectedNightEvent(n int) selectedNightBand {
	if len(h.PricePoints) <= n || len(h.Frames) == 0 {
		return selectedNightBand{}
	}
	z := h.Frames[len(h.Frames)-1]
	a := h.Frames[h.PricePoints[len(h.PricePoints)-n-1]]
	if z.Time-a.Time > 60 {
		return selectedNightBand{}
	}
	return selectedNightBetween(a, z)
}

// selectedNightClock は目標時計以前の最後正常行を過去端とします。
//
// 引数: seconds(float64)。返り値: selectedNightBand。過去端の15秒超遅れは未知です。
func (h *selectedNightBookFeatures) selectedNightClock(seconds float64) selectedNightBand {
	if len(h.Frames) == 0 {
		return selectedNightBand{}
	}
	z := h.Frames[len(h.Frames)-1]
	target := z.Time - seconds
	i := sort.Search(len(h.Frames), func(i int) bool { return h.Frames[i].Time > target }) - 1
	if i < 0 || target-h.Frames[i].Time > 15 {
		return selectedNightBand{}
	}
	return selectedNightBetween(h.Frames[i], z)
}
