package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

//go:embed assets/*
var assets embed.FS

// NewHandler は全接続元向けの画面と同一オリジン限定の操作APIを構成します。
//
// 引数: manager(*Manager)は商品管理。返り値: http.Handlerとerror。乱数をCSRF対策に使用します。
func NewHandler(manager *Manager) (http.Handler, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(nonce[:])
	files, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil, err
	}
	static := http.FileServer(http.FS(files))
	mux := http.NewServeMux()
	registerAPI(mux, manager, token)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			entry, err := fs.Stat(files, strings.TrimPrefix(r.URL.Path, "/"))
			if err != nil || entry.IsDir() {
				http.NotFound(w, r)
				return
			}
		}
		static.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		// 画面へのリンクは接続元を問わず受け付け、状態変更の操作時に画面との一致を確認します。
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(token)) != 1 {
				respond(w, http.StatusForbidden, map[string]string{"error": "操作画面を再読み込みしてください"})
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				scheme := "http"
				if r.TLS != nil {
					scheme = "https"
				}
				if err != nil || u.Scheme != scheme || !strings.EqualFold(u.Host, r.Host) || u.User != nil {
					http.Error(w, "別のサイトからの操作は受け付けません", http.StatusForbidden)
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "別のサイトからの接続は受け付けません", http.StatusForbidden)
				return
			}
		}
		mux.ServeHTTP(w, r)
	}), nil
}

// respond はJSONをUTF-8で一回返します。
//
// 引数: w(http.ResponseWriter)、code(int)、body(any)。返り値: なし。
func respond(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// Run は設定したポートで画面を提供し、終了時は全商品の安全終了を待ちます。
//
// 引数: ctx(context.Context)は停止要求、cfg(config.Config)は検査済みWeb設定。
// 返り値: errorは待受失敗・終了時の未返済などです。
func Run(ctx context.Context, cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.Web == nil {
		return errors.New("[web]設定が必要です")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.Web.Host, strconv.Itoa(cfg.Web.Port)))
	if err != nil {
		return fmt.Errorf("Webの待受を開始できません: %w", err)
	}
	defer listener.Close()
	manager, err := NewManager(cfg, runner.RunControlled)
	if err != nil {
		return err
	}
	defer manager.Close()
	handler, err := NewHandler(manager)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if ip := net.ParseIP(cfg.Web.Host); ip != nil && ip.IsUnspecified() {
		fmt.Printf("Web画面: http://localhost:%d/ / 全インターフェースで待受中。別PCからは起動PCのIPアドレス・ホスト名で接続してください。\n", cfg.Web.Port)
	} else {
		fmt.Printf("Web画面: http://%s/\n", listener.Addr())
	}
	fmt.Println("初期OFFの商品は画面からONにしてください。")
	if err = manager.StartConfigured(); err == nil {
		select {
		case <-ctx.Done():
		case err = <-done:
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
		}
	}
	// 待受を維持したまま新規を禁止し、状態画面で返済中の状況を確認可能にします。
	closeErr := manager.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpErr := server.Shutdown(shutdown)
	if httpErr != nil {
		_ = server.Close()
	}
	return errors.Join(err, closeErr, httpErr)
}
