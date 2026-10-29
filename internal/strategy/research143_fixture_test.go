package strategy

import (
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type matrix143 struct {
	File    string
	Rows    int
	Columns []string
}
type input143 struct {
	Session, Symbol  string
	Start, End, Tick float64
	USOpen           *float64 `json:"us_open"`
	Matrices         map[string]map[string]matrix143
	Notices          map[string][][]float64
}

// fixtureDirectory143 は明示指定された保存済み移植試験資料の場所を返します。
//
// 引数: t(*testing.T)。返り値: stringは資料ディレクトリです。
// 環境変数が未指定なら省略し、指定先が不正な場合は試験を失敗させます。
func fixtureDirectory143(t *testing.T) string {
	t.Helper()
	directory := os.Getenv("SESSIONTRADER_FIXTURE143")
	if directory == "" {
		t.Skip("任意の外部保存照合資料はSESSIONTRADER_FIXTURE143で明示指定してください")
	}
	info, err := os.Stat(filepath.Join(directory, "manifest.json"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("明示した保存照合資料のmanifest.jsonを読めません: %s / %v", directory, err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct{ Cases []json.RawMessage }
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("明示した保存照合資料のmanifest.jsonが不正です: %v", err)
	}
	if len(manifest.Cases) == 0 {
		t.Fatal("明示した保存照合資料の場一覧が空です")
	}
	return directory
}

// jsonFixture143 はgzipの検査資料を復号します。
//
// 引数: t(*testing.T)、path(string)、out(any)。返り値: なし。
func jsonFixture143(t *testing.T, path string, out any) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	if err = json.NewDecoder(z).Decode(out); err != nil {
		t.Fatal(err)
	}
}

// matrixFixture143 は原行を間引かず倍精度行列へ戻します。
//
// 引数: t(*testing.T)、directory(string)、spec(matrix143)。返り値: [][]float64は保存行列です。
func matrixFixture143(t *testing.T, directory string, spec matrix143) [][]float64 {
	t.Helper()
	f, err := os.Open(filepath.Join(directory, spec.File))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	raw, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	cols := len(spec.Columns)
	if len(raw) != spec.Rows*cols*8 {
		t.Fatal("行列サイズ不一致", spec.File)
	}
	flat := make([]float64, spec.Rows*cols)
	for i := range flat {
		flat[i] = math.Float64frombits(binary.LittleEndian.Uint64(raw[i*8 : i*8+8]))
	}
	rows := make([][]float64, spec.Rows)
	for i := range rows {
		rows[i] = flat[i*cols : (i+1)*cols]
	}
	return rows
}

// fixtureObservation143 は当時価格と公開済み特徴だけを観測へ写します。
//
// 引数: symbol(string)、i(int)、market/features/history/band20/band40([][]float64)。返り値: Observationです。
func fixtureObservation143(symbol string, i int, market, features, history, band20, band40 [][]float64) Observation {
	m := market[i]
	o := Observation{Reference: symbol, Index: int64(i), Time: m[0], Received: m[0], Last: m[1], Bid: m[2], Ask: m[3], QuoteOK: m[4] > 0, ATR: m[5]}
	if !finite(o.ATR) {
		o.ATR = 0
	}
	for j, v := range features[i] {
		o.Research.Known[j] = finite(v)
		if finite(v) {
			o.Research.Values[j] = v
		}
	}
	o.MidHistory.Known = true
	o.Research.MA20Known = finite(history[i][1])
	if o.Research.MA20Known {
		o.Research.MA20 = history[i][1]
	}
	for j, v := range history[i] {
		if !finite(v) {
			o.MidHistory.Known = false
		} else {
			o.MidHistory.Values[j] = v
		}
	}
	for j, data := range [][][]float64{band20, band40} {
		b := ResearchBand{Known: true}
		for k, v := range data[i] {
			if !finite(v) {
				b.Known = false
			} else {
				b.Values[k] = v
			}
		}
		if j == 0 {
			o.Research.Band20 = b
		} else {
			o.Research.Band40 = b
		}
	}
	return o
}

// TestResearch143SavedExecution は原通知から個別玉・残玉を逐次管理し、研究の全注文列と一致することを確認します。
//
// 引数: t(*testing.T)。返り値: なし。未来の約定・期待値は戦略へ渡しません。
func TestResearch143SavedExecution(t *testing.T) {
	directory := fixtureDirectory143(t)
	var manifest struct {
		Cases []struct{ Session, Input, Expected string }
	}
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, c := range manifest.Cases {
		var input input143
		jsonFixture143(t, filepath.Join(directory, c.Input), &input)
		var expected struct{ Orders map[string][][]float64 }
		jsonFixture143(t, filepath.Join(directory, c.Expected), &expected)
		spec := input.Matrices["NK225"]
		market := matrixFixture143(t, directory, spec["market"])
		features := matrixFixture143(t, directory, spec["features"])
		history := matrixFixture143(t, directory, spec["history"])
		b20 := matrixFixture143(t, directory, spec["band20"])
		b40 := matrixFixture143(t, directory, spec["band40"])
		for _, method := range []string{"TG04", "PG04"} {
			t.Run(method+"/"+c.Session, func(t *testing.T) {
				kind := "Day"
				if strings.HasSuffix(c.Session, "Night") {
					kind = "Night"
				}
				parameters, _ := ParametersForMethod("NK225", kind, method)
				session := Session{ID: c.Session, Kind: kind, Start: input.Start, ForceClose: input.End, End: input.End + 600}
				if input.USOpen != nil {
					session.USOpen = *input.USOpen
				}
				p, err := New(Config{Reference: "NK225", Product: "NK225mini", CoreQuantity: 5, ExtraMaxQuantity: 5, Cap: 10, LargeTick: 10, PointValue: 100, Parameters: parameters}, session)
				if err != nil {
					t.Fatal(err)
				}
				type indexed struct {
					index int
					event ResearchEvent
				}
				notices := map[string][]indexed{}
				cursor := map[string]int{}
				for _, s := range p.Research.Sleeves {
					name := "B0"
					if s.Name == "addon" {
						name = "A3"
					}
					if s.Name == "episode" {
						name = "CS00"
						if method == "PG04" {
							name = "CA00"
						}
					}
					for _, v := range input.Notices[name] {
						action := "close"
						if v[5] > 0 {
							action = "new"
						}
						notices[s.Name] = append(notices[s.Name], indexed{int(v[0]), ResearchEvent{Action: action, Campaign: int(v[1]), Side: int(v[2]), Quantity: int(v[3]), Reason: int(v[4]), Ticket: int(v[6])}})
					}
				}
				var pending []Signal
				var got [][]float64
				for i := range market {
					o := fixtureObservation143("NK225", i, market, features, history, b20, b40)
					p.LastIndex = o.Index
					p.LastTime = o.Time
					if quoteValid(o) {
						for _, sig := range pending {
							if sig.SourceIndex >= o.Index {
								t.Fatal("同原行約定")
							}
							price := liquidation(o, -sig.Side)
							fee := math.Floor(float64(sig.Quantity) * 38.5)
							fill := Fill{SignalID: sig.ID, ExecutionID: sig.ID + "-fill", HoldID: sig.ID + "-hold", Closed: sig.Targets, Quantity: sig.Quantity, Price: price, Time: o.Time, Fee: fee}
							if err := p.OnFill(fill); err != nil {
								t.Fatal(i, err)
							}
							if err := p.OnOrderDone(sig.ID); err != nil {
								t.Fatal(err)
							}
							offset := 0
							for j, s := range p.Research.Sleeves {
								if s.Name == sig.Owner {
									offset = j * 10000000
								}
							}
							newFlag := 0.
							if sig.Action == "new" {
								newFlag = 1
							}
							got = append(got, []float64{float64(i), float64(sig.SourceIndex), float64(sig.Campaign + offset), float64(sig.Side), float64(sig.Quantity), price, float64(sig.ReasonCode), newFlag, float64(sig.Ticket), fee})
						}
						pending = nil
					}
					for _, s := range p.Research.Sleeves {
						var events []ResearchEvent
						list := notices[s.Name]
						k := cursor[s.Name]
						for k < len(list) && list[k].index == i {
							events = append(events, list[k].event)
							k++
						}
						cursor[s.Name] = k
						pending = append(pending, p.nativeResearch(s, o, events, true, true)...)
						p.researchManage(s, o)
						pending = append(pending, p.flushResearchGoals(s, o)...)
					}
				}
				sort.SliceStable(got, func(i, j int) bool {
					if got[i][0] != got[j][0] {
						return got[i][0] < got[j][0]
					}
					return int(got[i][2])/10000000 < int(got[j][2])/10000000
				})
				want := expected.Orders[method]
				if len(want) == 0 {
					t.Fatal("期待注文がありません", method)
				}
				if len(got) != len(want) {
					t.Errorf("注文数 got=%d want=%d", len(got), len(want))
				}
				for i := 0; i < min(len(got), len(want)); i++ {
					if !reflect.DeepEqual(got[i], want[i]) {
						t.Errorf("最初の相違 注文%d\ngot  %v\nwant %v", i, got[i], want[i])
						break
					}
				}
				for _, s := range p.Research.Sleeves {
					if s.held() != 0 {
						t.Error("場末未返済", s.Name, s.held())
					}
				}
			})
		}
	}
}
