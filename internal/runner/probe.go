package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// Probe は設定銘柄・当場足・口座状態・生PUSHを読取専用で確認します。
//
// 引数: ctx(context.Context)、cfg(config.Config)。返り値: errorは通信・登録・応答不整合です。
// 注文・取消の通信機能はdryrun固定とし、状態DBや約定ログは変更しません。
func Probe(ctx context.Context, cfg config.Config) error {
	client, err := controller.New(cfg.Controller.BaseURL, duration(cfg.Controller.RequestTimeoutSeconds))
	if err != nil {
		return err
	}
	client.SetDryRun(true)
	reg, err := client.Registrations(ctx)
	if err != nil {
		return err
	}
	known := map[string]controller.Future{}
	for _, f := range reg.Futures {
		known[f.FutureCode] = f
	}
	wanted := map[string]bool{"TOPIX": true, "NK225": true}
	for _, mc := range cfg.Markets {
		if mc.Enabled {
			wanted[mc.Product] = true
		}
	}
	symbols := map[string]string{}
	for _, code := range []string{"TOPIX", "NK225", "TOPIXmini", "NK225mini", "NK225micro"} {
		if !wanted[code] {
			continue
		}
		f, ok := known[code]
		if !ok || f.Symbol == "" {
			return fmt.Errorf("%s がcontrollerに登録されていません", code)
		}
		symbols[f.Symbol] = code
		fmt.Printf("登録確認: %s / %s / 限月%d\n", code, f.Symbol, f.DerivMonth)
	}
	view, err := client.Orders(ctx, "all")
	if err != nil {
		return err
	}
	if view.Session == nil {
		fmt.Println("API接続確認済み。現在は立会情報がありません。注文・取消は行っていません。")
		return nil
	}
	fmt.Printf("上流接続=%t / 立会=%s / 注文鮮度=%t / 建玉鮮度=%t\n", view.Connected, view.Session.Name, !view.BrokerStale, !view.PositionsStale)
	bundle, err := fetchCandles(ctx, client)
	if err != nil {
		return err
	}
	fmt.Printf("当場=%s / TOPIX確定足%d本 / NK225確定足%d本\n", bundle.One["TOPIX"].Snapshot.SessionID, len(bundle.One["TOPIX"].Data), len(bundle.One["NK225"].Data))
	streamCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	seen := map[string]bool{}
	err = client.Stream(streamCtx, func(b controller.Board) error {
		code, ok := symbols[b.Symbol]
		if !ok {
			return nil
		}
		if !seen[code] {
			fmt.Printf("生PUSH: %s / 買気配%.4f / 売気配%.4f\n", code, b.Buy1.Price, b.Sell1.Price)
			seen[code] = true
			if len(seen) == len(symbols) {
				cancel()
			}
		}
		return nil
	})
	if len(seen) != len(symbols) {
		return fmt.Errorf("生PUSH確認が未完了です: %d/%d商品（%v）", len(seen), len(symbols), err)
	}
	fmt.Println("接続確認完了。実注文・取消は送っていません。")
	return nil
}
