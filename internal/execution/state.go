package execution

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// OrderState は約定ゼロの終了も判別できる注文の状態です。
type OrderState struct {
	IntentID      string    `json:"intent_id"`
	Owner         string    `json:"owner"`
	Product       string    `json:"product"`
	Session       string    `json:"session"`
	TradeType     string    `json:"trade_type"`
	Side          string    `json:"side"`
	Requested     int       `json:"requested"`
	Filled        int       `json:"filled"`
	Status        string    `json:"status"`
	Done          bool      `json:"done"`
	BrokerOrderID string    `json:"broker_order_id"`
	Error         string    `json:"error"`
	At            time.Time `json:"at"`
}

// Orders は全担当の受付・部分約定・終了状態のコピーを返します。
//
// 引数: なし。
// 返り値: []OrderState は数量ゼロ終了や結果不明を含む注文状態です。
func (m *Manager) Orders() []OrderState {
	result := make([]OrderState, 0, len(m.state.Orders))
	for _, p := range m.state.Orders {
		filled := 0
		for _, t := range m.trades {
			if t.ClientOrderID == p.Intent.ID {
				filled += t.Quantity
			}
		}
		result = append(result, OrderState{IntentID: p.Intent.ID, Owner: p.Intent.Owner,
			Product: p.Intent.Product, Session: p.Intent.Session, TradeType: p.Intent.TradeType,
			Side: p.Intent.Side, Requested: p.Intent.Quantity, Filled: filled, Status: p.Status,
			Done: p.Status == "done" || p.Status == "rejected", BrokerOrderID: p.BrokerOrderID,
			Error: p.Error, At: p.Intent.At})
	}
	return result
}

// Quantity は担当と保有方向を限定した実約定済み数量を返します。
//
// 引数: owner string は core / extra / addon / episode、side string は buy または sell です。
// 返り値: int は残建玉枚数です。所有不明の実口座玉は含めません。
func (m *Manager) Quantity(owner, side string) int {
	quantity := 0
	for _, h := range m.state.Holdings {
		if h.Owner == owner && h.Side == side {
			quantity += h.Quantity
		}
	}
	return quantity
}

// Close は台帳を閉じます。取消や返済の発注は行いません。
//
// 引数: なし。
// 返り値: error は保存器の終了エラーです。
func (m *Manager) Close() error { return m.store.Close() }

// RecordBook は生の観測を受信時刻・世代とともに台帳へ保存します。
//
// 引数: session/generation/code string は所属、received time.Time は自端末受信時刻、raw json.RawMessage は生本文です。
// 返り値: int64 は保存識別子、error は保存エラーです。
func (m *Manager) RecordBook(session, generation, code string, received time.Time, raw json.RawMessage) (int64, error) {
	return m.store.RecordBook(session, generation, code, received, raw)
}

// Event は判断や照合の原情報を台帳へ保存します。
//
// 引数: kind/session/code/correlation string は分類、at time.Time は観測時刻、payload any は根拠です。
// 返り値: error は保存エラーです。
func (m *Manager) Event(kind, session, code, correlation string, at time.Time, payload any) error {
	return m.store.Event(kind, session, code, correlation, at, payload)
}

// VerifyOwned は注文未確定がないとき、自分の建玉 ID と口座実残の一致を検査します。
//
// 引数: view controller.OrdersView は呼出元が取得時刻の鮮度を確認した口座照会です。
// 返り値: error は所有記録との不一致です。状態は修正せず、継続時間の判定は呼出元が行います。
// 仮想実行・未確定注文・取得失敗・切断中は検査を行いません。
func (m *Manager) VerifyOwned(view controller.OrdersView) error {
	if m.state.DryRun || m.HasPending() || !view.Connected || view.BrokerStale || view.PositionsStale ||
		view.BrokerError != "" || view.PositionsError != "" {
		return nil
	}
	for _, owned := range m.Positions() {
		observed := 0
		found := false
		for _, position := range view.Positions {
			if position.ExecutionID != owned.HoldID {
				continue
			}
			if found {
				return fmt.Errorf("所有建玉 %s が口座照会で重複しています", owned.HoldID)
			}
			found = true
			if position.Symbol != owned.ContractSymbol || position.Side != owned.Side {
				return fmt.Errorf("所有建玉 %s の銘柄または方向が口座と異なります", owned.HoldID)
			}
			if math.IsNaN(position.Quantity) || math.IsInf(position.Quantity, 0) ||
				position.Quantity < 0 || math.Trunc(position.Quantity) != position.Quantity {
				return fmt.Errorf("所有建玉 %s の口座数量が不正です", owned.HoldID)
			}
			if position.Quantity != float64(owned.Quantity) {
				return fmt.Errorf("所有建玉 %s の残数不一致: 台帳%d枚・口座%.0f枚", owned.HoldID, owned.Quantity, position.Quantity)
			}
			observed = int(position.Quantity)
		}
		if observed != owned.Quantity {
			return fmt.Errorf("所有建玉 %s の残数不一致: 台帳%d枚・口座%d枚", owned.HoldID, owned.Quantity, observed)
		}
	}
	return nil
}
