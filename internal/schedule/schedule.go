// Package schedule はcontrollerの立会と各国の時計を接続します。
package schedule

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
)

// Plan は実行中の立会と、その中の判断時刻です。
type Plan struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	ForceClose time.Time `json:"force_close"`
	USOpen     time.Time `json:"us_open"`
	TopixExtra time.Time `json:"topix_extra"`
	NKExtra    time.Time `json:"nk_extra"`
	ExtraUntil time.Time `json:"extra_until"`
}

// Build はAPIの開始・終了に基づき、米欧それぞれの夏時間を適用します。
//
// 引数: id(string)は立会ID、start/end(time.Time)はcontroller提供時刻、cfg(config.Schedule)は判断時刻設定。
// 返り値: Planとerror。休日や立会開始を独自に推測しません。
func Build(id string, start, end time.Time, cfg config.Schedule) (Plan, error) {
	p := Plan{ID: id, Start: start, End: end}
	if err := cfg.NormalizeJST(); err != nil {
		return p, err
	}
	if start.IsZero() || !end.After(start) {
		return p, fmt.Errorf("立会開始・終了が不正です")
	}
	if strings.HasSuffix(id, "_Day") {
		p.Kind = "Day"
	} else if strings.HasSuffix(id, "_Night") {
		p.Kind = "Night"
	} else {
		return p, fmt.Errorf("立会IDが不正です: %s", id)
	}
	jp, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		return p, err
	}
	if start.In(jp).Format("2006-01-02")+"_"+p.Kind != id {
		return p, fmt.Errorf("立会IDと開始日の不一致です")
	}
	p.ForceClose = end.Add(-time.Duration(cfg.ForceCloseBeforeSeconds * float64(time.Second)))
	p.ExtraUntil = p.ForceClose.Add(-time.Duration(cfg.ExtraCutoffSeconds * float64(time.Second)))
	if !p.ExtraUntil.After(start) {
		return p, fmt.Errorf("追加新規期限が立会開始以前です")
	}
	if p.Kind == "Day" {
		p.TopixExtra, err = localClock(start, "Asia/Tokyo", cfg.DayExtra)
		p.NKExtra = p.TopixExtra
	} else {
		p.USOpen, err = localClock(start, "America/New_York", cfg.USOpen)
		if err != nil {
			return p, err
		}
		p.NKExtra = p.USOpen.Add(time.Duration(cfg.USExtraDelayMinutes) * time.Minute)
		p.TopixExtra, err = localClock(start, "Europe/London", cfg.LondonExtra)
	}
	if err != nil {
		return p, err
	}
	return p, nil
}

// localClock は開始時点の現地日付に、指定された現地時刻を置きます。
//
// 引数: start(time.Time)は立会開始、zone/clock(string)はIANA地域名とHH:MM。
// 返り値: time.Timeとerror。欧米の夏時間切替日が違っても地域別に求めます。
func localClock(start time.Time, zone, clock string) (time.Time, error) {
	location, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse("15:04", clock)
	if err != nil {
		return time.Time{}, err
	}
	local := start.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), parsed.Hour(), parsed.Minute(), 0, 0, location), nil
}
