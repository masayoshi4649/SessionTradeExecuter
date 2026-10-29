// 配布先ごとに実行ファイル・設定サンプル・説明書を組み立てます。
package main

import (
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/cli"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
)

const bundleDirectory = "SessionTradeExecuter"

//go:embed README.dist.md
var distributionReadme []byte

//go:embed README.root.md
var distributionRootReadme []byte

//go:embed README.web.md
var distributionWebReadme []byte

// main はプロジェクトルートから配布物を作成します。
//
// 引数: なし。実行ディレクトリと Go のビルド環境を使用します。
// 返り値: なし。失敗時は終了コード 1 で終了します。
func main() {
	root, err := os.Getwd()
	if err == nil {
		err = buildDistribution(root, compile)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "配布物の作成に失敗しました:", err)
		os.Exit(1)
	}
	fmt.Println("dist/windows-amd64/SessionTradeExecuter と dist/linux-amd64/SessionTradeExecuter を更新しました。既存の実設定・データ・ログは保持しています。")
}

// ----------------------------------------

// buildDistribution は両 OS のビルド成功後に配布物を配置します。
//
// 引数: root string はアプリのルート、build func(string,string,string)error はビルド処理です。
// 返り値: error は設定・ビルド・配置の失敗です。稼働中のプロセスは停止しません。
func buildDistribution(root string, build func(string, string, string) error) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !strings.Contains(string(module), "module github.com/masayoshi4649/SessionTradeExecuter") {
		return fmt.Errorf("SessionTradeExecuter のルートで go run ./cmd/build-dist を実行してください")
	}
	destination := filepath.Join(root, "dist")
	if err = rejectLink(destination); err != nil {
		return err
	}
	if err = os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(destination, ".build-")
	if err != nil {
		return err
	}
	// 自分で作った直下の一時領域に限定し、設定・data・log は削除しません。
	if filepath.Dir(stage) != destination || !strings.HasPrefix(filepath.Base(stage), ".build-") {
		return fmt.Errorf("一時領域が配布先の外にあります")
	}
	defer os.RemoveAll(stage)
	if err = prepareWebConfiguration(root, stage); err != nil {
		return err
	}
	examples := []string{bundleDirectory}

	targets := []struct{ os, folder, executable string }{
		{"windows", "windows-amd64", "session-trade-executer.exe"},
		{"linux", "linux-amd64", "session-trade-executer"},
	}
	for _, target := range targets {
		path := filepath.Join(stage, target.folder)
		if err = os.MkdirAll(path, 0755); err != nil {
			return err
		}
		if err = build(root, target.os, filepath.Join(path, target.executable)); err != nil {
			return fmt.Errorf("%s のビルド: %w", target.os, err)
		}
		if err = copyDocumentation(filepath.Join(root, "docs"), filepath.Join(path, "docs")); err != nil {
			return err
		}
	}
	// ----------------------------------------
	// ビルド失敗時は既存の実行ファイル・実設定を更新しません。
	for _, target := range targets {
		path := filepath.Join(destination, target.folder)
		if err = rejectLink(path); err != nil {
			return err
		}
		if err = os.MkdirAll(path, 0755); err != nil {
			return err
		}
		for _, example := range examples {
			// 商品フォルダだけをコピーして配備できるよう、実行ファイルも個別に同梱します。
			bundle := filepath.Join(path, example)
			if err = rejectLink(bundle); err != nil {
				return err
			}
			if err = copyConfiguration(filepath.Join(stage, example, "conf"), filepath.Join(bundle, "conf")); err != nil {
				return err
			}
			if err = copyRuleFiles(filepath.Join(stage, example, "conf", "rules"), filepath.Join(bundle, "conf", "rules")); err != nil {
				return err
			}
			if err = copyDocumentation(filepath.Join(stage, target.folder, "docs"), filepath.Join(bundle, "docs")); err != nil {
				return err
			}
			readme := distributionReadme
			if example == bundleDirectory {
				readme = distributionWebReadme
			}
			if err = atomicFile(filepath.Join(bundle, "README.md"), readme, 0644); err != nil {
				return err
			}
			executable, readErr := os.ReadFile(filepath.Join(stage, target.folder, target.executable))
			if readErr != nil {
				return readErr
			}
			if err = atomicFile(filepath.Join(bundle, target.executable), executable, 0755); err != nil {
				return err
			}
		}
		if err = atomicFile(filepath.Join(path, "README.md"), distributionRootReadme, 0644); err != nil {
			return err
		}
	}
	return nil
}

// prepareSingleConfigurations は商品ごとに独立配置するconfの設定例を組み立てます。
//
// 引数: stage string は一時領域、settings config.Config は既定設定です。
// 返り値: []string は商品フォルダ名、errorは生成または検証の失敗です。
func prepareSingleConfigurations(stage string, settings config.Config) ([]string, error) {
	template, err := readSampleTemplate(filepath.Join(stage, "conf"))
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, example := range []struct {
		folder, product, id, day, night string
		cap                             int
	}{
		{"TOPIX", "TOPIX", "topix", "T06", "T04", 2},
		{"TOPIXmini", "TOPIXmini", "topix-mini", "T06", "T04", 10},
		{"NK225", "NK225", "nk225", "S01", "D07", 2},
		{"NK225mini", "NK225mini", "nk225mini", "S01", "D07", 10},
		{"NK225micro", "NK225micro", "nk-micro", "S01", "D07", 20},
	} {
		if _, exists := settings.Products[example.product]; !exists {
			return nil, fmt.Errorf("配布用設定に商品の規格がありません: %s", example.product)
		}
		annotated, err := replaceSampleScalars(template, []sampleReplacement{
			{"trading", "instance_id", example.id},
			{"market", "product", example.product},
			{"market", "max_per_side", example.cap},
			{"methods", "day", example.day},
			{"methods", "night", example.night},
		})
		if err != nil {
			return nil, fmt.Errorf("%s の説明付き設定例を生成できません: %w", example.folder, err)
		}
		directory := filepath.Join(stage, example.folder, "conf")
		if err := os.MkdirAll(directory, 0755); err != nil {
			return nil, err
		}
		for _, name := range []string{"settings.toml", "settings.toml.sample"} {
			if err := os.WriteFile(filepath.Join(directory, name), annotated, 0644); err != nil {
				return nil, err
			}
		}
		generated, err := config.Load(directory)
		if err != nil {
			return nil, fmt.Errorf("%s の設定例が不正です: %w", example.folder, err)
		}
		if err := cli.ValidateAlgorithms(generated); err != nil {
			return nil, fmt.Errorf("%s の設定例の手法が不正です: %w", example.folder, err)
		}
		names = append(names, example.folder)
	}
	return names, nil
}

// compile は CGO 不要の Windows/Linux amd64 実行ファイルを生成します。
//
// 引数: root string はルート、goos string は OS、output string は出力ファイルです。
// 返り値: error は Go コマンドの終了エラーです。
func compile(root, goos, output string) error {
	fmt.Printf("%s / amd64 をビルドします。\n", goos)
	command := exec.Command("go", "build", "-trimpath", "-o", output, ".")
	command.Dir = root
	command.Env = buildEnvironment(goos)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

// buildEnvironment は親環境を保ったまま対象 OS とアーキテクチャを設定します。
//
// 引数: goos string は対象 OS です。
// 返り値: []string は CGO_ENABLED=0 を含む子プロセス環境です。
func buildEnvironment(goos string) []string {
	environment := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch strings.ToUpper(key) {
		case "GOOS", "GOARCH", "CGO_ENABLED":
			continue
		}
		environment = append(environment, value)
	}
	return append(environment, "GOOS="+goos, "GOARCH=amd64", "CGO_ENABLED=0")
}

// copyConfiguration はサンプルを更新し、存在しない実設定だけ初期化します。
//
// 引数: source/destination string は設定の配布元と配布先です。
// 返り値: error は読書きの失敗です。既存 *.toml は開いて上書きしません。
func copyConfiguration(source, destination string) error {
	names, err := filepath.Glob(filepath.Join(source, "*.toml.sample"))
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("conf に *.toml.sample がありません")
	}
	if err = rejectLink(destination); err != nil {
		return err
	}
	if err = os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	for _, name := range names {
		data, e := os.ReadFile(name)
		if e != nil {
			return e
		}
		base := filepath.Base(name)
		if e = atomicFile(filepath.Join(destination, base), data, 0644); e != nil {
			return e
		}
		actual := filepath.Join(destination, strings.TrimSuffix(base, ".sample"))
		file, e := os.OpenFile(actual, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// copyDocumentation は docs 配下の通常ファイルだけを配布します。
//
// 引数: source/destination string は説明書の配布元と配布先です。
// 返り値: error はリンク・読書き・走査の失敗です。既存の未知ファイルは削除しません。
func copyDocumentation(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("説明書のリンクは配布しません: %s", relative)
		}
		if err = rejectLink(target); err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("通常ファイルではない説明書です: %s", relative)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return atomicFile(target, data, 0644)
	})
}

// atomicFile は一時ファイルの確定後に配布物だけを置換します。
//
// 引数: path string は配置先、data []byte は内容、mode fs.FileMode は権限です。
// 返り値: error は配置の失敗です。
func atomicFile(path string, data []byte, mode fs.FileMode) error {
	if err := rejectLink(path); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".publish-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Chmod(mode)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

// rejectLink は既存リンクを経由する配布先への書込みを防ぎます。
//
// 引数: path string は検査先です。
// 返り値: error はリンクまたは属性取得の失敗です。未作成は許可します。
func rejectLink(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("配布先のシンボリックリンクは更新しません: %s", path)
	}
	return nil
}
