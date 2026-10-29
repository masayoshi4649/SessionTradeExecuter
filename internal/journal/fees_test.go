package journal

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// TestFeeLotsAndLegacyJournal は旧形式を改変せず、新しい手数料・返済内訳を復元します。
//
// 引数: t(*testing.T)は試験です。
// 返り値: なし。SQLite正本の費用・所有内訳を照合し、旧ファイルは生成しません。
func TestFeeLotsAndLegacyJournal(t *testing.T) {
	s, path, logs := testStore(t)
	old := testTrade("old")
	before, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(before, []byte(`"fee"`)) || bytes.Contains(before, []byte(`"closed"`)) {
		t.Fatal("旧形式に新項目を強制追加しました")
	}
	if err = s.Save(map[string]int{"state": 1}, []Trade{old}); err != nil {
		t.Fatal(err)
	}
	current := testTrade("new-close")
	current.TradeType, current.Side, current.Fee = "close", "sell", 83
	current.Quantity = 2
	current.Closed = []controller.ClosePosition{{HoldID: "old", Quantity: 2}}
	current.ClosedBasis = "口座残数照合による配賦"
	if err = s.Save(map[string]int{"state": 2}, []Trade{old, current}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(path, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var state map[string]int
	trades, err := restored.Load(&state)
	if err != nil || len(trades) != 2 || trades[0].Fee != 0 || trades[0].Closed != nil {
		t.Fatal("旧形式または費用/玉指定の復元失敗", trades, err)
	}
	currentBefore, _ := json.Marshal(current)
	currentAfter, _ := json.Marshal(trades[1])
	if !bytes.Equal(currentBefore, currentAfter) {
		t.Fatal("返済約定の再保存内容が不一致")
	}
	oldAfter, _ := json.Marshal(trades[0])
	if !bytes.Equal(before, oldAfter) {
		t.Fatal("過去約定の会計を書き換えました")
	}
	if err = restored.Save(state, trades); err != nil {
		t.Fatal("復元後の同一保存を拒否しました", err)
	}
	if !reflect.DeepEqual(trades[1].Closed, current.Closed) || trades[1].ClosedBasis != current.ClosedBasis || trades[1].Fee != 83 {
		t.Fatal("返済玉・費用のDB復元不一致")
	}
	assertLegacyLogAbsent(t, logs)
}
