package strategy

import (
	"bytes"
	"fmt"
	"testing"
)

// selectedTestPortfolio は外部通信なしで商品上限を指定した選定手法を作ります。
//
// 引数: t(*testing.T)、method(string)、cap(int)。返り値: *Portfolio。
func selectedTestPortfolio(t *testing.T, method string, cap int) *Portfolio {
	t.Helper()
	kind := "Night"
	if method == "D164_15" {
		kind = "Day"
	}
	params, err := ParametersForMethod("NK225", kind, method)
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{Namespace: "人工選定", Reference: "NK225", Product: "NK225mini", CoreQuantity: 1, ExtraMaxQuantity: 1, Cap: cap, LargeTick: 10, PointValue: 100, Parameters: params}, Session{ID: "人工_" + kind, Kind: kind, Start: 1000, ForceClose: 47000, End: 48000, USOpen: 20000, ExtraAt: 21800})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// selectedTestNew は人工の実注文を元IDへ対応させて予約します。
//
// 引数: t(*testing.T)、p(*Portfolio)、side/micro(int)、source(string)。返り値: *Signal。
func selectedTestNew(t *testing.T, p *Portfolio, side, micro int, source string) *Signal {
	t.Helper()
	parent := fmt.Sprintf("core/%s", source)
	p.Selected.SourceLots[source+"-玉"] = &selectedSourceLot{SourceID: source, Parent: parent, Owner: "core", Campaign: 1, Side: side, RemainingMicro: micro}
	p.Selected.SourceOrder = append(p.Selected.SourceOrder, source+"-玉")
	o := p05TestObservation(0, 1100, 60000, 60010)
	s, err := p.emitSelected(o, SelectedIntent{Parent: parent, SourceID: source, Owner: "core", Campaign: 1, Side: side, QuantityMicro: micro, Kind: "initial", Reason: "人工新規"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSelectedFixedSourceAndRoundTrip は実上限が変わっても研究源のmini10配分を保つことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedFixedSourceAndRoundTrip(t *testing.T) {
	for _, method := range []string{"D164_15", "P164_06", "U165_03"} {
		for _, cap := range []int{1, 3, 10, 11, 100} {
			t.Run(fmt.Sprintf("%s/%d", method, cap), func(t *testing.T) {
				p := selectedTestPortfolio(t, method, cap)
				shadow := p.Selected.Shadow
				if shadow.Config.Cap != 10 || shadow.Config.CoreQuantity != 5 || shadow.Config.ExtraMaxQuantity != 5 || shadow.Config.Product != "NK225mini" {
					t.Fatal("固定元配分が変化")
				}
				raw, err := p.Dump()
				if err != nil {
					t.Fatal(err)
				}
				restored, err := Restore(raw)
				if err != nil {
					t.Fatal(err)
				}
				again, _ := restored.Dump()
				if !bytes.Equal(raw, again) {
					t.Fatal("保存復元で判断状態が変化")
				}
			})
		}
	}
}

// TestSelectedReservationAndOwnedFill は片方向上限、端数切捨て、実建値、所有返済の拒否を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedReservationAndOwnedFill(t *testing.T) {
	p := selectedTestPortfolio(t, "P164_06", 11)
	buy := selectedTestNew(t, p, 1, 33, "買源")
	sell := selectedTestNew(t, p, -1, 100, "売源")
	if buy.Quantity != 3 || sell.Quantity != 11 {
		t.Fatal("実商品の整数配分が不正")
	}
	extra := selectedTestNew(t, p, 1, 100, "追加源")
	if extra.Quantity != 8 {
		t.Fatal("未約定予約を控除していません")
	}
	if selectedTestNew(t, p, 1, 100, "満杯源") != nil {
		t.Fatal("上限を超過しました")
	}
	before, _ := p.Selected.Shadow.Dump()
	f := Fill{SignalID: buy.ID, ExecutionID: "約定1", Quantity: 2, Price: 60023, Time: 1101, HoldID: "口座玉1", Fee: 77}
	if err := p.OnFill(f); err != nil {
		t.Fatal(err)
	}
	if err := p.OnFill(f); err != nil {
		t.Fatal(err)
	}
	f.ExecutionID = "約定2"
	f.Quantity = 1
	f.Price = 60028
	f.Fee = 38
	if err := p.OnFill(f); err != nil {
		t.Fatal(err)
	}
	if err := p.OnOrderDone(buy.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := p.Selected.Shadow.Dump()
	if !bytes.Equal(before, after) {
		t.Fatal("実約定で元手法が変化")
	}
	if len(p.Selected.Lots) != 2 || p.Selected.Lots[0].Entry != 60023 || p.Selected.Lots[1].Entry != 60028 {
		t.Fatal("部分約定の建値を混ぜました")
	}
	close, err := p.emitSelected(p05TestObservation(1, 1102, 60040, 60050), SelectedIntent{ActualID: "約定1", Quantity: 2, Kind: "policy_close", Reason: "人工返済"})
	if err != nil || close == nil {
		t.Fatal(err)
	}
	if close.Targets[0].ID != "口座玉1" || close.Quantity != 2 {
		t.Fatal("返済先が不正")
	}
	bad := Fill{SignalID: close.ID, ExecutionID: "誤返済", Quantity: 1, Price: 60040, Time: 1103, Closed: []LotTarget{{ID: "手動玉", Quantity: 1}}}
	state, _ := p.Dump()
	if p.OnFill(bad) == nil {
		t.Fatal("手動玉を返済対象にしました")
	}
	same, _ := p.Dump()
	if !bytes.Equal(state, same) {
		t.Fatal("拒否した約定で実会計が変化")
	}
	bad.ExecutionID = "正返済"
	bad.Closed = []LotTarget{{ID: "口座玉1", Quantity: 1}}
	if err := p.OnFill(bad); err != nil {
		t.Fatal(err)
	}
	if p.Selected.Lots[0].Realized != 1700 || p.Selected.Lots[0].Quantity != 1 {
		t.Fatal("実約定の建値で損益計算していません")
	}
}

// TestSelectedLateFillAndCloseRetry は全返済後の遅着と取消後の残玉再送を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSelectedLateFillAndCloseRetry(t *testing.T) {
	p := selectedTestPortfolio(t, "P164_06", 10)
	s := selectedTestNew(t, p, 1, 50, "源")
	p.Flatten(1101, "時間境界")
	if err := p.OnFill(Fill{SignalID: s.ID, ExecutionID: "遅着", Quantity: 2, Price: 60000, Time: 1102, HoldID: "自己玉"}); err != nil {
		t.Fatal(err)
	}
	if err := p.OnOrderDone(s.ID); err != nil {
		t.Fatal(err)
	}
	out := p.Flatten(1103, "時間境界")
	if len(out) != 1 || out[0].Quantity != 2 {
		t.Fatal("遅着を返済しません")
	}
	if err := p.OnFill(Fill{SignalID: out[0].ID, ExecutionID: "半返済", Quantity: 1, Price: 59990, Time: 1104, Closed: out[0].Targets[:0]}); err != nil {
		t.Fatal(err)
	}
	if err := p.OnOrderDone(out[0].ID); err != nil {
		t.Fatal(err)
	}
	retry := p.Flatten(1105, "時間境界")
	if len(retry) != 1 || retry[0].Quantity != 1 {
		t.Fatal("取消後に返済希望を忘れました")
	}
	raw, _ := p.Dump()
	if _, err := Restore(raw); err != nil {
		t.Fatal(err)
	}
}
