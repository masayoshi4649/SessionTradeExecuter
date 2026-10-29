package runner

import (
	"bytes"
	"fmt"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/controller"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// referenceBoard は最良気配と10段を、判断用ラージの観測へ変換します。
//
// 引数: reference(string)、clock/received(float64)、b(controller.Board)。返り値: strategy.Boardです。
func referenceBoard(reference string, clock, received float64, b controller.Board) strategy.Board {
	o := strategy.Board{Reference: reference, Time: clock, Received: received, Last: b.CurrentPrice, Bid: b.Buy1.Price, Ask: b.Sell1.Price, BidQuantity: b.Buy1.Qty, AskQuantity: b.Sell1.Qty}
	buy := []controller.Level{b.Buy1, b.Buy2, b.Buy3, b.Buy4, b.Buy5, b.Buy6, b.Buy7, b.Buy8, b.Buy9, b.Buy10}
	sell := []controller.Level{b.Sell1, b.Sell2, b.Sell3, b.Sell4, b.Sell5, b.Sell6, b.Sell7, b.Sell8, b.Sell9, b.Sell10}
	for i := range buy {
		o.BidLevels[i] = strategy.ResearchLevel{Price: buy[i].Price, Quantity: buy[i].Qty}
		o.AskLevels[i] = strategy.ResearchLevel{Price: sell[i].Price, Quantity: sell[i].Qty}
	}
	return o
}

// quietOwner は米国開始時に新規・保有を静観対象とする担当を判定します。
//
// 引数: m(*marketRuntime)、owner(string)。返り値: boolは静観対象です。
func quietOwner(m *marketRuntime, owner string) bool {
	return owner == "core" || (m.Portfolio != nil && (m.Portfolio.Research != nil || m.Portfolio.P05 != nil))
}

// resolvedMethods は省略時の標準値を補った昼夜の手法IDを返します。
//
// 引数: cfg(config.Config)。返り値: config.Methodsは保存・照合に使う明示IDです。
func resolvedMethods(cfg config.Config) config.Methods {
	return config.Methods{Day: cfg.MethodID("Day"), Night: cfg.MethodID("Night")}
}

// previousMethods は旧手法だけを現設定へ戻した署名で、手法以外が変わっていないことを確かめます。
//
// 引数: m(*marketRuntime)、cfg(config.Config)、old(*strategy.Portfolio)は旧戦略で未準備ならnilです。
// 返り値: config.Methodsは署名照合済みの旧ID、errorは欠落・不整合・手法以外の変更です。
// 昼夜IDを保存していない旧形式では、当場の手法変更と標準組合せまたは同一研究手法だけを検証します。
func previousMethods(m *marketRuntime, cfg config.Config, old *strategy.Portfolio) (config.Methods, error) {
	previous := config.Methods{}
	if m.Saved.Methods != nil {
		previous = *m.Saved.Methods
	} else {
		if old == nil {
			return previous, fmt.Errorf("%s 旧状態に昼夜の手法を照合できる情報がありません", m.Reference)
		}
		if method := old.Config.Parameters.Method; method != "" {
			previous = config.Methods{Day: method, Night: method}
		} else {
			legacy := cfg
			legacy.Methods = config.Methods{}
			previous = resolvedMethods(legacy)
		}
		oldCurrent := previous.Day
		if old.Session.Kind == "Night" {
			oldCurrent = previous.Night
		}
		if oldCurrent == cfg.MethodID(old.Session.Kind) {
			return previous, fmt.Errorf("%s 旧形式では反対セッションだけの手法変更を確認できません。元のconfで一度起動・停止して昼夜IDを保存してください", m.Reference)
		}
	}
	for _, item := range []struct{ kind, id string }{{"Day", previous.Day}, {"Night", previous.Night}} {
		if _, err := strategy.ParametersForMethod(m.Reference, item.kind, item.id); err != nil {
			return previous, fmt.Errorf("%s 保存した昼夜の手法が不正です: %w", m.Reference, err)
		}
	}
	oldCfg := cfg
	oldCfg.Methods = previous
	if oldCfg.Signature() != m.Saved.Signature {
		return previous, fmt.Errorf("%s 手法以外の設定変更、または保存した昼夜の手法・設定指紋の不整合があります。元のconfと照合してください", m.Reference)
	}
	if previous == resolvedMethods(cfg) {
		return previous, fmt.Errorf("%s 昼夜の手法が変わっていません。元の設定と照合してください", m.Reference)
	}
	if old != nil {
		method := previous.Day
		if old.Session.Kind == "Night" {
			method = previous.Night
		}
		params, err := strategy.ParametersForMethod(m.Reference, old.Session.Kind, method)
		if err != nil || params.Method != old.Config.Parameters.Method {
			return previous, fmt.Errorf("%s 保存した昼夜の手法と旧戦略の手法が一致しません", m.Reference)
		}
	}
	return previous, nil
}

// ----------------------------------------

// resetChangedStrategy は建玉・予約・未反映約定が全てない場合だけ、新手法へ移行します。
//
// 引数: m(*marketRuntime)、cfg(config.Config)、previous([]byte)。返り値: errorは旧状態を残すべき矛盾です。
func resetChangedStrategy(m *marketRuntime, cfg config.Config, previous []byte) error {
	if cfg.MethodID("Day") != "TG04" && cfg.MethodID("Day") != "PG04" && cfg.MethodID("Night") != "TG04" && cfg.MethodID("Night") != "PG04" {
		return fmt.Errorf("%s 保存状態と戦略・設定が異なります。元の設定と照合してください", m.Reference)
	}
	if m.Saved.Version != 1 || m.Saved.Algorithm != algorithmVersion {
		return fmt.Errorf("%s 保存状態の版が不明なため自動移行できません", m.Reference)
	}
	if len(m.Manager.Positions()) != 0 || m.Manager.HasPending() || len(m.Manager.OutstandingFills()) != 0 || len(m.Saved.Queue) != 0 {
		return fmt.Errorf("%s 未返済・未確定・未反映の売買が残るため手法を切り替えられません。元のconfで照合・返済してください。DBは削除しないでください", m.Reference)
	}
	var old *strategy.Portfolio
	if raw := bytes.TrimSpace(m.Saved.Portfolio); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var err error
		old, err = strategy.Restore(raw)
		if err != nil {
			return err
		}
		status := old.Status()
		if len(status.Pending) > 0 {
			return fmt.Errorf("%s 旧戦略の注文予約が残っています", m.Reference)
		}
		for _, pos := range status.Positions {
			if pos.Quantity != 0 {
				return fmt.Errorf("%s 旧戦略と約定台帳の残数が一致しません", m.Reference)
			}
		}
	}
	previousIDs, err := previousMethods(m, cfg, old)
	if err != nil {
		return err
	}
	namespace, err := newNamespace()
	if err != nil {
		return err
	}
	methods := resolvedMethods(cfg)
	if err = m.Manager.Event("保有ゼロでの手法切替", m.Saved.Plan.ID, m.Reference, "", time.Now(), map[string]any{"previous_state": string(previous), "previous_methods": previousIDs, "new_signature": cfg.Signature(), "day_method": methods.Day, "night_method": methods.Night, "new_namespace": namespace}); err != nil {
		return err
	}
	m.Saved = savedRuntime{Version: 1, Algorithm: algorithmVersion, Signature: cfg.Signature(), Namespace: namespace, Methods: &methods, Done: map[string]bool{}}
	m.Portfolio = nil
	return nil
}
