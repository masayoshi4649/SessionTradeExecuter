package journal

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// SessionPL は手数料と未実現損益を含めない、終了場の確認済み粗損益です。
type SessionPL struct {
	Session  string  `json:"session"`
	Product  string  `json:"product"`
	Mode     string  `json:"mode"`
	Profit   float64 `json:"profit"`
	Loss     float64 `json:"loss"`
	Net      float64 `json:"net"`
	EntryQty int     `json:"entry_qty"`
}

var reportPLHeader = []string{"利益", "損失", "収支", "新規建合計qty", "セッション", "銘柄", "モード"}
var reportTradeHeader = []string{"売買種別", "price", "qty", "timestamp", "損益", "手法ID"}

// reportKey は商品とモードを含めた終了場の識別子を返します。
//
// 引数: value(SessionPL)。返り値: string。集計CSVに同じ識別子を重複させません。
func reportKey(value SessionPL) string { return value.Session + "/" + value.Product + "/" + value.Mode }

// validReportProduct は商品名を一階層の安全なファイル名として検査します。
//
// 引数: product(string)。返り値: bool。パス区切りや相対移動を許可しません。
func validReportProduct(product string) bool {
	if product == "" || product == "." || product == ".." || strings.TrimSpace(product) != product {
		return false
	}
	for _, c := range product {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// ReadSessionPL は利用者が残した集計CSVだけを読み、DBから履歴を復元しません。
//
// 引数: logRoot(string)。返り値: []SessionPLとerror。欠損は空、破損・重複行はエラーです。
func ReadSessionPL(logRoot string) ([]SessionPL, error) {
	if strings.TrimSpace(logRoot) == "" {
		return nil, fmt.Errorf("共通ログ保存先が空です")
	}
	raw, err := os.ReadFile(filepath.Join(logRoot, "sessionPL.csv"))
	if os.IsNotExist(err) {
		return []SessionPL{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || !reflect.DeepEqual(rows[0], reportPLHeader) {
		return nil, fmt.Errorf("sessionPL.csvの見出しが不正です")
	}
	out, seen := []SessionPL{}, map[string]bool{}
	for _, row := range rows[1:] {
		if len(row) != 7 {
			return nil, fmt.Errorf("sessionPL.csvの列数が不正です")
		}
		v := SessionPL{Session: row[4], Product: row[5], Mode: row[6]}
		values := []*float64{&v.Profit, &v.Loss, &v.Net}
		for i, dst := range values {
			*dst, err = strconv.ParseFloat(row[i], 64)
			if err != nil || math.IsNaN(*dst) || math.IsInf(*dst, 0) {
				return nil, fmt.Errorf("sessionPL.csvの損益が不正です")
			}
		}
		v.EntryQty, err = strconv.Atoi(row[3])
		if err != nil || v.EntryQty < 0 || v.Profit < 0 || v.Loss < 0 || math.Abs(v.Net-(v.Profit-v.Loss)) > 1e-7 || !sessionPattern.MatchString(v.Session) || !validReportProduct(v.Product) || v.Mode != "live" && v.Mode != "dryrun" {
			return nil, fmt.Errorf("sessionPL.csvの識別子又は会計が不正です")
		}
		key := reportKey(v)
		if seen[key] {
			return nil, fmt.Errorf("sessionPL.csvの場が重複しています: %s", key)
		}
		seen[key] = true
		out = append(out, v)
	}
	return out, nil
}

// reportCSV は日本語を保持したUTF-8の正式CSVを生成します。
//
// 引数: header([]string)、rows([][]string)。返り値: []byteとerror。日本語Excel表示用BOMを付けます。
func reportCSV(header []string, rows [][]string) ([]byte, error) {
	var data bytes.Buffer
	data.WriteString("\xef\xbb\xbf")
	w := csv.NewWriter(&data)
	if err := w.Write(header); err != nil {
		return nil, err
	}
	if err := w.WriteAll(rows); err != nil {
		return nil, err
	}
	return data.Bytes(), w.Error()
}

// reportNumber は損益と価格を桁落ちさせずに十進表記へ変換します。
//
// 引数: value(float64)、signed(bool)。返り値: string。正の損益も符号を明示します。
func reportNumber(value float64, signed bool) string {
	s := strconv.FormatFloat(value, 'f', -1, 64)
	if signed && value > 0 {
		s = "+" + s
	}
	return s
}

// reportMethod は約定当時のIDを優先し、旧記録は当時のルールプロフィールで補完します。
//
// 引数: trade(Trade)。返り値: string。現在設定による推定や旧約定の書換えはしません。
func reportMethod(trade Trade) string {
	if strings.TrimSpace(trade.MethodID) != "" {
		return trade.MethodID
	}
	type methodEvidence struct {
		Method  string `json:"method_id"`
		Profile string `json:"rules_profile"`
	}
	var evidence struct {
		methodEvidence
		Decision methodEvidence `json:"decision"`
	}
	if json.Unmarshal(trade.Evidence, &evidence) == nil {
		for _, entry := range []methodEvidence{evidence.methodEvidence, evidence.Decision} {
			if strings.TrimSpace(entry.Method) != "" {
				return entry.Method
			}
			if name := rules.Name(entry.Profile); name != "" {
				return name
			}
			if strings.HasPrefix(entry.Profile, "NK225_") || strings.HasPrefix(entry.Profile, "TOPIX_") {
				return entry.Profile
			}
		}
	}
	return "不明"
}

// buildReport は確定順の実約定から六列明細と粗損益を同時生成します。
//
// 引数: value(SessionPL)、trades([]Trade)。返り値: SessionPL、[]byte、error。新規の損益は0です。
func buildReport(value SessionPL, trades []Trade) (SessionPL, []byte, error) {
	rows := [][]string{}
	for _, trade := range trades {
		if trade.Session != value.Session || (trade.DryRun != (value.Mode == "dryrun")) {
			continue
		}
		if trade.Product != "" && trade.Product != value.Product {
			return value, nil, fmt.Errorf("終了場の約定商品が登録商品と不一致です")
		}
		if trade.Quantity <= 0 || trade.At.IsZero() || math.IsNaN(trade.Price) || math.IsInf(trade.Price, 0) || trade.Price <= 0 || math.IsNaN(trade.PnL) || math.IsInf(trade.PnL, 0) {
			return value, nil, fmt.Errorf("終了場の約定値が不正です: %s", trade.ID)
		}
		kind, pnl := "", 0.0
		switch trade.TradeType + "/" + trade.Side {
		case "new/buy":
			kind = "LongN"
		case "new/sell":
			kind = "ShortN"
		case "close/sell":
			kind, pnl = "LongR", trade.PnL
		case "close/buy":
			kind, pnl = "ShortR", trade.PnL
		default:
			return value, nil, fmt.Errorf("終了場の売買種別が不正です: %s", trade.ID)
		}
		if trade.TradeType == "new" {
			value.EntryQty += trade.Quantity
		}
		if pnl > 0 {
			value.Profit += pnl
		} else {
			value.Loss -= pnl
		}
		rows = append(rows, []string{kind, reportNumber(trade.Price, false), strconv.Itoa(trade.Quantity), trade.At.Format(time.RFC3339Nano), reportNumber(pnl, true), reportMethod(trade)})
	}
	value.Net = value.Profit - value.Loss
	raw, err := reportCSV(reportTradeHeader, rows)
	return value, raw, err
}

// writeSessionPL は対象行だけを置換し、他商品や利用者が削除した行を再構築しません。
//
// 引数: logRoot(string)、value(SessionPL)、first(bool)。返り値: error。初回以外は存在する対象行だけ更新します。
func writeSessionPL(logRoot string, value SessionPL, first bool) error {
	items, err := ReadSessionPL(logRoot)
	if err != nil {
		return err
	}
	found := false
	for i := range items {
		if reportKey(items[i]) == reportKey(value) {
			items[i], found = value, true
			break
		}
	}
	if !found {
		if !first {
			return nil
		}
		items = append(items, value)
	}
	sort.Slice(items, func(i, j int) bool { return reportKey(items[i]) < reportKey(items[j]) })
	rows := make([][]string, 0, len(items))
	for _, v := range items {
		rows = append(rows, []string{reportNumber(v.Profit, false), reportNumber(v.Loss, false), reportNumber(v.Net, false), strconv.Itoa(v.EntryQty), v.Session, v.Product, v.Mode})
	}
	raw, err := reportCSV(reportPLHeader, rows)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(logRoot, "sessionPL.csv"), raw)
}
