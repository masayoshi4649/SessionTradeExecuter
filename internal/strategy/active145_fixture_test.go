package strategy

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestD07SavedAddon は保存したV05/V16の全約定を追加担当だけでも独立照合します。
//
// 引数: t(*testing.T)。返り値: なし。期待約定は判定に渡さず、判断後の正常気配のみで約定します。
func TestD07SavedAddon(t *testing.T) {
	dir, source, cases := d07Fixture(t)
	for _, c := range cases {
		t.Run(c.Session, func(t *testing.T) {
			input, market, features, history, b20, b40, _ := d07Input(t, source, c.Session)
			var expected d07Expected
			jsonFixture143(t, filepath.Join(dir, c.Expected), &expected)
			session := d07Session(input, c.Session)
			params, err := ParametersForMethod("NK225", session.Kind, "D07")
			if err != nil {
				t.Fatal(err)
			}
			s, err := newResearchSource(Config{Reference: "NK225", Product: "NK225mini", Cap: 10, LargeTick: input.Tick, PointValue: 100, Parameters: params, RuleExpressions: d07RuleExpressions(t)}, session, "addon", 3)
			if err != nil {
				t.Fatal(err)
			}
			pending := []ResearchEvent{}
			decision := int64(-1)
			got := [][]float64{}
			for i := range market {
				o := fixtureObservation143("NK225", i, market, features, history, b20, b40)
				if quoteValid(o) && o.Index > decision {
					for _, e := range pending {
						newFlag := 0.
						if e.Action == "new" {
							newFlag = 1
						}
						got = append(got, []float64{float64(i), float64(decision), float64(e.Campaign), float64(e.Side), float64(e.Quantity), liquidation(o, -e.Side), float64(e.Reason), newFlag, float64(e.Ticket), 0})
					}
					pending = nil
				}
				e, err := s.Step(o)
				if err != nil {
					t.Fatal(i, err)
				}
				if len(e) > 0 {
					pending = append(pending, e...)
					decision = o.Index
				}
			}
			if len(got) != len(expected.Addon) {
				t.Errorf("追加約定件数 %d/%d", len(got), len(expected.Addon))
			}
			bad := 0
			for i := 0; i < min(len(got), len(expected.Addon)); i++ {
				if !reflect.DeepEqual(got[i], expected.Addon[i]) {
					bad++
					if bad <= 3 {
						t.Errorf("追加約定%d Go=%v 保存=%v", i, got[i], expected.Addon[i])
					}
				}
			}
			if bad > 3 {
				t.Errorf("追加約定差分 合計%d件", bad)
			}
			if len(pending) > 0 || s.Active145.Book.total() > 0 {
				t.Fatal("場末残玉又は予約")
			}
		})
	}
}
