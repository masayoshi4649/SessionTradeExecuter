//go:build windows

package journal

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

// acquireReportFileLock は商品DBをまたぐCSV更新をWindowsの排他で保護します。
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
	var overlap windows.Overlapped
	if err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap); err != nil {
		f.Close()
		return nil, fmt.Errorf("終了レポートは他の書込み完了後に再試行します: %w", err)
	}
	return f, nil
}
