package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
)

// NewRunAPI は前回の所有注文だけを取り消し、終了を照合する機能です。
type NewRunAPI interface {
	CancelAPI
	Orders(context.Context, string) (controller.OrdersView, error)
	CancelReservation(context.Context, string, string) error
}

// OpenNewRun は旧状態を監査保存し、毎起動を空の取引状態から始めます。
//
// 引数: ctx(context.Context)は旧注文終了の待機期限、api(NewRunAPI)は所有注文の取消・照会です。
// logDir/statePath(string)は保存先、dryRun(bool)、symbol(string)、multiplier(float64)、maxPosition(int)は新設定です。
// namespace(string)は新注文の識別範囲、runtime(json.RawMessage)は未保有の新戦略状態です。
// 返り値: *Managerは当起動の所有玉だけを管理する台帳、errorは保存・未確定注文の照合失敗です。
// 旧建玉は返済せず、旧約定・旧状態を消しません。仮想の旧preparedを約定し直すこともありません。
func OpenNewRun(ctx context.Context, api NewRunAPI, logDir, statePath string, dryRun bool, symbol string, multiplier float64, maxPosition int, namespace string, runtime json.RawMessage) (*Manager, error) {
	if symbol == "" || !positive(multiplier) || maxPosition < 1 || namespace == "" || !json.Valid(runtime) {
		return nil, fmt.Errorf("新規運転の設定・識別子・戦略状態が不正です")
	}
	s, err := journal.Open(statePath, logDir)
	if err != nil {
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			_ = s.Close()
		}
	}()
	m := &Manager{store: s}
	var previous json.RawMessage
	m.trades, err = s.Load(&previous)
	if err != nil {
		return nil, err
	}
	if len(previous) > 0 && string(previous) != "null" {
		if err = json.Unmarshal(previous, &m.state); err != nil {
			return nil, fmt.Errorf("旧注文台帳の読込: %w", err)
		}
		if m.state.Symbol != "" && (m.state.Symbol != symbol || m.state.DryRun != dryRun) {
			return nil, fmt.Errorf("旧台帳の商品または実注文・仮想の区別が異なります。同じ商品・モードの保存先を使用してください")
		}
	}
	if m.state.Holdings == nil {
		m.state.Holdings = map[string]*Holding{}
	}
	for _, p := range m.state.Orders {
		if p == nil || p.Intent.ID == "" || !validOwner(p.Intent.Owner) {
			return nil, fmt.Errorf("旧注文の所有IDまたは担当を確認できません")
		}
		if !dryRun && p.Status != "done" && p.Status != "rejected" && (p.Seen == nil || p.CloseConsumed == nil || p.Intent.Quantity < 1 || !positive(p.Intent.Price) || (p.Intent.Side != "buy" && p.Intent.Side != "sell")) {
			return nil, fmt.Errorf("旧未確定注文の数量・方向・照合記録が不正です")
		}
	}
	// ----------------------------------------
	// 実口座の旧注文だけ先に停止する。受付を完了とは扱わず、約定競合も記録する。
	if !dryRun && m.HasPending() {
		if m.state.AckCount < 0 || m.state.AckCount > len(m.trades) {
			return nil, fmt.Errorf("旧未確定注文の約定反映位置が不正です")
		}
		if api == nil {
			return nil, fmt.Errorf("旧注文を確認する通信機能がありません")
		}
		if err = m.finishPreviousOrders(ctx, api); err != nil {
			return nil, err
		}
	}
	previousHoldings := m.Positions()
	oldQuantity := 0
	for _, h := range previousHoldings {
		oldQuantity += h.Quantity
	}
	var oldRuntime, newRuntime map[string]json.RawMessage
	if len(m.state.RuntimeState) > 0 && string(m.state.RuntimeState) != "null" {
		_ = json.Unmarshal(m.state.RuntimeState, &oldRuntime)
	}
	if err = json.Unmarshal(runtime, &newRuntime); err != nil || newRuntime == nil {
		return nil, fmt.Errorf("新戦略状態はJSONオブジェクトで指定してください")
	}
	if legacy := oldRuntime["legacy_data_directory"]; len(legacy) > 0 {
		newRuntime["legacy_data_directory"] = legacy
	}
	runtime, err = json.Marshal(newRuntime)
	if err != nil {
		return nil, err
	}
	state := savedState{Symbol: symbol, DryRun: dryRun, Multiplier: multiplier, MaxPosition: maxPosition,
		Holdings: map[string]*Holding{}, RuntimeState: runtime, AckCount: len(m.trades)}
	metadata := map[string]any{"new_namespace": namespace, "dryrun": dryRun, "product": symbol,
		"previous_max_position": m.state.MaxPosition, "new_max_position": maxPosition,
		"previous_position_count": oldQuantity, "previous_holdings": previousHoldings,
		"previous_runtime": m.state.RuntimeState, "confirmed_trades_kept": len(m.trades),
		"reason": "毎起動を新規扱い。旧仮想玉は引継がず、旧実建玉は手動管理。新規上限は当起動の所有玉だけ。"}
	if err = s.ArchiveAndSave(state, m.trades, metadata); err != nil {
		return nil, err
	}
	m.state = state
	opened = true
	return m, nil
}

// finishPreviousOrders は所有を記録済みの旧注文だけを取消し、全件の終了確認まで待ちます。
//
// 引数: ctx(context.Context)は待機期限、api(NewRunAPI)は照会・取消です。
// 返り値: errorは期限・所有不一致・結果不明です。建玉返済や新規注文を送信しません。
func (m *Manager) finishPreviousOrders(ctx context.Context, api NewRunAPI) error {
	for {
		view, err := api.Orders(ctx, "all")
		if err != nil {
			return fmt.Errorf("前回注文の確認に失敗しました。新規運転を開始しません: %w", err)
		}
		if !view.Connected || view.BrokerStale || view.PositionsStale || view.BrokerError != "" || view.PositionsError != "" {
			return fmt.Errorf("前回注文の取消・終了確認には新鮮な注文・建玉照会が必要です")
		}
		if _, err = m.Reconcile(view); err != nil {
			return fmt.Errorf("前回注文の約定照合に失敗しました。新規運転を開始しません: %w", err)
		}
		if !m.HasPending() {
			return nil
		}
		if err = m.CancelAllOwnedPending(ctx, api, view, time.Now()); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("前回注文の終了確認が完了しませんでした。新規運転を開始せず旧状態を保持します: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}
