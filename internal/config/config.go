// Package config は接続・数量・時間・保存先をTOMLから読み込みます。
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// Config は実行設定です。Rootは設定フォルダの親で、相対保存先の基準です。
type Config struct {
	SettingsVersion int                   `toml:"settings_version" json:"-"`
	Startup         Startup               `toml:"startup"`
	Controller      Controller            `toml:"controller"`
	Trading         Trading               `toml:"trading"`
	Runtime         Runtime               `toml:"runtime"`
	Schedule        Schedule              `toml:"schedule"`
	Methods         Methods               `toml:"methods"`
	Windows         []MethodWindow        `toml:"windows" json:"windows,omitempty"`
	Market          *SingleMarket         `toml:"market"`
	Markets         map[string]Market     `toml:"markets"`
	Products        map[string]Product    `toml:"products"`
	Algorithm       map[string]float64    `toml:"algorithm"`
	Web             *Web                  `toml:"web"`
	Instruments     []Instrument          `toml:"instruments"`
	RuleEngine      RuleEngine            `toml:"rule_engine"`
	RuleSet         *rules.Set            `toml:"-" json:"-"`
	RuleSets        map[string]*rules.Set `toml:"-" json:"-"`
	Root            string                `toml:"-" json:"-"`
	WebRevision     string                `toml:"-" json:"-"`
}

// Startup は引数を使わず選択する起動動作です。戦略の復元指紋には含めません。
type Startup struct {
	Action            string  `toml:"action"`
	DurationSeconds   float64 `toml:"duration_seconds"`
	LegacyConf        string  `toml:"legacy_conf"`
	DiagnosticSession string  `toml:"diagnostic_session"`
}

type Controller struct {
	BaseURL               string  `toml:"base_url"`
	RequestTimeoutSeconds float64 `toml:"request_timeout_seconds"`
	HistoryTimeoutSeconds float64 `toml:"history_timeout_seconds"`
	AccountPollSeconds    float64 `toml:"account_poll_seconds"`
	CandlePollSeconds     float64 `toml:"candle_poll_seconds"`
	ReconnectSeconds      float64 `toml:"reconnect_seconds"`
}

type Trading struct {
	DryRun     bool   `toml:"dryrun"`
	InstanceID string `toml:"instance_id"`
}

type Runtime struct {
	DataDirectory            string  `toml:"data_directory"`
	LogDirectory             string  `toml:"log_directory"`
	QuoteMaxAgeSeconds       float64 `toml:"quote_max_age_seconds"`
	AccountMaxAgeSeconds     float64 `toml:"account_max_age_seconds"`
	ReconnectGraceSeconds    float64 `toml:"reconnect_grace_seconds"`
	CheckpointSeconds        float64 `toml:"checkpoint_seconds"`
	StatusSeconds            float64 `toml:"status_seconds"`
	ShutdownSeconds          float64 `toml:"shutdown_seconds"`
	EntryOrderTimeoutSeconds float64 `toml:"entry_order_timeout_seconds"`
}

type Schedule struct {
	ForceCloseBeforeSeconds float64 `toml:"force_close_before_seconds"`
	ExtraCutoffSeconds      float64 `toml:"extra_cutoff_seconds"`
	DayExtra                string  `toml:"day_extra"`
	USOpen                  string  `toml:"us_open"`
	USExtraDelayMinutes     int     `toml:"us_extra_delay_minutes"`
	LondonExtra             string  `toml:"london_extra"`
	DayExtraJST             string  `toml:"day_extra_jst" json:"-"`
	USOpenJST               string  `toml:"us_open_jst" json:"-"`
	LondonExtraJST          string  `toml:"london_extra_jst" json:"-"`
}

// SingleMarket はこのプロセスで売買する単一商品の設定です。
type SingleMarket struct {
	Product       string `toml:"product"`
	MaxPerSide    int    `toml:"max_per_side"`
	CoreQuantity  int    `toml:"core_quantity"`
	ExtraQuantity int    `toml:"extra_quantity"`
}

// Market は正規化した発注商品と旧設定との互換用です。Enabled は常に一件だけです。
type Market struct {
	Enabled       bool   `toml:"enabled"`
	Product       string `toml:"product"`
	MaxPerSide    int    `toml:"max_per_side"`
	CoreQuantity  int    `toml:"core_quantity"`
	ExtraQuantity int    `toml:"extra_quantity"`
}

// Product はcontrollerから取得できない商品規格です。銘柄IDや限月は持ちません。
type Product struct {
	Tick       float64 `toml:"tick"`
	PointValue float64 `toml:"point_value"`
}

// Defaults は安全な初期設定を返します。
//
// 引数: なし。
// 返り値: Config。銘柄・枚数は設定ファイルで明示します。
func Defaults() Config {
	return Config{
		Startup: Startup{Action: "run"},
		Controller: Controller{BaseURL: "http://10.10.100.1:8080", RequestTimeoutSeconds: 15,
			HistoryTimeoutSeconds: 180, AccountPollSeconds: 2, CandlePollSeconds: 10, ReconnectSeconds: 3},
		Trading: Trading{DryRun: true},
		Runtime: Runtime{DataDirectory: "data", LogDirectory: "log", QuoteMaxAgeSeconds: 30,
			AccountMaxAgeSeconds: 15, ReconnectGraceSeconds: 180, CheckpointSeconds: 5,
			StatusSeconds: 30, ShutdownSeconds: 30, EntryOrderTimeoutSeconds: 15},
		Schedule: Schedule{ForceCloseBeforeSeconds: 600, ExtraCutoffSeconds: 600, DayExtra: "12:30",
			USOpen: "09:30", USExtraDelayMinutes: 30, LondonExtra: "16:30"},
		Markets: make(map[string]Market), Products: make(map[string]Product),
		Algorithm: make(map[string]float64),
	}
}

// Load はconf直下の*.tomlを名前順に読み込み、重複テーブル・未知キーを拒否します。
//
// 引数: directory(string)は設定フォルダ。
// 返り値: Configとerror。*.toml.sampleは読みません。
func Load(directory string) (Config, error) {
	c := Defaults()
	abs, err := filepath.Abs(directory)
	if err != nil {
		return c, err
	}
	files, err := filepath.Glob(filepath.Join(abs, "*.toml"))
	if err != nil {
		return c, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return c, fmt.Errorf("設定フォルダに*.tomlがありません: %s", abs)
	}
	var text strings.Builder
	sources := make(map[string][]byte, len(files))
	for _, name := range files {
		data, e := os.ReadFile(name)
		if e != nil {
			return c, e
		}
		sources[filepath.Base(name)] = data
		if filepath.Base(name) == webOverlayName {
			continue
		}
		text.WriteString(strings.TrimPrefix(string(data), "\ufeff"))
		text.WriteByte('\n')
	}
	meta, err := toml.Decode(text.String(), &c)
	if err != nil {
		return c, fmt.Errorf("TOMLを解析できません: %w", err)
	}
	if unknown := meta.Undecoded(); len(unknown) > 0 {
		return c, fmt.Errorf("未対応の設定キー: %v", unknown)
	}
	if err := c.normalizeInput(meta); err != nil {
		return c, err
	}
	c.Root = filepath.Dir(abs)
	if overlay, ok := sources[webOverlayName]; ok {
		if c.Web == nil {
			return c, fmt.Errorf("%sは[web]を持つ設定だけで使用できます", webOverlayName)
		}
		var settings WebSettings
		meta, err := toml.Decode(string(overlay), &settings)
		if err != nil || len(meta.Undecoded()) != 0 {
			return c, fmt.Errorf("%sの設定が不正です: %v / %v", webOverlayName, err, meta.Undecoded())
		}
		for _, item := range settings.Instruments {
			if item.Mode != "off" && item.Mode != "dryrun" && item.Mode != "live" {
				return c, fmt.Errorf("%sの商品別modeはoff/dryrun/liveを明示してください", webOverlayName)
			}
		}
		c = applyWebFields(c, settings)
		// 旧保存ファイルが新しい配布本文を上書きする場合も、その保存版を基準に移行します。
		c.SettingsVersion = settings.SettingsVersion
	}
	c.WebRevision = revisionOf(sources)
	if err := c.loadRules(); err != nil {
		return c, err
	}
	// 改変式を先に照合し、標準式の旧昼夜設定だけを標準時間帯へ移行します。
	c.migrateLegacyD07Windows(true)
	if c.RuleEngine.File == "" && c.RuleEngine.Directory == "" {
		if err := c.bindDefaultRules(); err != nil {
			return c, err
		}
	}
	c.migrateStandardDayS01()
	if err := c.migrateSelectedMethods(); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// Validate は数量・有限値・設定間の不整合を検査します。
//
// 引数: なし。受け手Configを検査します。
// 返り値: error。不正なら起動前に停止します。
func (c *Config) Validate() error {
	if c.SettingsVersion < 0 || c.SettingsVersion > currentSettingsVersion {
		return fmt.Errorf("settings_versionがこのアプリの対応範囲外です: %d", c.SettingsVersion)
	}
	if c.RuleEngine.File != "" && c.RuleEngine.Directory != "" {
		return fmt.Errorf("rule_engineはdirectoryか旧fileの一方だけを指定してください")
	}
	if c.RuleEngine.File == "" && c.RuleEngine.Directory == "" {
		if err := c.bindDefaultRules(); err != nil {
			return err
		}
	}
	if c.RuleEngine.File != "" && c.RuleSet == nil {
		return fmt.Errorf("外部ルールが未読込です。confから読み直してください")
	}
	if c.RuleEngine.Directory != "" && c.RuleSets == nil {
		return fmt.Errorf("外部ルールフォルダが未読込です")
	}
	if c.Web != nil {
		_, err := c.WorkerConfigs()
		return err
	}
	if len(c.Instruments) > 0 {
		return fmt.Errorf("instrumentsを使用する場合は[web]で待受先を指定してください")
	}
	if err := c.Schedule.NormalizeJST(); err != nil {
		return err
	}
	switch c.Startup.Action {
	case "run", "check", "probe", "migrate_legacy", "diagnostics":
	default:
		return fmt.Errorf("startup.actionはrun/check/probe/migrate_legacy/diagnosticsから一つ指定してください")
	}
	if c.Startup.DiagnosticSession != "" {
		if c.Startup.Action != "diagnostics" || !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}_(Day|Night)$`).MatchString(c.Startup.DiagnosticSession) {
			return fmt.Errorf("startup.diagnostic_sessionはaction=diagnosticsでYYYY-MM-DD_DayまたはNightを指定してください")
		}
		if _, err := time.Parse("2006-01-02", c.Startup.DiagnosticSession[:10]); err != nil {
			return fmt.Errorf("startup.diagnostic_sessionの日付が不正です")
		}
	}
	if math.IsNaN(c.Startup.DurationSeconds) || math.IsInf(c.Startup.DurationSeconds, 0) ||
		c.Startup.DurationSeconds < 0 || c.Startup.DurationSeconds > 9223372036 {
		return fmt.Errorf("startup.duration_secondsは0〜9223372036の有限秒数にしてください")
	}
	if c.Startup.DurationSeconds > 0 && (c.Startup.Action != "run" || !c.Trading.DryRun) {
		return fmt.Errorf("startup.duration_secondsの正数指定はaction=runかつdryrun=trueの場合だけ使用できます")
	}
	if (c.Startup.Action == "migrate_legacy") != (strings.TrimSpace(c.Startup.LegacyConf) != "") {
		return fmt.Errorf("startup.legacy_confはaction=migrate_legacyの場合だけ必須です。それ以外は空文字にしてください")
	}
	if c.Startup.LegacyConf != strings.TrimSpace(c.Startup.LegacyConf) {
		return fmt.Errorf("startup.legacy_confの先頭・末尾に空白を付けないでください")
	}
	u, err := url.Parse(c.Controller.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base_urlは認証情報やAPIパスを含まないhttp(s) URLにしてください")
	}
	c.Controller.BaseURL = strings.TrimRight(c.Controller.BaseURL, "/")
	for name, value := range map[string]float64{
		"要求期限": c.Controller.RequestTimeoutSeconds, "履歴取得期限": c.Controller.HistoryTimeoutSeconds,
		"口座照会間隔": c.Controller.AccountPollSeconds, "足照会間隔": c.Controller.CandlePollSeconds,
		"再接続間隔": c.Controller.ReconnectSeconds, "気配鮮度": c.Runtime.QuoteMaxAgeSeconds,
		"口座鮮度": c.Runtime.AccountMaxAgeSeconds, "再接続猶予": c.Runtime.ReconnectGraceSeconds,
		"状態保存間隔": c.Runtime.CheckpointSeconds, "状況表示間隔": c.Runtime.StatusSeconds,
		"停止猶予": c.Runtime.ShutdownSeconds, "新規注文期限": c.Runtime.EntryOrderTimeoutSeconds,
		"終了前返済": c.Schedule.ForceCloseBeforeSeconds, "追加入口期限": c.Schedule.ExtraCutoffSeconds,
	} {
		if !positive(value) {
			return fmt.Errorf("%sは正の有限秒数にしてください", name)
		}
	}
	if c.Runtime.ReconnectGraceSeconds < c.Runtime.QuoteMaxAgeSeconds {
		return fmt.Errorf("再接続猶予は気配鮮度以上にしてください")
	}
	if c.Schedule.ForceCloseBeforeSeconds < 30 || c.Schedule.ForceCloseBeforeSeconds > 3600 {
		return fmt.Errorf("終了前返済は30〜3600秒にしてください")
	}
	for _, clock := range []string{c.Schedule.DayExtra, c.Schedule.USOpen, c.Schedule.LondonExtra} {
		if _, e := time.Parse("15:04", clock); e != nil {
			return fmt.Errorf("時計はHH:MMで指定してください: %s", clock)
		}
	}
	if c.Schedule.USExtraDelayMinutes < 0 || c.Schedule.USExtraDelayMinutes > 240 {
		return fmt.Errorf("米国開始後の追加待機は0〜240分にしてください")
	}
	if c.Runtime.DataDirectory == "" || c.Runtime.LogDirectory == "" {
		return fmt.Errorf("data_directoryとlog_directoryを指定してください")
	}
	if c.Market != nil {
		if len(c.Markets) != 0 {
			return fmt.Errorf("[market]と旧[markets]は同時指定できません。売買する商品を一つだけ指定してください")
		}
		market := *c.Market
		ref := Reference(market.Product)
		if ref == "" {
			return fmt.Errorf("market.productはTOPIX/TOPIXmini/NK225/NK225mini/NK225microから一つ指定してください")
		}
		c.Markets = map[string]Market{ref: {Enabled: true, Product: market.Product,
			MaxPerSide: market.MaxPerSide, CoreQuantity: market.CoreQuantity, ExtraQuantity: market.ExtraQuantity}}
		c.Market = nil
	}
	enabled := 0
	selected := make(map[string]Market)
	for ref, market := range c.Markets {
		if ref != "TOPIX" && ref != "NK225" {
			return fmt.Errorf("観測市場はTOPIX/NK225のみです: %s", ref)
		}
		if !market.Enabled {
			continue
		}
		enabled++
		if Reference(market.Product) != ref {
			return fmt.Errorf("%sの発注商品が異なる指数系統です", ref)
		}
		// 0 は担当停止ではなく、片方向上限の半分を割り当てる指定です。
		automatic := market.MaxPerSide / 2
		if automatic < 1 {
			automatic = 1
		}
		if market.CoreQuantity == 0 {
			market.CoreQuantity = automatic
		}
		if market.ExtraQuantity == 0 {
			market.ExtraQuantity = automatic
		}
		selected[ref] = market
		if market.MaxPerSide < 1 || market.MaxPerSide > 1000000 ||
			market.CoreQuantity < 1 || market.ExtraQuantity < 1 ||
			market.CoreQuantity > market.MaxPerSide || market.ExtraQuantity > market.MaxPerSide {
			return fmt.Errorf("%sの初回/追加枚数は1以上、片方向上限以下にしてください", ref)
		}
		if p, ok := c.Products[market.Product]; !ok || !positive(p.Tick) || !positive(p.PointValue) {
			return fmt.Errorf("%sの商品規格がありません", market.Product)
		}
	}
	if enabled == 0 {
		return fmt.Errorf("[market]で売買する商品を一つ指定してください。旧[markets]ではenabled=trueが一件必要です")
	}
	if enabled != 1 {
		return fmt.Errorf("このアプリは1プロセス1銘柄です。旧[markets]でenabled=trueが複数あります。銘柄別の設定フォルダで別々に起動してください")
	}
	c.Markets = selected
	reference, _ := c.Selected()
	for _, method := range []string{c.Methods.Day, c.Methods.Night} {
		if _, err := methodShortID(reference, method); err != nil {
			return fmt.Errorf("methods.day/night: %w", err)
		}
	}
	c.Methods = Methods{Day: c.MethodID("Day"), Night: c.MethodID("Night")}
	if err := c.ValidateMethodWindows(); err != nil {
		return err
	}
	if c.Trading.InstanceID == "" {
		_, market := c.Selected()
		c.Trading.InstanceID = market.Product
	}
	c.Trading.InstanceID = strings.ToLower(c.Trading.InstanceID)
	if !validInstanceID(c.Trading.InstanceID) {
		return fmt.Errorf("trading.instance_idはASCII英数・_・-の1〜64文字で指定してください。Windows予約名は使用できません")
	}
	for _, ref := range []string{"TOPIX", "NK225"} {
		p, ok := c.Products[ref]
		if !ok || !positive(p.Tick) || !positive(p.PointValue) {
			return fmt.Errorf("観測市場%sの商品規格がありません", ref)
		}
	}
	for key, value := range c.Algorithm {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("algorithm.%sは有限値にしてください", key)
		}
	}
	return nil
}

// ----------------------------------------

// Selected はこのプロセスが売買する一商品の設定を返します。
//
// 引数: なし。検証済みのConfigを参照します。
// 返り値: stringは判断用ラージ市場、Marketは発注商品の設定です。未設定では空値です。
func (c Config) Selected() (string, Market) {
	for _, reference := range []string{"TOPIX", "NK225"} {
		if market, ok := c.Markets[reference]; ok && market.Enabled {
			return reference, market
		}
	}
	return "", Market{}
}

// InstanceID は実行主体の識別子を返します。
//
// 引数: なし。受け手の設定を参照します。
// 返り値: stringは明示した識別子、未指定なら売買する商品名です。
func (c Config) InstanceID() string {
	if c.Trading.InstanceID != "" {
		return strings.ToLower(c.Trading.InstanceID)
	}
	_, market := c.Selected()
	return strings.ToLower(market.Product)
}

// validInstanceID は識別子を安全なファイル名成分として検査します。
//
// 引数: value string は設定された識別子です。
// 返り値: boolはパス区切り・非ASCII・Windows予約名を含まない場合にtrueです。
func validInstanceID(value string) bool {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(value) {
		return false
	}
	upper := strings.ToUpper(value)
	switch upper {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	return !(len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9')
}

// Reference は発注商品の観測ラージを返します。
//
// 引数: product(string)は5種類の発注商品コード。
// 返り値: string。不明商品は空文字です。
func Reference(product string) string {
	switch product {
	case "TOPIX", "TOPIXmini":
		return "TOPIX"
	case "NK225", "NK225mini", "NK225micro":
		return "NK225"
	}
	return ""
}

// Path は設定フォルダの親を基準に保存先を解決します。
//
// 引数: name(string)は絶対または相対パス。
// 返り値: stringは解決済みパスです。
func (c Config) Path(name string) string {
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Join(c.Root, name)
}

// Signature は戦略復元時の設定照合に使う指紋を返します。
//
// 引数: なし。受け手の設定を参照します。
// 返り値: stringはSHA256です。保存先と表示周期は売買条件に含めません。
func (c Config) Signature() string {
	value := struct {
		URL       string
		Trading   Trading
		Schedule  Schedule
		Markets   map[string]Market
		Products  map[string]Product
		Algorithm map[string]float64
	}{c.Controller.BaseURL, c.Trading, c.Schedule, c.Markets, c.Products, c.Algorithm}
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	legacy := hex.EncodeToString(digest[:])
	if c.RuleSets != nil {
		digest = sha256.Sum256([]byte(legacy + "/expr-profiles-v1/" + c.RuleHash()))
		legacy = hex.EncodeToString(digest[:])
	} else if c.RuleSet != nil {
		digest = sha256.Sum256([]byte(legacy + "/expr-v1/" + c.RuleSet.Hash()))
		legacy = hex.EncodeToString(digest[:])
	}
	reference, _ := c.Selected()
	if len(c.Windows) > 0 {
		raw, _ = json.Marshal(c.Windows)
		digest = sha256.Sum256(append([]byte(legacy+"/windows-v1/"), raw...))
		legacy = hex.EncodeToString(digest[:])
	}
	methods := Methods{Day: c.MethodID("Day"), Night: c.MethodID("Night")}
	if methods == defaultMethods(reference) {
		return legacy
	}
	// 標準の組合せは旧指紋を維持し、手法を変えた場合だけ追加の照合値を作ります。
	raw, _ = json.Marshal(struct {
		Base    string
		Methods Methods
	}{legacy, methods})
	digest = sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// positive は正の有限値を確認します。
//
// 引数: v(float64)は検査値。
// 返り値: boolは適切な値ならtrueです。
func positive(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }
