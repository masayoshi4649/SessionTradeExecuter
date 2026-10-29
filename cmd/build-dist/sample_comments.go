package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// sampleReplacement は配布例の商品差として明示的に変える単一の設定値です。
type sampleReplacement struct {
	section, key string
	value        any
}

var sampleTable = regexp.MustCompile(`^\[([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)\]\s*(?:#.*)?$`)
var sampleKey = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*|"[A-Za-z_][A-Za-z0-9_.]*")\s*=`)
var sampleScalar = regexp.MustCompile(`^(\s*(?:[A-Za-z_][A-Za-z0-9_]*|"[A-Za-z_][A-Za-z0-9_.]*")\s*=\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[+-]?[0-9][0-9_]*)([ \t]*(?:#.*)?)$`)

// readSampleTemplate は配布元の最小設定を説明付きのまま一つの本文にまとめます。
//
// 引数: directory(string) は配布準備用confです。*.toml.sampleだけを読みます。
// 返り値: []byte は設定の本文、error は読取またはサンプル欠落です。
// Config全体を再出力しないため、内部の既定値が設定例へ増えることはありません。
func readSampleTemplate(directory string) ([]byte, error) {
	paths, err := filepath.Glob(filepath.Join(directory, "*.toml.sample"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("説明付きの*.toml.sampleがありません")
	}
	order := map[string]int{"system.toml.sample": 0, "markets.toml.sample": 1, "algorithm.toml.sample": 2}
	sort.SliceStable(paths, func(i, j int) bool {
		left, exists := order[filepath.Base(paths[i])]
		if !exists {
			left = 3
		}
		right, exists := order[filepath.Base(paths[j])]
		if !exists {
			right = 3
		}
		return left < right
	})
	var output strings.Builder
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		output.WriteString(strings.TrimRight(strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n"), "\n"))
		output.WriteString("\n\n")
	}
	return []byte(output.String()), nil
}

// replaceSampleScalars は説明と設定項目数を維持し、指定した単純値だけを置き換えます。
//
// 引数: template([]byte) は既知形式のサンプル、replacements([]sampleReplacement) は商品差です。
// 返り値: []byte は生成設定、error は構文・説明・差替え対象の欠落または意図しない値変更です。
// 値の解析と出力の検証には正規TOMLデコーダーを使い、複数行値などへ推測で対応しません。
func replaceSampleScalars(template []byte, replacements []sampleReplacement) ([]byte, error) {
	var expected map[string]any
	if _, err := toml.Decode(string(template), &expected); err != nil {
		return nil, err
	}
	encoded := map[string]string{}
	for _, replacement := range replacements {
		path := replacement.section + "." + replacement.key
		if _, exists := encoded[path]; exists {
			return nil, fmt.Errorf("設定例の差替え指定が重複しています: %s", path)
		}
		table, exists := expected[replacement.section].(map[string]any)
		if !exists {
			return nil, fmt.Errorf("設定例に差替え先テーブルがありません: %s", replacement.section)
		}
		if _, exists = table[replacement.key]; !exists {
			return nil, fmt.Errorf("設定例に差替え先キーがありません: %s", path)
		}
		var value bytes.Buffer
		if err := toml.NewEncoder(&value).Encode(map[string]any{replacement.key: replacement.value}); err != nil {
			return nil, err
		}
		var normalized map[string]any
		if _, err := toml.Decode(value.String(), &normalized); err != nil {
			return nil, err
		}
		table[replacement.key] = normalized[replacement.key]
		_, literal, _ := strings.Cut(strings.TrimSpace(value.String()), "=")
		encoded[path] = strings.TrimSpace(literal)
	}
	// ----------------------------------------
	// 説明や無効化例は移動せず、既知の単純値の部分だけを置き換えます。
	lines := strings.Split(strings.ReplaceAll(string(template), "\r\n", "\n"), "\n")
	section, explained := "", false
	seen := map[string]bool{}
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			explained = explained || strings.TrimSpace(strings.TrimPrefix(trimmed, "#")) != ""
			continue
		}
		if match := sampleTable.FindStringSubmatch(trimmed); match != nil {
			section, explained = match[1], false
			continue
		}
		match := sampleKey.FindStringSubmatch(trimmed)
		if match == nil || section == "" {
			return nil, fmt.Errorf("設定サンプルが未対応の形式です: %d行", index+1)
		}
		path := section + "." + strings.Trim(match[1], `"`)
		if !explained {
			return nil, fmt.Errorf("配布元サンプルに設定項目の説明がありません: %s", path)
		}
		explained = false
		if literal, exists := encoded[path]; exists {
			scalar := sampleScalar.FindStringSubmatch(line)
			if scalar == nil || seen[path] {
				return nil, fmt.Errorf("設定例の差替え対象が単純値ではないか重複しています: %s", path)
			}
			lines[index] = scalar[1] + literal + scalar[3]
			seen[path] = true
		}
	}
	for path := range encoded {
		if !seen[path] {
			return nil, fmt.Errorf("設定例の差替え対象を特定できません: %s", path)
		}
	}
	output := strings.Join(lines, "\n")
	var actual map[string]any
	if _, err := toml.Decode(output, &actual); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(expected, actual) {
		return nil, fmt.Errorf("指定した商品差以外の設定値または設定項目が変わりました")
	}
	return []byte(output), nil
}
