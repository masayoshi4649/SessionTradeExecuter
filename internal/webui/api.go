package webui

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
)

// registerAPI は静的画面に依存しない設定・状態・商品操作のREST APIを登録します。
//
// 引数: mux(*http.ServeMux)は経路、manager(*Manager)は運転と保存、token(string)は操作用トークンです。
// 返り値: なし。保存要求は全て版番号を検査し、旧画面による上書きを防ぎます。
func registerAPI(mux *http.ServeMux, manager *Manager, token string) {
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		state := manager.State()
		state.CSRFToken = token
		respond(w, http.StatusOK, state)
	})
	mux.HandleFunc("GET /api/session-pl", func(w http.ResponseWriter, r *http.Request) {
		rows, err := manager.SessionPL()
		if err != nil {
			respond(w, http.StatusInternalServerError, map[string]string{"error": "セッション損益の読取に失敗しました: " + err.Error()})
			return
		}
		respond(w, http.StatusOK, map[string]any{"rows": rows})
	})
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		settings, err := manager.Settings()
		if err != nil {
			respond(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		respond(w, http.StatusOK, settings)
	})
	mux.HandleFunc("GET /api/rules", func(w http.ResponseWriter, r *http.Request) {
		choices, err := manager.Rules()
		if err != nil {
			respond(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		respond(w, http.StatusOK, map[string]any{"rules": choices})
	})

	// ----------------------------------------
	mux.HandleFunc("PUT /api/products/{product}/mode", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode     string `json:"mode"`
			Revision string `json:"revision"`
		}
		if !decodeAPI(w, r, &body) || !knownProduct(w, manager, r.PathValue("product")) {
			return
		}
		if body.Mode != "off" && body.Mode != "dryrun" && body.Mode != "live" {
			respond(w, http.StatusBadRequest, map[string]string{"error": "modeはoff・dryrun・liveのいずれかを指定してください"})
			return
		}
		if !requireRevision(w, body.Revision) {
			return
		}
		if err := manager.SetMode(r.PathValue("product"), body.Mode, body.Revision, "REST API / "+r.RemoteAddr); err != nil {
			respond(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		respondSettings(w, manager, token)
	})
	mux.HandleFunc("PUT /api/products/{product}/settings", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxPerSide int                    `json:"max_per_side"`
			Day        string                 `json:"day"`
			Night      string                 `json:"night"`
			Windows    *[]config.MethodWindow `json:"windows"`
			Revision   string                 `json:"revision"`
		}
		if !decodeAPI(w, r, &body) || !knownProduct(w, manager, r.PathValue("product")) {
			return
		}
		if body.Windows != nil && len(*body.Windows) == 0 {
			respond(w, http.StatusBadRequest, map[string]string{"error": "時間帯は最低1枠が必要です。全て静観する場合も静観枠を残してください"})
			return
		}
		if body.MaxPerSide < 1 || ((body.Windows == nil || len(*body.Windows) == 0) && (body.Day == "" || body.Night == "")) {
			respond(w, http.StatusBadRequest, map[string]string{"error": "Qtyは1以上の整数、時間帯一覧又は互換昼夜の手法IDを指定してください"})
			return
		}
		if !requireRevision(w, body.Revision) {
			return
		}
		if err := manager.UpdateProductWindows(r.PathValue("product"), body.MaxPerSide, body.Day, body.Night, body.Windows, body.Revision, "REST API / "+r.RemoteAddr); err != nil {
			respond(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		respondSettings(w, manager, token)
	})
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var settings config.WebSettings
		if !decodeAPI(w, r, &settings) || !requireRevision(w, settings.Revision) {
			return
		}
		if err := manager.UpdateSettings(settings, "REST API / "+r.RemoteAddr); err != nil {
			respond(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		respondSettings(w, manager, token)
	})

	// ----------------------------------------
	// 旧画面のキャッシュが残っていても、従来の明示的なON/OFFだけは受け付けます。
	mux.HandleFunc("POST /api/products/{product}/enabled", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if !decodeAPI(w, r, &body) {
			return
		}
		if body.Enabled == nil {
			respond(w, http.StatusBadRequest, map[string]string{"error": "enabledにtrueまたはfalseを指定してください"})
			return
		}
		if err := manager.SetEnabled(r.PathValue("product"), *body.Enabled, "旧画面 / "+r.RemoteAddr); err != nil {
			respond(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		respond(w, http.StatusOK, map[string]any{"product": r.PathValue("product"), "enabled": *body.Enabled})
	})
}

// decodeAPI はJSON一件だけを受け取り、余分な項目・本文・過大な要求を拒否します。
//
// 引数: w(http.ResponseWriter)は応答、r(*http.Request)は要求、target(any)は受取先のポインターです。
// 返り値: boolは解析に成功した場合trueです。失敗時はここで応答します。
func decodeAPI(w http.ResponseWriter, r *http.Request, target any) bool {
	kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if kind != "application/json" {
		respond(w, http.StatusUnsupportedMediaType, map[string]string{"error": "JSONで指定してください"})
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		respond(w, http.StatusBadRequest, map[string]string{"error": "設定内容の形式が不正です: " + err.Error()})
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		respond(w, http.StatusBadRequest, map[string]string{"error": "JSONは一件だけ指定してください"})
		return false
	}
	return true
}

// knownProduct は商品単位の操作で対象が存在することを確認します。
//
// 引数: w(http.ResponseWriter)、manager(*Manager)、product(string)は発注商品名です。
// 返り値: boolは登録済みの場合trueです。不明商品の場合は404を返します。
func knownProduct(w http.ResponseWriter, manager *Manager, product string) bool {
	for _, item := range manager.State().Products {
		if item.Product == product {
			return true
		}
	}
	respond(w, http.StatusNotFound, map[string]string{"error": "対象の商品がありません"})
	return false
}

// requireRevision は設定の上書き事故を防ぐ版番号の指定を確認します。
//
// 引数: w(http.ResponseWriter)、revision(string)はGETで取得した設定の版番号です。
// 返り値: boolは空欄でない場合trueです。一致は保存直前にも検査します。
func requireRevision(w http.ResponseWriter, revision string) bool {
	if revision == "" {
		respond(w, http.StatusConflict, map[string]string{"error": "設定を再取得してから保存してください（版番号がありません）"})
		return false
	}
	return true
}

// respondSettings は保存後の確定設定と実行状態をまとめて返します。
//
// 引数: w(http.ResponseWriter)、manager(*Manager)、token(string)は操作用トークンです。
// 返り値: なし。適用待ちと再起動待ちは状態に明示し、保存を即時反映と誤表示しません。
func respondSettings(w http.ResponseWriter, manager *Manager, token string) {
	settings, err := manager.Settings()
	if err != nil {
		respond(w, http.StatusInternalServerError, map[string]string{"error": "変更を受け付けましたが設定の再取得に失敗しました: " + err.Error()})
		return
	}
	state := manager.State()
	state.CSRFToken = token
	message := "保存しました。"
	for _, item := range state.Products {
		if item.Pending {
			message = "保存しました。未約定注文の取消・照合が終わってから切り替えます。保有玉は決済せず管理対象から外します。"
			break
		}
	}
	if state.RestartRequired {
		message += " 待受先・ポートの変更はアプリ再起動後に反映されます。"
	}
	respond(w, http.StatusOK, map[string]any{"settings": settings, "state": state, "message": message, "restart_required": state.RestartRequired})
}
