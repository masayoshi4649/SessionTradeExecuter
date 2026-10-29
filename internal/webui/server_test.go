package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

// TestHTTPControlsRequireSameOriginToken は不正なブラウザ要求が新規許可を変えないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。HTTPも売買処理も試験内だけで完結します。
func TestHTTPControlsRequireSameOriginToken(t *testing.T) {
	m, err := NewManager(testConfig(t), func(ctx context.Context, _ config.Config, _ *runner.Control, _ func(runner.Snapshot)) error {
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	handler, err := NewHandler(m)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err = json.NewDecoder(response.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(state.Products) != 5 || state.CSRFToken == "" {
		t.Fatal("状態取得が不正です")
	}
	for _, test := range []struct {
		name, token, origin, host, body string
		status                          int
	}{
		{"トークンなし", "", "", "", `{"enabled":true}`, 403},
		{"別サイト", state.CSRFToken, "http://external.invalid", "", `{"enabled":true}`, 403},
		{"項目不足", state.CSRFToken, "", "", `{}`, 400},
		{"追加項目", state.CSRFToken, "", "", `{"enabled":true,"price":1}`, 400},
		{"二重本文", state.CSRFToken, "", "", `{"enabled":true}{}`, 400},
		{"正常ON", state.CSRFToken, server.URL, "", `{"enabled":true}`, 200},
		{"正常OFF", state.CSRFToken, server.URL, "", `{"enabled":false}`, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/products/NK225mini/enabled", strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-CSRF-Token", test.token)
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if test.host != "" {
				req.Host = test.host
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != test.status {
				raw, _ := io.ReadAll(res.Body)
				t.Fatalf("状態%d: %s", res.StatusCode, raw)
			}
			if test.status != 200 && m.State().Products[1].Enabled {
				t.Fatal("拒否要求で新規を許可しました")
			}
		})
	}
	res, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	text, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(text), "日経225") || res.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("埋込画面または制限ヘッダーがありません")
	}
}

// TestLANAccessByAddressAndHostname は別PCからの画面取得と商品ON/OFFを検査します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。
// IPアドレスとDNS名の両方を使い、売買担当は外部へ通信しない試験用関数へ置き換えます。
func TestLANAccessByAddressAndHostname(t *testing.T) {
	m, err := NewManager(testConfig(t), func(ctx context.Context, _ config.Config, control *runner.Control, _ func(runner.Snapshot)) error {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if control.DetachStopRequested() {
					return nil
				}
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	handler, err := NewHandler(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"10.10.100.1:8090", "trader:8090", "trader.lan:8090", "[fd00::1]:8090"} {
		t.Run(host, func(t *testing.T) {
			origin := "http://" + host
			var state State
			for _, path := range []string{"/", "/api/state"} {
				req := httptest.NewRequest(http.MethodGet, origin+path, nil)
				req.RemoteAddr = "10.10.100.20:51000"
				req.Header.Set("Sec-Fetch-Site", "cross-site")
				res := httptest.NewRecorder()
				handler.ServeHTTP(res, req)
				if res.Code != http.StatusOK {
					t.Fatalf("別PCからの取得失敗 %s: %d %s", path, res.Code, res.Body.String())
				}
				if path == "/api/state" {
					if err := json.Unmarshal(res.Body.Bytes(), &state); err != nil || state.CSRFToken == "" {
						t.Fatal("操作用トークンを取得できません", err)
					}
				}
			}
			for _, enabled := range []bool{true, false} {
				body, _ := json.Marshal(map[string]bool{"enabled": enabled})
				req := httptest.NewRequest(http.MethodPost, origin+"/api/products/NK225mini/enabled", strings.NewReader(string(body)))
				req.RemoteAddr = "10.10.100.20:51000"
				req.Header.Set("Origin", origin)
				req.Header.Set("Sec-Fetch-Site", "same-origin")
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-CSRF-Token", state.CSRFToken)
				res := httptest.NewRecorder()
				handler.ServeHTTP(res, req)
				if res.Code != http.StatusOK || m.State().Products[1].Enabled != enabled {
					t.Fatalf("別PCからの操作失敗 enabled=%t: %d %s", enabled, res.Code, res.Body.String())
				}
				if !enabled {
					until(t, func() bool { return !m.State().Products[1].Pending })
				}
			}
			req := httptest.NewRequest(http.MethodPost, origin+"/api/products/NK225mini/enabled", strings.NewReader(`{"enabled":true}`))
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			req.Header.Set("X-CSRF-Token", state.CSRFToken)
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != http.StatusForbidden || m.State().Products[1].Enabled {
				t.Fatal("別サイトからの操作で新規許可が変わりました")
			}
		})
	}
}
