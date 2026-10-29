package strategy

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type nativeResult143 struct {
	Method, Session                                                        string
	Orders, ExpectedOrders                                                 int
	Gross, Fees, Net, ExpectedGross, ExpectedFees, ExpectedNet, Difference float64
	Exact, Passed                                                          bool
	Differences                                                            []nativeDifference143
}

type nativeDifference143 struct {
	Index                            int
	AllowedTerminal                  bool
	Actual, Expected                 []float64
	OldDecisionTime, NewDecisionTime float64
	OldDecisionSeq, NewDecisionSeq   int
}

// canonicalOrders143 は担当ごとの親ID表記だけを初登場順へ正規化します。
//
// 引数: orders([][]float64)。返り値: [][]float64 は複製した注文です。時刻・玉ID・枚数は変更しません。
func canonicalOrders143(orders [][]float64) [][]float64 {
	result := make([][]float64, len(orders))
	ids := map[int]map[int]int{}
	next := map[int]int{}
	for i, row := range orders {
		result[i] = append([]float64(nil), row...)
		id := int(row[2])
		owner := id / 10000000
		if ids[owner] == nil {
			ids[owner] = map[int]int{}
		}
		index, ok := ids[owner][id]
		if !ok {
			index = next[owner]
			next[owner]++
			ids[owner][id] = index
		}
		result[i][2] = float64(owner*10000000 + index)
	}
	return result
}

// orderAccount143 は保存又は生成された約定列の費用前後収支を計算します。
//
// 引数: orders([][]float64)。返り値: gross/fees/net(float64) は日経miniの円会計です。
func orderAccount143(orders [][]float64) (gross, fees, net float64) {
	for _, o := range orders {
		gross -= o[3] * o[4] * o[5] * 100
		fees += o[9]
	}
	return gross, fees, gross - fees
}

// TestResearch143NativeIntegration は源から実建玉までを全経路で既存保存結果へ照合します。
//
// 引数: t(*testing.T)。返り値: なし。期待注文は判定へ渡さず、原行と当時特徴だけをObserveへ渡します。
func TestResearch143NativeIntegration(t *testing.T) {
	dir, scenes := sourceFixtureLocation143(t)
	if _, err := os.Stat(filepath.Join(dir, "legacy", "manifest.json")); err != nil {
		t.Skip("旧核の保存特徴なし")
	}
	results := []nativeResult143{}
	for _, scene := range scenes {
		var input input143
		jsonFixture143(t, filepath.Join(dir, scene+"_input.json.gz"), &input)
		var expected struct{ Orders map[string][][]float64 }
		jsonFixture143(t, filepath.Join(dir, scene+"_expected.json.gz"), &expected)
		var legacy sourceFixture143
		jsonFixture143(t, filepath.Join(dir, "legacy", scene+"_legacy.json.gz"), &legacy)
		if strings.HasSuffix(scene, "Day") && !legacy.GateAvailable["NK225"] {
			t.Log("旧G不明のため未検証", scene)
			continue
		}
		m := input.Matrices["NK225"]
		market := matrixFixture143(t, dir, m["market"])
		features := matrixFixture143(t, dir, m["features"])
		history := matrixFixture143(t, dir, m["history"])
		b20 := matrixFixture143(t, dir, m["band20"])
		b40 := matrixFixture143(t, dir, m["band40"])
		marketFlat := sourceMatrix143(t, filepath.Join(dir, m["market"].File))
		historyFlat := sourceMatrix143(t, filepath.Join(dir, m["history"].File))
		cols := map[string][]float64{}
		for _, name := range []string{"e01", "day_market", "last_market", "mid_market", "last_history"} {
			cols[name] = sourceMatrix143(t, filepath.Join(dir, "legacy", legacy.Matrices["NK225"][name].File))
		}
		for _, method := range []string{"TG04", "PG04"} {
			t.Run(method+"/"+scene, func(t *testing.T) {
				kind := "Day"
				if strings.HasSuffix(scene, "Night") {
					kind = "Night"
				}
				parameters, err := ParametersForMethod("NK225", kind, method)
				if err != nil {
					t.Fatal(err)
				}
				session := Session{ID: scene, Kind: kind, Start: input.Start, ForceClose: input.End, End: input.End + 600, ExtraAt: input.Start + 13500}
				if input.USOpen != nil {
					session.USOpen = *input.USOpen
					if kind == "Night" {
						session.ExtraAt = session.USOpen + 1800
					}
				}
				p, err := New(Config{Reference: "NK225", Product: "NK225mini", CoreQuantity: 5, ExtraMaxQuantity: 5, Cap: 10, LargeTick: input.Tick, PointValue: 100, Parameters: parameters, RuleExpressions: allRulesExternalExpressions(t, "NK225", kind, method)}, session)
				if err != nil {
					t.Fatal(err)
				}
				pending := []Signal{}
				got := [][]float64{}
				for i := range market {
					o := sourceCoreObservation143(marketFlat, historyFlat, cols, i)
					o.Research = fixtureObservation143("NK225", i, market, features, history, b20, b40).Research
					if quoteValid(o) {
						for _, sig := range pending {
							if sig.SourceIndex >= o.Index {
								t.Fatal("判断と同一原行で約定")
							}
							price := liquidation(o, -sig.Side)
							fee := math.Floor(float64(sig.Quantity) * 38.5)
							fill := Fill{SignalID: sig.ID, ExecutionID: sig.ID + "-約定", HoldID: sig.ID + "-玉", Closed: sig.Targets, Quantity: sig.Quantity, Price: price, Time: o.Time, Fee: fee}
							if err = p.OnFill(fill); err != nil {
								t.Fatal(i, err)
							}
							if err = p.OnOrderDone(sig.ID); err != nil {
								t.Fatal(i, err)
							}
							offset := 0
							for k, s := range p.Research.Sleeves {
								if s.Name == sig.Owner {
									offset = k * 10000000
								}
							}
							newFlag := 0.
							if sig.Action == "new" {
								newFlag = 1
							}
							got = append(got, []float64{float64(i), float64(sig.SourceIndex), float64(offset + sig.Campaign), float64(sig.Side), float64(sig.Quantity), price, float64(sig.ReasonCode), newFlag, float64(sig.Ticket), fee})
						}
						pending = nil
					}
					signals, e := p.Observe(o, true)
					if e != nil {
						t.Fatalf("原行%d: %v", i, e)
					}
					pending = append(pending, signals...)
				}
				sort.SliceStable(got, func(i, j int) bool {
					if got[i][0] != got[j][0] {
						return got[i][0] < got[j][0]
					}
					return int(got[i][2])/10000000 < int(got[j][2])/10000000
				})
				actual := canonicalOrders143(got)
				want := canonicalOrders143(expected.Orders[method])
				result := nativeResult143{Method: method, Session: scene, Orders: len(actual), ExpectedOrders: len(want), Exact: true, Passed: true, Differences: []nativeDifference143{}}
				result.Gross, result.Fees, result.Net = orderAccount143(actual)
				result.ExpectedGross, result.ExpectedFees, result.ExpectedNet = orderAccount143(want)
				result.Difference = result.Net - result.ExpectedNet
				if len(actual) != len(want) {
					t.Errorf("件数相違 %d/%d", len(actual), len(want))
					result.Exact = false
					result.Passed = false
				}
				for i := 0; i < min(len(actual), len(want)); i++ {
					g, w := actual[i], want[i]
					if reflect.DeepEqual(g, w) {
						continue
					}
					d := nativeDifference143{Index: i, Actual: g, Expected: w}
					gi, wi := int(g[1]), int(w[1])
					d.OldDecisionTime, d.NewDecisionTime = market[wi][0], market[gi][0]
					d.OldDecisionSeq, d.NewDecisionSeq = int(market[wi][6]), int(market[gi][6])
					// ----------------------------------------
					// 旧日中の終了93だけは予定時刻前へ判断を遡及した保存値と区別します。
					d.AllowedTerminal = kind == "Day" && g[6] == 93 && w[6] == 93 && g[7] == 0 && w[7] == 0 && gi == wi+1 && market[wi][0] < input.End && market[gi][0] >= input.End
					for _, c := range []int{2, 3, 4, 8, 9} {
						d.AllowedTerminal = d.AllowedTerminal && g[c] == w[c]
					}
					next := gi + 1
					for next < len(market) && !(market[next][4] > 0 && market[next][2] > 0 && market[next][3] > market[next][2]) {
						next++
					}
					d.AllowedTerminal = d.AllowedTerminal && int(g[0]) == next
					result.Exact = false
					result.Differences = append(result.Differences, d)
					if !d.AllowedTerminal {
						result.Passed = false
						if len(result.Differences) <= 3 {
							t.Errorf("相違 注文%d\nGo=%v\n保存=%v", i, g, w)
						}
					} else {
						t.Logf("場末93の因果差: 判断%d→%d 約定%d→%d", wi, gi, int(w[0]), int(g[0]))
					}
				}
				for _, s := range p.Research.Sleeves {
					if s.held() != 0 {
						t.Error("場末保有", s.Name, s.held())
						result.Passed = false
					}
				}
				if len(pending) != 0 {
					t.Error("場末発注予約", pending)
					result.Passed = false
				}
				results = append(results, result)
			})
		}
	}
	if output := os.Getenv("SESSIONTRADER_NATIVE143_REPORT"); output != "" {
		payload := struct {
			Scope   string
			Results []nativeResult143
		}{"凍結研究のGo移植統合照合。新規金融探索・実注文なし。", results}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(output, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{"TG04", "PG04"} {
		count, exact, net, expected := 0, 0, 0., 0.
		for _, r := range results {
			if r.Method == method {
				count++
				if r.Exact {
					exact++
				}
				net += r.Net
				expected += r.ExpectedNet
			}
		}
		t.Log(fmt.Sprintf("%s %d場 完全一致%d場 Go収支%.0f/保存%.0f 差%.0f", method, count, exact, net, expected, net-expected))
	}
}
