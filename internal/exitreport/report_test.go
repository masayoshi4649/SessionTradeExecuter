package exitreport

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReportRetainsHistoryWithoutConfiguration は設定が無くても停止理由を追記できることを検証します。
//
// 引数: t *testing.T は検証環境です。
// 返り値: なし。入力待機・履歴消失・不正な日時・保存先の相違で失敗します。
func TestReportRetainsHistoryWithoutConfiguration(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "日本語の商品", "sessiontrader.exe")
	now := time.Date(2026, 10, 23, 8, 43, 45, 0, time.FixedZone("JST", 9*3600))
	var output bytes.Buffer
	for _, message := range []string{"設定ファイルがありません", "保存=20枚 / 現在=10枚"} {
		report(executable, errors.New(message), rejectReader{t}, &output, false, now)
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(executable), "log", "error.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"設定ファイルがありません", "保存=20枚 / 現在=10枚"} {
		if strings.Count(string(content), message) != 1 || !strings.Contains(output.String(), message) {
			t.Fatalf("停止理由が画面とログに残っていません: %s / %s", output.String(), content)
		}
	}
	if strings.Count(string(content), "2026-10-23T08:43:45+09:00") != 2 || strings.Contains(output.String(), "Enter") {
		t.Fatalf("記録日時または非対話終了が不正です: %s / %s", output.String(), content)
	}
}

// TestReportWaitsAfterSaving は終了確認より先に画面とログへ理由が残ることを検証します。
//
// 引数: t *testing.T は検証環境です。
// 返り値: なし。入力を読む前に理由・案内・ログが無い場合に失敗します。
func TestReportWaitsAfterSaving(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "sessiontrader.exe")
	var output bytes.Buffer
	read := false
	input := readerFunc(func(p []byte) (int, error) {
		read = true
		content, err := os.ReadFile(filepath.Join(filepath.Dir(executable), "log", "error.log"))
		if err != nil || !strings.Contains(string(content), "停止理由") || !strings.Contains(output.String(), "Enter キー") {
			t.Fatalf("待機前に理由が残っていません: %v / %s / %s", err, content, output.String())
		}
		return copy(p, "\r\n"), nil
	})
	report(executable, errors.New("停止理由"), input, &output, true, time.Now())
	if !read {
		t.Fatal("単独起動時の終了確認を待っていません")
	}
}

// TestReportStillWaitsWhenLogFails は書込不能でも元の理由を表示して終了確認を待つことを検証します。
//
// 引数: t *testing.T は検証環境です。
// 返り値: なし。保存失敗で元の理由や終了確認が失われる場合に失敗します。
func TestReportStillWaitsWhenLogFails(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "log"), []byte("既存ファイル"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	input := strings.NewReader("\n")
	report(filepath.Join(directory, "sessiontrader.exe"), errors.New("本来の停止理由"), input, &output, true, time.Now())
	if input.Len() != 0 || !strings.Contains(output.String(), "本来の停止理由") || !strings.Contains(output.String(), "エラーログを保存できませんでした") {
		t.Fatalf("保存失敗時の表示・終了確認が不正です: %s", output.String())
	}
}

// TestReportIgnoresSuccess は正常終了なら表示・ログ作成・入力読取を行わないことを検証します。
//
// 引数: t *testing.T は検証環境です。
// 返り値: なし。正常終了に異常終了の副作用があれば失敗します。
func TestReportIgnoresSuccess(t *testing.T) {
	directory := t.TempDir()
	var output bytes.Buffer
	report(filepath.Join(directory, "sessiontrader.exe"), nil, rejectReader{t}, &output, true, time.Now())
	_, err := os.Stat(filepath.Join(directory, "log"))
	if output.Len() != 0 || !os.IsNotExist(err) {
		t.Fatalf("正常終了時に停止理由を出力しました: %s / %v", output.String(), err)
	}
}

// TestRedirectedConsoleNeverWaits は入力またはエラー表示の転送時に待たないことを検証します。
//
// 引数: t *testing.T は検証環境です。
// 返り値: なし。コンソール不在・転送・無効ハンドルで待機を許す場合に失敗します。
func TestRedirectedConsoleNeverWaits(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "転送先")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, streams := range [][3]*os.File{
		{file, os.Stdout, os.Stderr}, {os.Stdin, file, os.Stderr}, {os.Stdin, os.Stdout, file},
		{nil, os.Stdout, os.Stderr}, {os.Stdin, nil, os.Stderr}, {os.Stdin, os.Stdout, nil},
	} {
		if standaloneConsole(streams[0], streams[1], streams[2]) {
			t.Fatal("転送またはコンソールなしでも入力待機しようとしました")
		}
	}
}

// ----------------------------------------

type rejectReader struct{ t *testing.T }

// Read は入力してはいけない経路でテストを失敗させます。
//
// 引数: p []byte は読取領域です。
// 返り値: int は0、error は io.EOF です。
func (r rejectReader) Read(p []byte) (int, error) {
	r.t.Error("非対話経路で標準入力を読みました")
	return 0, io.EOF
}

type readerFunc func([]byte) (int, error)

// Read は検証用の入力関数へ処理を渡します。
//
// 引数: p []byte は読取領域です。
// 返り値: int は読取数、error は読取の失敗です。
func (f readerFunc) Read(p []byte) (int, error) { return f(p) }
