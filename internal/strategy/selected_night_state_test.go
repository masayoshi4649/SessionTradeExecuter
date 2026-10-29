// 00165凍結正本から識別子と同一パッケージ参照だけを移した夜間板判断です。
package strategy

import "testing"

// selectedNightArtificialState は手計算した窓を使って状態遷移を検査します。
//
// 引数: index(int64)、bid/ask(float64)、buy(bool)。返り値: selectedNightFeature。
func selectedNightArtificialState(index int64, bid, ask float64, buy bool) selectedNightFeature {
	d := 1.0
	if !buy {
		d = -1
	}
	fraction := 1.0
	if !buy {
		fraction = 0
	}
	b := selectedNightBand{Known: true, QuantityKnown: true, BidMove: d * 20, AskMove: d * 20, GrossPriceMove: 40, UpFraction: fraction}
	return selectedNightFeature{Current: selectedNightFrame{Index: index, Line: index + 1, Time: 100, Bid: bid, Ask: ask}, Changed: true, Event8: b, Clock15: b}
}

// TestSelectedNightStateTwoLaterEvents は未来二更新を先取りしない買売鏡像を確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedNightStateTwoLaterEvents(t *testing.T) {
	for _, side := range []int{1, -1} {
		s := &selectedNightRepricing{}
		d := float64(side)
		for i := int64(0); i <= 2; i++ {
			bid := 1000 + d*float64(i)*10
			b := selectedNightBookTest(100, bid, bid+10)
			v := s.stepSelectedNight(selectedNightArtificialState(i, b.Bid, b.Ask, side > 0), b)
			if (i < 2 && v.ConfirmedSide != 0) || (i == 2 && v.ConfirmedSide != side) {
				t.Fatalf("先取り/不足 %+v", v)
			}
		}
	}
}

// TestSelectedNightStatePressureHoldsAndNewPullback は数量だけの逆圧力で降りず、新しい押戻りを要求します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedNightStatePressureHoldsAndNewPullback(t *testing.T) {
	s := &selectedNightRepricing{}
	for i := int64(0); i <= 2; i++ {
		b := selectedNightBookTest(100, 1000+float64(i)*10, 1010+float64(i)*10)
		s.stepSelectedNight(selectedNightArtificialState(i, b.Bid, b.Ask, true), b)
	}
	b := selectedNightBookTest(100, 1020, 1030)
	b.AskQuantity = 1000
	f := selectedNightArtificialState(3, b.Bid, b.Ask, true)
	f.Changed = false
	v := s.stepSelectedNight(f, b)
	if !v.Buy.Active || v.RetestID != 0 {
		t.Fatal("数量だけで価格帯を破棄")
	}
	for i, bid := range []float64{1010, 1020, 1030} {
		b = selectedNightBookTest(100, bid, bid+10)
		v = s.stepSelectedNight(selectedNightArtificialState(int64(i+4), b.Bid, b.Ask, true), b)
		if i < 2 && v.RetestID != 0 {
			t.Fatal("押戻り後の二更新先取り")
		}
	}
	if v.RetestSide != 1 || v.RetestID != 1 {
		t.Fatalf("新波:%+v", v)
	}
	b = selectedNightBookTest(100, 1040, 1050)
	v = s.stepSelectedNight(selectedNightArtificialState(8, b.Bid, b.Ask, true), b)
	if v.RetestID != 0 {
		t.Fatal("同波を再発火")
	}
}

// TestSelectedNightStateUnknownReset は不正板/実端点未知をゼロ圧力として使いません。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedNightStateUnknownReset(t *testing.T) {
	s := &selectedNightRepricing{}
	b := selectedNightBookTest(100, 1000, 1010)
	f := selectedNightArtificialState(0, 1000, 1010, true)
	f.Event8.Known = false
	s.stepSelectedNight(f, b)
	if s.Candidates[0].Active {
		t.Fatal("未知を新準備")
	}
	s.stepSelectedNight(selectedNightArtificialState(1, 1010, 1020, true), selectedNightBookTest(100, 1010, 1020))
	s.stepSelectedNight(selectedNightFeature{}, selectedNightBookTest(100, 1010, 1010))
	if s.Candidates[0].Active || s.Holds[0].Active {
		t.Fatal("不正板を越えた状態")
	}
}
