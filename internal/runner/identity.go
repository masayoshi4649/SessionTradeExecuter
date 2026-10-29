package runner

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
)

// newNamespace は保存状態ごとに一度生成する注文識別子の接頭辞を返します。
//
// 引数: なし。返り値: string は128ビットの乱数、errorは乱数取得失敗です。
func newNamespace() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// productLockDirectory は保存先に依存しない同一商品運転のロック先を返します。
//
// 引数: cfg config.Config は検証済み設定です。返り値: stringは共通一時領域内のパスです。
// 同一OSユーザー・同一一時領域での排他です。別ホストやURL別名の同一口座は判別しません。
func productLockDirectory(cfg config.Config) string {
	u, _ := url.Parse(cfg.Controller.BaseURL)
	port := u.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		}
	}
	endpoint := strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
	_, market := cfg.Selected()
	key := sha256.Sum256([]byte(endpoint + "|" + market.Product + "|" + mode(cfg.Trading.DryRun)))
	return filepath.Join(os.TempDir(), "sessiontrader-product-locks", hex.EncodeToString(key[:]))
}

// acquireProductLock は同じ商品を別設定・別保存先で二重起動する操作を拒否します。
//
// 引数: cfg config.Config。返り値: *processLock と error。異なる商品は共存します。
func acquireProductLock(cfg config.Config) (*processLock, error) {
	lock, err := acquireLock(productLockDirectory(cfg))
	if err != nil {
		_, market := cfg.Selected()
		return nil, fmt.Errorf("同じcontroller・商品・運転モードの二重起動を拒否しました (%s): %w", market.Product, err)
	}
	return lock, nil
}
