package strategy

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/e01"
)

type featureEvent143 struct {
	Symbol         string
	Time, Last     float64
	Row            int
	Bid, Ask, ATR  *float64
	BidQuantity    float64         `json:"bid_quantity"`
	AskQuantity    float64         `json:"ask_quantity"`
	BidLevels      [10][2]*float64 `json:"bid_levels"`
	AskLevels      [10][2]*float64 `json:"ask_levels"`
	Features       [30]*float64
	Band20, Band40 [9]*float64
}

// featureValue143 は人工資料の未知値を NaN として観測器へ渡します。
//
// 引数: value(*float64)。返り値: float64 は値又はNaNです。
func featureValue143(value *float64) float64 {
	if value == nil {
		return math.NaN()
	}
	return *value
}

// researchInput143 は既知ATRを注入し、研究特徴だけを実際の逐次処理へ通します。
//
// 引数: f(*Features)、b(Board)、atr(float64)。返り値: ResearchFeatures は逐次生成値です。
func researchInput143(f *Features, b Board, atr float64) ResearchFeatures {
	r := rawRow{Time: b.Time, Last: b.Last, ATR: atr, ValidLast: finite(b.Last) && b.Last > 0,
		ValidMid: finite(b.Bid) && finite(b.Ask) && b.Bid > 0 && b.Ask > b.Bid}
	if !finite(r.ATR) {
		r.ATR = 0
	}
	if r.ValidMid {
		r.Mid = (b.Bid + b.Ask) * .5
		if f.OpeningMid[b.Reference] == 0 {
			f.OpeningMid[b.Reference] = r.Mid
		}
	}
	if r.ValidLast && f.OpeningLast[b.Reference] == 0 {
		f.OpeningLast[b.Reference] = r.Last
	}
	f.updateResearchRow(b, &r, f.Rows[b.Reference])
	f.Rows[b.Reference] = append(f.Rows[b.Reference], r)
	keep := sort.Search(len(f.Rows[b.Reference]), func(i int) bool { return f.Rows[b.Reference][i].Time >= b.Time-2700 })
	f.Rows[b.Reference] = f.Rows[b.Reference][keep:]
	return f.researchFeatures(b.Reference)
}

// compareFeature143 は有効性も含めて独立Python特徴との数値一致を検査します。
//
// 引数: t(*testing.T)、label(string)、values/known/expected は同じ列数の値です。返り値: なし。
func compareFeature143(t *testing.T, label string, values []float64, known []bool, expected []*float64) {
	t.Helper()
	for i, want := range expected {
		if known[i] != (want != nil) {
			t.Fatalf("%s 列%d 有効性=%v 期待=%v", label, i, known[i], want != nil)
		}
		if want != nil && math.Abs(values[i]-*want) > 1e-8*math.Max(1, math.Abs(*want)) {
			t.Fatalf("%s 列%d 値=%.12g 期待=%.12g", label, i, values[i], *want)
		}
	}
}

// TestResearch143ArtificialFeatures は同時計・欠測・空白・復元を含む全30列を照合します。
//
// 引数: t(*testing.T)。返り値: なし。ATR生成そのものと金融成績はこの検査の対象外です。
func TestResearch143ArtificialFeatures(t *testing.T) {
	file, err := os.Open("testdata/research_features143.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	z, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var fixture struct {
		Session string
		Start   float64
		Events  []featureEvent143
	}
	if err = json.NewDecoder(z).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	f, err := NewFeatures(Session{ID: fixture.Session, Start: fixture.Start}, map[string]float64{"NK225": 10, "TOPIX": .5}, e01.DefaultFeatureConfig())
	if err != nil {
		t.Fatal(err)
	}
	f.EnableResearch()
	for index, event := range fixture.Events {
		b := Board{Reference: event.Symbol, Time: event.Time, Received: event.Time, Last: event.Last, Bid: featureValue143(event.Bid), Ask: featureValue143(event.Ask), BidQuantity: event.BidQuantity, AskQuantity: event.AskQuantity}
		for i := 0; i < 10; i++ {
			// 欠測価格はゼロ、数量は有限の値だけを共通Boardへ渡します。
			if event.BidLevels[i][0] != nil {
				b.BidLevels[i] = ResearchLevel{Price: *event.BidLevels[i][0], Quantity: *event.BidLevels[i][1]}
			}
			if event.AskLevels[i][0] != nil {
				b.AskLevels[i] = ResearchLevel{Price: *event.AskLevels[i][0], Quantity: *event.AskLevels[i][1]}
			}
		}
		got := researchInput143(f, b, featureValue143(event.ATR))
		compareFeature143(t, event.Symbol, got.Values[:], got.Known[:], event.Features[:])
		for _, pair := range []struct {
			got  ResearchBand
			want [9]*float64
		}{{got.Band20, event.Band20}, {got.Band40, event.Band40}} {
			flags := make([]bool, 9)
			for j := range flags {
				flags[j] = pair.got.Known
			}
			compareFeature143(t, event.Symbol+"帯", pair.got.Values[:], flags, pair.want[:])
		}
		if index == len(fixture.Events)/2 {
			raw, e := f.Dump()
			if e != nil {
				t.Fatal(e)
			}
			f, e = RestoreFeatures(raw)
			if e != nil {
				t.Fatal(e)
			}
			if !f.ResearchEnabled {
				t.Fatal("復元で研究特徴が無効化")
			}
		}
	}
}

// TestResearch143SavedPriceFeatures は四保存場の価格特徴を二市場原行順で作り直します。
//
// 引数: t(*testing.T)。返り値: なし。原板欠落行を必要とする24～27列は人工全原行試験で検査します。
func TestResearch143SavedPriceFeatures(t *testing.T) {
	directory := fixtureDirectory143(t)
	manifestRaw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct{ Cases []struct{ Session string } }
	if err = json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, item := range manifest.Cases {
		scene := item.Session
		t.Run(scene, func(t *testing.T) {
			var spec input143
			jsonFixture143(t, filepath.Join(directory, scene+"_input.json.gz"), &spec)
			type marketFixture struct {
				market, features, band20, band40, history [][]float64
				index                                     int
			}
			all := map[string]*marketFixture{}
			for _, symbol := range []string{"NK225", "TOPIX"} {
				m := spec.Matrices[symbol]
				all[symbol] = &marketFixture{market: matrixFixture143(t, directory, m["market"]), features: matrixFixture143(t, directory, m["features"]), band20: matrixFixture143(t, directory, m["band20"]), band40: matrixFixture143(t, directory, m["band40"]), history: matrixFixture143(t, directory, m["history"])}
			}
			f, err := NewFeatures(Session{ID: scene, Start: spec.Start}, map[string]float64{"NK225": 10, "TOPIX": .5}, e01.DefaultFeatureConfig())
			if err != nil {
				t.Fatal(err)
			}
			f.EnableResearch()
			count := 0
			for {
				symbol := ""
				now := math.Inf(1)
				for _, s := range []string{"NK225", "TOPIX"} {
					m := all[s]
					if m.index < len(m.market) && m.market[m.index][0] < now {
						symbol, now = s, m.market[m.index][0]
					}
				}
				if symbol == "" {
					break
				}
				m := all[symbol]
				index := m.index
				r := m.market[index]
				got := researchInput143(f, Board{Reference: symbol, Time: r[0], Received: r[0], Last: r[1], Bid: r[2], Ask: r[3]}, r[5])
				ma := m.history[index][1]
				if got.MA20Known != finite(ma) || (finite(ma) && math.Abs(got.MA20-ma) > 1e-8*math.Max(1, math.Abs(ma))) {
					t.Fatalf("%s 行%d 個別MA20の有効=%v 値%.12g 期待%.12g", symbol, index, got.MA20Known, got.MA20, ma)
				}
				for column, want := range m.features[index] {
					if column >= 24 && column <= 27 {
						continue
					}
					if got.Known[column] != finite(want) {
						t.Fatalf("%s 行%d 時刻%.0f 列%d 有効=%v 期待=%v", symbol, index, r[0], column, got.Known[column], finite(want))
					}
					if finite(want) && math.Abs(got.Values[column]-want) > 1e-8*math.Max(1, math.Abs(want)) {
						t.Fatalf("%s 行%d 列%d 実装%.12g 期待%.12g", symbol, index, column, got.Values[column], want)
					}
				}
				for _, p := range []struct {
					got  ResearchBand
					want []float64
				}{{got.Band20, m.band20[index]}, {got.Band40, m.band40[index]}} {
					for column, want := range p.want {
						if p.got.Known != finite(want) || (finite(want) && math.Abs(p.got.Values[column]-want) > 1e-8*math.Max(1, math.Abs(want))) {
							t.Fatal(fmt.Sprintf("%s 行%d 帯列%d 有効%v 実装%.12g 期待%.12g", symbol, index, column, p.got.Known, p.got.Values[column], want))
						}
					}
				}
				m.index++
				count++
			}
			t.Logf("二市場 %d 原行、価格26列と公開20/40分帯を全行照合", count)
		})
	}
}

// TestResearch143OnBoardWiring は通常受信経路の研究特徴接続と45分保持・復元を検査します。
//
// 引数: t(*testing.T)。返り値: なし。足ATRの不足は短ATRで補い、40分帯の公開を待ちます。
func TestResearch143OnBoardWiring(t *testing.T) {
	const start = 1792885500.0
	f, err := NewFeatures(Session{ID: "2026-10-25_Day", Start: start}, map[string]float64{"NK225": 10, "TOPIX": .5}, e01.DefaultFeatureConfig())
	if err != nil {
		t.Fatal(err)
	}
	f.EnableResearch()
	var duplicate *Features
	var latest Observation
	for minute := 0; minute <= 65; minute++ {
		for index, symbol := range []string{"NK225", "TOPIX"} {
			tick, price := 10.0, 65000.0+float64(minute)*10
			if symbol == "TOPIX" {
				tick, price = .5, 4000+float64(minute)*.5
			}
			at := start + float64(minute*60+index)
			b := Board{Reference: symbol, Time: at, Received: at, Last: price, Bid: price - tick, Ask: price + tick, BidQuantity: 10, AskQuantity: 11}
			latest, err = f.OnBoard(b)
			if err != nil {
				t.Fatal(err)
			}
			if duplicate != nil {
				other, err := duplicate.OnBoard(b)
				if err != nil || other.Research != latest.Research {
					t.Fatalf("復元後の通常受信に差分: 分%d 市場%s err=%v", minute, symbol, err)
				}
			}
		}
		if minute == 37 {
			raw, err := f.Dump()
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err = RestoreFeatures(raw)
			if err != nil || !duplicate.ResearchEnabled {
				t.Fatalf("研究特徴の状態を復元できません: %v", err)
			}
		}
	}
	if !latest.Research.Band40.Known || !latest.Research.MA20Known || !latest.Research.Known[9] {
		t.Fatal("通常のOnBoardから40分帯・個別MA20・準備幅を生成できません")
	}
	for symbol, rows := range f.Rows {
		if len(rows) != 46 || rows[len(rows)-1].Time-rows[0].Time != 2700 {
			t.Fatalf("%s の研究保持は45分ではありません: 行数%d", symbol, len(rows))
		}
	}
}

// TestResearch143PartialHistoryToSource は全体履歴が欠けても相手20分差を個別に接続します。
//
// 引数: t(*testing.T)。返り値: なし。人工確定足と通常OnBoardから夜間の境界退出まで検査します。
func TestResearch143PartialHistoryToSource(t *testing.T) {
	for _, missing := range []string{"NK225", "TOPIX"} {
		t.Run(missing+"履歴欠測", func(t *testing.T) {
			session := Session{ID: "接続_Night", Kind: "Night", Start: 0, ForceClose: 20000, End: 21000, USOpen: 10000}
			f, err := NewFeatures(session, map[string]float64{"NK225": 10, "TOPIX": .5}, e01.DefaultFeatureConfig())
			if err != nil {
				t.Fatal(err)
			}
			f.EnableResearch()
			var observation Observation
			for minute := 0; minute <= 32; minute++ {
				for index, symbol := range []string{"TOPIX", "NK225"} {
					price, tick := 4000-float64(minute), .5
					if symbol == "NK225" {
						price, tick = 65000, 10
					}
					at := float64(minute*60 + index)
					// 確定済み5分足は公開待機後に追加し、板の欠測とは独立にATRを用意します。
					if minute >= 6 && minute%5 == 1 {
						bar := Candle{Time: float64((minute - 6) * 60), Open: price, High: price + 10*tick, Low: price - 10*tick, Close: price, Closed: true, Received: at}
						if _, err := f.AddCandles(symbol, 300, []Candle{bar}, at); err != nil {
							t.Fatal(err)
						}
					}
					if symbol == missing && minute >= 20 && minute <= 23 {
						continue
					}
					o, err := f.OnBoard(Board{Reference: symbol, Time: at, Received: at, Last: price, Bid: price - tick, Ask: price + tick, BidQuantity: 10, AskQuantity: 11})
					if err != nil {
						t.Fatal(err)
					}
					if symbol == "NK225" {
						observation = o
					}
				}
			}
			wantKnown := missing == "NK225"
			if observation.MidHistory.Known || !observation.MidMarket.Known || observation.Research.Known[29] != wantKnown {
				t.Fatalf("部分欠測の人工条件が不正: 全体%v 短窓%v 相手差%v", observation.MidHistory.Known, observation.MidMarket.Known, observation.Research.Known[29])
			}
			if wantKnown && observation.Research.Values[29] >= 0 {
				t.Fatal("相手の20分下落を失いました")
			}
			parameters, err := ParametersForMethod("NK225", "Night", "TG04")
			if err != nil {
				t.Fatal(err)
			}
			cfg := Config{Reference: "NK225", Product: "NK225mini", CoreQuantity: 5, ExtraMaxQuantity: 5, Cap: 10, LargeTick: 10, PointValue: 100, Parameters: parameters}
			source, err := newResearchSource(cfg, session, "core", 5)
			if err != nil {
				t.Fatal(err)
			}
			source.Legacy.Core.Position = Position{Owner: "core", Side: 1, Quantity: 10, Average: 65000, Risk: 1000, Boundary: 70000, Floor: -1000, Campaign: 0}
			source.CoreAccepted[0], source.CoreActive = true, true
			source.CoreRemaining[0], source.CoreOutputHeld[0] = 10, 5
			events, err := source.Step(observation)
			if err != nil {
				t.Fatal(err)
			}
			if wantKnown {
				if len(events) != 1 || events[0].Action != "close" || events[0].Reason != 72 || events[0].Quantity != 5 {
					t.Fatalf("当時既知の相手逆行による境界退出を失いました: %+v", events)
				}
			} else if len(events) != 0 {
				t.Fatalf("未知の相手20分差を既知として退出しました: %+v", events)
			}
		})
	}
}
