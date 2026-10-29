package journal

import (
	"os"
	"path/filepath"
)

// sessionName は、実運転と仮想運転を区別したファイルの基底名を返します。
//
// 引数: session string は検証済みセッション名、dryRun bool は仮想運転です。
// 返り値: string は拡張子を含まないファイル名です。
func sessionName(session string, dryRun bool) string {
	if dryRun {
		return session + "_dryrun"
	}
	return session
}

// atomicWrite は同一ディレクトリの一時ファイルを同期後に置換します。
//
// 引数: path string は派生ファイル、data []byte は内容です。
// 返り値: error は作成・同期・置換失敗です。正本SQLiteは変更しません。
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".sessiontrader-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
