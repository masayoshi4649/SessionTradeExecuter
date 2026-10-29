package strategy

import "github.com/masayoshi4649/SessionTradeExecuter/internal/rules"

// bindRules は選択手法の全担当を同じ条件・エラー監視へ接続します。
//
// 引数: なし。返り値: errorは保存された式の不整合です。
func (p *Portfolio) bindRules() error {
	profile, err := portfolioRuleProfile(p.Config, p.Session)
	if err != nil {
		return err
	}
	runtime, err := rules.NewRuntimeFor(profile, p.Config.RuleExpressions)
	if err != nil {
		return err
	}
	p.Rules = runtime
	if p.Paper != nil {
		p.Paper.Rules = runtime
	}
	if p.Research != nil {
		for _, sleeve := range p.Research.Sleeves {
			sleeve.Source.attachRules(runtime)
		}
	}
	return nil
}

// bindRules は独立した担当の照合でも同じ外部条件を読み込みます。
//
// 引数: なし。返り値: errorは条件の不整合です。
func (s *ResearchSource) bindRules() error {
	profile, err := portfolioRuleProfile(s.Config, s.Session)
	if err != nil {
		return err
	}
	runtime, err := rules.NewRuntimeFor(profile, s.Config.RuleExpressions)
	if err != nil {
		return err
	}
	s.attachRules(runtime)
	return nil
}

// attachRules は保存対象外の評価器を担当へ接続します。
//
// 引数: runtime(*rules.Runtime)は一戦略専用の評価状態。返り値: なし。
func (s *ResearchSource) attachRules(runtime *rules.Runtime) {
	s.Rules = runtime
	if s.Active145 != nil {
		s.Active145.Rules, s.Active145.D07Rules = runtime, true
	}
	if s.Config.Parameters.Method == "TG04" || s.Config.Parameters.Method == "PG04" {
		if s.Active != nil {
			s.Active.Rules, s.Active.ResearchRules = runtime, true
		}
		if s.Episode != nil {
			s.Episode.Rules, s.Episode.ResearchRules = runtime, true
		}
	}
	// 独立した夜間episodeはCS04のままで、追加担当の入口式は適用しません。
}

// portfolioRuleProfile は内部の空Methodも対応する既存手法名へ解決します。
//
// 引数: cfg(Config)、session(Session)。返り値: stringとerrorです。
func portfolioRuleProfile(cfg Config, session Session) (string, error) {
	method := cfg.Parameters.Method
	if method == "" {
		method = "T06"
		if session.Kind == "Night" {
			if cfg.Reference == "NK225" {
				method = "U06"
			} else {
				method = "T04"
			}
		}
	}
	return rules.SelectProfile(cfg.Reference, session.Kind, method)
}
