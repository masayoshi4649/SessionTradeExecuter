package runner

import (
	"context"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// TestRuleFaultStopsDispatchAndTimer は診断時の評価異常後に待機依頼も送信しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。送信器が存在しなくても送信経路へ到達しません。
func TestRuleFaultStopsDispatchAndTimer(t *testing.T) {
	runtime, err := rules.NewRuntime(nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Bool("存在しない条件", nil)
	if runtime.Err() == nil {
		t.Fatal("評価異常が保持されていません")
	}
	m := &marketRuntime{Portfolio: &strategy.Portfolio{Rules: runtime}}
	m.Saved.Queue = []queuedSignal{{Signal: strategy.Signal{ID: "未送信", Action: "new"}}}
	a := &app{markets: map[string]*marketRuntime{"NK225": m}}
	if err := a.dispatch(context.Background(), m, time.Now()); err == nil {
		t.Fatal("異常後に送信処理へ進みました")
	}
	if err := a.tick(context.Background(), time.Now()); err == nil {
		t.Fatal("異常後に時計処理を継続しました")
	}
}
