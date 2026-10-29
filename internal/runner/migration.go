package runner

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/masayoshi4649/SessionTradeExecuter/internal/config"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/schedule"
	"github.com/masayoshi4649/SessionTradeExecuter/internal/strategy"
)

// MigrateLegacy は旧二商品版の一商品台帳を、API接続せず単一商品用の保存先へ移します。
//
// 引数: cfg config.Config は新設定、legacyDirectory string は変更前に退避した旧confです。
// 返り値: error は設定照合・排他・SQLite複製の失敗です。旧DBと旧ログは変更しません。
func MigrateLegacy(cfg config.Config, legacyDirectory string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := validateMethodSelection(cfg); err != nil {
		return err
	}
	legacy, legacySignature, err := config.ReadLegacyForMigration(legacyDirectory)
	if err != nil {
		return err
	}
	reference, market := cfg.Selected()
	if legacy.Controller.BaseURL != cfg.Controller.BaseURL || legacy.Trading.DryRun != cfg.Trading.DryRun ||
		!reflect.DeepEqual(legacy.Schedule, cfg.Schedule) || !reflect.DeepEqual(legacy.Products, cfg.Products) ||
		!reflect.DeepEqual(legacy.Algorithm, cfg.Algorithm) || legacy.Markets[reference] != market {
		return errors.New("移行では接続先・仮想指定・時間・商品規格・戦略係数・対象商品の数量を変更できません。旧設定に合わせてください")
	}
	sourceBase, err := filepath.Abs(filepath.Join(legacy.Path(legacy.Runtime.DataDirectory), mode(cfg.Trading.DryRun)))
	if err != nil {
		return err
	}
	targetBase := filepath.Join(cfg.Path(cfg.Runtime.DataDirectory), mode(cfg.Trading.DryRun))
	targetDirectory := filepath.Join(targetBase, cfg.InstanceID())
	source := filepath.Join(sourceBase, market.Product+".sqlite")
	target := filepath.Join(targetDirectory, market.Product+".sqlite")
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("旧台帳を確認できません (%s): %w。旧confは旧アプリ配下の退避フォルダを指定してください", source, err)
	}
	// 旧版の排他ロックと同じ場所を排他的に取得します。
	sourceLock, err := acquireLock(sourceBase)
	if err != nil {
		return fmt.Errorf("旧版または同じ保存先の新版を停止してから移行してください: %w", err)
	}
	defer sourceLock.Close()
	if !strings.EqualFold(filepath.Clean(sourceBase), filepath.Clean(targetBase)) {
		baseLock, err := acquireLock(targetBase)
		if err != nil {
			return err
		}
		defer baseLock.Close()
	}
	productLock, err := acquireProductLock(cfg)
	if err != nil {
		return err
	}
	defer productLock.Close()
	targetLock, err := acquireLock(targetDirectory)
	if err != nil {
		return err
	}
	defer targetLock.Close()
	logLock, err := acquireReportOwnerLock(cfg)
	if err != nil {
		return err
	}
	defer logLock.Close()
	for _, name := range []string{target, source + ".migration.json"} {
		if _, err := os.Lstat(name); err == nil {
			return fmt.Errorf("既存の移行先または移行記録があります。上書き・再複製しません: %s", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	uriPath := filepath.ToSlash(source)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err = migrationIntegrity(db); err != nil {
		return err
	}
	var before []byte
	if err = db.QueryRow("SELECT state FROM snapshot WHERE id=1").Scan(&before); err != nil {
		return fmt.Errorf("旧台帳の戦略状態がありません: %w", err)
	}
	after, namespace, err := migrateState(before, cfg, legacySignature, sourceBase)
	if err != nil {
		return err
	}
	// ----------------------------------------
	// コピーの途中終了も検出するため、旧正本の隣に一度限りの移行先を先に記録します。
	marker := source + ".migration.json"
	beforeHash := sha256.Sum256(before)
	metadata := map[string]any{"source": source, "target": target, "instance_id": cfg.InstanceID(),
		"namespace": namespace, "created_at": time.Now().Format(time.RFC3339Nano),
		"source_snapshot_sha256": hex.EncodeToString(beforeHash[:]), "status": "移行準備"}
	markerFile, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	markerBody, _ := json.MarshalIndent(metadata, "", "  ")
	_, writeErr := markerFile.Write(markerBody)
	syncErr := markerFile.Sync()
	closeErr := markerFile.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(marker)
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.Remove(marker)
		}
	}()
	temporary, err := os.CreateTemp(targetDirectory, ".legacy-import-*.sqlite")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err = temporary.Close(); err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	// VACUUM INTOは読み取りトランザクションとしてWAL内の確定分も含めます。
	if _, err = db.Exec("VACUUM INTO ?", temporaryPath); err != nil {
		return fmt.Errorf("旧台帳の整合コピーに失敗しました: %w", err)
	}
	if err = rewriteMigratedSnapshot(temporaryPath, before, after, metadata); err != nil {
		return err
	}
	// 同じディレクトリ内のハードリンク作成は既存targetを置換しません。
	if err = os.Link(temporaryPath, target); err != nil {
		return fmt.Errorf("移行先を確定できません（既存ファイルは上書きしません）: %w", err)
	}
	published = true
	metadata["status"] = "移行完了"
	markerBody, _ = json.MarshalIndent(metadata, "", "  ")
	if err = os.WriteFile(marker, markerBody, 0600); err != nil {
		return fmt.Errorf("台帳の移行は完了しましたが移行記録の完了表示に失敗しました。再移行せず確認してください: %w", err)
	}
	return nil
}

// migrationIntegrity は対応するSQLite形式と物理整合性を確認します。
//
// 引数: db *sql.DB は読み取り対象です。
// 返り値: error は非対応形式または破損です。
func migrationIntegrity(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		return fmt.Errorf("移行対象のSQLite形式が不正です: %d / %v", version, err)
	}
	var integrity string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("移行対象のSQLite整合性を確認できません: %s / %v", integrity, err)
	}
	return nil
}

// migrateState は所有建玉・未確定注文・未通知約定を変更せず戦略の所属情報だけを移します。
//
// 引数: before []byte は旧snapshot、cfg config.Config は新設定、legacySignature string は旧設定指紋、legacyDataDirectory string は旧保存先の絶対パスです。
// 返り値: []byteは更新後snapshot、stringは新運転ID、errorは照合失敗です。
func migrateState(before []byte, cfg config.Config, legacySignature, legacyDataDirectory string) ([]byte, string, error) {
	var snapshot map[string]json.RawMessage
	var header struct {
		Symbol      string  `json:"symbol"`
		DryRun      bool    `json:"dryrun"`
		Multiplier  float64 `json:"multiplier"`
		MaxPosition int     `json:"max_position"`
	}
	if err := json.Unmarshal(before, &snapshot); err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal(before, &header); err != nil {
		return nil, "", err
	}
	reference, market := cfg.Selected()
	if header.Symbol != market.Product || header.DryRun != cfg.Trading.DryRun ||
		header.Multiplier != cfg.Products[market.Product].PointValue || header.MaxPosition != market.MaxPerSide {
		return nil, "", errors.New("旧注文台帳と新設定の商品・仮想指定・倍率・上限が異なります")
	}
	var runtime savedRuntime
	if err := json.Unmarshal(snapshot["runtime_state"], &runtime); err != nil {
		return nil, "", fmt.Errorf("旧戦略状態を読み取れません: %w", err)
	}
	if runtime.Version != 1 || runtime.Algorithm != algorithmVersion || runtime.Signature != legacySignature || runtime.Namespace != "" {
		return nil, "", errors.New("旧台帳の署名・戦略版が退避した旧設定と一致しません。移行済み台帳の再複製もできません")
	}
	namespace, err := newNamespace()
	if err != nil {
		return nil, "", err
	}
	if len(runtime.Portfolio) == 0 || string(runtime.Portfolio) == "null" {
		return nil, "", errors.New("旧台帳に戦略の準備済み状態がないため自動移行できません")
	}
	portfolio, err := strategy.Restore(runtime.Portfolio)
	if err != nil {
		return nil, "", err
	}
	parameters, err := methodParameters(cfg, reference, portfolio.Session.Kind)
	if err != nil {
		return nil, "", err
	}
	expected := strategy.Config{Reference: reference, Product: market.Product, CoreQuantity: market.CoreQuantity,
		ExtraMaxQuantity: market.ExtraQuantity, Cap: market.MaxPerSide, LargeTick: cfg.Products[reference].Tick,
		PointValue: cfg.Products[market.Product].PointValue, Parameters: parameters}
	if !reflect.DeepEqual(portfolio.Config, expected) {
		return nil, "", errors.New("旧戦略内部の市場・商品・数量・係数が設定と一致しません")
	}
	plan, err := schedule.Build(runtime.Plan.ID, runtime.Plan.Start, runtime.Plan.End, cfg.Schedule)
	if err != nil || !sameMigrationPlan(plan, runtime.Plan) {
		return nil, "", fmt.Errorf("旧戦略の時刻設定が新設定と一致しません: %v", err)
	}
	extraAt := plan.TopixExtra
	if reference == "NK225" {
		extraAt = plan.NKExtra
	}
	session := strategy.Session{ID: plan.ID, Kind: plan.Kind, Start: seconds(plan.Start), End: seconds(plan.End),
		ForceClose: seconds(plan.ForceClose), USOpen: seconds(plan.USOpen), ExtraAt: seconds(extraAt)}
	if !reflect.DeepEqual(portfolio.Session, session) || runtime.Contract.FutureCode != market.Product {
		return nil, "", errors.New("旧戦略内部の立会または登録商品が台帳と一致しません")
	}
	portfolio.Config.Namespace = namespace
	runtime.Portfolio, err = portfolio.Dump()
	if err != nil {
		return nil, "", err
	}
	// runtime全体を型で再生成せず、将来項目と注文待ち行列をそのまま保持します。
	var runtimeFields map[string]json.RawMessage
	if err = json.Unmarshal(snapshot["runtime_state"], &runtimeFields); err != nil {
		return nil, "", err
	}
	runtimeFields["Portfolio"] = runtime.Portfolio
	runtimeFields["Signature"], _ = json.Marshal(cfg.Signature())
	runtimeFields["namespace"], _ = json.Marshal(namespace)
	runtimeFields["legacy_data_directory"], _ = json.Marshal(legacyDataDirectory)
	snapshot["runtime_state"], err = json.Marshal(runtimeFields)
	if err != nil {
		return nil, "", err
	}
	after, err := json.Marshal(snapshot)
	return after, namespace, err
}

// sameMigrationPlan は時差の内部表現に依存せず全ての立会判断時刻を比較します。
//
// 引数: a,b schedule.Plan は計算値と旧保存値です。
// 返り値: boolは全時刻が一致する場合trueです。
func sameMigrationPlan(a, b schedule.Plan) bool {
	return a.ID == b.ID && a.Kind == b.Kind && a.Start.Equal(b.Start) && a.End.Equal(b.End) &&
		a.ForceClose.Equal(b.ForceClose) && a.USOpen.Equal(b.USOpen) && a.TopixExtra.Equal(b.TopixExtra) &&
		a.NKExtra.Equal(b.NKExtra) && a.ExtraUntil.Equal(b.ExtraUntil)
}

// rewriteMigratedSnapshot はコピー先だけの署名・運転IDと移行監査記録を同時に確定します。
//
// 引数: path string は一時コピー、before/after []byte は移行前後の正本、metadata map[string]any は出典です。
// 返り値: error は元状態の差異または書き込み失敗です。
func rewriteMigratedSnapshot(path string, before, after []byte, metadata map[string]any) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err = db.Exec("PRAGMA journal_mode=DELETE"); err != nil {
		return err
	}
	if _, err = db.Exec("PRAGMA synchronous=FULL"); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var copied []byte
	if err = tx.QueryRow("SELECT state FROM snapshot WHERE id=1").Scan(&copied); err != nil || string(copied) != string(before) {
		return fmt.Errorf("コピー中に旧状態が変化したため移行を中止しました: %v", err)
	}
	now := time.Now().Format(time.RFC3339Nano)
	if _, err = tx.Exec("UPDATE snapshot SET state=?,revision=revision+1,updated_at=? WHERE id=1", after, now); err != nil {
		return err
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO events(kind,session,code,correlation,at,payload) VALUES(?,?,?,?,?,?)",
		"旧版台帳移行", "", "", "", now, payload); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return migrationIntegrity(db)
}

// ----------------------------------------

// acquireMigratedLegacyLock は移行先の正本を読取専用で参照し、旧版運転と競合する共有ロックを取得します。
//
// 引数: statePath string は移行先の商品SQLite、currentBase string は既に共有ロック済みの保存先です。
// 返り値: *processLock は運転終了まで保持するロック、error は旧版運転中又は読み取り不整合です。
// 移行前・過去の移行済み状態・同一保存先では nil を返し、注文や約定状態を更新しません。
func acquireMigratedLegacyLock(statePath, currentBase string) (*processLock, error) {
	if _, err := os.Stat(statePath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(statePath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var hasSnapshot int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='snapshot'").Scan(&hasSnapshot); err != nil {
		return nil, err
	}
	if hasSnapshot == 0 {
		return nil, nil
	}
	var raw []byte
	if err = db.QueryRow("SELECT state FROM snapshot WHERE id=1").Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var saved struct {
		Runtime struct {
			LegacyDataDirectory string `json:"legacy_data_directory"`
		} `json:"runtime_state"`
	}
	if err = json.Unmarshal(raw, &saved); err != nil {
		return nil, err
	}
	source := saved.Runtime.LegacyDataDirectory
	if source == "" {
		return nil, nil
	}
	if !filepath.IsAbs(source) {
		return nil, errors.New("保存された旧版移行元が絶対パスではありません")
	}
	source = filepath.Clean(source)
	current, err := filepath.Abs(currentBase)
	if err != nil {
		return nil, err
	}
	if source == current {
		return nil, nil
	}
	sourceInfo, sourceErr := os.Stat(source)
	currentInfo, currentErr := os.Stat(current)
	if sourceErr == nil && currentErr == nil && os.SameFile(sourceInfo, currentInfo) {
		return nil, nil
	}
	lock, err := acquireSharedLock(source)
	if err != nil {
		return nil, fmt.Errorf("移行元の旧版運転または移行処理との同時起動はできません (%s): %w", source, err)
	}
	return lock, nil
}
