package runner

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/journal"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/schedule"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// prepared は履歴から準備した特徴と、実注文を出さずに復元するための観測列です。
type prepared struct {
	Features     *strategy.Features
	Plan         schedule.Plan
	Generation   string
	Cutoff       time.Time
	Last         map[string]strategy.Observation
	Rows         []strategy.Observation
	SourceHashes map[string][]string
}

// bootstrapRow は原板の保存IDと、暖機に使う価格時計を対応させます。
type bootstrapRow struct {
	reference  string
	board      controller.Board
	at         time.Time
	inputID    int64
	sourceHash string
}

// prepare は同じ立会世代の全履歴を保存し、過去発注なしで特徴だけを準備します。
//
// 引数: ctx context.Context、client *controller.Client、cfg config.Config、bundle candleBundle、store *journal.Store は中断・履歴取得・設定・足履歴・市場正本です。
// 返り値: *prepared は暖機済み特徴と観測列、error は世代混在・保存・準備失敗です。
func prepare(ctx context.Context, client *controller.Client, cfg config.Config, bundle candleBundle, store *journal.Store) (*prepared, error) {
	cutoff := time.Now()
	log.Printf("[履歴準備開始] 二市場の原板を取得します")
	histories, err := loadBooks(ctx, client, cutoff)
	if err != nil {
		return nil, err
	}
	received := time.Now()
	result, err := prepareHistory(ctx, cfg, bundle, histories, store, cutoff, received)
	if err != nil {
		return nil, err
	}
	log.Printf("[履歴準備完了] %s / 採用%d行", result.Plan.ID, len(result.Rows))
	return result, nil
}

// prepareHistory は保存済みの取得応答を検査し、時計順に特徴へ投入します。
//
// 引数: ctx context.Context、cfg config.Config、bundle candleBundle、histories map[string]controller.BookHistory、store *journal.Store、cutoff/received time.Time は因果境界と実取込時刻です。
// 返り値: *prepared は注文を生成しない暖機結果、error は保存または入力不整合です。
func prepareHistory(ctx context.Context, cfg config.Config, bundle candleBundle, histories map[string]controller.BookHistory, store *journal.Store, cutoff, received time.Time) (*prepared, error) {
	if store == nil || cutoff.IsZero() || received.Before(cutoff) {
		return nil, fmt.Errorf("市場正本または履歴取込時計が不正です")
	}
	references := []string{"TOPIX", "NK225"}
	snapshots := []controller.Snapshot{}
	for _, reference := range references {
		one, oneOK := bundle.One[reference]
		five, fiveOK := bundle.Five[reference]
		book, bookOK := histories[reference]
		if !oneOK || !fiveOK || !bookOK || one.Snapshot.Code != reference ||
			five.Snapshot.Code != reference || book.Snapshot.Code != reference {
			return nil, fmt.Errorf("%sの足または原板履歴が揃っていません", reference)
		}
		snapshots = append(snapshots, one.Snapshot, five.Snapshot, book.Snapshot)
	}
	if err := controller.ValidateSnapshots(snapshots...); err != nil {
		return nil, err
	}
	base := bundle.One["TOPIX"].Snapshot
	plan, err := schedule.Build(base.SessionID, base.Session.StartsAt, base.Session.EndsAt, cfg.Schedule)
	if err != nil {
		return nil, err
	}
	// 特徴量は売買する一商品の手法から選び、その共通条件で両ラージを観測します。
	reference, _ := cfg.Selected()
	parameters, err := methodParameters(cfg, reference, plan.Kind)
	if err != nil {
		return nil, err
	}
	session := strategy.Session{ID: plan.ID, Kind: plan.Kind, Start: bootstrapSeconds(plan.Start),
		ForceClose: bootstrapSeconds(plan.ForceClose), End: bootstrapSeconds(plan.End),
		USOpen: bootstrapSeconds(plan.USOpen)}
	features, err := strategy.NewFeatures(session, map[string]float64{
		"TOPIX": cfg.Products["TOPIX"].Tick, "NK225": cfg.Products["NK225"].Tick,
	}, parameters.Features)
	if err != nil {
		return nil, err
	}
	result := &prepared{Features: features, Plan: plan, Generation: base.Generation, Cutoff: cutoff,
		Last: make(map[string]strategy.Observation), SourceHashes: make(map[string][]string)}
	researchRequired, err := researchFeaturesRequired(cfg, reference, plan.Kind)
	if err != nil {
		return nil, err
	}
	if researchRequired {
		features.EnableResearch()
	}
	if err = store.Event("履歴準備開始", plan.ID, "", base.Generation, received, map[string]any{
		"cutoff": cutoff, "received_at": received, "snapshots": snapshots,
		"features": parameters.Features, "candles": bundle,
		"warmup_clock": "暖機のみ価格時計と足の名目公開遅延を使用。実取込時計は別保存。過去注文を出さない。",
	}); err != nil {
		return nil, err
	}
	// ----------------------------------------
	// 原文は元の配列順で全件保存する。時刻不正・逆行・場外の行も失わない。
	accepted := []bootstrapRow{}
	inputs := map[string][]int64{}
	counts := map[string]map[string]int{}
	total := 0
	for _, reference := range references {
		history := histories[reference]
		ids := make([]int64, 0, len(history.Data))
		for begin := 0; begin < len(history.Data); begin += 1000 {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			end := begin + 1000
			if end > len(history.Data) {
				end = len(history.Data)
			}
			batch := make([]journal.BookRecord, 0, end-begin)
			for _, board := range history.Data[begin:end] {
				raw := board.Raw
				if len(raw) == 0 {
					return nil, fmt.Errorf("%sの履歴原文がありません", reference)
				}
				batch = append(batch, journal.BookRecord{Session: plan.ID, Generation: base.Generation,
					Code: reference, Received: received, Source: "起動履歴", Raw: raw})
			}
			added, recordErr := store.RecordBooks(batch)
			if recordErr != nil {
				return nil, recordErr
			}
			ids = append(ids, added...)
		}
		counts[reference] = map[string]int{"saved": len(ids), "accepted": 0, "invalid_clock": 0,
			"outside": 0, "reversed": 0, "invalid_price": 0}
		var previous time.Time
		for i, board := range history.Data {
			at, parseErr := time.Parse(time.RFC3339Nano, board.CurrentPriceTime)
			if parseErr != nil {
				counts[reference]["invalid_clock"]++
				continue
			}
			if at.Before(plan.Start) || !at.Before(plan.End) || !at.Before(cutoff) {
				counts[reference]["outside"]++
				continue
			}
			if !previous.IsZero() && at.Before(previous) {
				counts[reference]["reversed"]++
				continue
			}
			if board.CurrentPrice <= 0 || math.IsNaN(board.CurrentPrice) || math.IsInf(board.CurrentPrice, 0) {
				counts[reference]["invalid_price"]++
				continue
			}
			previous = at
			digest := sha256.Sum256(board.Raw)
			accepted = append(accepted, bootstrapRow{reference, board, at, ids[i], fmt.Sprintf("%x", digest)})
			counts[reference]["accepted"]++
		}
		total += len(ids)
	}
	log.Printf("[履歴保存完了] %s / 二市場%d行", plan.ID, total)
	// ----------------------------------------
	// 暖機では取得済みの確定足を各足の名目公開時刻に置く。本受信へは流用しない。
	for _, reference := range references {
		for _, set := range []struct {
			interval int
			data     controller.Candles
		}{{60, bundle.One[reference]}, {300, bundle.Five[reference]}} {
			bars := append([]controller.Candle(nil), set.data.Data...)
			sort.SliceStable(bars, func(i, j int) bool { return bars[i].Time.Before(bars[j].Time) })
			for _, bar := range bars {
				if !bar.Closed || bar.Time.Before(plan.Start) || !bar.Time.Before(plan.End) {
					continue
				}
				end := bar.Time.Add(time.Duration(set.interval) * time.Second)
				if end.After(cutoff) || end.After(set.data.Snapshot.AsOf) {
					continue
				}
				release := bootstrapSeconds(end) + parameters.Features.CandleDelay
				row := strategy.Candle{Time: bootstrapSeconds(bar.Time), Open: bar.Open, High: bar.High,
					Low: bar.Low, Close: bar.Close, Closed: true}
				if _, err = features.AddCandles(reference, set.interval, []strategy.Candle{row}, release); err != nil {
					return nil, err
				}
			}
		}
	}
	sort.SliceStable(accepted, func(i, j int) bool { return accepted[i].at.Before(accepted[j].at) })
	for i, row := range accepted {
		if i%1000 == 0 {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
		}
		clock := bootstrapSeconds(row.at)
		board := row.board
		observation, observeErr := features.OnBoard(referenceBoard(row.reference, clock, clock, board))
		if observeErr != nil {
			return nil, observeErr
		}
		result.Rows = append(result.Rows, observation)
		result.Last[row.reference] = observation
		result.SourceHashes[row.reference] = append(result.SourceHashes[row.reference], row.sourceHash)
		inputs[row.reference] = append(inputs[row.reference], row.inputID)
	}
	if err = store.Event("履歴準備完了", plan.ID, "", base.Generation, time.Now(), map[string]any{
		"cutoff": cutoff, "received_at": received, "counts": counts,
		"observation_input_ids": inputs,
		"index_rule":            "市場別Observation.Indexは0始まり。対応配列の同じ添字がbooks.id。",
		"historical_orders":     0,
	}); err != nil {
		return nil, err
	}
	return result, nil
}

// bootstrapSeconds はゼロ時刻を欠測0にし、その他をUNIX小数秒へ変換します。
//
// 引数: at time.Time は時刻です。
// 返り値: float64 は秒単位の時計です。
func bootstrapSeconds(at time.Time) float64 {
	if at.IsZero() {
		return 0
	}
	return float64(at.Unix()) + float64(at.Nanosecond())/1e9
}
