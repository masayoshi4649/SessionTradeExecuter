// 00165凍結正本から識別子と同一パッケージ参照だけを移した夜間板判断です。
package strategy

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

// TestSelectedNightFeatureRestore は同時計途中の保存復元で後続の状態が変わらないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。観測済みprefixだけを保存し、後続板を両者へ同じ順に渡します。
func TestSelectedNightFeatureRestore(t *testing.T) {
	type persisted struct {
		Features selectedNightBookFeatures
		State    selectedNightRepricing
	}
	a, b := &persisted{}, &persisted{}
	for i := 0; i < 80; i++ {
		bid := 1000 + float64(i/5)*10 + float64(i%3)*10
		board := selectedNightBookTest(100+float64(i/20), bid, bid+10)
		board.BidQuantity += float64(i % 7)
		board.AskQuantity += float64((i + 3) % 9)
		feature := a.Features.addSelectedNight(board, int64(i), int64(i+1))
		view := a.State.stepSelectedNight(feature, board)
		if i == 39 {
			data, err := json.Marshal(a)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(data, b); err != nil {
				t.Fatal(err)
			}
		} else if i > 39 {
			other := b.Features.addSelectedNight(board, int64(i), int64(i+1))
			otherView := b.State.stepSelectedNight(other, board)
			if !reflect.DeepEqual(feature, other) || !reflect.DeepEqual(view, otherView) {
				t.Fatalf("同時計の保存復元で状態が変化しました: 原行%d", i)
			}
		}
	}
}

// selectedNightBookTest は同価格対応を検査する人工十段板を作ります。
//
// 引数: at/bid/ask(float64)。返り値: Board。
func selectedNightBookTest(at, bid, ask float64) Board {
	b := Board{Reference: "NK225", Time: at, Received: at, Last: (bid + ask) / 2, Bid: bid, Ask: ask, BidQuantity: 10, AskQuantity: 10}
	for i := 0; i < 10; i++ {
		b.BidLevels[i] = ResearchLevel{Price: bid - float64(i)*10, Quantity: 10}
		b.AskLevels[i] = ResearchLevel{Price: ask + float64(i)*10, Quantity: 10}
	}
	return b
}

// TestSelectedNightFeatureSameClockPrefix は同時計の後続板を先取りしないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedNightFeatureSameClockPrefix(t *testing.T) {
	h := &selectedNightBookFeatures{}
	out := []selectedNightFeature{}
	for i := 0; i < 10; i++ {
		out = append(out, h.addSelectedNight(selectedNightBookTest(100, 1000+float64(i)*10, 1010+float64(i)*10), int64(i), int64(i+1)))
	}
	if !out[8].Event8.Known || out[8].Event8.BidMove != 80 || out[8].Event8.Elapsed != 0 {
		t.Fatalf("同時計順: %+v", out[8])
	}
	h2 := &selectedNightBookFeatures{}
	for i := 0; i <= 8; i++ {
		v := h2.addSelectedNight(selectedNightBookTest(100, 1000+float64(i)*10, 1010+float64(i)*10), int64(i), int64(i+1))
		if !reflect.DeepEqual(v, out[i]) {
			t.Fatal("prefix差")
		}
	}
	h.addSelectedNight(selectedNightBookTest(100, 3000, 3010), 10, 11)
	if out[8].Current.Bid != 1080 {
		t.Fatal("過去の参照を変更")
	}
}

// TestSelectedNightFeatureSeparateWidening は片側板引きと両気配移動を分離します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedNightFeatureSeparateWidening(t *testing.T) {
	h := &selectedNightBookFeatures{}
	for i := 0; i <= 8; i++ {
		h.addSelectedNight(selectedNightBookTest(float64(i), 1000, 1010+float64(i)*10), int64(i), int64(i+1))
	}
	b := h.selectedNightEvent(8)
	if b.BidMove != 0 || b.AskMove != 80 || b.AskOnlyUp != 8 || b.SpreadChange != 80 {
		t.Fatalf("片側:%+v", b)
	}
}

// TestSelectedNightFeatureBreaksUnknown は不正板と長い空白で連続性を切ります。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedNightFeatureBreaksUnknown(t *testing.T) {
	h := &selectedNightBookFeatures{}
	for i := 0; i <= 8; i++ {
		h.addSelectedNight(selectedNightBookTest(float64(i), 1000+float64(i)*10, 1010+float64(i)*10), int64(i), int64(i+1))
	}
	h.addSelectedNight(selectedNightBookTest(9, 1100, 1100), 9, 10)
	v := h.addSelectedNight(selectedNightBookTest(10, 1110, 1120), 10, 11)
	if v.Event8.Known || v.InvalidRows != 1 {
		t.Fatal("不正板を接続")
	}
	v = h.addSelectedNight(selectedNightBookTest(71, 1200, 1210), 11, 12)
	if v.Event8.Known || v.LongGaps != 1 {
		t.Fatal("61秒空白を接続")
	}
}

// TestSelectedNightFeatureQuantitiesAndDepth は同価格の数量だけを回復代理に数えます。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedNightFeatureQuantitiesAndDepth(t *testing.T) {
	h := &selectedNightBookFeatures{}
	a := selectedNightBookTest(100, 1000, 1010)
	h.addSelectedNight(a, 0, 1)
	b := a
	b.AskQuantity = 30
	b.AskLevels[0].Quantity = 30
	v := h.addSelectedNight(b, 1, 2)
	if v.Changed || v.Current.PriceEvents != 0 || v.Current.AskRefill != .5 {
		t.Fatal("価格と数量を混同")
	}
	for i := 2; i < 102; i++ {
		h.addSelectedNight(b, int64(i), int64(i+1))
	}
	if h.Frames[len(h.Frames)-1].QuantityEvents != 1 {
		t.Fatal("同値板で分母が膨張")
	}
	shift := selectedNightBookTest(101, 1010, 1020)
	u, d, k := selectedNightCommonDepth(a.BidLevels, shift.BidLevels)
	if !k || u != 0 || d != 0 {
		t.Fatalf("段ずれを増減と誤認 %v %v %v", u, d, k)
	}
	b = selectedNightBookTest(102, 1020, 1030)
	b.BidQuantity = math.Inf(1)
	v = h.addSelectedNight(b, 102, 103)
	if v.Current.BadQuantity != 1 {
		t.Fatal("無限数量を既知")
	}
}

// TestSelectedNightFeatureClockEndpoint は過去端を包含し同時計の未来を参照しません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedNightFeatureClockEndpoint(t *testing.T) {
	h := &selectedNightBookFeatures{}
	h.addSelectedNight(selectedNightBookTest(100, 1000, 1010), 0, 1)
	h.addSelectedNight(selectedNightBookTest(100, 1010, 1020), 1, 2)
	v := h.addSelectedNight(selectedNightBookTest(115, 1030, 1040), 2, 3)
	if !v.Clock15.Known || v.Clock15.StartIndex != 1 || v.Clock15.BidMove != 20 || v.Clock15.Elapsed != 15 {
		t.Fatalf("端点:%+v", v.Clock15)
	}
	v = h.addSelectedNight(selectedNightBookTest(146, 1040, 1050), 3, 4)
	if v.Clock15.Known {
		t.Fatal("過去端16秒遅れ")
	}
}
