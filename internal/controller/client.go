package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const responseLimit = 32 * 1024 * 1024

// Client は再送とリダイレクトを行わない controller 通信処理です。
type Client struct {
	baseURL *url.URL
	http    *http.Client
	dryRun  atomic.Bool
}

// APIError は確定した拒否と、照合が必要な送信結果不明を区別します。
type APIError struct {
	StatusCode int
	Unknown    bool
	Message    string
}

// Error は通信失敗の説明を返します。
// 引数: なし。
// 返り値: string エラーの説明。
func (e *APIError) Error() string { return e.Message }

// New は URL と通信期限を検証してクライアントを生成します。
// 引数: baseURL string 接続先、timeout time.Duration 各HTTP要求の制限時間。
// 返り値: *Client 通信処理、error 設定が不正な場合のエラー。
func New(baseURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("controller の URL は認証情報・クエリを含まない http/https を指定してください")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("HTTP 制限時間は正数が必要です")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	c := &Client{baseURL: u, http: &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
	c.dryRun.Store(true)
	return c, nil
}

// SetDryRun は変更 API の物理的な送信禁止を設定します。初期値は true です。
//
// 引数: enabled bool は仮想実行で送信を禁止する指定です。
// 返り値: なし。
func (c *Client) SetDryRun(enabled bool) { c.dryRun.Store(enabled) }

// endpoint は接続先のパスとクエリを構成します。
// 引数: path string APIパス、query url.Values クエリ。
// 返り値: string 完全なURL。
func (c *Client) endpoint(path string, query url.Values) string {
	u := *c.baseURL
	u.Path += path
	u.RawQuery = query.Encode()
	return u.String()
}

// request は1回だけHTTP要求を送信し、受付不明をエラーに残します。
// 引数: ctx context.Context 中断制御、method/path string 要求、query url.Values クエリ、body any 本文。
// 返り値: *http.Response 応答、error 通信またはHTTPエラー。
func (c *Client) request(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	if method != http.MethodGet && c.dryRun.Load() {
		return nil, &APIError{Message: "dryrun のため変更 API を送信しません"}
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, &APIError{Unknown: method != "GET", Message: fmt.Sprintf("controller 通信失敗: %v", err)}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 8192))
		var failure struct {
			Error   string `json:"error"`
			Message string `json:"Message"`
		}
		_ = json.Unmarshal(raw, &failure)
		message := failure.Error
		if message == "" {
			message = failure.Message
		}
		if message == "" {
			message = res.Status
		}
		return nil, &APIError{StatusCode: res.StatusCode, Unknown: method != "GET" && (res.StatusCode >= 500 || res.StatusCode == 408 || res.StatusCode >= 300 && res.StatusCode < 400), Message: fmt.Sprintf("controller HTTP %d: %s", res.StatusCode, message)}
	}
	return res, nil
}

// decode は上限を設けたJSONを復号します。送信後の不正応答は結果不明です。
// 引数: res *http.Response 応答、out any 格納先、wrapped bool status/data形式、mutation bool 注文操作。
// 返り値: error 不正本文・サイズ超過・明示エラー。
func decode(res *http.Response, out any, wrapped, mutation bool) error {
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, responseLimit+1))
	if err != nil || len(raw) > responseLimit {
		return &APIError{StatusCode: res.StatusCode, Unknown: mutation, Message: "controller 応答が不完全、またはサイズ上限を超えました"}
	}
	if wrapped {
		var envelope struct {
			Status string          `json:"status"`
			Data   json.RawMessage `json:"data"`
			Error  string          `json:"error"`
		}
		if err = json.Unmarshal(raw, &envelope); err != nil || envelope.Status != "ok" || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
			return &APIError{StatusCode: res.StatusCode, Unknown: mutation, Message: "controller 応答の status/data が不正です: " + envelope.Error}
		}
		raw = envelope.Data
	}
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return &APIError{StatusCode: res.StatusCode, Unknown: mutation, Message: "controller 応答が空です"}
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return &APIError{StatusCode: res.StatusCode, Unknown: mutation, Message: fmt.Sprintf("controller JSON が不正です: %v", err)}
	}
	return nil
}

// json は通常のJSON APIを1回実行します。
// 引数: ctx context.Context 中断制御、method/path string 要求、query url.Values クエリ、body/out any 本文と格納先、wrapped bool 外形指定。
// 返り値: error 通信・本文エラー。
func (c *Client) json(ctx context.Context, method, path string, query url.Values, body, out any, wrapped bool) error {
	res, err := c.request(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	return decode(res, out, wrapped, method != "GET")
}

// Registrations はcontrollerの登録一覧を取得します。
// 引数: ctx context.Context 中断制御。
// 返り値: Registrations 登録一覧、error 通信エラー。
func (c *Client) Registrations(ctx context.Context) (Registrations, error) {
	var v Registrations
	err := c.json(ctx, "GET", "/api/v1/sys/registrations/future", nil, nil, &v, true)
	return v, err
}

// Orders は指定scopeの予約・注文・建玉・セッションを取得します。
// 引数: ctx context.Context 中断制御、scope string active/history/all。
// 返り値: OrdersView 一覧、error 通信エラー。
func (c *Client) Orders(ctx context.Context, scope string) (OrdersView, error) {
	var v OrdersView
	err := c.json(ctx, "GET", "/api/v1/orders/future", url.Values{"scope": {scope}}, nil, &v, true)
	return v, err
}

// Boards は登録済み先物の最新生板を取得します。鮮度は呼出側で判断します。
// 引数: ctx context.Context 中断制御。
// 返り値: []Board 受信済み板、error 通信エラー。
func (c *Client) Boards(ctx context.Context) ([]Board, error) {
	var v []Board
	err := c.json(ctx, "GET", "/api/v1/cache/board", url.Values{"type": {"f"}}, nil, &v, false)
	return v, err
}

// Board は1銘柄の最新生板を取得します。
// 引数: ctx context.Context 中断制御、symbol string 登録済み銘柄ID。
// 返り値: Board 最新板、error 通信エラー。
func (c *Client) Board(ctx context.Context, symbol string) (Board, error) {
	var v Board
	err := c.json(ctx, "GET", "/api/v1/cache/board", url.Values{"symbol": {symbol}, "type": {"f"}}, nil, &v, false)
	return v, err
}

// historyQuery は時刻をRFC3339で符号化します。
// 引数: from/to time.Time 任意の範囲、generation string 任意の履歴世代。
// 返り値: url.Values API用クエリ。
func historyQuery(from, to time.Time, generation string) url.Values {
	q := url.Values{}
	if !from.IsZero() {
		q.Set("from", from.Format(time.RFC3339Nano))
	}
	if !to.IsZero() {
		q.Set("to", to.Format(time.RFC3339Nano))
	}
	if generation != "" {
		q.Set("generation", generation)
	}
	return q
}

// Candles はcontrollerで集約済みの足を世代情報付きで取得します。
// 引数: ctx context.Context 中断制御、code/interval/generation string 商品・足幅・世代、from/to time.Time 範囲。
// 返り値: Candles 履歴、error 不正指定・通信エラー。
func (c *Client) Candles(ctx context.Context, code, interval, generation string, from, to time.Time) (Candles, error) {
	var v Candles
	if (code != "NK225" && code != "TOPIX") || (interval != "1min" && interval != "5min") {
		return v, fmt.Errorf("未対応の履歴商品または足幅です")
	}
	err := c.json(ctx, "GET", "/api/v1/history/"+code+"/candles/"+interval, historyQuery(from, to, generation), nil, &v, false)
	return v, err
}

// Book は生板履歴を読み切り、完結トレーラーを確認してから返します。
// 引数: ctx context.Context 中断制御、code string NK225/TOPIX、from/to time.Time 受信時刻範囲。
// 返り値: BookHistory 完結した履歴、error 中断・世代変更・通信エラー。
func (c *Client) Book(ctx context.Context, code string, from, to time.Time) (BookHistory, error) {
	var result BookHistory
	if code != "NK225" && code != "TOPIX" {
		return result, fmt.Errorf("未対応の履歴商品です")
	}
	res, err := c.request(ctx, "GET", "/api/v1/history/"+code+"/book", historyQuery(from, to, ""), nil)
	if err != nil {
		return result, err
	}
	defer res.Body.Close()
	d := json.NewDecoder(res.Body)
	for {
		var b Board
		err = d.Decode(&b)
		if err == io.EOF {
			break
		}
		if err != nil {
			return BookHistory{}, fmt.Errorf("板履歴が不完全です: %w", err)
		}
		result.Data = append(result.Data, b)
	}
	if res.Trailer.Get("X-History-Complete") != "true" {
		return BookHistory{}, fmt.Errorf("板履歴の完結を確認できません")
	}
	result.Snapshot.SessionID = res.Header.Get("X-History-Session-Id")
	result.Snapshot.Generation = res.Header.Get("X-History-Generation")
	result.Snapshot.Code = res.Header.Get("X-History-Code")
	result.Snapshot.AsOf, err = time.Parse(time.RFC3339Nano, res.Header.Get("X-History-As-Of"))
	if err != nil {
		return BookHistory{}, fmt.Errorf("板履歴の取得時刻が不正です")
	}
	result.Snapshot.Sequence, err = strconv.ParseUint(res.Header.Get("X-History-Sequence"), 10, 64)
	if err != nil {
		return BookHistory{}, fmt.Errorf("板履歴の連番が不正です")
	}
	if result.Snapshot.SessionID == "" || result.Snapshot.Generation == "" || result.Snapshot.Code != code {
		return BookHistory{}, fmt.Errorf("板履歴の対象・世代が不正です")
	}
	return result, nil
}

// ----------------------------------------

// Reservation は既存予約の照合状態を取得します。
// 引数: ctx context.Context 中断制御、id string 予約ID。
// 返り値: Reservation 予約状態、error 通信エラー。
func (c *Client) Reservation(ctx context.Context, id string) (Reservation, error) {
	var v Reservation
	if strings.ContainsAny(id, "/\\") || id == "" {
		return v, fmt.Errorf("予約IDが不正です")
	}
	err := c.json(ctx, "GET", "/api/v1/orders/future/reservations/"+id, nil, nil, &v, true)
	return v, err
}

// SendFuture は非SOR商品にも対応した透過先物注文を1回だけ送ります。
// 引数: ctx context.Context 中断制御、request BrokerRequest 確認済みの発注明細。
// 返り値: OrderResponse 受付番号、error 失敗・結果不明。
func (c *Client) SendFuture(ctx context.Context, request BrokerRequest) (OrderResponse, error) {
	var v OrderResponse
	err := c.json(ctx, "POST", "/api/v1/redirect/sendorder/future", nil, request, &v, false)
	if err == nil {
		if v.Result != 0 {
			err = &APIError{Message: fmt.Sprintf("上流先物注文が拒否されました: Result=%d", v.Result)}
		} else if v.OrderID == "" {
			err = &APIError{Unknown: true, Message: "上流注文の受付番号が欠落しています"}
		}
	}
	return v, err
}

// CancelFuture は透過取消APIを1回要求します。再送は行いません。
// 引数: ctx context.Context 中断制御、orderID string 上流注文番号。
// 返り値: OrderResponse 受付応答、error 失敗・結果不明。
func (c *Client) CancelFuture(ctx context.Context, orderID string) (OrderResponse, error) {
	var v OrderResponse
	if strings.TrimSpace(orderID) == "" {
		return v, fmt.Errorf("取消対象の注文番号が空です")
	}
	err := c.json(ctx, "PUT", "/api/v1/redirect/cancelorder", nil, map[string]string{"OrderId": orderID}, &v, false)
	if err == nil && v.Result != 0 {
		err = &APIError{Message: fmt.Sprintf("上流取消が拒否されました: Result=%d", v.Result)}
	}
	return v, err
}

// BrokerOrders は透過APIで先物注文と約定明細を再照会します。
// 引数: ctx context.Context 中断制御。
// 返り値: []BrokerOrder 注文明細、error 通信エラー。
func (c *Client) BrokerOrders(ctx context.Context) ([]BrokerOrder, error) {
	var v []BrokerOrder
	err := c.json(ctx, "GET", "/api/v1/redirect/orders", url.Values{"product": {"3"}, "details": {"true"}}, nil, &v, false)
	return v, err
}

// BrokerPositions は透過APIで先物建玉を再照会します。
// 引数: ctx context.Context 中断制御。
// 返り値: []BrokerPosition 建玉、error 通信エラー。
func (c *Client) BrokerPositions(ctx context.Context) ([]BrokerPosition, error) {
	var v []BrokerPosition
	err := c.json(ctx, "GET", "/api/v1/redirect/positions", url.Values{"product": {"3"}, "addinfo": {"true"}}, nil, &v, false)
	return v, err
}

// ----------------------------------------

// Stream は生PUSHを順番どおりコールバックへ渡します。再接続は呼出元が制御します。
// 引数: ctx context.Context 中断制御、onBoard func(Board)error 観測ごとの処理。
// 返り値: error 切断・中断・観測処理エラー。
func (c *Client) Stream(ctx context.Context, onBoard func(Board) error) error {
	u := *c.baseURL
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path += "/api/v1/redirect/websocket"
	dialer := websocket.Dialer{HandshakeTimeout: c.http.Timeout}
	conn, res, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
		return fmt.Errorf("生PUSH接続失敗: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(4 * 1024 * 1024)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("生PUSH切断: %w", err)
		}
		var b Board
		if err = json.Unmarshal(raw, &b); err != nil {
			return fmt.Errorf("生PUSHのJSONが不正です: %w", err)
		}
		if b.Symbol == "" {
			continue
		}
		if err = onBoard(b); err != nil {
			return err
		}
	}
}
