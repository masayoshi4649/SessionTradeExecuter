package strategy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// allRulesExternalExpressions は商品・昼夜・手法に対応する実TOMLを厳格に読み込みます。
//
// 引数: t(*testing.T)、symbol/kind/method(string)。返り値: map[string]stringは外部条件です。
func allRulesExternalExpressions(t *testing.T, symbol, kind, method string) map[string]string {
	t.Helper()
	directory := os.Getenv("SESSIONTRADER_ALL_RULES_DIRECTORY")
	if directory == "" {
		return nil
	}
	profile := strings.ToLower(method)
	if method == "T06" {
		profile += "_" + strings.ToLower(symbol)
	}
	loaded, err := rules.LoadFor(filepath.Join(directory, rules.FileName(profile)), profile)
	if err != nil {
		t.Fatal(err)
	}
	return loaded.Expressions()
}
