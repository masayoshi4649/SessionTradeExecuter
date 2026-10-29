package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
)

// acquireReportOwnerLock は同じ商品・モードのレポートを別台帳から上書きする二重起動を防ぎます。
//
// 引数: cfg(config.Config)は共通ログ保存先と対象商品を持つ運転設定です。
// 返り値: *processLockとerror。ロックはOS一時領域へ置き、旧log階層を作りません。
// 同一OSユーザーの排他です。CSV更新そのものの排他はjournalが別途行います。
func acquireReportOwnerLock(cfg config.Config) (*processLock, error) {
	root, err := filepath.Abs(cfg.Path(cfg.Runtime.LogDirectory))
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
	}
	_, market := cfg.Selected()
	key := sha256.Sum256([]byte(root + "|" + market.Product + "|" + mode(cfg.Trading.DryRun)))
	lock, err := acquireLock(filepath.Join(os.TempDir(), "sessiontrader-report-owner-locks", hex.EncodeToString(key[:])))
	if err != nil {
		return nil, fmt.Errorf("同じレポート保存先・商品・モードの二重起動を拒否しました (%s): %w", market.Product, err)
	}
	return lock, nil
}
