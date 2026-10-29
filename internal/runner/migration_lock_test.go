package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
)

// lockMetadataDatabase は実口座状態を含まない移行元ロック検査用の正本を作成します。
//
// 引数: t(*testing.T)、source(string) は旧保存先です。返り値: string は新SQLiteの絶対パスです。
func lockMetadataDatabase(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "state.sqlite")
	store, err := journal.Open(path, filepath.Join(root, "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := map[string]any{"runtime_state": savedRuntime{LegacyDataDirectory: source}}
	if err = store.Save(state, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestMigratedProductsShareLegacyLock は二つの移行先が同じ旧保存先を共有し、旧版だけを拒否することを検査します。
//
// 引数: t(*testing.T) は検査環境です。返り値: なし。
func TestMigratedProductsShareLegacyLock(t *testing.T) {
	source := t.TempDir()
	firstDB := lockMetadataDatabase(t, source)
	secondDB := lockMetadataDatabase(t, source)
	first, err := acquireMigratedLegacyLock(firstDB, filepath.Dir(firstDB))
	if err != nil || first == nil {
		t.Fatal("一商品目で旧版排他を確保できません", err)
	}
	defer first.Close()
	second, err := acquireMigratedLegacyLock(secondDB, filepath.Dir(secondDB))
	if err != nil || second == nil {
		t.Fatal("二商品目の共有が拒否されました", err)
	}
	defer second.Close()
	if old, err := acquireLock(source); err == nil {
		_ = old.Close()
		t.Fatal("移行先運転中に旧版を起動できました")
	}
	_ = first.Close()
	if old, err := acquireLock(source); err == nil {
		_ = old.Close()
		t.Fatal("一商品だけ停止した段階で旧版を起動できました")
	}
	_ = second.Close()
	old, err := acquireLock(source)
	if err != nil {
		t.Fatal("全移行先終了後にロックが解放されません", err)
	}
	_ = old.Close()
}

// TestMigrationLockSameDirectoryAndLegacyCompatibility は同じ保存先のロック兼用と過去形式の互換を検査します。
//
// 引数: t(*testing.T) は検査環境です。返り値: なし。
func TestMigrationLockSameDirectoryAndLegacyCompatibility(t *testing.T) {
	source := t.TempDir()
	baseLock, err := acquireSharedLock(source)
	if err != nil {
		t.Fatal(err)
	}
	defer baseLock.Close()
	path := lockMetadataDatabase(t, source)
	if same, err := acquireMigratedLegacyLock(path, source); err != nil || same != nil {
		if same != nil {
			_ = same.Close()
		}
		t.Fatal("同じ保存先のロックを二重取得しました", err)
	}
	oldFormat := lockMetadataDatabase(t, "")
	if old, err := acquireMigratedLegacyLock(oldFormat, t.TempDir()); err != nil || old != nil {
		if old != nil {
			_ = old.Close()
		}
		t.Fatal("旧形式の状態を読み取れません", err)
	}
	invalid := lockMetadataDatabase(t, "relative-source")
	if _, err := acquireMigratedLegacyLock(invalid, t.TempDir()); err == nil {
		t.Fatal("相対移行元をCWD基準で採用しました")
	}
}

// TestRunRejectsActiveLegacyBeforeStateMutation は別ルート移行後の旧版先行起動を、注文復旧より先に拒否します。
//
// 引数: t(*testing.T) は検査環境です。返り値: なし。
func TestRunRejectsActiveLegacyBeforeStateMutation(t *testing.T) {
	f := legacyMigrationFixture(t)
	f.Config.Root = t.TempDir()
	if err := MigrateLegacy(f.Config, f.LegacyDirectory); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.Config.Path(f.Config.Runtime.DataDirectory), "dryrun", f.Config.InstanceID(), "TOPIXmini.sqlite")
	before, _ := readMigrationSnapshot(t, target)
	old, err := acquireLock(filepath.Dir(f.Source))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = Run(ctx, f.Config); err == nil || !strings.Contains(err.Error(), "移行元の旧版運転") {
		t.Fatal("旧版の運転を台帳復旧前に検出しませんでした", err)
	}
	after, _ := readMigrationSnapshot(t, target)
	if string(before) != string(after) {
		t.Fatal("旧版運転中に移行先の約定又は所有状態を更新しました")
	}
	_ = old.Close()
	// ----------------------------------------
	// 設定が違っても新規起動するが、終了時には移行元ロックを解放します。
	f.Config.Algorithm = map[string]float64{"day.risk_floor": .8}
	if err = Run(ctx, f.Config); err != nil {
		t.Fatal("新規起動なのに旧設定との不一致で停止しました", err)
	}
	released, err := acquireLock(filepath.Dir(f.Source))
	if err != nil {
		t.Fatal("終了時に移行元ロックが残りました", err)
	}
	_ = released.Close()
}
