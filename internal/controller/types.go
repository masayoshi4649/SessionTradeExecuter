// Package controller は kabus-controller の公開API契約を表現します。
package controller

import (
	"encoding/json"
	"fmt"
	"time"
)

// Session は API が割り当てた立会です。営業時間をクライアントで再定義しません。
type Session struct {
	Name      string    `json:"name"`
	Exchange  int       `json:"exchange"`
	StartsAt  time.Time `json:"startsAt"`
	SendFrom  time.Time `json:"sendFrom"`
	SendUntil time.Time `json:"sendUntil"`
	EndsAt    time.Time `json:"endsAt"`
}

// Future は登録に成功した商品の銘柄コード・限月・市場です。
type Future struct {
	FutureCode string `json:"futureCode"`
	DerivMonth int    `json:"derivMonth"`
	Symbol     string `json:"symbol"`
	SymbolName string `json:"symbolName"`
	Exchange   int    `json:"exchange"`
}

// Registrations は controller が保持する登録先物一覧です。
type Registrations struct {
	State     string   `json:"state"`
	UpdatedAt string   `json:"updatedAt"`
	Futures   []Future `json:"futures"`
}

// Level は上流の板価格と数量です。
type Level struct {
	Price float64 `json:"Price"`
	Qty   float64 `json:"Qty"`
}

// Board は生PUSHの必要項目です。時刻の null は空文字として保持します。
type Board struct {
	Raw               json.RawMessage `json:"-"`
	Symbol            string          `json:"Symbol"`
	SymbolName        string          `json:"SymbolName"`
	Exchange          int             `json:"Exchange"`
	CurrentPrice      float64         `json:"CurrentPrice"`
	CurrentPriceTime  string          `json:"CurrentPriceTime"`
	TradingVolume     float64         `json:"TradingVolume"`
	TradingVolumeTime string          `json:"TradingVolumeTime"`
	Buy1              Level           `json:"Buy1"`
	Buy2              Level           `json:"Buy2"`
	Buy3              Level           `json:"Buy3"`
	Buy4              Level           `json:"Buy4"`
	Buy5              Level           `json:"Buy5"`
	Buy6              Level           `json:"Buy6"`
	Buy7              Level           `json:"Buy7"`
	Buy8              Level           `json:"Buy8"`
	Buy9              Level           `json:"Buy9"`
	Buy10             Level           `json:"Buy10"`
	Sell1             Level           `json:"Sell1"`
	Sell2             Level           `json:"Sell2"`
	Sell3             Level           `json:"Sell3"`
	Sell4             Level           `json:"Sell4"`
	Sell5             Level           `json:"Sell5"`
	Sell6             Level           `json:"Sell6"`
	Sell7             Level           `json:"Sell7"`
	Sell8             Level           `json:"Sell8"`
	Sell9             Level           `json:"Sell9"`
	Sell10            Level           `json:"Sell10"`
}

// UnmarshalJSON は未知フィールドを含む生板を複製して保持します。
//
// 引数: data []byte は一件の生板 JSON です。
// 返り値: error は不正 JSON または型の不一致です。
func (b *Board) UnmarshalJSON(data []byte) error {
	type fields Board
	var value fields
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*b = Board(value)
	b.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// Snapshot は履歴の読取範囲とセッション世代です。
type Snapshot struct {
	SessionID  string    `json:"sessionId"`
	Generation string    `json:"generation"`
	Code       string    `json:"code"`
	Session    Session   `json:"session"`
	AsOf       time.Time `json:"asOf"`
	Sequence   uint64    `json:"sequence"`
}

// Candle は controller 側で集約した足です。
type Candle struct {
	Time           time.Time `json:"time"`
	Open           float64   `json:"open"`
	High           float64   `json:"high"`
	Low            float64   `json:"low"`
	Close          float64   `json:"close"`
	Volume         float64   `json:"volume"`
	VolumeComplete bool      `json:"volumeComplete"`
	Closed         bool      `json:"closed"`
}

// Candles は snapshot と足配列を持つ履歴応答です。
type Candles struct {
	Snapshot Snapshot `json:"snapshot"`
	Data     []Candle `json:"data"`
}

// BookHistory はトレーラー完結を確認した生板履歴です。
type BookHistory struct {
	Snapshot Snapshot
	Data     []Board
}

// Execution は条件成立後の実際の執行方法です。
type Execution struct {
	Type  string  `json:"type"`
	Price float64 `json:"price,omitempty"`
}

// Condition は controller に登録する条件です。
type Condition struct {
	Type            string    `json:"type"`
	Price           float64   `json:"price,omitempty"`
	Offset          float64   `json:"offset,omitempty"`
	ActivationPrice float64   `json:"activationPrice,omitempty"`
	Reference       string    `json:"reference"`
	Execution       Execution `json:"execution"`
}

// ClosePosition は照合済み建玉IDと返済枚数です。
type ClosePosition struct {
	HoldID   string `json:"holdId"`
	Quantity int    `json:"quantity"`
}

// Request は起動世代・重複排除キー付きの予約要求です。
type Request struct {
	InstanceID     string          `json:"instanceId"`
	ClientOrderID  string          `json:"clientOrderId"`
	Method         string          `json:"method"`
	Symbol         string          `json:"symbol"`
	Side           string          `json:"side"`
	TradeType      string          `json:"tradeType"`
	Quantity       int             `json:"quantity"`
	ProfitOnly     bool            `json:"profitOnly,omitempty"`
	ExpiresAt      time.Time       `json:"expiresAt"`
	Entry          *Condition      `json:"entry,omitempty"`
	Exits          []Condition     `json:"exits,omitempty"`
	ClosePositions []ClosePosition `json:"closePositions,omitempty"`
}

// Leg は予約内の注文とその照合状態です。
type Leg struct {
	ID                  string          `json:"id"`
	Role                string          `json:"role"`
	Side                string          `json:"side"`
	TradeType           string          `json:"tradeType"`
	Quantity            int             `json:"quantity"`
	State               string          `json:"state"`
	BrokerOrderID       string          `json:"brokerOrderId"`
	FilledQuantity      float64         `json:"filledQuantity"`
	UnprotectedQuantity float64         `json:"unprotectedQuantity"`
	TriggeredAt         *time.Time      `json:"triggeredAt"`
	Error               string          `json:"error"`
	CancelRequested     bool            `json:"cancelRequested"`
	SwitchPending       bool            `json:"switchPending"`
	ClosePositions      []ClosePosition `json:"closePositions"`
}

// Reservation は予約受付結果です。Finished は建玉ゼロを意味しません。
type Reservation struct {
	ID            string    `json:"id"`
	ClientOrderID string    `json:"clientOrderId"`
	Method        string    `json:"method"`
	Symbol        string    `json:"symbol"`
	SymbolName    string    `json:"symbolName"`
	Side          string    `json:"side"`
	TradeType     string    `json:"tradeType"`
	Quantity      int       `json:"quantity"`
	State         string    `json:"state"`
	CreatedAt     time.Time `json:"createdAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
	Session       Session   `json:"session"`
	Legs          []Leg     `json:"legs"`
	Error         string    `json:"error"`
	CanCancel     bool      `json:"canCancel"`
	Finished      bool      `json:"finished"`
}

// BrokerDetail は上流の約定明細です。RecType=8 が約定です。
type BrokerDetail struct {
	SeqNum       int     `json:"SeqNum"`
	RecType      int     `json:"RecType"`
	State        int     `json:"State"`
	ExecutionID  string  `json:"ExecutionID"`
	Qty          float64 `json:"Qty"`
	Price        float64 `json:"Price"`
	TransactTime string  `json:"TransactTime"`
}

// BrokerOrder は予約経由または他媒体からの注文です。
type BrokerOrder struct {
	ID              string         `json:"ID"`
	State           int            `json:"State"`
	Symbol          string         `json:"Symbol"`
	Side            string         `json:"Side"`
	Exchange        int            `json:"Exchange"`
	CashMargin      int            `json:"CashMargin"`
	OrderQty        float64        `json:"OrderQty"`
	CumQty          float64        `json:"CumQty"`
	Price           float64        `json:"Price"`
	RecvTime        string         `json:"RecvTime"`
	Details         []BrokerDetail `json:"Details"`
	Source          string         `json:"source"`
	ReservationID   string         `json:"reservationId"`
	LegID           string         `json:"legId"`
	Finished        bool           `json:"finished"`
	CanCancel       bool           `json:"canCancel"`
	CancelRequested bool           `json:"cancelRequested"`
	CancelState     string         `json:"cancelState"`
	CancelError     string         `json:"cancelError"`
}

// Position は controller が返済可否も確認した建玉です。
type Position struct {
	ExecutionID       string  `json:"executionId"`
	Symbol            string  `json:"symbol"`
	Side              string  `json:"side"`
	Quantity          float64 `json:"quantity"`
	HeldQuantity      float64 `json:"heldQuantity"`
	AvailableQuantity float64 `json:"availableQuantity"`
	Price             float64 `json:"price"`
	CanClose          bool    `json:"canClose"`
}

// OrdersView は注文・建玉・通信状態・立会を同時に取得する応答です。
type OrdersView struct {
	InstanceID         string        `json:"instanceId"`
	Session            *Session      `json:"session"`
	SessionError       string        `json:"sessionError"`
	Reservations       []Reservation `json:"reservations"`
	BrokerOrders       []BrokerOrder `json:"brokerOrders"`
	BrokerUpdatedAt    *time.Time    `json:"brokerUpdatedAt"`
	BrokerStale        bool          `json:"brokerStale"`
	BrokerError        string        `json:"brokerError"`
	Positions          []Position    `json:"positions"`
	PositionsUpdatedAt *time.Time    `json:"positionsUpdatedAt"`
	PositionsStale     bool          `json:"positionsStale"`
	PositionsError     string        `json:"positionsError"`
	Connected          bool          `json:"connected"`
}

// BrokerClosePosition は透過APIで指定する返済建玉です。
type BrokerClosePosition struct {
	HoldID string `json:"HoldID"`
	Qty    int    `json:"Qty"`
}

// BrokerRequest はAPIキーを含まない上流形式の先物注文です。
type BrokerRequest struct {
	Symbol         string                `json:"Symbol"`
	Exchange       int                   `json:"Exchange"`
	TradeType      int                   `json:"TradeType"`
	TimeInForce    int                   `json:"TimeInForce"`
	Side           string                `json:"Side"`
	Qty            int                   `json:"Qty"`
	Price          float64               `json:"Price"`
	ExpireDay      int                   `json:"ExpireDay"`
	FrontOrderType int                   `json:"FrontOrderType"`
	ClosePositions []BrokerClosePosition `json:"ClosePositions,omitempty"`
}

// OrderResponse は上流の受付結果です。受付と約定は異なります。
type OrderResponse struct {
	Result  int    `json:"Result"`
	OrderID string `json:"OrderId"`
}

// UnmarshalJSON は Result 欠落を成功のゼロと取り違えないよう検証します。
// 引数: data []byte 上流の注文・取消応答。
// 返り値: error Result が欠落・null・不正型の場合のエラー。
func (o *OrderResponse) UnmarshalJSON(data []byte) error {
	var v struct {
		Result  *int   `json:"Result"`
		OrderID string `json:"OrderId"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	if v.Result == nil {
		return fmt.Errorf("上流応答の Result が欠落しています")
	}
	o.Result, o.OrderID = *v.Result, v.OrderID
	return nil
}

// BrokerPosition は上流から直接照会した建玉です。
type BrokerPosition struct {
	ExecutionID string  `json:"ExecutionID"`
	Symbol      string  `json:"Symbol"`
	Side        string  `json:"Side"`
	Exchange    int     `json:"Exchange"`
	LeavesQty   float64 `json:"LeavesQty"`
	HoldQty     float64 `json:"HoldQty"`
	Price       float64 `json:"Price"`
}
