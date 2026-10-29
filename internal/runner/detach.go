package runner

import (
	"context"
	"time"
)

// detaching は保有玉を返済せず管理外へ移す終了要求を確認します。
//
// 引数: なし。返り値: boolはWebのOFFまたはモード変更による管理終了要求です。
func (a *app) detaching() bool {
	return a.control != nil && a.control.DetachStopRequested()
}

// discardDetachedQueue は未送信の新規と返済を破棄し、送信済みの依頼は台帳照合だけに残します。
//
// 引数: m(*marketRuntime)。返り値: errorは戦略予約解除・保存失敗です。
func (a *app) discardDetachedQueue(m *marketRuntime) error {
	if len(m.Saved.Queue) == 0 {
		return nil
	}
	sent := map[string]bool{}
	for _, order := range m.Manager.Orders() {
		sent[order.IntentID] = true
	}
	for _, queued := range m.Saved.Queue {
		if !sent[queued.Signal.ID] && m.Portfolio != nil {
			if err := m.Portfolio.OnOrderRejected(queued.Signal.ID); err != nil {
				return err
			}
		}
	}
	m.Saved.Queue = nil
	m.Dirty = true
	return a.checkpoint(m)
}

// tickDetach は発注と自動返済を行わず、当運転の未約定注文だけを取消照合します。
//
// 引数: ctx(context.Context)は取消用の通信期限、now(time.Time)は現在時計。
// 返り値: errorは保存・整合性の失敗です。取消結果不明は台帳と画面へ残して照合を継続します。
func (a *app) tickDetach(ctx context.Context, now time.Time) error {
	for _, m := range a.markets {
		if err := a.discardDetachedQueue(m); err != nil {
			return err
		}
		if m.Manager.HasPending() && (a.cfg.Trading.DryRun || a.detachAccountFresh(now)) {
			if err := m.Manager.CancelAllOwnedPending(ctx, a.client, a.view, now); err != nil {
				if err = a.notice("管理終了時の取消照合待ち", err); err != nil {
					return err
				}
			}
		}
		if err := a.applyFills(m); err != nil {
			return err
		}
		if m.Dirty {
			if err := a.checkpoint(m); err != nil {
				return err
			}
		}
	}
	a.acknowledgePause(a.control.pauseSequence.Load())
	return nil
}

// detachAccountFresh は取消のために注文・建玉情報の鮮度を確認し、新規送信時間とは分けます。
//
// 引数: now(time.Time)。返り値: boolは接続済みで新鮮な口座情報の場合trueです。
func (a *app) detachAccountFresh(now time.Time) bool {
	v := a.view
	if !v.Connected || v.BrokerStale || v.PositionsStale || v.BrokerError != "" || v.PositionsError != "" ||
		now.Sub(a.viewAt) > duration(a.cfg.Runtime.AccountMaxAgeSeconds) {
		return false
	}
	for _, at := range []*time.Time{v.BrokerUpdatedAt, v.PositionsUpdatedAt} {
		if at == nil || now.Sub(*at) > duration(a.cfg.Runtime.AccountMaxAgeSeconds) || at.After(now.Add(time.Second)) {
			return false
		}
	}
	return true
}

// detachStopReady は所有建玉を残したままでも、注文と約定の記録が完了したか確認します。
//
// 引数: なし。返り値: boolは未送信・未確定・未反映の注文が全て解消した場合trueです。
func (a *app) detachStopReady() bool {
	if !a.detaching() || a.control.pausePending() {
		return false
	}
	for _, m := range a.markets {
		if m.Manager.HasPending() || len(m.Manager.OutstandingFills()) != 0 || len(m.Saved.Queue) != 0 {
			return false
		}
	}
	return true
}

// finishDetach は残った建玉を監査記録へ残し、返済せずにこの運転の管理を終了します。
//
// 引数: なし。返り値: errorは監査記録・最終状態の保存失敗です。
func (a *app) finishDetach() error {
	for _, m := range a.markets {
		if err := m.Manager.Event("保有玉の管理外移管", m.Saved.Plan.ID, m.Reference, "", time.Now(), map[string]any{
			"positions": m.Manager.Positions(), "orders": m.Manager.Orders(), "dryrun": a.cfg.Trading.DryRun,
			"reason": "OFFまたはモード変更により新規・自動返済を終了。旧建玉は次の運転へ引き継がず、次の数量上限から除外します。",
		}); err != nil {
			return err
		}
	}
	return a.finish("保有玉を管理外へ移して運転終了", nil)
}
