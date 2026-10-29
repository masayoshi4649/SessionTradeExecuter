package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

const webOverlayName = "web-ui.toml"

// ErrSettingsConflict は読み込み後の外部編集又は別画面による保存を表します。
var ErrSettingsConflict = errors.New("設定が別の操作で変更されています。再読み込みしてから保存してください")

var settingsLocks sync.Map

// configSources は設定フォルダ直下の実TOMLだけを独立した本文として読みます。
//
// 引数: cfg Configは保存場所を含みます。返り値: map[string][]byteは名前と本文、errorは読取失敗です。
func configSources(cfg Config) (map[string][]byte, error) {
	paths, err := filepath.Glob(filepath.Join(cfg.Root, "conf", "*.toml"))
	if err != nil || len(paths) == 0 {
		return nil, fmt.Errorf("設定ファイルを確認できません: %v", err)
	}
	sources := make(map[string][]byte, len(paths))
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("通常の設定ファイルではありません: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sources[filepath.Base(path)] = data
	}
	return sources, nil
}

// revisionOf はファイル名と長さを含めて全実設定の保存版を識別します。
//
// 引数: sources map[string][]byteは各実設定。返り値: stringはSHA256です。
func revisionOf(sources map[string][]byte) string {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		fmt.Fprintf(hash, "%d:%s:%d:", len(name), name, len(sources[name]))
		hash.Write(sources[name])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// settingsRevision は現在のディスク設定の識別子を計算します。
//
// 引数: cfg Configは保存場所。返り値: stringは保存版、errorは読取失敗です。
func settingsRevision(cfg Config) (string, error) {
	sources, err := configSources(cfg)
	if err != nil {
		return "", err
	}
	return revisionOf(sources), nil
}

// SaveWebSettings は既存本文を保持し、画面専用TOMLだけを同一フォルダで原子的に置換します。
//
// 引数: cfg Configは現在採用中、settings WebSettingsは全公開項目、revision stringは編集開始時の保存版です。
// 返り値: Configは保存成功した新設定、errorは競合・検査・書込失敗です。失敗時は元のcfgを返します。
// プロセス内の保存を直列化し、外部編集は書込前と置換直前の二回検査します。
func SaveWebSettings(cfg Config, settings WebSettings, revision string) (Config, error) {
	candidate, err := ApplyWebSettings(cfg, settings)
	if err != nil {
		return cfg, err
	}
	directory := filepath.Join(cfg.Root, "conf")
	key, err := filepath.Abs(directory)
	if err != nil {
		return cfg, err
	}
	lockValue, _ := settingsLocks.LoadOrStore(strings.ToLower(key), &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	if revision == "" || (cfg.WebRevision != "" && cfg.WebRevision != revision) || (settings.Revision != "" && settings.Revision != revision) {
		return cfg, ErrSettingsConflict
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return cfg, fmt.Errorf("設定の保存先を確認できません: %v", err)
	}
	sources, err := configSources(cfg)
	if err != nil {
		return cfg, err
	}
	if revisionOf(sources) != revision {
		return cfg, ErrSettingsConflict
	}
	body, err := encodeWebSettings(webSettingsOf(candidate))
	if err != nil {
		return cfg, err
	}
	file, err := os.CreateTemp(directory, ".web-ui-*.tmp")
	if err != nil {
		return cfg, err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(body)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return cfg, err
	}
	if closeErr != nil {
		return cfg, closeErr
	}
	latest, err := settingsRevision(cfg)
	if err != nil {
		return cfg, err
	}
	if latest != revision {
		return cfg, ErrSettingsConflict
	}
	if err = os.Rename(file.Name(), filepath.Join(directory, webOverlayName)); err != nil {
		return cfg, err
	}
	sources[webOverlayName] = body
	candidate.WebRevision = revisionOf(sources)
	return candidate, nil
}

// encodeWebSettings は全項目へ日本語説明を付けた画面専用の完全なTOMLを生成します。
//
// 引数: settings WebSettingsは保存内容。返り値: []byteはUTF-8、errorは符号化又は説明不足です。
func encodeWebSettings(settings WebSettings) ([]byte, error) {
	var raw bytes.Buffer
	if err := toml.NewEncoder(&raw).Encode(settings); err != nil {
		return nil, err
	}
	var out strings.Builder
	out.WriteString("# Web画面から保存した設定です。conf内の他のTOMLより後に、この公開範囲へ適用します。\n# 元の設定・コメント・非公開設定は他ファイルに保持します。変更後は画面で再読み込み、又は再起動します。\n")
	section := ""
	for _, line := range strings.Split(raw.String(), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			section = strings.Trim(trimmed, "[]")
		}
		if field, _, found := strings.Cut(trimmed, "="); found && !strings.HasPrefix(trimmed, "#") {
			field = strings.TrimSpace(field)
			if section == "instruments" && field == "enabled" {
				continue
			}
			key := section + "." + field
			comment, exists := webComments[key]
			if !exists {
				return nil, fmt.Errorf("設定項目%sの日本語説明がありません", strconv.Quote(key))
			}
			out.WriteString("# " + comment + "\n")
		}
		out.WriteString(line + "\n")
	}
	return []byte(out.String()), nil
}

var webComments = map[string]string{
	".settings_version":                   "標準設定の移行版。アプリが管理します。手法を選び直した後の再移行を防ぐため変更しないでください。",
	"web.host":                            "画面の待受IP。0.0.0.0は全IPv4、127.0.0.1は同じPCのみ。変更は再起動で反映します。",
	"web.port":                            "画面のポート番号（1〜65535）。controllerと別の番号を指定し、変更は再起動で反映します。",
	"controller.base_url":                 "kabus-controllerの接続先。APIパス・キーは付けません。",
	"controller.request_timeout_seconds":  "通常API要求の通信期限（秒）。",
	"controller.history_timeout_seconds":  "過去板履歴の取得期限（秒）。",
	"controller.account_poll_seconds":     "注文・建玉の照合間隔（秒）。",
	"controller.candle_poll_seconds":      "確定足の取得間隔（秒）。",
	"controller.reconnect_seconds":        "通信切断後の再接続待ち（秒）。",
	"schedule.force_close_before_seconds": "立会終了の何秒前に所有玉の返済を要求するか。",
	"schedule.extra_cutoff_seconds":       "補完入口を立会終了の何秒前まで許可するか。",
	"schedule.day_extra_jst":              "日中の未参加補完を許可する日本時間（HH:MM）。",
	"schedule.us_open_jst":                "米国現物開始の基準となる冬の日本時間。米国夏時間は自動で1時間早めます。",
	"schedule.london_extra_jst":           "TOPIX夜間補完の冬の日本時間。英国夏時間は自動で1時間早めます。",
	"schedule.us_extra_delay_minutes":     "日経夜間補完を米国開始から何分後に許可するか（0〜240）。",
	"runtime.data_directory":              "SQLite保存先。相対パスは実行ファイルのフォルダが基準です。",
	"runtime.log_directory":               "約定・診断ログ保存先。相対パスは実行ファイルのフォルダが基準です。",
	"runtime.quote_max_age_seconds":       "新規判断に使う価格・気配の鮮度上限（秒）。",
	"runtime.account_max_age_seconds":     "注文・建玉照会の鮮度上限（秒）。",
	"runtime.reconnect_grace_seconds":     "受信中断時、返済判断へ移るまでの猶予（秒）。気配鮮度以上を指定します。",
	"runtime.checkpoint_seconds":          "戦略状態を保存する間隔（秒）。約定は別途即時保存します。",
	"runtime.status_seconds":              "端末へ稼働状況を表示する間隔（秒）。",
	"runtime.shutdown_seconds":            "運転終了時の取消・返済・照合を試みる猶予（秒）。",
	"runtime.entry_order_timeout_seconds": "新規判断・新規注文の有効期限（秒）。",
	"instruments.product":                 "発注商品。判断には対応するラージ市場を使用します。",
	"instruments.mode":                    "offは予約取消後に停止し、保有玉を管理外として返済しません。dryrunは仮想売買、liveは実注文。この希望状態を次回起動にも適用します。",
	"instruments.max_per_side":            "この運転の買い・売りそれぞれの保有上限枚数。自分の未約定新規を含みます。",
	"instruments.day":                     "日中の手法名。銘柄付きのルールファイル名から拡張子を除いた値です。",
	"instruments.night":                   "夜間の手法名。銘柄付きのルールファイル名から拡張子を除いた値です。",
	"instruments.windows.id":              "時間帯の識別名。商品内で重複しない英数・_・-を指定します。",
	"instruments.windows.session":         "立会区分。Dayは日中、Nightは夜間。時間帯ごとに手法を選べます。",
	"instruments.windows.start":           "開始を含む時計。日本時間HH:MM、SESSION_START又はUS_OPEN±HH:MM。夜間の午前時計は翌日です。",
	"instruments.windows.end":             "終了を含まない時計。日本時間HH:MM、SESSION_END又はUS_OPEN±HH:MM。米国相対は夏時間へ自動追従します。",
	"instruments.windows.method":          "この時間帯の銘柄付き手法ID。空文字は静観です。枠間の空白も静観し、境界では当運転の所有玉を返済・照合します。",
}
