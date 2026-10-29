// Package strategy は00131の昼夜手法と00143の独立担当を実約定から管理します。
package strategy

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/e01"
)

// Session は外部で確定したセッションと市場時刻です。全時計は UNIX 秒です。
type Session struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Start      float64 `json:"start"`
	ForceClose float64 `json:"force_close"`
	End        float64 `json:"end"`
	USOpen     float64 `json:"us_open"`
	ExtraAt    float64 `json:"extra_at"`
	// NightCoreStart はP05の独立仮想主担当だけの最早準備時刻です。0なら既存の米国開始後準備を用います。
	NightCoreStart float64 `json:"night_core_start,omitempty"`
}

// Parameters は研究既定値を変更できる設定です。商品規格は含みません。
type Parameters struct {
	Method   string            `json:"method,omitempty" toml:"-"`
	Features e01.FeatureConfig `json:"features" toml:"features"`
	Day      DayParameters     `json:"day" toml:"day"`
	Night    NightParameters   `json:"night" toml:"night"`
	Extra    ExtraParameters   `json:"extra" toml:"extra"`
}

type DayParameters struct {
	WarmupSeconds      float64 `json:"warmup_seconds" toml:"warmup_seconds"`
	CooldownSeconds    float64 `json:"cooldown_seconds" toml:"cooldown_seconds"`
	PendingSeconds     float64 `json:"pending_seconds" toml:"pending_seconds"`
	EntryCutoffSeconds float64 `json:"entry_cutoff_seconds" toml:"entry_cutoff_seconds"`
	MaxHoldSeconds     float64 `json:"max_hold_seconds" toml:"max_hold_seconds"`
	EntryATR           float64 `json:"entry_atr" toml:"entry_atr"`
	NormalRiskATR      float64 `json:"normal_risk_atr" toml:"normal_risk_atr"`
	TightRiskATR       float64 `json:"tight_risk_atr" toml:"tight_risk_atr"`
	TrailArmATR        float64 `json:"trail_arm_atr" toml:"trail_arm_atr"`
	NormalTrailATR     float64 `json:"normal_trail_atr" toml:"normal_trail_atr"`
	TightArmATR        float64 `json:"tight_arm_atr" toml:"tight_arm_atr"`
	TightTrailATR      float64 `json:"tight_trail_atr" toml:"tight_trail_atr"`
	SplitATR           float64 `json:"split_atr" toml:"split_atr"`
	RiskFloor          float64 `json:"risk_floor" toml:"risk_floor"`
	WeakSupport        float64 `json:"weak_support" toml:"weak_support"`
	QuoteSpreadATR     float64 `json:"quote_spread_atr" toml:"quote_spread_atr"`
	QuoteDistanceATR   float64 `json:"quote_distance_atr" toml:"quote_distance_atr"`
	WithdrawalATR      float64 `json:"withdrawal_atr" toml:"withdrawal_atr"`
	PersistenceSeconds float64 `json:"persistence_seconds" toml:"persistence_seconds"`
	MaximumGapSeconds  float64 `json:"maximum_gap_seconds" toml:"maximum_gap_seconds"`
}

type NightParameters struct {
	Retest                 bool    `json:"retest" toml:"retest"`
	StopR                  float64 `json:"stop_r" toml:"stop_r"`
	TrailStartR            float64 `json:"trail_start_r" toml:"trail_start_r"`
	TrailWidthR            float64 `json:"trail_width_r" toml:"trail_width_r"`
	CooldownSeconds        float64 `json:"cooldown_seconds" toml:"cooldown_seconds"`
	QuietSeconds           float64 `json:"quiet_seconds" toml:"quiet_seconds"`
	PostOpenPrepareSeconds float64 `json:"post_open_prepare_seconds" toml:"post_open_prepare_seconds"`
	MinimumTicks           float64 `json:"minimum_ticks" toml:"minimum_ticks"`
	Efficiency             float64 `json:"efficiency" toml:"efficiency"`
	QuoteSpreadR           float64 `json:"quote_spread_r" toml:"quote_spread_r"`
	QuoteDistanceR         float64 `json:"quote_distance_r" toml:"quote_distance_r"`
	ArmLifetimeSeconds     float64 `json:"arm_lifetime_seconds" toml:"arm_lifetime_seconds"`
	ConfirmSeconds         float64 `json:"confirm_seconds" toml:"confirm_seconds"`
	BoundaryBreakR         float64 `json:"boundary_break_r" toml:"boundary_break_r"`
	BounceR                float64 `json:"bounce_r" toml:"bounce_r"`
	ProgressR              float64 `json:"progress_r" toml:"progress_r"`
	RetestProgressR        float64 `json:"retest_progress_r" toml:"retest_progress_r"`
	RetestReturnR          float64 `json:"retest_return_r" toml:"retest_return_r"`
	LongSupportR           float64 `json:"long_support_r" toml:"long_support_r"`
	LongOppositionR        float64 `json:"long_opposition_r" toml:"long_opposition_r"`
}

type ExtraParameters struct {
	Style              int     `json:"style" toml:"style"`
	StopR              float64 `json:"stop_r" toml:"stop_r"`
	TrailStartR        float64 `json:"trail_start_r" toml:"trail_start_r"`
	TrailWidthR        float64 `json:"trail_width_r" toml:"trail_width_r"`
	MinimumTicks       float64 `json:"minimum_ticks" toml:"minimum_ticks"`
	BandFraction       float64 `json:"band_fraction" toml:"band_fraction"`
	QuoteSpreadR       float64 `json:"quote_spread_r" toml:"quote_spread_r"`
	EntryCutoffSeconds float64 `json:"entry_cutoff_seconds" toml:"entry_cutoff_seconds"`
	BudgetTicks        float64 `json:"budget_ticks" toml:"budget_ticks"`
}

// Config は実商品とラージ参照市場を分けた設定です。
type Config struct {
	RuleExpressions map[string]string `json:"rule_expressions,omitempty" toml:"-"`
	// Namespace は保存先ごとの永続運転 ID です。空文字は既存状態との互換用です。
	Namespace        string     `json:"namespace,omitempty" toml:"namespace"`
	Reference        string     `json:"reference" toml:"reference"`
	Product          string     `json:"product" toml:"product"`
	CoreQuantity     int        `json:"core_quantity" toml:"core_quantity"`
	ExtraMaxQuantity int        `json:"extra_max_quantity" toml:"extra_max_quantity"`
	Cap              int        `json:"cap" toml:"cap"`
	LargeTick        float64    `json:"large_tick" toml:"large_tick"`
	PointValue       float64    `json:"point_value" toml:"point_value"`
	Parameters       Parameters `json:"parameters" toml:"parameters"`
}

// DefaultParameters は選定済み担当の既定設定を返します。
//
// 引数: reference(string) は NK225/TOPIX、kind(string) は Day/Night。
// 返り値: Parameters は 00131 の選定設定です。
func DefaultParameters(reference, kind string) Parameters {
	p := Parameters{Features: e01.DefaultFeatureConfig(),
		Day:   DayParameters{1200, 300, 300, 300, 21600, .25, 1, .75, 1.5, 3, 3, .75, 2, .75, -.05, .5, .5, .25, 120, 15},
		Night: NightParameters{false, 1, 1, .75, 300, 900, 1320, 4, .35, .25, .5, 1800, 60, .25, .25, .25, .75, .25, .05, .25},
		Extra: ExtraParameters{1, 1, 1, .5, 8, .5, .5, 600, 0},
	}
	if reference == "NK225" {
		p.Day.PersistenceSeconds = 60
		p.Night.Retest = true
	}
	if kind == "Night" {
		if reference == "TOPIX" {
			p.Extra.Style = 0
			p.Extra.TrailStartR = 1.25
			p.Extra.TrailWidthR = .75
		} else {
			p.Extra.TrailStartR = .75
			p.Extra.BudgetTicks = 12
		}
	}
	return p
}

// Validate は設定の有限性と枚数制約を検査します。
//
// 引数: なし。返り値: error は矛盾した設定、正常なら nil です。
func (c Config) Validate() error {
	if (c.Reference != "TOPIX" && c.Reference != "NK225") || c.Product == "" || c.CoreQuantity < 1 || c.ExtraMaxQuantity < 1 || c.Cap < c.CoreQuantity || c.Cap < c.ExtraMaxQuantity || c.LargeTick <= 0 || c.PointValue <= 0 || !finite(c.LargeTick) || !finite(c.PointValue) {
		return errors.New("参照市場・商品・枚数上限・規格が不正です")
	}
	if _, err := json.Marshal(c.Parameters); err != nil {
		return err
	}
	d, n, x := c.Parameters.Day, c.Parameters.Night, c.Parameters.Extra
	for _, v := range []float64{d.WarmupSeconds, d.CooldownSeconds, d.PendingSeconds, d.EntryCutoffSeconds, d.MaxHoldSeconds, d.EntryATR, d.NormalRiskATR, d.TightRiskATR, d.TrailArmATR, d.NormalTrailATR, d.TightArmATR, d.TightTrailATR, d.SplitATR, d.RiskFloor, d.QuoteSpreadATR, d.QuoteDistanceATR, d.WithdrawalATR, d.PersistenceSeconds, d.MaximumGapSeconds, n.StopR, n.TrailStartR, n.TrailWidthR, n.CooldownSeconds, n.QuietSeconds, n.PostOpenPrepareSeconds, n.MinimumTicks, n.Efficiency, n.QuoteSpreadR, n.QuoteDistanceR, n.ArmLifetimeSeconds, n.ConfirmSeconds, n.BoundaryBreakR, n.BounceR, n.ProgressR, n.RetestProgressR, n.RetestReturnR, n.LongSupportR, n.LongOppositionR, x.StopR, x.TrailStartR, x.TrailWidthR, x.MinimumTicks, x.BandFraction, x.QuoteSpreadR, x.EntryCutoffSeconds, x.BudgetTicks} {
		if !finite(v) || v < 0 {
			return errors.New("戦略係数は非負の有限値が必要です")
		}
	}
	if d.RiskFloor <= 0 || d.RiskFloor > 1 || n.StopR <= 0 || x.StopR <= 0 || x.Style < 0 || x.Style > 1 {
		return errors.New("損失幅・方向方式の設定が不正です")
	}
	return nil
}

// ApplyOverrides は既知の階層キーだけを設定へ適用します。
//
// 引数: p(*Parameters)、values(map[string]float64) は toml タグの二階層名です。
// 返り値: error は未知キー・型不一致です。範囲は Config.Validate が検査します。
func ApplyOverrides(p *Parameters, values map[string]float64) error {
	if p == nil {
		return errors.New("戦略設定がありません")
	}
	for key, value := range values {
		if !finite(value) {
			return errors.New("設定値が有限でありません: " + key)
		}
		parts := strings.Split(key, ".")
		if len(parts) != 2 {
			return errors.New("設定キーは部門.変数です: " + key)
		}
		current := reflect.ValueOf(p).Elem()
		for _, part := range parts {
			t := current.Type()
			found := false
			for i := 0; i < current.NumField(); i++ {
				if t.Field(i).Tag.Get("toml") == part {
					current = current.Field(i)
					found = true
					break
				}
			}
			if !found {
				return errors.New("未知の戦略設定: " + key)
			}
		}
		switch current.Kind() {
		case reflect.Float64:
			current.SetFloat(value)
		case reflect.Int:
			if value != math.Trunc(value) {
				return errors.New("整数設定に小数を指定しました: " + key)
			}
			current.SetInt(int64(value))
		case reflect.Bool:
			if value != 0 && value != 1 {
				return errors.New("真偽設定は 0 または 1 です: " + key)
			}
			current.SetBool(value == 1)
		default:
			return errors.New("数値設定ではありません: " + key)
		}
	}
	return nil
}

// Market は元板時計に基づく二市場比較です。変化量は当該市場 ATR 単位です。
type Market struct {
	Own, Peer, OwnATR, PeerATR                                float64
	Own30, Own120, Peer30, Peer120                            float64
	PeerTime, Own30Time, Peer30Time                           float64
	Known, Own30Known, Own120Known, Peer30Known, Peer120Known bool
}

// History は公開遅延済みの 15 列です。Known が false のとき値を判断に使いません。
type History struct {
	Values [15]float64
	Known  bool
}

// Observation は一件の参照ラージ板から作った因果的特徴です。
type Observation struct {
	Reference                        string
	Time, Received                   float64
	Index                            int64
	Last, Bid, Ask, ATR              float64
	QuoteOK                          bool
	E01                              e01.Observation
	GateDirection                    int
	DayMarket, LastMarket, MidMarket Market
	LastHistory, MidHistory          History
	Research                         ResearchFeatures
	Selected                         SelectedObservation
}

// ResearchBand は公開済みの価格帯です。未知値をゼロの観測と混同しません。
type ResearchBand struct {
	Values [9]float64
	Known  bool
}

// ResearchFeatures は00143の当時特徴と公開20分・40分帯です。
type ResearchFeatures struct {
	MA20           float64
	MA20Known      bool
	Values         [30]float64
	Known          [30]bool
	Band20, Band40 ResearchBand
}

// ResearchEvent は独立した仮想候補から当時届く発注通知です。
type ResearchEvent struct {
	Action                                   string
	Side, Quantity, Reason, Campaign, Ticket int
	Risk, Boundary                           float64
}

// LotTarget は返済する所有建玉IDと数量です。
type LotTarget struct {
	ID       string `json:"id"`
	Quantity int    `json:"quantity"`
}

// Signal は発注依頼です。Quantity は注文枚数、Side は買い 1 / 売り -1 です。
type Signal struct {
	ID          string          `json:"id"`
	Owner       string          `json:"owner"`
	Action      string          `json:"action"`
	Side        int             `json:"side"`
	Quantity    int             `json:"quantity"`
	Time        float64         `json:"time"`
	SourceIndex int64           `json:"source_index"`
	Reason      string          `json:"reason"`
	ReasonCode  int             `json:"reason_code"`
	Risk        float64         `json:"risk"`
	Boundary    float64         `json:"boundary"`
	Campaign    int             `json:"campaign"`
	Evidence    json.RawMessage `json:"evidence"`
	Ticket      int             `json:"ticket,omitempty"`
	Targets     []LotTarget     `json:"targets,omitempty"`
}

// Fill はブローカーが確認した約定です。ExecutionID は再通知の二重反映を防ぎます。
type Fill struct {
	SignalID, ExecutionID string
	Quantity              int
	Price, Time, Fee      float64
	HoldID                string
	Closed                []LotTarget
}

// Position は担当ごとの実約定残玉です。
type Position struct {
	Owner                                                string `json:"owner"`
	Side, Quantity                                       int
	Average, Risk, Boundary, Realized, Fees, Peak, Floor float64
	InitialQuantity, Campaign                            int
	EntryTime                                            float64
	EntryIndex                                           int64
	RiskFraction                                         float64
	AnchorOwn, AnchorPeer, AnchorPeerATR, AnchorPeerTime float64
	AnchorKnown                                          bool
	AnchorInitialized                                    bool
	FailureSince, LastValidTime                          float64
}

// Status は運転表示と再起動照合に使う残玉・待機注文です。
type Status struct {
	Positions                   []Position
	Pending                     []Signal
	CoreEntered, ExtraAttempted bool
}

// finite は有限値を判定します。
//
// 引数: v(float64)。返り値: bool は有限なら true です。
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
