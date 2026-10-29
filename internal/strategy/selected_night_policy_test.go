package strategy

import (
	"encoding/json"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// selectedNightTestPolicy は規定の実Exprを持つ人工用の夜間政策を作ります。
//
// 引数: t(*testing.T)、method(string)。返り値: *selectedNightPolicyと*rules.Runtime。
func selectedNightTestPolicy(t *testing.T, method string) (*selectedNightPolicy, *rules.Runtime) {
	t.Helper()
	n, err := newSelectedNightPolicy(method)
	if err != nil {
		t.Fatal(err)
	}
	profile := "p164_06"
	if method == "NK225_U165_03" {
		profile = "u165_03"
	}
	r, err := rules.NewRuntimeFor(profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	return n, r
}

// selectedNightTestObservation は指定した返済側含み幅の正常ラージ観測を作ります。
//
// 引数: side(int)、entry/gain/at(float64)、index(int64)。返り値: Observation。
func selectedNightTestObservation(side int, entry, gain, at float64, index int64) Observation {
	mark := entry + float64(side)*gain
	if side > 0 {
		return Observation{Reference: "NK225", Time: at, Received: at, Index: index, Last: mark + 5, Bid: mark, Ask: mark + 10, QuoteOK: true}
	}
	return Observation{Reference: "NK225", Time: at, Received: at, Index: index, Last: mark - 5, Bid: mark - 10, Ask: mark, QuoteOK: true}
}

// TestSelectedNightSmallInitialAndPendingNew は買売鏡像の小量保護と新規予約中の継続を検査します。
//
// 引数: t(*testing.T)。返り値: なし。元返済予約は優先し、新規予約だけなら旧玉の保護を止めません。
func TestSelectedNightSmallInitialAndPendingNew(t *testing.T) {
	for _, side := range []int{1, -1} {
		n, runtime := selectedNightTestPolicy(t, "NK225_P164_06")
		o := selectedNightTestObservation(side, 1000, 0, 100, 10)
		if _, err := n.sourceNewSelectedNight("core/1", "源1", side, 20, o, runtime); err != nil {
			t.Fatal(err)
		}
		lot := selectedNightLotView{ID: "実1", Parent: "core/1", SourceID: "源1", Kind: "initial", Side: side, RemainingMicro: 20, Entry: 1000, BestGain: 30}
		o = selectedNightTestObservation(side, 1000, 30, 101, 11)
		intent, err := n.protectSelectedNight(lot, o, false, true, runtime)
		if err != nil || intent.QuantityMicro != 0 || !n.Lots[lot.ID].Activated || n.Lots[lot.ID].Floor != 10 {
			t.Fatalf("起動と床: %+v %v", intent, err)
		}
		// ----------------------------------------
		// 当行の元追加予約後も既存玉は判断します。元返済予約があれば送ります。
		o = selectedNightTestObservation(side, 1000, 10, 102, 12)
		added, err := n.sourceNewSelectedNight("core/1", "源2", side, 20, o, runtime)
		if err != nil || added.Kind != "source_add" || added.QuantityMicro != 20 {
			t.Fatal("元追加の識別", added, err)
		}
		intent, err = n.protectSelectedNight(lot, o, true, true, runtime)
		if err != nil || intent.QuantityMicro != 0 {
			t.Fatal("元返済予約に割込", intent, err)
		}
		intent, err = n.protectSelectedNight(lot, o, false, true, runtime)
		if err != nil || intent.QuantityMicro != 20 || intent.ActualID != "実1" || intent.Reason != "protect_10trail20" {
			t.Fatal("新規予約だけで保護停止", intent, err)
		}
	}
}

// TestSelectedNightPartialFillOwnership は部分約定ごとの建値と資格対象時点を検査します。
//
// 引数: t(*testing.T)。返り値: なし。同じ原行の後順の新規約定へ資格を先渡ししません。
func TestSelectedNightPartialFillOwnership(t *testing.T) {
	n, runtime := selectedNightTestPolicy(t, "NK225_P164_06")
	o := selectedNightTestObservation(1, 1000, 100, 100, 10)
	_, err := n.sourceNewSelectedNight("core/1", "源1", 1, 50, o, runtime)
	if err != nil {
		t.Fatal(err)
	}
	old := selectedNightLotView{ID: "先の部分約定", Parent: "core/1", SourceID: "源1", Kind: "initial", Side: 1, RemainingMicro: 30, Entry: 1000, BestGain: 100}
	late := selectedNightLotView{ID: "後の部分約定", Parent: "core/1", SourceID: "源1", Kind: "initial", Side: 1, RemainingMicro: 20, Entry: 1050, BestGain: 0}
	qualified := n.qualifySelectedNight("core/1", "source_partial/70", 3000, 30, o, []selectedNightLotView{old})
	if len(qualified) != 1 || qualified[0] != old.ID || n.Lots[late.ID] != nil {
		t.Fatal("資格を後続実約定へ先渡し", qualified)
	}
	// ----------------------------------------
	// 資格前の最高益100を遡及起動へ使わず、今の返済側幅が10へ戻るまで待ちます。
	o = selectedNightTestObservation(1, 1000, -10, 101, 11)
	intent, err := n.protectSelectedNight(old, o, false, true, runtime)
	if err != nil || intent.QuantityMicro != 0 || n.Lots[old.ID].Activated {
		t.Fatal("資格前MFEの遡及", intent, err)
	}
	o = selectedNightTestObservation(1, 1000, 10, 102, 12)
	_, err = n.protectSelectedNight(old, o, false, true, runtime)
	if err != nil || !n.Lots[old.ID].Activated || n.Lots[old.ID].Floor != 0 {
		t.Fatal("残玉の建値床", err)
	}
	o = selectedNightTestObservation(1, 1000, 0, 103, 13)
	intent, err = n.protectSelectedNight(old, o, false, true, runtime)
	if err != nil || intent.QuantityMicro != 30 || intent.Reason != "protect_residual_floor" {
		t.Fatal("残玉返済数量", intent, err)
	}
	intent, err = n.protectSelectedNight(late, o, false, true, runtime)
	if err != nil || intent.QuantityMicro != 0 || n.Lots[late.ID].ResidualEligible {
		t.Fatal("後順の実玉へ旧資格が漏れた", intent, err)
	}
}

// TestSelectedNightSourceAddStopAndStale は追撃だけの50円停止と古板非発火を検査します。
//
// 引数: t(*testing.T)。返り値: なし。初回大玉へ新しい50円停止を加えません。
func TestSelectedNightSourceAddStopAndStale(t *testing.T) {
	for _, side := range []int{1, -1} {
		n, runtime := selectedNightTestPolicy(t, "NK225_P164_06")
		o := selectedNightTestObservation(side, 1000, -50, 100, 10)
		_, err := n.sourceNewSelectedNight("addon/1", "源1", side, 30, o, runtime)
		if err != nil {
			t.Fatal(err)
		}
		lot := selectedNightLotView{ID: "初回", Parent: "addon/1", SourceID: "源1", Kind: "initial", Side: side, RemainingMicro: 30, Entry: 1000}
		intent, err := n.protectSelectedNight(lot, o, false, true, runtime)
		if err != nil || intent.QuantityMicro != 0 {
			t.Fatal("担当名addonをSource追撃と誤認", intent, err)
		}
		lot.ID, lot.Kind = "追撃", "source_add"
		intent, err = n.protectSelectedNight(lot, o, false, false, runtime)
		if err != nil || intent.QuantityMicro != 0 {
			t.Fatal("古板の新規保護注文", intent, err)
		}
		intent, err = n.protectSelectedNight(lot, o, false, true, runtime)
		if err != nil || intent.QuantityMicro != 30 || intent.Reason != "source_add_stop50" {
			t.Fatal("追撃50円停止", intent, err)
		}
	}
}

// TestSelectedNightU03DelayBounds は待機期限・新状態・現在元残量の全境界を検査します。
//
// 引数: t(*testing.T)。返り値: なし。30秒ちょうどは解除可、超過・同原行・古板・予約中は解除しません。
func TestSelectedNightU03DelayBounds(t *testing.T) {
	for _, side := range []int{1, -1} {
		n, runtime := selectedNightTestPolicy(t, "NK225_U165_03")
		n.View.Known = true
		opp := &n.View.Sell
		if side < 0 {
			opp = &n.View.Buy
		}
		opp.Active, opp.QuantitySupport = true, true
		o := selectedNightTestObservation(side, 1000, 0, 100, 10)
		intent, err := n.sourceNewSelectedNight("core/1", "源1", side, 50, o, runtime)
		if err != nil || intent.QuantityMicro != 0 || !n.Parents["core/1"].Deferred {
			t.Fatal("反対帯の初回待機", intent, err)
		}
		same := &n.View.Buy
		if side < 0 {
			same = &n.View.Sell
		}
		same.Active, same.Index = true, 10
		o = selectedNightTestObservation(side, 1000, 0, 130, 11)
		intent, err = n.releaseSelectedNight("core/1", 30, false, true, 1000, o, runtime)
		if err != nil || intent.QuantityMicro != 0 {
			t.Fatal("待機前と同じ帯で解除", intent, err)
		}
		same.Index = 11
		for _, test := range []struct{ pending, fresh bool }{{true, true}, {false, false}} {
			intent, err = n.releaseSelectedNight("core/1", 30, test.pending, test.fresh, 1000, o, runtime)
			if err != nil || intent.QuantityMicro != 0 {
				t.Fatal("予約中又は古板で解除", intent, err)
			}
		}
		intent, err = n.releaseSelectedNight("core/1", 30, false, true, 1000, o, runtime)
		if err != nil || intent.QuantityMicro != 30 || intent.SourceID != "源1" || intent.Kind != "delayed_initial" {
			t.Fatal("元部分返済後の残量と期限境界", intent, err)
		}
		n.finishReleaseSelectedNight("core/1", 30)
		if n.Parents["core/1"].Deferred || n.Parents["core/1"].Cancelled {
			t.Fatal("解除の重複又は誤取消")
		}
	}
}

// TestSelectedNightU03CancelNoResurrection は期限切れと源消滅後の同親追加復活を禁止します。
//
// 引数: t(*testing.T)。返り値: なし。初回の特徴未知は元通知を保ち、新しい別親は通常開始できます。
func TestSelectedNightU03CancelNoResurrection(t *testing.T) {
	for _, sourceDead := range []bool{false, true} {
		n, runtime := selectedNightTestPolicy(t, "NK225_U165_03")
		n.View = selectedNightStateView{Known: true, Sell: selectedNightHold{Active: true, QuantitySupport: true}}
		o := selectedNightTestObservation(1, 1000, 0, 100, 10)
		_, err := n.sourceNewSelectedNight("core/1", "源1", 1, 50, o, runtime)
		if err != nil {
			t.Fatal(err)
		}
		if sourceDead {
			n.sourceRemainingSelectedNight("core/1", "源1", 0)
		} else {
			o.Time, o.Index = 130.01, 11
			_, err = n.releaseSelectedNight("core/1", 50, false, true, 1000, o, runtime)
			if err != nil {
				t.Fatal(err)
			}
		}
		intent, err := n.sourceNewSelectedNight("core/1", "源2", 1, 20, o, runtime)
		if err != nil || intent.QuantityMicro != 0 || !n.Parents["core/1"].Cancelled {
			t.Fatal("取消親を元追加で復活", intent, err)
		}
		n.View = selectedNightStateView{}
		intent, err = n.sourceNewSelectedNight("core/2", "源3", 1, 50, o, runtime)
		if err != nil || intent.QuantityMicro != 50 || intent.Kind != "initial" {
			t.Fatal("別親又は未知fallbackを拒否", intent, err)
		}
		data, err := json.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		var restored selectedNightPolicy
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		if !restored.Parents["core/1"].Cancelled || restored.Parents["core/2"].Cancelled {
			t.Fatal("保存復元で取消親が変化")
		}
	}
}

// TestSelectedNightActualQuantityAndInjectedRow は非整除上限の実返済と当原行への特徴接続を検査します。
//
// 引数: t(*testing.T)。返り値: なし。1実玉の返済をmicro整数へ丸めて0枚にしません。
func TestSelectedNightActualQuantityAndInjectedRow(t *testing.T) {
	n, runtime := selectedNightTestPolicy(t, "NK225_P164_06")
	o := selectedNightTestObservation(1, 1000, 10, 100, 10)
	o.Selected.NightAvailable = true
	o.Selected.NightIndex, o.Selected.NightTime = o.Index, o.Time
	if err := n.injectSelectedNightView(o); err != nil {
		t.Fatal(err)
	}
	future := o
	future.Index = 11
	future.Selected.NightIndex = 12
	if err := n.injectSelectedNightView(future); err == nil {
		t.Fatal("同時計の未来原行を注入")
	}
	_, err := n.sourceNewSelectedNight("core/1", "源1", 1, 20, o, runtime)
	if err != nil {
		t.Fatal(err)
	}
	lot := selectedNightLotView{ID: "実1", Parent: "core/1", SourceID: "源1", Kind: "initial", Side: 1, RemainingActual: 1, Cap: 7, RemainingMicro: 14, Entry: 1000, BestGain: 10}
	_, err = n.protectSelectedNight(lot, o, false, true, runtime)
	if err != nil {
		t.Fatal(err)
	}
	o = selectedNightTestObservation(1, 1000, 0, 101, 11)
	intent, err := n.protectSelectedNight(lot, o, false, true, runtime)
	if err != nil || intent.QuantityActual != 1 || intent.ActualID != "実1" {
		t.Fatal("非整除上限の実返済", intent, err)
	}
}
