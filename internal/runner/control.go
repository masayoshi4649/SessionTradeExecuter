package runner

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
)

// Control は新規許可と、保有玉を管理外へ移す運転終了の要求を保持します。
type Control struct {
	enabled       atomic.Bool
	submit        sync.RWMutex
	pauseSequence atomic.Uint64
	clearedPause  atomic.Uint64
	detachStop    atomic.Bool
}

// NewControl は新規許可の初期状態を持つ制御器を作ります。
//
// 引数: enabled(bool) は新規・追加を許可する場合trueです。
// 返り値: *Control は複数ゴルーチンから安全に使用できる制御器です。
func NewControl(enabled bool) *Control {
	c := &Control{}
	c.enabled.Store(enabled)
	if !enabled {
		c.pauseSequence.Store(1)
	}
	return c
}

// SetEnabled は新規許可だけを変更し、保有玉と注文識別子を保持します。
//
// 引数: enabled(bool) は新しい許可状態です。
// 返り値: なし。送信中の呼出しがあれば完了まで待ち、OFF後の追加送信を防ぎます。既受付注文の照合と残新規取消は運転ループが行います。
func (c *Control) SetEnabled(enabled bool) {
	c.submit.Lock()
	defer c.submit.Unlock()
	if enabled && c.detachStop.Load() {
		return
	}
	if !enabled {
		// ONへ戻るまでが短くても、停止時の未完了新規の整理を省略させません。
		c.pauseSequence.Add(1)
	}
	c.enabled.Store(enabled)
}

// RequestDetachStop は新規と返済の両送信を止め、未約定注文の取消照合後に保有玉を管理外へ移します。
//
// 引数: なし。返り値: なし。送信中の要求の完了を待って確定し、以後は再開できません。
func (c *Control) RequestDetachStop() {
	c.submit.Lock()
	defer c.submit.Unlock()
	c.detachStop.Store(true)
	c.enabled.Store(false)
	c.pauseSequence.Add(1)
}

// DetachStopRequested は返済せずに管理外へ移す終了要求の有無を返します。
//
// 引数: なし。返り値: boolは管理終了を要求済みの場合trueです。
func (c *Control) DetachStopRequested() bool {
	return c.detachStop.Load()
}

// Enabled は画面で指定された新規許可を取得します。
//
// 引数: なし。返り値: bool はON指定の場合trueです。直前のOFFに伴う取消照合中は実際の新規を別途禁止します。
func (c *Control) Enabled() bool {
	return c.enabled.Load()
}

// entryPermit はOFFの確定操作と実際の新規送信開始を直列化します。
//
// 引数: なし。返り値: func()は送信後に必ず呼ぶ解放関数、boolは新規許可です。
func (c *Control) entryPermit() (func(), bool) {
	return c.tradePermit("new")
}

// tradePermit は管理終了の確定操作と、新規・返済の送信開始を直列化します。
//
// 引数: action(string)はnew/closeです。返り値: func()は送信後の解放関数、boolは送信許可です。
func (c *Control) tradePermit(action string) (func(), bool) {
	c.submit.RLock()
	if c.detachStop.Load() || action == "new" && (!c.Enabled() || c.pausePending()) {
		c.submit.RUnlock()
		return func() {}, false
	}
	return c.submit.RUnlock, true
}

// pausePending は停止要求後の新規整理が未完了か確認します。
//
// 引数: なし。返り値: bool は未送信破棄または受付済み新規の終了照合が必要な場合trueです。
func (c *Control) pausePending() bool {
	return c.clearedPause.Load() != c.pauseSequence.Load()
}

// TradeSnapshot は画面表示に必要な直近約定だけを持つ軽量なコピーです。
type TradeSnapshot struct {
	At       time.Time `json:"at"`
	Owner    string    `json:"owner"`
	Action   string    `json:"action"`
	Side     string    `json:"side"`
	Quantity int       `json:"quantity"`
	Price    float64   `json:"price"`
	PnLYen   float64   `json:"pnl_yen"`
	FeeYen   float64   `json:"fee_yen"`
	Reason   string    `json:"reason"`
}

// Snapshot は一商品の同じ運転時点から作る読取用の状態です。
// 確定損益・手数料・直近約定は当セッション分です。同場の再起動前の確定約定も含めます。
type Snapshot struct {
	Product            string              `json:"product"`
	Reference          string              `json:"reference"`
	Session            string              `json:"session"`
	DryRun             bool                `json:"dryrun"`
	EntriesEnabled     bool                `json:"entries_enabled"`
	State              string              `json:"state"`
	ReceivedAt         time.Time           `json:"received_at"`
	ProductReceivedAt  time.Time           `json:"product_received_at"`
	DecisionAt         time.Time           `json:"decision_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
	BuyQuantity        int                 `json:"buy_quantity"`
	SellQuantity       int                 `json:"sell_quantity"`
	Pending            bool                `json:"pending"`
	PendingError       string              `json:"pending_error"`
	Queued             int                 `json:"queued"`
	InputCount         uint64              `json:"input_count"`
	DecisionCount      uint64              `json:"decision_count"`
	UnrealizedYen      *float64            `json:"unrealized_yen"`
	RealizedYen        float64             `json:"realized_yen"`
	SessionRealizedYen float64             `json:"session_realized_yen"`
	FeesYen            float64             `json:"fees_yen"`
	Positions          []execution.Holding `json:"positions"`
	RecentTrades       []TradeSnapshot     `json:"recent_trades"`
	Stopping           bool                `json:"stopping"`
	Detaching          bool                `json:"detaching"`
	ActiveSlotID       string              `json:"active_slot_id"`
	ActiveMethod       string              `json:"active_method"`
	ActiveSlotStart    time.Time           `json:"active_slot_start"`
	ActiveSlotEnd      time.Time           `json:"active_slot_end"`
	SlotSwitching      bool                `json:"slot_switching"`
}

// RunControlled は外部の新規許可を監視しながら、所有玉の管理を続けます。
//
// 引数: ctx(context.Context) は終了要求、cfg(config.Config) は単一商品の設定、control(*Control) は新規許可、publish(func(Snapshot)) は状態の受取関数です。
// 返り値: error は起動・記録・運転の失敗です。nilのcontrolは初期OFF、nilのpublishは通知なしです。
// publishは運転ループから毎秒程度呼ぶため、呼出先は通信や長い処理をせずコピーを保存してください。
func RunControlled(ctx context.Context, cfg config.Config, control *Control, publish func(Snapshot)) error {
	if control == nil {
		control = NewControl(false)
	}
	return runControlled(ctx, cfg, control, publish)
}

// entriesEnabled は従来の運転器とWeb制御の許可を共通化します。
//
// 引数: なし。返り値: bool は新規許可です。制御器なしの従来運転はONです。
func (a *app) entriesEnabled() bool {
	return a.control == nil || a.control.Enabled() && !a.control.pausePending()
}

// acknowledgePause は取消受付だけで再開せず、全新規の終了確認後に停止世代を完了します。
//
// 引数: sequence(uint64) はこのtick開始時に読んだ停止世代です。
// 返り値: なし。途中で届いた次のOFF要求は次のtickで別途処理します。
func (a *app) acknowledgePause(sequence uint64) {
	if a.control == nil {
		return
	}
	for _, m := range a.markets {
		for _, queued := range m.Saved.Queue {
			if queued.Signal.Action == "new" {
				return
			}
		}
		for _, order := range m.Manager.Orders() {
			if order.TradeType == "new" && !order.Done {
				return
			}
		}
	}
	a.control.clearedPause.Store(sequence)
}

// publishSnapshot は運転ループだけから独立した画面用コピーを発行します。
//
// 引数: now(time.Time) は通知時刻、force(bool) は周期を待たず発行する指定です。
// 返り値: なし。コールバック先へ可変な戦略状態や原板を公開しません。
func (a *app) publishSnapshot(now time.Time, force bool) {
	if a.publish == nil || (!force && now.Before(a.nextPublish)) {
		return
	}
	a.nextPublish = now.Add(time.Second)
	for _, m := range a.markets {
		selectSnapshotSession(m)
		s := a.statusFor(m, now)
		v := Snapshot{Product: s.Product, Reference: s.Reference, Session: m.Saved.Plan.ID,
			DryRun: s.DryRun, EntriesEnabled: a.entriesEnabled(), State: s.State,
			ReceivedAt: s.ReceivedAt, ProductReceivedAt: s.ProductReceivedAt, DecisionAt: s.DecisionAt,
			UpdatedAt: now, Pending: s.Pending, PendingError: s.PendingError, Queued: s.Queued,
			InputCount: s.InputCount, DecisionCount: s.DecisionCount, UnrealizedYen: s.UnrealizedYen,
			RealizedYen: m.RealizedYen, SessionRealizedYen: m.SessionRealized[m.Saved.Plan.ID], FeesYen: m.FeesYen,
			Positions: s.Positions, RecentTrades: append([]TradeSnapshot{}, m.RecentTrades...), Stopping: a.stopping, Detaching: a.detaching()}
		if slot := m.Saved.ActiveSlot; slot != nil {
			v.ActiveSlotID, v.ActiveSlotStart, v.ActiveSlotEnd = slot.ID, slot.Start, slot.End
			if slot.Enabled {
				v.ActiveMethod = config.NameOfMethod(m.Reference, slot.Method)
			}
		} else if m.Saved.Plan.ID != "" {
			v.ActiveSlotID, v.ActiveSlotStart, v.ActiveSlotEnd = m.Saved.Plan.Kind, m.Saved.Plan.Start, m.Saved.Plan.End
			v.ActiveMethod = a.cfg.MethodName(m.Saved.Plan.Kind)
		}
		v.SlotSwitching = m.Saved.SlotSwitch != nil
		for _, h := range v.Positions {
			if h.Side == "buy" {
				v.BuyQuantity += h.Quantity
			} else if h.Side == "sell" {
				v.SellQuantity += h.Quantity
			}
		}
		if a.detaching() {
			v.State = "管理終了待ち・新規と返済を停止し、未約定注文を取消照合中"
		} else if a.control != nil && a.control.Enabled() && a.control.pausePending() && !a.stopping {
			v.State = "新規再開待ち・停止時の新規注文を取消照合中"
		} else if !v.EntriesEnabled && !a.stopping {
			if v.BuyQuantity+v.SellQuantity > 0 || v.Pending {
				v.State = "新規OFF・保有玉の返済と注文照合を継続"
			} else {
				v.State = "新規OFF・指標と受信を監視中"
			}
		}
		a.publish(v)
	}
}

// recordSnapshotFill は一度だけ反映した約定から表示損益を累積します。
//
// 引数: m(*marketRuntime) は当商品の運転状態、f(execution.Fill) は確認済み約定です。
// 返り値: なし。約定の元セッションへ計上し、仮想源の損益は混ぜません。
func recordSnapshotFill(m *marketRuntime, f execution.Fill) {
	if m.SnapshotFills == nil {
		m.SnapshotFills = map[string]bool{}
		m.SessionRealized = map[string]float64{}
		m.SessionFees = map[string]float64{}
		m.SessionTrades = map[string][]TradeSnapshot{}
	}
	if m.SnapshotFills[f.ID] {
		return
	}
	m.SnapshotFills[f.ID] = true
	net := f.PnL - f.Fee
	session := f.Session
	if session == "" {
		// 旧形式・人工試験の通知だけは現在の場を採用します。実約定は保存された場IDを必ず持ちます。
		session = m.Saved.Plan.ID
	}
	m.SessionRealized[session] += net
	m.SessionFees[session] += f.Fee
	recent := append(m.SessionTrades[session], TradeSnapshot{At: f.At, Owner: f.Owner, Action: f.TradeType,
		Side: f.Side, Quantity: f.Quantity, Price: f.Price, PnLYen: net, FeeYen: f.Fee, Reason: f.Reason})
	if len(recent) > 20 {
		recent = append([]TradeSnapshot(nil), recent[len(recent)-20:]...)
	}
	m.SessionTrades[session] = recent
	selectSnapshotSession(m)
}

// selectSnapshotSession は当場の確定結果だけを表示用フィールドへ選びます。
//
// 引数: m(*marketRuntime)は表示対象です。返り値: なし。過去場の正本・建玉・未確定注文は変更しません。
func selectSnapshotSession(m *marketRuntime) {
	m.RealizedYen = m.SessionRealized[m.Saved.Plan.ID]
	m.FeesYen = m.SessionFees[m.Saved.Plan.ID]
	m.RecentTrades = m.SessionTrades[m.Saved.Plan.ID]
}

// restoreSnapshotHistory は起動時に確定約定だけを表示台帳へ復元します。
//
// 引数: m(*marketRuntime)は当商品の管理器です。返り値: なし。旧戦略・旧建玉は復元せず、同場の履歴合算だけを行います。
func restoreSnapshotHistory(m *marketRuntime) {
	for _, trade := range m.Manager.Records() {
		recordSnapshotFill(m, execution.Fill{ID: trade.ID, Session: trade.Session, At: trade.At, Owner: trade.Owner,
			TradeType: trade.TradeType, Side: trade.Side, Quantity: trade.Quantity, Price: trade.Price,
			PnL: trade.PnL, Fee: trade.Fee, Reason: trade.Reason})
	}
}
