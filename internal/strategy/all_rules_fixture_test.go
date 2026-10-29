package strategy

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/schedule"
)

type legacyAllRulesResult struct {
	Session, Symbol, Method string
	Orders                  [][]float64
	Gross                   float64
}

// TestAllRulesLegacyInputs は現行T06/T04/U06を補完担当込みで保存全入力に再生します。
//
// 引数: t(*testing.T)、環境変数SESSIONTRADER_ALL_RULES_REPORTは出力先です。
// 返り値: なし。期待注文は渡さず、凍結旧Goと同一driverの全列を別途比較します。
func TestAllRulesLegacyInputs(t *testing.T) {
	output := os.Getenv("SESSIONTRADER_ALL_RULES_REPORT")
	if output == "" {
		t.Skip("全現行手法の登録済み照合実行ではありません")
	}
	if _, err := os.Stat(os.Getenv("SESSIONTRADER_ALL_RULES_RECEIPT")); err != nil {
		t.Fatal("独立照合の事前登録受領がありません", err)
	}
	dir, scenes := sourceFixtureLocation143(t)
	results := []legacyAllRulesResult{}
	for _, scene := range scenes {
		var input input143
		var legacy sourceFixture143
		jsonFixture143(t, filepath.Join(dir, scene+"_input.json.gz"), &input)
		jsonFixture143(t, filepath.Join(dir, "legacy", scene+"_legacy.json.gz"), &legacy)
		kind := "Day"
		if strings.HasSuffix(scene, "Night") {
			kind = "Night"
		}
		plan, err := schedule.Build(scene, time.Unix(int64(input.Start), 0), time.Unix(int64(input.End+600), 0), config.Defaults().Schedule)
		if err != nil {
			t.Fatal(err)
		}
		for _, symbol := range []string{"TOPIX", "NK225"} {
			t.Run(scene+"/"+symbol, func(t *testing.T) {
				if kind == "Day" && !legacy.GateAvailable[symbol] {
					t.Fatal("当時の入口特徴がありません")
				}
				method := "T06"
				if kind == "Night" {
					method = "T04"
					if symbol == "NK225" {
						method = "U06"
					}
				}
				params, err := ParametersForMethod(symbol, kind, method)
				if err != nil {
					t.Fatal(err)
				}
				product, tick, point, feeRate := "TOPIXmini", .5, 1000., 41.8
				extra := plan.TopixExtra
				if symbol == "NK225" {
					product, tick, point, feeRate, extra = "NK225mini", 10, 100, 38.5, plan.NKExtra
				}
				session := Session{ID: scene, Kind: kind, Start: input.Start, ForceClose: input.End, End: input.End + 600, USOpen: float64(plan.USOpen.Unix()), ExtraAt: float64(extra.Unix())}
				if kind == "Day" {
					session.USOpen = 0
				}
				p, err := New(Config{Reference: symbol, Product: product, CoreQuantity: 5, ExtraMaxQuantity: 5, Cap: 10, LargeTick: tick, PointValue: point, Parameters: params, RuleExpressions: allRulesExternalExpressions(t, symbol, kind, method)}, session)
				if err != nil {
					t.Fatal(err)
				}
				m, lm := input.Matrices[symbol], legacy.Matrices[symbol]
				market := sourceMatrix143(t, filepath.Join(dir, m["market"].File))
				history := sourceMatrix143(t, filepath.Join(dir, m["history"].File))
				cols := map[string][]float64{}
				for _, name := range []string{"e01", "day_market", "last_market", "mid_market", "last_history"} {
					cols[name] = sourceMatrix143(t, filepath.Join(dir, "legacy", lm[name].File))
				}
				row := legacyAllRulesResult{Session: scene, Symbol: symbol, Method: method, Orders: [][]float64{}}
				pending := []Signal{}
				for i := 0; i < m["market"].Rows; i++ {
					o := sourceCoreObservation143(market, history, cols, i)
					o.Reference = symbol
					if quoteValid(o) {
						for _, signal := range pending {
							if signal.SourceIndex >= o.Index {
								t.Fatal("判断原行で約定しました")
							}
							if signal.Action == "new" && (o.Time >= session.ForceClose || (kind == "Night" && o.Time >= session.USOpen-900 && o.Time < session.USOpen+900)) {
								if err := p.OnOrderRejected(signal.ID); err != nil {
									t.Fatal(err)
								}
								continue
							}
							price, fee := liquidation(o, -signal.Side), math.Floor(float64(signal.Quantity)*feeRate)
							if err := p.OnFill(Fill{SignalID: signal.ID, ExecutionID: signal.ID + "約定", HoldID: signal.ID + "玉", Closed: signal.Targets, Quantity: signal.Quantity, Price: price, Time: o.Time, Fee: fee}); err != nil {
								t.Fatal(i, err)
							}
							if err := p.OnOrderDone(signal.ID); err != nil {
								t.Fatal(i, err)
							}
							owner, entry := 0., 0.
							if signal.Owner == "extra" {
								owner = 1
							}
							if signal.Action == "new" {
								entry = 1
							}
							row.Orders = append(row.Orders, []float64{float64(i), float64(signal.SourceIndex), owner, float64(signal.Campaign), float64(signal.Ticket), float64(signal.Side), float64(signal.Quantity), price, float64(signal.ReasonCode), entry, fee})
							row.Gross -= float64(signal.Side*signal.Quantity) * price * point
						}
						pending = nil
					}
					signals, err := p.Observe(o, true)
					if err != nil {
						t.Fatal(i, err)
					}
					pending = append(pending, signals...)
				}
				if len(pending) != 0 || p.Core.Position.Quantity != 0 || p.Extra.Position.Quantity != 0 {
					t.Fatal("場末に予約又は保有が残りました")
				}
				results = append(results, row)
			})
		}
	}
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
