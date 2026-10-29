package config

import (
	"fmt"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/rules"
)

// WebSettings はRESTで公開する設定です。表示・保存とも時刻は冬の日本時間です。
type WebSettings struct {
	SettingsVersion int           `json:"-" toml:"settings_version"`
	Revision        string        `json:"revision" toml:"-"`
	Web             Web           `json:"web" toml:"web"`
	Controller      WebController `json:"controller" toml:"controller"`
	Schedule        WebSchedule   `json:"schedule" toml:"schedule"`
	Runtime         WebRuntime    `json:"runtime" toml:"runtime"`
	Instruments     []Instrument  `json:"instruments" toml:"instruments"`
}

// WebController は画面で編集するcontroller通信設定です。
type WebController struct {
	BaseURL               string  `json:"base_url" toml:"base_url"`
	RequestTimeoutSeconds float64 `json:"request_timeout_seconds" toml:"request_timeout_seconds"`
	HistoryTimeoutSeconds float64 `json:"history_timeout_seconds" toml:"history_timeout_seconds"`
	AccountPollSeconds    float64 `json:"account_poll_seconds" toml:"account_poll_seconds"`
	CandlePollSeconds     float64 `json:"candle_poll_seconds" toml:"candle_poll_seconds"`
	ReconnectSeconds      float64 `json:"reconnect_seconds" toml:"reconnect_seconds"`
}

// WebSchedule は既存の地域時計を利用者向けの日本時間に変換した設定です。
type WebSchedule struct {
	ForceCloseBeforeSeconds float64 `json:"force_close_before_seconds" toml:"force_close_before_seconds"`
	ExtraCutoffSeconds      float64 `json:"extra_cutoff_seconds" toml:"extra_cutoff_seconds"`
	DayExtraJST             string  `json:"day_extra_jst" toml:"day_extra_jst"`
	USOpenJST               string  `json:"us_open_jst" toml:"us_open_jst"`
	LondonExtraJST          string  `json:"london_extra_jst" toml:"london_extra_jst"`
	USExtraDelayMinutes     int     `json:"us_extra_delay_minutes" toml:"us_extra_delay_minutes"`
}

// WebRuntime は保存先と受信鮮度・運転猶予の公開設定です。
type WebRuntime struct {
	DataDirectory            string  `json:"data_directory" toml:"data_directory"`
	LogDirectory             string  `json:"log_directory" toml:"log_directory"`
	QuoteMaxAgeSeconds       float64 `json:"quote_max_age_seconds" toml:"quote_max_age_seconds"`
	AccountMaxAgeSeconds     float64 `json:"account_max_age_seconds" toml:"account_max_age_seconds"`
	ReconnectGraceSeconds    float64 `json:"reconnect_grace_seconds" toml:"reconnect_grace_seconds"`
	CheckpointSeconds        float64 `json:"checkpoint_seconds" toml:"checkpoint_seconds"`
	StatusSeconds            float64 `json:"status_seconds" toml:"status_seconds"`
	ShutdownSeconds          float64 `json:"shutdown_seconds" toml:"shutdown_seconds"`
	EntryOrderTimeoutSeconds float64 `json:"entry_order_timeout_seconds" toml:"entry_order_timeout_seconds"`
}

// ReadWebSettings は稼働中の設定値と現在の保存版識別子を画面へ渡します。
//
// 引数: cfg Configはロード済み設定です。
// 返り値: WebSettingsは編集可能な値、errorは設定又は保存版の読取失敗です。
func ReadWebSettings(cfg Config) (WebSettings, error) {
	if err := cfg.Validate(); err != nil {
		return WebSettings{}, err
	}
	if cfg.Web == nil {
		return WebSettings{}, fmt.Errorf("Web設定が必要です")
	}
	revision := cfg.WebRevision
	if revision == "" {
		var err error
		revision, err = settingsRevision(cfg)
		if err != nil {
			return WebSettings{}, err
		}
	}
	out := webSettingsOf(cfg)
	out.Revision = revision
	return out, nil
}

// webSettingsOf は正規化済みの設定を外部表示へ変換します。
//
// 引数: cfg ConfigはWeb設定。返り値: WebSettingsは独立した商品一覧を持ちます。
func webSettingsOf(cfg Config) WebSettings {
	out := WebSettings{SettingsVersion: cfg.SettingsVersion, Web: *cfg.Web, Controller: WebController(cfg.Controller), Runtime: WebRuntime(cfg.Runtime),
		Schedule: WebSchedule{ForceCloseBeforeSeconds: cfg.Schedule.ForceCloseBeforeSeconds, ExtraCutoffSeconds: cfg.Schedule.ExtraCutoffSeconds,
			DayExtraJST: jstClock(cfg.Schedule.DayExtra, 0), USOpenJST: jstClock(cfg.Schedule.USOpen, 14),
			LondonExtraJST: jstClock(cfg.Schedule.LondonExtra, 9), USExtraDelayMinutes: cfg.Schedule.USExtraDelayMinutes},
		Instruments: CloneInstruments(cfg.Instruments)}
	for i := range out.Instruments {
		out.Instruments[i].Mode = ModeOf(out.Instruments[i], cfg.Trading.DryRun)
		out.Instruments[i].Enabled = out.Instruments[i].Mode != "off"
		out.Instruments[i].Day = NameOfMethod(Reference(out.Instruments[i].Product), out.Instruments[i].Day)
		out.Instruments[i].Night = NameOfMethod(Reference(out.Instruments[i].Product), out.Instruments[i].Night)
		for j := range out.Instruments[i].Windows {
			window := &out.Instruments[i].Windows[j]
			window.Method = NameOfMethod(Reference(out.Instruments[i].Product), window.Method)
		}
	}
	return out
}

// jstClock は地域時計へ固定時差を加え、冬の日本時間へ戻します。
//
// 引数: value stringはHH:MM、hours intは日本との時差です。返り値: stringは日本時間です。
func jstClock(value string, hours int) string {
	clock, err := time.Parse("15:04", value)
	if err != nil {
		return value
	}
	return clock.Add(time.Duration(hours) * time.Hour).Format("15:04")
}

// ApplyWebSettings は保存せずに編集内容をコピーへ適用し、全商品の手法と範囲を検査します。
//
// 引数: cfg Configは更新前、settings WebSettingsは全項目を含む更新要求です。
// 返り値: Configは検査済み候補、errorは不正な値です。入力cfgとディスクは変更しません。
func ApplyWebSettings(cfg Config, settings WebSettings) (Config, error) {
	if cfg.Web == nil {
		return cfg, fmt.Errorf("Web版の設定だけを更新できます")
	}
	previousWindows := map[string]bool{}
	for _, item := range cfg.Instruments {
		previousWindows[item.Product] = len(item.Windows) > 0
	}
	for _, item := range settings.Instruments {
		if item.Windows != nil && len(item.Windows) == 0 {
			return cfg, fmt.Errorf("%sのwindowsは空配列にできません。最低1つの時間帯又は静観枠を指定してください", item.Product)
		}
		if previousWindows[item.Product] && item.Windows == nil {
			return cfg, fmt.Errorf("%sの既存時間帯を省略して削除できません。画面を再読み込みしてください", item.Product)
		}
	}
	candidate := applyWebFields(cfg, settings)
	candidate.migrateLegacyD07Windows(false)
	if err := candidate.Validate(); err != nil {
		return cfg, err
	}
	for _, item := range candidate.Instruments {
		if item.Mode != "off" && item.Mode != "dryrun" && item.Mode != "live" {
			return cfg, fmt.Errorf("%sのmodeを明示してください", item.Product)
		}
		for _, part := range instrumentMethods(item) {
			if part.Method == "" {
				continue
			}
			if _, err := rules.SelectProfile(Reference(item.Product), part.Session, part.Method); err != nil {
				return cfg, err
			}
		}
	}
	if err := reloadChangedWebRules(cfg, &candidate); err != nil {
		return cfg, err
	}
	return candidate, nil
}

// applyWebFields は画面の公開範囲だけを置換し、非公開の起動・商品規格・外部式設定を保持します。
//
// 引数: cfg Configは更新前、settings WebSettingsは更新内容です。
// 返り値: Configは未検査の独立コピーです。規格map・ルールは読み取り専用で継承します。
func applyWebFields(cfg Config, settings WebSettings) Config {
	web := settings.Web
	cfg.Web = &web
	cfg.Controller = Controller(settings.Controller)
	cfg.Runtime = Runtime(settings.Runtime)
	cfg.Schedule = Schedule{ForceCloseBeforeSeconds: settings.Schedule.ForceCloseBeforeSeconds,
		ExtraCutoffSeconds: settings.Schedule.ExtraCutoffSeconds, DayExtraJST: settings.Schedule.DayExtraJST,
		USOpenJST: settings.Schedule.USOpenJST, LondonExtraJST: settings.Schedule.LondonExtraJST,
		USExtraDelayMinutes: settings.Schedule.USExtraDelayMinutes}
	// ValidateのWorkerConfigsは値コピーを検査するため、親Configも先に地域時計へ揃えます。
	_ = cfg.Schedule.NormalizeJST()
	cfg.Instruments = CloneInstruments(settings.Instruments)
	for i := range cfg.Instruments {
		cfg.Instruments[i].Enabled = cfg.Instruments[i].Mode != "off"
	}
	return cfg
}
