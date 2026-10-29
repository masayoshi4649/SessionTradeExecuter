package strategy

import (
	"math"
	"sort"
)

// ResearchLevel は可視板一段の価格と数量です。価格ゼロは段の欠測を表します。
type ResearchLevel struct {
	Price, Quantity float64
}

// researchState は確定した短ATRと板平均の累積値を再開可能な形で保持します。
type researchState struct {
	Started                     bool
	Slot, Count                 int
	High, Low, Close, Previous  float64
	CurrentKnown, PreviousKnown bool
	TR                          [5]float64
	OFISum, DepthSum            float64
	OFICount, DepthCount        int64
	Band20, Band40              ResearchBand
	Band20At, Band40At          float64
	Band20Cached, Band40Cached  bool
}

// EnableResearch は00143用の45分履歴と追加特徴を有効にします。
//
// 引数: なし。返り値: なし。旧手法の公開条件と25分保持は無効時に維持します。
func (f *Features) EnableResearch() {
	f.ResearchEnabled = true
	if f.ResearchState == nil {
		f.ResearchState = map[string]*researchState{}
	}
	for _, symbol := range []string{"NK225", "TOPIX"} {
		if f.ResearchState[symbol] == nil {
			f.ResearchState[symbol] = &researchState{}
		}
	}
}

// updateResearchRow は現在足を含めない短ATRと原行順の板変化を一件追加します。
//
// 引数: b(Board)、r(*rawRow)、rows([]rawRow) は現在板・追加行・直前までの原行。
// 返り値: なし。未受信段の厚さをゼロの有効観測に置き換えません。
func (f *Features) updateResearchRow(b Board, r *rawRow, rows []rawRow) {
	f.EnableResearch()
	s := f.ResearchState[b.Reference]
	tick := f.E01.Ticks[b.Reference]
	current := int(math.Floor((b.Time - f.Session.Start) / 60))
	if !s.Started || current != s.Slot {
		if s.Started && s.CurrentKnown {
			tr := s.High - s.Low
			if s.PreviousKnown {
				tr = math.Max(tr, math.Max(math.Abs(s.High-s.Previous), math.Abs(s.Low-s.Previous)))
			}
			s.TR[s.Count%5] = tr
			s.Count++
			s.Previous, s.PreviousKnown = s.Close, true
		} else if s.Started {
			s.Count, s.PreviousKnown = 0, false
		}
		if s.Started && current-s.Slot > 1 {
			s.Count, s.PreviousKnown = 0, false
		}
		s.Started, s.Slot, s.CurrentKnown = true, current, false
	}
	if s.Count >= 3 {
		count := min(s.Count, 5)
		var total float64
		for _, tr := range s.TR[:count] {
			total += tr
		}
		r.ShortATR, r.ShortATRKnown = math.Max(2*tick, total/float64(count)), true
	}
	if r.ValidMid {
		if !s.CurrentKnown {
			s.High, s.Low = r.Mid, r.Mid
		}
		s.High, s.Low = math.Max(s.High, r.Mid), math.Min(s.Low, r.Mid)
		s.Close, s.CurrentKnown = r.Mid, true
	}
	// ----------------------------------------
	// OFIは直前原行の正常板だけと比較し、厚さは価格距離で重みを変えます。
	r.Bid, r.Ask, r.BidQuantity, r.AskQuantity = b.Bid, b.Ask, b.BidQuantity, b.AskQuantity
	if !finite(r.Bid) {
		r.Bid = 0
	}
	if !finite(r.Ask) {
		r.Ask = 0
	}
	if !finite(r.BidQuantity) {
		r.BidQuantity = 0
	}
	if !finite(r.AskQuantity) {
		r.AskQuantity = 0
	}
	r.BookQuantityKnown = r.ValidMid && finite(b.BidQuantity) && finite(b.AskQuantity) && b.BidQuantity > 0 && b.AskQuantity > 0
	if r.BookQuantityKnown && len(rows) > 0 {
		p := rows[len(rows)-1]
		if p.BookQuantityKnown {
			v := 0.0
			if b.Bid >= p.Bid {
				v += b.BidQuantity
			}
			if b.Bid <= p.Bid {
				v -= p.BidQuantity
			}
			if b.Ask <= p.Ask {
				v -= b.AskQuantity
			}
			if b.Ask >= p.Ask {
				v += p.AskQuantity
			}
			r.OFI = v / (b.BidQuantity + p.BidQuantity + b.AskQuantity + p.AskQuantity)
			r.OFIKnown = true
		}
	}
	if r.BookQuantityKnown {
		weighted := func(levels [10]ResearchLevel) float64 {
			total := 0.0
			for _, level := range levels {
				if finite(level.Price) && level.Price > 0 && finite(level.Quantity) && level.Quantity >= 0 {
					total += level.Quantity / (1 + math.Abs(level.Price-r.Mid)/tick)
				}
			}
			return total
		}
		bid, ask := weighted(b.BidLevels), weighted(b.AskLevels)
		if bid+ask > 0 {
			r.Depth, r.DepthKnown = (bid-ask)/(bid+ask), true
		}
	}
	if r.OFIKnown {
		s.OFISum += r.OFI
		s.OFICount++
	}
	if r.DepthKnown {
		s.DepthSum += r.Depth
		s.DepthCount++
	}
	r.OFISum, r.OFICount, r.DepthSum, r.DepthCount = s.OFISum, s.OFICount, s.DepthSum, s.DepthCount
}

// researchBand は確定一分終値の公開20分又は40分帯を返します。
//
// 引数: reference(string)、at(float64)、window(int) は市場・判断時計・期間分。
// 返り値: ResearchBand は高安等の9列。終値境界から60秒後だけ公開します。
func (f *Features) researchBand(reference string, at float64, window int) ResearchBand {
	var result ResearchBand
	boundary := f.Session.Start + math.Floor((at-f.Session.Start-60)/60)*60
	if boundary < f.Session.Start+float64(window+1)*60 || at-boundary >= 120 {
		return result
	}
	state := f.ResearchState[reference]
	if state != nil {
		if window == 20 && state.Band20Cached && state.Band20At == boundary {
			return state.Band20
		}
		if window == 40 && state.Band40Cached && state.Band40At == boundary {
			return state.Band40
		}
	}
	rows := f.Rows[reference]
	closes := make([]float64, window+1)
	for k := range closes {
		b := boundary - float64(window-k)*60
		j := sort.Search(len(rows), func(i int) bool { return rows[i].Time >= b }) - 1
		for j >= 0 && !rows[j].ValidMid {
			j--
		}
		if j < 0 || b-rows[j].Time > 90 {
			return result
		}
		closes[k] = rows[j].Mid
	}
	high, low := closes[0], closes[0]
	mean5, mean, path := 0.0, 0.0, 0.0
	for k, price := range closes {
		high, low = math.Max(high, price), math.Min(low, price)
		if k >= window-4 {
			mean5 += price
		}
		if k > 0 {
			mean += price
			path += math.Abs(price - closes[k-1])
		}
	}
	eff := 0.0
	if path > 0 {
		eff = math.Abs(closes[window]-closes[0]) / path
	}
	result = ResearchBand{Values: [9]float64{high, low, eff, closes[window] - closes[window-5], closes[window] - closes[0], mean5 / 5, mean / float64(window), f.OpeningMid[reference], boundary}, Known: true}
	if state != nil {
		if window == 20 {
			state.Band20, state.Band20At, state.Band20Cached = result, boundary, true
		}
		if window == 40 {
			state.Band40, state.Band40At, state.Band40Cached = result, boundary, true
		}
	}
	return result
}

// researchSet は有限性を維持したまま一列を公開します。
//
// 引数: out(*ResearchFeatures)、column(int)、value(float64)、known(bool)。返り値: なし。
func researchSet(out *ResearchFeatures, column int, value float64, known bool) {
	if known && finite(value) {
		out.Values[column], out.Known[column] = value, true
	}
}

// researchOwn は指定原行までの自己価格・短ATR・原板30秒平均だけを計算します。
//
// 引数: reference(string)、i(int) は市場と保存行添字。返り値: ResearchFeatures は相手列を含まない値です。
func (f *Features) researchOwn(reference string, i int) ResearchFeatures {
	var out ResearchFeatures
	rows := f.Rows[reference]
	if i < 0 || i >= len(rows) {
		return out
	}
	r := rows[i]
	out.Band20, out.Band40 = f.researchBand(reference, r.Time, 20), f.researchBand(reference, r.Time, 40)
	if out.Band40.Known {
		for j, v := range out.Band40.Values {
			researchSet(&out, j, v, true)
		}
		atr, known := r.ATR, finite(r.ATR) && r.ATR > 0
		if !known {
			atr, known = 2*r.ShortATR, r.ShortATRKnown
		}
		if known {
			width := math.Max(math.Max(atr, .5*(out.Values[0]-out.Values[1])), 8*f.E01.Ticks[reference])
			researchSet(&out, 9, width, true)
		}
	}
	researchSet(&out, 10, r.Mid, r.ValidMid)
	researchSet(&out, 11, r.ShortATR, r.ShortATRKnown)
	for column, seconds := range map[int]float64{12: 30, 13: 120} {
		bound := r.Time - seconds
		j := sort.Search(i+1, func(j int) bool { return rows[j].Time >= bound }) - 1
		if j >= 0 && bound-rows[j].Time <= 30 && r.ValidMid && rows[j].ValidMid && out.Known[9] {
			researchSet(&out, column, (r.Mid-rows[j].Mid)/out.Values[9], true)
		}
	}
	left := sort.Search(i+1, func(j int) bool { return rows[j].Time >= r.Time-30 })
	beforeOFI, beforeDepth := rows[left].OFISum, rows[left].DepthSum
	beforeOFICount, beforeDepthCount := rows[left].OFICount, rows[left].DepthCount
	if rows[left].OFIKnown {
		beforeOFI -= rows[left].OFI
		beforeOFICount--
	}
	if rows[left].DepthKnown {
		beforeDepth -= rows[left].Depth
		beforeDepthCount--
	}
	if count := r.OFICount - beforeOFICount; count > 0 {
		researchSet(&out, 24, (r.OFISum-beforeOFI)/float64(count), true)
	}
	if count := r.DepthCount - beforeDepthCount; count > 0 {
		researchSet(&out, 25, (r.DepthSum-beforeDepth)/float64(count), true)
	}
	return out
}

// researchFeatures は同時計の相手後続行を除いた30列と公開帯を接続します。
//
// 引数: reference(string) は現在追加した市場。返り値: ResearchFeatures は00142/00143の列順です。
func (f *Features) researchFeatures(reference string) ResearchFeatures {
	rows := f.Rows[reference]
	if len(rows) == 0 {
		return ResearchFeatures{}
	}
	now := rows[len(rows)-1].Time
	out := f.researchOwn(reference, len(rows)-1)
	peer := "NK225"
	if reference == peer {
		peer = "TOPIX"
	}
	other := f.Rows[peer]
	j := sort.Search(len(other), func(j int) bool { return other[j].Time >= now }) - 1
	if j >= 0 && now-other[j].Time <= 15 {
		p := f.researchOwn(peer, j)
		for _, pair := range [][2]int{{14, 3}, {15, 4}, {16, 2}, {17, 7}, {18, 9}, {19, 10}, {20, 12}, {21, 13}, {23, 8}, {26, 24}, {27, 25}} {
			researchSet(&out, pair[0], p.Values[pair[1]], p.Known[pair[1]])
		}
		researchSet(&out, 22, other[j].Time, true)
	}
	// ----------------------------------------
	// 20分差の相手だけは元15列履歴と同じ追加60秒公開待機を維持します。
	own20, peer20 := out.Band20, f.researchBand(peer, now-60, 20)
	if own20.Known {
		researchSet(&out, 28, own20.Values[4], true)
		out.MA20, out.MA20Known = own20.Values[6], true
	}
	if peer20.Known {
		researchSet(&out, 29, peer20.Values[4], true)
	}
	return out
}
