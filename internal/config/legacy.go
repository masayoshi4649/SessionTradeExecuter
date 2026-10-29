package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// ReadLegacyForMigration は旧二商品版の設定を移行専用に読み、旧形式の指紋を再現します。
//
// 引数: directory string は書き換える前に退避した旧confフォルダです。
// 返り値: Configは旧設定、stringは旧形式SHA256、errorは不明設定・形式・値の不正です。
// この設定を連続運転へ渡さず、元のSQLiteとの照合にだけ使用します。
func ReadLegacyForMigration(directory string) (Config, string, error) {
	c := Defaults()
	abs, err := filepath.Abs(directory)
	if err != nil {
		return c, "", err
	}
	files, err := filepath.Glob(filepath.Join(abs, "*.toml"))
	if err != nil || len(files) == 0 {
		return c, "", fmt.Errorf("旧設定フォルダに*.tomlがありません: %s", abs)
	}
	sort.Strings(files)
	var text strings.Builder
	for _, name := range files {
		raw, err := os.ReadFile(name)
		if err != nil {
			return c, "", err
		}
		text.WriteString(strings.TrimPrefix(string(raw), "\ufeff"))
		text.WriteByte('\n')
	}
	meta, err := toml.Decode(text.String(), &c)
	if err != nil {
		return c, "", fmt.Errorf("旧設定TOMLを解析できません: %w", err)
	}
	if unknown := meta.Undecoded(); len(unknown) != 0 {
		return c, "", fmt.Errorf("旧設定の未対応キー: %v", unknown)
	}
	if c.Market != nil || meta.IsDefined("trading", "instance_id") {
		return c, "", fmt.Errorf("旧設定には[market]とinstance_idを指定できません。書き換え前のconfを指定してください")
	}
	if meta.IsDefined("methods") || meta.IsDefined("schedule", "day_extra_jst") ||
		meta.IsDefined("schedule", "us_open_jst") || meta.IsDefined("schedule", "london_extra_jst") {
		return c, "", fmt.Errorf("旧設定にはmethodsと日本時間の新キーを指定できません。書き換え前のconfを指定してください")
	}
	c.Root = filepath.Dir(abs)
	enabled := 0
	for reference, market := range c.Markets {
		if reference != "TOPIX" && reference != "NK225" {
			return c, "", fmt.Errorf("旧設定の観測市場が不明です: %s", reference)
		}
		if !market.Enabled {
			continue
		}
		enabled++
		validation := c
		validation.Markets = map[string]Market{reference: market}
		if err := validation.Validate(); err != nil {
			return c, "", fmt.Errorf("旧設定が不正です: %w", err)
		}
		c.Controller.BaseURL = validation.Controller.BaseURL
		c.Markets[reference] = validation.Markets[reference]
	}
	if enabled == 0 {
		return c, "", fmt.Errorf("旧設定に有効な売買商品がありません")
	}
	value := struct {
		URL       string
		Trading   struct{ DryRun bool }
		Schedule  Schedule
		Markets   map[string]Market
		Products  map[string]Product
		Algorithm map[string]float64
	}{c.Controller.BaseURL, struct{ DryRun bool }{c.Trading.DryRun}, c.Schedule, c.Markets, c.Products, c.Algorithm}
	raw, err := json.Marshal(value)
	if err != nil {
		return c, "", err
	}
	digest := sha256.Sum256(raw)
	return c, hex.EncodeToString(digest[:]), nil
}
