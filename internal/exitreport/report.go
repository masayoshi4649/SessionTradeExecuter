// Package exitreport は異常終了の内容を画面と設定読込前から使えるログへ残します。
package exitreport

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Report は停止理由を保存し、Windows の単独コンソールでは終了確認を待ちます。
//
// 引数: executablePath string は実行ファイルのパス、failure error は停止理由です。
// 返り値: なし。ログ保存の失敗も画面へ表示します。運転処理の終了後に呼び出します。
func Report(executablePath string, failure error) {
	report(executablePath, failure, os.Stdin, os.Stderr, standaloneConsole(os.Stdin, os.Stdout, os.Stderr), time.Now())
}

// report は停止理由の記録と、必要な場合だけ Enter 入力の待機を行います。
//
// 引数: executablePath string は実行ファイル、failure error は停止理由、input io.Reader は入力、
// output io.Writer は表示先、wait bool は終了確認の要否、now time.Time は記録日時です。
// 返り値: なし。正常終了なら表示・保存・入力待機のいずれも行いません。
func report(executablePath string, failure error, input io.Reader, output io.Writer, wait bool, now time.Time) {
	if failure == nil {
		return
	}
	entry := fmt.Sprintf("%s SessionTradeExecuter を停止しました: %v\n", now.Format(time.RFC3339Nano), failure)
	fmt.Fprint(output, entry)
	path, err := appendErrorLog(executablePath, entry)
	if err != nil {
		fmt.Fprintf(output, "エラーログを保存できませんでした: %v\n", err)
	} else {
		fmt.Fprintf(output, "エラーログ: %s\n", path)
	}

	// ----------------------------------------
	// 売買は既に停止しています。既存端末・リダイレクト・Linux では入力を読みません。
	if wait {
		fmt.Fprintln(output, "\n内容を確認したら Enter キーで閉じます。")
		_, _ = bufio.NewReader(input).ReadString('\n')
	}
}

// appendErrorLog は実行ファイルの隣の log/error.log へ停止理由を追記します。
//
// 引数: executablePath string は実行ファイルのパス、entry string は日時付き停止理由です。
// 返り値: string は保存先の絶対パス、error はパス解決・保存・終了処理の失敗です。
func appendErrorLog(executablePath, entry string) (string, error) {
	if executablePath == "" {
		return "", fmt.Errorf("実行ファイルの場所を取得できないため保存先を決められません")
	}
	absolute, err := filepath.Abs(executablePath)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
		absolute = resolved
	}
	path := filepath.Join(filepath.Dir(absolute), "log", "error.log")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return path, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return path, err
	}
	_, writeErr := file.WriteString(entry)
	syncErr := file.Sync()
	return path, errors.Join(writeErr, syncErr, file.Close())
}
