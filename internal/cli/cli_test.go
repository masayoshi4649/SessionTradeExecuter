package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
)

const testConfiguration = `
[trading]
dryrun=true
[markets.TOPIX]
enabled=true
product="TOPIXmini"
max_per_side=10
[products.TOPIXmini]
tick=0.25
point_value=1000
[products.TOPIX]
tick=0.5
point_value=10000
[products.NK225]
tick=10
point_value=1000
`

// configuration は実行ファイルと隣接するconfを一時領域に作成します。
//
// 引数: t *testing.T は試験、body string はTOML本文です。
// 返り値: string は実行ファイルの絶対パスです。
func configuration(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	conf := filepath.Join(root, "conf")
	if err := os.MkdirAll(conf, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "config.toml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "sessiontrader.exe")
	if err := os.WriteFile(executable, []byte("実行ファイル位置の試験用"), 0600); err != nil {
		t.Fatal(err)
	}
	return executable
}

// TestCheckAndProbeDoNotTrade はTOMLで選ぶ設定検査と接続検査が実売買を開始しないことを検証します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestCheckAndProbeDoNotTrade(t *testing.T) {
	base := strings.Replace(testConfiguration, "dryrun=true", "dryrun=false", 1)
	runCalls, probeCalls := 0, 0
	ops := Operations{
		Run: func(context.Context, config.Config) error { runCalls++; return nil },
		Probe: func(_ context.Context, cfg config.Config) error {
			probeCalls++
			if !cfg.Trading.DryRun {
				t.Fatal("接続検査で実売買を許可しました")
			}
			return nil
		},
	}
	var out bytes.Buffer
	executable := configuration(t, base+"\n[startup]\naction='check'\n")
	if err := Run(context.Background(), executable, nil, &out, &out, ops); err != nil {
		t.Fatal(err)
	}
	if runCalls != 0 || probeCalls != 0 || !strings.Contains(out.String(), "基準 5 枚") {
		t.Fatal("設定検査で接続したか自動枚数が不正です")
	}
	executable = configuration(t, base+"\n[startup]\naction='probe'\n")
	if err := Run(context.Background(), executable, nil, &out, &out, ops); err != nil {
		t.Fatal(err)
	}
	if runCalls != 0 || probeCalls != 1 {
		t.Fatal("接続検査で運転入口を呼びました")
	}
}

// TestResearchCheckShowsRealAllocation は旧半分配分でなく新手法の実配分を表示することを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearchCheckShowsRealAllocation(t *testing.T) {
	body := "[trading]\ndryrun=false\n[market]\nproduct='NK225mini'\nmax_per_side=11\n[methods]\nday='TG04'\nnight='PG04'\n[startup]\naction='check'\n"
	var out bytes.Buffer
	calls := 0
	ops := Operations{
		Run:   func(context.Context, config.Config) error { calls++; return nil },
		Probe: func(context.Context, config.Config) error { calls++; return nil },
	}
	if err := Run(context.Background(), configuration(t, body), nil, &out, &out, ops); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || !strings.Contains(out.String(), "Day NK225_TG04 / 基本最大6枚・追加最大3枚・別入口最大2枚") ||
		!strings.Contains(out.String(), "Night NK225_PG04 / 基本最大6枚・追加最大0枚・別入口最大5枚") {
		t.Fatalf("設定検査の接続回数%d、表示%s", calls, out.String())
	}
}

// TestDiagnosticsNeverRunsTrading は実注文設定でも診断が売買・接続入口を呼ばないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestDiagnosticsNeverRunsTrading(t *testing.T) {
	body := strings.Replace(testConfiguration, "dryrun=true", "dryrun=false", 1) + "\n[startup]\naction='diagnostics'\ndiagnostic_session='2026-10-26_Day'\n"
	calls, diagnostics := 0, 0
	var out bytes.Buffer
	ops := Operations{
		Run:     func(context.Context, config.Config) error { calls++; return nil },
		Probe:   func(context.Context, config.Config) error { calls++; return nil },
		Migrate: func(config.Config, string) error { calls++; return nil },
		Diagnostics: func(_ context.Context, cfg config.Config) ([]string, error) {
			diagnostics++
			if cfg.Startup.DiagnosticSession != "2026-10-26_Day" || cfg.Trading.DryRun {
				t.Fatal("診断対象場または実/仮想保存先を変えました")
			}
			return []string{"log/diagnostics/試験.zip"}, nil
		},
	}
	if err := Run(context.Background(), configuration(t, body), nil, &out, &out, ops); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || diagnostics != 1 || !strings.Contains(out.String(), "試験.zip") {
		t.Fatal(calls, diagnostics, out.String())
	}
}

// TestDurationOnlyDryRun は設定による運転秒数が仮想運転にだけ適用されることを確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestDurationOnlyDryRun(t *testing.T) {
	var out bytes.Buffer
	startup := "\n[startup]\naction='run'\nduration_seconds=0.001\n"
	calls := 0
	ops := Operations{Run: func(ctx context.Context, _ config.Config) error {
		calls++
		if _, exists := ctx.Deadline(); !exists {
			t.Fatal("運転へ期限を渡していません")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
			t.Fatal("期限で停止信号が届きません")
			return nil
		}
	}}
	live := configuration(t, strings.Replace(testConfiguration, "dryrun=true", "dryrun=false", 1)+startup)
	if err := Run(context.Background(), live, nil, &out, &out, ops); err == nil || calls != 0 {
		t.Fatal("実注文で時間指定運転を許可しました")
	}
	dry := configuration(t, testConfiguration+startup)
	if err := Run(context.Background(), dry, nil, &out, &out, ops); err != nil || calls != 1 {
		t.Fatalf("仮想運転の期限が不正です: %v", err)
	}
}

// TestAlgorithmTyposAndArguments は誤記と全ての起動引数を運転開始前に拒否します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestAlgorithmTyposAndArguments(t *testing.T) {
	for _, addition := range []string{
		"\n[algorithm]\n\"night.unknown\"=1\n",
		"\n[algorithm]\n\"night.retest\"=0.5\n",
		"\n[algorithm]\n\"night.stop_r\"=-1\n",
		"\n[algorithm]\n\"features.atr_period\"=0\n",
		"\n[algorithm]\n\"features.ema_slow\"=1.5\n",
	} {
		executable := configuration(t, testConfiguration+addition+"\n[startup]\naction='check'\n")
		var out bytes.Buffer
		if err := Run(context.Background(), executable, nil, &out, &out, Operations{}); err == nil {
			t.Fatal("不正なアルゴリズム設定を受け付けました")
		}
	}
	for _, args := range [][]string{{"-conf", "conf"}, {"-check"}, {"-probe"}, {"-duration", "1s"}, {"-migrate-legacy"}, {"-h"}, {"unexpected"}} {
		var out bytes.Buffer
		if err := Run(context.Background(), "", args, &out, &out, Operations{}); err == nil || !strings.Contains(err.Error(), "起動引数は使用できません") {
			t.Fatalf("引数を拒否して設定方法へ案内しません: %v", err)
		}
	}
}

// TestSingleProductDisplay は売買対象と実行IDを一商品だけ表示することを検証します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestSingleProductDisplay(t *testing.T) {
	body := strings.Replace(testConfiguration, "[markets.TOPIX]\nenabled=true", "[market]", 1)
	body = strings.Replace(body, "dryrun=true", "dryrun=true\ninstance_id='TOPIX-mini'", 1)
	var out bytes.Buffer
	if err := Run(context.Background(), configuration(t, body+"\n[startup]\naction='check'\n"), nil, &out, &out, Operations{}); err != nil {
		t.Fatal(err)
	}
	result := out.String()
	if strings.Count(result, "片方向上限") != 1 || !strings.Contains(result, "実行ID=topix-mini") || !strings.Contains(result, "TOPIX / TOPIXmini") || strings.Contains(result, "NK225 /") {
		t.Fatalf("単一商品の表示が不正です: %s", result)
	}
	if !strings.Contains(result, "手法: 昼=TOPIX_T06 / 夜=TOPIX_T04") {
		t.Fatalf("実効手法IDを表示しません: %s", result)
	}
	other := "\n[markets.NK225]\nenabled=true\nproduct='NK225'\nmax_per_side=20\n"
	called := false
	ops := Operations{Run: func(context.Context, config.Config) error { called = true; return nil }}
	if err := Run(context.Background(), configuration(t, testConfiguration+other), nil, &out, &out, ops); err == nil || called {
		t.Fatalf("二商品設定で運転しました: %v", err)
	}
}

// TestMethodSelectionStopsBeforeOperations は未知IDと昼夜・銘柄違いを接続・運転・移行の前に拒否します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
func TestMethodSelectionStopsBeforeOperations(t *testing.T) {
	nk := strings.Replace(testConfiguration, "[markets.TOPIX]", "[markets.NK225]", 1)
	nk = strings.Replace(nk, `product="TOPIXmini"`, `product="NK225micro"`, 1)
	nk += "\n[products.NK225micro]\ntick=5\npoint_value=10\n"
	for _, test := range []struct{ name, body, methods string }{
		{"未知", testConfiguration, "day='unknown'\nnight='T04'"},
		{"昼夜違い", testConfiguration, "day='T04'\nnight='T04'"},
		{"TOPIXの夜に日経用", testConfiguration, "day='T06'\nnight='U06'"},
		{"日経の夜にTOPIX用", nk, "day='T06'\nnight='T04'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, action := range []string{"run", "check", "probe", "migrate_legacy"} {
				body := test.body + "\n[methods]\n" + test.methods + "\n[startup]\naction='" + action + "'\n"
				if action == "migrate_legacy" {
					body += "legacy_conf='old-conf'\n"
				}
				called := false
				ops := Operations{
					Run:     func(context.Context, config.Config) error { called = true; return nil },
					Probe:   func(context.Context, config.Config) error { called = true; return nil },
					Migrate: func(config.Config, string) error { called = true; return nil },
				}
				var out bytes.Buffer
				err := Run(context.Background(), configuration(t, body), nil, &out, &out, ops)
				if err == nil || called || !strings.Contains(err.Error(), "手法ID") {
					t.Fatalf("%s の不正手法を実行前に拒否しません: %v", action, err)
				}
			}
		})
	}
}

// TestMigrationIsOffline は設定で選んだ移行が旧confの相対パスを実行ファイル基準で解決することを確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestMigrationIsOffline(t *testing.T) {
	executable := configuration(t, testConfiguration+"\n[startup]\naction='migrate_legacy'\nlegacy_conf='conf-legacy'\n")
	var out bytes.Buffer
	calls := 0
	ops := Operations{
		Run: func(context.Context, config.Config) error {
			t.Fatal("移行で運転を開始しました")
			return nil
		},
		Probe: func(context.Context, config.Config) error { t.Fatal("移行で接続しました"); return nil },
		Migrate: func(cfg config.Config, legacy string) error {
			calls++
			if legacy != filepath.Join(filepath.Dir(executable), "conf-legacy") || cfg.InstanceID() != "topixmini" {
				t.Fatal("移行先設定と旧設定フォルダが不正です")
			}
			return nil
		},
	}
	if err := Run(context.Background(), executable, nil, &out, &out, ops); err != nil || calls != 1 {
		t.Fatalf("オフライン移行が不正です: %v", err)
	}
}

// TestExecutableDirectoryWinsOverWorkingDirectory は作業ディレクトリ側のconfを一切参照しないことを確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestExecutableDirectoryWinsOverWorkingDirectory(t *testing.T) {
	executable := configuration(t, strings.Replace(testConfiguration, "dryrun=true", "dryrun=true\ninstance_id='own-folder'", 1))
	other := configuration(t, strings.Replace(testConfiguration, "dryrun=true", "dryrun=false\ninstance_id='wrong-folder'", 1))
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Dir(other)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	var out bytes.Buffer
	calls := 0
	ops := Operations{Run: func(_ context.Context, cfg config.Config) error {
		calls++
		if !cfg.Trading.DryRun || cfg.InstanceID() != "own-folder" || cfg.Root != filepath.Dir(executable) || cfg.Path("data") != filepath.Join(filepath.Dir(executable), "data") {
			t.Fatal("作業ディレクトリ側の設定または保存先を使用しました")
		}
		return nil
	}}
	if err := Run(context.Background(), executable, nil, &out, &out, ops); err != nil || calls != 1 {
		t.Fatalf("実行ファイル隣の設定を読めません: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "sessiontrader.exe")
	if err := os.WriteFile(missing, []byte("試験"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), missing, nil, &out, &out, ops); err == nil || calls != 1 {
		t.Fatal("隣にconfがないのに作業ディレクトリへフォールバックしました")
	}
}

// TestTwoExecutableFoldersAreIndependent は二つの配布フォルダの銘柄・保存先を独立して読むことを確認します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。
func TestTwoExecutableFoldersAreIndependent(t *testing.T) {
	nk := strings.Replace(testConfiguration, "[markets.TOPIX]", "[markets.NK225]", 1)
	nk = strings.Replace(nk, `product="TOPIXmini"`, `product="NK225micro"`, 1)
	nk += "\n[products.NK225micro]\ntick=5\npoint_value=10\n"
	executables := []string{configuration(t, testConfiguration), configuration(t, nk)}
	products := []string{"TOPIXmini", "NK225micro"}
	var out bytes.Buffer
	for index, executable := range executables {
		calls := 0
		ops := Operations{Run: func(_ context.Context, cfg config.Config) error {
			calls++
			_, market := cfg.Selected()
			if market.Product != products[index] || cfg.Root != filepath.Dir(executable) {
				t.Fatal("二つの配布フォルダの設定が混ざりました")
			}
			return nil
		}}
		if err := Run(context.Background(), executable, nil, &out, &out, ops); err != nil || calls != 1 {
			t.Fatalf("独立した起動に失敗しました: %v", err)
		}
	}
}
