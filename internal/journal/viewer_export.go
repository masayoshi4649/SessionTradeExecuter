package journal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type viewerTrade struct {
	ID         string   `json:"id"`
	Timestamp  string   `json:"timestamp"`
	Type       string   `json:"type"`
	PositionID string   `json:"position_id"`
	Price      *float64 `json:"price,omitempty"`
	Qty        int      `json:"qty"`
	FeeYen     float64  `json:"fee_yen,omitempty"`
	MethodID   string   `json:"method_id,omitempty"`
}

type viewerPosition struct {
	HoldID   string `json:"hold_id"`
	Product  string `json:"product"`
	Session  string `json:"session"`
	Side     string `json:"side"`
	Quantity int    `json:"quantity"`
}

type viewerFact struct {
	At        time.Time
	Sequence  int64
	Trade     *Trade
	Positions []viewerPosition
}

type viewerLot struct {
	Price        float64
	Quantity     int
	Side, Method string
}

var viewerJapan = time.FixedZone("JST", 9*3600)

// viewerBounds はViewerと同じ日本時間の場内範囲を返します。
//
// 引数: session(string)。返り値: 開始/終了(time.Time)、error。夜間日付は開始日です。
func viewerBounds(session string) (time.Time, time.Time, error) {
	if !sessionPattern.MatchString(session) {
		return time.Time{}, time.Time{}, fmt.Errorf("Viewerの場IDが不正です")
	}
	d, err := time.ParseInLocation("2006-01-02", session[:10], viewerJapan)
	if err != nil {
		return d, d, err
	}
	if strings.HasSuffix(session, "_Day") {
		return d.Add(8*time.Hour + 45*time.Minute), d.Add(15*time.Hour + 45*time.Minute), nil
	}
	return d.Add(17 * time.Hour), d.Add(30 * time.Hour), nil
}

// readViewerFacts は当場の確定約定と、所有終了の証明に必要な少量のイベントだけを読みます。
//
// 引数: tx(*sql.Tx)、session(viewerSession)。返り値: []viewerFact、最大約定/移管連番(int64)、error。
// 巨大なprevious_stateや原板は読み取らず、ライフサイクルの保有一覧だけをSQLで抜き出します。
func readViewerFacts(tx *sql.Tx, session viewerSession) ([]viewerFact, int64, int64, error) {
	start, end, err := viewerBounds(session.Session)
	if err != nil {
		return nil, 0, 0, err
	}
	facts := []viewerFact{}
	var lastTrade, lastEvent int64
	rows, err := tx.Query(`SELECT seq,body FROM trades WHERE session=? AND dryrun=? ORDER BY seq`, session.Session, session.Dry)
	if err != nil {
		return nil, 0, 0, err
	}
	for rows.Next() {
		var sequence int64
		var raw []byte
		if err = rows.Scan(&sequence, &raw); err != nil {
			rows.Close()
			return nil, 0, 0, err
		}
		var trade Trade
		if err = json.Unmarshal(raw, &trade); err != nil {
			rows.Close()
			return nil, 0, 0, err
		}
		if trade.Session != session.Session || trade.DryRun != session.Dry || trade.Product != "" && trade.Product != session.Product || trade.At.Before(start) || trade.At.After(end) || trade.At.IsZero() {
			rows.Close()
			return nil, 0, 0, fmt.Errorf("Viewer約定の商品・場内時計が不正です: %s", trade.ID)
		}
		copyTrade := trade
		facts = append(facts, viewerFact{At: trade.At, Sequence: sequence, Trade: &copyTrade})
		lastTrade = sequence
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, 0, 0, err
	}
	rows.Close()
	rows, err = tx.Query(`SELECT seq,kind,at,
CASE WHEN kind='保有玉の管理外移管' THEN json_extract(payload,'$.positions') ELSE json_extract(payload,'$.metadata.previous_holdings') END,
COALESCE(json_extract(payload,'$.dryrun'),json_extract(payload,'$.metadata.dryrun'))
FROM events WHERE kind IN ('保有玉の管理外移管','起動時状態切替') ORDER BY seq`)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int64
		var kind, clock string
		var raw sql.NullString
		var dry sql.NullInt64
		if err = rows.Scan(&sequence, &kind, &clock, &raw, &dry); err != nil {
			return nil, 0, 0, err
		}
		lastEvent = sequence
		if !raw.Valid || !dry.Valid || dry.Int64 != 0 && dry.Int64 != 1 || (dry.Int64 == 1) != session.Dry {
			continue
		}
		at, e := time.Parse(time.RFC3339Nano, clock)
		if e != nil {
			return nil, 0, 0, e
		}
		// 場末後の移管を旧場末へ遡及せず、その場では残玉の記録を保ちます。
		if at.Before(start) || at.After(end) {
			continue
		}
		var positions []viewerPosition
		if err = json.Unmarshal([]byte(raw.String), &positions); err != nil {
			return nil, 0, 0, fmt.Errorf("Viewer移管の保有一覧が不正です: %w", err)
		}
		facts = append(facts, viewerFact{At: at, Sequence: sequence, Positions: positions})
	}
	if err = rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	// 確認の遅着で正本を並べ替えず、表示資料だけを実約定時計へ安定整列します。
	sort.SliceStable(facts, func(i, j int) bool {
		if !facts[i].At.Equal(facts[j].At) {
			return facts[i].At.Before(facts[j].At)
		}
		if (facts[i].Trade != nil) != (facts[j].Trade != nil) {
			return facts[i].Trade != nil
		}
		return facts[i].Sequence < facts[j].Sequence
	})
	return facts, lastTrade, lastEvent, nil
}

// viewerPrice は有効な約定値を任意JSON項目へ渡します。
//
// 引数: value(float64)。返り値: *float64。
func viewerPrice(value float64) *float64 { return &value }

// allocateViewerFee は一約定の手数料を数量比で分け、最後の片へ端数を残します。
//
// 引数: fee(float64)、quantities([]int)、total(int)。返り値: []float64。整数円は整数円のまま分けます。
func allocateViewerFee(fee float64, quantities []int, total int) []float64 {
	fees := make([]float64, len(quantities))
	assigned := 0.0
	for i, qty := range quantities {
		part := fee - assigned
		if i < len(quantities)-1 {
			part = fee * float64(qty) / float64(total)
			if math.Trunc(fee) == fee {
				part = math.Floor(part)
			}
		}
		fees[i] = part
		assigned += part
	}
	return fees
}

// buildViewerTrades は所有IDで部分返済と管理外移管を追跡し、最小JSONLを生成します。
//
// 引数: session(viewerSession)、facts([]viewerFact)。返り値: JSONL([]byte)、入口手法一覧([]string)、error。
// P/L照合に失敗する資料は出力せず、CSVや正本へ架空の返済を追加しません。
func buildViewerTrades(session viewerSession, facts []viewerFact) ([]byte, []string, error) {
	lots := map[string]*viewerLot{}
	seen := map[string]bool{}
	methods := map[string]bool{}
	result := []viewerTrade{}
	for _, fact := range facts {
		stamp := fact.At.In(viewerJapan).Format(time.RFC3339Nano)
		if fact.Trade == nil {
			for _, p := range fact.Positions {
				if p.Session != "" && p.Session != session.Session || p.Product != "" && p.Product != session.Product {
					continue
				}
				lot := lots[p.HoldID]
				if lot == nil {
					if p.Session == session.Session && p.Quantity > 0 {
						return nil, nil, fmt.Errorf("Viewer移管対象の新規約定がありません: %s", p.HoldID)
					}
					continue
				}
				if lot.Quantity == 0 {
					continue
				}
				if p.Quantity != lot.Quantity || p.Side != "" && p.Side != lot.Side {
					return nil, nil, fmt.Errorf("Viewer移管の所有数量・方向が不一致です: %s", p.HoldID)
				}
				result = append(result, viewerTrade{ID: "detach/" + strconv.FormatInt(fact.Sequence, 10) + "/" + p.HoldID, Timestamp: stamp, Type: "Detach", PositionID: p.HoldID, Qty: p.Quantity, MethodID: lot.Method})
				lot.Quantity = 0
			}
			continue
		}
		t := *fact.Trade
		if t.ID == "" || seen[t.ID] || t.Quantity <= 0 || !viewerFinitePositive(t.Price) || math.IsNaN(t.Fee) || math.IsInf(t.Fee, 0) || t.Fee < 0 || math.IsNaN(t.PnL) || math.IsInf(t.PnL, 0) {
			return nil, nil, fmt.Errorf("Viewer約定の識別子・数値が不正です: %s", t.ID)
		}
		seen[t.ID] = true
		method := reportMethod(t)
		switch t.TradeType + "/" + t.Side {
		case "new/buy", "new/sell":
			id := t.HoldID
			if id == "" {
				id = t.ExecutionID
			}
			if id == "" || lots[id] != nil || t.PnL != 0 {
				return nil, nil, fmt.Errorf("Viewer新規の所有ID又は損益が不正です: %s", t.ID)
			}
			lots[id] = &viewerLot{Price: t.Price, Quantity: t.Quantity, Side: t.Side, Method: method}
			kind := "LongN"
			if t.Side == "sell" {
				kind = "ShortN"
			}
			result = append(result, viewerTrade{ID: "fill/" + t.ID + "#0", Timestamp: stamp, Type: kind, PositionID: id, Price: viewerPrice(t.Price), Qty: t.Quantity, FeeYen: t.Fee, MethodID: method})
			methods[method] = true
		case "close/buy", "close/sell":
			if len(t.Closed) == 0 {
				return nil, nil, fmt.Errorf("Viewer返済の確認済み対象IDがありません: %s", t.ID)
			}
			remaining, pnl := t.Quantity, 0.0
			quantities := []int{}
			for _, target := range t.Closed {
				lot := lots[target.HoldID]
				if lot == nil || lot.Side == t.Side || target.Quantity <= 0 || target.Quantity > lot.Quantity || target.Quantity > remaining {
					return nil, nil, fmt.Errorf("Viewer返済の所有ID・数量・因果が不正です: %s", t.ID)
				}
				direction := 1.0
				if lot.Side == "sell" {
					direction = -1
				}
				pnl += (t.Price - lot.Price) * direction * float64(target.Quantity) * session.PointValue
				lot.Quantity -= target.Quantity
				remaining -= target.Quantity
				quantities = append(quantities, target.Quantity)
			}
			if remaining != 0 || math.Abs(pnl-t.PnL) > 1e-6 {
				return nil, nil, fmt.Errorf("Viewer返済と保存済み粗損益が不一致です: %s", t.ID)
			}
			fees := allocateViewerFee(t.Fee, quantities, t.Quantity)
			for i, target := range t.Closed {
				kind := "LongR"
				if t.Side == "buy" {
					kind = "ShortR"
				}
				result = append(result, viewerTrade{ID: "fill/" + t.ID + "#" + strconv.Itoa(i+1), Timestamp: stamp, Type: kind, PositionID: target.HoldID, Price: viewerPrice(t.Price), Qty: target.Quantity, FeeYen: fees[i], MethodID: method})
			}
		default:
			return nil, nil, fmt.Errorf("Viewer約定の売買種別が不正です: %s", t.ID)
		}
	}
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	encoder.SetEscapeHTML(false)
	for _, row := range result {
		if err := encoder.Encode(row); err != nil {
			return nil, nil, err
		}
	}
	names := make([]string, 0, len(methods))
	for name := range methods {
		names = append(names, name)
	}
	sort.Strings(names)
	return raw.Bytes(), names, nil
}
