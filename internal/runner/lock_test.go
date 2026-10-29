package runner

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
)

// TestDirectoryLock は同じ復旧先の同時使用を拒否し、終了後に再取得します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestDirectoryLock(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := acquireLock(dir)
	if err == nil {
		_ = second.Close()
		_ = first.Close()
		t.Fatal("同じ保存先で二重起動できました")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := acquireLock(dir)
	if err != nil {
		t.Fatal("終了後にロックを再取得できません:", err)
	}
	_ = third.Close()
}

// TestSharedLocksExcludeLegacyRunner は新版二つを許可し、旧版の排他ロックとは相互に競合することを検査します。
//
// 引数: t(*testing.T) は検査環境です。返り値: なし。
func TestSharedLocksExcludeLegacyRunner(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireSharedLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := acquireSharedLock(dir)
	if err != nil {
		t.Fatal("新版同士の共有ロックが競合しました", err)
	}
	defer second.Close()
	if old, err := acquireLock(dir); err == nil {
		_ = old.Close()
		t.Fatal("新版運転中に旧版の排他ロックを取得できました")
	}
	_ = first.Close()
	_ = second.Close()
	old, err := acquireLock(dir)
	if err != nil {
		t.Fatal("新版終了後の旧版ロック取得に失敗しました", err)
	}
	defer old.Close()
	if newer, err := acquireSharedLock(dir); err == nil {
		_ = newer.Close()
		t.Fatal("旧版運転中に新版の共有ロックを取得できました")
	}
	_ = old.Close()
	last, err := acquireSharedLock(dir)
	if err != nil {
		t.Fatal("旧版終了後の新版ロック取得に失敗しました", err)
	}
	_ = last.Close()
}

// TestProductLocksIgnoreConfigLocation は設定・保存先を変えても同一商品の二重起動を拒否し、別商品は許可します。
//
// 引数: t(*testing.T) は検査環境です。返り値: なし。
func TestProductLocksIgnoreConfigLocation(t *testing.T) {
	namespace, err := newNamespace()
	if err != nil {
		t.Fatal(err)
	}
	firstConfig := config.Defaults()
	firstConfig.Controller.BaseURL = "http://" + namespace + ".invalid"
	firstConfig.Markets["TOPIX"] = config.Market{Enabled: true, Product: "TOPIXmini"}
	firstConfig.Root = t.TempDir()
	firstConfig.Trading.InstanceID = "first"
	first, err := acquireProductLock(firstConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	secondConfig := firstConfig
	secondConfig.Controller.BaseURL = "http://" + strings.ToUpper(namespace) + ".INVALID:80/"
	secondConfig.Root = t.TempDir()
	secondConfig.Runtime.DataDirectory = filepath.Join(secondConfig.Root, "different")
	secondConfig.Trading.InstanceID = "second"
	if productLockDirectory(firstConfig) != productLockDirectory(secondConfig) {
		t.Fatal("同じendpointの表記違いでロック先が変わりました")
	}
	if second, err := acquireProductLock(secondConfig); err == nil {
		_ = second.Close()
		t.Fatal("設定や保存先を変えることで同商品のロックを回避できました")
	}
	otherConfig := secondConfig
	otherConfig.Markets = map[string]config.Market{"NK225": {Enabled: true, Product: "NK225micro"}}
	other, err := acquireProductLock(otherConfig)
	if err != nil {
		t.Fatal("別商品の同時起動を拒否しました", err)
	}
	defer other.Close()
	_ = first.Close()
	resumed, err := acquireProductLock(secondConfig)
	if err != nil {
		t.Fatal("終了後に別設定から同商品を再開できません", err)
	}
	_ = resumed.Close()
	for _, endpoint := range []string{"https://EXAMPLE.invalid", "https://example.invalid:443/"} {
		cfg := firstConfig
		cfg.Controller.BaseURL = endpoint
		firstConfig.Controller.BaseURL = "https://example.invalid"
		if productLockDirectory(cfg) != productLockDirectory(firstConfig) {
			t.Fatal("HTTPS既定ポート又はホスト大小文字を正規化できません")
		}
	}
}
