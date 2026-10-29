package e01

import (
	"compress/gzip"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// testEngine は既存の TOPIX ミニ条件で検査用状態を作ります。
//
// 引数: t(*testing.T)、mode(string)。返り値: *Engine は注文器です。
func testEngine(t *testing.T, mode string) *Engine {
	t.Helper()
	cfg := DefaultConfig("TOPIX", 5, 10, .5, 1000)
	cfg.FillMode = mode
	e, err := NewEngine(cfg, Session{ID: "検査_Day", Start: 0, ForceClose: 30000, Close: 30600})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// testTick は価格観測を投入して、異常を検査失敗にします。
//
// 引数: t(*testing.T)、e(*Engine)、at/price(float64)、adverse(int)。返り値: []Event は発生した約定です。
func testTick(t *testing.T, e *Engine, at, price float64, adverse int) []Event {
	t.Helper()
	events, err := e.OnTick(Tick{Time: at, Price: price, ATR: 10, Direction: 1, Adverse: adverse, AdverseSource: e.Row + 1, AllowEntry: true, InputIndex: -1})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// openEngine は次観測の条件成立で新規建てします。
//
// 引数: t(*testing.T)。返り値: *Engine は5枚保有した状態です。
func openEngine(t *testing.T) *Engine {
	t.Helper()
	e := testEngine(t, "specified")
	if len(testTick(t, e, 1200, 100, 0)) != 0 {
		t.Fatal("準備観測で約定しました")
	}
	v := testTick(t, e, 1201, 103, 0)
	if len(v) != 1 || v[0].Price != 102.5 || v[0].SourceRow != 0 {
		t.Fatalf("次観測の新規が不一致: %+v", v)
	}
	return e
}

// TestStopsAndOriginalSlots は損失予算と半減後の元枠二重返済防止を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestStopsAndOriginalSlots(t *testing.T) {
	e := openEngine(t)
	v := testTick(t, e, 1202, 95, 0)
	if len(v) != 1 || v[0].RealizedYen != -37500 {
		t.Fatalf("損切りが不一致: %+v", v)
	}
	e = openEngine(t)
	testTick(t, e, 1202, 118, 0)
	testTick(t, e, 1203, 117, -1)
	v = testTick(t, e, 1204, 116, 0)
	if len(v) != 1 || v[0].ReasonCode != 30 || v[0].Quantity != 2 {
		t.Fatalf("悪化半減が不一致: %+v", v)
	}
	if v = testTick(t, e, 1205, 123, 0); len(v) != 0 || e.Status().Quantity != 3 {
		t.Fatal("廃止済みの元枠を再返済しました")
	}
}

// TestShadowAndRestore は早期出口後の通常影継続と状態復元の一致を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestShadowAndRestore(t *testing.T) {
	e := openEngine(t)
	other, err := e.Clone()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]float64{{1202, 123}, {1203, 133}, {1204, 125}, {1600, 130}} {
		a := testTick(t, e, p[0], p[1], 0)
		b := testTick(t, other, p[0], p[1], 0)
		if !reflect.DeepEqual(a, b) {
			t.Fatal("復元後の注文不一致")
		}
	}
	if e.Status().Quantity != 0 || e.Normal.Direction != 1 || e.Normal.Pending != nil {
		t.Fatal("通常影による再参入制限が不一致")
	}
}

// TestCloseAndSourceGuards は終場遡及返済と未来の悪化根拠を拒否します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestCloseAndSourceGuards(t *testing.T) {
	e := openEngine(t)
	if _, err := e.ForceFlatten(30600, 100, "終了"); err == nil {
		t.Fatal("終場後の遡及返済を許可しました")
	}
	_, err := e.OnTick(Tick{Time: 1202, Price: 100, Adverse: 1, AdverseSource: 100, InputIndex: -1})
	if err == nil {
		t.Fatal("未来の根拠を許可しました")
	}
	testTick(t, e, 30000, 100, 0)
	v := testTick(t, e, 30001, 100, 0)
	if len(v) != 1 || v[0].ReasonCode != 9 {
		t.Fatal("セッション内返済が不一致")
	}
}

// TestObservedAndDisabledEntry は実観測価格と新規遮断を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestObservedAndDisabledEntry(t *testing.T) {
	e := testEngine(t, "observed")
	testTick(t, e, 1200, 100, 0)
	v := testTick(t, e, 1201, 110, 0)
	if v[0].Price != 110 {
		t.Fatal("現在観測価格で新規になりません")
	}
	if v = testTick(t, e, 1202, 90, 0); v[0].Price != 90 {
		t.Fatal("現在観測価格で返済になりません")
	}
	e = testEngine(t, "observed")
	testTick(t, e, 1200, 100, 0)
	v, err := e.OnTick(Tick{Time: 1201, Price: 110, ATR: 10, Direction: 1, InputIndex: -1, AdverseSource: -1, AllowEntry: false})
	if err != nil || len(v) != 0 || e.Status().Quantity != 0 {
		t.Fatal("新規遮断を無視しました")
	}
}

// TestActualPartialFill は未約定から部分新規、部分返済、復元後の累積同期を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestActualPartialFill(t *testing.T) {
	e := testEngine(t, "observed")
	testTick(t, e, 1200, 100, 0)
	entry := testTick(t, e, 1201, 103, 0)[0]
	if err := e.SyncActual(0, 0, 0, 0, entry); err != nil {
		t.Fatal(err)
	}
	if e.Status().Quantity != 0 || e.Exit.Direction != 1 {
		t.Fatal("未約定時に影も消しました")
	}
	if err := e.SyncActual(3, 1, 104, 0, entry); err != nil {
		t.Fatal(err)
	}
	if e.Status().Quantity != 3 || e.Actual.Anchor != 104 {
		t.Fatal("実新規が不一致")
	}
	close := testTick(t, e, 1202, 90, 0)[0]
	if close.Quantity != 3 {
		t.Fatal("保有以上を返済しました")
	}
	if err := e.SyncActual(2, 1, 104, -14000, close); err != nil {
		t.Fatal(err)
	}
	if e.Status().Quantity != 2 || e.Actual.Realized != -14 {
		t.Fatal("部分返済を復元できません")
	}
	clone, err := e.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if err = clone.SyncActual(0, 0, 0, -42000, close); err != nil {
		t.Fatal(err)
	}
	if clone.Status().Quantity != 0 || clone.RealizedYen != -42000 {
		t.Fatal("累積返済損益が不一致")
	}
}

type goldenCase struct {
	Symbol   string      `json:"symbol"`
	Session  Session     `json:"session"`
	Mode     string      `json:"mode"`
	Ticks    [][]float64 `json:"ticks"`
	Orders   [][]float64 `json:"orders"`
	Realized float64     `json:"realized"`
}

// checkGolden は Python の既存注文と Go の全約定項目を照合します。
//
// 引数: t(*testing.T)、path(string) は圧縮検査データ。返り値: なし。
func checkGolden(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	dec := json.NewDecoder(gz)
	var cases []goldenCase
	if err = dec.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Session.ID+"_"+c.Symbol+"_"+c.Mode, func(t *testing.T) {
			q, cap, tick, value := 5, 10, .5, 1000.0
			if c.Symbol == "NK225" {
				q, cap, tick, value = 10, 20, 10, 10
			}
			cfg := DefaultConfig(c.Symbol, q, cap, tick, value)
			cfg.FillMode = c.Mode
			e, err := NewEngine(cfg, c.Session)
			if err != nil {
				t.Fatal(err)
			}
			actual := [][]float64{}
			for i, row := range c.Ticks {
				v, err := e.OnTick(Tick{Time: row[0], Price: row[1], ATR: row[2], Direction: int(row[3]), Adverse: int(row[4]), AdverseSource: int64(row[5]), InputIndex: int64(i), AllowEntry: true})
				if err != nil {
					t.Fatalf("観測%d: %v", i, err)
				}
				for _, event := range v {
					entry := 0.0
					if event.Event == "新規" {
						entry = 1
					}
					actual = append(actual, []float64{float64(event.Row), float64(event.SourceRow), float64(event.Campaign), float64(event.Side()), float64(event.Quantity), event.Price, float64(event.ReasonCode), entry})
				}
			}
			if !reflect.DeepEqual(actual, c.Orders) {
				limit := min(len(actual), len(c.Orders))
				for i := 0; i < limit; i++ {
					if !reflect.DeepEqual(actual[i], c.Orders[i]) {
						t.Fatalf("注文%d不一致: Go=%v Python=%v", i, actual[i], c.Orders[i])
					}
				}
				t.Fatalf("注文数不一致 Go=%d Python=%d", len(actual), len(c.Orders))
			}
			if math.Abs(e.RealizedYen-c.Realized) > .001 {
				t.Fatalf("損益不一致 Go=%v Python=%v", e.RealizedYen, c.Realized)
			}
			t.Logf("観測%d件・注文%d件・損益%+.0f円一致", len(c.Ticks), len(actual), e.RealizedYen)
		})
	}
}

// TestPythonGolden は既存 Python 実装から生成した固定系列で完全一致を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestPythonGolden(t *testing.T) {
	checkGolden(t, filepath.Join("testdata", "python_orders.json.gz"))
}

// TestHistoricalParity は同梱した保存実データ由来の入力で完全一致を検査します。
//
// 引数: t(*testing.T)。返り値: なし。E01_PARITY_FILE で別の検査データを指定できます。
func TestHistoricalParity(t *testing.T) {
	path := os.Getenv("E01_PARITY_FILE")
	if path == "" {
		path = filepath.Join("testdata", "historical_orders.json.gz")
	}
	checkGolden(t, path)
}
