//go:build windows

package exitreport

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// standaloneConsole は入出力がコンソールで、その接続者が自分だけかを調べます。
//
// 引数: input *os.File は標準入力、output *os.File は標準出力、errorOutput *os.File はエラー表示先です。
// 返り値: bool は単独起動の終了確認が必要なら true。不明・端末共有・出力転送時は false です。
func standaloneConsole(input, output, errorOutput *os.File) bool {
	var mode uint32
	for _, file := range []*os.File{input, output, errorOutput} {
		if file == nil || windows.GetConsoleMode(windows.Handle(file.Fd()), &mode) != nil {
			return false
		}
	}
	if getConsoleProcessList.Find() != nil {
		return false
	}
	var process [1]uint32
	count, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&process[0])), uintptr(len(process)))
	// 配列が不足すると必要数だけが返るため、1件以外は配列を参照しません。
	return count == 1 && process[0] == uint32(os.Getpid())
}
