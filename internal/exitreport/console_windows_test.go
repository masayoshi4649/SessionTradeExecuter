//go:build windows

package exitreport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const consoleHelperMode = "SESSIONTRADER_CONSOLE_TEST_MODE"
const consoleHelperDirectory = "SESSIONTRADER_CONSOLE_TEST_DIRECTORY"

// TestWindowsConsoleProcess は非表示の実コンソールで単独起動・共有端末・終了確認を検証します。
//
// 引数: t *testing.T は検証環境です。子プロセスの場合は専用環境変数で検証内容を選びます。
// 返り値: なし。判定の相違・入力前の終了・Enter 入力後の停止継続・子の異常終了で失敗します。
func TestWindowsConsoleProcess(t *testing.T) {
	mode := os.Getenv(consoleHelperMode)
	if mode == "" {
		if err := runConsoleHelper(t.TempDir(), "単独", windows.CREATE_NEW_CONSOLE); err != nil {
			t.Fatal(err)
		}
		return
	}
	directory := os.Getenv(consoleHelperDirectory)
	var err error
	switch mode {
	case "単独":
		err = checkStandaloneConsole(directory)
	case "共有":
		if standaloneConsole(os.Stdin, os.Stdout, os.Stderr) {
			err = errors.New("親と共有した実コンソールでも単独起動と判定しました")
		}
	default:
		err = fmt.Errorf("不明な検証モード: %s", mode)
	}
	message := "成功"
	if err != nil {
		message = err.Error()
	}
	if writeErr := os.WriteFile(filepath.Join(directory, mode+".txt"), []byte(message), 0600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// checkStandaloneConsole は単独コンソールにおける実際の終了確認と共有子の判定を調べます。
//
// 引数: directory string は子の結果とエラーログを保存する一時フォルダです。
// 返り値: error は判定・入力待機・コンソールへの Enter 投入の失敗です。
func checkStandaloneConsole(directory string) error {
	if !standaloneConsole(os.Stdin, os.Stdout, os.Stderr) {
		return errors.New("新しい単独コンソールが終了確認の対象になっていません")
	}
	if err := runConsoleHelper(directory, "共有", 0); err != nil {
		return err
	}

	// ----------------------------------------
	// 実際の標準入力で待たせ、ログの保存後にも自動終了しないことを確かめます。
	done := make(chan struct{})
	go func() {
		Report(filepath.Join(directory, "sessiontrader.exe"), errors.New("実コンソールの終了確認テスト"))
		close(done)
	}()
	select {
	case <-done:
		return errors.New("Enter を入力する前に終了確認が終わりました")
	case <-time.After(200 * time.Millisecond):
	}
	content, err := os.ReadFile(filepath.Join(directory, "log", "error.log"))
	if err != nil || !strings.Contains(string(content), "実コンソールの終了確認テスト") {
		return fmt.Errorf("実入力の待機中に停止理由が保存されていません: %v / %s", err, content)
	}
	if err := sendConsoleEnter(); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("Enter の入力後も終了確認が終わりません")
	}
}

// runConsoleHelper は標準ハンドルを転送せず、指定したコンソールで同じ検証用 exe を実行します。
//
// 引数: directory string は結果の保存先、mode string は検証種別、flags uint32 は生成方式です。
// 返り値: error は起動・8秒以内の終了・結果照合の失敗です。残った子は必ず終了します。
func runConsoleHelper(directory, mode string, flags uint32) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command, err := windows.UTF16PtrFromString(windows.EscapeArg(executable) + " -test.run=^TestWindowsConsoleProcess$ -test.timeout=7s")
	if err != nil {
		return err
	}
	environment := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, consoleHelperMode+"=") && !strings.HasPrefix(value, consoleHelperDirectory+"=") {
			environment = append(environment, value)
		}
	}
	environment = append(environment, consoleHelperMode+"="+mode, consoleHelperDirectory+"="+directory)
	block := utf16.Encode([]rune(strings.Join(environment, "\x00") + "\x00\x00"))
	startup := windows.StartupInfo{Flags: windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	var process windows.ProcessInformation
	if err := windows.CreateProcess(nil, command, nil, nil, false, flags|windows.CREATE_UNICODE_ENVIRONMENT, &block[0], nil, &startup, &process); err != nil {
		return fmt.Errorf("%sコンソールの子を作成できません: %w", mode, err)
	}
	defer windows.CloseHandle(process.Thread)
	defer windows.CloseHandle(process.Process)
	defer func() {
		state, _ := windows.WaitForSingleObject(process.Process, 0)
		if state != windows.WAIT_OBJECT_0 {
			_ = windows.TerminateProcess(process.Process, 1)
			_, _ = windows.WaitForSingleObject(process.Process, 1000)
		}
	}()
	state, err := windows.WaitForSingleObject(process.Process, 8000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("%sコンソールの子が8秒以内に終了しません: 状態=%d / %v", mode, state, err)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process.Process, &exitCode); err != nil {
		return err
	}
	result, err := os.ReadFile(filepath.Join(directory, mode+".txt"))
	if err != nil || exitCode != 0 || string(result) != "成功" {
		return fmt.Errorf("%sコンソールの検証失敗: 終了=%d / %v / %s", mode, exitCode, err, result)
	}
	return nil
}

// sendConsoleEnter は実コンソールの入力バッファへ Enter の押下・解放を投入します。
//
// 引数: なし。検証用の単独プロセスの標準入力だけを使用します。
// 返り値: error は Windows の入力イベント登録失敗です。
func sendConsoleEnter() error {
	// KEY_EVENT_RECORD を含む INPUT_RECORD と同じ20バイトの配置です。
	type keyRecord struct {
		EventType uint16
		Padding   uint16
		KeyDown   int32
		Repeat    uint16
		Virtual   uint16
		Scan      uint16
		Character uint16
		Control   uint32
	}
	keys := [2]keyRecord{
		{EventType: 1, KeyDown: 1, Repeat: 1, Virtual: 13, Character: 13},
		{EventType: 1, KeyDown: 0, Repeat: 1, Virtual: 13, Character: 13},
	}
	procedure := windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleInputW")
	if err := procedure.Find(); err != nil {
		return err
	}
	var written uint32
	result, _, err := procedure.Call(os.Stdin.Fd(), uintptr(unsafe.Pointer(&keys[0])), uintptr(len(keys)), uintptr(unsafe.Pointer(&written)))
	if result == 0 || written != uint32(len(keys)) {
		return fmt.Errorf("Enter を入力できません: 登録=%d / %v", written, err)
	}
	return nil
}
