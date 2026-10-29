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
	"strings"
	"testing"
)

type sourceFixture143 struct {
	Session          string
	Start, End, Tick float64
	USOpen           *float64 `json:"us_open"`
	Matrices         map[string]map[string]struct {
		File    string
		Rows    int
		Columns []string
	}
	Notices       map[string][][]int
	GateAvailable map[string]bool `json:"e01_gate_available"`
}

// sourceFixtureLocation143 は明示した保存検査群だけを選びます。
//
// 引数: t(*testing.T)。返り値: string は資料位置、[]string は場名です。
// 環境変数が未指定なら省略し、指定先の欠損や空集合は試験を失敗させます。
func sourceFixtureLocation143(t *testing.T) (string, []string) {
	t.Helper()
	dir := fixtureDirectory143(t)
	paths, err := filepath.Glob(filepath.Join(dir, "*_input.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	scenes := make([]string, 0, len(paths))
	for _, p := range paths {
		scenes = append(scenes, strings.TrimSuffix(filepath.Base(p), "_input.json.gz"))
	}
	if len(scenes) == 0 {
		t.Fatal("明示した保存照合資料に入力ファイルがありません")
	}
	return dir, scenes
}

// sourceRead143 は保存済みfixtureだけを読みます。
//
// 引数: t(*testing.T)、path(string)。返り値: []byte は展開した保存内容です。
func sourceRead143(t *testing.T, path string) []byte {
	t.Helper()
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	z, e := gzip.NewReader(f)
	if e != nil {
		t.Fatal(e)
	}
	defer z.Close()
	b, e := io.ReadAll(z)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

// sourceMatrix143 は未知値を保った倍精度行列を読みます。
//
// 引数: t(*testing.T)、path(string)。返り値: []float64 は行優先の値です。
func sourceMatrix143(t *testing.T, path string) []float64 {
	t.Helper()
	b := sourceRead143(t, path)
	a := make([]float64, len(b)/8)
	for i := range a {
		a[i] = math.Float64frombits(binary.LittleEndian.Uint64(b[i*8:]))
	}
	return a
}

// TestResearchNativeSavedSources143 は凍結した源通知とGo核の同値を検証します。
//
// 引数: t(*testing.T)。返り値: なし。金融探索や条件選択は行いません。
func TestResearchNativeSavedSources143(t *testing.T) {
	dir, scenes := sourceFixtureLocation143(t)
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("明示した保存照合資料のmanifest.jsonを読めません: %v", err)
	}
	for _, scene := range scenes {
		t.Run(scene, func(t *testing.T) {
			var input sourceFixture143
			if err := json.Unmarshal(sourceRead143(t, filepath.Join(dir, scene+"_input.json.gz")), &input); err != nil {
				t.Fatal(err)
			}
			m := input.Matrices["NK225"]
			market := sourceMatrix143(t, filepath.Join(dir, m["market"].File))
			features := sourceMatrix143(t, filepath.Join(dir, m["features"].File))
			band20 := sourceMatrix143(t, filepath.Join(dir, m["band20"].File))
			band40 := sourceMatrix143(t, filepath.Join(dir, m["band40"].File))
			kind := "Day"
			if strings.HasSuffix(scene, "Night") {
				kind = "Night"
			}
			session := Session{ID: scene, Kind: kind, Start: input.Start, ForceClose: input.End, End: input.End + 600}
			if input.USOpen != nil {
				session.USOpen = *input.USOpen
			}
			for _, spec := range []struct {
				Name, Expected string
				Cap            int
			}{{"addon", "A3", 3}, {"episode", "CA00", 5}, {"episode", "CS00", 2}} {
				t.Run(spec.Expected, func(t *testing.T) {
					cfg := Config{Reference: "NK225", Product: "NK225mini", LargeTick: input.Tick}
					s, err := newResearchSource(cfg, session, spec.Name, spec.Cap)
					if err != nil {
						t.Fatal(err)
					}
					got := [][]int{}
					parentMap := map[int]int{}
					nextParent := 0
					for i := 0; i < m["market"].Rows; i++ {
						r := market[i*7 : (i+1)*7]
						o := Observation{Reference: "NK225", Index: int64(i), Time: r[0], Received: r[0], Last: r[1], Bid: r[2], Ask: r[3], QuoteOK: r[4] != 0, ATR: r[5]}
						for k := 0; k < 30; k++ {
							v := features[i*30+k]
							o.Research.Values[k] = v
							o.Research.Known[k] = finite(v)
						}
						o.Research.Band20.Known = true
						o.Research.Band40.Known = true
						for k := 0; k < 9; k++ {
							v := band20[i*9+k]
							o.Research.Band20.Values[k] = v
							o.Research.Band20.Known = o.Research.Band20.Known && finite(v)
							v = band40[i*9+k]
							o.Research.Band40.Values[k] = v
							o.Research.Band40.Known = o.Research.Band40.Known && finite(v)
						}
						events, e := s.Step(o)
						if e != nil {
							t.Fatal(e)
						}
						if i == m["market"].Rows/2 {
							raw, e := json.Marshal(s)
							if e != nil {
								t.Fatal(e)
							}
							var restored ResearchSource
							if e = json.Unmarshal(raw, &restored); e != nil {
								t.Fatal(e)
							}
							if e = restored.Restore(); e != nil {
								t.Fatal(e)
							}
							s = &restored
						}
						for _, e := range events {
							p, ok := parentMap[e.Campaign]
							if !ok {
								p = nextParent
								nextParent++
								parentMap[e.Campaign] = p
							}
							newFlag := 0
							if e.Action == "new" {
								newFlag = 1
							}
							got = append(got, []int{i, p, e.Side, e.Quantity, e.Reason, newFlag, e.Ticket})
						}
					}
					want := [][]int{}
					parentMap = map[int]int{}
					nextParent = 0
					for _, v := range input.Notices[spec.Expected] {
						row := append([]int(nil), v...)
						p, ok := parentMap[row[1]]
						if !ok {
							p = nextParent
							nextParent++
							parentMap[row[1]] = p
						}
						row[1] = p
						want = append(want, row)
					}
					if !reflect.DeepEqual(got, want) {
						for i := 0; i < min(len(got), len(want)); i++ {
							if !reflect.DeepEqual(got[i], want[i]) {
								t.Fatalf("通知%d相違 got=%v want=%v (全件%d/%d)", i, got[i], want[i], len(got), len(want))
							}
						}
						t.Fatalf("通知件数相違 got=%v want=%v", got, want)
					}
					var book *ResearchBook
					if s.Active != nil {
						book = &s.Active.Book
					} else if s.Episode != nil {
						book = &s.Episode.Book
					}
					if book.total() != 0 || len(book.Pending) != 0 {
						t.Fatal("源の場末残高又は予約が残りました")
					}
				})
			}
		})
	}
}

// TestResearchActiveMirror143 は上下反転でも準備と注文の時刻が一致することを確認します。
//
// 引数: t(*testing.T)。返り値: なし。人工価格だけを用います。
func TestResearchActiveMirror143(t *testing.T) {
	left := ResearchActive{Book: ResearchBook{Parent: -1}, Initial: 2, Maximum: 2}
	right := left
	points := []struct{ At, Mid float64 }{{1, 90}, {2, 93}, {32, 93}, {33, 94}, {34, 110}, {35, 102}, {36, 102}}
	session := Session{Kind: "Day", Start: 0, ForceClose: 1000}
	seen := 0
	for i, p := range points {
		o := Observation{Index: int64(i), Time: p.At, Bid: p.Mid - .5, Ask: p.Mid + .5, QuoteOK: true}
		for k := 0; k < 24; k++ {
			o.Research.Known[k] = true
		}
		a := &o.Research.Values
		a[0], a[1], a[2], a[9], a[10] = 110, 90, .1, 8, p.Mid
		a[12], a[13], a[20], a[21] = .1, .2, .1, .2
		mirror := o
		mirror.Bid, mirror.Ask = 200-o.Ask, 200-o.Bid
		mirror.Research.Values[0], mirror.Research.Values[1] = 200-a[1], 200-a[0]
		for _, c := range []int{5, 6, 7, 10, 17, 19} {
			mirror.Research.Values[c] = 200 - a[c]
		}
		for _, c := range []int{3, 4, 12, 13, 14, 15, 20, 21, 28, 29} {
			mirror.Research.Values[c] = -a[c]
		}
		l, r := left.step(o, session, 1, false), right.step(mirror, session, 1, false)
		if len(l) != len(r) {
			t.Fatal(l, r)
		}
		for j := range l {
			seen++
			if l[j].Action != r[j].Action || l[j].Side != -r[j].Side || l[j].Quantity != r[j].Quantity || l[j].Reason != r[j].Reason {
				t.Fatal(l, r)
			}
		}
	}
	if seen != 2 || left.Book.total() != 0 || right.Book.total() != 0 || math.Abs(left.Book.Cash-right.Book.Cash) > 1e-8 {
		t.Fatalf("上下対称不一致 seen=%d cash=%v/%v", seen, left.Book.Cash, right.Book.Cash)
	}
}

// TestResearchSourcePendingRestore143 は未約定予約と同秒次行の処理を検証します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestResearchSourcePendingRestore143(t *testing.T) {
	b := ResearchBook{Parent: -1}
	o := Observation{Index: 7, Time: 10, Bid: 100, Ask: 110, QuoteOK: true}
	b.queueNew(o, 1, 1, 2, 144, 80, 100)
	if b.filled(o) != 0 {
		t.Fatal("判断行で約定した")
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var restored ResearchBook
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	o.Index = 8
	if restored.filled(o) != 1 || restored.total() != 2 || restored.Base != 110 || restored.Cash != -22077 {
		t.Fatalf("予約復元不一致: %+v", restored)
	}
	close := restored.queueClose(o, 2, 2, 32, false)
	if len(close) != 1 || close[0].Side != -1 {
		t.Fatal(close)
	}
	o.Index = 9
	o.QuoteOK = false
	if restored.filled(o) != 0 {
		t.Fatal("不正気配で返済した")
	}
	o.Index = 10
	o.QuoteOK = true
	if restored.filled(o) != 2 || restored.total() != 0 || restored.Cash != -2154 {
		t.Fatal(restored)
	}
}

// TestResearchNightExtraConfiguredTime143 は外部設定した補完開始を既定値より優先します。
//
// 引数: t(*testing.T)。返り値: なし。約定・実通信は発生させません。
func TestResearchNightExtraConfiguredTime143(t *testing.T) {
	cfg := Config{Reference: "NK225", Product: "NK225mini", LargeTick: 10}
	session := Session{ID: "人工_Night", Kind: "Night", Start: 1, USOpen: 1000, ExtraAt: 4000, ForceClose: 10000, End: 10600}
	s, err := newResearchSource(cfg, session, "core", 5)
	if err != nil {
		t.Fatal(err)
	}
	o := Observation{Reference: "NK225", Index: 0, Time: 2800, Received: 2800, Bid: 100, Ask: 110, ATR: 80, Last: 100, QuoteOK: true, MidHistory: History{Known: true}}
	o.MidHistory.Values[3], o.MidHistory.Values[4], o.MidHistory.Values[8] = 130, 80, 100
	o.MidHistory.Values[6], o.MidHistory.Values[7], o.MidHistory.Values[10], o.MidHistory.Values[11], o.MidHistory.Values[13] = 1, 1, 1, 1, 1
	got, err := s.Step(o)
	if err != nil || len(got) != 0 {
		t.Fatalf("設定開始前に通知: %v %v", got, err)
	}
	o.Index = 1
	o.Time = 4000
	o.Received = 4000
	got, err = s.Step(o)
	if err != nil || len(got) != 1 || got[0].Reason != 1424 || got[0].Quantity != 5 {
		t.Fatalf("設定時刻の補完通知: %v %v", got, err)
	}
	session.ExtraAt = 0
	s, err = newResearchSource(cfg, session, "core", 5)
	if err != nil {
		t.Fatal(err)
	}
	o.Index = 0
	o.Time = 2800
	o.Received = 2800
	got, err = s.Step(o)
	if err != nil || len(got) != 1 || got[0].Reason != 1424 {
		t.Fatalf("未指定時の既定開始: %v %v", got, err)
	}
}
