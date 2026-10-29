package strategy

import "testing"

// TestD07ProductsAndAllocation は日経三商品の枚数変更と状態保存が同じ運転仕様を保つことを確認します。
//
// 引数: t(*testing.T)。返り値: なし。実注文は行いません。
func TestD07ProductsAndAllocation(t *testing.T) {
	for _, kind := range []string{"Day", "Night"} {
		parameters, err := ParametersForMethod("NK225", kind, "D07")
		if err != nil {
			t.Fatal(err)
		}
		for _, product := range []string{"NK225", "NK225mini", "NK225micro"} {
			for _, cap := range []int{1, 2, 3, 10, 11, 20} {
				p, err := New(Config{Reference: "NK225", Product: product, CoreQuantity: 1, ExtraMaxQuantity: 1,
					Cap: cap, LargeTick: 10, PointValue: 100, Parameters: parameters}, Session{ID: "検査", Kind: kind, Start: 100, ForceClose: 10000, End: 10600, USOpen: 5000})
				if err != nil {
					t.Fatal(product, cap, kind, err)
				}
				total := 0
				for _, sleeve := range p.Research.Sleeves {
					total += sleeve.Capacity
				}
				if total != cap {
					t.Fatal("配分の合計が上限と異なります", total, cap)
				}
				raw, err := p.Dump()
				if err != nil {
					t.Fatal(err)
				}
				restored, err := Restore(raw)
				if err != nil || restored.Config.Parameters.Method != "D07" {
					t.Fatal("D07を保存復元できません", err)
				}
				if cap == 10 {
					if p.sleeve("core").Capacity != 5 || p.sleeve("addon").Capacity != 3 || p.sleeve("episode").Capacity != 2 {
						t.Fatal("研究10枚の配分が違います")
					}
					if p.sleeve("addon").Source.Active145 == nil {
						t.Fatal("D07の追加担当がありません")
					}
				}
			}
		}
		if _, err := ParametersForMethod("TOPIX", kind, "D07"); err == nil {
			t.Fatal("TOPIXに日経専用D07を適用しました")
		}
	}
}
