package e01

import (
	"compress/gzip"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
)

// featureEngine は API 指定に相当する呼値を使う検査用指標器を作ります。
//
// 引数: t(*testing.T)。返り値: *Features は特徴量器です。
func featureEngine(t *testing.T) *Features {
	t.Helper()
	f, err := NewFeatures("検査_Day", 0, map[string]float64{"TOPIX": .5, "NK225": 10}, DefaultFeatureConfig())
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// featureCandle は検査用の確定足を作ります。
//
// 引数: at/close/width(float64)。返り値: Candle は価格関係が正しい足です。
func featureCandle(at, close, width float64) Candle {
	return Candle{Time: at, Open: close, High: close + width/2, Low: close - width/2, Close: close, Closed: true}
}

// warmedFeatures は上向きの二市場の EMA を暖機します。
//
// 引数: t(*testing.T)。返り値: *Features は暖機状態、float64 は受信時計です。
func warmedFeatures(t *testing.T) (*Features, float64) {
	t.Helper()
	f := featureEngine(t)
	received := 5700.0
	for symbol, scale := range map[string]float64{"TOPIX": 1, "NK225": 20} {
		for _, interval := range []int{300, 60} {
			rows := []Candle{}
			n := 90
			if interval == 300 {
				n = 18
			}
			for i := 0; i < n; i++ {
				rows = append(rows, featureCandle(float64(i*interval), 1000*scale+float64(i*interval)/60*scale, 4*scale))
			}
			if _, err := f.AddCandles(symbol, interval, rows, received); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f, received
}

// featureBook は有効な最良気配を作ります。
//
// 引数: at/mid(float64)。返り値: Book は検査板です。
func featureBook(at, mid float64) Book {
	return Book{QuoteTime: at, Price: mid, Bid: mid - .5, Ask: mid + .5, BidQty: 2, AskQty: 3}
}

// TestATRPublication は未来の足利用防止・最小本数・TR別式を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestATRPublication(t *testing.T) {
	f := featureEngine(t)
	for i, width := range []float64{4, 6, 8} {
		if _, err := f.AddCandles("TOPIX", 300, []Candle{featureCandle(float64(i*300), 100, width)}, float64((i+1)*300)); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := f.Snapshot("TOPIX", 959)
	b, _ := f.Snapshot("TOPIX", 960)
	expected := ((4.0*11+6)/12*11 + 8) / 12
	if a.ATR != 0 || math.Abs(b.ATR-expected) > 1e-12 {
		t.Fatalf("公開又はATR不一致: %+v %+v", a, b)
	}
}

// TestReceiptPeerDelayAndClosedFormEMA は実受信時計・相手待機と独立式による EMA を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestReceiptPeerDelayAndClosedFormEMA(t *testing.T) {
	f, received := warmedFeatures(t)
	a, _ := f.Snapshot("TOPIX", received-.01)
	b, _ := f.Snapshot("TOPIX", received)
	c, _ := f.Snapshot("TOPIX", received+59.99)
	d, _ := f.Snapshot("TOPIX", received+60)
	if a.ATR != 0 || b.MainDirection != 1 || c.AllowedDirection != 0 || d.AllowedDirection != 1 {
		t.Fatal("実受信・相手公開待機が不一致")
	}
	for period, actual := range map[int]float64{15: d.EMAFast, 60: d.EMASlow} {
		alpha := 2 / float64(period+1)
		expected := 1000 * math.Pow(1-alpha, 89)
		for i := 1; i < 90; i++ {
			expected += alpha * float64(1000+i) * math.Pow(1-alpha, float64(89-i))
		}
		if math.Abs(expected-actual) > 1e-9 {
			t.Fatal("EMAの独立式が不一致")
		}
	}
}

// TestCandleAtomicity は確定値変更と不正な一括足入力の部分反映を防ぎます。
//
// 引数: t(*testing.T)。返り値: なし。
func TestCandleAtomicity(t *testing.T) {
	f := featureEngine(t)
	bad := featureCandle(60, -1, 4)
	if _, err := f.AddCandles("TOPIX", 60, []Candle{featureCandle(0, 100, 4), bad}, 120); err == nil {
		t.Fatal("不正足を受け入れました")
	}
	if len(f.Markets["TOPIX"].Bars[60]) != 0 {
		t.Fatal("一括入力を部分適用しました")
	}
	f.AddCandles("TOPIX", 60, []Candle{featureCandle(0, 100, 4)}, 60)
	if _, err := f.AddCandles("TOPIX", 60, []Candle{featureCandle(0, 101, 4)}, 60); err == nil {
		t.Fatal("確定足を書き換えました")
	}
}

// TestMidReleaseAndRestore は同時刻最終中値の封印と復元一致を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestMidReleaseAndRestore(t *testing.T) {
	f, at := warmedFeatures(t)
	at += 60
	f.OnBook("TOPIX", featureBook(at, 1000), at)
	f.OnBook("TOPIX", featureBook(at, 1002), at+.1)
	if len(f.Markets["TOPIX"].Events) != 0 {
		t.Fatal("同時刻中値を先読みしました")
	}
	data, _ := f.DumpState()
	restored, err := RestoreFeatures(data)
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.OnBook("TOPIX", featureBook(at+1, 1050), at+1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := restored.OnBook("TOPIX", featureBook(at+1, 1050), at+1)
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("復元後の指標が不一致")
	}
	if *f.Markets["TOPIX"].Events[0].Mid != 1002 || a.InputIndex != 2 {
		t.Fatal("前時刻最終中値が不一致")
	}
}

// TestLongGapClearsSupport は欠測を跨いだ価格差を30秒の支持と誤認しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestLongGapClearsSupport(t *testing.T) {
	f, at := warmedFeatures(t)
	at += 60
	var result Observation
	for i := 0; i <= 70; i++ {
		result, _ = f.OnBook("TOPIX", featureBook(at+float64(i), 1100+float64(i)*.2), at+float64(i))
	}
	if result.Direction != 1 {
		t.Fatal("新しい支持を検知できません")
	}
	for i := 0; i < 20; i++ {
		now := at + 160 + float64(i)
		result, _ = f.OnBook("TOPIX", featureBook(now, 1200+float64(i)), now)
		if result.Direction != 0 {
			t.Fatal("欠測前を短期支持に使いました")
		}
	}
}

// TestCurrentCannotReadFuture は短期状態を過去時計へ公開しないことを検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestCurrentCannotReadFuture(t *testing.T) {
	f, at := warmedFeatures(t)
	f.OnBook("TOPIX", featureBook(at+60, 1100), at+60)
	if _, err := f.Current("TOPIX", at+59); err == nil {
		t.Fatal("最新観測の短期状態を過去時計へ公開しました")
	}
}

type featureAction struct {
	Kind     string    `json:"kind"`
	Symbol   string    `json:"symbol"`
	Interval int       `json:"interval"`
	Received float64   `json:"received"`
	Candles  []Candle  `json:"candles"`
	Book     Book      `json:"book"`
	Expected []float64 `json:"expected"`
}

// TestPythonFeatureGolden は Python の指標公開と板時系列を一観測ごとに照合します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestPythonFeatureGolden(t *testing.T) {
	file, err := os.Open("testdata/python_features.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	var rows []featureAction
	if err = json.NewDecoder(gz).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	f := featureEngine(t)
	observations := 0
	for index, r := range rows {
		switch r.Kind {
		case "candles":
			if _, err = f.AddCandles(r.Symbol, r.Interval, r.Candles, r.Received); err != nil {
				t.Fatal(err)
			}
		case "book":
			actual, err := f.OnBook(r.Symbol, r.Book, r.Received)
			if err != nil {
				t.Fatalf("行%d: %v", index, err)
			}
			fields := []float64{actual.ATR, float64(actual.MainDirection), float64(actual.PeerDirection), float64(actual.AllowedDirection), actual.EMAFast, actual.EMASlow, float64(actual.Direction), float64(actual.Adverse), float64(actual.AdverseSource), float64(actual.InputIndex)}
			for col, value := range fields {
				if math.Abs(value-r.Expected[col]) > 1e-8 {
					t.Fatalf("行%d項目%d: Go=%v Python=%v", index, col, value, r.Expected[col])
				}
			}
			observations++
		default:
			t.Fatal("検査行種別が不正です")
		}
	}
	t.Logf("Python指標と%d観測・10項目一致", observations)
}
