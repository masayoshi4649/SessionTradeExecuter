package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// SelectedCancellationPending は保存済みの個別取消が上流の照合を待つ状態です。
// 保存失敗とは区別し、取消受付だけで注文完了や保有減少と扱いません。
type SelectedCancellationPending struct{ Cause error }

// Error は照合待ちの理由を返します。
//
// 引数: なし。返り値: stringは利用者へ通知する理由です。
func (e *SelectedCancellationPending) Error() string { return e.Cause.Error() }

// Unwrap は取消通信の原因を外側の検査へ渡します。
//
// 引数: なし。返り値: errorは元の通信・注文番号不明の理由です。
func (e *SelectedCancellationPending) Unwrap() error { return e.Cause }

// CancelSelectedEntries は指定した自分の新規意図だけを期限前でも一度取り消します。
//
// 引数: ctx(context.Context)は通信期限、api(CancelAPI)は取消先、ids([]string)は戦略の意図ID、now(time.Time)は記録時計です。
// 返り値: errorは保存失敗又は照合待ちです。返済・他の新規・保有玉を変更しません。
// 実注文は取消要求を先に保存し、応答不明や拒否でも再送せずReconcileで終了と競合約定を確認します。
func (m *Manager) CancelSelectedEntries(ctx context.Context, api CancelAPI, ids []string, now time.Time) error {
	selected := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			selected[id] = true
		}
	}
	for _, p := range m.state.Orders {
		if !selected[p.Intent.ID] || p.Intent.TradeType != "new" || p.Status == "done" || p.Status == "rejected" || p.CancelRequested {
			continue
		}
		if m.state.DryRun {
			p.Status, p.CancelRequested, p.CancelState = "done", true, "accepted"
			p.Error = "手法の個別取消で未実行の仮想新規を終了しました。既存約定は保持します"
			if err := m.save(); err != nil {
				return err
			}
			continue
		}
		if api == nil {
			return fmt.Errorf("個別新規取消の通信機能がありません")
		}
		if p.BrokerOrderID == "" {
			p.Error = "個別新規取消は注文番号不明のため照合待ちです。推測した番号で取消・再送しません"
			if err := m.save(); err != nil {
				return err
			}
			return &SelectedCancellationPending{Cause: errors.New(p.Error)}
		}
		p.CancelRequested, p.CancelState = true, "prepared"
		p.Error = "手法の個別新規取消を要求中です: " + now.Format(time.RFC3339)
		if err := m.save(); err != nil {
			return err
		}
		_, err := api.CancelFuture(ctx, p.BrokerOrderID)
		if err != nil {
			p.CancelState = "unknown"
			p.Error = "個別新規取消の結果不明。再送せず終了・約定を照合します: " + err.Error()
			var apiErr *controller.APIError
			if errors.As(err, &apiErr) && !apiErr.Unknown {
				p.CancelState = "rejected"
				p.Error = "個別新規取消が拒否されました。注文・約定の照合を続けます: " + err.Error()
			}
			if saveErr := m.save(); saveErr != nil {
				return saveErr
			}
			return &SelectedCancellationPending{Cause: err}
		}
		p.CancelState = "accepted"
		p.Error = "個別新規取消受付済み。上流終了と競合約定の照合待ちです"
		if err := m.save(); err != nil {
			return err
		}
	}
	return nil
}
