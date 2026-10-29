package schedule

import (
	"reflect"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
)

// TestSelectedStandardWindowsSummerWinter は米国前返済・静観・後半の境界と夏冬切替を確認します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。元の立会開始・US開始・追加時計を変更しません。
func TestSelectedStandardWindowsSummerWinter(t *testing.T) {
	cfg := config.Defaults()
	cfg.Market = &config.SingleMarket{Product: "NK225mini", MaxPerSide: 10}
	cfg.Windows = config.SelectedNK225Windows()
	for _, tc := range []struct{ date, preEnd, postStart string }{
		{"2026-03-06", "23:15", "23:45"},
		{"2026-03-09", "22:15", "22:45"},
		{"2026-10-26", "22:15", "22:45"},
		{"2026-11-02", "23:15", "23:45"},
	} {
		t.Run(tc.date, func(t *testing.T) {
			plan := windowPlan(t, tc.date)
			before := plan
			slots, err := ResolveSlots(plan, cfg, "NK225")
			if err != nil || len(slots) != 3 {
				t.Fatal(slots, err)
			}
			if slots[0].Method != "P164_06" || !slots[0].Start.Equal(plan.Start) || slots[0].End.In(plan.Start.Location()).Format("15:04") != tc.preEnd ||
				slots[1].Enabled || slots[2].Method != "U165_03" || slots[2].Start.In(plan.Start.Location()).Format("15:04") != tc.postStart || !slots[2].End.Equal(plan.End) {
				t.Fatal("標準前後半の時刻・手法・静観が不正です", slots)
			}
			for _, boundary := range []struct {
				at time.Time
				id string
			}{
				{slots[0].End.Add(-time.Nanosecond), "NightPreUS"},
				{slots[0].End, "NightUSQuiet"},
				{slots[2].Start.Add(-time.Nanosecond), "NightUSQuiet"},
				{slots[2].Start, "NightPostUS"},
				{plan.End, "__after_session"},
			} {
				got, err := ResolveSlot(plan, cfg, "NK225", boundary.at)
				if err != nil || got.ID != boundary.id {
					t.Fatal("半開境界の判定が不正です", boundary, got, err)
				}
			}
			if !reflect.DeepEqual(before, plan) || plan.NKExtra.Sub(plan.USOpen) != 30*time.Minute {
				t.Fatal("時間枠の開始へ元の立会・米国時計をずらしました")
			}
		})
	}
	jp, _ := time.LoadLocation("Asia/Tokyo")
	start, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-26 08:45", jp)
	day, err := Build("2026-10-26_Day", start, start.Add(7*time.Hour), cfg.Schedule)
	if err != nil {
		t.Fatal(err)
	}
	slots, err := ResolveSlots(day, cfg, "NK225")
	if err != nil || len(slots) != 1 || slots[0].Method != "D164_15" || !slots[0].Start.Equal(day.Start) || !slots[0].End.Equal(day.End) {
		t.Fatal("日中の既存D07枠を変更しました", slots, err)
	}
}

// TestSelectedRuntimeWindowLimit は読込検査を通さない呼出しでも米国後や別銘柄のP05を拒否します。
//
// 引数: t(*testing.T)は試験です。返り値: なし。
func TestSelectedRuntimeWindowLimit(t *testing.T) {
	plan := windowPlan(t, "2026-10-26")
	cfg := config.Defaults()
	cfg.Windows = []config.MethodWindow{{ID: "pre", Session: "Night", Start: "SESSION_START", End: "US_OPEN-00:14", Method: "NK225_P164_06"}}
	if _, err := ResolveSlots(plan, cfg, "NK225"); err == nil {
		t.Fatal("米国直前へP05を延長しました")
	}
	cfg.Windows[0].Method, cfg.Windows[0].End = "P164_06", "US_OPEN-00:15"
	if _, err := ResolveSlots(plan, cfg, "TOPIX"); err == nil {
		t.Fatal("TOPIXへP05を適用しました")
	}
}
