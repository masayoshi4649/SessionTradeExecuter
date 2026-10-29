package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

// TestP05RESTWindowLimitsPersistenceAndRunningRejection はP05の保存・期限検査・両稼働モードの編集拒否を確認します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。運転は模擬関数に置き換え実注文・外部接続を行いません。
func TestP05RESTWindowLimitsPersistenceAndRunningRejection(t *testing.T) {
	for _, mode := range []string{"dryrun", "live"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testConfig(t)
			started := make(chan config.Config, 1)
			m, err := NewManager(cfg, func(ctx context.Context, worker config.Config, _ *runner.Control, _ func(runner.Snapshot)) error {
				started <- worker
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
			response := apiRequest(t, handler, http.MethodGet, "/api/state", "", nil)
			if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
				t.Fatal(err)
			}
			settings, err := m.Settings()
			if err != nil {
				t.Fatal(err)
			}
			windows := config.StandardNK225Windows("NK225_TG04")
			body := map[string]any{"revision": settings.Revision, "max_per_side": 11, "day": "NK225_TG04", "night": "NK225_D07", "windows": windows}
			for _, end := range []string{"US_OPEN-00:14", "SESSION_END", "22:16"} {
				invalid := append([]config.MethodWindow(nil), windows...)
				invalid[1].End = end
				body["windows"] = invalid
				response = apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/settings", state.CSRFToken, body)
				if response.Code != http.StatusConflict {
					t.Fatal("米国前の期限を越えたP05をRESTで保存しました", end, response.Code, response.Body.String())
				}
			}
			// 同じP05枠をTOPIXへ流用しても、接頭辞の違いを勝手に読み替えません。
			body["windows"] = windows
			response = apiRequest(t, handler, http.MethodPut, "/api/products/TOPIXmini/settings", state.CSRFToken, body)
			if response.Code != http.StatusConflict {
				t.Fatal("P05をTOPIXへ混用しました", response.Code)
			}
			current, _ := m.Settings()
			if current.Revision != settings.Revision || current.Instruments[1].MaxPerSide != 10 || current.Instruments[1].Windows != nil {
				t.Fatal("拒否した設定を一部だけ採用しました")
			}
			response = apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/settings", state.CSRFToken, body)
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body.String())
			}
			loaded, err := config.Load(filepath.Join(cfg.Root, "conf"))
			if err != nil || !reflect.DeepEqual(loaded.Instruments[1].Windows, windows) || loaded.Instruments[1].MaxPerSide != 11 || config.ModeOf(loaded.Instruments[1], true) != "off" {
				t.Fatal("P05の時間帯・Qty・OFFを再読込できません", err)
			}
			current, _ = m.Settings()
			response = apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/mode", state.CSRFToken,
				map[string]string{"revision": current.Revision, "mode": mode})
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body.String())
			}
			until(t, func() bool { return len(started) == 1 })
			worker := <-started
			if worker.Trading.DryRun != (mode == "dryrun") || !reflect.DeepEqual(worker.Windows, windows) || len(worker.RuleExpressionsForMethod("NK225", "Night", "NK225_P05")) == 0 {
				t.Fatal("稼働workerへP05の時間帯・条件・モードが渡りません")
			}
			current, _ = m.Settings()
			before, err := os.ReadFile(filepath.Join(cfg.Root, "conf", "web-ui.toml"))
			if err != nil {
				t.Fatal(err)
			}
			modified := append([]config.MethodWindow(nil), windows...)
			modified[1].End, modified[2].Start = "US_OPEN-00:30", "US_OPEN-00:30"
			body["revision"], body["windows"] = current.Revision, modified
			response = apiRequest(t, handler, http.MethodPut, "/api/products/NK225mini/settings", state.CSRFToken, body)
			if response.Code != http.StatusConflict {
				t.Fatal("稼働中の単品RESTで時間帯を変更しました", response.Code)
			}
			current.Instruments[1].Windows = modified
			response = apiRequest(t, handler, http.MethodPut, "/api/settings", state.CSRFToken, current)
			if response.Code != http.StatusConflict {
				t.Fatal("稼働中の全般RESTで時間帯を変更しました", response.Code)
			}
			after, err := os.ReadFile(filepath.Join(cfg.Root, "conf", "web-ui.toml"))
			if err != nil || string(before) != string(after) {
				t.Fatal("拒否した稼働中変更が設定ファイルに保存されました", err)
			}
			product := m.State().Products[1]
			if product.SettingsEditable || product.SettingsBlockReason == "" {
				t.Fatal("UIへ編集拒否と理由を通知していません", product)
			}
		})
	}
}
