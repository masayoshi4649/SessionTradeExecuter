package strategy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

type d07Case struct{ Session, Expected string }
type d07Manifest struct {
	SourceDirectory string `json:"source_directory"`
	Cases           []d07Case
}
type d07Expected struct{ Orders, Addon [][]float64 }
type d07Result struct {
	Session                string
	Exact, Passed          bool
	Orders, ExpectedOrders int
	Gross, ExpectedGross   float64
	Differences            []nativeDifference143
	Actual                 [][]float64
}

// d07Fixture は明示された保存済み資料の同値照合範囲を選びます。
//
// 引数: t(*testing.T)。返り値: string二つは期待値と当時入力の場所、[]d07Caseは場名です。
// 環境変数が未指定なら省略し、指定資料の欠損や空集合は試験を失敗させます。
func d07Fixture(t *testing.T) (string, string, []d07Case) {
	t.Helper()
	dir := os.Getenv("SESSIONTRADER_D07_FIXTURE")
	if dir == "" {
		t.Skip("任意の外部保存照合資料はSESSIONTRADER_D07_FIXTUREで明示指定してください")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("明示したD07照合資料のディレクトリが不正です: %s / %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "登録受領.json")); err != nil {
		t.Fatalf("明示したD07照合資料に付随する登録受領がありません: %s / %v", dir, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("明示したD07照合資料のmanifest.jsonを読めません: %s / %v", dir, err)
	}
	var m d07Manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.SourceDirectory == "" || len(m.Cases) == 0 {
		t.Fatal("明示したD07照合資料の入力位置又は場一覧が空です")
	}
	cases := m.Cases
	if os.Getenv("SESSIONTRADER_D07_ALL") == "" {
		cases = nil
		selected := map[string]bool{"2026-09-21_Day": true, "2026-09-28_Day": true, "2026-10-16_Night": true, "2026-10-20_Night": true}
		for _, c := range m.Cases {
			if selected[c.Session] {
				cases = append(cases, c)
			}
		}
	}
	if selected := os.Getenv("SESSIONTRADER_D07_SCENE"); selected != "" {
		cases = nil
		for _, c := range m.Cases {
			if c.Session == selected {
				cases = append(cases, c)
			}
		}
		if len(cases) != 1 {
			t.Fatal("指定したD07検証場がありません", selected)
		}
	}
	if len(cases) == 0 {
		t.Fatal("明示したD07照合資料に検査対象の場がありません。全場はSESSIONTRADER_D07_ALLで指定してください")
	}
	return dir, filepath.Clean(filepath.Join(dir, m.SourceDirectory)), cases
}

// d07RuleExpressions は指定時だけ実配布用の外部TOMLを検証入力に接続します。
//
// 引数: t(*testing.T)。返り値: map[string]stringは外部式、指定なしなら標準Expr用のnilです。
func d07RuleExpressions(t *testing.T) map[string]string {
	t.Helper()
	path := os.Getenv("SESSIONTRADER_D07_RULES")
	if directory := os.Getenv("SESSIONTRADER_ALL_RULES_DIRECTORY"); path == "" && directory != "" {
		path = filepath.Join(directory, rules.FileName("d07"))
	}
	if path == "" {
		return nil
	}
	loaded, err := rules.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return loaded.Expressions()
}

// d07Input は判定時点の原行・特徴だけを読み、終値や期待注文を混ぜません。
//
// 引数: t(*testing.T)、dir/scene(string)。返り値: input143、六行列、旧核列mapです。
func d07Input(t *testing.T, dir, scene string) (input143, [][]float64, [][]float64, [][]float64, [][]float64, [][]float64, map[string][]float64) {
	t.Helper()
	var input input143
	jsonFixture143(t, filepath.Join(dir, scene+"_input.json.gz"), &input)
	var legacy sourceFixture143
	jsonFixture143(t, filepath.Join(dir, "legacy", scene+"_legacy.json.gz"), &legacy)
	if strings.HasSuffix(scene, "Day") && !legacy.GateAvailable["NK225"] {
		t.Fatal("旧入口許可が不明です")
	}
	m := input.Matrices["NK225"]
	market := matrixFixture143(t, dir, m["market"])
	features := matrixFixture143(t, dir, m["features"])
	history := matrixFixture143(t, dir, m["history"])
	b20 := matrixFixture143(t, dir, m["band20"])
	b40 := matrixFixture143(t, dir, m["band40"])
	cols := map[string][]float64{}
	for _, name := range []string{"e01", "day_market", "last_market", "mid_market", "last_history"} {
		cols[name] = sourceMatrix143(t, filepath.Join(dir, "legacy", legacy.Matrices["NK225"][name].File))
	}
	return input, market, features, history, b20, b40, cols
}

// d07Session は各場の既存公開時計を移植試験へ接続します。
//
// 引数: input(input143)、scene(string)。返り値: Session。
func d07Session(input input143, scene string) Session {
	kind := "Day"
	if strings.HasSuffix(scene, "Night") {
		kind = "Night"
	}
	s := Session{ID: scene, Kind: kind, Start: input.Start, ForceClose: input.End, End: input.End + 600, ExtraAt: input.Start + 13500}
	if input.USOpen != nil {
		s.USOpen = *input.USOpen
		if kind == "Night" {
			s.ExtraAt = s.USOpen + 1800
		}
	}
	return s
}

// TestD07SavedIntegration は源から実玉まで当時の全原行で再生し、保存D07の全約定を照合します。
//
// 引数: t(*testing.T)。返り値: なし。期待約定・事後方向を判定に使いません。
func TestD07SavedIntegration(t *testing.T) {
	dir, source, cases := d07Fixture(t)
	results := []d07Result{}
	for _, c := range cases {
		t.Run(c.Session, func(t *testing.T) {
			input, market, features, history, b20, b40, cols := d07Input(t, source, c.Session)
			var expected d07Expected
			jsonFixture143(t, filepath.Join(dir, c.Expected), &expected)
			session := d07Session(input, c.Session)
			parameters, err := ParametersForMethod("NK225", session.Kind, "D07")
			if err != nil {
				t.Fatal(err)
			}
			p, err := New(Config{Reference: "NK225", Product: "NK225mini", CoreQuantity: 5, ExtraMaxQuantity: 5, Cap: 10, LargeTick: input.Tick, PointValue: 100, Parameters: parameters, RuleExpressions: d07RuleExpressions(t)}, session)
			if err != nil {
				t.Fatal(err)
			}
			m := input.Matrices["NK225"]
			marketFlat := sourceMatrix143(t, filepath.Join(source, m["market"].File))
			historyFlat := sourceMatrix143(t, filepath.Join(source, m["history"].File))
			pending := []Signal{}
			got := [][]float64{}
			for i := range market {
				o := sourceCoreObservation143(marketFlat, historyFlat, cols, i)
				o.Research = fixtureObservation143("NK225", i, market, features, history, b20, b40).Research
				if quoteValid(o) {
					for _, sig := range pending {
						if sig.SourceIndex >= o.Index {
							t.Fatal("判断と同じ原行で約定")
						}
						price := liquidation(o, -sig.Side)
						fill := Fill{SignalID: sig.ID, ExecutionID: sig.ID + "-約定", HoldID: sig.ID + "-玉", Closed: sig.Targets, Quantity: sig.Quantity, Price: price, Time: o.Time, Fee: 0}
						if err = p.OnFill(fill); err != nil {
							t.Fatal(i, err)
						}
						if err = p.OnOrderDone(sig.ID); err != nil {
							t.Fatal(i, err)
						}
						offset := map[string]int{"core": 0, "addon": 10000000, "episode": 20000000}[sig.Owner]
						newFlag := 0.
						if sig.Action == "new" {
							newFlag = 1
						}
						got = append(got, []float64{float64(i), float64(sig.SourceIndex), float64(offset + sig.Campaign), float64(sig.Side), float64(sig.Quantity), price, float64(sig.ReasonCode), newFlag, float64(sig.Ticket), 0})
					}
					pending = nil
				}
				signals, e := p.Observe(o, true)
				if e != nil {
					t.Fatal(i, e)
				}
				pending = append(pending, signals...)
			}
			sort.SliceStable(got, func(i, j int) bool {
				if got[i][0] != got[j][0] {
					return got[i][0] < got[j][0]
				}
				return int(got[i][2])/10000000 < int(got[j][2])/10000000
			})
			actual, want := canonicalOrders143(got), canonicalOrders143(expected.Orders)
			r := d07Result{Session: c.Session, Orders: len(actual), ExpectedOrders: len(want), Exact: true, Passed: true, Actual: actual}
			r.Gross, _, _ = orderAccount143(actual)
			r.ExpectedGross, _, _ = orderAccount143(want)
			if len(actual) != len(want) {
				r.Exact = false
				r.Passed = false
				t.Errorf("約定件数 %d/%d", len(actual), len(want))
			}
			for i := 0; i < min(len(actual), len(want)); i++ {
				g, w := actual[i], want[i]
				if reflect.DeepEqual(g, w) {
					continue
				}
				d := nativeDifference143{Index: i, Actual: g, Expected: w}
				gi, wi := int(g[1]), int(w[1])
				d.OldDecisionTime, d.NewDecisionTime = market[wi][0], market[gi][0]
				d.AllowedTerminal = session.Kind == "Day" && g[6] == 93 && w[6] == 93 && g[7] == 0 && w[7] == 0 && gi == wi+1 && market[wi][0] < input.End && market[gi][0] >= input.End
				for _, k := range []int{2, 3, 4, 8, 9} {
					d.AllowedTerminal = d.AllowedTerminal && g[k] == w[k]
				}
				next := gi + 1
				for next < len(market) && !(market[next][4] > 0 && market[next][2] > 0 && market[next][3] > market[next][2]) {
					next++
				}
				d.AllowedTerminal = d.AllowedTerminal && int(g[0]) == next
				r.Exact = false
				r.Differences = append(r.Differences, d)
				if !d.AllowedTerminal {
					r.Passed = false
					if len(r.Differences) <= 3 {
						t.Errorf("約定%d Go=%v 保存=%v", i, g, w)
					}
				}
			}
			for _, s := range p.Research.Sleeves {
				if s.held() != 0 {
					r.Passed = false
					t.Error("場末保有", s.Name, s.held())
				}
			}
			if len(pending) != 0 {
				r.Passed = false
				t.Error("場末予約", len(pending))
			}
			results = append(results, r)
		})
	}
	if path := os.Getenv("SESSIONTRADER_D07_REPORT"); path != "" {
		raw, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
