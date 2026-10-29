package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCancelReservationUsesExplicitOwnershipKey は一予約の解除契約と仮想時の送信遮断を確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestCancelReservationUsesExplicitOwnershipKey(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/orders/future/reservations/owned/cancel" {
			t.Error("取消先が不正です", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["instanceId"] != "generation" {
			t.Error("controller世代が明示されていません", body, err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"ok","data":{"id":"owned","finished":false}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CancelReservation(context.Background(), "owned", "generation"); err == nil || calls != 0 {
		t.Fatal("dryrunで解除APIを呼びました")
	}
	client.SetDryRun(false)
	if err := client.CancelReservation(context.Background(), "owned", "generation"); err != nil || calls != 1 {
		t.Fatal("所有済み一予約の解除要求に失敗", err, calls)
	}
	if err := client.CancelReservation(context.Background(), "../all", "generation"); err == nil || calls != 1 {
		t.Fatal("不正な予約IDを送信しました")
	}
}

// TestCancelReservationKeepsMetacharactersInID は予約IDをクエリ等に誤解釈しないことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestCancelReservationKeepsMetacharactersInID(t *testing.T) {
	id := "owned?query#fragment%25"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/orders/future/reservations/"+id+"/cancel" || r.URL.RawQuery != "" || r.URL.Fragment != "" {
			t.Error("所有予約IDが別のURLへ変わりました", r.URL)
		}
		_, _ = w.Write([]byte(`{"status":"ok","data":{"id":"owned","finished":false}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.SetDryRun(false)
	if err := client.CancelReservation(context.Background(), id, "generation"); err != nil {
		t.Fatal(err)
	}
}
