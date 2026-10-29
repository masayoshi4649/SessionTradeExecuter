// Package execution は、発注意図・約定・所有建玉を別々に管理します。
package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
)

// API は、発注と照合に必要なcontrollerの機能です。
type API interface {
	SendFuture(context.Context, controller.BrokerRequest) (controller.OrderResponse, error)
}

// CancelAPI は、既知の上流注文番号だけを取消する機能です。
type CancelAPI interface {
	CancelFuture(context.Context, string) (controller.OrderResponse, error)
}

// Intent は、条件成立後の一度限りの発注意思です。
type Intent struct {
	Owner          string                     `json:"owner"`
	Product        string                     `json:"product"`
	MethodID       string                     `json:"method_id,omitempty"`
	Evidence       json.RawMessage            `json:"evidence,omitempty"`
	ID             string                     `json:"id"`
	Session        string                     `json:"session"`
	ContractSymbol string                     `json:"contract_symbol"`
	Side           string                     `json:"side"`
	TradeType      string                     `json:"trade_type"`
	Reason         string                     `json:"reason"`
	Quantity       int                        `json:"quantity"`
	Price          float64                    `json:"price"`
	At             time.Time                  `json:"at"`
	ExpiresAt      time.Time                  `json:"expires_at"`
	ClosePositions []controller.ClosePosition `json:"close_positions,omitempty"`
}

// Holding は、当プログラムの新規約定から証明できる建玉だけを保持します。
type Holding struct {
	Owner          string          `json:"owner"`
	Product        string          `json:"product"`
	Evidence       json.RawMessage `json:"evidence,omitempty"`
	HoldID         string          `json:"hold_id"`
	ContractSymbol string          `json:"contract_symbol"`
	Side           string          `json:"side"`
	Quantity       int             `json:"quantity"`
	Price          float64         `json:"price"`
	OpenedAt       time.Time       `json:"opened_at"`
	Session        string          `json:"session"`
	EntryIntentID  string          `json:"entry_intent_id,omitempty"`
}

// Fill は、戦略へ確認済み数量と単価を通知する約定です。
type Fill struct {
	Owner       string
	Product     string
	Session     string
	Evidence    json.RawMessage
	ID          string
	IntentID    string
	TradeType   string
	Side        string
	Quantity    int
	Price       float64
	PnL         float64
	Fee         float64
	HoldID      string
	Closed      []controller.ClosePosition
	ClosedBasis string
	At          time.Time
	Reason      string
}

// pending は、発注前に永続化する本文と照合済み数量です。
type pending struct {
	Intent          Intent                   `json:"intent"`
	InstanceID      string                   `json:"instance_id"`
	ReservationID   string                   `json:"reservation_id"`
	BrokerOrderID   string                   `json:"broker_order_id"`
	Request         controller.Request       `json:"request"`
	RawRequest      controller.BrokerRequest `json:"raw_request"`
	Raw             bool                     `json:"raw"`
	Status          string                   `json:"status"`
	Error           string                   `json:"error"`
	Seen            map[string]int           `json:"seen"`
	CloseConsumed   map[string]int           `json:"close_consumed"`
	CancelRequested bool                     `json:"cancel_requested"`
	CancelState     string                   `json:"cancel_state"`
}

// savedState は、注文・所有建玉・戦略の復旧を一つの正本にまとめます。
type savedState struct {
	Symbol       string              `json:"symbol"`
	DryRun       bool                `json:"dryrun"`
	Multiplier   float64             `json:"multiplier"`
	MaxPosition  int                 `json:"max_position"`
	Orders       []*pending          `json:"orders"`
	Holdings     map[string]*Holding `json:"holdings"`
	RuntimeState json.RawMessage     `json:"runtime_state"`
	AckCount     int                 `json:"ack_count"`
}

// Manager は、単一実行ループで呼ぶ注文台帳です。
type Manager struct {
	store  *journal.Store
	state  savedState
	trades []journal.Trade
}

// Open は、既存の注文と戦略状態を復元します。
//
// 引数: logDir・statePath string は保存先、dryRun bool は仮想指定、symbol string は商品コード、multiplier float64 は円換算倍率、maxPosition int は片方向上限です。
// 返り値: *Manager は台帳、error は復元・設定不一致です。
func Open(logDir, statePath string, dryRun bool, symbol string, multiplier float64, maxPosition int) (*Manager, error) {
	if symbol == "" || !positive(multiplier) || maxPosition < 1 {
		return nil, fmt.Errorf("注文管理の設定が不正です")
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
	m.trades, err = s.Load(&m.state)
	if err != nil {
		return nil, err
	}
	if m.state.Symbol != "" && (m.state.Symbol != symbol || m.state.DryRun != dryRun || m.state.Multiplier != multiplier || m.state.MaxPosition != maxPosition) {
		differences := []string{}
		if m.state.Symbol != symbol {
			differences = append(differences, fmt.Sprintf("market.product: 保存=%q / 現在=%q", m.state.Symbol, symbol))
		}
		if m.state.DryRun != dryRun {
			differences = append(differences, fmt.Sprintf("trading.dryrun: 保存=%t / 現在=%t", m.state.DryRun, dryRun))
		}
		if m.state.Multiplier != multiplier {
			differences = append(differences, fmt.Sprintf("円換算倍率: 保存=%g / 現在=%g", m.state.Multiplier, multiplier))
		}
		if m.state.MaxPosition != maxPosition {
			differences = append(differences, fmt.Sprintf("market.max_per_side: 保存=%d枚 / 現在=%d枚", m.state.MaxPosition, maxPosition))
		}
		return nil, fmt.Errorf("保存状態と現在の設定が異なるため、復元を中止しました。\n対象DB: %s\n%s\n再開する場合は、conf/*.toml をこのDBを作成した元の設定（上記の保存値）へ戻してください。円換算倍率が異なる場合は商品仕様も確認してください。保存済みの建玉・注文は変更していません。DBを削除して回避しないでください。", statePath, strings.Join(differences, "\n"))
	}
	m.state.Symbol = symbol
	m.state.DryRun = dryRun
	m.state.Multiplier = multiplier
	m.state.MaxPosition = maxPosition
	if m.state.Holdings == nil {
		m.state.Holdings = make(map[string]*Holding)
	}
	if m.state.AckCount < 0 || m.state.AckCount > len(m.trades) {
		return nil, fmt.Errorf("約定反映位置が不正です")
	}
	for _, p := range m.state.Orders {
		if !validOwner(p.Intent.Owner) {
			return nil, fmt.Errorf("保存注文の担当が不明です。旧アプリの台帳は流用できません")
		}
		if p.Status != "prepared" {
			continue
		}
		if dryRun {
			if err = m.applyFill(p, "dry-"+p.Intent.ID, p.Intent.Quantity, p.Intent.Price, p.Intent.At); err != nil {
				return nil, err
			}
			p.Status = "done"
		} else {
			p.Status = "unknown"
			p.Error = "発注意図の保存後に中断しました。送信済みの可能性があるため自動再送しません"
		}
		if err = m.save(); err != nil {
			return nil, err
		}
	}
	opened = true
	return m, nil
}

// State は、最後に約定反映と同時保存した戦略状態を返します。
//
// 引数: なし。
// 返り値: json.RawMessage は戦略・特徴量の復元データです。
func (m *Manager) State() json.RawMessage {
	return append(json.RawMessage(nil), m.state.RuntimeState...)
}

// Checkpoint は、通知済み約定を反映した戦略状態を原子的に確定します。
//
// 引数: state json.RawMessage はOutstandingFills全件を反映した戦略状態です。
// 返り値: error は保存失敗です。
func (m *Manager) Checkpoint(state json.RawMessage) error {
	if len(state) > 0 && !json.Valid(state) {
		return fmt.Errorf("戦略状態がJSONではありません")
	}
	m.state.RuntimeState = append(json.RawMessage(nil), state...)
	m.state.AckCount = len(m.trades)
	return m.save()
}

// OutstandingFills は、まだ戦略状態に確定反映されていない約定を返します。
//
// 引数: なし。
// 返り値: []Fill は再起動後も同じIDで通知する約定です。
func (m *Manager) OutstandingFills() []Fill {
	var out []Fill
	for _, t := range m.trades[m.state.AckCount:] {
		holdID := t.HoldID
		if holdID == "" && t.TradeType == "new" {
			holdID = t.ExecutionID
		}
		out = append(out, Fill{Owner: t.Owner, Product: t.Product, Session: t.Session, Evidence: append(json.RawMessage(nil), t.Evidence...), ID: t.ID, IntentID: t.ClientOrderID, TradeType: t.TradeType, Side: t.Side, Quantity: t.Quantity, Price: t.Price, PnL: t.PnL, Fee: t.Fee, HoldID: holdID, Closed: append([]controller.ClosePosition(nil), t.Closed...), ClosedBasis: t.ClosedBasis, At: t.At, Reason: t.Reason})
	}
	return out
}

// HasPending は、結果未確定の発注意図が残っているか返します。
//
// 引数: なし。
// 返り値: bool は重複した注文判断を止める必要がある場合に真です。
func (m *Manager) HasPending() bool {
	for _, p := range m.state.Orders {
		if p.Status != "done" && p.Status != "rejected" {
			return true
		}
	}
	return false
}

// PendingError は、未確定注文の説明を返します。
//
// 引数: なし。
// 返り値: string は停止・照合待ちの理由です。
func (m *Manager) PendingError() string {
	for _, p := range m.state.Orders {
		if p.Status != "done" && p.Status != "rejected" && p.Error != "" {
			return p.Error
		}
	}
	return ""
}

// Positions は、所有権を確認できた建玉のコピーを返します。
//
// 引数: なし。
// 返り値: []Holding は残数量が正の所有建玉です。
func (m *Manager) Positions() []Holding {
	out := []Holding{}
	for _, h := range m.state.Holdings {
		if h.Quantity > 0 {
			value := *h
			value.Evidence = append(json.RawMessage(nil), h.Evidence...)
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HoldID < out[j].HoldID })
	return out
}

// Records は、確定約定記録のコピーを返します。
//
// 引数: なし。
// 返り値: []journal.Trade はこれまでの実約定または仮想約定です。
func (m *Manager) Records() []journal.Trade {
	result := append([]journal.Trade(nil), m.trades...)
	for i := range result {
		result[i].Evidence = append(json.RawMessage(nil), result[i].Evidence...)
		result[i].Closed = append([]controller.ClosePosition(nil), result[i].Closed...)
	}
	return result
}

// EnsureSession は、商品とモードを含む運転セッションをSQLiteへ登録します。
//
// 引数: session string はAPI由来のセッション名です。
// 返り値: error は保存失敗です。この時点ではCSVを作りません。
func (m *Manager) EnsureSession(session string) error {
	return m.store.EnsureProductSession(session, m.state.DryRun, m.state.Symbol)
}

// CancelPending は、期限到達または停止時の未完了注文へ取消を一度要求します。
//
// 引数: ctx context.Context は通信期限、api CancelAPI は取消機能、now time.Time は現在、force bool は新規だけ期限前も対象にする指定です。
// 返り値: error は取消不能・受付不明・保存失敗です。成功でも返済済みや取消完了とは扱いません。
func (m *Manager) CancelPending(ctx context.Context, api CancelAPI, now time.Time, force bool) error {
	if m.state.DryRun {
		return nil
	}
	if api == nil {
		return fmt.Errorf("取消の通信機能がありません")
	}
	for _, p := range m.state.Orders {
		if p.Status == "done" || p.Status == "rejected" || p.CancelRequested {
			continue
		}
		forceEntry := force && p.Intent.TradeType == "new"
		if !forceEntry && (p.Intent.ExpiresAt.IsZero() || now.Before(p.Intent.ExpiresAt)) {
			continue
		}
		if p.BrokerOrderID == "" {
			p.Error = "発注結果不明で上流注文番号がないため取消できません。口座で確認が必要です"
			if err := m.save(); err != nil {
				return err
			}
			return fmt.Errorf("%s", p.Error)
		}
		p.CancelRequested = true
		p.CancelState = "prepared"
		if err := m.save(); err != nil {
			return err
		}
		_, err := api.CancelFuture(ctx, p.BrokerOrderID)
		if err != nil {
			p.CancelState = "unknown"
			p.Error = "取消結果不明。再送せず照合を継続します: " + err.Error()
			var apiErr *controller.APIError
			if errors.As(err, &apiErr) && !apiErr.Unknown {
				p.CancelState = "rejected"
				p.Error = "取消要求が拒否されました: " + err.Error()
			}
			if saveErr := m.save(); saveErr != nil {
				return saveErr
			}
			return err
		}
		p.CancelState = "accepted"
		p.Error = "取消受付済み。上流終了と約定の照合待ちです"
		if err = m.save(); err != nil {
			return err
		}
	}
	return nil
}

// Submit は、意図をディスクへ確定した後、一度だけ送信します。
//
// 引数: ctx context.Context は通信期限、api API はcontroller、intent Intent は判断、view controller.OrdersView は新鮮な照会です。
// 返り値: []Fill は未反映約定、error は拒否・保存・通信失敗です。通信失敗で新しいIDを作って再送してはいけません。
func (m *Manager) Submit(ctx context.Context, api API, intent Intent, view controller.OrdersView) ([]Fill, error) {
	if !validOwner(intent.Owner) {
		return nil, fmt.Errorf("注文の担当は core / extra / addon / episode が必要です")
	}
	if intent.Product == "" {
		intent.Product = m.state.Symbol
	}
	if intent.Product != m.state.Symbol {
		return nil, fmt.Errorf("注文の商品と台帳の商品が異なります")
	}
	if _, err := standardOrderFee(intent.Product, 0); err != nil {
		return nil, err
	}
	if len(intent.Evidence) > 0 && !json.Valid(intent.Evidence) {
		return nil, fmt.Errorf("判断根拠が JSON ではありません")
	}
	intent.Evidence = append(json.RawMessage(nil), intent.Evidence...)
	intent.ClosePositions = append([]controller.ClosePosition(nil), intent.ClosePositions...)
	for _, p := range m.state.Orders {
		if intent.ID != "" && p.Intent.ID == intent.ID {
			old, _ := json.Marshal(p.Intent)
			current, _ := json.Marshal(intent)
			if string(old) != string(current) {
				return nil, fmt.Errorf("同じ注文 ID の内容または担当が変更されています")
			}
			return m.OutstandingFills(), nil
		}
	}
	if m.HasPending() || len(m.OutstandingFills()) > 0 {
		return nil, fmt.Errorf("未確定注文または未反映約定があります")
	}
	if intent.ID == "" {
		id, err := newID()
		if err != nil {
			return nil, err
		}
		intent.ID = "session-" + id
	}
	if intent.Side != "buy" && intent.Side != "sell" {
		return nil, fmt.Errorf("売買方向が不正です")
	}
	if intent.TradeType != "new" && intent.TradeType != "close" {
		return nil, fmt.Errorf("取引区分が不正です")
	}
	if intent.Quantity < 1 || !positive(intent.Price) || intent.ContractSymbol == "" || intent.At.IsZero() || intent.Session == "" {
		return nil, fmt.Errorf("発注意図の必須項目が不正です")
	}
	if intent.TradeType == "new" {
		if len(intent.ClosePositions) != 0 {
			return nil, fmt.Errorf("新規注文に返済建玉を指定できません")
		}
		qty := 0
		// 上限は当起動で取得した所有玉だけ。旧建玉・手動注文・別アプリを取り込まない。
		// 未確定の自分の注文は上のHasPendingで新規を止めるため、予約を二重に作らない。
		for _, h := range m.state.Holdings {
			if h.Side == intent.Side {
				qty += h.Quantity
			}
		}
		if qty+intent.Quantity > m.state.MaxPosition {
			return nil, fmt.Errorf("当起動の所有建玉の片方向上限%d枚を超えます", m.state.MaxPosition)
		}
	}
	p := &pending{Intent: intent, InstanceID: view.InstanceID, Status: "prepared", Seen: map[string]int{}, CloseConsumed: map[string]int{}}
	p.Request = controller.Request{InstanceID: view.InstanceID, ClientOrderID: intent.ID, Symbol: intent.ContractSymbol, Side: intent.Side, TradeType: intent.TradeType, Quantity: intent.Quantity, ExpiresAt: intent.ExpiresAt}
	if intent.TradeType == "close" {
		targets, err := m.closeTargets(intent, view)
		if err != nil {
			return nil, err
		}
		p.Request.ClosePositions = targets
		p.Request.Method = "market"
		p.Request.Entry = &controller.Condition{Type: "market"}
	} else {
		p.Request.Method = "market"
		p.Request.Entry = &controller.Condition{Type: "market"}
	}
	if !m.state.DryRun {
		if api == nil {
			return nil, fmt.Errorf("実注文の通信機能がありません")
		}
		if view.InstanceID == "" || view.Session == nil || view.BrokerStale || view.PositionsStale || !view.Connected ||
			view.SessionError != "" || view.BrokerError != "" || view.PositionsError != "" {
			return nil, fmt.Errorf("新鮮な接続・注文・建玉・セッション情報が必要です")
		}
		if !intent.ExpiresAt.After(intent.At) {
			return nil, fmt.Errorf("発注期限を過ぎています")
		}
		{
			p.Raw = true
			exchange := view.Session.Exchange
			if exchange != 23 && exchange != 24 && exchange != 33 && exchange != 34 {
				return nil, fmt.Errorf("未対応の市場番号です: %d", exchange)
			}
			if m.state.Symbol == "NK225micro" && exchange >= 30 {
				exchange -= 10
			}
			side := "2"
			if intent.Side == "sell" {
				side = "1"
			}
			tradeType := 1
			if intent.TradeType == "close" {
				tradeType = 2
			}
			p.RawRequest = controller.BrokerRequest{Symbol: intent.ContractSymbol, Exchange: exchange, TradeType: tradeType, TimeInForce: 2, Side: side, Qty: intent.Quantity, FrontOrderType: 120}
			for _, c := range p.Request.ClosePositions {
				p.RawRequest.ClosePositions = append(p.RawRequest.ClosePositions, controller.BrokerClosePosition{HoldID: c.HoldID, Qty: c.Quantity})
			}
		}
	}
	m.state.Orders = append(m.state.Orders, p)
	if err := m.save(); err != nil {
		return nil, err
	}
	if m.state.DryRun {
		if err := m.applyFill(p, "dry-"+intent.ID, intent.Quantity, intent.Price, intent.At); err != nil {
			return nil, err
		}
		p.Status = "done"
		if err := m.save(); err != nil {
			return nil, err
		}
		return m.OutstandingFills(), nil
	}
	// ----------------------------------------
	// 永続化された同一発注意図だけを一度送信します。
	if p.Raw {
		result, err := api.SendFuture(ctx, p.RawRequest)
		if err != nil {
			p.Status = "unknown"
			p.Error = "発注結果不明。自動再送せず照合待ち: " + err.Error()
			var apiErr *controller.APIError
			if errors.As(err, &apiErr) && !apiErr.Unknown {
				p.Status = "rejected"
				p.Error = err.Error()
			}
			if e := m.save(); e != nil {
				return nil, e
			}
			return nil, err
		}
		if result.OrderID == "" {
			p.Status = "unknown"
			p.Error = "上流注文番号を取得できませんでした"
		} else {
			p.BrokerOrderID = result.OrderID
			p.Status = "submitted"
		}
	}
	return nil, m.save()
}

// Reconcile は、注文の約定明細と建玉IDを使って差分を確定します。
//
// 引数: view controller.OrdersView はscope=allの照会結果です。
// 返り値: []Fill は未反映約定、error は数量・所有関係の不整合です。
func (m *Manager) Reconcile(view controller.OrdersView) (fills []Fill, err error) {
	if m.state.DryRun {
		return m.OutstandingFills(), nil
	}
	if view.BrokerStale || view.PositionsStale {
		return m.OutstandingFills(), nil
	}
	previousState, _ := json.Marshal(m.state)
	previousTrades := append([]journal.Trade(nil), m.trades...)
	defer func() {
		if err != nil {
			var projectionError *journal.ProjectionError
			if errors.As(err, &projectionError) {
				return
			}
			var restored savedState
			_ = json.Unmarshal(previousState, &restored)
			m.state = restored
			m.trades = previousTrades
		}
	}()
	changed := false
	for _, p := range m.state.Orders {
		if p.Status == "done" || p.Status == "rejected" {
			continue
		}
		var reservation *controller.Reservation
		if !p.Raw && p.InstanceID == view.InstanceID {
			for i := range view.Reservations {
				r := &view.Reservations[i]
				if r.ClientOrderID == p.Intent.ID {
					reservation = r
					p.ReservationID = r.ID
					for _, leg := range r.Legs {
						if leg.BrokerOrderID != "" {
							if p.BrokerOrderID != "" && p.BrokerOrderID != leg.BrokerOrderID {
								return nil, fmt.Errorf("想定外の複数注文です")
							}
							p.BrokerOrderID = leg.BrokerOrderID
						}
					}
					changed = true
					break
				}
			}
		}
		if p.BrokerOrderID == "" {
			if p.InstanceID != view.InstanceID {
				p.Status = "unknown"
				p.Error = "controllerが再起動しました。旧発注の結果不明につき再発注しません"
				changed = true
			}
			if reservation != nil && reservation.Finished {
				filled := float64(0)
				for _, l := range reservation.Legs {
					filled += l.FilledQuantity
				}
				if filled == 0 {
					p.Status = "done"
					changed = true
				}
			}
			continue
		}
		var broker *controller.BrokerOrder
		for i := range view.BrokerOrders {
			if view.BrokerOrders[i].ID == p.BrokerOrderID {
				broker = &view.BrokerOrders[i]
				break
			}
		}
		if broker == nil {
			continue
		}
		if broker.Symbol != p.Intent.ContractSymbol {
			return nil, fmt.Errorf("上流注文の銘柄が発注意図と異なります")
		}
		expectedSide := "2"
		if p.Intent.Side == "sell" {
			expectedSide = "1"
		}
		if broker.Side != expectedSide {
			return nil, fmt.Errorf("上流注文の方向が発注意図と異なります")
		}
		if broker.CumQty < 0 || math.Trunc(broker.CumQty) != broker.CumQty || broker.CumQty > float64(p.Intent.Quantity) {
			return nil, fmt.Errorf("上流の累計約定枚数が不正です")
		}
		qtyByID := map[string]int{}
		valueByID := map[string]float64{}
		timeByID := map[string]time.Time{}
		ids := []string{}
		for _, d := range broker.Details {
			if d.RecType != 8 {
				continue
			}
			if d.ExecutionID == "" || d.Qty <= 0 || math.Trunc(d.Qty) != d.Qty || !positive(d.Price) {
				return nil, fmt.Errorf("約定明細のID・数量・価格が不正です")
			}
			if _, ok := qtyByID[d.ExecutionID]; !ok {
				ids = append(ids, d.ExecutionID)
			}
			qtyByID[d.ExecutionID] += int(d.Qty)
			valueByID[d.ExecutionID] += d.Qty * d.Price
			if d.TransactTime != "" {
				at, parseError := time.Parse(time.RFC3339Nano, d.TransactTime)
				if parseError != nil {
					return nil, fmt.Errorf("約定時刻が不正です: %w", parseError)
				}
				timeByID[d.ExecutionID] = at
			}
		}
		detailQty := 0
		for _, qty := range qtyByID {
			detailQty += qty
		}
		if float64(detailQty) > broker.CumQty {
			return nil, fmt.Errorf("約定明細と累計数量が不一致です")
		}
		for _, id := range ids {
			qty := qtyByID[id]
			seen := p.Seen[id]
			delta := qty - seen
			if delta < 0 {
				return nil, fmt.Errorf("既確認の約定数量が減っています")
			}
			if delta == 0 {
				continue
			}
			price := valueByID[id] / float64(qty)
			if seen > 0 {
				return nil, fmt.Errorf("同じ約定IDの数量が変更されました。手動照合が必要です")
			}
			if p.Intent.TradeType == "new" {
				found := false
				for _, h := range view.Positions {
					if h.ExecutionID == id && h.Symbol == p.Intent.ContractSymbol && h.Side == p.Intent.Side && h.Quantity >= float64(qty) {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
			var closeTargets []controller.ClosePosition
			if p.Intent.TradeType == "close" {
				closeTargets, err = m.confirmedCloseTargets(p, delta, view)
				if err != nil {
					return nil, err
				}
				if len(closeTargets) == 0 {
					continue
				}
			}
			at := timeByID[id]
			if at.IsZero() {
				at = time.Now()
			}
			if err := m.applyFill(p, id, delta, price, at, closeTargets); err != nil {
				return nil, err
			}
			p.Seen[id] = qty
			changed = true
		}
		seen := 0
		for _, qty := range p.Seen {
			seen += qty
		}
		if float64(seen) > broker.CumQty || seen > p.Intent.Quantity {
			return nil, fmt.Errorf("注文数量を超える約定を検出しました")
		}
		terminal := broker.Finished || broker.State == 5
		if reservation != nil {
			terminal = terminal && reservation.Finished
		}
		if terminal && float64(seen) == broker.CumQty {
			p.Status = "done"
			p.Error = ""
			changed = true
		}
	}
	if changed {
		if err := m.save(); err != nil {
			return nil, err
		}
	}
	return m.OutstandingFills(), nil
}

// closeTargets は、自分が所有し、現在返済可能な建玉だけを選びます。
//
// 引数: intent Intent は返済意図、view controller.OrdersView は照会です。
// 返り値: []controller.ClosePosition は返済対象、error は数量不足です。
func (m *Manager) closeTargets(intent Intent, view controller.OrdersView) ([]controller.ClosePosition, error) {
	if len(intent.ClosePositions) > 0 {
		return m.explicitCloseTargets(intent, view)
	}
	holdings := m.Positions()
	sort.SliceStable(holdings, func(i, j int) bool { return holdings[i].OpenedAt.Before(holdings[j].OpenedAt) })
	remaining := intent.Quantity
	targets := []controller.ClosePosition{}
	for _, h := range holdings {
		if h.Owner != intent.Owner || h.Side == intent.Side || h.ContractSymbol != intent.ContractSymbol || (h.Product != "" && h.Product != intent.Product) {
			continue
		}
		available := h.Quantity
		if !m.state.DryRun {
			available = 0
			for _, p := range view.Positions {
				if p.ExecutionID == h.HoldID && p.Symbol == h.ContractSymbol && p.Side == h.Side {
					if p.AvailableQuantity < 0 || math.Trunc(p.AvailableQuantity) != p.AvailableQuantity {
						return nil, fmt.Errorf("返済可能数量が不正です")
					}
					available = int(p.AvailableQuantity)
					if available > h.Quantity {
						available = h.Quantity
					}
					break
				}
			}
		}
		if available > remaining {
			available = remaining
		}
		if available <= 0 {
			continue
		}
		targets = append(targets, controller.ClosePosition{HoldID: h.HoldID, Quantity: available})
		remaining -= available
		if remaining == 0 {
			break
		}
	}
	if remaining != 0 {
		return nil, fmt.Errorf("所有する返済可能建玉が%d枚不足しています", remaining)
	}
	return targets, nil
}

// applyFill は、確認済みの約定だけで所有建玉と実現損益を更新します。
//
// 引数: p *pending は注文、executionID string は約定ID、qty int は差分数量、price float64 は実単価、at time.Time は確認時刻、confirmed ...[]controller.ClosePosition は実残数量から確認した返済内訳です。
// 返り値: error は所有関係・返済数量の不一致です。
func (m *Manager) applyFill(p *pending, executionID string, qty int, price float64, at time.Time, confirmed ...[]controller.ClosePosition) error {
	if executionID == "" || qty < 1 || !positive(price) || at.IsZero() {
		return fmt.Errorf("確認済み約定の識別・数量・価格・時刻が不正です")
	}
	prior := 0
	for _, trade := range m.trades {
		if trade.ExecutionID == executionID && trade.ClientOrderID != p.Intent.ID {
			return fmt.Errorf("過去の別注文で記録済みの約定IDを再使用できません")
		}
		if trade.ClientOrderID == p.Intent.ID {
			if trade.ExecutionID == executionID {
				return fmt.Errorf("同じ注文の約定IDが重複しています")
			}
			prior += trade.Quantity
		}
	}
	if qty > p.Intent.Quantity || prior > p.Intent.Quantity-qty {
		return fmt.Errorf("約定数量が注文枚数を超えています")
	}
	feeBefore, err := standardOrderFee(m.state.Symbol, prior)
	if err != nil {
		return err
	}
	feeAfter, err := standardOrderFee(m.state.Symbol, prior+qty)
	if err != nil {
		return err
	}
	pnl, holdID, closedBasis := float64(0), "", ""
	var closed []controller.ClosePosition
	if p.Intent.TradeType == "new" {
		if _, exists := m.state.Holdings[executionID]; exists {
			return fmt.Errorf("約定IDを重複して建玉に追加しようとしました")
		}
		holdID = executionID
		m.state.Holdings[executionID] = &Holding{Owner: p.Intent.Owner, Product: p.Intent.Product, Evidence: append(json.RawMessage(nil), p.Intent.Evidence...), HoldID: executionID, ContractSymbol: p.Intent.ContractSymbol, Side: p.Intent.Side, Quantity: qty, Price: price, OpenedAt: at, Session: p.Intent.Session, EntryIntentID: p.Intent.ID}
	} else {
		closed, err = m.fillCloseTargets(p, qty, confirmed...)
		if err != nil {
			return err
		}
		closedBasis = "仮想約定の指定順配賦"
		if len(confirmed) > 0 && confirmed[0] != nil {
			closedBasis = "口座残数照合による配賦"
		}
		if p.CloseConsumed == nil {
			p.CloseConsumed = map[string]int{}
		}
		for _, target := range closed {
			h := m.state.Holdings[target.HoldID]
			direction := float64(1)
			if h.Side == "sell" {
				direction = -1
			}
			pnl += (price - h.Price) * direction * float64(target.Quantity) * m.state.Multiplier
			h.Quantity -= target.Quantity
			p.CloseConsumed[target.HoldID] += target.Quantity
		}
	}
	m.trades = append(m.trades, journal.Trade{Owner: p.Intent.Owner, Product: p.Intent.Product, MethodID: p.Intent.MethodID, Evidence: append(json.RawMessage(nil), p.Intent.Evidence...), ID: p.Intent.ID + ":" + executionID, At: at, Session: p.Intent.Session, Symbol: m.state.Symbol, TradeType: p.Intent.TradeType, Side: p.Intent.Side, Quantity: qty, Price: price, PnL: pnl, Fee: feeAfter - feeBefore, HoldID: holdID, Closed: closed, ClosedBasis: closedBasis, Reason: p.Intent.Reason, ClientOrderID: p.Intent.ID, ReservationID: p.ReservationID, BrokerOrderID: p.BrokerOrderID, ExecutionID: executionID, DryRun: m.state.DryRun})
	return nil
}

// confirmedCloseTargets は、返済明細と対象建玉の実残数量から減少分を確定します。
//
// 引数: p *pending は返済注文、qty int は今回約定数量、view controller.OrdersView は口座照会です。
// 返り値: []controller.ClosePosition は確認済み減少分、error は数量不一致です。照会が追いつかなければ空です。
func (m *Manager) confirmedCloseTargets(p *pending, qty int, view controller.OrdersView) ([]controller.ClosePosition, error) {
	remaining := qty
	targets := []controller.ClosePosition{}
	for _, target := range p.Request.ClosePositions {
		h := m.state.Holdings[target.HoldID]
		if h == nil {
			return nil, fmt.Errorf("返済指定建玉の所有記録がありません")
		}
		observed := 0
		for _, position := range view.Positions {
			if position.ExecutionID == target.HoldID {
				if position.Symbol != h.ContractSymbol || position.Side != h.Side || position.Quantity < 0 || math.Trunc(position.Quantity) != position.Quantity {
					return nil, fmt.Errorf("返済建玉の照会値が不正です")
				}
				observed = int(position.Quantity)
				break
			}
		}
		decreased := h.Quantity - observed
		if decreased < 0 {
			continue
		}
		if decreased > target.Quantity-p.CloseConsumed[target.HoldID] {
			return nil, fmt.Errorf("返済注文以外で所有建玉の数量が減っています")
		}
		if decreased > remaining {
			decreased = remaining
		}
		if decreased > 0 {
			targets = append(targets, controller.ClosePosition{HoldID: target.HoldID, Quantity: decreased})
			remaining -= decreased
		}
		if remaining == 0 {
			return targets, nil
		}
	}
	return nil, nil
}

// save は、台帳と約定正本を確定します。
//
// 引数: なし。
// 返り値: error は永続化失敗です。
func (m *Manager) save() error { return m.store.Save(m.state, m.trades) }

// newID は、発注意図に使う予測困難な識別子を作ります。
//
// 引数: なし。
// 返り値: string は識別子、error は乱数失敗です。
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// positive は、価格・倍率として使用できる正の有限値か確認します。
//
// 引数: value float64 は検査値です。
// 返り値: bool は正の有限値なら真です。
func positive(value float64) bool { return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value) }
