package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// testClient は通信を模擬サーバーだけに接続します。
// 引数: t *testing.T 検証制御、handler http.HandlerFunc 模擬API。
// 返り値: *Client 検証用通信処理。
func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := New(s.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDryRun(false)
	return c
}

// TestReadContracts は登録・履歴・板・注文の実API外形と null 時刻を検証します。
// 引数: t *testing.T 検証制御。
// 返り値: なし。
func TestReadContracts(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("読取に実注文が混入: %s", r.Method)
		}
		switch r.URL.Path {
		case "/api/v1/sys/registrations/future":
			fmt.Fprint(w, `{"status":"ok","data":{"state":"succeeded","futures":[{"futureCode":"NK225micro","symbol":"161120023","exchange":2,"derivMonth":202612}]}}`)
		case "/api/v1/orders/future":
			if r.URL.Query().Get("scope") != "all" {
				t.Error("scope 不一致")
			}
			fmt.Fprint(w, `{"status":"ok","data":{"instanceId":"abc","session":null,"connected":false,"positionsStale":true,"brokerOrders":[{"ID":"order1","Details":[{"RecType":8,"ExecutionID":"fill1","Qty":2,"Price":67000}]}]}}`)
		case "/api/v1/cache/board":
			fmt.Fprint(w, `[{"Symbol":"161120023","CurrentPrice":null,"CurrentPriceTime":null,"Buy1":{"Price":67000,"Qty":2},"Sell1":{"Price":67005,"Qty":3}}]`)
		case "/api/v1/history/NK225/candles/5min":
			if r.URL.Query().Get("from") != "2026-10-02T17:00:00+09:00" || r.URL.Query().Get("generation") != "old" {
				t.Error("履歴の範囲符号化不一致")
			}
			fmt.Fprint(w, `{"snapshot":{"sessionId":"2026-10-02_Night","generation":"new","code":"NK225","session":{"startsAt":"2026-10-02T17:00:00+09:00","endsAt":"2026-10-03T06:00:00+09:00"}},"data":[{"time":"2026-10-02T17:00:00+09:00","open":67000,"high":67100,"low":66900,"close":67050,"closed":true}]}`)
		default:
			t.Errorf("想定外URL: %s", r.URL)
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	reg, err := c.Registrations(ctx)
	if err != nil || len(reg.Futures) != 1 || reg.Futures[0].FutureCode != "NK225micro" {
		t.Fatalf("登録取得: %+v %v", reg, err)
	}
	orders, err := c.Orders(ctx, "all")
	if err != nil || orders.Session != nil || !orders.PositionsStale || orders.BrokerOrders[0].Details[0].ExecutionID != "fill1" {
		t.Fatalf("注文取得: %+v %v", orders, err)
	}
	boards, err := c.Boards(ctx)
	if err != nil || len(boards) != 1 || boards[0].CurrentPriceTime != "" || boards[0].Buy1.Price != 67000 {
		t.Fatalf("板取得: %+v %v", boards, err)
	}
	from, _ := time.Parse(time.RFC3339, "2026-10-02T17:00:00+09:00")
	candles, err := c.Candles(ctx, "NK225", "5min", "old", from, time.Time{})
	if err != nil || candles.Snapshot.Generation != "new" || !candles.Data[0].Closed {
		t.Fatalf("足取得: %+v %v", candles, err)
	}
}

// TestMutationsAreNeverRetried は注文の曖昧な受付を再発注しないことを検証します。
// 引数: t *testing.T 検証制御。
// 返り値: なし。
func TestMutationsAreNeverRetried(t *testing.T) {
	for _, test := range []struct {
		name    string
		code    int
		body    string
		unknown bool
	}{{"HTTP拒否", 400, `{"error":"数量競合"}`, false}, {"送信結果不明", 502, `{"error":"上流切断"}`, true}, {"本文破損", 200, `{"status":`, true}, {"受付ID欠落", 201, `{"Result":0}`, true}, {"転送禁止", 307, `{}`, true}} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" {
					t.Error("POSTでない")
				}
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(test.code)
				fmt.Fprint(w, test.body)
			})
			_, err := c.SendFuture(context.Background(), BrokerRequest{})
			var failure *APIError
			if !errors.As(err, &failure) || failure.Unknown != test.unknown {
				t.Fatalf("失敗分類: %v %+v", err, failure)
			}
			if calls.Load() != 1 {
				t.Fatalf("危険な再発注または転送: %d", calls.Load())
			}
		})
	}
}

// TestRawMicroOrder は非SORマイクロ先物の本文と応答の必須項目を検証します。
// 引数: t *testing.T 検証制御。
// 返り値: なし。
func TestRawMicroOrder(t *testing.T) {
	var posted BrokerRequest
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/redirect/sendorder/future" || r.Method != "POST" {
			t.Error("透過先物URL不一致")
		}
		if r.Header.Get("X-API-KEY") != "" {
			t.Error("ローカルクライアントがAPIキーを再定義した")
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `{"Result":0,"OrderId":"broker-1"}`)
	})
	req := BrokerRequest{Symbol: "161120023", Exchange: 24, TradeType: 1, TimeInForce: 2, Side: "2", Qty: 20, FrontOrderType: 120}
	result, err := c.SendFuture(context.Background(), req)
	if err != nil || result.OrderID != "broker-1" || posted.Exchange != 24 || posted.Qty != 20 {
		t.Fatalf("micro 受付不一致: %+v %+v %v", result, posted, err)
	}
	for _, body := range []string{`{}`, `{"Result":null}`, `{"Result":0}`, `{"Result":0,"OrderId":null}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		_, err := c.SendFuture(context.Background(), req)
		var failure *APIError
		if !errors.As(err, &failure) || !failure.Unknown {
			t.Errorf("不完全受付を成功扱い: %s %v", body, err)
		}
	}
}

// TestBookCompleteness は切替・中断による不完全な履歴を採用しないことを検証します。
// 引数: t *testing.T 検証制御。
// 返り値: なし。
func TestBookCompleteness(t *testing.T) {
	for _, complete := range []string{"true", "false", ""} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Trailer", "X-History-Complete")
			w.Header().Set("X-History-Session-Id", "2026-10-02_Night")
			w.Header().Set("X-History-Generation", "generation")
			w.Header().Set("X-History-Code", "NK225")
			w.Header().Set("X-History-As-Of", "2026-10-03T03:00:00+09:00")
			w.Header().Set("X-History-Sequence", "11")
			fmt.Fprintln(w, `{"Symbol":"161120018","CurrentPrice":67000}`)
			w.Header().Set("X-History-Complete", complete)
		})
		data, err := c.Book(context.Background(), "NK225", time.Time{}, time.Time{})
		if complete == "true" {
			if err != nil || len(data.Data) != 1 || data.Snapshot.Sequence != 11 {
				t.Fatalf("完結履歴: %+v %v", data, err)
			}
		} else if err == nil || len(data.Data) != 0 {
			t.Fatalf("未完結履歴を採用: %s %+v %v", complete, data, err)
		}
	}
}

// TestStreamCancellation は生PUSHを受信した後の取消で待機を解消します。
// 引数: t *testing.T 検証制御。
// 返り値: なし。
func TestStreamCancellation(t *testing.T) {
	upgrader := websocket.Upgrader{}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/redirect/websocket" {
			t.Error("WS URL不一致")
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"Symbol":"161120018","CurrentPrice":67000,"CurrentPriceTime":null}`))
		_, _, _ = conn.ReadMessage()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	count := 0
	err := c.Stream(ctx, func(b Board) error {
		count++
		if b.Symbol != "161120018" {
			t.Error("銘柄不一致")
		}
		cancel()
		return nil
	})
	if count != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("WS中断不一致: %d %v", count, err)
	}
}

// TestInputValidation は通信前の不正URL・履歴コードを拒否します。
// 引数: t *testing.T 検証制御。
// 返り値: なし。
func TestInputValidation(t *testing.T) {
	for _, u := range []string{"file:///tmp/a", "http://user:secret@host", "http://host?key=secret"} {
		if _, err := New(u, time.Second); err == nil {
			t.Errorf("不正URLを受付: %s", strings.Split(u, "?")[0])
		}
	}
	c, _ := New("http://127.0.0.1:1", time.Second)
	if _, err := c.Candles(context.Background(), "../orders", "5min", "", time.Time{}, time.Time{}); err == nil {
		t.Error("不正履歴商品を受付")
	}
}
