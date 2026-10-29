package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

// TestWindowRESTPersistenceAndRunningRejection は時間帯保存と旧画面・稼働中変更の拒否を実通信なしで確認します。
//
// 引数: t(*testing.T)は試験。返り値: なし。
func TestWindowRESTPersistenceAndRunningRejection(t *testing.T) {
	cfg := testConfig(t)
	m, err := NewManager(cfg, func(ctx context.Context, c config.Config, control *runner.Control, publish func(runner.Snapshot)) error {
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
	var state State
	res := apiRequest(t, handler, http.MethodGet, "/api/state", "", nil)
	if err = json.Unmarshal(res.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	settings, _ := m.Settings()
	emptyBody := map[string]any{"revision": settings.Revision, "max_per_side": 10, "day": "NK225_D07", "night": "NK225_D07", "windows": []config.MethodWindow{}}
	res = apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/settings", state.CSRFToken, emptyBody)
	if res.Code != http.StatusBadRequest {
		t.Fatal("明示空配列が旧昼夜運転へ戻りました", res.Code, res.Body.String())
	}
	// 全般RESTはomitemptyを経由しない実際のJSON空配列を送る。
	rawSettings, _ := json.Marshal(settings)
	var globalBody map[string]any
	if err = json.Unmarshal(rawSettings, &globalBody); err != nil {
		t.Fatal(err)
	}
	globalBody["instruments"].([]any)[1].(map[string]any)["windows"] = []any{}
	res = apiRequest(t, handler, http.MethodPut, "/api/settings", state.CSRFToken, globalBody)
	if res.Code != http.StatusConflict {
		t.Fatal("全般RESTの明示空配列を許可しました", res.Code, res.Body.String())
	}
	windows := []config.MethodWindow{{ID: "day", Session: "Day", Start: "SESSION_START", End: "SESSION_END", Method: "NK225_D07"}, {ID: "early", Session: "Night", Start: "SESSION_START", End: "US_OPEN-00:15", Method: "NK225_D07"}, {ID: "late", Session: "Night", Start: "US_OPEN+00:15", End: "SESSION_END", Method: "NK225_PG04"}}
	body := map[string]any{"revision": settings.Revision, "max_per_side": 10, "day": "NK225_D07", "night": "NK225_D07", "windows": windows}
	res = apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/settings", state.CSRFToken, body)
	if res.Code != http.StatusOK {
		t.Fatal(res.Code, res.Body.String())
	}
	settings, _ = m.Settings()
	if len(settings.Instruments[1].Windows) != 3 {
		t.Fatal("時間帯が保存されません")
	}
	settings.Instruments[1].Windows[0].Method = "NK225_PG04"
	unchanged, _ := m.Settings()
	if unchanged.Instruments[1].Windows[0].Method != "NK225_D07" {
		t.Fatal("REST用コピーが元設定を変更しました")
	}
	delete(body, "windows")
	body["revision"] = unchanged.Revision
	res = apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/settings", state.CSRFToken, body)
	if res.Code != http.StatusConflict {
		t.Fatal("旧画面が明示枠を消しました", res.Code)
	}
	body["windows"] = windows
	if err = m.SetMode("NK225mini", "dryrun", unchanged.Revision, "試験"); err != nil {
		t.Fatal(err)
	}
	current, _ := m.Settings()
	body["revision"] = current.Revision
	res = apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/settings", state.CSRFToken, body)
	if res.Code != http.StatusConflict {
		t.Fatal("稼働中に時間帯を変更できました", res.Code)
	}
}
