package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimumConfig = `
[markets.TOPIX]
enabled = true
product = "TOPIXmini"
max_per_side = 10
[products.TOPIXmini]
tick = 0.25
point_value = 1000
[products.TOPIX]
tick = 0.5
point_value = 10000
[products.NK225]
tick = 10
point_value = 1000
`

// writeConfiguration は一時領域へ設定本文を書きます。
//
// 引数: t *testing.T は試験、body string は本文です。
// 返り値: string は設定フォルダです。
func writeConfiguration(t *testing.T, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "conf")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test.toml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestAutomaticQuantityAndDefaults は省略・ゼロの数量を安全な上限内で配分します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestAutomaticQuantityAndDefaults(t *testing.T) {
	for _, test := range []struct{ cap, expected string }{{"10", "5"}, {"1", "1"}, {"11", "5"}} {
		body := strings.Replace(minimumConfig, "max_per_side = 10", "max_per_side = "+test.cap+"\ncore_quantity=0\nextra_quantity=0", 1)
		cfg, err := Load(writeConfiguration(t, body))
		if err != nil {
			t.Fatal(err)
		}
		expected := 5
		if test.expected == "1" {
			expected = 1
		}
		if !cfg.Trading.DryRun || cfg.Markets["TOPIX"].CoreQuantity != expected || cfg.Markets["TOPIX"].ExtraQuantity != expected {
			t.Fatal("自動配分または仮想既定値が不正です")
		}
	}
}

// TestInvalidAndUnknownConfiguration は誤記・重複・不正枚数を起動前に拒否します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestInvalidAndUnknownConfiguration(t *testing.T) {
	for _, body := range []string{
		minimumConfig + "\n[unknown]\na=1\n",
		minimumConfig + "\n[markets.TOPIX]\nmax_per_side=2\n",
		strings.Replace(minimumConfig, "max_per_side = 10", "max_per_side=10\ncore_quantity=-1", 1),
		strings.Replace(minimumConfig, "max_per_side = 10", "max_per_side=10\nextra_quantity=11", 1),
		strings.Replace(minimumConfig, "TOPIXmini", "NK225micro", 1),
		minimumConfig + "\n[controller]\nbase_url='http://name:password@host'\n",
	} {
		if _, err := Load(writeConfiguration(t, body)); err == nil {
			t.Fatal("不正な設定を受け入れました")
		}
	}
}

// TestSampleIgnoredAndRelativePaths は sample を実設定として読まないことを検証します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestSampleIgnoredAndRelativePaths(t *testing.T) {
	dir := writeConfiguration(t, minimumConfig)
	if err := os.WriteFile(filepath.Join(dir, "invalid.toml.sample"), []byte("これはTOMLではない"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path("data") != filepath.Join(filepath.Dir(dir), "data") {
		t.Fatal("相対保存先の基準が異なります")
	}
	before := cfg.Signature()
	cfg.Markets["TOPIX"] = Market{Enabled: true, Product: "TOPIXmini", MaxPerSide: 12, CoreQuantity: 6, ExtraQuantity: 6}
	if cfg.Signature() == before {
		t.Fatal("数量変更が復元指紋に反映されません")
	}
}

// ----------------------------------------

// TestSingleProductConfiguration は五商品の単一指定と両市場の観測規格を検査します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestSingleProductConfiguration(t *testing.T) {
	base := strings.Replace(minimumConfig, "[markets.TOPIX]\nenabled = true", "[market]", 1)
	base += "\n[products.NK225mini]\ntick=5\npoint_value=100\n[products.NK225micro]\ntick=5\npoint_value=10\n"
	for _, product := range []string{"TOPIX", "TOPIXmini", "NK225", "NK225mini", "NK225micro"} {
		t.Run(product, func(t *testing.T) {
			body := strings.Replace(base, `product = "TOPIXmini"`, `product = "`+product+`"`, 1)
			cfg, err := Load(writeConfiguration(t, body))
			if err != nil {
				t.Fatal(err)
			}
			ref, market := cfg.Selected()
			if len(cfg.Markets) != 1 || ref != Reference(product) || market.Product != product || !market.Enabled || market.CoreQuantity != 5 {
				t.Fatalf("単一商品の正規化が不正です: %#v", cfg.Markets)
			}
			if cfg.InstanceID() != strings.ToLower(product) {
				t.Fatalf("既定の識別子が不正です: %s", cfg.InstanceID())
			}
			before := cfg.Signature()
			if err := cfg.Validate(); err != nil || cfg.Signature() != before {
				t.Fatalf("再検証で設定が変化しました: %v", err)
			}
		})
	}
	missingPeer := strings.Replace(base, "[products.NK225]\ntick = 10\npoint_value = 1000\n", "", 1)
	if _, err := Load(writeConfiguration(t, missingPeer)); err == nil {
		t.Fatal("片方だけの売買でも必要な相手市場の規格欠落を受け付けました")
	}
}

// TestLegacySingleProductAndMultipleRejection は旧単一設定を受理し旧二商品設定を拒否します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestLegacySingleProductAndMultipleRejection(t *testing.T) {
	other := "\n[markets.NK225]\nenabled=true\nproduct='NK225'\nmax_per_side=20\n"
	if _, err := Load(writeConfiguration(t, minimumConfig+other)); err == nil || !strings.Contains(err.Error(), "1プロセス1銘柄") {
		t.Fatalf("旧二商品設定を明確なエラーで拒否していません: %v", err)
	}
	legacy, err := Load(writeConfiguration(t, minimumConfig+strings.Replace(other, "enabled=true", "enabled=false", 1)))
	if err != nil || len(legacy.Markets) != 1 {
		t.Fatalf("旧単一設定を正規化できません: %v", err)
	}
	newConfig := strings.Replace(minimumConfig, "[markets.TOPIX]\nenabled = true", "[market]", 1)
	current, err := Load(writeConfiguration(t, newConfig))
	if err != nil || current.Signature() != legacy.Signature() {
		t.Fatalf("同じ単一設定の新旧形式で復元指紋が異なります: %v", err)
	}
	for _, body := range []string{newConfig + strings.Replace(other, "enabled=true", "enabled=false", 1), strings.Replace(minimumConfig, "enabled = true", "enabled = false", 1)} {
		if _, err := Load(writeConfiguration(t, body)); err == nil {
			t.Fatal("新旧形式の混在または売買商品なしを受け付けました")
		}
	}
}

// TestInstanceIDSafetyAndSignature は実行識別子の安全性と復元指紋への反映を確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestInstanceIDSafetyAndSignature(t *testing.T) {
	for _, id := range []string{"../topix", "a/b", `a\b`, ".", "日本語", "a b", "a:b", "CON", "nul", "COM1", "lpt9", strings.Repeat("a", 65)} {
		body := minimumConfig + "\n[trading]\ninstance_id='" + id + "'\n"
		if _, err := Load(writeConfiguration(t, body)); err == nil {
			t.Fatalf("安全でない識別子を受け付けました: %q", id)
		}
	}
	var lowerSignature string
	for _, id := range []string{"topix-mini_1", "TOPIX-mini_1"} {
		cfg, err := Load(writeConfiguration(t, minimumConfig+"\n[trading]\ninstance_id='"+id+"'\n"))
		if err != nil || cfg.InstanceID() != "topix-mini_1" || cfg.Trading.InstanceID != "topix-mini_1" {
			t.Fatalf("識別子の大文字小文字を正規化できません: %v", err)
		}
		if lowerSignature != "" && cfg.Signature() != lowerSignature {
			t.Fatal("大文字小文字だけの違いで復元指紋が変わりました")
		}
		lowerSignature = cfg.Signature()
		cfg.Trading.InstanceID = "another"
		if cfg.Signature() == lowerSignature {
			t.Fatal("別の実行識別子で復元指紋が同一です")
		}
	}
	if _, err := Load(writeConfiguration(t, minimumConfig+"\n[trading]\ninstance_id='"+strings.Repeat("a", 64)+"'\n")); err != nil {
		t.Fatalf("64文字の識別子を拒否しました: %v", err)
	}
}

// TestLegacyMigrationSignature は旧二市場の自動数量と旧Trading型の指紋を厳密に再現します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestLegacyMigrationSignature(t *testing.T) {
	body := minimumConfig + "\n[markets.NK225]\nenabled=true\nproduct='NK225'\nmax_per_side=2\n"
	old, signature, err := ReadLegacyForMigration(writeConfiguration(t, body))
	if err != nil || len(old.Markets) != 2 || old.Markets["NK225"].CoreQuantity != 1 {
		t.Fatalf("旧二市場を移行用に読めません: %v", err)
	}
	expected := struct {
		URL       string
		Trading   struct{ DryRun bool }
		Schedule  Schedule
		Markets   map[string]Market
		Products  map[string]Product
		Algorithm map[string]float64
	}{old.Controller.BaseURL, struct{ DryRun bool }{true}, old.Schedule, old.Markets, old.Products, old.Algorithm}
	raw, _ := json.Marshal(expected)
	digest := sha256.Sum256(raw)
	if signature != hex.EncodeToString(digest[:]) {
		t.Fatal("旧版と異なる指紋を生成しています")
	}
	for _, invalid := range []string{
		strings.Replace(minimumConfig, "[markets.TOPIX]\nenabled = true", "[market]", 1),
		minimumConfig + "\n[trading]\ninstance_id=''\n",
	} {
		if _, _, err := ReadLegacyForMigration(writeConfiguration(t, invalid)); err == nil {
			t.Fatal("書き換え後の新設定を旧設定として受け付けました")
		}
	}
}

// TestStartupValidation は起動動作をTOMLだけで選び、危険な指定の組合せを拒否します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestStartupValidation(t *testing.T) {
	for _, startup := range []string{
		"action='unknown'", "action=''", "action='run'\nduration_seconds=-1",
		"action='run'\nduration_seconds=nan", "action='run'\nduration_seconds=inf",
		"action='run'\nduration_seconds=9223372037", "action='check'\nduration_seconds=1",
		"action='probe'\nduration_seconds=1", "action='migrate_legacy'\nlegacy_conf='conf-old'\nduration_seconds=1",
		"action='migrate_legacy'", "action='migrate_legacy'\nlegacy_conf='  '",
		"action='run'\nlegacy_conf='conf-old'", "action='check'\nlegacy_conf='conf-old'",
		"action='migrate_legacy'\nlegacy_conf=' conf-old'",
	} {
		if _, err := Load(writeConfiguration(t, minimumConfig+"\n[startup]\n"+startup+"\n")); err == nil {
			t.Fatalf("不正な起動指定を受け付けました: %s", startup)
		}
	}
	if _, err := Load(writeConfiguration(t, minimumConfig+"\n[trading]\ndryrun=false\n[startup]\naction='run'\nduration_seconds=1\n")); err == nil {
		t.Fatal("実売買を時間指定で起動できてしまいます")
	}
	base, err := Load(writeConfiguration(t, minimumConfig))
	if err != nil || base.Startup.Action != "run" || base.Startup.DurationSeconds != 0 {
		t.Fatalf("旧設定で既定の連続起動を保てません: %v", err)
	}
	for _, startup := range []string{
		"action='run'\nduration_seconds=0.001", "action='check'", "action='probe'",
		"action='migrate_legacy'\nlegacy_conf='conf-old'",
	} {
		cfg, err := Load(writeConfiguration(t, minimumConfig+"\n[startup]\n"+startup+"\n"))
		if err != nil || cfg.Signature() != base.Signature() {
			t.Fatalf("起動動作変更が戦略復元指紋を変えました: %s / %v", startup, err)
		}
	}
}
