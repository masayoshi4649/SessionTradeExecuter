package strategy

import (
	"reflect"
	"testing"
)

// TestMethodsKeepSelectedParameters は手法IDで選んだ全設定が従来の選定値と一致することを検証します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
func TestMethodsKeepSelectedParameters(t *testing.T) {
	for _, test := range []struct{ reference, kind, id string }{
		{"TOPIX", "Day", "T06"}, {"NK225", "Day", "T06"},
		{"TOPIX", "Night", "T04"}, {"NK225", "Night", "U06"},
	} {
		t.Run(test.reference+"/"+test.kind, func(t *testing.T) {
			actual, err := ParametersForMethod(test.reference, test.kind, test.id)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, DefaultParameters(test.reference, test.kind)) {
				t.Fatal("手法選択で従来の実効設定が変わりました")
			}
			if test.reference == "NK225" && (!actual.Night.Retest || actual.Day.PersistenceSeconds != 60) {
				t.Fatal("日経専用の主担当条件が失われました")
			}
			if test.kind == "Night" && test.reference == "TOPIX" && (actual.Extra.Style != 0 || actual.Extra.TrailStartR != 1.25 || actual.Extra.TrailWidthR != .75) {
				t.Fatal("TOPIX夜間の未参加補完が異なります")
			}
			if test.kind == "Night" && test.reference == "NK225" && (actual.Extra.BudgetTicks != 12 || actual.Extra.TrailStartR != .75) {
				t.Fatal("日経夜間の未参加補完が異なります")
			}
		})
	}
}

// TestMethodsRejectUnsupportedCombinations は未知IDと銘柄・昼夜違いを暗黙の既定値へ置換しないことを検証します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
func TestMethodsRejectUnsupportedCombinations(t *testing.T) {
	for _, test := range []struct{ reference, kind, id string }{
		{"TOPIX", "Day", ""}, {"TOPIX", "Day", "unknown"}, {"TOPIX", "Day", "t06"},
		{"TOPIX", "Day", "T04"}, {"TOPIX", "Night", "T06"}, {"TOPIX", "Night", "U06"},
		{"NK225", "Night", "T04"}, {"NK225", "Day", "U06"},
		{"TOPIXmini", "Day", "T06"}, {"NK225", "未知", "U06"},
	} {
		t.Run(test.reference+"/"+test.kind+"/"+test.id, func(t *testing.T) {
			if _, err := ParametersForMethod(test.reference, test.kind, test.id); err == nil {
				t.Fatal("非対応の手法組合せを受け付けました")
			}
		})
	}
}
