// SessionTradeExecuter は設定を検査し、停止信号を連続運転へ渡します。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/cli"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/diagnostics"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/exitreport"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/webui"
)

// main は OS 共通の起動入口です。
//
// 引数: なし。実行ファイル隣のconfを使用し、コマンドライン引数は拒否します。
// 返り値: なし。設定・運転の失敗は終了コード 1 です。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	executable, err := os.Executable()
	if err == nil {
		err = cli.Run(ctx, executable, os.Args[1:], os.Stdout, os.Stderr, cli.Operations{Run: runner.Run, Web: webui.Run, Probe: runner.Probe, Migrate: runner.MigrateLegacy, Diagnostics: diagnostics.Export})
	}

	// ----------------------------------------
	if err != nil {
		// 運転の後片付けが終わってから表示を保持し、待機中の Ctrl+C も有効にします。
		stop()
		exitreport.Report(executable, err)
		os.Exit(1)
	}
}
