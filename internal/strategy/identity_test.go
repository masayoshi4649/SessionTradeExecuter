package strategy

import (
	"reflect"
	"strings"
	"testing"
)

// TestOrderNamespaceSeparatesInstances は同じ商品・セッション・連番でも独立運転の依頼 ID が衝突しないことを検査します。
//
// 引数: t(*testing.T) は検査の実行環境です。返り値: なし。
func TestOrderNamespaceSeparatesInstances(t *testing.T) {
	first := testPortfolio(t, "TOPIX", "Day")
	second := testPortfolio(t, "TOPIX", "Day")
	first.Config.Namespace = "運転識別子甲"
	second.Config.Namespace = "運転識別子乙"
	o := observation(first, 0, 1200, 100)
	a := first.emit(&first.Core, o, "new", 1, 5, 20, 10, 100, 0)
	b := second.emit(&second.Core, o, "new", 1, 5, 20, 10, 100, 0)
	if a == nil || b == nil || a.ID == b.ID || first.Sequence != second.Sequence {
		t.Fatalf("別運転の ID が衝突しました: %+v / %+v", a, b)
	}
	if a.ID != first.Config.Namespace+"-人工_Day-TOPIX-core-000001" || b.ID != second.Config.Namespace+"-人工_Day-TOPIX-core-000001" {
		t.Fatal("依頼 ID の構成が不正です", a.ID, b.ID)
	}
	if err := second.OnFill(Fill{SignalID: a.ID, ExecutionID: "他運転の約定", Quantity: 5, Price: 100, Time: 1201}); err == nil {
		t.Fatal("別運転の依頼に紐づく約定を受理しました")
	}
	if second.Core.Position.Quantity != 0 {
		t.Fatal("別運転の約定で建玉が増えました")
	}
}

// TestOrderNamespaceRestorePreservesPendingAndSequence は永続化した運転 ID と未約定依頼を復元しても ID が変わらないことを検査します。
//
// 引数: t(*testing.T) は検査の実行環境です。返り値: なし。
func TestOrderNamespaceRestorePreservesPendingAndSequence(t *testing.T) {
	p := testPortfolio(t, "TOPIX", "Day")
	p.Config.Namespace = "固定運転識別子"
	o := observation(p, 0, 1200, 100)
	order := p.emit(&p.Core, o, "new", 1, 5, 20, 10, 100, 0)
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Config.Namespace != p.Config.Namespace || restored.Core.Pending != order.ID || !reflect.DeepEqual(p.Orders, restored.Orders) {
		t.Fatal("復元で運転識別子又は未約定依頼が変わりました")
	}
	for _, current := range []*Portfolio{p, restored} {
		fillAll(t, current, *order, 100, 1201)
	}
	o = observation(p, 1, 1202, 99)
	a := p.emit(&p.Core, o, "close", -1, 5, 1, 10, 100, 0)
	b := restored.emit(&restored.Core, o, "close", -1, 5, 1, 10, 100, 0)
	if !reflect.DeepEqual(a, b) || a == nil || a.ID == order.ID || !strings.HasSuffix(a.ID, "-000002") {
		t.Fatalf("復元後の次の依頼が一致しません: %+v / %+v", a, b)
	}
}

// TestLegacyOrderIDsRemainUnchanged は運転 ID を持たない既存状態の復元で注文 ID を書き換えないことを検査します。
//
// 引数: t(*testing.T) は検査の実行環境です。返り値: なし。
func TestLegacyOrderIDsRemainUnchanged(t *testing.T) {
	p := testPortfolio(t, "NK225", "Day")
	s := p.emit(&p.Core, observation(p, 0, 1200, 100), "new", 1, 10, 20, 10, 100, 0)
	if s == nil || s.ID != "人工_Day-NK225-core-000001" {
		t.Fatal("旧 ID の形式が変わりました", s)
	}
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"namespace"`) {
		t.Fatal("空の運転 ID を旧形式へ書き込みました")
	}
	restored, err := Restore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Config.Namespace != "" || restored.Core.Pending != s.ID {
		t.Fatal("旧状態の依頼 ID が変わりました")
	}
}

// TestOrderReasonDistinguishesEntryAndExit は日中損切りに夜間新規の名称を付けないことを検査します。
//
// 引数: t(*testing.T) は検査の実行環境です。返り値: なし。
func TestOrderReasonDistinguishesEntryAndExit(t *testing.T) {
	cases := []struct {
		kind, action string
		reason       int
		want         string
	}{
		{"Day", "new", 20, "主担当の新規"},
		{"Night", "new", 1, "主担当の新規"},
		{"Day", "close", 1, "組の損切り"},
		{"Day", "close", 2, "追跡返済"},
		{"Day", "close", 7, "順行幅到達による部分利確"},
		{"Day", "close", 8, "保有時間上限"},
		{"Day", "close", 9, "セッション内返済"},
		{"Day", "close", 30, "順行後の悪化で半減"},
		{"Night", "close", 32, "損失幅へ到達"},
		{"Night", "close", 70, "利益保護の戻り"},
		{"Day", "close", 75, "二市場の逆行が継続"},
		{"Day", "new", 82, "未参加時の追加"},
		{"Night", "new", 83, "未参加時の追加"},
		{"Night", "close", 93, "セッション内返済"},
		{"Night", "close", 94, "米国現物開始の静観"},
	}
	for _, test := range cases {
		p := testPortfolio(t, "TOPIX", test.kind)
		if test.action == "close" {
			p.Core.Position.Quantity = 5
			p.Core.Position.Side = 1
		}
		s := p.emit(&p.Core, observation(p, 0, 1200, 100), test.action, 1, 5, test.reason, 10, 100, 0)
		if s == nil || s.Reason != test.want || s.ReasonCode != test.reason {
			t.Fatalf("理由表示が不正です: %+v / %+v", test, s)
		}
	}
}
