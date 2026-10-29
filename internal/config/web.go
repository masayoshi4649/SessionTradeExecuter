package config

import (
	"fmt"
	"net"
	"strings"
)

// Web は全商品を操作するHTTP画面の待受先です。設定の存在がWeb運転を選択します。
type Web struct {
	Host string `toml:"host" json:"host"`
	Port int    `toml:"port" json:"port"`
}

// Instrument はWeb画面で独立管理する一商品の初期設定です。
// Enabledは旧形式の互換用です。Modeのoffは新規を停止し、保有玉を管理外として返済しません。
type Instrument struct {
	Product    string         `toml:"product" json:"product"`
	MaxPerSide int            `toml:"max_per_side" json:"max_per_side"`
	Enabled    bool           `toml:"enabled" json:"-"`
	Mode       string         `toml:"mode" json:"mode"`
	Day        string         `toml:"day" json:"day"`
	Night      string         `toml:"night" json:"night"`
	Windows    []MethodWindow `toml:"windows,omitempty" json:"windows,omitempty"`
}

// ModeOf は商品別の明示モードを優先し、旧enabledと共通dryrunを読み替えます。
//
// 引数: item Instrumentは商品設定、fallbackDryRun boolは旧形式の共通モードです。
// 返り値: stringはoff/dryrun/live又は検査前の不正値です。不正値を実注文へ補正しません。
func ModeOf(item Instrument, fallbackDryRun bool) string {
	if item.Mode != "" {
		return item.Mode
	}
	if !item.Enabled {
		return "off"
	}
	if fallbackDryRun {
		return "dryrun"
	}
	return "live"
}

// WorkerConfigs は全五商品の設定を互いに独立した単品設定へ展開して検査します。
//
// 引数: なし。受け手ConfigのWeb設定・商品一覧・共通設定を使用します。
// 返り値: []Configはinstrumentsと同順の五商品、errorは設定の不足・混在・不正値です。
// 実行IDは商品名の小文字で固定し、商品の台帳・建玉ID・実行ロックを分離します。
func (c Config) WorkerConfigs() ([]Config, error) {
	if c.Web == nil {
		return nil, fmt.Errorf("全商品の運転には[web]の指定が必要です")
	}
	if c.Web.Port < 1 || c.Web.Port > 65535 {
		return nil, fmt.Errorf("web.portは1〜65535の整数で指定してください")
	}
	if c.Web.Host != "localhost" && net.ParseIP(c.Web.Host) == nil {
		return nil, fmt.Errorf("web.hostはIPアドレスまたはlocalhostを指定してください")
	}
	if c.Market != nil || len(c.Markets) != 0 || c.Methods.Day != "" || c.Methods.Night != "" || len(c.Windows) > 0 || c.Trading.InstanceID != "" {
		return nil, fmt.Errorf("Web版ではmarket/markets/methods/trading.instance_idを指定せず、商品ごとのinstrumentsを使用してください")
	}
	if len(c.Algorithm) != 0 {
		return nil, fmt.Errorf("Web版は商品ごとのday/night手法IDで指定し、algorithm係数の上書きは使用できません")
	}
	if c.Startup.Action == "migrate_legacy" {
		return nil, fmt.Errorf("Web版は単品台帳の自動移行に対応していません")
	}
	if len(c.Instruments) != 5 {
		return nil, fmt.Errorf("instrumentsには全5商品を一つずつ指定してください。売買しない商品はenabled=falseにします")
	}
	seen := map[string]bool{}
	workers := make([]Config, 0, len(c.Instruments))
	for _, item := range c.Instruments {
		if item.Windows != nil && len(item.Windows) == 0 {
			return nil, fmt.Errorf("%sのwindowsは空配列にできません。最低1つの時間帯又は静観枠を指定してください", item.Product)
		}
		mode := ModeOf(item, c.Trading.DryRun)
		if mode != "off" && mode != "dryrun" && mode != "live" {
			return nil, fmt.Errorf("%sのmodeはoff/dryrun/liveから指定してください", item.Product)
		}
		if Reference(item.Product) == "" || seen[item.Product] {
			return nil, fmt.Errorf("instrumentsの商品が未知または重複しています: %s", item.Product)
		}
		seen[item.Product] = true
		if len(item.Windows) == 0 && (strings.TrimSpace(item.Day) == "" || strings.TrimSpace(item.Night) == "") {
			return nil, fmt.Errorf("%sのday/night手法IDを明示してください", item.Product)
		}
		worker := c
		worker.Web, worker.Instruments = nil, nil
		worker.Markets = nil
		worker.Market = &SingleMarket{Product: item.Product, MaxPerSide: item.MaxPerSide}
		worker.Methods = Methods{Day: item.Day, Night: item.Night}
		worker.Windows = append([]MethodWindow(nil), item.Windows...)
		worker.Trading.InstanceID = strings.ToLower(item.Product)
		// OFFは売買しません。旧共通設定が実注文でも準備用workerは仮想を安全側の既定にします。
		worker.Trading.DryRun = mode != "live"
		worker.Products = make(map[string]Product, len(c.Products))
		for name, product := range c.Products {
			worker.Products[name] = product
		}
		worker.Algorithm = make(map[string]float64)
		if err := worker.Validate(); err != nil {
			return nil, fmt.Errorf("%sの設定: %w", item.Product, err)
		}
		workers = append(workers, worker)
	}
	return workers, nil
}
