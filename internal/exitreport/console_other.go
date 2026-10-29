//go:build !windows

package exitreport

import "os"

// standaloneConsole は Windows 以外で入力待機を無効にします。
//
// 引数: input *os.File は標準入力、output *os.File は標準出力、errorOutput *os.File はエラー表示先です。
// 返り値: bool は常に false。端末や監視プロセスへ終了コードを直ちに返します。
func standaloneConsole(input, output, errorOutput *os.File) bool {
	return false
}
