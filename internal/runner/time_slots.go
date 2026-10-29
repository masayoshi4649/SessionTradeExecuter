package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/schedule"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// slotTransition は時間帯境界の取消照合と所有玉返済の進行を保存します。
type slotTransition struct {
	Target    schedule.Slot `json:"target"`
	Phase     string        `json:"phase"`
	StartedAt time.Time     `json:"started_at"`
}

// sameSlot は同じ手法を異なる枠で再使用する場合も別の運転として区別します。
//
// 引数: left/right(schedule.Slot)。返り値: bool は同じ時間枠の指定ならtrueです。
func sameSlot(left, right schedule.Slot) bool {
	return left.ID == right.ID && left.Method == right.Method && left.Enabled == right.Enabled &&
		left.Start.Equal(right.Start) && left.End.Equal(right.End)
}

// resolveMethodSlot は当場の時間帯一覧を一度だけ解決し、受信ごとの日時変換を避けます。
//
// 引数: m(*marketRuntime)、now(time.Time)。返り値: schedule.Slot は適用枠、error は設定不整合です。
// 設定変更は運転OFF時だけ許されるため、同じ当場の一覧は稼働中に変化しません。
func (a *app) resolveMethodSlot(m *marketRuntime, now time.Time) (schedule.Slot, error) {
	plan := m.Saved.Plan
	if a.slotClockReversed(m, now) {
		now = m.Saved.SlotEvaluatedAt
	}
	if m.MethodSlots == nil || m.MethodSlotsPlan != plan.ID {
		slots, err := schedule.ResolveSlots(plan, a.cfg, m.Reference)
		if err != nil {
			return schedule.Slot{}, err
		}
		m.MethodSlots, m.MethodSlotsPlan = slots, plan.ID
	}
	for _, slot := range m.MethodSlots {
		if !now.Before(slot.Start) && now.Before(slot.End) {
			return slot, nil
		}
	}
	if now.Before(plan.Start) {
		return schedule.Slot{ID: "__before_session", Start: plan.Start, End: plan.Start}, nil
	}
	return schedule.Slot{ID: "__after_session", Start: plan.End, End: plan.End}, nil
}

// slotClockReversed は端末の壁時計が当場で既に評価した最大時刻より前かを判定します。
//
// 引数: m(*marketRuntime)、now(time.Time)。返り値: bool は新規停止が必要な時計逆行です。
// 単調時計成分を除いて比較し、OSの時刻補正でも終了済みの枠を再開しません。
func (a *app) slotClockReversed(m *marketRuntime, now time.Time) bool {
	return len(a.cfg.Windows) > 0 && !m.Saved.SlotEvaluatedAt.IsZero() && now.Round(0).Before(m.Saved.SlotEvaluatedAt.Round(0))
}

// slotBlockReason は時間帯の指定・切替状態に基づく新規禁止理由を返します。
//
// 引数: m(*marketRuntime)、now(time.Time)。返り値: string は禁止理由、空文字は時間帯上の許可です。
func (a *app) slotBlockReason(m *marketRuntime, now time.Time) string {
	if len(a.cfg.Windows) == 0 {
		return ""
	}
	if a.slotClockReversed(m, now) {
		return "端末時計の逆行・新規停止中、取消照合と返済は継続"
	}
	if m.Saved.SlotSwitch != nil {
		return "時間帯切替中・所有注文の取消照合と所有玉の返済を継続"
	}
	target, err := a.resolveMethodSlot(m, now)
	if err != nil {
		return "時間帯設定の照合エラー: " + err.Error()
	}
	if m.Saved.ActiveSlot == nil || !sameSlot(*m.Saved.ActiveSlot, target) {
		return "時間帯の境界・旧手法の返済と切替待ち"
	}
	if !target.Enabled {
		return "指定時間帯は静観・共通指標の観測を継続"
	}
	return ""
}

// newSlotPortfolio は共通指標を破棄せず、指定手法の売買状態だけを新規作成します。
//
// 引数: m(*marketRuntime)、plan(schedule.Plan)、slot(*schedule.Slot|nil)、namespace(string)。
// 返り値: *strategy.Portfolio と error。履歴による影の売買は再生しません。
func (a *app) newSlotPortfolio(m *marketRuntime, plan schedule.Plan, slot *schedule.Slot, namespace string) (*strategy.Portfolio, error) {
	ref := m.Reference
	mc := a.cfg.Markets[ref]
	method := a.cfg.MethodID(plan.Kind)
	if slot != nil && slot.Enabled {
		method = slot.Method
	}
	params, err := methodParametersFor(a.cfg, ref, plan.Kind, method)
	if err != nil {
		return nil, err
	}
	baseline, err := methodParameters(a.cfg, ref, plan.Kind)
	if err != nil {
		return nil, err
	}
	if err = validateSharedFeatureParameters(baseline, params); err != nil {
		return nil, err
	}
	if slot != nil && a.features != nil && a.plan.ID == plan.ID {
		if a.features.E01 == nil || a.features.E01.Config != params.Features {
			return nil, fmt.Errorf("時間帯の指標設定と稼働中の共通指標が一致しないため切り替えません")
		}
		if params.Method != "" && !a.features.ResearchEnabled {
			return nil, fmt.Errorf("時間帯の研究特徴が履歴から準備されていないため切り替えません")
		}
	}
	extra := plan.TopixExtra
	if ref == "NK225" {
		extra = plan.NKExtra
	}
	return strategy.New(strategy.Config{Namespace: namespace, Reference: ref, Product: mc.Product,
		CoreQuantity: mc.CoreQuantity, ExtraMaxQuantity: mc.ExtraQuantity, Cap: mc.MaxPerSide,
		LargeTick: a.cfg.Products[ref].Tick, PointValue: a.cfg.Products[mc.Product].PointValue,
		Parameters: params, RuleExpressions: a.cfg.RuleExpressionsForMethod(ref, plan.Kind, method)},
		strategy.Session{ID: plan.ID, Kind: plan.Kind, Start: seconds(plan.Start), End: seconds(plan.End),
			ForceClose: seconds(plan.ForceClose), USOpen: seconds(plan.USOpen), ExtraAt: seconds(extra)})
}

// discardSlotQueue は未送信判断だけを終了し、送信済みの結果を台帳照合へ残します。
//
// 引数: m(*marketRuntime)。返り値: error は注文状態または保存の不整合です。
func (a *app) discardSlotQueue(m *marketRuntime) error {
	sent := map[string]bool{}
	for _, order := range m.Manager.Orders() {
		sent[order.IntentID] = true
	}
	for _, queued := range m.Saved.Queue {
		if !sent[queued.Signal.ID] {
			if err := m.Portfolio.OnOrderRejected(queued.Signal.ID); err != nil {
				return err
			}
		}
	}
	m.Saved.Queue = nil
	m.Dirty = true
	return a.checkpoint(m)
}

// updateMethodSlot は境界の未確定注文を解消し、当運転の所有玉がゼロになってから切り替えます。
//
// 引数: ctx(context.Context)、m(*marketRuntime)、now(time.Time)。
// 返り値: bool は当枠で判断可能、error は状態・保存異常です。取消受付だけでは切り替えません。
func (a *app) updateMethodSlot(ctx context.Context, m *marketRuntime, now time.Time) (bool, error) {
	if len(a.cfg.Windows) == 0 {
		return true, nil
	}
	if a.slotClockReversed(m, now) {
		if m.Saved.SlotSwitch != nil {
			_, err := a.advanceSlotTransition(ctx, m, now)
			return false, err
		}
		// 既存手法の返済判断は進めます。新規はslotBlockReasonと送信直前検査で拒否します。
		return true, nil
	}
	if now.Round(0).After(m.Saved.SlotEvaluatedAt) {
		m.Saved.SlotEvaluatedAt = now.Round(0)
		m.Dirty = true
	}
	target, err := a.resolveMethodSlot(m, now)
	if err != nil {
		return false, err
	}
	if m.Saved.SlotSwitch == nil && m.Saved.ActiveSlot != nil && sameSlot(*m.Saved.ActiveSlot, target) {
		return target.Enabled, nil
	}
	if m.Saved.SlotSwitch == nil {
		m.Saved.SlotSwitch = &slotTransition{Target: target, Phase: "cancel", StartedAt: now}
		if err = a.discardSlotQueue(m); err != nil {
			return false, err
		}
		if err = m.Manager.Event("時間帯切替開始", m.Saved.Plan.ID, m.Reference, "", now,
			map[string]any{"previous": m.Saved.ActiveSlot, "next": target}); err != nil {
			return false, err
		}
	} else if !sameSlot(m.Saved.SlotSwitch.Target, target) {
		// 照合が長引いて次の境界も過ぎた場合、過去の枠を後追い起動しません。
		m.Saved.SlotSwitch.Target = target
		m.Dirty = true
		if err = a.checkpoint(m); err != nil {
			return false, err
		}
	}
	return a.advanceSlotTransition(ctx, m, now)
}

// advanceSlotTransition は取消照合と返済を別段階にして、境界返済自身の取消ループを防ぎます。
//
// 引数: ctx(context.Context)、m(*marketRuntime)、now(time.Time)。
// 返り値: bool は新枠の稼働可否、error は状態または保存の不整合です。
func (a *app) advanceSlotTransition(ctx context.Context, m *marketRuntime, now time.Time) (bool, error) {
	transition := m.Saved.SlotSwitch
	if transition == nil {
		return false, fmt.Errorf("時間帯切替の状態がありません")
	}
	if transition.Phase == "cancel" {
		if m.Manager.HasPending() {
			if !a.cfg.Trading.DryRun && !a.accountFresh(now) {
				return false, nil
			}
			if err := m.Manager.CancelAllOwnedPending(ctx, a.client, a.view, now); err != nil {
				return false, a.notice("時間帯切替の取消照合待ち", err)
			}
		}
		if err := a.applyFills(m); err != nil {
			return false, err
		}
		if m.Manager.HasPending() || len(m.Manager.OutstandingFills()) > 0 {
			return false, nil
		}
		transition.Phase = "close"
		m.Dirty = true
		if err := a.checkpoint(m); err != nil {
			return false, err
		}
	}
	if transition.Phase != "close" {
		return false, fmt.Errorf("時間帯切替の段階が不正です: %s", transition.Phase)
	}
	if m.Manager.HasPending() && a.accountFresh(now) {
		if err := m.Manager.CancelPending(ctx, a.client, now, false); err != nil {
			return false, a.notice("時間帯返済の期限照合待ち", err)
		}
	}
	if err := a.applyFills(m); err != nil {
		return false, err
	}
	if err := a.enqueue(m, m.Portfolio.Flatten(seconds(now), "時間帯の境界で当運転の所有玉を返済"), 0, now); err != nil {
		return false, err
	}
	if err := a.dispatch(ctx, m, now); err != nil {
		return false, err
	}
	if m.Manager.HasPending() || len(m.Manager.OutstandingFills()) > 0 || len(m.Manager.Positions()) > 0 || len(m.Saved.Queue) > 0 {
		return false, nil
	}
	status := m.Portfolio.Status()
	if len(status.Pending) > 0 {
		return false, fmt.Errorf("所有台帳が完了しても旧手法の未確定注文が残っています")
	}
	for _, position := range status.Positions {
		if position.Quantity != 0 {
			return false, fmt.Errorf("所有台帳がゼロでも旧手法の建玉が残っています")
		}
	}
	if a.slotClockReversed(m, now) {
		return false, nil
	}
	target := transition.Target
	m.Saved.SlotSequence++
	namespace := fmt.Sprintf("%s-slot-%d", m.Saved.Namespace, m.Saved.SlotSequence)
	portfolio, err := a.newSlotPortfolio(m, m.Saved.Plan, &target, namespace)
	if err != nil {
		return false, err
	}
	m.Portfolio = portfolio
	m.Saved.ActiveSlot, m.Saved.SlotSwitch = &target, nil
	m.Saved.SlotActivatedAt = now
	m.Saved.LastRawHash = ""
	m.Dirty = true
	if err = a.checkpoint(m); err != nil {
		return false, err
	}
	if err = m.Manager.Event("時間帯切替完了", m.Saved.Plan.ID, m.Reference, "", now,
		map[string]any{"slot": target, "namespace": namespace, "bootstrap": "共通指標のみ継続、過去売買の影再生なし"}); err != nil {
		return false, err
	}
	return target.Enabled, nil
}
