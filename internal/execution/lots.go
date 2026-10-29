package execution

import (
	"fmt"
	"math"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
)

// validOwner は旧担当名と研究用の独立担当名を確認します。
//
// 引数: owner(string)は注文・保存台帳に記載された担当です。
// 返り値: boolは既知担当ならtrueです。文字列の曖昧な部分一致は行いません。
func validOwner(owner string) bool {
	return owner == "core" || owner == "extra" || owner == "addon" || owner == "episode"
}

// standardOrderFee は一注文の累計枚数に対する立会手数料を整数円へ切り捨てます。
//
// 引数: product(string)は発注商品、quantity(int)は累計約定枚数です。
// 返り値: float64は標準手数料円、errorは未知商品・不正数量です。実請求額の照会値ではありません。
func standardOrderFee(product string, quantity int) (float64, error) {
	rate := int64(0)
	switch product {
	case "NK225":
		rate = 2750
	case "NK225mini":
		rate = 385
	case "NK225micro":
		rate = 110
	case "TOPIX":
		rate = 3300
	case "TOPIXmini":
		rate = 418
	default:
		return 0, fmt.Errorf("標準手数料が未定義の商品です: %s", product)
	}
	if quantity < 0 || int64(quantity) > (1<<63-1)/rate {
		return 0, fmt.Errorf("手数料計算の累計枚数が不正です")
	}
	return float64(int64(quantity) * rate / 10), nil
}

// ownedCloseHolding は返済IDが同じ担当・商品・限月・逆側の所有建玉か確認します。
//
// 引数: intent(Intent)は返済意図、holdID(string)は返済指定IDです。
// 返り値: *Holdingは所有建玉、errorは識別不一致です。旧Product省略は台帳商品を引き継ぎます。
func (m *Manager) ownedCloseHolding(intent Intent, holdID string) (*Holding, error) {
	h := m.state.Holdings[holdID]
	product := intent.Product
	if product == "" {
		product = m.state.Symbol
	}
	if h == nil || holdID == "" || h.HoldID != holdID || h.Owner != intent.Owner ||
		h.ContractSymbol != intent.ContractSymbol || product != m.state.Symbol ||
		(h.Product != "" && h.Product != product) ||
		!((intent.Side == "buy" && h.Side == "sell") || (intent.Side == "sell" && h.Side == "buy")) {
		return nil, fmt.Errorf("指定した返済建玉の所有・商品・方向が一致しません: %s", holdID)
	}
	return h, nil
}

// explicitCloseTargets は任意指定の返済IDを並べ替えず、全件の返済可能数と予約を確認します。
//
// 引数: intent(Intent)は指定IDを含む返済意図、view(controller.OrdersView)は新鮮な口座照会です。
// 返り値: []controller.ClosePositionは独立した指定順の配列、errorは不一致です。不足分を別の玉で代替しません。
func (m *Manager) explicitCloseTargets(intent Intent, view controller.OrdersView) ([]controller.ClosePosition, error) {
	seen := map[string]bool{}
	remaining := intent.Quantity
	for _, target := range intent.ClosePositions {
		if target.Quantity < 1 || target.Quantity > remaining || seen[target.HoldID] {
			return nil, fmt.Errorf("返済指定の重複・数量・合計が不正です: %s", target.HoldID)
		}
		seen[target.HoldID] = true
		h, err := m.ownedCloseHolding(intent, target.HoldID)
		if err != nil {
			return nil, err
		}
		if target.Quantity > h.Quantity {
			return nil, fmt.Errorf("返済指定が所有残数を超えています: %s", target.HoldID)
		}
		for _, order := range m.state.Orders {
			if order.Status == "done" || order.Status == "rejected" {
				continue
			}
			for _, reserved := range order.Request.ClosePositions {
				if reserved.HoldID == target.HoldID && reserved.Quantity > order.CloseConsumed[target.HoldID] {
					return nil, fmt.Errorf("指定建玉に未確定の返済注文があります: %s", target.HoldID)
				}
			}
		}
		if !m.state.DryRun {
			found := false
			for _, position := range view.Positions {
				if position.ExecutionID != target.HoldID {
					continue
				}
				if found || position.Symbol != h.ContractSymbol || position.Side != h.Side ||
					!nonnegativeQuantity(position.Quantity) || !nonnegativeQuantity(position.HeldQuantity) ||
					!nonnegativeQuantity(position.AvailableQuantity) || position.Quantity != float64(h.Quantity) ||
					position.HeldQuantity > position.Quantity || position.AvailableQuantity > position.Quantity-position.HeldQuantity ||
					position.AvailableQuantity < float64(target.Quantity) {
					return nil, fmt.Errorf("指定建玉の口座残数・返済可能数が不正または不足しています: %s", target.HoldID)
				}
				found = true
			}
			if !found {
				return nil, fmt.Errorf("指定建玉を現在の口座で確認できません: %s", target.HoldID)
			}
			for _, reservation := range view.Reservations {
				if reservation.Finished {
					continue
				}
				for _, leg := range reservation.Legs {
					for _, reserved := range leg.ClosePositions {
						if reserved.HoldID == target.HoldID && reserved.Quantity > 0 {
							return nil, fmt.Errorf("指定建玉は別の未終了予約に含まれています: %s", target.HoldID)
						}
					}
				}
			}
		}
		remaining -= target.Quantity
	}
	if remaining != 0 {
		return nil, fmt.Errorf("指定した返済建玉の合計が注文枚数と一致しません")
	}
	return append([]controller.ClosePosition(nil), intent.ClosePositions...), nil
}

// nonnegativeQuantity は照会数量が有限の非負整数か確認します。
//
// 引数: value(float64)は口座応答の数量です。
// 返り値: boolは有効な数量ならtrueです。
func nonnegativeQuantity(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && math.Trunc(value) == value
}

// fillCloseTargets は所有残数を変更する前に、今回の全返済内訳を検証します。
//
// 引数: p(*pending)は元注文、qty(int)は今回数量、confirmed(...[]controller.ClosePosition)は口座で確定した減少内訳です。
// 返り値: []controller.ClosePositionは今回実際に減らす玉、errorは指定外・数量・所有の不一致です。
func (m *Manager) fillCloseTargets(p *pending, qty int, confirmed ...[]controller.ClosePosition) ([]controller.ClosePosition, error) {
	allowed := map[string]int{}
	for _, target := range p.Request.ClosePositions {
		if target.HoldID == "" || target.Quantity < 1 || allowed[target.HoldID] != 0 {
			return nil, fmt.Errorf("保存された返済指定に空ID・重複・不正数量があります")
		}
		allowed[target.HoldID] = target.Quantity
	}
	targets := p.Request.ClosePositions
	actual := len(confirmed) > 0 && confirmed[0] != nil
	if actual {
		targets = confirmed[0]
	}
	remaining := qty
	closed := []controller.ClosePosition{}
	seen := map[string]bool{}
	for _, target := range targets {
		if seen[target.HoldID] || target.Quantity < 1 || allowed[target.HoldID] < 1 || p.CloseConsumed[target.HoldID] < 0 {
			return nil, fmt.Errorf("返済約定内訳が注文の指定外・重複・不正数量です")
		}
		seen[target.HoldID] = true
		available := allowed[target.HoldID] - p.CloseConsumed[target.HoldID]
		quantity := target.Quantity
		if actual {
			if quantity > available || quantity > remaining {
				return nil, fmt.Errorf("確認済み返済内訳が指定残数・今回約定枚数を超えています")
			}
		} else {
			quantity = min(available, remaining)
		}
		if quantity < 0 {
			return nil, fmt.Errorf("保存された返済済み枚数が指定枚数を超えています")
		}
		if quantity == 0 {
			continue
		}
		h, err := m.ownedCloseHolding(p.Intent, target.HoldID)
		if err != nil {
			return nil, err
		}
		if h.Quantity < quantity {
			return nil, fmt.Errorf("返済約定が所有残数を超えています: %s", target.HoldID)
		}
		closed = append(closed, controller.ClosePosition{HoldID: target.HoldID, Quantity: quantity})
		remaining -= quantity
	}
	if remaining != 0 {
		return nil, fmt.Errorf("返済約定内訳の合計が確認済み数量と一致しません")
	}
	return closed, nil
}
