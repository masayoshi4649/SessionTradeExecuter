package strategy

import (
	"math"
	"testing"
)

// selectedDay164TestBoard は人工的な対称気配を作ります。
//
// 引数: reference(string)、at/mid(float64)。返り値: Board。
func selectedDay164TestBoard(reference string, at, mid float64) Board {
	return Board{Reference: reference, Time: at, Received: at, Last: mid, Bid: mid - 5, Ask: mid + 5, BidQuantity: 100, AskQuantity: 100}
}

// TestSelectedDay164ElapsedSpeed は5秒と15秒の円幅を実経過秒で割り、一定速度を失速と誤認しないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164ElapsedSpeed(t *testing.T) {
	f := &SelectedDay164Features{}
	for _, at := range []float64{0, 10, 20, 30} {
		f.OnBoard(selectedDay164TestBoard("NK225", at, 10000+at))
	}
	s := f.Short(30)
	if !s.Five.Known || !s.Fifteen.Known || s.Five.Move != 10 || s.Five.Elapsed != 10 || s.Fifteen.Move != 20 || s.Fifteen.Elapsed != 20 || s.Five.Speed != 1 || s.Fifteen.Speed != 1 {
		t.Fatalf("実経過秒の速度が不一致: %+v", s)
	}
	if selectedDay164Decelerating(s, 1) || !selectedDay164Recovered(s, 1) || !selectedDay164Forward(s, 1) {
		t.Fatal("一定の上昇速度を失速に分類しました")
	}
	s.Five.Speed = .5
	if !selectedDay164Decelerating(s, 1) || selectedDay164Recovered(s, 1) {
		t.Fatal("半分の速度という境界が正本と異なります")
	}
	s.Five.Speed, s.Fifteen.Speed = -.5, -1
	s.Five.Move, s.Fifteen.Move = -10, -20
	if !selectedDay164Decelerating(s, -1) || !selectedDay164Forward(s, -1) || selectedDay164Forward(s, 1) {
		t.Fatal("売り方向が対称ではありません")
	}
}

// TestSelectedDay164PeerStrictlyPast は同じ価格時計の相手板や未来の相手板を支持に混ぜないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164PeerStrictlyPast(t *testing.T) {
	f := &SelectedDay164Features{}
	for at := 0.; at <= 340; at++ {
		f.OnBoard(selectedDay164TestBoard("NK225", at, 10000+at))
		if at < 340 {
			f.OnBoard(selectedDay164TestBoard("TOPIX", at, 4000+at/10))
		}
	}
	before := f.Support(340, 1)
	if !before.Known || !before.Components.PeerKnown || before.PeerAt != 339 {
		t.Fatalf("厳密過去の相手が既知ではありません: %+v", before)
	}
	f.OnBoard(selectedDay164TestBoard("TOPIX", 340, 1000000))
	f.OnBoard(selectedDay164TestBoard("TOPIX", 341, 2000000))
	after := f.Support(340, 1)
	if before.Peer120 != after.Peer120 || before.M != after.M || before.T != after.T || after.PeerAt != 339 {
		t.Fatal("同時計以後の相手板が過去の支持へ混ざりました")
	}
	beforeOwn := after
	f.OnBoard(selectedDay164TestBoard("NK225", 341, 2000000))
	if beforeOwn.M != before.M || beforeOwn.T != before.T {
		t.Fatal("後続自己板が保存済み支持を変更しました")
	}
}

// TestSelectedDay164UnknownIsNotZeroMovement は価格・数量・相手の未知を区別した正本の扱いを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164UnknownIsNotZeroMovement(t *testing.T) {
	f := &SelectedDay164Features{}
	for at := 0.; at <= 340; at++ {
		b := selectedDay164TestBoard("NK225", at, 10000+at)
		b.BidQuantity, b.AskQuantity = 0, 0
		f.OnBoard(b)
	}
	s := f.Support(340, 1)
	if !s.Known || s.Components.PeerKnown || s.Components.OFIKnown || s.Peer120 != 0 || s.OFI30 != 0 {
		t.Fatalf("補助未知で自己支持まで失われたか0寄与と区別できません: %+v", s)
	}
	expectedM := math.Max(0, .45*selectedDay164Clip(s.Own120/8)+.35*selectedDay164Clip(s.Own300/16))
	if math.Abs(s.M-expectedM) > 1e-12 {
		t.Fatal("未知の相手を除いた重みを再正規化しています")
	}
	short := f.Short(340)
	if !short.Five.Known || !short.Fifteen.Known {
		t.Fatal("数量欠測で正常価格の円毎秒まで未知になりました")
	}
	if f.Support(339, 1).Known {
		t.Fatal("自己現在時計と要求時計の不一致を許可しました")
	}
}

// TestSelectedDay164MissingPathAndEndpoint は支持の両端比と厳しい短期窓を意図通り区別します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedDay164MissingPathAndEndpoint(t *testing.T) {
	f := &SelectedDay164Features{}
	for at := 0.; at <= 340; at++ {
		b := selectedDay164TestBoard("NK225", at, 10000+at)
		if at == 338 {
			b.Bid, b.Ask = 0, 0
		}
		f.OnBoard(b)
	}
	s := f.Support(340, 1)
	if !s.Known || s.Components.OFIKnown {
		t.Fatal("正本の両端価格支持と異常を含むOFIの区別が不一致です")
	}
	short := f.Short(340)
	if short.Five.Known || short.Fifteen.Known || selectedDay164Decelerating(short, 1) || selectedDay164Recovered(short, 1) || selectedDay164Forward(short, 1) {
		t.Fatal("短期窓の途中異常を失速/再加速/追撃許可へ読み替えました")
	}
	g := &SelectedDay164Features{}
	g.OnBoard(selectedDay164TestBoard("NK225", 0, 10000))
	g.OnBoard(selectedDay164TestBoard("NK225", 31, 10031))
	if g.Short(31).Five.Known || g.Short(31).Fifteen.Known {
		t.Fatal("過去端15秒許容を越えた窓を採用しました")
	}
}
