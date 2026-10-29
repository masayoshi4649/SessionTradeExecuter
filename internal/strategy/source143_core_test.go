package strategy

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sourceMarket143 は保存済み十二列を既知フラグ付き二市場観測へ写します。
//
// 引数: v([]float64)。返り値: Market は当時の公開値です。
func sourceMarket143(v []float64) Market {
	return Market{Own: v[0], Peer: v[1], OwnATR: v[2], PeerATR: v[3], Own30: v[4], Own120: v[5], Peer30: v[6], Peer120: v[7], PeerTime: v[8], Own30Time: v[9], Peer30Time: v[10], Known: v[11] == 1, Own30Known: finite(v[4]), Own120Known: finite(v[5]), Peer30Known: finite(v[6]), Peer120Known: finite(v[7])}
}

// sourceCoreObservation143 は原板と旧核の公開特徴だけを観測へ接続します。
//
// 引数: market/hist([]float64)、cols(map[string][]float64)、i(int) は保存値と原行です。
// 返り値: Observation は期待注文を含まない入力です。Research特徴は呼出側で設定します。
func sourceCoreObservation143(market, hist []float64, cols map[string][]float64, i int) Observation {
	r := market[i*7 : (i+1)*7]
	o := Observation{Reference: "NK225", Index: int64(i), Time: r[0], Received: r[0], Last: r[1], Bid: r[2], Ask: r[3], QuoteOK: r[4] != 0, ATR: r[5]}
	if !finite(o.Last) {
		o.Last = 0
	}
	if !finite(o.ATR) {
		o.ATR = 0
	}
	e := cols["e01"][i*4 : (i+1)*4]
	o.E01.Direction = int(e[1])
	o.GateDirection = int(e[1])
	o.E01.Adverse = int(e[2])
	o.E01.AdverseSource = int64(e[3])
	o.DayMarket = sourceMarket143(cols["day_market"][i*12 : (i+1)*12])
	o.LastMarket = sourceMarket143(cols["last_market"][i*12 : (i+1)*12])
	o.MidMarket = sourceMarket143(cols["mid_market"][i*12 : (i+1)*12])
	o.LastHistory.Known = true
	o.MidHistory.Known = true
	for k := 0; k < 15; k++ {
		v := cols["last_history"][i*15+k]
		o.LastHistory.Values[k] = v
		o.LastHistory.Known = o.LastHistory.Known && finite(v)
		v = hist[i*15+k]
		o.MidHistory.Values[k] = v
		o.MidHistory.Known = o.MidHistory.Known && finite(v)
	}
	return o
}

// TestResearchNativeCore143 は完全な旧特徴が残る三場の主担当生成を照合します。
//
// 引数: t(*testing.T)。返り値: なし。期待注文を源の判断へ渡しません。
func TestResearchNativeCore143(t *testing.T) {
	dir, scenes := sourceFixtureLocation143(t)
	if _, err := os.Stat(filepath.Join(dir, "legacy", "manifest.json")); err != nil {
		t.Skip("保存fixtureなし")
	}
	for _, scene := range scenes {
		t.Run(scene, func(t *testing.T) {
			var input, legacy sourceFixture143
			if e := json.Unmarshal(sourceRead143(t, filepath.Join(dir, scene+"_input.json.gz")), &input); e != nil {
				t.Fatal(e)
			}
			if e := json.Unmarshal(sourceRead143(t, filepath.Join(dir, "legacy", scene+"_legacy.json.gz")), &legacy); e != nil {
				t.Fatal(e)
			}
			if !legacy.GateAvailable["NK225"] && strings.HasSuffix(scene, "Day") {
				t.Skip("保存G許可が未知のため主担当入口再生成を未検証とする")
			}
			m := input.Matrices["NK225"]
			lm := legacy.Matrices["NK225"]
			market := sourceMatrix143(t, filepath.Join(dir, m["market"].File))
			hist := sourceMatrix143(t, filepath.Join(dir, m["history"].File))
			features := sourceMatrix143(t, filepath.Join(dir, m["features"].File))
			cols := map[string][]float64{}
			for _, name := range []string{"e01", "day_market", "last_market", "mid_market", "last_history"} {
				cols[name] = sourceMatrix143(t, filepath.Join(dir, "legacy", lm[name].File))
			}
			kind := "Day"
			if strings.HasSuffix(scene, "Night") {
				kind = "Night"
			}
			session := Session{ID: scene, Kind: kind, Start: input.Start, ForceClose: input.End, End: input.End + 600, ExtraAt: input.Start + 13500}
			if input.USOpen != nil {
				session.USOpen = *input.USOpen
				if kind == "Night" {
					session.ExtraAt = session.USOpen + 1800
				}
			}
			s, err := newResearchSource(Config{Reference: "NK225", Product: "NK225mini", LargeTick: input.Tick}, session, "core", 5)
			if err != nil {
				t.Fatal(err)
			}
			got := [][]int{}
			parents := map[int]int{}
			next := 0
			restoredOnce := false
			for i := 0; i < m["market"].Rows; i++ {
				o := sourceCoreObservation143(market, hist, cols, i)
				for k := 0; k < 30; k++ {
					v := features[i*30+k]
					o.Research.Known[k] = finite(v)
					if finite(v) {
						o.Research.Values[k] = v
					}
				}
				events, err := s.Step(o)
				if err != nil {
					t.Fatalf("行%d %v", i, err)
				}
				for _, e := range events {
					parent, ok := parents[e.Campaign]
					if !ok {
						parent = next
						next++
						parents[e.Campaign] = parent
					}
					newFlag := 0
					if e.Action == "new" {
						newFlag = 1
					}
					got = append(got, []int{i, parent, e.Side, e.Quantity, e.Reason, newFlag, e.Ticket})
				}
				if !restoredOnce && len(events) > 0 {
					raw, e := json.Marshal(s)
					if e != nil {
						t.Fatal(e)
					}
					var copySource ResearchSource
					if e = json.Unmarshal(raw, &copySource); e != nil {
						t.Fatal(e)
					}
					if e = copySource.Restore(); e != nil {
						t.Fatal(e)
					}
					s = &copySource
					restoredOnce = true
				}
			}
			want := [][]int{}
			parents = map[int]int{}
			next = 0
			for _, v := range input.Notices["B0"] {
				row := append([]int(nil), v...)
				p, ok := parents[row[1]]
				if !ok {
					p = next
					next++
					parents[row[1]] = p
				}
				row[1] = p
				want = append(want, row)
			}
			if !reflect.DeepEqual(got, want) {
				for i := 0; i < min(len(got), len(want)); i++ {
					if !reflect.DeepEqual(got[i], want[i]) {
						g, w := got[i], want[i]
						terminalDifference := kind == "Day" && g[4] == 93 && w[4] == 93 && g[5] == 0 && g[0] == w[0]+1 && market[w[0]*7] < input.End && market[g[0]*7] >= input.End && reflect.DeepEqual(g[1:], w[1:])
						if terminalDifference {
							t.Logf("場末93の因果差を明示: 旧判断行%d（予定時刻前）→Go判断行%d（到達後）。次正常気配へ返済。", w[0], g[0])
							continue
						}
						t.Fatalf("通知%d got=%v want=%v\n全got=%v\n全want=%v", i, got[i], want[i], got, want)
					}
				}
				if len(got) != len(want) {
					t.Fatalf("通知数 got=%v want=%v", got, want)
				}
			}
			if len(s.PendingLegacy) != 0 || s.CoreActive || s.Supplement.Extra.Position.Quantity != 0 {
				t.Fatal("源の場末予約又は保有が残りました")
			}
			if math.IsNaN(s.LastTime) {
				t.Fatal("時計が不明")
			}
		})
	}
}
