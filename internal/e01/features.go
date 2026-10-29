package e01

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
)

// FeatureConfig は E01 の確定足・短期中値の設定です。既定値は Python 実装に一致します。
type FeatureConfig struct {
	ATRPeriod       int     `json:"atr_period" toml:"atr_period"`
	ATRMinBars      int     `json:"atr_min_bars" toml:"atr_min_bars"`
	EMAFast         int     `json:"ema_fast" toml:"ema_fast"`
	EMASlow         int     `json:"ema_slow" toml:"ema_slow"`
	SlopeBars       int     `json:"slope_bars" toml:"slope_bars"`
	GapATR          float64 `json:"gap_atr" toml:"gap_atr"`
	SlopeATR        float64 `json:"slope_atr" toml:"slope_atr"`
	CandleDelay     float64 `json:"candle_delay_seconds" toml:"candle_delay_seconds"`
	PeerDelay       float64 `json:"peer_delay_seconds" toml:"peer_delay_seconds"`
	Warmup          float64 `json:"warmup_seconds" toml:"warmup_seconds"`
	SupportATR      float64 `json:"support_atr" toml:"support_atr"`
	SupportExpiry   float64 `json:"support_expiry_seconds" toml:"support_expiry_seconds"`
	AdverseLongATR  float64 `json:"adverse_long_atr" toml:"adverse_long_atr"`
	AdverseShortATR float64 `json:"adverse_short_atr" toml:"adverse_short_atr"`
}

// DefaultFeatureConfig は E01 の既存の公開待機と指標設定を返します。
//
// 引数: なし。返り値: FeatureConfig は既定条件です。
func DefaultFeatureConfig() FeatureConfig {
	return FeatureConfig{ATRPeriod: 12, ATRMinBars: 3, EMAFast: 15, EMASlow: 60, SlopeBars: 3, GapATR: .15, SlopeATR: .02, CandleDelay: 60, PeerDelay: 60, Warmup: 1200, SupportATR: .05, SupportExpiry: 120, AdverseLongATR: .25, AdverseShortATR: .1}
}

// Candle は controller の確定足です。Received は実受信時計で上書きします。
type Candle struct {
	Time     float64 `json:"time"`
	Open     float64 `json:"open"`
	High     float64 `json:"high"`
	Low      float64 `json:"low"`
	Close    float64 `json:"close"`
	Closed   bool    `json:"closed"`
	Received float64 `json:"received"`
}

// Book は controller の最良気配と現在価格を表します。欠測値はゼロとします。
type Book struct {
	QuoteTime float64
	Price     float64
	Bid       float64
	Ask       float64
	BidQty    float64
	AskQty    float64
}

// Indicators は現在までに実際に利用可能になった指標です。ATR=0 は暖機未完了です。
type Indicators struct {
	ATR              float64 `json:"atr"`
	MainDirection    int     `json:"main_direction"`
	PeerDirection    int     `json:"peer_direction"`
	AllowedDirection int     `json:"allowed_direction"`
	EMAFast          float64 `json:"ema_fast"`
	EMASlow          float64 `json:"ema_slow"`
	MainAvailable    float64 `json:"main_available"`
	MainNominal      float64 `json:"main_nominal"`
	PeerAvailable    float64 `json:"peer_available"`
	PeerNominal      float64 `json:"peer_nominal"`
	ATRAvailable     float64 `json:"atr_available"`
	ATRNominal       float64 `json:"atr_nominal"`
}

// Observation は一件の受信板に対応する E01 入力と監査情報です。
type Observation struct {
	Indicators
	Timestamp     float64 `json:"timestamp"`
	Price         float64 `json:"price"`
	QuoteTime     float64 `json:"quote_time"`
	InputIndex    int64   `json:"input_index"`
	Direction     int     `json:"direction"`
	Adverse       int     `json:"adverse"`
	AdverseSource int64   `json:"adverse_source"`
	Quality       string  `json:"quality"`
	Bid           float64 `json:"bid"`
	Ask           float64 `json:"ask"`
}

// Tick は特徴量を注文器の入力へ変換します。発注価格は対象商品の観測値に差し替えできます。
//
// 引数: allow(bool) は新規許可。返り値: Tick は戦略入力です。
func (o Observation) Tick(allow bool) Tick {
	return Tick{Time: o.Timestamp, Price: o.Price, ATR: o.ATR, Direction: o.Direction, Adverse: o.Adverse, AdverseSource: o.AdverseSource, AllowEntry: allow, InputIndex: o.InputIndex}
}

type quoteBlock struct {
	Time float64
	Mid  *float64
}
type midEvent struct {
	Available, SourceTime float64
	Mid                   *float64
	Usable, Gap           bool
	Index                 int64
}
type market struct {
	Bars                                   map[int][]Candle
	InputIndex                             int64
	Block                                  *quoteBlock
	Events                                 []midEvent
	Direction                              int
	Supported, LastRelease                 *float64
	Adverse                                int
	AdverseSource                          int64
	AdverseAt, LastQuoteTime, LastReceived *float64
	LastPrice, LastBid, LastAsk            float64
	LastBookValid                          bool
}
type atrRow struct{ Nominal, Available, Value float64 }
type mainRow struct {
	Nominal, Available     float64
	Direction              int
	Gap, Slope, Fast, Slow float64
}
type featureModel struct {
	ATR  []atrRow
	Main []mainRow
}

// Features は二市場の指標を、実受信時計と公開待機を守って更新します。
type Features struct {
	Version   int                `json:"version"`
	SessionID string             `json:"session_id"`
	Start     float64            `json:"start"`
	Config    FeatureConfig      `json:"config"`
	Ticks     map[string]float64 `json:"ticks"`
	Markets   map[string]*market `json:"markets"`
	models    map[string]featureModel
}

// NewFeatures は外部指定の二市場呼値で特徴量器を作ります。
//
// 引数: sessionID(string)、start(float64) はセッション、ticks(map[string]float64) は NK225/TOPIX の呼値、cfg(FeatureConfig) は指標設定。
// 返り値: *Features は特徴量器、error は設定不整合です。
func NewFeatures(sessionID string, start float64, ticks map[string]float64, cfg FeatureConfig) (*Features, error) {
	if sessionID == "" || !finite(start) || cfg.ATRPeriod < 1 || cfg.ATRMinBars < 1 || cfg.EMAFast < 1 || cfg.EMASlow < cfg.EMAFast || cfg.SlopeBars < 1 {
		return nil, errors.New("特徴量の期間設定が不正です")
	}
	for _, v := range []float64{cfg.GapATR, cfg.SlopeATR, cfg.CandleDelay, cfg.PeerDelay, cfg.Warmup, cfg.SupportATR, cfg.SupportExpiry, cfg.AdverseLongATR, cfg.AdverseShortATR} {
		if !finite(v) || v < 0 {
			return nil, errors.New("特徴量の係数は非負の有限値が必要です")
		}
	}
	f := &Features{Version: 1, SessionID: sessionID, Start: start, Config: cfg, Ticks: map[string]float64{}, Markets: map[string]*market{}, models: map[string]featureModel{}}
	for _, symbol := range []string{"NK225", "TOPIX"} {
		tick := ticks[symbol]
		if !finite(tick) || tick <= 0 {
			return nil, errors.New("二市場の呼値が必要です")
		}
		f.Ticks[symbol] = tick
		f.Markets[symbol] = &market{Bars: map[int][]Candle{60: {}, 300: {}}, InputIndex: -1, AdverseSource: -1}
	}
	return f, nil
}

// DumpState は未公開中値を含む再開用状態を保存します。
//
// 引数: なし。返り値: []byte は JSON、error は符号化エラーです。
func (f *Features) DumpState() ([]byte, error) { return json.Marshal(f) }

// RestoreFeatures は保存状態を復元します。
//
// 引数: data([]byte) は DumpState の結果。返り値: *Features は復元状態、error は不整合です。
func RestoreFeatures(data []byte) (*Features, error) {
	var f Features
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Version != 1 {
		return nil, errors.New("特徴量保存形式が非対応です")
	}
	if _, err := NewFeatures(f.SessionID, f.Start, f.Ticks, f.Config); err != nil {
		return nil, err
	}
	for _, symbol := range []string{"NK225", "TOPIX"} {
		if f.Markets[symbol] == nil || f.Markets[symbol].Bars == nil {
			return nil, errors.New("特徴量保存状態の市場が不足しています")
		}
	}
	f.models = map[string]featureModel{}
	return &f, nil
}

// ResetShortHistory は長い中断後の短期根拠だけを破棄します。
//
// 引数: symbol(string) は対象、空文字なら二市場。返り値: error は不明な市場です。
func (f *Features) ResetShortHistory(symbol string) error {
	symbols := []string{symbol}
	if symbol == "" {
		symbols = []string{"NK225", "TOPIX"}
	}
	for _, s := range symbols {
		m := f.Markets[s]
		if m == nil {
			return errors.New("不明な参照市場です")
		}
		m.Block = nil
		m.Supported = nil
		m.LastRelease = nil
		m.AdverseAt = nil
		m.Events = nil
		m.Direction = 0
		m.Adverse = 0
		m.AdverseSource = -1
	}
	return nil
}

// AddCandles は確定足を保存し、未確定足・未来足・取得済み足の書換えを拒みます。
//
// 引数: symbol(string)、interval(int) は60/300秒、rows([]Candle)、received(float64) は実受信時計。
// 返り値: int は追加数、error は入力異常です。異常時は一括入力の全体を反映しません。
func (f *Features) AddCandles(symbol string, interval int, rows []Candle, received float64) (int, error) {
	m := f.Markets[symbol]
	if m == nil || (interval != 60 && interval != 300) || !finite(received) {
		return 0, errors.New("足の市場・期間・受信時計が不正です")
	}
	known := map[float64]Candle{}
	for _, b := range m.Bars[interval] {
		known[b.Time] = b
	}
	pending := []Candle{}
	for _, raw := range rows {
		if !raw.Closed {
			continue
		}
		if !finite(raw.Time) {
			return 0, errors.New("確定足の時計が不正です")
		}
		if raw.Time < f.Start {
			continue
		}
		for _, v := range []float64{raw.Open, raw.High, raw.Low, raw.Close} {
			if !finite(v) || v <= 0 {
				return 0, errors.New("確定足の価格が不正です")
			}
		}
		if raw.High < math.Max(raw.Open, math.Max(raw.Low, raw.Close)) || raw.Low > math.Min(raw.Open, math.Min(raw.High, raw.Close)) {
			return 0, errors.New("確定足の高安関係が不正です")
		}
		if raw.Time+float64(interval) > received+5 {
			return 0, errors.New("実受信より未来の確定足です")
		}
		if old, ok := known[raw.Time]; ok {
			if old.Open != raw.Open || old.High != raw.High || old.Low != raw.Low || old.Close != raw.Close {
				return 0, errors.New("取得済みの確定足が変更されました")
			}
			continue
		}
		raw.Received = received
		known[raw.Time] = raw
		pending = append(pending, raw)
	}
	if len(pending) > 0 {
		m.Bars[interval] = append(m.Bars[interval], pending...)
		sort.Slice(m.Bars[interval], func(i, j int) bool { return m.Bars[interval][i].Time < m.Bars[interval][j].Time })
		delete(f.models, symbol)
	}
	return len(pending), nil
}

// asofATR は指定時点以前の最後の ATR を探します。
//
// 引数: rows([]atrRow)、clock(float64)、nominal(bool) は名目公開時刻の指定。返り値: *atrRow は該当行、未公開なら nil です。
func asofATR(rows []atrRow, clock float64, nominal bool) *atrRow {
	i := sort.Search(len(rows), func(i int) bool {
		v := rows[i].Available
		if nominal {
			v = rows[i].Nominal
		}
		return v > clock
	}) - 1
	if i < 0 {
		return nil
	}
	return &rows[i]
}

// asofMain は実際に利用可能な最後の主方向を探します。
//
// 引数: rows([]mainRow)、clock(float64)。返り値: *mainRow は該当行、未公開なら nil です。
func asofMain(rows []mainRow, clock float64) *mainRow {
	i := sort.Search(len(rows), func(i int) bool { return rows[i].Available > clock }) - 1
	if i < 0 {
		return nil
	}
	return &rows[i]
}

// model は確定足から ATR と EMA 方向の公開時系列を作ります。
//
// 引数: symbol(string) は市場。返り値: featureModel は公開時計付きの計算結果です。
func (f *Features) model(symbol string) featureModel {
	if m, ok := f.models[symbol]; ok {
		return m
	}
	market := f.Markets[symbol]
	cfg := f.Config
	result := featureModel{}
	previous, running := 0.0, 0.0
	receivedPrefix := f.Start
	for i, b := range market.Bars[300] {
		tr := b.High - b.Low
		if i > 0 {
			tr = math.Max(tr, math.Max(math.Abs(b.High-previous), math.Abs(b.Low-previous)))
		}
		if i == 0 {
			running = tr
		} else {
			alpha := 1 / float64(cfg.ATRPeriod)
			running = running*(1-alpha) + tr*alpha
		}
		previous = b.Close
		receivedPrefix = math.Max(receivedPrefix, b.Received)
		nominal := b.Time + 300 + cfg.CandleDelay
		value := 0.0
		if i >= cfg.ATRMinBars-1 {
			value = math.Max(running, 2*f.Ticks[symbol])
		}
		result.ATR = append(result.ATR, atrRow{Nominal: nominal, Available: math.Max(nominal, receivedPrefix), Value: value})
	}
	// ----------------------------------------
	// 1分足ごとの方向は、その足の名目公開時刻までの ATR で計算します。
	fast, slow := 0.0, 0.0
	slowHistory := []float64{}
	direction := 0
	receivedPrefix = f.Start
	availablePrefix := f.Start
	for i, b := range market.Bars[60] {
		if i == 0 {
			fast = b.Close
			slow = b.Close
		} else {
			af := 2 / float64(cfg.EMAFast+1)
			as := 2 / float64(cfg.EMASlow+1)
			fast = fast*(1-af) + b.Close*af
			slow = slow*(1-as) + b.Close*as
		}
		slowHistory = append(slowHistory, slow)
		receivedPrefix = math.Max(receivedPrefix, b.Received)
		nominal := b.Time + 60 + cfg.CandleDelay
		ar := asofATR(result.ATR, nominal, true)
		atr := 0.0
		atrAvailable := f.Start
		if ar != nil {
			atr = ar.Value
			atrAvailable = ar.Available
		}
		availablePrefix = math.Max(availablePrefix, math.Max(nominal, math.Max(receivedPrefix, atrAvailable)))
		gap, slope := 0.0, 0.0
		gapValid := atr > 0 && i >= cfg.EMASlow-1
		slopeValid := atr > 0 && i-cfg.SlopeBars >= cfg.EMASlow-1
		if gapValid {
			gap = (fast - slow) / atr
		}
		if slopeValid {
			slope = (slow - slowHistory[i-cfg.SlopeBars]) / atr
		}
		if !gapValid {
			direction = 0
		} else {
			if float64(direction)*gap < 0 {
				direction = 0
			}
			if slopeValid {
				if gap > cfg.GapATR && slope > cfg.SlopeATR {
					direction = 1
				} else if gap < -cfg.GapATR && slope < -cfg.SlopeATR {
					direction = -1
				}
			}
		}
		fr, sr := 0.0, 0.0
		if i >= cfg.EMAFast-1 {
			fr = fast
		}
		if i >= cfg.EMASlow-1 {
			sr = slow
		}
		result.Main = append(result.Main, mainRow{Nominal: nominal, Available: availablePrefix, Direction: direction, Gap: gap, Slope: slope, Fast: fr, Slow: sr})
	}
	f.models[symbol] = result
	return result
}

// Snapshot は指定時計までに利用可能になった自市場・相手市場の指標を返します。
//
// 引数: symbol(string)、at(float64) は判断時計。返り値: Indicators は指標、error は不明な市場等です。
func (f *Features) Snapshot(symbol string, at float64) (Indicators, error) {
	if f.Markets[symbol] == nil || !finite(at) {
		return Indicators{}, errors.New("指標の市場又は時計が不正です")
	}
	model := f.model(symbol)
	own := asofMain(model.Main, at)
	ar := asofATR(model.ATR, at, false)
	peerSymbol := "NK225"
	if symbol == "NK225" {
		peerSymbol = "TOPIX"
	}
	peer := asofMain(f.model(peerSymbol).Main, at-f.Config.PeerDelay)
	r := Indicators{}
	if own != nil {
		r.MainDirection = own.Direction
		r.EMAFast = own.Fast
		r.EMASlow = own.Slow
		r.MainAvailable = own.Available
		r.MainNominal = own.Nominal
	}
	if ar != nil {
		r.ATR = ar.Value
		r.ATRAvailable = ar.Available
		r.ATRNominal = ar.Nominal
	}
	if peer != nil {
		r.PeerDirection = peer.Direction
		r.PeerAvailable = peer.Available + f.Config.PeerDelay
		r.PeerNominal = peer.Nominal + f.Config.PeerDelay
	}
	if r.MainDirection == r.PeerDirection {
		r.AllowedDirection = r.MainDirection
	}
	if at < f.Start+f.Config.Warmup || r.ATR <= 0 {
		r.AllowedDirection = 0
	}
	return r, nil
}

// RearmDirection は持続した旧方向ではなく当時の両市場 EMA の再成立を返します。
//
// 引数: symbol(string) は自己市場、at(float64) は判断時計。
// 返り値: int は両方のギャップと傾きが閾値を超えた方向、未成立なら 0 です。
func (f *Features) RearmDirection(symbol string, at float64) int {
	peer := "NK225"
	if symbol == "NK225" {
		peer = "TOPIX"
	}
	own := asofMain(f.model(symbol).Main, at)
	other := asofMain(f.model(peer).Main, at-f.Config.PeerDelay)
	if own == nil || other == nil {
		return 0
	}
	for _, side := range []int{1, -1} {
		d := float64(side)
		if d*own.Gap > f.Config.GapATR && d*other.Gap > f.Config.GapATR &&
			d*own.Slope > f.Config.SlopeATR && d*other.Slope > f.Config.SlopeATR {
			return side
		}
	}
	return 0
}

// asofMid は中値公開系列から過去の基準を探します。
//
// 引数: rows([]midEvent)、clock(float64)。返り値: *midEvent は該当基準、ない場合 nil です。
func asofMid(rows []midEvent, clock float64) *midEvent {
	i := sort.Search(len(rows), func(i int) bool { return rows[i].Available > clock }) - 1
	if i < 0 {
		return nil
	}
	return &rows[i]
}

// release は前の取引時刻の最終中値を公開して、支持と逆行条件を更新します。
//
// 引数: symbol(string)、received(float64)、source(quoteBlock)、index(int64)、ind(Indicators)。返り値: なし。
func (f *Features) release(symbol string, received float64, source quoteBlock, index int64, ind Indicators) {
	m := f.Markets[symbol]
	usable := source.Mid != nil && received-source.Time >= 0 && received-source.Time <= 60
	gap := len(m.Events) > 0 && received-m.Events[len(m.Events)-1].Available > 60
	event := midEvent{Available: received, SourceTime: source.Time, Mid: source.Mid, Usable: usable, Gap: gap, Index: index}
	m.Events = append(m.Events, event)
	for len(m.Events) > 1 && m.Events[1].Available < received-240 {
		m.Events = m.Events[1:]
	}
	if len(m.Events) > 4096 {
		m.Events = m.Events[len(m.Events)-4096:]
	}
	atr := ind.ATR
	longRef := asofMid(m.Events, received-120)
	shortRef := asofMid(m.Events, received-30)
	allowed := ind.AllowedDirection
	if m.LastRelease == nil || received-*m.LastRelease > f.Config.SupportExpiry || m.Supported == nil || received-*m.Supported > f.Config.SupportExpiry || m.Direction != allowed {
		m.Direction = 0
	}
	supportValid := usable && shortRef != nil && shortRef.Usable && shortRef.Mid != nil && received-30-shortRef.Available <= 30
	for _, v := range m.Events {
		if v.Available > received-30 && (!v.Usable || v.Gap) {
			supportValid = false
		}
	}
	if !usable || gap {
		m.Direction = 0
		m.Supported = nil
	}
	if supportValid && atr > 0 {
		change := (*event.Mid - *shortRef.Mid) / atr
		if float64(allowed)*change > f.Config.SupportATR {
			m.Direction = allowed
			m.Supported = pointer(received)
		}
	}
	m.LastRelease = pointer(received)
	// ----------------------------------------
	// 120秒と30秒の価格反応が同方向で閾値を超える場合だけ、逆行候補とします。
	longFresh := longRef != nil && received-120-longRef.Available <= 60
	shortFresh := shortRef != nil && received-30-shortRef.Available <= 60
	valid := usable && atr > 0 && longFresh && shortFresh && longRef.Usable && longRef.Mid != nil && shortRef.Mid != nil
	windowCount := 0
	for _, v := range m.Events {
		if v.Available > received-120 {
			windowCount++
			if !v.Usable || v.Gap {
				valid = false
			}
		}
	}
	valid = valid && windowCount >= 5
	candidate := 0
	if valid {
		longChange := *event.Mid - *longRef.Mid
		shortChange := *event.Mid - *shortRef.Mid
		longLimit := math.Max(f.Config.AdverseLongATR*atr, 2*f.Ticks[symbol])
		shortLimit := math.Max(f.Config.AdverseShortATR*atr, f.Ticks[symbol])
		for _, side := range []int{-1, 1} {
			if float64(side)*longChange >= longLimit && float64(side)*shortChange >= shortLimit {
				candidate = side
			}
		}
	}
	m.Adverse = candidate
	m.AdverseSource = index
	m.AdverseAt = pointer(received)
}

// OnBook は一件の生板から、現在以降だけで使用できる特徴量を返します。
//
// 引数: symbol(string)、book(Book)、received(float64) は実受信時計。
// 返り値: Observation は戦略入力、error は価格・時計の異常です。短い通信断の扱いは運転側で制御します。
func (f *Features) OnBook(symbol string, book Book, received float64) (Observation, error) {
	m := f.Markets[symbol]
	if m == nil {
		return Observation{}, errors.New("不明な参照市場です")
	}
	quoted := book.QuoteTime
	if !finite(received) || !finite(quoted) || !finite(book.Price) || book.Price <= 0 {
		return Observation{}, errors.New("現在価格又は時計が不正です")
	}
	if quoted > received+5 {
		return Observation{}, errors.New("未来の価格時刻です")
	}
	if m.LastQuoteTime != nil && quoted < *m.LastQuoteTime {
		return Observation{}, errors.New("価格時刻が逆行しました")
	}
	if m.LastReceived != nil && received < *m.LastReceived {
		return Observation{}, errors.New("受信時計が逆行しました")
	}
	valid := finite(book.Bid) && finite(book.Ask) && book.Bid > 0 && book.Bid < book.Ask && finite(book.BidQty) && finite(book.AskQty) && book.BidQty > 0 && book.AskQty > 0
	ind, err := f.Snapshot(symbol, received)
	if err != nil {
		return Observation{}, err
	}
	index := m.InputIndex + 1
	if m.Block == nil || quoted != m.Block.Time {
		if m.Block != nil && m.Block.Mid != nil && valid {
			f.release(symbol, received, *m.Block, index, ind)
		}
		m.Block = &quoteBlock{Time: quoted}
	}
	if valid {
		m.Block.Mid = pointer((book.Bid + book.Ask) / 2)
	}
	// ----------------------------------------
	// 公開板イベントがない同一秒でも、支持期限と主方向撤回を検査します。
	if m.Supported == nil || received-*m.Supported > f.Config.SupportExpiry || m.Direction != ind.AllowedDirection {
		m.Direction = 0
	}
	adverseValid := m.AdverseAt != nil && received-*m.AdverseAt <= 60
	m.InputIndex = index
	m.LastQuoteTime = pointer(quoted)
	m.LastReceived = pointer(received)
	m.LastPrice = book.Price
	m.LastBid = book.Bid
	m.LastAsk = book.Ask
	m.LastBookValid = valid
	quality := "有効"
	if !valid {
		quality = "板不正"
	}
	if ind.ATR == 0 {
		quality = "ATR暖機中"
	} else if ind.MainDirection == 0 || ind.PeerDirection == 0 {
		quality = "方向未確定"
	}
	r := Observation{Indicators: ind, Timestamp: received, Price: book.Price, QuoteTime: quoted, InputIndex: index, AdverseSource: -1, Quality: quality, Bid: book.Bid, Ask: book.Ask}
	if valid {
		r.Direction = m.Direction
		if adverseValid {
			r.Adverse = m.Adverse
			r.AdverseSource = m.AdverseSource
		}
	}
	return r, nil
}

// Current は新しい板を追加せず、現在時刻で有効な短期根拠を返します。
//
// 引数: symbol(string) は参照市場、at(float64) は判断時刻。
// 返り値: Observation は既知の根拠、error は市場・時計異常です。InputIndex は参照市場の連番であり、別商品の観測連番ではありません。
func (f *Features) Current(symbol string, at float64) (Observation, error) {
	ind, err := f.Snapshot(symbol, at)
	if err != nil {
		return Observation{}, err
	}
	m := f.Markets[symbol]
	if m.LastReceived != nil && at < *m.LastReceived {
		return Observation{}, errors.New("短期特徴量を最新受信より過去の時計へ公開できません")
	}
	r := Observation{Indicators: ind, Timestamp: at, Price: m.LastPrice, InputIndex: m.InputIndex, AdverseSource: -1, Bid: m.LastBid, Ask: m.LastAsk, Quality: "方向未確定"}
	if m.LastQuoteTime != nil {
		r.QuoteTime = *m.LastQuoteTime
	}
	valid := m.LastBookValid && m.LastReceived != nil && at-*m.LastReceived <= 60
	if valid && m.Supported != nil && at-*m.Supported <= f.Config.SupportExpiry && m.Direction == ind.AllowedDirection {
		r.Direction = m.Direction
	}
	if valid && m.AdverseAt != nil && at-*m.AdverseAt <= 60 {
		r.Adverse = m.Adverse
		r.AdverseSource = m.AdverseSource
	}
	if ind.ATR == 0 {
		r.Quality = "ATR暖機中"
	} else if !valid {
		r.Quality = "板待機"
	} else if r.Direction != 0 {
		r.Quality = "有効"
	}
	return r, nil
}
