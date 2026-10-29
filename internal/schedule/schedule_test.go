package schedule

import (
	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"reflect"
	"testing"
	"time"
)

// TestSeparateDST は米欧の切替日が異なる週と日中の境界を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSeparateDST(t *testing.T) {
	jp, _ := time.LoadLocation("Asia/Tokyo")
	c := config.Defaults().Schedule
	for _, tc := range []struct{ day, us, london string }{
		{"2026-10-20", "22:30", "00:30"},
		{"2026-10-26", "22:30", "01:30"},
		{"2026-11-02", "23:30", "01:30"},
	} {
		start, _ := time.ParseInLocation("2006-01-02 15:04", tc.day+" 17:00", jp)
		p, err := Build(tc.day+"_Night", start, start.Add(13*time.Hour), c)
		if err != nil {
			t.Fatal(err)
		}
		if p.USOpen.In(jp).Format("15:04") != tc.us || p.TopixExtra.In(jp).Format("15:04") != tc.london {
			t.Fatalf("%s: %+v", tc.day, p)
		}
		if p.ForceClose.In(jp).Format("15:04") != "05:50" || p.ExtraUntil.In(jp).Format("15:04") != "05:40" {
			t.Fatal(p)
		}
	}
	start, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-21 08:45", jp)
	p, err := Build("2026-10-21_Day", start, start.Add(7*time.Hour), c)
	if err != nil || p.TopixExtra.In(jp).Format("15:04") != "12:30" || p.ForceClose.In(jp).Format("15:04") != "15:35" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err = Build("2026-10-20_Day", start, start.Add(7*time.Hour), c); err == nil {
		t.Fatal("異なる日付を許可しました")
	}
}

// TestJapaneseInputAcrossSeparateDST は冬の日本時間指定から米欧別々の夏時間と翌日を再現します。
//
// 引数: t *testing.T は試験です。
// 返り値: なし。旧ローカル時刻指定と計画全体が同値であることも検査します。
func TestJapaneseInputAcrossSeparateDST(t *testing.T) {
	jp, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	old := config.Defaults().Schedule
	current := old
	current.DayExtraJST, current.USOpenJST, current.LondonExtraJST = "12:30", "23:30", "01:30"
	for _, tc := range []struct{ day, us, london string }{
		{"2026-03-06", "23:30", "01:30"},
		{"2026-03-09", "22:30", "01:30"},
		{"2026-03-30", "22:30", "00:30"},
		{"2026-10-20", "22:30", "00:30"},
		{"2026-10-26", "22:30", "01:30"},
		{"2026-11-02", "23:30", "01:30"},
	} {
		t.Run(tc.day, func(t *testing.T) {
			start, err := time.ParseInLocation("2006-01-02 15:04", tc.day+" 17:00", jp)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := Build(tc.day+"_Night", start, start.Add(13*time.Hour), old)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := Build(tc.day+"_Night", start, start.Add(13*time.Hour), current)
			if err != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatalf("新旧時計の場内計画が異なります: %v / %+v / %+v", err, actual, expected)
			}
			if actual.USOpen.In(jp).Format("2006-01-02 15:04") != tc.day+" "+tc.us ||
				actual.TopixExtra.In(jp).Format("2006-01-02 15:04") != start.AddDate(0, 0, 1).Format("2006-01-02")+" "+tc.london {
				t.Fatalf("米欧別DSTまたは翌日日付が不正です: %+v", actual)
			}
			if actual.NKExtra.Sub(actual.USOpen) != 30*time.Minute {
				t.Fatal("米国開始から日経追加までの間隔が変化しました")
			}
		})
	}
	start, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-21 08:45", jp)
	day, err := Build("2026-10-21_Day", start, start.Add(7*time.Hour), current)
	if err != nil || day.TopixExtra.In(jp).Format("2006-01-02 15:04") != "2026-10-21 12:30" || !day.TopixExtra.Equal(day.NKExtra) {
		t.Fatalf("日中の日本時間指定が不正です: %v / %+v", err, day)
	}
}
