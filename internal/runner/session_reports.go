package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// decisionMethod は新規・返済の判断を発生させた時点の手法IDを確定します。
//
// 引数: m(*marketRuntime)は現在の手法と時間枠です。
// 返り値: stringは銘柄接頭辞を含む手法IDです。静観への切替返済も旧手法を記録します。
func (a *app) decisionMethod(m *marketRuntime) string {
	if m.Saved.ActiveSlot != nil && m.Saved.ActiveSlot.Enabled && m.Saved.ActiveSlot.Method != "" {
		return config.NameOfMethod(m.Reference, m.Saved.ActiveSlot.Method)
	}
	if m.Portfolio != nil && m.Portfolio.Rules != nil {
		return config.NameOfMethod(m.Reference, m.Portfolio.Rules.Set.Name())
	}
	return a.cfg.MethodName(m.Saved.Plan.Kind)
}

// queuedMethod は保存済みの判断根拠から当時のIDを読み、現在の時間枠で上書きしません。
//
// 引数: evidence(json.RawMessage)は待ち列へ保存した判断根拠です。
// 返り値: stringは当時のIDです。旧形式でも元のrules_profileを使い、不明なら空です。
func queuedMethod(evidence json.RawMessage) string {
	var value struct {
		MethodID string `json:"method_id"`
		Profile  string `json:"rules_profile"`
	}
	if json.Unmarshal(evidence, &value) != nil {
		return ""
	}
	if value.MethodID != "" {
		return value.MethodID
	}
	if name := rules.Name(value.Profile); name != "" {
		return name
	}
	return value.Profile
}

// finalizeSessionReports は終了CSVと場中の小容量Viewer資料を売買ループと独立して再試行します。
//
// 引数: now(time.Time)は現在時計です。
// 返り値: なし。派生出力の障害で保有玉の監視を止めず、失敗の変化を標準出力へ残します。
func (a *app) finalizeSessionReports(now time.Time) {
	if now.Before(a.nextSessionReport) {
		return
	}
	a.nextSessionReport = now.Add(5 * time.Second)
	message := ""
	for _, m := range a.markets {
		if m.Manager == nil {
			continue
		}
		logRoot := a.cfg.Path(a.cfg.Runtime.LogDirectory)
		csvErr := m.Manager.FinalizeReports(logRoot, now)
		viewerErr := m.Manager.PublishViewer(logRoot, now)
		if err := errors.Join(csvErr, viewerErr); err != nil {
			message += fmt.Sprintf("%s: %v; ", m.Reference, err)
		}
	}
	if message != a.sessionReportError {
		if message != "" {
			fmt.Printf("%s [セッションレポート保存待ち] %s\n", now.Format(time.RFC3339), message)
		} else {
			fmt.Printf("%s [セッションレポート保存復旧]\n", now.Format(time.RFC3339))
		}
		a.sessionReportError = message
	}
}
