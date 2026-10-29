package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDefaultDryRunBlocksEveryMutation は仮想指定が通信前に全変更操作を遮断することを確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestDefaultDryRunBlocksEveryMutation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"Result":0,"OrderId":"mock"}`))
	}))
	defer server.Close()
	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = client.SendFuture(ctx, BrokerRequest{}); err == nil {
		t.Fatal("既定値で実発注を許しました")
	}
	if _, err = client.CancelFuture(ctx, "a"); err == nil {
		t.Fatal("仮想時に取消を許しました")
	}
	if _, err = client.request(ctx, "POST", "/api/v1/orders/future/reservations", nil, Request{}); err == nil {
		t.Fatal("仮想時に予約変更を許しました")
	}
	if calls != 0 {
		t.Fatal("禁止要求を送信しました")
	}
	client.SetDryRun(false)
	if _, err = client.SendFuture(ctx, BrokerRequest{}); err != nil || calls != 1 {
		t.Fatalf("明示実行の模擬送信に失敗: %v", err)
	}
	client.SetDryRun(true)
	_, _ = client.CancelFuture(ctx, "a")
	if calls != 1 {
		t.Fatal("再度仮想へ戻した後に送信しました")
	}
}

// TestRawUnknownFieldsAndGeneration は観測原文の欠落と世代の混同を防ぎます。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestRawUnknownFieldsAndGeneration(t *testing.T) {
	data := []byte(`{"Symbol":"x","CurrentPriceTime":null,"NewField":{"巨数":9876543210123456789},"Buy1":{"Price":101,"Qty":3}}`)
	var board Board
	if err := json.Unmarshal(data, &board); err != nil || string(board.Raw) != string(data) {
		t.Fatalf("生板原文が残りません: %v", err)
	}
	data[0] = 'x'
	if board.Raw[0] != '{' {
		t.Fatal("元バッファを書き換えると台帳根拠も変わります")
	}
	first := Snapshot{SessionID: "2026-10-20_Day", Generation: "one", Code: "TOPIX", AsOf: time.Now()}
	second := first
	second.Code = "NK225"
	if err := ValidateSnapshots(first, second); err != nil {
		t.Fatal(err)
	}
	second.Generation = "two"
	if err := ValidateSnapshots(first, second); err == nil {
		t.Fatal("同場のcontroller再起動を見落としました")
	}
	second = first
	second.SessionID = "2026-10-20_Night"
	if err := ValidateSnapshots(first, second); err == nil {
		t.Fatal("異なる場の履歴を混ぜました")
	}
}
