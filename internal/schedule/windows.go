package schedule

import (
	"fmt"
	"sort"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// Slot は当場のcontroller時計に解決した一つの時間帯です。
// Methodは内部短ID、Enabled=falseは時間帯静観であり商品OFFとは異なります。
type Slot struct {
	ID      string    `json:"id"`
	Method  string    `json:"method"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Enabled bool      `json:"enabled"`
}

// ResolveSlot は現在時刻に適用する時間帯を半開区間で選択します。
//
// 引数: plan(Plan)、cfg(config.Config)、reference(string)、now(time.Time)は当場予定・単品設定・参照市場・現在時計です。
// 返り値: Slotは適用時間帯、errorは枠の不正です。設定に空白がある時間は静観を返します。
func ResolveSlot(plan Plan, cfg config.Config, reference string, now time.Time) (Slot, error) {
	slots, err := ResolveSlots(plan, cfg, reference)
	if err != nil {
		return Slot{}, err
	}
	for _, slot := range slots {
		if !now.Before(slot.Start) && now.Before(slot.End) {
			return slot, nil
		}
	}
	if now.Before(plan.Start) {
		return Slot{ID: "__before_session", Start: plan.Start, End: plan.Start}, nil
	}
	return Slot{ID: "__after_session", Start: plan.End, End: plan.End}, nil
}

// ResolveSlots は明示枠と空白静観を当場のJST時刻へ解決します。
//
// 引数: plan(Plan)、cfg(config.Config)、reference(string)はcontroller予定・単品設定・参照市場です。
// 返り値: []Slotは時刻順で全立会を覆う一覧、errorは場外・重複・逆転等です。立会時刻を独自定義しません。
func ResolveSlots(plan Plan, cfg config.Config, reference string) ([]Slot, error) {
	if plan.Start.IsZero() || !plan.End.After(plan.Start) {
		return nil, fmt.Errorf("時間帯を解決する立会時計が不正です")
	}
	var slots []Slot
	for _, window := range cfg.MethodWindows() {
		if window.Session != plan.Kind {
			continue
		}
		start, err := resolveWindowClock(plan, window.Start)
		if err != nil {
			return nil, fmt.Errorf("時間帯%sの開始: %w", window.ID, err)
		}
		end, err := resolveWindowClock(plan, window.End)
		if err != nil {
			return nil, fmt.Errorf("時間帯%sの終了: %w", window.ID, err)
		}
		if start.Before(plan.Start) || end.After(plan.End) || !end.After(start) {
			return nil, fmt.Errorf("時間帯%sは当場%s〜%sの範囲内で開始より後に終了する必要があります", window.ID, plan.Start.Format(time.RFC3339), plan.End.Format(time.RFC3339))
		}
		method, err := config.ShortMethodID(reference, window.Method)
		if err != nil {
			return nil, fmt.Errorf("時間帯%s: %w", window.ID, err)
		}
		if method != "" {
			if _, err := rules.SelectProfile(reference, plan.Kind, method); err != nil {
				return nil, fmt.Errorf("時間帯%s: %w", window.ID, err)
			}
		}
		if config.IsWindowOnlyMethod(method) && (reference != "NK225" || plan.Kind != "Night" || plan.USOpen.IsZero() ||
			(method != "U165_03" && end.After(plan.USOpen.Add(-15*time.Minute))) ||
			(method == "U165_03" && start.Before(plan.USOpen.Add(15*time.Minute)))) {
			return nil, fmt.Errorf("時間帯%s: %sの米国開始前後の範囲が不正です", window.ID, window.Method)
		}
		slots = append(slots, Slot{ID: window.ID, Method: method, Start: start, End: end, Enabled: method != ""})
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].Start.Before(slots[j].Start) })
	var result []Slot
	previous := plan.Start
	for i, slot := range slots {
		if slot.Start.Before(previous) {
			return nil, fmt.Errorf("時間帯%sが直前の時間帯と重複します", slot.ID)
		}
		if slot.Start.After(previous) {
			result = append(result, Slot{ID: fmt.Sprintf("__gap_%d", i), Start: previous, End: slot.Start})
		}
		result = append(result, slot)
		previous = slot.End
	}
	if previous.Before(plan.End) {
		result = append(result, Slot{ID: fmt.Sprintf("__gap_%d", len(slots)), Start: previous, End: plan.End})
	}
	return result, nil
}

// resolveWindowClock はJST時計・立会端・米国開始相対の一地点を求めます。
//
// 引数: plan(Plan)、value(string)は当場予定と設定表記です。
// 返り値: time.Timeは実時刻、errorは表記不正又は米国開始の欠測です。
func resolveWindowClock(plan Plan, value string) (time.Time, error) {
	moment, err := config.ParseWindowMoment(value)
	if err != nil {
		return time.Time{}, err
	}
	switch moment.Anchor {
	case "SESSION_START":
		return plan.Start, nil
	case "SESSION_END":
		return plan.End, nil
	case "US_OPEN":
		if plan.Kind != "Night" || plan.USOpen.IsZero() {
			return time.Time{}, fmt.Errorf("当場に米国開始時刻がありません")
		}
		return plan.USOpen.Add(time.Duration(moment.Minutes) * time.Minute), nil
	case "JST":
		jp, err := time.LoadLocation("Asia/Tokyo")
		if err != nil {
			return time.Time{}, err
		}
		date := plan.Start.In(jp)
		value := time.Date(date.Year(), date.Month(), date.Day(), moment.Minutes/60, moment.Minutes%60, 0, 0, jp)
		if plan.Kind == "Night" && moment.Minutes < 12*60 {
			value = value.AddDate(0, 0, 1)
		}
		return value, nil
	}
	return time.Time{}, fmt.Errorf("未知の時間帯時計です")
}
