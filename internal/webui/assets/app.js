"use strict";

const productInfo = {
  NK225: { name: "日経225", detail: "ラージ / NK225", group: "nikkei-cards" },
  NK225mini: { name: "日経225 mini", detail: "ミニ / NK225mini", group: "nikkei-cards" },
  NK225micro: { name: "日経225 micro", detail: "マイクロ / NK225micro", group: "nikkei-cards" },
  TOPIX: { name: "TOPIX", detail: "ラージ / TOPIX", group: "topix-cards" },
  TOPIXmini: { name: "TOPIX mini", detail: "ミニ / TOPIXmini", group: "topix-cards" }
};
const modes = { off: "OFF", dryrun: "DryRun", live: "ON" };
const modeDescriptions = { off: "管理停止", dryrun: "仮想売買", live: "実取引" };
const globalSchema = [
  { key: "schedule", title: "取引スケジュール", description: "日中は日本時間、米国・ロンドンは冬の日本時間で指定します。夏時間は各地域の暦で自動的に1時間早まります。", fields: [
    ["us_open_jst", "米国開始（冬の日本時間）", "time", "", "例）23:30 → 米国夏時間は22:30に自動調整"],
    ["us_extra_delay_minutes", "米国開始からの追加待機", "number", "分", "米国開始時刻から追加判断までの待機時間", 0, 1],
    ["day_extra_jst", "日中の追加判断時刻", "time", "", "日中の追加判断に使う時刻"],
    ["london_extra_jst", "ロンドン追加（冬の日本時間）", "time", "", "英国の夏時間は自動で1時間早まります"],
    ["force_close_before_seconds", "終了前の返済余裕", "number", "秒", "セッション終了より前に返済を始める時間", 0, "any"],
    ["extra_cutoff_seconds", "終了前の追加停止", "number", "秒", "セッション終了前に追加注文を止める時間", 0, "any"]
  ] },
  { key: "controller", title: "売買システムへの接続", description: "接続先とデータ取得の間隔を設定します。保存後、次にONまたはDryRunを開始すると反映されます。", fields: [
    ["base_url", "接続先URL", "url", "", "例）http://10.10.100.1:8080"],
    ["request_timeout_seconds", "通常リクエストの待ち時間", "number", "秒", "", 0.001, "any"],
    ["history_timeout_seconds", "履歴取得の待ち時間", "number", "秒", "", 0.001, "any"],
    ["account_poll_seconds", "口座情報の取得間隔", "number", "秒", "", 0.001, "any"],
    ["candle_poll_seconds", "足データの取得間隔", "number", "秒", "", 0.001, "any"],
    ["reconnect_seconds", "再接続の間隔", "number", "秒", "", 0.001, "any"]
  ] },
  { key: "runtime", title: "監視と保存", description: "データの鮮度、注文監視、ログ保存先を設定します。保存後、次にONまたはDryRunを開始すると反映されます。", fields: [
    ["quote_max_age_seconds", "気配の有効期間", "number", "秒", "", 0.001, "any"],
    ["account_max_age_seconds", "口座情報の有効期間", "number", "秒", "", 0.001, "any"],
    ["reconnect_grace_seconds", "受信中断時の猶予", "number", "秒", "", 0, "any"],
    ["entry_order_timeout_seconds", "新規注文の待ち時間", "number", "秒", "", 0.001, "any"],
    ["checkpoint_seconds", "状態保存の間隔", "number", "秒", "", 0.001, "any"],
    ["status_seconds", "状態記録の間隔", "number", "秒", "", 0.001, "any"],
    ["shutdown_seconds", "終了処理の待ち時間", "number", "秒", "", 0.001, "any"],
    ["data_directory", "データの保存先", "text", "", ""],
    ["log_directory", "ログの保存先", "text", "", ""]
  ] },
  { key: "web", title: "管理画面", description: "変更したホスト・ポートは次回起動時に反映されます。現在の画面は今のアドレスで利用できます。", fields: [
    ["host", "待受ホスト", "text", "", "例）0.0.0.0（すべての接続先から待受）"],
    ["port", "待受ポート", "number", "", "1～65535", 1, 1, 65535]
  ] }
];
const cards = new Map();
const globalInputs = new Map();
const operationErrors = new Map();
let latestState = null;
let latestSettings = null;
let rules = [];
let rulesLoaded = false;
let connected = false;
let stateLoading = false;
let configurationLoading = null;
let mutationBusy = false;
let mutationEpoch = 0;
let lastSuccessfulAt = 0;
let globalBaseline = null;
let globalDirty = false;
let productEditor = null;
let toastTimer = null;
let sessionPLRows = null;
let sessionPLLoading = false;
let sessionPLError = "";
let sessionPLLastSuccess = 0;

/**
 * IDに対応する画面要素を取得します。
 * @param {string} id - 要素のID。
 * @returns {HTMLElement} 対応する画面要素。
 */
function byId(id) { return document.getElementById(id); }

/**
 * DOM要素を作成し、表示文字列を安全に設定します。
 * @param {string} tag - HTMLタグ名。
 * @param {string} className - CSSクラス名。
 * @param {string} text - 表示文字列。
 * @returns {HTMLElement} 作成した要素。
 */
function element(tag, className, text = "") {
  const node = document.createElement(tag);
  node.className = className;
  node.textContent = text;
  return node;
}

/**
 * 円表示を整形し、正負の色を設定します。
 * @param {HTMLElement} node - 数字を表示する要素。
 * @param {number|null|undefined} value - 円単位の値。
 * @returns {void} 返り値はありません。
 */
function money(node, value) {
  const known = typeof value === "number" && Number.isFinite(value);
  node.textContent = known ? (value > 0 ? "+" : value < 0 ? "−" : "") + Math.abs(value).toLocaleString("ja-JP", { maximumFractionDigits: 0 }) : "—";
  node.classList.toggle("positive", known && value > 0);
  node.classList.toggle("negative", known && value < 0);
}

/**
 * APIの時計を日本時間に揃えます。
 * @param {string|null|undefined} value - ISO形式の時計。
 * @param {boolean} full - 日付も表示する場合はtrue。
 * @returns {string} 日本時間の表示、または未取得を示す記号。
 */
function clock(value, full = false) {
  const date = new Date(value || "");
  if (!Number.isFinite(date.getTime()) || date.getUTCFullYear() < 2000) return "—";
  const options = { timeZone: "Asia/Tokyo", hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" };
  if (full) Object.assign(options, { year: "numeric", month: "2-digit", day: "2-digit" });
  return date.toLocaleString("ja-JP", options);
}

/**
 * APIへJSON要求を送り、応答と失敗理由を統一して扱います。
 * @param {string} path - APIの相対パス。
 * @param {string} method - HTTPメソッド。
 * @param {object|undefined} body - 送信するJSONデータ。
 * @returns {Promise<object>} 取得したJSONデータ。
 */
async function api(path, method = "GET", body) {
  const abort = new AbortController();
  const timer = setTimeout(() => abort.abort(), method === "GET" ? 6000 : 30000);
  const headers = { Accept: "application/json" };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    headers["X-CSRF-Token"] = latestState?.csrf_token || "";
  }
  try {
    const response = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), credentials: "same-origin", cache: "no-store", signal: abort.signal });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(data.error || "要求に失敗しました（" + response.status + "）。");
      error.status = response.status;
      throw error;
    }
    return data;
  } catch (error) {
    if (error.name === "AbortError") throw new Error("応答を確認できません。保存済みの状態を再取得してから操作してください。");
    throw error;
  } finally { clearTimeout(timer); }
}

/**
 * 保存結果を画面と支援技術へ通知します。
 * @param {string} message - 通知する内容。
 * @returns {void} 返り値はありません。
 */
function notify(message) {
  byId("notification").textContent = message;
  byId("toast").textContent = message;
  byId("toast").hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { byId("toast").hidden = true; }, 7000);
}

/**
 * 設定と運転状態の版が揃っていて操作できるかを判定します。
 * @returns {boolean} 保存要求に使用できる設定がある場合はtrue。
 */
function settingsReady() {
  return Boolean(latestSettings && connected && latestState?.csrf_token && (!latestState.settings_revision || latestSettings.revision === latestState.settings_revision));
}

/**
 * 商品オプションを変更できない理由をサーバー状態から確認します。
 * @param {object|undefined} current - 商品の運転状態。
 * @returns {string} 変更不可の理由。変更可能なら空文字列。
 */
function optionBlockedReason(current) {
  if (!current) return "商品状態を取得できません";
  if (current.settings_editable === false) return current.settings_block_reason || "停止処理が完了すると設定を変更できます";
  if (current.mode !== "off") return "数量・手法はOFFにしてから変更できます";
  if (current.pending) return "注文の取消・照合が完了すると設定を変更できます";
  if (current.error) return "運転エラーを確認してから設定を変更してください";
  return "";
}

/**
 * 商品カードを一度だけ作成して、定期更新中のフォーカスを保持します。
 * @param {string} product - 商品ID。
 * @returns {object} カードと更新対象の要素。
 */
function makeCard(product) {
  const info = productInfo[product];
  const card = element("article", "product-card");
  card.dataset.product = product;
  card.setAttribute("aria-labelledby", "title-" + product);
  const header = element("div", "card-header");
  const identity = element("div", "product-identity");
  const title = element("h3", "product-title", info.name);
  title.id = "title-" + product;
  identity.append(title, element("p", "product-subtitle", info.detail));
  const settingsButton = element("button", "icon-button settings-button", "⚙");
  settingsButton.type = "button";
  settingsButton.setAttribute("aria-label", info.name + "の商品設定");
  settingsButton.setAttribute("aria-haspopup", "dialog");
  settingsButton.disabled = true;
  header.append(identity, settingsButton);
  const picker = element("div", "mode-picker");
  picker.setAttribute("role", "group");
  picker.setAttribute("aria-label", info.name + "の保存済み運転モード");
  const modeButtons = {};
  for (const mode of Object.keys(modes)) {
    const button = element("button", "mode-button");
    button.type = "button";
    button.dataset.mode = mode;
    button.setAttribute("aria-pressed", "false");
    button.setAttribute("aria-label", info.name + "を" + modes[mode] + "（" + modeDescriptions[mode] + "）に設定");
    button.disabled = true;
    button.append(element("strong", "", modes[mode]), element("span", "", modeDescriptions[mode]));
    button.addEventListener("click", () => requestMode(product, mode));
    modeButtons[mode] = button;
    picker.append(button);
  }

  // ----------------------------------------
  const confirm = element("div", "confirm-panel");
  confirm.hidden = true;
  const confirmActions = element("div", "confirm-actions");
  const confirmButton = element("button", "button live-button", "実取引をONにする");
  confirmButton.type = "button";
  const cancelButton = element("button", "button secondary", "やめる");
  cancelButton.type = "button";
  confirmActions.append(confirmButton, cancelButton);
  confirm.append(element("p", "", info.name + "を実取引に変更します。保存後、条件が成立すると実口座に注文します。"), confirmActions);
  const stateRow = element("div", "card-state");
  const state = element("span", "", "状態を取得しています");
  stateRow.append(element("span", "card-state-dot"), state);
  const active = element("div", "active-mode", "—");
  const settingsHint = element("p", "settings-hint");
  settingsHint.id = "settings-hint-" + product;
  settingsButton.setAttribute("aria-describedby", settingsHint.id);
  const settingsPending = element("p", "settings-pending");
  settingsPending.hidden = true;
  const methods = element("div", "method-row");
  const day = element("b", "", "—");
  const night = element("b", "", "—");
  const dayBadge = element("span", "method", "日中");
  const nightBadge = element("span", "method", "夜間");
  dayBadge.append(day);
  nightBadge.append(night);
  const cap = element("b", "", "—");
  const capWrap = element("span", "cap", "上限 ");
  capWrap.append(cap, document.createTextNode(" 枚 / 方向"));
  methods.append(dayBadge, nightBadge, capWrap);

  // ----------------------------------------
  const pnl = element("div", "pnl-row");
  const realized = element("span", "pnl-value", "—");
  const unrealized = element("span", "pnl-value", "—");
  for (const [label, value] of [["確定損益・当セッション", realized], ["含み損益", unrealized]]) {
    const item = element("div", "pnl-item");
    item.append(element("span", "pnl-label", label), value, element("span", "yen", "円"));
    pnl.append(item);
  }
  const positionRow = element("div", "position-row");
  const buy = element("b", "", "—");
  const sell = element("b", "", "—");
  const buyPosition = element("span", "position", "買");
  const sellPosition = element("span", "position", "売");
  buyPosition.append(buy, document.createTextNode("枚"));
  sellPosition.append(sell, document.createTextNode("枚"));
  const pendingBadge = element("span", "pending-badge", "注文照合中");
  pendingBadge.hidden = true;
  positionRow.append(element("span", "position-label", "担当保有"), buyPosition, sellPosition, pendingBadge);
  const bottom = element("div", "card-bottom");
  const session = element("span", "session-label", "セッション待ち");
  const timestamps = element("div", "timestamps");
  const received = element("time", "", "—");
  const productReceived = element("time", "", "—");
  const decision = element("time", "", "—");
  for (const [label, value] of [["板受信", received], ["商品気配", productReceived], ["判断", decision]]) {
    const row = element("div", "", label);
    row.append(value);
    timestamps.append(row);
  }
  bottom.append(session, timestamps);
  const error = element("p", "card-error");
  error.hidden = true;
  error.setAttribute("role", "alert");
  card.append(header, picker, confirm, stateRow, active, settingsHint, settingsPending, methods, pnl, positionRow, bottom, error);
  byId(info.group).append(card);
  settingsButton.addEventListener("click", () => openProduct(product, settingsButton));
  confirmButton.addEventListener("click", () => changeMode(product, "live"));
  cancelButton.addEventListener("click", () => { confirm.hidden = true; modeButtons.live.focus(); });
  return { card, modeButtons, settingsButton, settingsHint, state, active, settingsPending, day, night, cap, realized, unrealized, buy, sell, pendingBadge, session, received, productReceived, decision, confirm, confirmButton, cancelButton, error };
}

/**
 * 希望モードを選び、実取引への変更時だけカード内に確認を表示します。
 * @param {string} product - 商品ID。
 * @param {string} mode - 希望するモード。
 * @returns {void} 返り値はありません。
 */
function requestMode(product, mode) {
  const current = latestState?.products.find(item => item.product === product);
  if (!current || !settingsReady() || mutationBusy || latestState.closing) return;
  const nodes = cards.get(product);
  nodes.confirm.hidden = true;
  // 保存失敗時は、実際にOFFへ移行済みでも同じOFFを選び直して永続保存を再試行できます。
  if (current.mode === mode && !current.error) return;
  if (mode === "live") {
    nodes.confirm.hidden = false;
    nodes.confirmButton.focus();
  } else changeMode(product, mode);
}

/**
 * 商品モードを永続保存し、確定したサーバー状態を反映します。
 * @param {string} product - 商品ID。
 * @param {string} mode - 希望するモード。
 * @returns {Promise<void>} 保存処理の完了。
 */
async function changeMode(product, mode) {
  if (!settingsReady() || mutationBusy || latestState.closing) return;
  mutationBusy = true;
  mutationEpoch++;
  cards.get(product).confirm.hidden = true;
  operationErrors.delete(product);
  render();
  try {
    const result = await api("/api/products/" + encodeURIComponent(product) + "/mode", "PUT", { mode, revision: latestSettings.revision });
    applyResult(result);
    notify(result.message || productInfo[product].name + "を" + modes[mode] + "で保存しました。");
  } catch (error) {
    operationErrors.set(product, error.message);
  } finally {
    mutationBusy = false;
    mutationEpoch++;
    await refresh();
    await loadConfiguration();
    render();
  }
}

/**
 * 保存応答に含まれる確定済み設定と運転状態を採用します。
 * @param {object} result - 保存APIの応答。
 * @param {boolean} resetDraft - 全般設定の入力を確定値に揃える場合はtrue。
 * @returns {void} 返り値はありません。
 */
function applyResult(result, resetDraft = false) {
  if (result.state) latestState = { ...latestState, ...result.state };
  if (result.settings) adoptSettings(result.settings, resetDraft);
  if (result.restart_required) byId("restart-alert").hidden = false;
}

/**
 * サーバーで確定した設定を保持し、未保存の入力は維持します。
 * @param {object} settings - 最新の設定。
 * @param {boolean} resetDraft - 入力を最新設定に揃える場合はtrue。
 * @returns {void} 返り値はありません。
 */
function adoptSettings(settings, resetDraft = false) {
  if (!settings || !Array.isArray(settings.instruments) || !settings.revision) throw new Error("設定データの形式を確認できません。");
  latestSettings = settings;
  if (!globalDirty || resetDraft) populateGlobal();
  updateGlobalStatus();
}

/**
 * 保存済み設定と手法候補を独立取得し、設定の読み込み失敗を表示します。
 * @returns {Promise<void>} 読み込み処理の完了。
 */
async function loadConfiguration() {
  if (configurationLoading) return configurationLoading;
  const epoch = mutationEpoch;
  configurationLoading = (async () => {
    const results = await Promise.allSettled([api("/api/settings"), api("/api/rules")]);
    if (epoch !== mutationEpoch) return;
    const errors = [];
    if (results[0].status === "fulfilled") {
      try { adoptSettings(results[0].value.settings || results[0].value); } catch (error) { errors.push(error.message); }
    } else errors.push("設定を取得できません。" + results[0].reason.message);
    if (results[1].status === "fulfilled" && Array.isArray(results[1].value.rules)) {
      rules = results[1].value.rules;
      rulesLoaded = true;
    } else {
      rulesLoaded = false;
      errors.push("手法候補を取得できません。商品設定は取得後に変更できます。");
    }
    byId("settings-load-alert").textContent = errors.join(" ");
    byId("settings-load-alert").hidden = errors.length === 0;
    render();
  })().finally(() => { configurationLoading = null; });
  return configurationLoading;
}

/**
 * 保存済み商品状態からカードと実取引・仮想売買別の合計を更新します。
 * @returns {void} 返り値はありません。
 */
function render() {
  const reportError = latestState?.report_error || "";
  byId("report-save-alert").hidden = !reportError;
  byId("report-save-alert").textContent = reportError ? "セッションレポート保存失敗。 " + reportError : "";
  if (!latestState) return;
  byId("controller-url").textContent = latestState.controller_url || "—";
  if (latestState.restart_required) byId("restart-alert").hidden = false;
  let liveCount = 0;
  let dryrunCount = 0;
  let holdingCount = 0;
  let holdingKnown = true;
  const totals = { live: 0, dryrun: 0 };
  const known = { live: true, dryrun: true };
  const disabled = !settingsReady() || mutationBusy || latestState.closing;
  for (const [product, nodes] of cards) {
    const current = latestState.products.find(item => item.product === product);
    if (!current) {
      nodes.state.textContent = "設定にこの商品がありません";
      nodes.settingsButton.disabled = true;
      for (const button of Object.values(nodes.modeButtons)) button.disabled = true;
      holdingKnown = false;
      known.live = false;
      known.dryrun = false;
      continue;
    }
    const mode = current.mode || "off";
    const activeMode = current.active_mode || mode;
    const waiting = mode === "off" && !current.pending;
    const snapshot = waiting ? {} : current.snapshot || {};
    const buy = Number.isFinite(snapshot.buy_quantity) ? snapshot.buy_quantity : waiting ? 0 : null;
    const sell = Number.isFinite(snapshot.sell_quantity) ? snapshot.sell_quantity : waiting ? 0 : null;
    const realized = waiting ? 0 : snapshot.session_realized_yen;
    const unrealized = buy === 0 && sell === 0 ? 0 : snapshot.unrealized_yen;
    const err = operationErrors.get(product) || current.error || "";
    liveCount += mode === "live" ? 1 : 0;
    dryrunCount += mode === "dryrun" ? 1 : 0;
    holdingCount += (buy || 0) + (sell || 0);
    if (buy === null || sell === null) holdingKnown = false;
    if (activeMode === "live" || activeMode === "dryrun") {
      if (Number.isFinite(realized)) totals[activeMode] += realized;
      else known[activeMode] = false;
    }
    nodes.card.classList.toggle("is-live", activeMode === "live");
    nodes.card.classList.toggle("is-dryrun", activeMode === "dryrun");
    nodes.card.classList.toggle("has-error", Boolean(err));
    const settingsBlocked = optionBlockedReason(current);
    nodes.settingsButton.disabled = disabled || Boolean(settingsBlocked);
    nodes.settingsButton.title = settingsBlocked || "数量と時間帯別の手法を変更";
    nodes.settingsHint.textContent = settingsBlocked;
    nodes.settingsHint.hidden = !settingsBlocked;
    for (const [value, button] of Object.entries(nodes.modeButtons)) {
      button.setAttribute("aria-pressed", String(value === mode));
      button.disabled = disabled;
    }
    nodes.confirmButton.disabled = disabled;
    nodes.cancelButton.disabled = mutationBusy;
    if (!connected || mode === "live" || latestState.closing) nodes.confirm.hidden = true;
    nodes.state.textContent = latestState.closing ? "終了処理中・注文の状態を確認しています" : current.status || snapshot.state || "待機中";
    nodes.active.textContent = mutationBusy ? "保存処理中…" : "指定 " + (modes[mode] || mode) + "  /  現在 " + (modeDescriptions[activeMode] || activeMode);
    if (snapshot.active_slot_id) nodes.active.textContent += " · " + (snapshot.slot_switching ? "時間帯切替・返済照合待ち" : snapshot.active_method || "時間帯静観") + (snapshot.active_slot_end ? "（" + clock(snapshot.active_slot_end) + "まで）" : "");
    nodes.settingsPending.hidden = !current.pending;
    nodes.settingsPending.textContent = mode === "off" ? "停止待ち · 自身の注文の取消・照合を確認しています" : "反映待ち · 現在の運転・注文の処理完了を確認しています";
    const setting = latestSettings?.instruments.find(item => item.product === product);
    nodes.day.textContent = setting?.windows?.length ? windowSummary(setting.windows, "Day") : setting?.day || current.day_method || "—";
    nodes.night.textContent = setting?.windows?.length ? windowSummary(setting.windows, "Night") : setting?.night || current.night_method || "—";
    nodes.day.title = nodes.day.textContent;
    nodes.night.title = nodes.night.textContent;
    nodes.cap.textContent = setting?.max_per_side ?? current.max_per_side ?? "—";
    money(nodes.realized, realized);
    money(nodes.unrealized, unrealized);
    nodes.realized.title = Number.isFinite(snapshot.session_realized_yen) ? "当場の確定損益: " + snapshot.session_realized_yen.toLocaleString("ja-JP") + "円" : "当場の確定損益: 未取得";
    nodes.buy.textContent = buy === null ? "—" : buy.toLocaleString("ja-JP");
    nodes.sell.textContent = sell === null ? "—" : sell.toLocaleString("ja-JP");
    nodes.pendingBadge.hidden = !snapshot.pending;
    nodes.session.textContent = snapshot.session ? snapshot.session.replace("_Day", " 日中").replace("_Night", " 夜間") : "セッション待ち";
    nodes.received.textContent = clock(snapshot.received_at);
    nodes.productReceived.textContent = clock(snapshot.product_received_at);
    nodes.decision.textContent = clock(snapshot.decision_at);
    nodes.error.hidden = !err;
    nodes.error.textContent = err;
  }
  byId("live-total").textContent = String(liveCount);
  byId("dryrun-total").textContent = String(dryrunCount);
  byId("holding-total").textContent = holdingKnown ? holdingCount.toLocaleString("ja-JP") : "—";
  money(byId("realized-live"), known.live ? totals.live : null);
  money(byId("realized-dryrun"), known.dryrun ? totals.dryrun : null);
  updateGlobalStatus();
  updateProductStatus();
}

/**
 * 設定項目の定義から日本語ラベル付きのフォームを構成します。
 * @returns {void} 返り値はありません。
 */
function buildGlobal() {
  for (const group of globalSchema) {
    const section = element("section", "settings-section");
    const heading = element("div", "settings-section-heading");
    const title = element("h2", "", group.title);
    title.id = "group-" + group.key;
    section.setAttribute("aria-labelledby", title.id);
    heading.append(title, element("p", "", group.description));
    const grid = element("div", "settings-grid");
    for (const [key, labelText, type, unit, hint, min, step, max] of group.fields) {
      const field = element("div", "field");
      const id = group.key + "-" + key;
      const label = element("label", "", labelText);
      label.htmlFor = id;
      const input = element("input", "");
      input.id = id;
      input.name = group.key + "." + key;
      input.type = type;
      input.required = true;
      if (min !== undefined) input.min = String(min);
      if (step !== undefined) input.step = String(step);
      if (max !== undefined) input.max = String(max);
      if (type === "number") input.inputMode = step === 1 ? "numeric" : "decimal";
      const wrap = element("div", unit ? "input-suffix" : "");
      wrap.append(input);
      if (unit) wrap.append(element("span", "", unit));
      field.append(label, wrap);
      if (hint) {
        const description = element("p", "field-hint", hint);
        description.id = id + "-hint";
        input.setAttribute("aria-describedby", description.id);
        field.append(description);
      }
      if (type === "url" || key.endsWith("_directory")) field.classList.add("field-wide");
      grid.append(field);
      globalInputs.set(group.key + "." + key, input);
    }
    section.append(heading, grid);
    byId("global-groups").append(section);
  }
}

/**
 * 保存済み設定を全般設定の編集開始点として表示します。
 * @returns {void} 返り値はありません。
 */
function populateGlobal() {
  if (!latestSettings) return;
  globalBaseline = structuredClone(latestSettings);
  for (const [path, input] of globalInputs) {
    const [group, key] = path.split(".");
    input.value = globalBaseline[group]?.[key] ?? "";
  }
  globalDirty = false;
  byId("global-error").hidden = true;
}

/**
 * 全般設定の未保存変更、版競合、保存中を表示します。
 * @returns {void} 返り値はありません。
 */
function updateGlobalStatus() {
  if (!globalBaseline) return;
  globalDirty = [...globalInputs].some(([path, input]) => {
    const [group, key] = path.split(".");
    return input.value !== String(globalBaseline[group]?.[key] ?? "");
  });
  const conflict = globalDirty && latestSettings?.revision !== globalBaseline.revision;
  const blockedProduct = latestState?.products.find(item => optionBlockedReason(item));
  const globallyBlocked = latestState?.settings_editable === false || Boolean(blockedProduct);
  byId("settings-lock").hidden = !globallyBlocked;
  byId("settings-lock").textContent = globallyBlocked ? "全般設定の保存には、全商品をOFFにして注文の取消・照合を完了させてください。入力はそのまま保持します。" : "";
  byId("settings-conflict").hidden = !conflict;
  byId("settings-status").textContent = mutationBusy ? "保存しています…" : conflict ? "未保存の変更 · 最新設定との確認が必要" : globalDirty ? "未保存の変更があります" : "すべて保存済み";
  byId("settings-status").classList.toggle("dirty", globalDirty);
  byId("global-fields").disabled = mutationBusy || !connected || !latestSettings;
  byId("global-save").disabled = !globalDirty || conflict || mutationBusy || !settingsReady() || globallyBlocked || latestState?.closing;
  byId("global-reset").disabled = mutationBusy || !latestSettings || !globalDirty;
}

/**
 * 全般設定の入力を保存し、未編集の項目と商品設定を保持します。
 * @param {SubmitEvent} event - フォーム送信イベント。
 * @returns {Promise<void>} 保存処理の完了。
 */
async function saveGlobal(event) {
  event.preventDefault();
  updateGlobalStatus();
  if (byId("global-save").disabled || !byId("global-form").reportValidity()) return;
  const draft = structuredClone(globalBaseline);
  for (const [path, input] of globalInputs) {
    const [group, key] = path.split(".");
    draft[group][key] = input.type === "number" ? Number(input.value) : input.value.trim();
  }
  mutationBusy = true;
  mutationEpoch++;
  byId("global-error").hidden = true;
  render();
  try {
    const result = await api("/api/settings", "PUT", draft);
    applyResult(result, true);
    notify(result.message || "全般設定を保存しました。");
  } catch (error) {
    byId("global-error").textContent = error.message;
    byId("global-error").hidden = false;
  } finally {
    mutationBusy = false;
    mutationEpoch++;
    await refresh();
    await loadConfiguration();
    render();
  }
}

/**
 * 手法候補を昼夜別に表示し、未対応の候補は選択不可にします。
 * @param {HTMLSelectElement} select - 候補を表示する選択欄。
 * @param {string} phase - 日中ならday、夜間ならnight。
 * @param {string} current - 保存済みの手法名。
 * @param {string} reference - 商品が参照する市場の接頭辞。
 * @returns {void} 返り値はありません。
 */
function populateRules(select, phase, current, reference) {
  select.replaceChildren();
  const choices = rules.filter(rule => rule.reference === reference && (rule[phase] === true || rule.supported === false));
  for (const rule of choices) {
    const option = element("option", "", rule.name + (rule.supported === false ? "（未対応）" : ""));
    option.value = rule.name;
    option.disabled = rule.supported === false;
    select.append(option);
  }
  if (![...select.options].some(option => option.value === current)) {
    const preserved = element("option", "", current + "（保存済み・候補外）");
    preserved.value = current;
    preserved.disabled = true;
    select.prepend(preserved);
  }
  select.value = current;
}

// ----------------------------------------

/**
 * 保存済み時間帯を表示用に複製し、旧設定は昼夜の全体枠へ変換します。
 * @param {object} item - 商品設定。
 * @returns {Array<object>} 独立した時間帯配列。
 */
function effectiveWindows(item) {
  if (item.windows?.length) return structuredClone(item.windows);
  return [{ id: "Day", session: "Day", start: "SESSION_START", end: "SESSION_END", method: item.day }, { id: "Night", session: "Night", start: "SESSION_START", end: "SESSION_END", method: item.night }];
}

/**
 * 商品カードに時間帯の件数と手法を短く表示します。
 * @param {Array<object>} windows - 保存済み時間帯。
 * @param {string} kind - Day又はNight。
 * @returns {string} 手法名又は複数枠の表示。
 */
function windowSummary(windows, kind) {
  const selected = windows.filter(item => item.session === kind);
  if (!selected.length) return "静観";
  if (selected.length === 1) return selected[0].method || "静観";
  return selected.length + "時間帯";
}

/**
 * 日本時間又は米国開始相対の時計入力を作ります。
 * @param {string} labelText - 開始又は終了のラベル。
 * @param {string} value - 保存済み時計。
 * @returns {object} 入力要素と時計読取関数。
 */
function createWindowClock(labelText, value) {
  const field = element("div", "field window-clock");
  const label = element("label", "", labelText);
  const kind = element("select");
  kind.setAttribute("aria-label", labelText + "の基準");
  for (const [id, name] of [["JST", "日本時間"], ["SESSION_START", "立会開始"], ["SESSION_END", "立会終了"], ["US_OPEN", "米国開始"], ["US_BEFORE", "米国開始の前"], ["US_AFTER", "米国開始の後"]]) {
    const option = element("option", "", name); option.value = id; kind.append(option);
  }
  const input = element("input");
  input.setAttribute("aria-label", labelText + "の時刻又は分数");
  const unit = element("span", "field-hint");
  let selected = "JST", initial = value;
  if (["SESSION_START", "SESSION_END", "US_OPEN"].includes(value)) { selected = value; initial = ""; }
  const relative = /^US_OPEN([+-])(\d{2}):(\d{2})$/.exec(value);
  if (relative) { selected = relative[1] === "-" ? "US_BEFORE" : "US_AFTER"; initial = String(Number(relative[2]) * 60 + Number(relative[3])); }
  kind.value = selected;
  const sync = () => {
    const relativeClock = kind.value === "US_BEFORE" || kind.value === "US_AFTER";
    input.hidden = !(kind.value === "JST" || relativeClock);
    input.required = !input.hidden;
    input.type = relativeClock ? "number" : "time";
    input.step = relativeClock ? "1" : "60";
    if (relativeClock) { input.min = "0"; input.max = "720"; }
    else { input.removeAttribute("min"); input.removeAttribute("max"); }
    unit.textContent = relativeClock ? "分" : kind.value === "JST" ? "日本時間 · 夜間の午前は翌日" : "";
  };
  sync(); input.value = initial;
  kind.addEventListener("change", () => { sync(); input.value = kind.value === "JST" ? "17:00" : "15"; updateProductStatus(); });
  label.append(kind); field.append(label, input, unit);
  return { field, kind, input, read: () => {
    if (kind.value === "JST") return input.value;
    if (kind.value === "US_BEFORE" || kind.value === "US_AFTER") {
      const minutes = Number(input.value);
      return "US_OPEN" + (kind.value === "US_BEFORE" ? "-" : "+") + String(Math.floor(minutes / 60)).padStart(2, "0") + ":" + String(minutes % 60).padStart(2, "0");
    }
    return kind.value;
  } };
}

/**
 * 一つの時間帯を編集する行を追加します。
 * @param {object} window - 時間帯ID、立会、開始、終了、手法。
 * @returns {void} 返り値はありません。
 */
function appendWindowRow(window) {
  const container = element("section", "window-editor");
  const top = element("div", "window-row-top");
  const sessionLabel = element("label", "", "立会 ");
  const session = element("select");
  for (const [id, name] of [["Day", "日中"], ["Night", "夜間"]]) { const option = element("option", "", name); option.value = id; session.append(option); }
  session.value = window.session; sessionLabel.append(session);
  const remove = element("button", "button secondary", "削除"); remove.type = "button";
  top.append(sessionLabel, element("small", "field-hint", window.id), remove);
  const clocks = element("div", "window-clocks");
  const start = createWindowClock("開始", window.start), end = createWindowClock("終了", window.end);
  clocks.append(start.field, end.field);
  const methodLabel = element("label", "field", "手法");
  const method = element("select"); method.setAttribute("aria-label", "この時間帯の手法");
  const methodHint = element("small", "field-hint");
  const updateMethodHint = () => { methodHint.textContent = rules.find(rule => rule.name === method.value)?.window_hint || ""; };
  const reference = productEditor.product.startsWith("NK225") ? "NK225" : "TOPIX";
  const populate = (current) => {
    populateRules(method, session.value.toLowerCase(), current, reference);
    const oldEmpty = [...method.options].find(option => option.value === ""); if (oldEmpty) oldEmpty.remove();
    const pause = element("option", "", "静観（新規取引なし）"); pause.value = ""; method.prepend(pause); method.value = current;
    updateMethodHint();
  };
  populate(window.method);
  session.addEventListener("change", () => { populate(""); updateProductStatus(); });
  method.addEventListener("change", updateMethodHint);
  methodLabel.append(method, methodHint); container.append(top, clocks, methodLabel);
  const row = { id: window.id, container, session, start, end, method };
  productEditor.windowRows.push(row);
  remove.addEventListener("click", () => { productEditor.windowRows = productEditor.windowRows.filter(value => value !== row); container.remove(); updateProductStatus(); });
  byId("product-windows").append(container);
}

/**
 * 編集中の時間帯一覧をREST設定の形で取り出します。
 * @returns {Array<object>} 時間帯設定。
 */
function readWindowRows() {
  return productEditor.windowRows.map(row => ({ id: row.id, session: row.session.value, start: row.start.read(), end: row.end.read(), method: row.method.value }));
}

/**
 * 商品内で重複しないIDを付けて静観枠を追加します。
 * @returns {void} 返り値はありません。
 */
function addWindowRow() {
  if (!productEditor || productEditor.windowRows.length >= 64) return;
  let number = 1; while (productEditor.windowRows.some(row => row.id === "window" + number)) number++;
  appendWindowRow({ id: "window" + number, session: "Night", start: "SESSION_START", end: "SESSION_END", method: "" });
  updateProductStatus();
}

/**
 * 商品の保存済み値を読み込み、アクセシブルなモーダルを開きます。
 * @param {string} product - 商品ID。
 * @param {HTMLElement} opener - 閉じた後にフォーカスを戻す要素。
 * @returns {void} 返り値はありません。
 */
function openProduct(product, opener) {
  if (!settingsReady() || mutationBusy) return;
  if (optionBlockedReason(latestState?.products.find(item => item.product === product))) return;
  const current = latestSettings.instruments.find(item => item.product === product);
  if (!current) return;
  productEditor = { product, opener, revision: latestSettings.revision, baseline: structuredClone(current), baselineWindows: effectiveWindows(current), windowRows: [] };
  byId("product-dialog-title").textContent = productInfo[product].name;
  byId("product-current").textContent = "上限 " + current.max_per_side + " 枚 / 方向　·　" + productEditor.baselineWindows.length + "時間帯";
  byId("product-quantity").value = current.max_per_side;
  byId("product-windows").replaceChildren();
  for (const window of productEditor.baselineWindows) appendWindowRow(window);
  byId("product-rules-note").textContent = rulesLoaded ? "登録された手法名から選択できます。未対応の手法は選択できません。" : "手法候補を取得できないため、現在は保存できません。閉じてからもう一度お試しください。";
  byId("product-error").hidden = true;
  updateProductStatus();
  byId("product-dialog").showModal();
  byId("product-quantity").focus();
}

/**
 * 商品設定の未保存状態と選択中のルール情報を表示します。
 * @returns {void} 返り値はありません。
 */
function updateProductStatus() {
  if (!productEditor) return;
  const baseline = productEditor.baseline;
  const dirty = byId("product-quantity").value !== String(baseline.max_per_side) || JSON.stringify(readWindowRows()) !== JSON.stringify(productEditor.baselineWindows);
  const conflict = latestSettings?.revision !== productEditor.revision;
  const blocked = optionBlockedReason(latestState?.products.find(item => item.product === productEditor.product));
  const candidatesValid = productEditor.windowRows.length > 0 && productEditor.windowRows.every(row => {
    const select = row.method;
    return select.selectedOptions.length === 1 && !select.selectedOptions[0].disabled;
  });
  byId("product-fields").disabled = mutationBusy || !connected;
  byId("product-close").disabled = mutationBusy;
  byId("product-cancel").disabled = mutationBusy;
  byId("product-save").disabled = !dirty || !rulesLoaded || !candidatesValid || conflict || Boolean(blocked) || mutationBusy || !settingsReady() || latestState?.closing;
  byId("product-save-status").textContent = mutationBusy ? "保存しています…" : conflict ? "設定が更新されました。開き直してください。" : blocked || (dirty ? "未保存の変更" : "保存済み");
  byId("product-save-status").classList.toggle("dirty", dirty || conflict);
  byId("product-window-add").disabled = productEditor.windowRows.length >= 64 || mutationBusy;
}

/**
 * 商品モーダルを閉じ、起点の商品設定ボタンにフォーカスを戻します。
 * @returns {void} 返り値はありません。
 */
function closeProduct() {
  if (mutationBusy) return;
  byId("product-dialog").close();
  const opener = productEditor?.opener;
  productEditor = null;
  opener?.focus();
}

/**
 * キーボードのフォーカスを商品モーダルの操作要素内で循環させます。
 * @param {KeyboardEvent} event - キーボード操作のイベント。
 * @returns {void} 返り値はありません。
 */
function trapProductFocus(event) {
  if (event.key !== "Tab") return;
  const focusable = [...byId("product-dialog").querySelectorAll("button:not(:disabled), input:not(:disabled), select:not(:disabled), a[href]")].filter(node => node.getClientRects().length > 0);
  if (!focusable.length) { event.preventDefault(); return; }
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}

/**
 * 商品の数量と時間帯別手法を永続保存します。
 * @param {SubmitEvent} event - フォーム送信イベント。
 * @returns {Promise<void>} 保存処理の完了。
 */
async function saveProduct(event) {
  event.preventDefault();
  updateProductStatus();
  if (!productEditor || byId("product-save").disabled || !byId("product-form").reportValidity()) return;
  const product = productEditor.product;
  const body = { revision: productEditor.revision, max_per_side: Number(byId("product-quantity").value), day: productEditor.baseline.day || "", night: productEditor.baseline.night || "", windows: readWindowRows() };
  mutationBusy = true;
  mutationEpoch++;
  byId("product-error").hidden = true;
  render();
  let success = false;
  try {
    const result = await api("/api/products/" + encodeURIComponent(product) + "/settings", "PUT", body);
    applyResult(result);
    notify(result.message || productInfo[product].name + "の商品設定を保存しました。");
    success = true;
  } catch (error) {
    byId("product-error").textContent = error.message;
    byId("product-error").hidden = false;
  } finally {
    mutationBusy = false;
    mutationEpoch++;
    if (success) closeProduct();
    await refresh();
    await loadConfiguration();
    render();
  }
}

/**
 * 売買管理・保存済み損益・全般設定を切り替え、編集中の入力を保持します。
 * @returns {void} 返り値はありません。
 */
function route() {
  const page = location.hash === "#settings" ? "settings" : location.hash === "#session-pl" ? "session-pl" : "dashboard";
  for (const name of ["dashboard", "session-pl", "settings"]) {
    byId(name + "-page").hidden = page !== name;
    if (page === name) byId("nav-" + name).setAttribute("aria-current", "page");
    else byId("nav-" + name).removeAttribute("aria-current");
  }
  document.title = "SessionTrader | " + ({ dashboard: "売買管理", "session-pl": "セッション損益", settings: "全般設定" })[page];
  if (page === "session-pl") void refreshSessionPL();
  window.scrollTo(0, 0);
}

// ----------------------------------------
/**
 * セッション損益APIの行を検査し、不明値を0円として表示しないようにします。
 * @param {object} row - CSV読取APIが返した一行。
 * @returns {boolean} 表示に必要なキーと数値が揃う場合true。
 */
function validSessionPLRow(row) {
  return row && /^\d{4}-\d{2}-\d{2}_(Day|Night)$/.test(row.session) && typeof row.product === "string" && row.product.length > 0 &&
    (row.mode === "live" || row.mode === "dryrun") && Number.isFinite(row.profit) && row.profit >= 0 &&
    Number.isFinite(row.loss) && row.loss >= 0 && Number.isFinite(row.net) && Number.isSafeInteger(row.entry_qty) && row.entry_qty >= 0;
}

/**
 * 保存済み行を商品・モードで絞り、正額の損失と符号付き収支を分けて表示します。
 * @returns {void} 返り値はありません。データが未取得・空・絞込ゼロの状態を区別します。
 */
function renderSessionPL() {
  const body = byId("session-pl-rows");
  body.replaceChildren();
  byId("session-pl-table-wrap").hidden = true;
  byId("session-pl-empty").hidden = true;
  if (sessionPLRows === null) {
    byId("session-pl-status").textContent = sessionPLError ? "損益は未取得です。更新して再度確認してください。" : "損益を取得しています…";
    return;
  }
  const product = byId("session-pl-product").value;
  const mode = byId("session-pl-mode").value;
  const filtered = sessionPLRows.filter(row => (!product || row.product === product) && (!mode || row.mode === mode));
  byId("session-pl-status").textContent = (sessionPLError ? "前回取得した記録を表示中・更新失敗 · " : "") + filtered.length.toLocaleString("ja-JP") + " 件表示 / 全 " + sessionPLRows.length.toLocaleString("ja-JP") + " 件";
  if (filtered.length === 0) {
    byId("session-pl-empty").hidden = false;
    byId("session-pl-empty-title").textContent = sessionPLRows.length ? "この条件に一致するセッションはありません" : "セッション損益の記録はまだありません";
    byId("session-pl-empty-description").textContent = sessionPLRows.length ? "商品・モードの条件を変更してください。" : "セッション終了後に記録が保存されると表示されます。";
    return;
  }
  const fragment = document.createDocumentFragment();
  for (const row of filtered) {
    const tr = element("tr", "");
    tr.append(element("td", "", row.session.replace("_Day", " 日中").replace("_Night", " 夜間")), element("td", "", productInfo[row.product]?.name || row.product));
    const modeCell = element("td", "");
    modeCell.append(element("span", "session-pl-mode is-" + row.mode, row.mode === "live" ? "ON・実取引" : "DryRun・仮想売買"));
    tr.append(modeCell);
    const format = value => value.toLocaleString("ja-JP", { maximumFractionDigits: 2 });
    tr.append(element("td", "amount" + (row.profit > 0 ? " positive" : ""), format(row.profit)));
    tr.append(element("td", "amount" + (row.loss > 0 ? " negative" : ""), format(row.loss)));
    const sign = row.net > 0 ? "+" : row.net < 0 ? "−" : "";
    tr.append(element("td", "amount net" + (row.net > 0 ? " positive" : row.net < 0 ? " negative" : ""), sign + format(Math.abs(row.net))));
    tr.append(element("td", "amount", row.entry_qty.toLocaleString("ja-JP")));
    fragment.append(tr);
  }
  body.append(fragment);
  byId("session-pl-table-wrap").hidden = false;
}

/**
 * 正本CSVをAPIで再取得し、削除された記録を古い状態から補完しません。
 * @returns {Promise<void>} 取得処理の完了。失敗時は前回表示と取得時刻を残し、異常を明示します。
 */
async function refreshSessionPL() {
  if (sessionPLLoading || document.hidden || location.hash !== "#session-pl") return;
  sessionPLLoading = true;
  byId("session-pl-refresh").disabled = true;
  byId("session-pl-refresh").textContent = "更新中…";
  byId("session-pl-table-wrap").setAttribute("aria-busy", "true");
  try {
    const data = await api("/api/session-pl");
    if (!Array.isArray(data.rows) || !data.rows.every(validSessionPLRow)) throw new Error("セッション損益データの形式を確認できません。");
    sessionPLRows = [...data.rows].sort((a, b) => b.session.localeCompare(a.session) || a.product.localeCompare(b.product) || a.mode.localeCompare(b.mode));
    sessionPLError = "";
    sessionPLLastSuccess = Date.now();
    byId("session-pl-updated").textContent = clock(new Date(sessionPLLastSuccess).toISOString(), true);
    const select = byId("session-pl-product");
    const available = new Set(Array.from(select.options, option => option.value));
    for (const row of sessionPLRows) if (!available.has(row.product)) { const option = element("option", "", productInfo[row.product]?.name || row.product); option.value = row.product; select.append(option); available.add(row.product); }
  } catch (error) {
    sessionPLError = error.message;
  } finally {
    sessionPLLoading = false;
    byId("session-pl-refresh").disabled = false;
    byId("session-pl-refresh").textContent = "更新";
    byId("session-pl-table-wrap").setAttribute("aria-busy", "false");
    byId("session-pl-error").hidden = !sessionPLError;
    byId("session-pl-error").textContent = sessionPLError ? "セッション損益を更新できません。 " + sessionPLError : "";
    renderSessionPL();
  }
}

/**
 * 管理画面の接続状態を表示し、取得不能時は変更操作を止めます。
 * @param {boolean} healthy - 状態取得に成功した場合はtrue。
 * @param {string} message - 失敗時の説明。
 * @returns {void} 返り値はありません。
 */
function connectionState(healthy, message = "") {
  connected = healthy;
  byId("connection-dot").classList.toggle("online", healthy);
  byId("connection-dot").classList.toggle("error", !healthy);
  byId("connection-message").textContent = healthy ? "画面接続中" : "接続を確認中";
  byId("connection-alert").hidden = healthy;
  byId("connection-alert").textContent = healthy ? "" : "状態を更新できません。表示は最後に取得した状態です。サーバー側の売買は継続している場合があります。 " + message;
  render();
}

/**
 * 運転状態を取得し、保存処理より古い応答による表示の巻き戻しを防ぎます。
 * @returns {Promise<void>} 取得処理の完了。
 */
async function refresh() {
  if (stateLoading || mutationBusy) return;
  stateLoading = true;
  const epoch = mutationEpoch;
  try {
    const state = await api("/api/state");
    if (epoch !== mutationEpoch) return;
    if (!Array.isArray(state.products) || !state.csrf_token) throw new Error("状態データの形式を確認できません。");
    latestState = state;
    lastSuccessfulAt = Date.now();
    byId("last-updated").textContent = clock(state.server_time || new Date().toISOString(), true);
    connectionState(true);
    if (!latestSettings || !rulesLoaded || (state.settings_revision && state.settings_revision !== latestSettings.revision)) void loadConfiguration();
  } catch (error) {
    if (epoch === mutationEpoch) connectionState(false, error.message);
  } finally { stateLoading = false; }
}

// ----------------------------------------
for (const product of Object.keys(productInfo)) cards.set(product, makeCard(product));
for (const [product, info] of Object.entries(productInfo)) { const option = element("option", "", info.name); option.value = product; byId("session-pl-product").append(option); }
byId("session-pl-product").addEventListener("change", renderSessionPL);
byId("session-pl-mode").addEventListener("change", renderSessionPL);
byId("session-pl-refresh").addEventListener("click", () => void refreshSessionPL());
buildGlobal();
byId("global-form").addEventListener("input", updateGlobalStatus);
byId("global-form").addEventListener("submit", saveGlobal);
byId("global-reset").addEventListener("click", () => { populateGlobal(); updateGlobalStatus(); });
byId("product-form").addEventListener("input", updateProductStatus);
byId("product-form").addEventListener("change", updateProductStatus);
byId("product-form").addEventListener("submit", saveProduct);
byId("product-window-add").addEventListener("click", addWindowRow);
byId("product-close").addEventListener("click", closeProduct);
byId("product-cancel").addEventListener("click", closeProduct);
byId("product-dialog").addEventListener("cancel", event => { event.preventDefault(); closeProduct(); });
byId("product-dialog").addEventListener("keydown", trapProductFocus);
window.addEventListener("hashchange", route);
window.addEventListener("beforeunload", event => {
  const productDirty = productEditor && byId("product-save-status").classList.contains("dirty");
  if (globalDirty || productDirty || mutationBusy) { event.preventDefault(); event.returnValue = ""; }
});
document.addEventListener("visibilitychange", () => { if (!document.hidden) { void refresh(); void refreshSessionPL(); } });
route();
void refresh();
void loadConfiguration();
setInterval(() => {
  if (lastSuccessfulAt && Date.now() - lastSuccessfulAt > 10000 && connected && !mutationBusy) connectionState(false);
  void refresh();
}, 2000);
setInterval(() => { void refreshSessionPL(); }, 5000);
