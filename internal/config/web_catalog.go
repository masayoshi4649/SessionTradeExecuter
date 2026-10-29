package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// RuleChoice は実ファイルから得た手法候補と選択可能な昼夜です。
type RuleChoice struct {
	Name       string `json:"name"`
	Reference  string `json:"reference"`
	File       string `json:"file"`
	Day        bool   `json:"day"`
	Night      bool   `json:"night"`
	Supported  bool   `json:"supported"`
	Reason     string `json:"reason"`
	WindowOnly bool   `json:"window_only"`
	WindowHint string `json:"window_hint"`
}

// ListRuleChoices は銘柄接頭辞のある実TOMLを列挙し、内容を実行エンジンの型で検査します。
//
// 引数: cfg Configは外部ルールの保存先を含みます。
// 返り値: []RuleChoiceは名前順の候補、errorはフォルダ列挙失敗です。不正・未実装のファイルも理由付きで返します。
func ListRuleChoices(cfg Config) ([]RuleChoice, error) {
	directory := cfg.RuleEngine.Directory
	if directory == "" {
		if cfg.RuleEngine.File != "" {
			directory = filepath.Dir(cfg.RuleEngine.File)
		} else {
			directory = filepath.Join("conf", "rules")
			if info, err := os.Stat(cfg.Path("rules")); err == nil && info.IsDir() {
				directory = "rules"
			}
		}
	}
	entries, err := os.ReadDir(cfg.Path(directory))
	if os.IsNotExist(err) {
		return []RuleChoice{}, nil
	}
	if err != nil {
		return nil, err
	}
	profiles := map[string]string{}
	for _, profile := range rules.ProfileNames() {
		profiles[rules.Name(profile)] = profile
	}
	choices := []RuleChoice{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".toml" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".toml")
		reference, _, found := strings.Cut(name, "_")
		if !found || (reference != "NK225" && reference != "TOPIX") {
			continue
		}
		choice := RuleChoice{Name: name, Reference: reference, File: entry.Name()}
		profile, known := profiles[name]
		if !known {
			choice.Reason = "この名前の売買手法は実行エンジンに未実装です。式ファイルの複製だけでは選択できません"
		} else if _, err := rules.LoadFor(filepath.Join(cfg.Path(directory), entry.Name()), profile); err != nil {
			choice.Reason = fmt.Sprintf("ルール本文を読み込めません: %v", err)
		} else {
			choice.Supported = true
			_, dayErr := rules.SelectProfile(reference, "Day", name)
			_, nightErr := rules.SelectProfile(reference, "Night", name)
			choice.Day, choice.Night = dayErr == nil, nightErr == nil
			if profile == "p05" || profile == "p164_06" {
				choice.WindowOnly = true
				choice.WindowHint = "夜間開始から米国開始15分前まで。時間帯枠で指定してください"
			}
			if profile == "u165_03" {
				choice.WindowOnly = true
				choice.WindowHint = "米国開始15分後から夜間終了まで。時間帯枠で指定してください"
			}
		}
		choices = append(choices, choice)
	}
	sort.Slice(choices, func(i, j int) bool { return choices[i].Name < choices[j].Name })
	return choices, nil
}

// reloadChangedWebRules は変更した手法だけを実ファイルで検査し、他の起動済み条件を保持します。
//
// 引数: previous Configは現在採用中、candidate *Configは検査済み更新候補です。
// 返り値: errorは変更先ファイルの読取・型不整合です。OFF操作を無関係な外部編集で妨げません。
func reloadChangedWebRules(previous Config, candidate *Config) error {
	if candidate.RuleEngine.Directory == "" {
		return nil
	}
	before := map[string]Instrument{}
	for _, item := range previous.Instruments {
		before[item.Product] = item
	}
	sets := make(map[string]*rules.Set, len(candidate.RuleSets))
	for name, set := range candidate.RuleSets {
		sets[name] = set
	}
	for _, item := range candidate.Instruments {
		old := before[item.Product]
		ref := Reference(item.Product)
		previousProfiles := map[string]bool{}
		for _, part := range instrumentMethods(old) {
			if part.Method != "" {
				previousProfiles[part.Session+"/"+NameOfMethod(ref, part.Method)] = true
			}
		}
		for _, part := range instrumentMethods(item) {
			if part.Method == "" || previousProfiles[part.Session+"/"+NameOfMethod(ref, part.Method)] {
				continue
			}
			profile, err := rules.SelectProfile(ref, part.Session, part.Method)
			if err != nil {
				return err
			}
			set, err := rules.LoadFor(filepath.Join(candidate.Path(candidate.RuleEngine.Directory), rules.FileName(profile)), profile)
			if err != nil {
				return fmt.Errorf("変更先のルール%sを読み込めません: %w", part.Method, err)
			}
			sets[profile] = set
		}
	}
	candidate.RuleSets = sets
	if set, ok := sets["d07"]; ok {
		candidate.RuleSet = set
	}
	return nil
}
