package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

// apiRequest はJSONのREST要求をテスト用ハンドラーへ送り、外部通信を行わず応答を取得します。
//
// 引数: t(*testing.T)、handler(http.Handler)、method/path/token(string)、body(any)は送信内容です。
// 返り値: *httptest.ResponseRecorderはHTTP状態と本文です。
func apiRequest(t *testing.T, handler http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "http://trader.lan:8090"+path, bytes.NewReader(raw))
	req.RemoteAddr = "10.10.100.20:54321"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://trader.lan:8090")
	req.Header.Set("X-CSRF-Token", token)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

// TestSettingsRESTPersistenceAndIndependentModes は画面と同じREST操作で保存・再読込・個別モードを確認します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。商品運転は模擬関数で実注文を送りません。
func TestSettingsRESTPersistenceAndIndependentModes(t *testing.T) {
	cfg := testConfig(t)
	directory := filepath.Join(cfg.Root, "conf", "rules")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, profile := range rules.ProfileNames() {
		if err := os.WriteFile(filepath.Join(directory, rules.FileName(profile)), rules.Content(profile), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join(cfg.Root, "conf", "settings.toml")
	file, err := os.OpenFile(base, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n[rule_engine]\ndirectory='conf/rules'\n")
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(filepath.Dir(base))
	if err != nil {
		t.Fatal(err)
	}
	type started struct {
		product string
		dryrun  bool
	}
	starts := make(chan started, 5)
	m, err := NewManager(cfg, func(ctx context.Context, c config.Config, _ *runner.Control, publish func(runner.Snapshot)) error {
		_, market := c.Selected()
		starts <- started{market.Product, c.Trading.DryRun}
		publish(runner.Snapshot{Product: market.Product, DryRun: c.Trading.DryRun})
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
	stateResponse := apiRequest(t, handler, http.MethodGet, "/api/state", "", nil)
	var state State
	if err := json.Unmarshal(stateResponse.Body.Bytes(), &state); err != nil || state.CSRFToken == "" {
		t.Fatal("状態の取得失敗", err)
	}
	token := state.CSRFToken
	getSettings := func() config.WebSettings {
		res := apiRequest(t, handler, http.MethodGet, "/api/settings", "", nil)
		var settings config.WebSettings
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &settings) != nil || settings.Revision == "" {
			t.Fatal("設定の取得失敗", res.Code, res.Body.String())
		}
		return settings
	}
	settings := getSettings()
	catalog := apiRequest(t, handler, http.MethodGet, "/api/rules", "", nil)
	var choiceList struct {
		Rules []config.RuleChoice `json:"rules"`
	}
	if catalog.Code != 200 || json.Unmarshal(catalog.Body.Bytes(), &choiceList) != nil || len(choiceList.Rules) != len(rules.ProfileNames()) {
		t.Fatal("実ファイルからのルール候補取得失敗", catalog.Body.String())
	}
	p05Found, s01Found := false, false
	selectedFound := map[string]bool{}
	for _, choice := range choiceList.Rules {
		switch choice.Name {
		case "NK225_D164_15", "NK225_P164_06", "NK225_U165_03":
			day := choice.Name == "NK225_D164_15"
			if !choice.Supported || choice.Reference != "NK225" || choice.Day != day || choice.Night == day || choice.WindowOnly == day || (!day && choice.WindowHint == "") {
				t.Fatal("新3手法の昼夜と時間帯案内が不正です", choice)
			}
			selectedFound[choice.Name] = true
		}
		if choice.Name == "NK225_S01" {
			s01Found = true
			if choice.Reference != "NK225" || !choice.Supported || !choice.Day || choice.Night || choice.WindowOnly {
				t.Fatal("S01の日経日中専用のカタログが不正です", choice)
			}
		}
		if choice.Name == "NK225_P05" {
			p05Found = true
			if choice.Reference != "NK225" || !choice.Supported || choice.Day || !choice.Night || !choice.WindowOnly || choice.WindowHint == "" {
				t.Fatal("P05の銘柄・夜間前半専用の案内が不正です", choice)
			}
		}
	}
	if !p05Found || !s01Found || len(selectedFound) != 3 {
		t.Fatal("P05又はS01がRESTの実ルール候補にありません")
	}

	// ----------------------------------------
	options := map[string]any{"revision": settings.Revision, "max_per_side": 11, "day": "NK225_S01", "night": "NK225_PG04"}
	res := apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/settings", token, options)
	if res.Code != 200 {
		t.Fatal("商品設定保存失敗", res.Code, res.Body.String())
	}
	loaded, err := config.Load(filepath.Dir(base))
	if err != nil || loaded.Instruments[1].MaxPerSide != 11 || loaded.Instruments[1].Day != "NK225_S01" || loaded.Instruments[1].Night != "NK225_PG04" {
		t.Fatal("商品設定がconfに保存されていません", err)
	}
	if stale := apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/settings", token, options); stale.Code != 409 {
		t.Fatal("古い版番号で上書きできました", stale.Code)
	}
	settings = getSettings()
	settings.Web.Port = 18099
	settings.Schedule.USOpenJST = "23:35"
	res = apiRequest(t, handler, http.MethodPut, "/api/settings", token, settings)
	if res.Code != 200 {
		t.Fatal("共通設定保存失敗", res.Body.String())
	}
	if !m.State().RestartRequired {
		t.Fatal("Webポートの再起動待ちが表示されていません")
	}
	loaded, err = config.Load(filepath.Dir(base))
	if err != nil || loaded.Web.Port != 18099 {
		t.Fatal("共通設定がconfに保存されていません", err)
	}
	reloadedSettings, err := config.ReadWebSettings(loaded)
	if err != nil || reloadedSettings.Schedule.USOpenJST != "23:35" {
		t.Fatal("米国時刻の保存後に表示が変わりました", err, reloadedSettings.Schedule)
	}

	// ----------------------------------------
	for _, item := range []struct{ product, mode string }{{"NK225mini", "dryrun"}, {"TOPIXmini", "live"}} {
		res = apiRequest(t, handler, http.MethodPut, "/api/products/"+item.product+"/mode", token,
			map[string]string{"mode": item.mode, "revision": getSettings().Revision})
		if res.Code != 200 {
			t.Fatal("個別モード設定失敗", res.Code, res.Body.String())
		}
	}
	until(t, func() bool { return len(starts) == 2 })
	for range 2 {
		start := <-starts
		if start.dryrun != (start.product == "NK225mini") {
			t.Fatal("仮想・実取引のモードが別商品へ混入しました", start)
		}
	}
	options["revision"] = getSettings().Revision
	options["max_per_side"] = 12
	if res = apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/settings", token, options); res.Code != 409 {
		t.Fatal("稼働中の商品設定変更が拒否されません", res.Code)
	}
	if getSettings().Instruments[1].MaxPerSide != 11 {
		t.Fatal("拒否した変更が保存されました")
	}
	loaded, err = config.Load(filepath.Dir(base))
	if err != nil || config.ModeOf(loaded.Instruments[1], true) != "dryrun" || config.ModeOf(loaded.Instruments[4], true) != "live" {
		t.Fatal("個別モードが保存されていません", err)
	}
	if res = apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/mode", "", map[string]string{"mode": "off", "revision": getSettings().Revision}); res.Code != 403 {
		t.Fatal("トークンなしの設定変更を許可しました", res.Code)
	}
}
