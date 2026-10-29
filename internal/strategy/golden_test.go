package strategy

import (
	"compress/gzip"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/e01"
)

type goldenCase struct {
	Session, Symbol, Method string
	Start, End, Tick        float64
	USOpen                  float64 `json:"us_open"`
	Rows                    [][]*float64
	Orders                  [][]float64
}

// gv は保存 null を有限値と有効性へ分けます。
//
// 引数: row([]*float64)、i(int)。返り値: float64、bool です。
func gv(row []*float64, i int) (float64, bool) {
	if row[i] == nil {
		return 0, false
	}
	return *row[i], true
}

// goldenObservation は保存済み当時特徴を共通 API へ変換します。
//
// 引数: c(goldenCase)、i(int)。返り値: Observation です。
func goldenObservation(c goldenCase, i int) Observation {
	r := c.Rows[i]
	val := func(j int) float64 { v, _ := gv(r, j); return v }
	o := Observation{Reference: c.Symbol, Index: int64(i), Time: val(0), Received: val(0), Last: val(1), Bid: val(2), Ask: val(3), QuoteOK: val(4) == 1, ATR: val(5), GateDirection: int(val(6)), E01: e01.Observation{Direction: int(val(6)), Adverse: int(val(7)), AdverseSource: int64(val(8))}}
	m := Market{Own: val(9), Peer: val(10), OwnATR: val(11), PeerATR: val(12), Own30: val(13), Own120: val(14), Peer30: val(15), Peer120: val(16), PeerTime: val(17), Own30Time: val(18), Peer30Time: val(19), Known: val(20) == 1}
	_, m.Own30Known = gv(r, 13)
	_, m.Own120Known = gv(r, 14)
	_, m.Peer30Known = gv(r, 15)
	_, m.Peer120Known = gv(r, 16)
	o.DayMarket = m
	o.LastMarket = m
	o.MidMarket = m
	h := History{Known: true}
	for j := 0; j < 15; j++ {
		v, ok := gv(r, 21+j)
		h.Values[j] = v
		h.Known = h.Known && ok
	}
	o.LastHistory = h
	o.MidHistory = h
	return o
}

// TestSelectedSavedOrders は四商品場の保存済み発注・約定列と逐次移植を照合します。
//
// 引数: t(*testing.T)。返り値: なし。探索・条件選択は行いません。
func TestSelectedSavedOrders(t *testing.T) {
	f, err := os.Open("testdata/selected_orders.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var fixture struct{ Cases []goldenCase }
	if err = json.NewDecoder(z).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		t.Run(c.Method+"_"+c.Session, func(t *testing.T) {
			kind := "Night"
			if c.Method == "DV07" || c.Method == "DV05" {
				kind = "Day"
			}
			p := testPortfolio(t, c.Symbol, kind)
			p.Session = Session{ID: c.Session, Kind: kind, Start: c.Start, ForceClose: c.End, End: c.End + 600, USOpen: c.USOpen, ExtraAt: c.End}
			p.Config.LargeTick = c.Tick
			p, err = New(p.Config, p.Session)
			if err != nil {
				t.Fatal(err)
			}
			got := [][]float64{}
			pending := []Signal{}
			for i := range c.Rows {
				o := goldenObservation(c, i)
				if quoteValid(o) && len(pending) > 0 {
					for _, s := range pending {
						if s.Action == "new" && (o.Time >= p.Session.ForceClose || (kind == "Night" && o.Time >= p.Session.USOpen-900 && o.Time < p.Session.USOpen+900)) {
							if err := p.OnOrderRejected(s.ID); err != nil {
								t.Fatal(err)
							}
							continue
						}
						price := liquidation(o, -s.Side)
						fillAll(t, p, s, price, o.Time)
						newFlag := 0.
						if s.Action == "new" {
							newFlag = 1
						}
						got = append(got, []float64{float64(i), float64(s.SourceIndex), 0, float64(s.Side), float64(s.Quantity), price, float64(s.ReasonCode), newFlag})
					}
					pending = nil
				}
				signals := observe(t, p, o, true)
				pending = append(pending, signals...)
			}
			if len(got) != len(c.Orders) {
				t.Fatalf("注文数: 実装%d 保存%d\n実装%v\n保存%v", len(got), len(c.Orders), got, c.Orders)
			}
			for i, a := range got {
				b := c.Orders[i]
				for _, j := range []int{0, 1, 3, 4, 5, 7} {
					// 旧日中末尾は判定行で返済済みとしたため、実装は同じ判断後の次行へ送ります。
					// 保存時刻への遡及発注をせず、ここでは価格も同じ一件だけを明示的に許容します。
					terminalDifference := kind == "Day" && b[6] == 93 && a[0] == b[0]+1 && a[1] == b[0] && a[5] == b[5]
					if terminalDifference && (j == 0 || j == 1) {
						continue
					}
					if math.Abs(a[j]-b[j]) > 1e-7 {
						t.Fatalf("注文%d 列%d 実装%.12g 保存%.12g\n実装%v\n保存%v", i, j, a[j], b[j], got, c.Orders)
					}
				}
			}
		})
	}
}
