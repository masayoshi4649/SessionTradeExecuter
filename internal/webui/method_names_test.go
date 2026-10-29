package webui

import (
	"context"
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

// TestDisplayQualifiedMethodNames は旧短ID設定でも画面へ銘柄付き手法名を渡すことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。運転開始やAPI接続は行いません。
func TestDisplayQualifiedMethodNames(t *testing.T) {
	m, err := NewManager(testConfig(t), func(context.Context, config.Config, *runner.Control, func(runner.Snapshot)) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, product := range m.State().Products {
		prefix := config.Reference(product.Product) + "_"
		if !strings.HasPrefix(product.DayMethod, prefix) || !strings.HasPrefix(product.NightMethod, prefix) {
			t.Fatal("画面の手法名に判断銘柄がありません", product)
		}
	}
}
