//go:build !windows

package runner

import (
	"fmt"
	unixsys "golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

type processLock struct{ file *os.File }

// acquireLock はOSロックで同一保存先の二重起動を拒否します。
//
// 引数: directory string。保存先。
// 返り値: *processLock と error。プロセス終了時にはOSが解放します。
func acquireLock(directory string) (*processLock, error) {
	return acquireFileLock(directory, true)
}

// acquireSharedLock は旧版の排他ロックと競合し、新版同士は共存するロックを取得します。
//
// 引数: directory string はモード別の保存先です。返り値: *processLock、error。
func acquireSharedLock(directory string) (*processLock, error) {
	return acquireFileLock(directory, false)
}

// acquireFileLock はOSのファイルロックを非待機で取得します。
//
// 引数: directory string、exclusive bool。返り値: *processLock、error。
func acquireFileLock(directory string, exclusive bool) (*processLock, error) {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(directory, ".sessiontrader.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	flags := unixsys.LOCK_SH | unixsys.LOCK_NB
	if exclusive {
		flags = unixsys.LOCK_EX | unixsys.LOCK_NB
	}
	if err = unixsys.Flock(int(f.Fd()), flags); err != nil {
		f.Close()
		return nil, fmt.Errorf("同じ保存先で既にSessionTraderが動作中です: %w", err)
	}
	return &processLock{file: f}, nil
}

// Close はロックを解放します。
//
// 引数: なし。返り値: error。ファイル終了エラー。
func (l *processLock) Close() error { return l.file.Close() }
