package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/e01"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/execution"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// statusReadyDay は表示検査用の二市場方向準備済み状態を作ります。
//
// 引数: t(*testing.T) は試験です。
// 返り値: *app、*marketRuntime、time.Time は運転器・担当・表示時計です。
func statusReadyDay(t *testing.T) (*app, *marketRuntime, time.Time) {
	t.Helper()
	a, m, now := indexDayApp(t)
	start := now.Add(-2 * time.Hour)
	a.plan.Start, m.Saved.Plan.Start, a.view.Session.StartsAt = start, start, start
	m.Portfolio.Session.Start = seconds(start)
	a.features.Session.Start, a.features.E01.Start = seconds(start), seconds(start)
	for _, reference := range []string{"TOPIX", "NK225"} {
		for i := 0; i < 63; i++ {
			bar := strategy.Candle{Time: seconds(start.Add(time.Duration(i) * time.Minute)), Open: 100,
				High: 102, Low: 99, Close: 101, Closed: true}
			if _, err := a.features.AddCandles(reference, 60, []strategy.Candle{bar}, bar.Time+120); err != nil {
				t.Fatal(err)
			}
		}
	}
	a.observed = map[string]strategy.Observation{"TOPIX": {Reference: "TOPIX", Time: seconds(now), Received: seconds(now),
		ATR: 5, GateDirection: 1, E01: e01.Observation{Indicators: e01.Indicators{MainDirection: 1, PeerDirection: 1}, Direction: 1}}}
	a.lastHandled["TOPIX"] = now
	a.inputCounts, a.decisionCounts = map[string]uint64{"TOPIX": 100}, map[string]uint64{"TOPIX": 90}
	return a, m, now
}

// TestStatusDistinguishesStartupAndDirection は寄前、指標準備、条件待ちを混同しないことを確認します。
//
// 引数: t(*testing.T) は試験です。
// 返り値: なし。
func TestStatusDistinguishesStartupAndDirection(t *testing.T) {
	a, m, now := statusReadyDay(t)
	if got := a.statusFor(m, m.Saved.Plan.Start.Add(-time.Minute)).State; !strings.Contains(got, "立会開始待ち") {
		t.Fatal("寄前の待機理由が不明です", got)
	}
	ready := a.statusFor(m, now)
	if ready.RequiredBars != 63 || ready.OwnBars != 63 || ready.PeerBars != 63 || ready.State != "反発・気配条件の成立待ち" {
		t.Fatalf("方向準備済みの条件待ちが不正です: %+v", ready)
	}
	a.features.E01.Markets["NK225"].Bars[60] = a.features.E01.Markets["NK225"].Bars[60][:62]
	preparing := a.statusFor(m, now)
	if !strings.Contains(preparing.State, "方向指標を準備中") || preparing.PeerBars != 62 {
		t.Fatalf("相手指標の不足を示していません: %+v", preparing)
	}
}

// TestStatusExplainsOrderPrerequisites は口座、発注商品、未来価格時計による新規停止理由を確認します。
//
// 引数: t(*testing.T) は試験です。
// 返り値: なし。表示と既存canEnterの真偽が一致することも調べます。
func TestStatusExplainsOrderPrerequisites(t *testing.T) {
	for _, name := range []string{"口座", "商品板", "未来価格時計"} {
		t.Run(name, func(t *testing.T) {
			a, m, now := statusReadyDay(t)
			if !a.canEnter(m, now) {
				t.Fatal("人工の発注前提が成立していません")
			}
			want := ""
			switch name {
			case "口座":
				a.viewAt = now.Add(-time.Minute)
				want = "口座情報の受信待ち"
			case "商品板":
				delete(a.quotes, "TOPIXmini")
				want = "TOPIXminiの新鮮な正常気配待ち"
			case "未来価格時計":
				q := a.quotes["NK225"]
				q.Board.CurrentPriceTime = now.Add(2 * time.Second).Format(time.RFC3339Nano)
				a.quotes["NK225"] = q
				want = "NK225の価格時計が端末時刻より未来"
			}
			if a.canEnter(m, now) || a.entryBlockReason(m, now) != want || a.statusFor(m, now).State != want {
				t.Fatal("不成立理由と新規制御が一致しません", a.entryBlockReason(m, now), a.statusFor(m, now).State)
			}
		})
	}
}

// TestStatusReportsWithoutTradesOrStateMutation は無約定でも表示とJSONLが出て判断正本は不変なことを確認します。
//
// 引数: t(*testing.T) は試験です。
// 返り値: なし。
func TestStatusReportsWithoutTradesOrStateMutation(t *testing.T) {
	a, m, now := statusReadyDay(t)
	beforePortfolio, err := m.Portfolio.Dump()
	if err != nil {
		t.Fatal(err)
	}
	beforeFeatures, err := a.features.Dump()
	if err != nil {
		t.Fatal(err)
	}
	beforeSaved, err := json.Marshal(m.Saved)
	if err != nil {
		t.Fatal(err)
	}
	beforeState := string(m.Manager.State())
	output, err := os.CreateTemp(t.TempDir(), "status-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = output
	defer func() {
		os.Stdout = original
		_ = output.Close()
	}()
	for i := 0; i < 3; i++ {
		if err = a.reportStatus(now); err != nil {
			t.Fatal(err)
		}
	}
	os.Stdout = original
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
	printed, err := os.ReadFile(output.Name())
	if err != nil || !strings.Contains(string(printed), "[稼働/dryrun] TOPIXmini（参照TOPIX）:") ||
		!strings.Contains(string(printed), "受信100・判断90行") || !strings.Contains(string(printed), "仮想保有 買0・売0枚") {
		t.Fatal("無約定の状態が標準出力にありません", string(printed), err)
	}
	db, err := sql.Open("sqlite", filepath.Join(a.cfg.Root, "market.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var payload string
	if err = db.QueryRow("SELECT payload FROM events WHERE kind='稼働' ORDER BY seq DESC LIMIT 1").Scan(&payload); err != nil || !strings.Contains(payload, `"decision_count":90`) {
		t.Fatal("稼働状態がSQLiteにありません", payload, err)
	}
	if _, err = os.Stat(filepath.Join(a.cfg.Root, "log", "market", "trace.jsonl")); !os.IsNotExist(err) {
		t.Fatal("廃止したtrace.jsonlを生成しました", err)
	}
	afterPortfolio, _ := m.Portfolio.Dump()
	afterFeatures, _ := a.features.Dump()
	afterSaved, _ := json.Marshal(m.Saved)
	if string(beforePortfolio) != string(afterPortfolio) || string(beforeFeatures) != string(afterFeatures) ||
		string(beforeSaved) != string(afterSaved) || beforeState != string(m.Manager.State()) ||
		len(m.Manager.Records()) != 0 || len(m.Manager.Positions()) != 0 {
		t.Fatal("稼働表示が判断・注文・建玉を変更しました")
	}
}

// TestStatusDistinguishesPaperHoldingsFromEmptyAccount は空の実口座と仮想残玉を混同しないことを確認します。
//
// 引数: t(*testing.T)は試験です。
// 返り値: なし。再起動後も仮想残玉を保ち、表示による状態更新・実送信がないことを検査します。
func TestStatusDistinguishesPaperHoldingsFromEmptyAccount(t *testing.T) {
	a, m, calls, now := integrationApp(t, "NK225", true)
	a.observed = map[string]strategy.Observation{"NK225": {Reference: "NK225", Time: seconds(now), Received: seconds(now)}}
	a.lastHandled = map[string]time.Time{"NK225": now}
	intent := execution.Intent{ID: "status-virtual-sell", Owner: "core", Product: "NK225micro",
		Session: a.plan.ID, ContractSymbol: m.Saved.Contract.Symbol, Side: "sell", TradeType: "new",
		Quantity: 10, Price: 65000, At: now, ExpiresAt: now.Add(time.Minute), Reason: "仮想表示の試験"}
	if _, err := m.Manager.Submit(context.Background(), a.client, intent, a.view); err != nil {
		t.Fatal(err)
	}
	if err := m.Manager.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	m.Manager, err = execution.Open(filepath.Join(a.cfg.Root, "log", "NK225micro"), filepath.Join(a.cfg.Root, "NK225micro.sqlite"), true, "NK225micro", 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Manager.Close() })
	before := string(m.Manager.State())
	s := a.statusFor(m, now)
	line := statusLine(now, s)
	if !s.DryRun || s.PositionSource != "仮想約定台帳" || s.State != "仮想建玉の継続・返済を監視中" ||
		!strings.Contains(line, "[稼働/dryrun] NK225micro（参照NK225）:") || !strings.Contains(line, "仮想保有 買0・売10枚") {
		t.Fatalf("仮想保有の説明が不明です: %+v\n%s", s, line)
	}
	raw, err := json.Marshal(s)
	if err != nil || !strings.Contains(string(raw), `"dryrun":true`) || !strings.Contains(string(raw), `"position_source":"仮想約定台帳"`) {
		t.Fatal("JSONにも仮想状態が必要です", string(raw), err)
	}
	if calls.Load() != 0 || len(a.view.Positions) != 0 || len(a.view.BrokerOrders) != 0 ||
		len(m.Manager.Records()) != 1 || m.Manager.Quantity("core", "sell") != 10 || string(m.Manager.State()) != before {
		t.Fatal("表示と復元が実注文・実口座又は仮想台帳を変更しました")
	}
}

// TestStatusLabelsLiveAndIgnoresStrategyShadow は実運転表記と未約定の基準波の分離を検査します。
//
// 引数: t(*testing.T)は試験です。
// 返り値: なし。
func TestStatusLabelsLiveAndIgnoresStrategyShadow(t *testing.T) {
	a, m, now := statusReadyDay(t)
	m.Portfolio.Paper.Normal.Direction = -1
	if s := a.statusFor(m, now); len(s.Positions) != 0 || !strings.Contains(statusLine(now, s), "仮想保有 買0・売0枚") {
		t.Fatal("戦略内の基準波を仮想約定済み建玉へ混入しました", s)
	}
	live, lm, calls, at := integrationApp(t, "NK225", false)
	s := live.statusFor(lm, at)
	if s.DryRun || s.PositionSource != "実約定台帳" ||
		!strings.Contains(statusLine(at, s), "[稼働/live] NK225micro（参照NK225）:") ||
		!strings.Contains(statusLine(at, s), "実約定保有 買0・売0枚") || calls.Load() != 0 {
		t.Fatal("実運転と仮想運転の表示が混同されています", s)
	}
}

// TestStatusMarksOwnedHoldingsWithProductQuote は発注商品の返済側気配で含み損益を計算します。
//
// 引数: t(*testing.T)は試験です。
// 返り値: なし。参照ラージとの混同、原本の根拠削除、古い価格による評価を検出します。
func TestStatusMarksOwnedHoldingsWithProductQuote(t *testing.T) {
	a, m, calls, now := integrationApp(t, "TOPIX", true)
	for i, side := range []string{"buy", "sell"} {
		intent := execution.Intent{ID: "評価" + side, Owner: "core", Product: "TOPIXmini",
			Session: a.plan.ID, ContractSymbol: m.Saved.Contract.Symbol, Side: side, TradeType: "new",
			Quantity: i + 1, Price: 100, At: now, ExpiresAt: now.Add(time.Minute), Reason: "評価試験",
			Evidence: json.RawMessage(`{"根拠":"原本に保持"}`)}
		if _, err := m.Manager.Submit(context.Background(), a.client, intent, a.view); err != nil {
			t.Fatal(err)
		}
		if err := m.Manager.Checkpoint(json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	before := string(m.Manager.State())
	s := a.statusFor(m, now)
	// ラージは99.75/100.25、実商品は109.75/110.25。買1・売2の合計は-10,750円です。
	if s.UnrealizedYen == nil || *s.UnrealizedYen != -10750 || s.UnrealizedByOwner["core"] != -10750 {
		t.Fatalf("発注商品の返済側気配を使っていません: %+v", s)
	}
	for _, h := range s.Positions {
		if len(h.Evidence) != 0 {
			t.Fatal("定期表示に新規根拠全量が複製されました")
		}
	}
	if string(m.Manager.State()) != before || len(m.Manager.Positions()[0].Evidence) == 0 || calls.Load() != 0 {
		t.Fatal("診断が原本または注文を変更しました")
	}
	if stale := a.statusFor(m, now.Add(time.Hour)); stale.UnrealizedYen != nil || stale.UnrealizedByOwner != nil {
		t.Fatal("古い価格の評価を現在損益として表示しました")
	}
}
