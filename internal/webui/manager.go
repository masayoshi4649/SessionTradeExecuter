// Package webui は商品ごとに独立した売買運転を一つの画面へ公開します。
package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/runner"
)

// Worker は一商品の運転入口です。管理終了時は未確定注文・送信待ちを解消し、保有玉を監査記録に残してnilを返します。
type Worker func(context.Context, config.Config, *runner.Control, func(runner.Snapshot)) error

// ProductState は希望設定と現在の運転を分けて画面に公開します。
type ProductState struct {
	Product             string           `json:"product"`
	Reference           string           `json:"reference"`
	MaxPerSide          int              `json:"max_per_side"`
	DayMethod           string           `json:"day_method"`
	NightMethod         string           `json:"night_method"`
	Mode                string           `json:"mode"`
	ActiveMode          string           `json:"active_mode"`
	Pending             bool             `json:"pending"`
	SettingsRevision    string           `json:"settings_revision"`
	Enabled             bool             `json:"enabled"`
	Status              string           `json:"status"`
	Error               string           `json:"error"`
	Snapshot            *runner.Snapshot `json:"snapshot"`
	DetachedSnapshot    *runner.Snapshot `json:"detached_snapshot"`
	SettingsEditable    bool             `json:"settings_editable"`
	SettingsBlockReason string           `json:"settings_block_reason"`
}

// State は一つのHTTP応答にまとめる全商品の状態です。
type State struct {
	Mode             string         `json:"mode"`
	ServerTime       time.Time      `json:"server_time"`
	ControllerURL    string         `json:"controller_url"`
	CSRFToken        string         `json:"csrf_token"`
	SettingsRevision string         `json:"settings_revision"`
	RestartRequired  bool           `json:"restart_required"`
	SettingsEditable bool           `json:"settings_editable"`
	Closing          bool           `json:"closing"`
	ReportError      string         `json:"report_error"`
	Products         []ProductState `json:"products"`
}

type productWorker struct {
	cfg             config.Config
	control         *runner.Control
	activeSignature string
	started         bool
	finished        bool
	state           ProductState
}

// Manager は設定保存と起動を直列化し、商品通知用の短いロックを分離します。
type Manager struct {
	operation   sync.Mutex
	mu          sync.Mutex
	cfg         config.Config
	settings    config.WebSettings
	initialWeb  config.Web
	ctx         context.Context
	cancel      context.CancelFunc
	worker      Worker
	products    []*productWorker
	wg          sync.WaitGroup
	closing     bool
	audit       *os.File
	closeOnce   sync.Once
	closeErr    error
	reportsOnce sync.Once
	reportError string
}

// NewManager は運転を開始せず、商品別の希望設定と操作記録を準備します。
//
// 引数: cfg(config.Config)はWeb設定、worker(Worker)は運転処理です。
// 返り値: *Managerとerror。ここではAPI接続・発注・取消を行いません。
func NewManager(cfg config.Config, worker Worker) (*Manager, error) {
	if cfg.Web == nil || worker == nil {
		return nil, errors.New("Web設定と運転処理が必要です")
	}
	workers, err := cfg.WorkerConfigs()
	if err != nil {
		return nil, err
	}
	settings, err := config.ReadWebSettings(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{cfg: cfg, settings: settings, initialWeb: *cfg.Web, ctx: ctx, cancel: cancel, worker: worker}
	for index, item := range workers {
		ref, market := item.Selected()
		m.products = append(m.products, &productWorker{cfg: item, control: runner.NewControl(false),
			state: ProductState{Product: market.Product, Reference: ref, MaxPerSide: market.MaxPerSide,
				DayMethod: item.MethodName("Day"), NightMethod: item.MethodName("Night"),
				Mode: config.ModeOf(cfg.Instruments[index], cfg.Trading.DryRun), ActiveMode: "off",
				SettingsRevision: settings.Revision, Status: "待機（新規取引OFF）"}})
	}
	directory := filepath.Join(cfg.Path(cfg.Runtime.LogDirectory), "web")
	if err = os.MkdirAll(directory, 0700); err != nil {
		cancel()
		return nil, err
	}
	m.audit, err = os.OpenFile(filepath.Join(directory, "操作.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		cancel()
		return nil, err
	}
	return m, nil
}

// State は同一時点の希望モード・実稼働モード・切替待ちを返します。
//
// 引数: なし。返り値: State。通知データは公開後に変更しません。
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := State{Mode: "off", ServerTime: time.Now(), ControllerURL: m.cfg.Controller.BaseURL, SettingsEditable: true,
		SettingsRevision: m.settings.Revision, RestartRequired: *m.cfg.Web != m.initialWeb,
		Closing: m.closing, ReportError: m.reportError, Products: make([]ProductState, 0, len(m.products))}
	for index, p := range m.products {
		state := p.state
		state.SettingsEditable = editableProduct(p) == nil
		if err := editableProduct(p); err != nil {
			state.SettingsBlockReason = err.Error()
			s.SettingsEditable = false
		}
		s.Products = append(s.Products, state)
		if index == 0 {
			s.Mode = p.state.Mode
		} else if s.Mode != p.state.Mode {
			s.Mode = "mixed"
		}
	}
	return s
}

// Settings は外部編集前の版を保った編集用設定のコピーを返します。
//
// 引数: なし。返り値: config.WebSettingsとerror。保存時にディスク上の版と照合します。
func (m *Manager) Settings() (config.WebSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.settings
	s.Instruments = config.CloneInstruments(s.Instruments)
	for index := range s.Instruments {
		if p := m.productLocked(s.Instruments[index].Product); p != nil {
			// 保存失敗時の緊急OFFを別商品の設定保存で再びONへ戻さないよう、実際の希望状態を返します。
			s.Instruments[index].Mode = p.state.Mode
		}
	}
	return s, nil
}

// Rules は現在の設定で選択可能なルール文書を列挙します。
//
// 引数: なし。返り値: []config.RuleChoiceとerror。
func (m *Manager) Rules() ([]config.RuleChoice, error) {
	m.mu.Lock()
	cfg := m.cfg
	m.mu.Unlock()
	return config.ListRuleChoices(cfg)
}

// ----------------------------------------

// SetMode は希望モードを保存し、旧運転の未約定注文を取消照合した後に切り替えます。
//
// 引数: product(string)、mode(string)はoff/dryrun/live、revision(string)は編集元の版、source(string)は操作元。
// 返り値: error。取消照合完了は待たず受付結果を返し、旧建玉は返済せず管理外へ移します。
func (m *Manager) SetMode(product, mode, revision, source string) error {
	m.operation.Lock()
	defer m.operation.Unlock()
	return m.setModeLocked(product, mode, revision, source)
}

// setModeLocked は設定操作ロックを保持した呼出元から一商品のモードを更新します。
//
// 引数: product/mode/revision/source(string)。返り値: errorは検査・保存・操作記録の失敗です。
func (m *Manager) setModeLocked(product, mode, revision, source string) error {
	if mode != "off" && mode != "dryrun" && mode != "live" {
		return errors.New("modeはoff/dryrun/liveから指定してください")
	}
	m.mu.Lock()
	p := m.productLocked(product)
	if p == nil {
		m.mu.Unlock()
		return errors.New("対象の商品がありません")
	}
	if p.finished && mode != "off" {
		m.mu.Unlock()
		return errors.New("この商品の運転はエラーで停止しています。所有建玉・注文・ログを確認してください。安全を確認できないため画面から再起動しません")
	}
	m.mu.Unlock()
	s, _ := m.Settings()
	s.Revision = revision
	for index := range s.Instruments {
		if s.Instruments[index].Product == product {
			s.Instruments[index].Mode = mode
		}
	}
	safetyOff := ""
	if mode == "off" {
		safetyOff = product
	}
	err := m.updateSettingsLocked(s, source, safetyOff)
	if err != nil && mode == "off" {
		m.applyEmergencyOff(product)
		m.mu.Lock()
		if !p.finished {
			p.state.Error = "OFFは受け付けましたが、設定保存に失敗しました: " + err.Error()
		}
		m.mu.Unlock()
	}
	return err
}

// SetEnabled は旧APIを商品別モードへ変換し、OFF後の開始を新規運転として扱います。
//
// 引数: product(string)、enabled(bool)、source(string)。返り値: error。
func (m *Manager) SetEnabled(product string, enabled bool, source string) error {
	m.operation.Lock()
	defer m.operation.Unlock()
	m.mu.Lock()
	p := m.productLocked(product)
	mode := "off"
	if enabled {
		mode = "dryrun"
		if !m.cfg.Trading.DryRun {
			mode = "live"
		}
		if p != nil && p.state.ActiveMode != "off" {
			mode = p.state.ActiveMode
		}
	}
	revision := m.settings.Revision
	m.mu.Unlock()
	return m.setModeLocked(product, mode, revision, source)
}

// UpdateProduct はOFFかつ未約定注文の取消照合が済んだ商品の枚数上限と昼夜手法を保存します。
//
// 引数: product(string)、max(int)、day/night/revision/source(string)。返り値: error。
func (m *Manager) UpdateProduct(product string, max int, day, night, revision, source string) error {
	return m.UpdateProductWindows(product, max, day, night, nil, revision, source)
}

// UpdateProductWindows はOFF・取消照合済みの商品について上限と時間帯を同時に保存します。
//
// 引数: product(string)、max(int)、day/night(string)、windows(*[]config.MethodWindow)、revision/source(string)は商品・互換手法・明示枠・保存版・操作元です。
// 返り値: errorは稼働中・競合・時間帯不正です。nilの枠は旧APIで、既存の明示枠を上書きできません。
func (m *Manager) UpdateProductWindows(product string, max int, day, night string, windows *[]config.MethodWindow, revision, source string) error {
	if windows != nil && len(*windows) == 0 {
		return errors.New("時間帯は最低1枠が必要です。全て静観する場合も静観枠を残してください")
	}
	m.operation.Lock()
	defer m.operation.Unlock()
	m.mu.Lock()
	p := m.productLocked(product)
	if p == nil {
		m.mu.Unlock()
		return errors.New("対象の商品がありません")
	}
	err := editableProduct(p)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	s, _ := m.Settings()
	for index := range s.Instruments {
		if s.Instruments[index].Product == product {
			if windows == nil && len(s.Instruments[index].Windows) > 0 {
				return errors.New("時間帯設定のある商品を旧画面から上書きできません。画面を再読み込みしてください")
			}
			s.Instruments[index].MaxPerSide = max
			s.Instruments[index].Day, s.Instruments[index].Night = day, night
			if windows != nil {
				s.Instruments[index].Windows = append([]config.MethodWindow(nil), (*windows)...)
			}
		}
	}
	s.Revision = revision
	return m.updateSettingsLocked(s, source, "")
}

// UpdateSettings は全体設定を一括保存し、必要な各商品の運転交代を予約します。
//
// 引数: settings(config.WebSettings)、source(string)。返り値: error。待受先は再起動後に適用します。
func (m *Manager) UpdateSettings(settings config.WebSettings, source string) error {
	m.operation.Lock()
	defer m.operation.Unlock()
	m.mu.Lock()
	for _, item := range settings.Instruments {
		if p := m.productLocked(item.Product); p != nil && config.ModeOf(item, m.cfg.Trading.DryRun) != p.state.Mode {
			m.mu.Unlock()
			return errors.New("運転モードは商品ごとのOFF・DryRun・実取引の操作で変更してください")
		}
	}
	m.mu.Unlock()
	return m.updateSettingsLocked(settings, source, "")
}

// editableProduct はOFFかつ旧運転の未約定注文の取消照合が完了したか確認します。
//
// 引数: p(*productWorker)。返り値: errorは稼働中・終了待ち・照合失敗です。muを保持して呼びます。
func editableProduct(p *productWorker) error {
	if p.state.Mode != "off" || p.state.Pending || p.finished || p.state.Error != "" {
		return fmt.Errorf("%sの設定変更はOFF・切替完了・エラーなしの場合だけ可能です", p.state.Product)
	}
	if p.started {
		return fmt.Errorf("%sの未約定注文の取消・照合と旧運転の終了を待ってから設定を変更してください", p.state.Product)
	}
	return nil
}

// updateSettingsLocked は検証・版照合・保存を済ませてから、新しい希望設定を公開します。
//
// 引数: settings(config.WebSettings)、source(string)、safetyOff(string)は保存失敗でも停止する商品です。
// 返り値: error。呼出元はoperationを保持し、運転通知のmuはファイル操作中に保持しません。
func (m *Manager) updateSettingsLocked(settings config.WebSettings, source, safetyOff string) error {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return errors.New("アプリの終了処理中です")
	}
	if settings.Revision != m.settings.Revision {
		m.mu.Unlock()
		return errors.New("設定の版が更新されています。画面を再読み込みしてください")
	}
	cfg := m.cfg
	m.mu.Unlock()
	validated, err := config.ApplyWebSettings(cfg, settings)
	if err != nil {
		return err
	}
	workers, err := validated.WorkerConfigs()
	if err != nil {
		return err
	}
	m.mu.Lock()
	for index, worker := range workers {
		_, market := worker.Selected()
		p := m.productLocked(market.Product)
		nextMode := config.ModeOf(validated.Instruments[index], validated.Trading.DryRun)
		if p.finished && nextMode != "off" && nextMode != p.state.Mode {
			m.mu.Unlock()
			return errors.New("エラー停止した商品の新規運転は許可できません。所有建玉・注文・ログを確認してください")
		}
		// モード変更と共通設定変更を区別し、後者だけOFF・旧運転終了を必須とします。
		oldOptions, newOptions := p.cfg, worker
		oldOptions.Trading.DryRun, newOptions.Trading.DryRun = true, true
		if workerSignature(oldOptions) != workerSignature(newOptions) {
			if err = editableProduct(p); err != nil {
				m.mu.Unlock()
				return err
			}
		}
		// 旧運転の終了前はそのモードを保持し、新しい設定を既存の台帳へ適用しません。
		if nextMode == "off" && p.started {
			workers[index].Trading.DryRun = p.state.ActiveMode == "dryrun"
		}
	}
	m.mu.Unlock()
	// 記録先の変更は保存前に新しいファイルの追記と同期を確認し、保存成功後だけ交換します。
	var nextAudit *os.File
	if cfg.Path(cfg.Runtime.LogDirectory) != validated.Path(validated.Runtime.LogDirectory) {
		directory := filepath.Join(validated.Path(validated.Runtime.LogDirectory), "web")
		if err = os.MkdirAll(directory, 0700); err != nil {
			return fmt.Errorf("新しい操作記録先を準備できません: %w", err)
		}
		nextAudit, err = os.OpenFile(filepath.Join(directory, "操作.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("新しい操作記録を開けません: %w", err)
		}
		defer func() {
			if nextAudit != nil {
				_ = nextAudit.Close()
			}
		}()
	}
	operationRecord := map[string]any{"at": time.Now(), "event": "設定変更要求", "source": source, "settings": settings}
	var auditErr error
	if nextAudit != nil {
		auditErr = json.NewEncoder(nextAudit).Encode(operationRecord)
		if auditErr == nil {
			auditErr = nextAudit.Sync()
		}
	} else {
		auditErr = m.recordOperation(operationRecord)
	}
	if auditErr != nil && safetyOff == "" {
		return auditErr
	}
	saved, saveErr := config.SaveWebSettings(cfg, settings, settings.Revision)
	if saveErr != nil {
		if safetyOff != "" {
			m.applyEmergencyOff(safetyOff)
			return fmt.Errorf("新規OFFは適用しましたが、設定保存に失敗しました: %w", errors.Join(saveErr, auditErr))
		}
		return saveErr
	}
	next, err := config.ReadWebSettings(saved)
	if err != nil {
		for _, p := range m.products {
			m.applyEmergencyOff(p.state.Product)
		}
		return fmt.Errorf("設定保存後の読み直しに失敗しました。新規取引を停止しました: %w", err)
	}
	var auditCloseErr error
	if nextAudit != nil {
		previousAudit := m.audit
		m.audit, nextAudit = nextAudit, nil
		auditCloseErr = previousAudit.Close()
	}
	m.mu.Lock()
	m.cfg, m.settings = saved, next
	for index, worker := range workers {
		_, market := worker.Selected()
		p := m.productLocked(market.Product)
		p.cfg = worker
		p.state.Mode = config.ModeOf(validated.Instruments[index], validated.Trading.DryRun)
		p.state.MaxPerSide = market.MaxPerSide
		p.state.DayMethod, p.state.NightMethod = worker.MethodName("Day"), worker.MethodName("Night")
		p.state.SettingsRevision = next.Revision
		if !p.finished {
			p.state.Error = ""
		}
	}
	m.mu.Unlock()
	for _, p := range m.products {
		m.applyDesired(p)
	}
	if auditErr != nil {
		return fmt.Errorf("新規OFFと設定保存は適用しましたが、操作記録の保存に失敗しました: %w", auditErr)
	}
	if auditCloseErr != nil {
		return fmt.Errorf("設定と新しい記録先は適用しましたが、以前の操作記録を閉じる際に失敗しました: %w", auditCloseErr)
	}
	return nil
}

// applyEmergencyOff は保存先の故障時にも、新規と返済を止めて管理終了を要求します。
//
// 引数: product(string)。返り値: なし。保存済み設定は変更せず、画面に実際の停止状態を残します。
func (m *Manager) applyEmergencyOff(product string) {
	m.mu.Lock()
	p := m.productLocked(product)
	m.mu.Unlock()
	if p == nil {
		return
	}
	p.control.RequestDetachStop()
	m.mu.Lock()
	p.state.Mode, p.state.Enabled = "off", false
	p.state.Pending = p.started
	p.state.Status = "OFF（設定保存失敗・未約定注文の取消照合中）"
	m.mu.Unlock()
}

// applyDesired は旧運転の新規・返済を止め、未約定注文の取消照合後の交代を要求します。
//
// 引数: p(*productWorker)。返り値: なし。operationの保持中に呼び、送信待ちの間はmuを外します。
func (m *Manager) applyDesired(p *productWorker) {
	m.mu.Lock()
	if p.finished || m.closing {
		m.mu.Unlock()
		return
	}
	if !p.started {
		if p.state.Mode != "off" {
			m.startProductLocked(p)
		}
		m.mu.Unlock()
		return
	}
	changed := workerSignature(p.cfg) != p.activeSignature
	drain := p.control.DetachStopRequested() || changed || p.state.Mode == "off" || p.state.Mode != p.state.ActiveMode
	enabled := p.state.Mode != "off" && !drain
	m.mu.Unlock()
	if drain {
		if !p.control.DetachStopRequested() {
			p.control.RequestDetachStop()
		}
	} else {
		p.control.SetEnabled(enabled)
	}
	m.mu.Lock()
	p.state.Enabled, p.state.Pending = enabled, drain
	if drain {
		p.state.Status = "管理終了待ち（未約定注文の取消・照合中、保有玉は管理外へ移管）"
	} else if !enabled {
		p.state.Status = "OFF（保有玉は管理外）"
	} else {
		p.state.Status = "稼働中"
	}
	m.mu.Unlock()
}

// workerSignature は売買・接続・保存の設定を比較し、待受先だけの変更を除外します。
//
// 引数: cfg(config.Config)。返り値: stringは正規化した設定とルール内容です。
func workerSignature(cfg config.Config) string {
	reference, _ := cfg.Selected()
	raw, _ := json.Marshal(map[string]any{"config": cfg, "day_rules": cfg.RuleExpressionsFor(reference, "Day"),
		"night_rules": cfg.RuleExpressionsFor(reference, "Night")})
	return string(raw)
}

// productLocked は画面上の商品IDから管理対象を探します。
//
// 引数: product(string)。返り値: *productWorker。不明商品ではnilです。muを保持して呼びます。
func (m *Manager) productLocked(product string) *productWorker {
	for _, p := range m.products {
		if p.state.Product == product {
			return p
		}
	}
	return nil
}

// recordOperation は設定操作を同期保存します。
//
// 引数: value(any)。返り値: errorは追記・同期失敗です。operationを保持して呼びます。
func (m *Manager) recordOperation(value any) error {
	if err := json.NewEncoder(m.audit).Encode(value); err != nil {
		return err
	}
	return m.audit.Sync()
}

// ----------------------------------------

// startProductLocked は旧運転が存在しない場合に新しい制御器と設定のコピーを渡します。
//
// 引数: p(*productWorker)。返り値: なし。muとoperationを保持して呼びます。
func (m *Manager) startProductLocked(p *productWorker) {
	p.control = runner.NewControl(true)
	p.started, p.finished = true, false
	p.state.Enabled, p.state.Pending = true, false
	p.state.ActiveMode = p.state.Mode
	p.state.Status, p.state.Error = "開始中（履歴準備）", ""
	p.state.Snapshot = nil
	p.activeSignature = workerSignature(p.cfg)
	m.wg.Add(1)
	go m.runProduct(p, p.cfg, p.control)
}

// runProduct は旧運転の正常終了後だけ最新の希望設定へ交代し、異常時は停止を維持します。
//
// 引数: p(*productWorker)、cfg(config.Config)、control(*runner.Control)はこの起動専用です。
// 返り値: なし。完了通知は設定操作と直列化し、同一商品の二重起動を禁止します。
func (m *Manager) runProduct(p *productWorker, cfg config.Config, control *runner.Control) {
	defer m.wg.Done()
	err := m.worker(m.ctx, cfg, control, func(snapshot runner.Snapshot) {
		raw, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			return
		}
		var copied runner.Snapshot
		if json.Unmarshal(raw, &copied) != nil {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		p.state.Snapshot = &copied
		if !p.state.Pending && p.state.Enabled {
			p.state.Status = "稼働中"
		}
	})
	m.operation.Lock()
	defer m.operation.Unlock()
	control.SetEnabled(false)
	m.mu.Lock()
	if err == nil && !m.closing && !control.DetachStopRequested() {
		err = errors.New("管理終了の要求がない運転停止です。所有建玉・未確定注文を確認してください")
	}
	p.started = false
	p.state.Enabled, p.state.Pending = false, false
	p.state.Status = "停止"
	if err != nil {
		p.finished = true
		p.state.Error, p.state.Status = err.Error(), "停止（エラー・要確認）"
	} else {
		p.state.ActiveMode = "off"
		if control.DetachStopRequested() {
			p.state.DetachedSnapshot = p.state.Snapshot
			p.state.Snapshot = nil
			p.state.Status = "OFF（旧建玉を管理外へ移管済み）"
		}
	}
	entry := map[string]any{"at": time.Now(), "product": p.state.Product, "event": "運転終了", "error": p.state.Error}
	m.mu.Unlock()
	if err != nil {
		mode := "live"
		if cfg.Trading.DryRun {
			mode = "dryrun"
		}
		fmt.Printf("%s [運転停止/%s] %s: %v\n", time.Now().Format(time.RFC3339), p.state.Product, mode, err)
	}
	auditErr := m.recordOperation(entry)
	m.mu.Lock()
	defer m.mu.Unlock()
	if auditErr != nil && !m.closing {
		p.finished = true
		p.state.Error = errors.Join(err, fmt.Errorf("運転終了記録の保存失敗: %w", auditErr)).Error()
		p.state.Status = "停止（エラー・要確認）"
	}
	if !p.finished && !m.closing && p.state.Mode != "off" {
		m.startProductLocked(p)
	}
}

// StartConfigured は保存された商品別モードをHTTP待受開始後に適用します。
//
// 引数: なし。返り値: errorは操作記録の失敗です。初期OFFの商品はAPIに接続しません。
func (m *Manager) StartConfigured() error {
	m.operation.Lock()
	defer m.operation.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return errors.New("アプリの終了処理中です")
	}
	m.startSessionReportsLocked()
	for _, p := range m.products {
		if p.state.Mode != "off" && !p.started && !p.finished {
			if err := m.recordOperation(map[string]any{"at": time.Now(), "product": p.state.Product, "mode": p.state.Mode, "source": "設定ファイル"}); err != nil {
				return err
			}
			m.startProductLocked(p)
		}
	}
	return nil
}

// Close は全商品の新規を止め、既存runnerの取消・返済処理の完了を待ちます。
//
// 引数: なし。返り値: errorは未返済などの商品別終了エラーです。
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.operation.Lock()
		m.mu.Lock()
		m.closing = true
		m.cancel()
		m.mu.Unlock()
		for _, p := range m.products {
			p.control.SetEnabled(false)
			m.mu.Lock()
			p.state.Enabled = false
			m.mu.Unlock()
		}
		m.operation.Unlock()
		m.wg.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, p := range m.products {
			if p.state.Error != "" {
				m.closeErr = errors.Join(m.closeErr, fmt.Errorf("%s: %s", p.state.Product, p.state.Error))
			}
		}
		m.closeErr = errors.Join(m.closeErr, m.audit.Close())
	})
	return m.closeErr
}
