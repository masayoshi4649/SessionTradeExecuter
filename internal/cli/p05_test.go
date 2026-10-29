package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
)

// TestP05ConfigurationCheckHasValidNightClock は標準P05を外部通信せず生成・検査し実時間帯を表示します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。実注文設定でもcheckは接続しません。
func TestP05ConfigurationCheckHasValidNightClock(t *testing.T) {
	body := "[trading]\ndryrun=false\n[market]\nproduct='NK225mini'\nmax_per_side=11\n[methods]\nday='D07'\nnight='D07'\n[startup]\naction='check'\n"
	var out bytes.Buffer
	calls := 0
	ops := Operations{
		Run:   func(context.Context, config.Config) error { calls++; return nil },
		Probe: func(context.Context, config.Config) error { calls++; return nil },
	}
	if err := Run(context.Background(), configuration(t, body), nil, &out, &out, ops); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"片方向上限 11 枚", "Day SESSION_START〜SESSION_END / NK225_D07", "Night SESSION_START〜US_OPEN-00:15 / NK225_P05", "US_OPEN-00:15〜US_OPEN+00:15 / 静観", "US_OPEN+00:15〜SESSION_END / NK225_D07"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatal("実際に選択した手法・静観・枚数を表示できません", expected, out.String())
		}
	}
	if calls != 0 {
		t.Fatal("設定検査が取引又は接続を呼びました")
	}
}
