package runner

import (
	"fmt"
	"math"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
)

// marketStatus は約定がない間も確認できる受信・判断・待機理由です。
type marketStatus struct {
	Product                                     string    `json:"product"`
	Reference                                   string    `json:"reference"`
	DryRun                                      bool      `json:"dryrun"`
	PositionSource                              string    `json:"position_source"`
	State                                       string    `json:"state"`
	ReceivedAt                                  time.Time `json:"received_at"`
	ProductReceivedAt                           time.Time `json:"product_received_at"`
	DecisionAt                                  time.Time `json:"decision_at"`
	PriceTime                                   string    `json:"price_time"`
	InputCount                                  uint64    `json:"input_count"`
	DecisionCount                               uint64    `json:"decision_count"`
	ATR                                         float64   `json:"atr"`
	OwnDirection, PeerDirection, ShortDirection int
	OwnBars, PeerBars, RequiredBars             int
	Positions                                   []execution.Holding `json:"positions"`
	Pending                                     bool                `json:"pending"`
	PendingError                                string              `json:"pending_error"`
	Queued                                      int                 `json:"queued"`
	Ignored                                     string              `json:"ignored"`
	ExitDiagnostics                             []map[string]any    `json:"exit_diagnostics,omitempty"`
	UnrealizedYen                               *float64            `json:"unrealized_yen,omitempty"`
	UnrealizedByOwner                           map[string]float64  `json:"unrealized_by_owner,omitempty"`
}

// ignoreReference は採用しなかった板の直近理由を表示用に残します。
//
// 引数: reference/reason(string)。返り値: なし。売買判断や約定状態を変更しません。
func (a *app) ignoreReference(reference, reason string) {
	if a.lastIgnored == nil {
		a.lastIgnored = map[string]string{}
	}
	a.lastIgnored[reference] = reason
}

// accountBlockReason は発注前提の不成立を返します。
//
// 引数: now(time.Time)。返り値: stringは理由、空文字は新鮮な口座・送信可能時間です。
func (a *app) accountBlockReason(now time.Time) string {
	v := a.view
	if now.Sub(a.viewAt) > duration(a.cfg.Runtime.AccountMaxAgeSeconds) {
		return "口座情報の受信待ち"
	}
	if v.Session == nil {
		return "立会情報待ち"
	}
	if !v.Connected {
		return "controllerの上流接続待ち"
	}
	if v.SessionError != "" {
		return "立会情報の取得エラー"
	}
	if v.BrokerError != "" || v.PositionsError != "" {
		return "注文・建玉情報の取得エラー"
	}
	if v.BrokerStale || v.PositionsStale {
		return "注文・建玉情報の更新待ち"
	}
	if now.Before(v.Session.SendFrom) || !now.Before(v.Session.SendUntil) {
		return "注文送信時間外"
	}
	for _, at := range []*time.Time{v.BrokerUpdatedAt, v.PositionsUpdatedAt} {
		if at == nil || now.Sub(*at) > duration(a.cfg.Runtime.AccountMaxAgeSeconds) {
			return "注文・建玉情報の更新待ち"
		}
		if at.After(now.Add(time.Second)) {
			return "口座の取得時計が端末時刻より未来"
		}
	}
	return ""
}

// entryBlockReason はcanEnterと表示で共用する新規禁止理由です。
//
// 引数: m(*marketRuntime)、now(time.Time)。返り値: stringは理由、空文字でも戦略条件成立を意味しません。
func (a *app) entryBlockReason(m *marketRuntime, now time.Time) string {
	if a.stopping {
		return "終了処理中"
	}
	if !a.entriesEnabled() {
		if a.control != nil && a.control.Enabled() {
			return "新規再開待ち・停止時の新規注文を取消照合中"
		}
		return "Web画面で新規OFF・返済監視は継続"
	}
	if a.features == nil {
		return "履歴準備中"
	}
	if reason := a.sessionTransitionReason(m); reason != "" {
		return reason
	}
	if m.Saved.Plan.ID != a.plan.ID {
		return "セッション切替待ち"
	}
	if reason := a.slotBlockReason(m, now); reason != "" {
		return reason
	}
	if !now.Before(m.Saved.Plan.ForceClose) {
		return "新規終了・場内返済"
	}
	if reason := a.accountBlockReason(now); reason != "" {
		return reason
	}
	if !a.view.Session.StartsAt.Equal(m.Saved.Plan.Start) {
		return "口座と履歴の立会照合待ち"
	}
	if a.bundle.One["TOPIX"].Snapshot.Generation != a.generation {
		return "履歴世代の再同期待ち"
	}
	if m.Manager.HasPending() {
		return "発注済み注文の照合待ち"
	}
	if m.OwnershipProblem != "" {
		return "所有建玉と口座の照合待ち"
	}
	for _, ref := range []string{"TOPIX", "NK225"} {
		q, ok := a.validQuote(ref, now)
		if !ok {
			return ref + "の新鮮な正常気配待ち"
		}
		if q.Board.CurrentPrice <= 0 || math.IsNaN(q.Board.CurrentPrice) || math.IsInf(q.Board.CurrentPrice, 0) {
			return ref + "の正常な現在価格待ち"
		}
		at, err := time.Parse(time.RFC3339Nano, q.Board.CurrentPriceTime)
		if err != nil || now.Sub(at) > duration(a.cfg.Runtime.QuoteMaxAgeSeconds) {
			return ref + "の新鮮な価格時計待ち"
		}
		if at.After(now.Add(time.Second)) {
			return ref + "の価格時計が端末時刻より未来"
		}
	}
	product := a.cfg.Markets[m.Reference].Product
	q, ok := a.validQuote(product, now)
	if !ok {
		return product + "の新鮮な正常気配待ち"
	}
	if q.Board.Symbol != m.Saved.Contract.Symbol {
		return "発注商品の限月照合待ち"
	}
	return ""
}

// availableBars は実受信と公開遅延の両方を満たす当場1分足の本数を数えます。
//
// 引数: reference(string)、now(time.Time)、peer(bool)は相手の追加公開待機です。
// 返り値: int。モデルと同じ受信時刻の累積最大を使い、未知の足を数えません。
func (a *app) availableBars(reference string, now time.Time, peer bool) int {
	if a.features == nil || a.features.E01.Markets[reference] == nil {
		return 0
	}
	f := a.features.E01
	delay := 0.0
	if peer {
		delay = f.Config.PeerDelay
	}
	clock := seconds(now) - delay
	received := f.Start
	count := 0
	for _, bar := range f.Markets[reference].Bars[60] {
		received = math.Max(received, bar.Received)
		if bar.Time+60+f.Config.CandleDelay <= clock && received <= clock {
			count++
		}
	}
	return count
}

// statusFor は読取だけで一市場の現在状態を作ります。
//
// 引数: m(*marketRuntime)、now(time.Time)。返り値: marketStatus。
func (a *app) statusFor(m *marketRuntime, now time.Time) marketStatus {
	ref := m.Reference
	product := a.cfg.Markets[ref].Product
	q := a.quotes[ref]
	o, observed := a.observed[ref]
	s := marketStatus{Product: product, Reference: ref, DryRun: a.cfg.Trading.DryRun, PositionSource: "実約定台帳",
		ReceivedAt: q.Received, ProductReceivedAt: a.quotes[product].Received,
		DecisionAt: a.lastHandled[ref], PriceTime: q.Board.CurrentPriceTime, InputCount: a.inputCounts[ref], DecisionCount: a.decisionCounts[ref],
		ATR: o.ATR, OwnDirection: o.E01.MainDirection, PeerDirection: o.E01.PeerDirection, ShortDirection: o.E01.Direction,
		Positions: m.Manager.Positions(), Pending: m.Manager.HasPending(), PendingError: m.Manager.PendingError(), Queued: len(m.Saved.Queue), Ignored: a.lastIgnored[ref]}
	if s.DryRun {
		s.PositionSource = "仮想約定台帳"
	}
	// 定期診断は建玉IDを残し、新規時の大きな根拠JSONは約定ログ側で一度だけ参照します。
	for i := range s.Positions {
		s.Positions[i].Evidence = nil
	}
	if observed && m.Portfolio != nil {
		s.ExitDiagnostics = m.Portfolio.ExitDiagnostics(o)
	}
	if own, ok := a.validQuote(product, now); ok {
		total := 0.
		s.UnrealizedByOwner = map[string]float64{}
		for _, h := range s.Positions {
			gain := own.Board.Buy1.Price - h.Price
			if h.Side == "sell" {
				gain = h.Price - own.Board.Sell1.Price
			}
			amount := gain * float64(h.Quantity) * a.cfg.Products[product].PointValue
			total += amount
			s.UnrealizedByOwner[h.Owner] += amount
		}
		s.UnrealizedYen = &total
	}
	if a.features == nil || m.Portfolio == nil {
		s.State = "履歴準備中"
		return s
	}
	if reason := a.sessionTransitionReason(m); reason != "" {
		s.State = reason
		return s
	}
	if now.Before(m.Saved.Plan.Start) {
		s.State = "立会開始待ち（" + m.Saved.Plan.Start.Format("15:04") + "）"
		return s
	}
	if reason := a.entryBlockReason(m, now); reason != "" {
		s.State = reason
		return s
	}
	if s.Ignored != "" {
		s.State = "板の判断除外：" + s.Ignored
		return s
	}
	if !observed {
		s.State = "当場価格の判断待ち"
		return s
	}
	if s.DecisionAt.IsZero() || now.Sub(s.DecisionAt) > duration(a.cfg.Runtime.QuoteMaxAgeSeconds) {
		s.State = "判断の更新待ち"
		return s
	}
	if len(s.Positions) > 0 {
		s.State = "建玉の継続・返済を監視中"
		if s.DryRun {
			s.State = "仮想建玉の継続・返済を監視中"
		}
		return s
	}
	p := m.Portfolio
	if p.Selected != nil {
		s.State = p.SelectedState()
		return s
	}
	if p.P05 != nil {
		if seconds(now) >= p.Session.USOpen-900 {
			s.State = "P05：米国開始前の終了・所有玉の返済を確認中"
		} else {
			s.State = "P05：両方向の新規と利益玉ごとの追尾を監視中"
		}
		return s
	}
	if p.Research != nil {
		s.State = config.NameOfMethod(ref, p.Research.Method) + "：独立担当ごとの新規・継続・利益予算を監視中"
		return s
	}
	if m.Saved.Plan.Kind == "Day" {
		cfg := p.Config.Parameters.Features
		peer := "NK225"
		if ref == "NK225" {
			peer = "TOPIX"
		}
		s.OwnBars = a.availableBars(ref, now, false)
		s.PeerBars = a.availableBars(peer, now, true)
		s.RequiredBars = cfg.EMASlow + cfg.SlopeBars
		if s.OwnBars < s.RequiredBars || s.PeerBars < s.RequiredBars {
			s.State = fmt.Sprintf("方向指標を準備中（自己%d・相手%d／各%d本）", s.OwnBars, s.PeerBars, s.RequiredBars)
			return s
		}
		if seconds(now) < p.Session.Start+cfg.Warmup || o.ATR <= 0 {
			s.State = "ATR・開始後の準備待ち"
			return s
		}
		if p.Paper != nil && p.Paper.Status().PendingEntry {
			s.State = "方向判定済み・追跡新規の成立待ち"
			return s
		}
		if p.Paper != nil && p.Paper.Normal.Direction != 0 {
			s.State = "基準波の追跡中・次の新規条件待ち"
			return s
		}
		if o.E01.MainDirection == 0 || o.E01.PeerDirection == 0 {
			s.State = "方向の確定待ち"
			return s
		}
		if o.E01.MainDirection != o.E01.PeerDirection {
			s.State = "二市場の方向一致待ち"
			return s
		}
		if o.E01.Direction == 0 || o.GateDirection != o.E01.Direction {
			s.State = "二市場の短期支持待ち"
			return s
		}
		s.State = "反発・気配条件の成立待ち"
		return s
	}
	if a.quiet(m, now) {
		s.State = "主担当は米国開始前後の静観中"
		return s
	}
	if seconds(now) < p.Session.USOpen+p.Config.Parameters.Night.PostOpenPrepareSeconds {
		s.State = "主担当は米国開始後の準備待ち"
		return s
	}
	if o.ATR <= 0 {
		s.State = "ATR準備中"
		return s
	}
	s.State = "夜間の二市場・境界条件待ち"
	return s
}

// sessionTransitionReason は旧場の所有物を残したまま初期化しない理由を明示します。
//
// 引数: m(*marketRuntime)は表示対象です。返り値: stringは切替待ち理由、同一場なら空文字です。
func (a *app) sessionTransitionReason(m *marketRuntime) string {
	if m.Saved.Plan.ID == "" || a.view.Session == nil || a.view.Session.StartsAt.Equal(m.Saved.Plan.Start) {
		return ""
	}
	if m.Manager.HasPending() {
		return "セッション切替待ち・前場の未確定注文を照合中（旧状態を保持）"
	}
	if len(m.Manager.OutstandingFills()) > 0 {
		return "セッション切替待ち・前場の確定約定を反映中（旧状態を保持）"
	}
	if len(m.Manager.Positions()) > 0 {
		return "セッション切替待ち・前場の所有建玉が残存（旧状態を保持）"
	}
	return "セッション切替待ち・当場の履歴を準備中"
}

// clockText は未受信をゼロ時刻として表示しないための表記です。
//
// 引数: at(time.Time)。返り値: string。
func clockText(at time.Time) string {
	if at.IsZero() {
		return "未受信"
	}
	return at.Format("15:04:05")
}

// statusLine は運転モード・発注商品と参照板を区別した表示行を作ります。
//
// 引数: now(time.Time)は表示時刻、s(marketStatus)は読取済みの状態です。
// 返り値: stringは改行付きの表示文。口座照会や注文・建玉の更新は行いません。
func statusLine(now time.Time, s marketStatus) string {
	buy, sell := 0, 0
	for _, h := range s.Positions {
		switch h.Side {
		case "buy":
			buy += h.Quantity
		case "sell":
			sell += h.Quantity
		}
	}
	label := "実約定保有"
	if s.DryRun {
		label = "仮想保有"
	}
	return fmt.Sprintf("%s [稼働/%s] %s（参照%s）: %s / 板%s・発注商品%s・判断%s / 受信%d・判断%d行 / %s 買%d・売%d枚\n",
		now.Format(time.RFC3339), mode(s.DryRun), s.Product, s.Reference, s.State,
		clockText(s.ReceivedAt), clockText(s.ProductReceivedAt), clockText(s.DecisionAt), s.InputCount, s.DecisionCount, label, buy, sell)
}

// reportStatus は同じ稼働情報をJSONLと標準出力に記録します。
//
// 引数: now(time.Time)。返り値: errorは保存失敗です。
func (a *app) reportStatus(now time.Time) error {
	states := map[string]marketStatus{}
	for _, ref := range []string{"TOPIX", "NK225"} {
		if m := a.markets[ref]; m != nil {
			states[ref] = a.statusFor(m, now)
		}
	}
	if err := a.store.Event("稼働", a.plan.ID, "", "", now, map[string]any{"markets": states, "fresh_account": a.accountFresh(now), "preparing": a.features == nil, "stopping": a.stopping}); err != nil {
		return err
	}
	for _, ref := range []string{"TOPIX", "NK225"} {
		s, ok := states[ref]
		if !ok {
			continue
		}
		fmt.Print(statusLine(now, s))
	}
	return nil
}
