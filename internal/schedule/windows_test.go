package schedule

import (
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
)

// windowPlan は指定日の夜間予定をcontroller応答と同じ時計で作ります。
//
// 引数: t(*testing.T)、date(string)は試験と年月日。返り値: Planは当場の予定です。
func windowPlan(t *testing.T, date string) Plan {
	t.Helper()
	jp, _ := time.LoadLocation("Asia/Tokyo")
	start, err := time.ParseInLocation("2006-01-02 15:04", date+" 17:00", jp)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Build(date+"_Night", start, start.Add(13*time.Hour), config.Defaults().Schedule)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestWindowResolutionDSTAndBoundaries は夏冬・空白静観・半開境界・翌日を検査します。
//
// 引数: t(*testing.T)は試験。返り値: なし。
func TestWindowResolutionDSTAndBoundaries(t *testing.T) {
	cfg := config.Defaults()
	cfg.Market = &config.SingleMarket{Product: "NK225mini", MaxPerSide: 10}
	cfg.Windows = []config.MethodWindow{{ID: "pre", Session: "Night", Start: "SESSION_START", End: "US_OPEN-00:15", Method: "NK225_D07"}, {ID: "post", Session: "Night", Start: "US_OPEN+00:15", End: "03:00", Method: "NK225_PG04"}}
	for _, tc := range []struct{ date, end string }{{"2026-10-26", "22:15"}, {"2026-11-02", "23:15"}} {
		plan := windowPlan(t, tc.date)
		slots, err := ResolveSlots(plan, cfg, "NK225")
		if err != nil {
			t.Fatal(err)
		}
		if len(slots) != 4 || slots[0].End.In(plan.Start.Location()).Format("15:04") != tc.end || slots[1].Enabled || slots[3].Enabled {
			t.Fatalf("夏冬又は空白静観が不正: %+v", slots)
		}
		atBoundary, err := ResolveSlot(plan, cfg, "NK225", slots[0].End)
		if err != nil || atBoundary.ID != slots[1].ID {
			t.Fatal("終了境界が旧枠に残りました", atBoundary, err)
		}
		atPost, err := ResolveSlot(plan, cfg, "NK225", slots[2].Start)
		if err != nil || atPost.Method != "PG04" {
			t.Fatal("新枠開始境界が不正", atPost, err)
		}
		if slots[2].End.Sub(plan.Start) != 10*time.Hour {
			t.Fatal("03:00が翌日ではありません")
		}
		first, _ := ResolveSlot(plan, cfg, "NK225", plan.Start.Add(-time.Minute))
		second, _ := ResolveSlot(plan, cfg, "NK225", plan.Start.Add(-time.Second))
		if first != second {
			t.Fatal("場前の枠が時計ごとに変わりました")
		}
	}
}

// TestWindowRuntimeRejectsOutsideAndOverlap はcontroller当場時刻による最終検査を確認します。
//
// 引数: t(*testing.T)は試験。返り値: なし。
func TestWindowRuntimeRejectsOutsideAndOverlap(t *testing.T) {
	plan := windowPlan(t, "2026-10-26")
	cfg := config.Defaults()
	for _, windows := range [][]config.MethodWindow{
		{{ID: "one", Session: "Night", Start: "16:59", End: "20:00", Method: "NK225_D07"}},
		{{ID: "one", Session: "Night", Start: "17:00", End: "06:01", Method: "NK225_D07"}},
		{{ID: "one", Session: "Night", Start: "17:00", End: "20:01", Method: "NK225_D07"}, {ID: "two", Session: "Night", Start: "20:00", End: "21:00", Method: "NK225_D07"}},
	} {
		cfg.Windows = windows
		if _, err := ResolveSlots(plan, cfg, "NK225"); err == nil {
			t.Fatal("場外又は重複を許可しました")
		}
	}
}
