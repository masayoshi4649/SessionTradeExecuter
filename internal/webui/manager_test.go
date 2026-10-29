package webui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

// testConfig は実通信をしない5商品設定を一時領域に読み込みます。
//
// 引数: t(*testing.T)。返り値: config.Config。
func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "conf")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	text.WriteString("[web]\nhost='127.0.0.1'\nport=8090\n[trading]\ndryrun=true\n")
	for _, code := range []string{"NK225", "NK225mini", "NK225micro", "TOPIX", "TOPIXmini"} {
		day, night := "TG04", "TG04"
		if config.Reference(code) == "TOPIX" {
			day, night = "T06", "T04"
		}
		text.WriteString("[[instruments]]\nproduct='" + code + "'\nmax_per_side=10\nenabled=false\nday='" + day + "'\nnight='" + night + "'\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.toml"), []byte(text.String()), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// until は非同期通知を短い上限内で待ちます。
//
// 引数: t(*testing.T)、condition(func()bool)。返り値: なし。
func until(t *testing.T, condition func() bool) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("非同期処理が完了しませんでした")
}

// TestIndependentOffKeepsAuditUntilDetached は5商品を同時に管理し、OFFの取消照合中は旧所有情報が残ることを検査します。
//
// 引数: t(*testing.T)。返り値: なし。実APIへは接続しません。
func TestIndependentOffKeepsAuditUntilDetached(t *testing.T) {
	cfg := testConfig(t)
	var calls atomic.Int32
	controls := make(chan *runner.Control, 5)
	m, err := NewManager(cfg, func(ctx context.Context, c config.Config, control *runner.Control, publish func(runner.Snapshot)) error {
		calls.Add(1)
		_, market := c.Selected()
		publish(runner.Snapshot{Product: market.Product, BuyQuantity: 2, RealizedYen: 1234})
		controls <- control
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.StartConfigured(); err != nil || calls.Load() != 0 {
		t.Fatal("初期OFFで売買を始めました", err)
	}
	for _, item := range cfg.Instruments {
		if err = m.SetEnabled(item.Product, true, "試験"); err != nil {
			t.Fatal(err)
		}
	}
	until(t, func() bool { return len(controls) == 5 })
	if err = m.SetEnabled("NK225mini", false, "試験"); err != nil {
		t.Fatal(err)
	}
	s := m.State()
	for _, p := range s.Products {
		if p.Snapshot == nil || p.Snapshot.BuyQuantity != 2 {
			t.Fatal("保有状態が消えました", p)
		}
		if p.Enabled != (p.Product != "NK225mini") {
			t.Fatal("他商品のON/OFFが変わりました", p)
		}
	}
	if err = m.SetEnabled("NK225mini", true, "試験"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 {
		t.Fatal("旧運転の終了前に新しい運転を開始しました")
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	if m.SetEnabled("NK225mini", true, "試験") == nil {
		t.Fatal("終了後の新規を許可しました")
	}
	raw, err := os.ReadFile(filepath.Join(cfg.Root, "log", "web", "操作.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatal("操作記録が不正です", err)
		}
	}
}

// TestWorkerFailureIsVisibleAndDoesNotRestart は一商品の故障が他商品を止めず、自動再送もしないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestWorkerFailureIsVisibleAndDoesNotRestart(t *testing.T) {
	cfg := testConfig(t)
	m, err := NewManager(cfg, func(ctx context.Context, c config.Config, _ *runner.Control, _ func(runner.Snapshot)) error {
		_, market := c.Selected()
		if market.Product == "NK225mini" {
			return errors.New("照合不整合の試験")
		}
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	_ = m.SetEnabled("NK225mini", true, "試験")
	_ = m.SetEnabled("TOPIXmini", true, "試験")
	until(t, func() bool { return m.State().Products[1].Error != "" })
	if m.SetEnabled("NK225mini", true, "試験") == nil {
		t.Fatal("結果不明の運転を自動再起動しました")
	}
	if !m.State().Products[4].Enabled {
		t.Fatal("他商品が停止しました")
	}
	if m.Close() == nil {
		t.Fatal("商品の終了エラーが消えました")
	}
}

// TestAuditFailureStillAllowsOff は記録先障害でも新規の停止を妨げないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。ONは記録不能なら拒否します。
func TestAuditFailureStillAllowsOff(t *testing.T) {
	m, err := NewManager(testConfig(t), func(ctx context.Context, _ config.Config, _ *runner.Control, _ func(runner.Snapshot)) error {
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err = m.SetEnabled("NK225mini", true, "試験"); err != nil {
		t.Fatal(err)
	}
	if err = m.audit.Close(); err != nil {
		t.Fatal(err)
	}
	if err = m.SetEnabled("NK225mini", false, "試験"); err == nil {
		t.Fatal("保存失敗を隠しました")
	}
	if m.State().Products[1].Enabled || m.products[1].control.Enabled() {
		t.Fatal("記録障害でOFFを拒否しました")
	}
	if m.SetEnabled("NK225mini", true, "試験") == nil {
		t.Fatal("記録できないONを許可しました")
	}
}
