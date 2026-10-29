package controller

import (
	"context"
	"fmt"
	"strings"
)

// CancelReservation は所有確認済みの一予約を解除し、その後の新規発動を止めます。
//
// 引数: ctx(context.Context)は通信期限、id/instanceID(string)は予約IDと現在のcontroller世代です。
// 返り値: errorは受付失敗です。受付を上流注文の取消完了や建玉返済とは扱いません。
func (c *Client) CancelReservation(ctx context.Context, id, instanceID string) error {
	if id == "" || instanceID == "" || strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("予約取消のIDまたはcontroller世代が不正です")
	}
	var result Reservation
	return c.json(ctx, "POST", "/api/v1/orders/future/reservations/"+id+"/cancel", nil, map[string]string{"instanceId": instanceID}, &result, true)
}
