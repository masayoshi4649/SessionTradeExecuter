package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/cli"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// prepareWebConfiguration は全商品を一画面で運転する設定例を検査して配置します。
//
// 引数: root stringはアプリルート、stage stringは配布用の専用一時領域です。
// 返り値: errorは設定コピー・構文・手法の不整合です。実設定への上書きは行いません。
func prepareWebConfiguration(root, stage string) error {
	sample, err := readSampleTemplate(filepath.Join(root, "web-conf"))
	if err != nil {
		return err
	}
	if err := validateWebSampleComments(sample); err != nil {
		return err
	}
	directory := filepath.Join(stage, bundleDirectory, "conf")
	if err := copyConfiguration(filepath.Join(root, "web-conf"), directory); err != nil {
		return fmt.Errorf("Web版の設定例: %w", err)
	}
	ruleDirectory := filepath.Join(directory, "rules")
	if err := os.MkdirAll(ruleDirectory, 0755); err != nil {
		return err
	}
	for _, profile := range rules.ProfileNames() {
		if err := os.WriteFile(filepath.Join(ruleDirectory, rules.FileName(profile)), rules.Content(profile), 0644); err != nil {
			return err
		}
	}
	settings, err := config.Load(directory)
	if err != nil {
		return fmt.Errorf("Web版の設定例が不正です: %w", err)
	}
	if settings.Web == nil || !settings.Trading.DryRun {
		return fmt.Errorf("Web版の設定例は[web]とdryrun=trueが必要です")
	}
	workers, err := settings.WorkerConfigs()
	if err != nil {
		return err
	}
	for i, worker := range workers {
		if config.ModeOf(settings.Instruments[i], settings.Trading.DryRun) != "off" {
			return fmt.Errorf("Web版の設定例は全商品のmode=offが必要です")
		}
		if err := cli.ValidateAlgorithms(worker); err != nil {
			return fmt.Errorf("Web版の手法が不正です: %w", err)
		}
	}
	return nil
}

// copyRuleFiles は全手法の実ルールを初回だけ配置し、利用者が変更した既存式を保持します。
//
// 引数: source/destination(string)はルールの配布元と配布先です。
// 返り値: errorは読書き又は必要ファイル欠落です。sampleは作成・更新しません。
func copyRuleFiles(source, destination string) error {
	if err := rejectLink(destination); err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	for _, profile := range rules.ProfileNames() {
		name := rules.FileName(profile)
		actual := filepath.Join(destination, name)
		if _, err := os.Lstat(actual); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return err
		}
		// 旧名の利用者条件がある更新先では、標準式で置き換えず内容を新名へ引き継ぎます。
		legacy := filepath.Join(destination, profile+".toml")
		if legacy != actual {
			if previous, readErr := os.ReadFile(legacy); readErr == nil {
				data = previous
			} else if !os.IsNotExist(readErr) {
				return readErr
			}
		}
		file, err := os.OpenFile(actual, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return err
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

// validateWebSampleComments は配布する設定の全有効キーに直前の説明を要求します。
//
// 引数: sample([]byte)はUTF-8のTOML。返り値: errorは説明の欠落又は未対応の行です。
// 配列テーブルと先頭のversionにも対応し、値の型や未知キーは正規ローダーへ委ねます。
func validateWebSampleComments(sample []byte) error {
	section, explained := "", false
	for index, line := range strings.Split(strings.ReplaceAll(string(sample), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			explained = explained || strings.TrimSpace(strings.TrimPrefix(line, "#")) != ""
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(strings.TrimSpace(strings.SplitN(line, "#", 2)[0]), "[]")
			explained = false
			continue
		}
		key := sampleKey.FindStringSubmatch(line)
		if key == nil {
			return fmt.Errorf("設定サンプルが未対応の形式です: %d行", index+1)
		}
		name := strings.Trim(key[1], `"`)
		if section != "" {
			name = section + "." + name
		}
		if !explained {
			return fmt.Errorf("配布元サンプルに設定項目の説明がありません: %s", name)
		}
		explained = false
	}
	return nil
}
