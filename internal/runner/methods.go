package runner

import (
	"fmt"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// methodParameters は選択手法へ時刻設定と旧形式の上書きを適用します。
//
// 引数: cfg(config.Config) は運転設定、reference/kind(string) は売買市場と昼夜です。
// 返り値: strategy.Parameters は実効設定、error は手法IDまたは上書きの不整合です。
func methodParameters(cfg config.Config, reference, kind string) (strategy.Parameters, error) {
	return methodParametersFor(cfg, reference, kind, cfg.MethodID(kind))
}

// methodParametersFor は時間帯で指定した手法へ既存の設定検査を適用します。
//
// 引数: cfg(config.Config)、reference/kind/method(string)。
// 返り値: strategy.Parameters は実効設定、error は指定または係数の不整合です。
func methodParametersFor(cfg config.Config, reference, kind, method string) (strategy.Parameters, error) {
	id, err := config.ShortMethodID(reference, method)
	if err != nil {
		return strategy.Parameters{}, err
	}
	parameters, err := strategy.ParametersForMethod(reference, kind, id)
	if err != nil {
		return strategy.Parameters{}, err
	}
	if parameters.Method != "" && len(cfg.Algorithm) > 0 {
		return strategy.Parameters{}, fmt.Errorf("%sは固定仕様です。旧algorithm係数の上書きは外してください", parameters.Method)
	}
	parameters.Extra.EntryCutoffSeconds = cfg.Schedule.ExtraCutoffSeconds
	if err := strategy.ApplyOverrides(&parameters, cfg.Algorithm); err != nil {
		return strategy.Parameters{}, err
	}
	return parameters, nil
}

// validateMethodSelection は接続・台帳変更より前に昼夜両方の手法指定を確認します。
//
// 引数: cfg(config.Config) は単一商品に正規化済みの設定です。
// 返り値: error は未対応IDまたは旧上書きの不整合です。
func validateMethodSelection(cfg config.Config) error {
	reference, _ := cfg.Selected()
	baseline := map[string]strategy.Parameters{}
	for _, kind := range []string{"Day", "Night"} {
		parameters, err := methodParameters(cfg, reference, kind)
		if err != nil {
			return err
		}
		baseline[kind] = parameters
	}
	for _, window := range cfg.Windows {
		if window.Method == "" {
			continue
		}
		parameters, err := methodParametersFor(cfg, reference, window.Session, window.Method)
		if err != nil {
			return err
		}
		if err = validateSharedFeatureParameters(baseline[window.Session], parameters); err != nil {
			return fmt.Errorf("時間帯%s: %w", window.ID, err)
		}
	}
	return nil
}

// validateSharedFeatureParameters は共通指標を継続できない異なる設定の手法併用を拒否します。
//
// 引数: baseline/candidate(strategy.Parameters)。返り値: error は指標設定の差、同じならnilです。
func validateSharedFeatureParameters(baseline, candidate strategy.Parameters) error {
	if baseline.Features != candidate.Features {
		return fmt.Errorf("時間帯の手法ごとに異なる指標設定は共用できません。専用の履歴準備が必要です")
	}
	return nil
}

// researchFeaturesRequired は当場の時間帯で一度でも必要になる研究特徴を準備対象に含めます。
//
// 引数: cfg(config.Config)、reference/kind(string)。返り値: bool は研究特徴の必要性、error は手法の不整合です。
func researchFeaturesRequired(cfg config.Config, reference, kind string) (bool, error) {
	baseline, err := methodParameters(cfg, reference, kind)
	if err != nil {
		return false, err
	}
	required := baseline.Method != ""
	for _, window := range cfg.Windows {
		if window.Session != kind || window.Method == "" {
			continue
		}
		candidate, err := methodParametersFor(cfg, reference, kind, window.Method)
		if err != nil {
			return false, err
		}
		if err = validateSharedFeatureParameters(baseline, candidate); err != nil {
			return false, err
		}
		required = required || candidate.Method != ""
	}
	return required, nil
}
