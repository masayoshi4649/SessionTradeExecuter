package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// SymbolInfo は登録銘柄の上流属性です。TradingUnit は円換算倍率ではありません。
type SymbolInfo struct {
	Symbol          string          `json:"Symbol"`
	SymbolName      string          `json:"SymbolName"`
	Exchange        int             `json:"Exchange"`
	DerivMonth      int             `json:"DerivMonth"`
	TradingUnit     float64         `json:"TradingUnit"`
	PriceRangeGroup string          `json:"PriceRangeGroup"`
	Raw             json.RawMessage `json:"-"`
}

// UnmarshalJSON は未知属性も原文のまま保持します。
//
// 引数: data []byte は上流の銘柄属性です。
// 返り値: error は不正 JSON です。
func (s *SymbolInfo) UnmarshalJSON(data []byte) error {
	type fields SymbolInfo
	var value fields
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = SymbolInfo(value)
	s.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// Symbol は取得済み銘柄 ID の最新属性を上流に照会します。
//
// 引数: ctx context.Context は中断制御、symbol string は登録銘柄、exchange int は市場です。
// 返り値: SymbolInfo は属性、error は通信または不正要求です。
func (c *Client) Symbol(ctx context.Context, symbol string, exchange int) (SymbolInfo, error) {
	var result SymbolInfo
	if symbol == "" || strings.ContainsAny(symbol, "/\\@?#") || exchange < 1 {
		return result, fmt.Errorf("銘柄属性の対象が不正です")
	}
	err := c.json(ctx, "GET", "/api/v1/redirect/symbol/"+symbol+"@"+strconv.Itoa(exchange),
		url.Values{"addinfo": {"true"}}, nil, &result, false)
	if err == nil && result.Symbol != symbol {
		return result, fmt.Errorf("銘柄属性の応答対象が異なります")
	}
	return result, err
}

// ValidateSnapshots は二市場・足幅の異なる履歴の同一世代性を検証します。
//
// 引数: snapshots ...Snapshot は別 API から読み取ったスナップショットです。
// 返り値: error は再起動・場替わり・不正メタデータによる混在です。
func ValidateSnapshots(snapshots ...Snapshot) error {
	if len(snapshots) == 0 {
		return fmt.Errorf("照合する履歴がありません")
	}
	base := snapshots[0]
	for _, item := range snapshots {
		if item.SessionID == "" || item.Generation == "" || item.Code == "" || item.AsOf.IsZero() ||
			item.SessionID != base.SessionID || item.Generation != base.Generation {
			return fmt.Errorf("履歴の対象場または世代が一致しません")
		}
		if !item.Session.StartsAt.IsZero() && !base.Session.StartsAt.IsZero() &&
			(!item.Session.StartsAt.Equal(base.Session.StartsAt) || !item.Session.EndsAt.Equal(base.Session.EndsAt)) {
			return fmt.Errorf("履歴の立会時刻が一致しません")
		}
	}
	return nil
}
