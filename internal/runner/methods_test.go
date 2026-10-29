package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// TestRunRejectsMethodBeforeIO は運転入口を直接呼んでも不正IDで接続・台帳作成しないことを検証します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
func TestRunRejectsMethodBeforeIO(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "呼ばれてはいけない接続", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cfg := startupConfig(t, server.URL, "NK225micro")
	cfg.Methods.Night = "T04"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Run(ctx, cfg); err == nil || !strings.Contains(err.Error(), "手法ID") {
		t.Fatalf("日経へTOPIX夜間手法を許可しました: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("手法の検査前に接続しました")
	}
	if _, err := os.Stat(cfg.Path(cfg.Runtime.DataDirectory)); !os.IsNotExist(err) {
		t.Fatalf("手法の検査前に保存先を作成しました: %v", err)
	}
}

// TestMethodParametersKeepOverrideOrder は手法選択後も時刻と旧上書きの優先順が変わらないことを検証します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
func TestMethodParametersKeepOverrideOrder(t *testing.T) {
	for _, product := range []string{"TOPIXmini", "NK225micro"} {
		cfg := startupConfig(t, "http://example.invalid", product)
		cfg.Schedule.ExtraCutoffSeconds = 777
		cfg.Algorithm = map[string]float64{"extra.entry_cutoff_seconds": 555, "features.atr_period": 9}
		reference, _ := cfg.Selected()
		for _, kind := range []string{"Day", "Night"} {
			actual, err := methodParameters(cfg, reference, kind)
			if err != nil {
				t.Fatal(err)
			}
			expected := strategy.DefaultParameters(reference, kind)
			expected.Extra.EntryCutoffSeconds = 555
			expected.Features.ATRPeriod = 9
			if !reflect.DeepEqual(actual, expected) {
				t.Fatal("旧設定上書きの数値または優先順が変わりました")
			}
		}
	}
}

// TestNightBootstrapUsesSelectedMethod は銘柄別の夜間手法から共通特徴量を選び、両ラージを準備することを検証します。
//
// 引数: t(*testing.T) は試験です。返り値: なし。
func TestNightBootstrapUsesSelectedMethod(t *testing.T) {
	for _, reference := range []string{"TOPIX", "NK225"} {
		t.Run(reference, func(t *testing.T) {
			cfg, bundle, histories, start, cutoff := bootstrapFixture(t)
			cfg.Markets = map[string]config.Market{reference: {Enabled: true, Product: reference, MaxPerSide: 10, CoreQuantity: 5, ExtraQuantity: 5}}
			shift := 8*time.Hour + 15*time.Minute
			start, cutoff = start.Add(shift), cutoff.Add(shift)
			for _, code := range []string{"TOPIX", "NK225"} {
				one, five := bundle.One[code], bundle.Five[code]
				one.Snapshot.SessionID = "2026-10-21_Night"
				one.Snapshot.Session.StartsAt, one.Snapshot.Session.EndsAt = start, start.Add(13*time.Hour)
				one.Snapshot.AsOf = cutoff
				for i := range one.Data {
					one.Data[i].Time = one.Data[i].Time.Add(shift)
				}
				five.Snapshot = one.Snapshot
				for i := range five.Data {
					five.Data[i].Time = five.Data[i].Time.Add(shift)
				}
				bundle.One[code], bundle.Five[code] = one, five
				history := histories[code]
				history.Snapshot = one.Snapshot
				histories[code] = history
			}
			store, _, _ := bootstrapStore(t)
			prepared, err := prepareHistory(context.Background(), cfg, bundle, histories, store, cutoff, cutoff)
			if err != nil {
				t.Fatal(err)
			}
			if prepared.Features.E01.Markets["TOPIX"] == nil || prepared.Features.E01.Markets["NK225"] == nil {
				t.Fatal("片方のラージを参照から落としました")
			}
			expected := strategy.DefaultParameters(reference, "Night").Features
			expected.ATRMinBars = 1
			if !reflect.DeepEqual(prepared.Features.E01.Config, expected) {
				t.Fatal("共通の特徴設定が異なります")
			}
		})
	}
}

// TestSlotSharedFeatureValidation は各手法の共通指標一致と、将来の係数差の拒否を検査します。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSlotSharedFeatureValidation(t *testing.T) {
	baseline := strategy.DefaultParameters("NK225", "Night")
	for _, method := range []string{"U06", "D07", "TG04", "PG04"} {
		candidate, err := strategy.ParametersForMethod("NK225", "Night", method)
		if err != nil || validateSharedFeatureParameters(baseline, candidate) != nil {
			t.Fatal("現行手法の共通指標が異なります", method, err)
		}
	}
	changed := baseline
	changed.Features.ATRPeriod++
	if err := validateSharedFeatureParameters(baseline, changed); err == nil {
		t.Fatal("異なる指標期間の共有を許可しました")
	}
}

// TestSlotBootstrapEnablesLaterResearchFeatures は旧手法の準備時にも後続D07の研究特徴を有効にします。
//
// 引数: t(*testing.T)。返り値: なし。
func TestSlotBootstrapEnablesLaterResearchFeatures(t *testing.T) {
	cfg, bundle, histories, _, cutoff := bootstrapFixture(t)
	cfg.Markets = map[string]config.Market{"NK225": {Enabled: true, Product: "NK225", MaxPerSide: 10, CoreQuantity: 5, ExtraQuantity: 5}}
	cfg.Algorithm = nil
	cfg.Windows = []config.MethodWindow{
		{ID: "morning", Session: "Day", Start: "SESSION_START", End: "10:00", Method: "NK225_T06"},
		{ID: "later", Session: "Day", Start: "10:00", End: "SESSION_END", Method: "NK225_D07"},
	}
	if err := validateMethodSelection(cfg); err != nil {
		t.Fatal(err)
	}
	store, _, _ := bootstrapStore(t)
	prepared, err := prepareHistory(context.Background(), cfg, bundle, histories, store, cutoff, cutoff)
	if err != nil || !prepared.Features.ResearchEnabled {
		t.Fatal("後続枠の研究特徴を履歴から準備しませんでした", err)
	}
}
