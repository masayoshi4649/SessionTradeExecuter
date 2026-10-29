//go:build !windows

package journal

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// acquireReportFileLock は商品DBをまたぐCSV更新をOSの排他で保護します。
//
// 引数: root(string)。返り値: *os.Fileとerror。Close又はプロセス終了で解放され、残置ファイルは妨げません。
func acquireReportFileLock(root string) (*os.File, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".sessionPL.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("終了レポートは他の書込み完了後に再試行します: %w", err)
	}
	return f, nil
}
