package execution

import (
	"context"
	"fmt"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// CancelAllOwnedPending は管理終了時に、自分の未完了新規・返済・予約だけを一度取り消します。
//
// 引数: ctx(context.Context)は通信期限、api(NewRunAPI)は取消機能、view(controller.OrdersView)は新鮮な口座応答、now(time.Time)は記録時刻です。
// 返り値: errorは所有不明・取消結果不明・保存失敗です。成功は取消受付までで、終了は後続Reconcileで確かめます。
// 保有建玉を返済せず、確定約定・所有IDを消しません。新規起動と画面OFFが同じ照合手順を使います。
func (m *Manager) CancelAllOwnedPending(ctx context.Context, api NewRunAPI, view controller.OrdersView, now time.Time) error {
	if !m.HasPending() {
		return nil
	}
	if m.state.DryRun {
		for _, p := range m.state.Orders {
			if p.Status != "done" && p.Status != "rejected" {
				p.Status, p.CancelRequested, p.CancelState = "done", true, "accepted"
				p.Error = "管理終了により未実行の仮想注文を取り消しました。約定には計上しません"
			}
		}
		return m.save()
	}
	if api == nil {
		return fmt.Errorf("所有注文を取り消す通信機能がありません")
	}
	if !view.Connected || view.BrokerStale || view.PositionsStale || view.BrokerError != "" || view.PositionsError != "" {
		return fmt.Errorf("所有注文の取消には新鮮な注文・建玉照会が必要です")
	}
	for _, p := range m.state.Orders {
		if p.Status == "done" || p.Status == "rejected" {
			continue
		}
		reservationID := ""
		if !p.Raw && p.InstanceID == view.InstanceID {
			for _, reservation := range view.Reservations {
				if reservation.ClientOrderID == p.Intent.ID && reservation.Symbol == p.Intent.ContractSymbol && !reservation.Finished {
					reservationID = reservation.ID
					break
				}
			}
		}
		if reservationID == "" && p.BrokerOrderID == "" {
			p.Error = fmt.Sprintf("所有注文 %s は送信結果不明で注文番号を確認できません。再送せず台帳と口座の照合を待ちます", p.Intent.ID)
			if err := m.save(); err != nil {
				return err
			}
			return fmt.Errorf("%s", p.Error)
		}
		if p.CancelRequested {
			continue
		}
		if reservationID == "" {
			matched := false
			for _, broker := range view.BrokerOrders {
				if broker.ID != p.BrokerOrderID {
					continue
				}
				// 終了済みで約定通知が未反映の場合、重ねて取消せずReconcileを待ちます。
				if broker.Finished || broker.State == 5 {
					break
				}
				matched = broker.Symbol == p.Intent.ContractSymbol && broker.Side == map[string]string{"buy": "2", "sell": "1"}[p.Intent.Side]
				break
			}
			if !matched {
				continue
			}
		}
		p.CancelRequested, p.CancelState = true, "prepared"
		p.Error = "管理終了に伴う所有注文の取消・終了照合中です: " + now.Format(time.RFC3339)
		if err := m.save(); err != nil {
			return err
		}
		var err error
		if reservationID != "" {
			err = api.CancelReservation(ctx, reservationID, view.InstanceID)
		} else {
			_, err = api.CancelFuture(ctx, p.BrokerOrderID)
		}
		if err != nil {
			p.CancelState = "unknown"
			p.Error = "所有注文の取消結果不明。再送せず、口座での終了確認が必要です: " + err.Error()
			if saveErr := m.save(); saveErr != nil {
				return saveErr
			}
			return fmt.Errorf("%s", p.Error)
		}
		p.CancelState = "accepted"
		if err := m.save(); err != nil {
			return err
		}
	}
	return nil
}
