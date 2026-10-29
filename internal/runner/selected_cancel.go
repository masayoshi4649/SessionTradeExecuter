package runner

import (
	"context"
	"errors"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
)

// cancelSelectedEntries は選定手法の取消対象だけを未送信列と送信済み台帳へ反映します。
//
// 引数: ctx(context.Context)は取消通信、m(*marketRuntime)は商品、ids([]string)は取消対象の新規意図ID、now(time.Time)は現在時計です。
// 返り値: errorは戦略・永続化の失敗です。保存済みの取消照合待ちは通知し、実約定とDone通知を既存経路で反映します。
func (a *app) cancelSelectedEntries(ctx context.Context, m *marketRuntime, ids []string, now time.Time) error {
	if m.Portfolio == nil || len(ids) == 0 {
		return nil
	}
	selected, sent := map[string]bool{}, map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	for _, order := range m.Manager.Orders() {
		sent[order.IntentID] = true
	}
	kept := make([]queuedSignal, 0, len(m.Saved.Queue))
	for _, queued := range m.Saved.Queue {
		if queued.Signal.Action != "new" || !selected[queued.Signal.ID] {
			kept = append(kept, queued)
			continue
		}
		if !sent[queued.Signal.ID] {
			if err := m.Portfolio.OnOrderRejected(queued.Signal.ID); err != nil {
				return err
			}
			if err := m.Manager.Event("手法による未送信新規取消", m.Saved.Plan.ID, m.Reference, queued.Signal.ID, now, queued); err != nil {
				return err
			}
		}
	}
	m.Saved.Queue = kept
	// 取消理由と新しい予約はenqueue後の状態で、通信前に同じ正本へ確定します。
	m.Dirty = true
	if err := a.checkpoint(m); err != nil {
		return err
	}
	if a.cfg.Trading.DryRun || a.detachAccountFresh(now) {
		if err := m.Manager.CancelSelectedEntries(ctx, a.client, ids, now); err != nil {
			var pending *execution.SelectedCancellationPending
			if !errors.As(err, &pending) {
				return err
			}
			if err := a.notice("手法の個別新規取消照合待ち", err); err != nil {
				return err
			}
		}
	}
	return a.applyFills(m)
}
