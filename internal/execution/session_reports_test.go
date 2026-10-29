package execution

import (
	"context"
	"path/filepath"
	"testing"
)

// TestTradeMethodIDSurvivesFillsAndRestart は注文時の手法IDを新規・返済別に確定保存します。
//
// 引数: t(*testing.T)は仮想約定試験です。
// 返り値: なし。再起動時にも当時のIDが残り、商品名や担当名で置き換わりません。
func TestTradeMethodIDSurvivesFillsAndRestart(t *testing.T) {
	m, path := setup(t, true, "NK225mini")
	api := &fakeAPI{}
	entry := testIntent("method-entry", "buy", "new", 2, 65000)
	entry.MethodID = "NK225_P164_06"
	if _, err := m.Submit(context.Background(), api, entry, testView()); err != nil {
		t.Fatal(err)
	}
	acknowledge(t, m)
	closeIntent := testIntent("method-close", "sell", "close", 2, 65010)
	closeIntent.MethodID = "NK225_U165_03"
	if _, err := m.Submit(context.Background(), api, closeIntent, testView()); err != nil {
		t.Fatal(err)
	}
	acknowledge(t, m)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Join(filepath.Dir(path), "log"), path, true, "NK225mini", 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	records := reopened.Records()
	if len(records) != 2 || records[0].MethodID != entry.MethodID || records[1].MethodID != closeIntent.MethodID || api.calls != 0 {
		t.Fatal("当時の手法IDが復元されません", records)
	}
}
