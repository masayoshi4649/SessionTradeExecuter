// Package cli は実行ファイル隣のconfだけを読み、起動動作と昼夜の戦略設定を検査します。
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/e01"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// Operations は実行入口と、変更 API を呼ばない接続検査です。
type Operations struct {
	Run         func(context.Context, config.Config) error
	Web         func(context.Context, config.Config) error
	Probe       func(context.Context, config.Config) error
	Migrate     func(config.Config, string) error
	Diagnostics func(context.Context, config.Config) ([]string, error)
}

// Run は設定を検査して、指定された動作一つだけを実行します。
//
// 引数: ctx context.Context は停止信号、executablePath string は実行ファイル、args []string は拒否する引数、out/errOut io.Writer は表示先、operations Operations は実行処理です。
// 返り値: error は不正設定または実行の失敗です。終了時の返済は runner.Run が担当します。
func Run(ctx context.Context, executablePath string, args []string, out, errOut io.Writer, operations Operations) error {
	if len(args) != 0 {
		return fmt.Errorf("起動引数は使用できません。実行ファイル隣のconf/*.tomlでstartup.action等を設定し、引数なしで起動してください")
	}
	if executablePath == "" || !filepath.IsAbs(executablePath) {
		return fmt.Errorf("実行ファイルの絶対パスを確認できません")
	}
	executable, err := filepath.EvalSymlinks(executablePath)
	if err != nil {
		return fmt.Errorf("実行ファイルの場所を解決できません: %w", err)
	}
	stat, err := os.Stat(executable)
	if err != nil || !stat.Mode().IsRegular() {
		return fmt.Errorf("実行ファイルを確認できません: %s / %v", executable, err)
	}
	directory := filepath.Join(filepath.Dir(executable), "conf")
	cfg, err := config.Load(directory)
	if err != nil {
		return err
	}
	if err = ValidateAlgorithms(cfg); err != nil {
		return err
	}
	if cfg.Web != nil {
		return runWeb(ctx, cfg, out, operations)
	}
	if cfg.Startup.Action == "diagnostics" {
		if operations.Diagnostics == nil {
			return fmt.Errorf("診断書出しの処理がありません")
		}
		paths, err := operations.Diagnostics(ctx, cfg)
		if err != nil {
			return err
		}
		for _, path := range paths {
			fmt.Fprintf(out, "診断ZIP: %s\n", path)
		}
		fmt.Fprintln(out, "診断書出しが完了しました。API接続・注文・取消・返済は行っていません。")
		return nil
	}
	if cfg.Startup.Action == "check" {
		fmt.Fprintf(out, "設定正常: dryrun=%t / 実行ID=%s\n", cfg.Trading.DryRun, cfg.InstanceID())
		reference, market := cfg.Selected()
		fmt.Fprintf(out, "%s / %s / 片方向上限 %d 枚\n", reference, market.Product, market.MaxPerSide)
		if len(cfg.Windows) > 0 {
			printMethodWindows(out, cfg, reference)
		} else {
			for _, kind := range []string{"Day", "Night"} {
				method := cfg.MethodID(kind)
				if method == "TG04" || method == "PG04" || method == "D07" {
					allocation := strategy.ResearchAllocation(method, market.MaxPerSide)
					fmt.Fprintf(out, "%s %s / 基本最大%d枚・追加最大%d枚・別入口最大%d枚\n", kind, cfg.MethodName(kind), allocation["core"], allocation["addon"], allocation["episode"])
				} else {
					fmt.Fprintf(out, "%s %s / 基準 %d 枚・未参加補完最大 %d 枚\n", kind, cfg.MethodName(kind), market.CoreQuantity, market.ExtraQuantity)
				}
			}
			fmt.Fprintf(out, "手法: 昼=%s / 夜=%s\n", cfg.MethodName("Day"), cfg.MethodName("Night"))
		}
		fmt.Fprintln(out, "観測は二市場のラージ、売買は上記一商品のみです。")
		return nil
	}
	if cfg.Startup.Action == "migrate_legacy" {
		if operations.Migrate == nil {
			return fmt.Errorf("旧版移行の処理がありません")
		}
		if err := operations.Migrate(cfg, cfg.Path(cfg.Startup.LegacyConf)); err != nil {
			return err
		}
		fmt.Fprintf(out, "旧版台帳の移行完了: 実行ID=%s。実注文・取消・返済は送っていません。\n", cfg.InstanceID())
		return nil
	}
	if cfg.Startup.Action == "probe" {
		if operations.Probe == nil {
			return fmt.Errorf("接続検査の処理がありません")
		}
		cfg.Trading.DryRun = true
		return operations.Probe(ctx, cfg)
	}
	if operations.Run == nil {
		return fmt.Errorf("運転処理がありません")
	}
	if cfg.Startup.DurationSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.Startup.DurationSeconds*float64(time.Second)))
		defer cancel()
	}
	return operations.Run(ctx, cfg)
}

// ValidateAlgorithms は両市場の Day/Night 既定値へ上書きを適用して検査します。
//
// 引数: cfg config.Config は構文検査済みの設定です。
// 返り値: error は未知キー・数値範囲・特徴量・手法構成の不整合です。市場データは読みません。
func ValidateAlgorithms(cfg config.Config) error {
	if cfg.Web != nil {
		workers, err := cfg.WorkerConfigs()
		if err != nil {
			return err
		}
		for _, worker := range workers {
			if err := ValidateAlgorithms(worker); err != nil {
				return err
			}
		}
		return nil
	}
	for _, reference := range []string{"TOPIX", "NK225"} {
		market, exists := cfg.Markets[reference]
		if !exists || !market.Enabled {
			continue
		}
		for _, window := range cfg.MethodWindows() {
			if window.Method == "" {
				continue
			}
			kind := window.Session
			method, err := config.ShortMethodID(reference, window.Method)
			if err != nil {
				return err
			}
			parameters, err := strategy.ParametersForMethod(reference, kind, method)
			if err != nil {
				return err
			}
			if parameters.Method != "" && len(cfg.Algorithm) > 0 {
				return fmt.Errorf("%sは固定仕様です。旧algorithm係数の上書きは外してください", parameters.Method)
			}
			parameters.Extra.EntryCutoffSeconds = cfg.Schedule.ExtraCutoffSeconds
			if err := strategy.ApplyOverrides(&parameters, cfg.Algorithm); err != nil {
				return fmt.Errorf("%s/%s の戦略設定: %w", reference, kind, err)
			}
			strategyConfig := strategy.Config{Reference: reference, Product: market.Product,
				CoreQuantity: market.CoreQuantity, ExtraMaxQuantity: market.ExtraQuantity,
				Cap: market.MaxPerSide, LargeTick: cfg.Products[reference].Tick,
				PointValue: cfg.Products[market.Product].PointValue, Parameters: parameters, RuleExpressions: cfg.RuleExpressionsForMethod(reference, kind, method)}
			// 構成検査用の架空時刻です。米国前専用手法にも有効な前後関係を与え、市場予定は書き換えません。
			session := strategy.Session{ID: "設定検査", Kind: kind, Start: 1, ForceClose: 7200, End: 7800}
			if kind == "Night" {
				session.USOpen, session.ExtraAt = 3600, 5400
			}
			if _, err := strategy.New(strategyConfig, session); err != nil {
				return fmt.Errorf("%s/%s の手法構成: %w", reference, kind, err)
			}
			ticks := map[string]float64{"TOPIX": cfg.Products["TOPIX"].Tick, "NK225": cfg.Products["NK225"].Tick}
			if _, err := e01.NewFeatures("設定検査", 1, ticks, parameters.Features); err != nil {
				return fmt.Errorf("%s/%s の特徴量: %w", reference, kind, err)
			}
		}
	}
	return nil
}

// printMethodWindows は旧昼夜欄より優先する実際の時間枠を設定検査へ表示します。
//
// 引数: out(io.Writer)は出力、cfg(config.Config)は単品設定、reference(string)は判断市場です。
// 返り値: なし。米国相対時計は設定表記で示し、夏時間へ追従する意味を維持します。
func printMethodWindows(out io.Writer, cfg config.Config, reference string) {
	for _, window := range cfg.MethodWindows() {
		method := config.NameOfMethod(reference, window.Method)
		if method == "" {
			method = "静観"
		}
		fmt.Fprintf(out, "時間帯 %s / %s %s〜%s / %s\n", window.ID, window.Session, window.Start, window.End, method)
	}
}

// runWeb は単品運転と同じ検査・診断を全商品に適用し、運転時だけWebを待ち受けます。
//
// 引数: ctx(context.Context)、cfg(config.Config)、out(io.Writer)、operations(Operations)。
// 返り値: errorは設定・接続・運転の失敗です。引数による設定変更は受け付けません。
func runWeb(ctx context.Context, cfg config.Config, out io.Writer, operations Operations) error {
	workers, err := cfg.WorkerConfigs()
	if err != nil {
		return err
	}
	switch cfg.Startup.Action {
	case "check":
		fmt.Fprintf(out, "Web設定正常: %s:%d / dryrun=%t\n", cfg.Web.Host, cfg.Web.Port, cfg.Trading.DryRun)
		for i, worker := range workers {
			reference, market := worker.Selected()
			if len(worker.Windows) > 0 {
				fmt.Fprintf(out, "%s / 上限%d枚 / 初期モード=%s\n", market.Product, market.MaxPerSide, config.ModeOf(cfg.Instruments[i], cfg.Trading.DryRun))
				printMethodWindows(out, worker, reference)
			} else {
				fmt.Fprintf(out, "%s / 上限%d枚 / 昼%s・夜%s / 初期ON=%t\n", market.Product, market.MaxPerSide,
					worker.MethodName("Day"), worker.MethodName("Night"), cfg.Instruments[i].Enabled)
			}
		}
		return nil
	case "probe":
		if operations.Probe == nil {
			return fmt.Errorf("接続検査の処理がありません")
		}
		for _, worker := range workers {
			worker.Trading.DryRun = true
			if err := operations.Probe(ctx, worker); err != nil {
				return err
			}
		}
		return nil
	case "diagnostics":
		if operations.Diagnostics == nil {
			return fmt.Errorf("診断書出しの処理がありません")
		}
		for _, worker := range workers {
			paths, err := operations.Diagnostics(ctx, worker)
			if err != nil {
				return err
			}
			for _, path := range paths {
				fmt.Fprintf(out, "診断ZIP: %s\n", path)
			}
		}
		return nil
	case "run":
		if operations.Web == nil {
			return fmt.Errorf("Web運転の処理がありません")
		}
		if cfg.Startup.DurationSeconds > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.Startup.DurationSeconds*float64(time.Second)))
			defer cancel()
		}
		return operations.Web(ctx, cfg)
	default:
		return fmt.Errorf("Web版では旧版移行を実行できません")
	}
}
