package diagnostics

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"

	_ "modernc.org/sqlite"
)

var validSession = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_(Day|Night)$`)

// snapshotParseLimit は旧巨大状態をSQLiteで構文解析する前の上限です。
const snapshotParseLimit = 16 << 20

// source は変更を禁止したSQLite読取トランザクションです。
type source struct {
	name string
	db   *sql.DB
	tx   *sql.Tx
}

// sourceSummary は省略を含む診断範囲を明示します。
type sourceSummary struct {
	Name               string `json:"source"`
	Trades             int    `json:"trades"`
	Events             int    `json:"events_written"`
	InputEventsOmitted int    `json:"input_notifications_omitted"`
	OversizeEvents     int    `json:"oversize_payloads_summarized"`
	FirstSequence      int64  `json:"first_event_sequence"`
	LastSequence       int64  `json:"last_event_sequence"`
	SnapshotRevision   int64  `json:"snapshot_revision"`
	SnapshotAt         string `json:"snapshot_at"`
	SnapshotBytes      int64  `json:"snapshot_bytes"`
	SnapshotOmitted    bool   `json:"snapshot_structure_omitted"`
	ParseSkippedEvents int    `json:"oversize_ledger_events_not_parsed"`
}

// Export は保存済み資料だけを読み、転送可能なサイズの診断ZIPへ書き出します。
//
// 引数: ctx(context.Context)は中断、cfg(config.Config)は商品・保存先・対象場です。
// 返り値: []stringは作成したZIP絶対パス、errorは読取・容量・出力失敗です。
// APIクライアント、売買実行器、journal.Openを使用せず、元DB・原ログを変更しません。
func Export(ctx context.Context, cfg config.Config) ([]string, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	_, market := cfg.Selected()
	mode := "live"
	if cfg.Trading.DryRun {
		mode = "dryrun"
	}
	base := filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), mode, cfg.InstanceID())
	sources := []*source{}
	missing := []string{}
	defer func() {
		for _, s := range sources {
			_ = s.tx.Rollback()
			_ = s.db.Close()
		}
	}()
	for _, name := range []string{market.Product, "market"} {
		s, err := readSource(ctx, filepath.Join(base, name+".sqlite"), name)
		if os.IsNotExist(err) {
			missing = append(missing, name)
			continue
		}
		if err != nil {
			return nil, err
		}
		sources = append(sources, s)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("診断対象の既存SQLiteがありません: %s", base)
	}
	session := cfg.Startup.DiagnosticSession
	if session == "" {
		for _, s := range sources {
			var latest sql.NullString
			err := s.tx.QueryRowContext(ctx, "SELECT max(session) FROM (SELECT max(session) AS session FROM sessions UNION ALL SELECT max(session) FROM events WHERE session<>'' UNION ALL SELECT max(session) FROM books)").Scan(&latest)
			if err != nil {
				return nil, err
			}
			if latest.Valid && latest.String > session {
				session = latest.String
			}
		}
	}
	if !validSession.MatchString(session) {
		return nil, fmt.Errorf("診断対象セッションがありません。startup.diagnostic_sessionで記録済みの場を指定してください")
	}
	start, end, err := sessionWindow(session)
	if err != nil {
		return nil, err
	}
	// 場IDのない起動・台帳更新は補助記録です。時計が同じだけではその場を取引済みと判定しません。
	found := false
	for _, s := range sources {
		var recorded bool
		if err = s.tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM sessions WHERE session=? UNION ALL
			SELECT 1 FROM trades WHERE session=? UNION ALL
			SELECT 1 FROM events WHERE session=? UNION ALL
			SELECT 1 FROM books WHERE session=?)`, session, session, session, session).Scan(&recorded); err != nil {
			return nil, err
		}
		found = found || recorded
	}
	if !found {
		return nil, fmt.Errorf("指定場%sの約定・診断記録がありません", session)
	}
	now := time.Now().In(time.FixedZone("JST", 9*3600))
	output := filepath.Join(cfg.Path(cfg.Runtime.LogDirectory), "diagnostics")
	if err = os.MkdirAll(output, 0700); err != nil {
		return nil, err
	}
	safe := map[string]any{"product": market.Product, "max_per_side": market.MaxPerSide, "methods": map[string]string{"day": cfg.MethodID("Day"), "night": cfg.MethodID("Night")}, "dryrun": cfg.Trading.DryRun, "instance_id": cfg.InstanceID(), "schedule": cfg.Schedule, "configuration_signature": cfg.Signature(), "configuration_context": "診断書出し時の現在設定です。対象セッションの運転時設定と同じとは限りません", "configuration_observed_at": now.Format(time.RFC3339Nano)}
	if build, ok := debug.ReadBuildInfo(); ok {
		settings := map[string]string{}
		for _, item := range build.Settings {
			switch item.Key {
			case "GOOS", "GOARCH", "CGO_ENABLED", "vcs", "vcs.revision", "vcs.time", "vcs.modified":
				settings[item.Key] = item.Value
			}
		}
		safe["binary"] = map[string]any{"go_version": build.GoVersion, "module": build.Main.Path, "version": build.Main.Version, "settings": settings}
	}
	settings, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return nil, err
	}
	scope := map[string]any{"session": session, "generated_at": now.Format(time.RFC3339Nano), "event_window_start": start.Format(time.RFC3339Nano), "event_window_end": end.Format(time.RFC3339Nano), "missing_sources": missing, "raw_orderbooks_included": false, "full_snapshot_included": false, "original_logs_modified": false, "api_contact": false, "snapshot_parse_limit_bytes": snapshotParseLimit, "settings_context": "settings.jsonは診断書出し時の現在設定であり、運転当時の設定の証明ではありません", "omitted_event_reference": "SQLで本文を省略した巨大イベントは元DBのsource/seq/kind/correlation/bytesで対応します。元本文のSHA256を新たに算出する保証はありません", "note": "商品DBと市場DBはそれぞれ読取開始時点で固定します。両DBを跨ぐ完全同時の確定ではありません。manifestの件数は当該ZIP作成時点で、最終partのsummary.jsonが最終件数です。"}
	a := &archives{dir: output, prefix: session + "_" + market.Product + "_" + mode + "_" + now.Format("20060102T150405.000000000"), settings: settings, summary: scope}
	defer a.abort()
	summaries := []sourceSummary{}
	for _, s := range sources {
		ss := sourceSummary{Name: s.name}
		if err = exportTrades(ctx, s, a, session, &ss); err != nil {
			return nil, err
		}
		if err = exportEvents(ctx, s, a, session, start, end, &ss); err != nil {
			return nil, err
		}
		if err = exportSnapshot(ctx, s, a, &ss); err != nil {
			return nil, err
		}
		summaries = append(summaries, ss)
	}
	scope["sources"] = summaries
	if err = putJSON(a, "summary.json", scope); err != nil {
		return nil, err
	}
	if err = a.finish(true); err != nil {
		return nil, err
	}
	return append([]string(nil), a.paths...), nil
}

// readSource は存在する通常ファイルをmode=roで開き、WALも含む読取状態を固定します。
//
// 引数: ctx(context.Context)、path/name(string)。返り値: *sourceと読取失敗errorです。
func readSource(ctx context.Context, path, name string) (*source, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, fmt.Errorf("診断対象DBは通常ファイルではありません")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		_ = tx.Rollback()
		_ = db.Close()
		return nil, fmt.Errorf("診断対象DBの形式が不明です: %s / %d / %v", name, version, err)
	}
	return &source{name: name, db: db, tx: tx}, nil
}

// sessionWindow はセッション名のない起動・障害イベントを含める日本時間範囲です。
//
// 引数: session(string)。返り値: start/end(time.Time)と日付不正errorです。
func sessionWindow(session string) (time.Time, time.Time, error) {
	if !validSession.MatchString(session) {
		return time.Time{}, time.Time{}, fmt.Errorf("不正な診断セッションです")
	}
	d, err := time.ParseInLocation("2006-01-02", session[:10], time.FixedZone("JST", 9*3600))
	if err != nil {
		return d, d, err
	}
	if strings.HasSuffix(session, "Night") {
		return d.Add(16 * time.Hour), d.Add(30*time.Hour + 30*time.Minute), nil
	}
	return d.Add(8 * time.Hour), d.Add(16 * time.Hour), nil
}

// exportTrades は当場の全約定を原文JSONLと費用・根拠付きCSVへ保存します。
//
// 引数: ctx(context.Context)、s(*source)、a(*archives)、session(string)、summary(*sourceSummary)。返り値: errorです。
func exportTrades(ctx context.Context, s *source, a *archives, session string, summary *sourceSummary) error {
	rows, err := s.tx.QueryContext(ctx, "SELECT body FROM trades WHERE session=? ORDER BY seq", session)
	if err != nil {
		return err
	}
	defer rows.Close()
	jsonOut := &chunkWriter{archive: a, name: s.name + "/trades.jsonl"}
	csvOut := &chunkWriter{archive: a, name: s.name + "/trades.csv"}
	w := csv.NewWriter(csvOut)
	if err = w.Write([]string{"記録ID", "約定確認時刻", "セッション", "商品", "担当", "区分", "売買", "枚数", "約定価格", "実現損益円", "標準手数料円", "理由", "発注意図ID", "上流注文ID", "約定ID", "仮想実行", "新規建玉ID", "返済建玉内訳JSON", "返済内訳の根拠", "判断根拠JSON"}); err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		var t journal.Trade
		if err = json.Unmarshal(raw, &t); err != nil {
			return err
		}
		if _, err = jsonOut.Write(append(raw, '\n')); err != nil {
			return err
		}
		closed, _ := json.Marshal(t.Closed)
		if err = w.Write([]string{t.ID, t.At.Format(time.RFC3339Nano), t.Session, t.Product, t.Owner, t.TradeType, t.Side, strconv.Itoa(t.Quantity), number(t.Price), number(t.PnL), number(t.Fee), t.Reason, t.ClientOrderID, t.BrokerOrderID, t.ExecutionID, strconv.FormatBool(t.DryRun), t.HoldID, string(closed), t.ClosedBasis, string(t.Evidence)}); err != nil {
			return err
		}
		summary.Trades++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	w.Flush()
	if err = w.Error(); err != nil {
		return err
	}
	if err = jsonOut.flush(); err != nil {
		return err
	}
	return csvOut.flush()
}

// number は金額・価格を丸めずCSV文字列へ変換します。
//
// 引数: v(float64)。返り値: stringです。
func number(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// exportEvents は巨大全状態をSQL側で避け、必要イベントを当場だけ出力します。
//
// 引数: ctx(context.Context)、s(*source)、a(*archives)、session(string)、start/end(time.Time)、summary(*sourceSummary)。返り値: errorです。
func exportEvents(ctx context.Context, s *source, a *archives, session string, start, end time.Time, summary *sourceSummary) error {
	query := `SELECT seq,kind,session,code,correlation,at,
 CASE WHEN kind='台帳更新' AND length(CAST(payload AS BLOB))>? THEN json_object('_diagnostic_payload_omitted',1,'json_bytes',length(CAST(payload AS BLOB)),'full_state_omitted',1,'structure_parsing_skipped',1,'reason','16MiBを超える旧台帳更新は構文解析せず、元DBのイベント連番で参照します')
 WHEN kind='台帳更新' THEN json_object(
 'revision',json_extract(payload,'$.revision'),
 'previous_sha256',json_extract(payload,'$.previous_sha256'),
 'state_sha256',json_extract(payload,'$.state_sha256'),
 'state_bytes',COALESCE(json_extract(payload,'$.state_bytes'),length(CAST(json_extract(payload,'$.state') AS BLOB))),
 'new_trade_count',json_extract(payload,'$.new_trade_count'),
 'full_state_omitted',json_extract(payload,'$.state') IS NOT NULL)
 WHEN length(CAST(payload AS BLOB))>262144 THEN json_object('_diagnostic_payload_omitted',1,'json_bytes',length(CAST(payload AS BLOB)),'reason','巨大イベント本文は診断ZIPに含めません。約定全原文はtrades.jsonlへ収録します') ELSE payload END,
 length(CAST(payload AS BLOB)) FROM events WHERE session=? OR (session='' AND julianday(at)>=julianday(?) AND julianday(at)<julianday(?)) ORDER BY seq`
	rows, err := s.tx.QueryContext(ctx, query, snapshotParseLimit, session, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	defer rows.Close()
	w := &chunkWriter{archive: a, name: s.name + "/events.jsonl"}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	for rows.Next() {
		var seq int64
		var kind, scene, code, correlation, at string
		var raw []byte
		var size int
		if err = rows.Scan(&seq, &kind, &scene, &code, &correlation, &at, &raw, &size); err != nil {
			return err
		}
		if !journal.IncludeTraceEvent(kind) {
			summary.InputEventsOmitted++
			continue
		}
		compact, err := journal.CompactPayload(kind, raw)
		if err != nil {
			return err
		}
		if size > 262144 && kind != "台帳更新" {
			summary.OversizeEvents++
		}
		if kind == "台帳更新" && size > snapshotParseLimit {
			summary.ParseSkippedEvents++
		}
		if err = encoder.Encode(map[string]any{"seq": seq, "kind": kind, "session": scene, "code": code, "correlation": correlation, "at": at, "payload": json.RawMessage(compact)}); err != nil {
			return err
		}
		if summary.FirstSequence == 0 {
			summary.FirstSequence = seq
		}
		summary.LastSequence = seq
		summary.Events++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return w.flush()
}

// exportSnapshot は最新復元状態から必要な識別・実保有・未確定注文だけを抽出します。
//
// 引数: ctx(context.Context)、s(*source)、a(*archives)、summary(*sourceSummary)。返り値: errorです。
func exportSnapshot(ctx context.Context, s *source, a *archives, summary *sourceSummary) error {
	// BLOBの長さを先に確認し、巨大旧状態をJSON1へ渡さない。
	err := s.tx.QueryRowContext(ctx, `SELECT revision,updated_at,length(CAST(state AS BLOB)) FROM snapshot WHERE id=1`).Scan(&summary.SnapshotRevision, &summary.SnapshotAt, &summary.SnapshotBytes)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if summary.SnapshotBytes > snapshotParseLimit {
		summary.SnapshotOmitted = true
		omitted := map[string]any{"_diagnostic_omitted": true, "reason": "16MiBを超える復元状態は構文解析しません。建玉ゼロや注文ゼロを意味しません", "snapshot_revision": summary.SnapshotRevision, "snapshot_at": summary.SnapshotAt, "snapshot_bytes": summary.SnapshotBytes, "parse_limit_bytes": snapshotParseLimit, "full_snapshot_included": false, "structure_parsing_skipped": true, "all_session_trades_included": true}
		for _, name := range []string{"current_snapshot_summary.json", "current_holdings.jsonl", "current_pending_orders.jsonl", "current_exit_state.jsonl", "current_s01_state.jsonl"} {
			if err = putJSON(a, s.name+"/"+name, omitted); err != nil {
				return err
			}
		}
		return nil
	}
	var raw []byte
	err = s.tx.QueryRowContext(ctx, `SELECT json_object('symbol',json_extract(state,'$.symbol'),'dryrun',json_extract(state,'$.dryrun'),'max_position',json_extract(state,'$.max_position'),'multiplier',json_extract(state,'$.multiplier'),'algorithm',json_extract(state,'$.runtime_state.Algorithm'),'signature',json_extract(state,'$.runtime_state.Signature'),'methods',json_extract(state,'$.runtime_state.Methods'),'session',json_extract(state,'$.runtime_state.Plan.ID'),'snapshot_bytes',length(CAST(state AS BLOB)),'full_snapshot_included',0) FROM snapshot WHERE id=1`).Scan(&raw)
	if err != nil {
		return err
	}
	if err = putJSON(a, s.name+"/current_snapshot_summary.json", json.RawMessage(raw)); err != nil {
		return err
	}
	for _, item := range []struct{ name, query string }{
		{"current_s01_state.jsonl", `SELECT json_object('guard',json_extract(state,'$.runtime_state.Portfolio.S01.Guard'),'rejected_parents',json_extract(state,'$.runtime_state.Portfolio.S01.RejectedParents'),'last_rejection',json_extract(state,'$.runtime_state.Portfolio.S01.LastRejection'),'baseline_episode',json((SELECT json_object('campaign',json_extract(e.value,'$.Campaign'),'side',json_extract(e.value,'$.Side'),'quantity',(SELECT COALESCE(SUM(json_extract(l.value,'$.Quantity')),0) FROM json_each(e.value,'$.Lots') l),'pending_count',(SELECT COUNT(*) FROM json_each(snapshot.state,'$.runtime_state.Portfolio.S01.Shadow.Orders') o WHERE json_extract(o.value,'$.Signal.owner')='episode' AND json_extract(o.value,'$.Done')=0)) FROM json_each(state,'$.runtime_state.Portfolio.S01.Shadow.Research.Sleeves') e WHERE json_extract(e.value,'$.Name')='episode')),'basis','S01の親利益床と独立D07影の占有要約です。影の玉は実保有ではありません') FROM snapshot WHERE id=1 AND json_type(state,'$.runtime_state.Portfolio.S01')='object'`},
		{"current_holdings.jsonl", `SELECT json_remove(j.value,'$.evidence') FROM snapshot,json_each(snapshot.state,'$.holdings') j WHERE snapshot.id=1 AND json_extract(j.value,'$.quantity')>0`},
		{"current_pending_orders.jsonl", `SELECT json_object('status',json_extract(j.value,'$.status'),'intent',json_remove(json_extract(j.value,'$.intent'),'$.evidence'),'broker_order_id',json_extract(j.value,'$.broker_order_id'),'error',json_extract(j.value,'$.error'),'cancel_requested',json_extract(j.value,'$.cancel_requested'),'cancel_state',json_extract(j.value,'$.cancel_state')) FROM snapshot,json_each(snapshot.state,'$.orders') j WHERE snapshot.id=1 AND json_extract(j.value,'$.status') NOT IN ('done','rejected')`},
		{"current_exit_state.jsonl", `SELECT json_object('owner',json_extract(j.value,'$.Name'),'capacity',json_extract(j.value,'$.Capacity'),'campaign',json_extract(j.value,'$.Campaign'),'side',json_extract(j.value,'$.Side'),'residual_stage',json_extract(j.value,'$.Stage'),'fixed_r',json_extract(j.value,'$.FixedR'),'fixed_ma20',json_extract(j.value,'$.FixedMA'),'ma_known',json_extract(j.value,'$.MAKnown'),'realized_net',json_extract(j.value,'$.Cash'),'fees',json_extract(j.value,'$.Fees'),'profit_budget',json_extract(j.value,'$.Budget'),'residual_best',json_extract(j.value,'$.Best'),'retained_hold_id',json_extract(j.value,'$.Retained'),'blocked',json_extract(j.value,'$.Blocked'),'force_reason',json_extract(j.value,'$.ForceReason'),'virtual_core_position',json_extract(j.value,'$.Source.Legacy.Core.Position'),'virtual_normal_exit',json_extract(j.value,'$.Source.Legacy.Paper.normal'),'virtual_extra_position',json_extract(j.value,'$.Source.Supplement.Extra.Position'),'virtual_active_book',json_extract(j.value,'$.Source.Active.Book'),'virtual_episode_book',json_extract(j.value,'$.Source.Episode.Book'),'basis','保存された現在状態です。仮想候補の出口水準は実注文の約定価格保証ではありません') FROM snapshot,json_each(snapshot.state,'$.runtime_state.Portfolio.Research.Sleeves') j WHERE snapshot.id=1`},
	} {
		rows, err := s.tx.QueryContext(ctx, item.query)
		if err != nil {
			return err
		}
		w := &chunkWriter{archive: a, name: s.name + "/" + item.name}
		for rows.Next() {
			var body []byte
			if err = rows.Scan(&body); err == nil {
				_, err = w.Write(append(body, '\n'))
			}
			if err != nil {
				_ = rows.Close()
				return err
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		if err = w.flush(); err != nil {
			return err
		}
	}
	return nil
}
