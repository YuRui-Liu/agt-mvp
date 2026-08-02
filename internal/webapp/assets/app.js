"use strict";

const AUTH_KEY = "kuaiAuth";
const UPLOAD_DRAFT_KEY = "kuaiUploadDraft";
const flowLogic = globalThis.KuaiFlowLogic;
const initialState = {
  scopes: [],
  sources: [],
  selectedScope: null,
  preparation: null,
  auth: null,
  consent: false,
  pending: false,
  uploadDraft: null
};
const state = {...initialState};
const byID = (id) => document.getElementById(id);

function bootstrap() {
  try { return JSON.parse(byID("bootstrapData").textContent) || {}; } catch (_) { return {}; }
}

function setStatus(message) { byID("status").textContent = message; }
function showError(message) {
  const node = byID("error");
  node.textContent = message;
  node.hidden = false;
  node.focus();
}
function clearError() { byID("error").hidden = true; }

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.body) headers.set("Content-Type", "application/json");
  if (state.auth && state.auth.bearer) headers.set("Authorization", `Bearer ${state.auth.bearer}`);
  let response;
  try { response = await fetch(path, {...options, headers}); }
  catch (_) { throw new Error("网络连接中断，请重试。"); }
  const type = response.headers.get("Content-Type") || "";
  const body = type.includes("application/json") ? await response.json() : null;
  if (!response.ok) throw new Error(body && body.message ? body.message : "操作未完成，请重试。");
  return {response, body};
}

function formatTime(value) {
  if (!value) return "时间未知";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "时间未知" : date.toLocaleString("zh-CN", {dateStyle: "medium", timeStyle: "short"});
}
function scopeType(value) {
  return {project: "Project", workspace: "Workspace", conversation_group: "Conversation Group",
    session_collection: "Session Collection"}[value] || "Assessment Scope";
}

function agentName(product, names) {
  return (Object.hasOwn(names, product) && names[product]) ||
    (typeof product === "string" && /^[a-z0-9][a-z0-9-]{0,63}$/.test(product)
    ? product : "Agent 未知");
}

function renderSources() {
  return flowLogic.renderSources(document, state.sources);
}

function renderScopes() {
  const list = byID("scopeList");
  const agentNames = flowLogic.sourceDisplayNames(state.sources);
  const workflow = byID("selectionWorkflow");
  if (workflow && list.contains(workflow)) byID("scopePanel").append(workflow);
  list.replaceChildren();
  byID("emptyScopes").hidden = state.scopes.length !== 0;
  state.scopes.forEach((scope) => {
    const card = document.createElement("article");
    card.className = `scope-card${state.selectedScope && state.selectedScope.key === scope.key ? " selected" : ""}${scope.selectable ? "" : " unsupported"}`;
    card.dataset.scopeKey = scope.key;
    const button = scope.selectable === true ? document.createElement("button") : document.createElement("div");
    if (scope.selectable === true) {
      button.type = "button";
      button.disabled = state.pending;
      button.setAttribute("aria-pressed", state.selectedScope && state.selectedScope.key === scope.key ? "true" : "false");
    }
    button.className = "scope-choice";
    const marker = document.createElement("span");
    marker.className = "radio-mark";
    const copy = document.createElement("span");
    copy.className = "scope-copy";
    const title = document.createElement("strong");
    title.textContent = scope.label || "未命名范围";
    const meta = document.createElement("span");
    meta.textContent = `${scopeType(scope.type)} · ${(scope.agents || []).map((product) => agentName(product, agentNames)).join("、") || "Agent 未知"}`;
    const detail = document.createElement("span");
    detail.textContent = flowLogic.formatScopeDetails({
      session_count: scope.session_count,
      ended_at: scope.ended_at,
      bytes: scope.bytes
    }, formatTime);
    const capability = document.createElement("span");
    capability.className = "capabilities";
    capability.textContent = scope.selectable ? `能力：${(scope.capabilities || []).join(" · ") || "消息"}` : "已检测，格式尚未验证";
    copy.append(title, meta, detail, capability);
    button.append(marker, copy);
    if (scope.selectable === true) {
      flowLogic.bindScopeSelection(button, scope, () => state.selectedScope,
        (selected) => { state.selectedScope = selected; }, (selected) => {
          byID("selectedScopeName").textContent = `${scopeType(selected.type)} · ${selected.label}`;
          byID("prepareScope").disabled = false;
          renderScopes();
        });
    }
    card.append(button);
    list.append(card);
  });
  if (state.preparation && state.selectedScope) {
    flowLogic.placeSelectionWorkflow(document, state.selectedScope.key);
  }
}

async function loadScopes() {
  try {
    const {body} = await api("/api/scopes");
    state.scopes = Array.isArray(body.scopes) ? body.scopes : [];
    state.sources = Array.isArray(body.sources) ? body.sources : [];
    if (state.uploadDraft) {
      const restored = flowLogic.restorePreparationState(state, state.scopes, sessionStorage, UPLOAD_DRAFT_KEY);
      if (!restored) flowLogic.hidePreparedWorkflows(document);
    }
    renderSources();
    renderScopes();
    setStatus(state.scopes.length ? "请选择一个可评估范围；页面不会默认选择。" : "未发现可评估范围。");
  } catch (error) { showError(error.message); setStatus("扫描未完成。"); }
}

function renderPreparation(body) {
  const list = byID("sessionProgress");
  list.replaceChildren();
  (body.session_progress || []).forEach((item, index) => {
    const row = document.createElement("li");
    const name = document.createElement("span");
    name.textContent = `Session ${index + 1}`;
    const status = document.createElement("strong");
    status.textContent = item.status === "exported" ? "已安全准备" : "等待";
    row.append(name, status);
    list.append(row);
  });
  flowLogic.showPreparedWorkflows(document, {authenticated: Boolean(state.auth)});
  flowLogic.placeSelectionWorkflow(document, state.selectedScope && state.selectedScope.key);
  byID("actionBar").hidden = true;
  if (!state.auth) flowLogic.scrollPreparedAuthentication(document, globalThis);
}

async function prepareSelected() {
  if (!state.selectedScope || state.pending) return;
  state.pending = true;
  byID("prepareScope").disabled = true;
  clearError();
  setStatus("正在本机逐 session 导出并脱敏…");
  try {
    const {body} = await api("/api/prepare", {method: "POST", body: JSON.stringify({scope_key: state.selectedScope.key})});
    state.preparation = body;
    state.uploadDraft = flowLogic.uploadDraft(state.uploadDraft, body.preparation_id, () => globalThis.crypto.randomUUID());
    state.uploadDraft.scope_key = state.selectedScope.key;
    state.uploadDraft.preparation = body;
    sessionStorage.setItem(UPLOAD_DRAFT_KEY, JSON.stringify(state.uploadDraft));
    renderPreparation(body);
    setStatus(state.auth ? "本地准备完成，请确认用途授权。" : "本地准备完成，请完成手机号认证。");
  } catch (error) {
    flowLogic.hidePreparedWorkflows(document);
    showError(error.message);
    setStatus("本地准备未完成。");
    byID("prepareScope").disabled = false;
  } finally { state.pending = false; }
}

async function requestOTP() {
  clearError();
  try {
    await api("/api/auth/request-code", {method: "POST", body: JSON.stringify({phone: byID("phone").value})});
    setStatus("验证码已发送，请输入 6 位验证码。");
  } catch (error) { showError(error.message); }
}

async function verifyOTP(event) {
  event.preventDefault();
  clearError();
  try {
    const phone = byID("phone").value;
    const {body} = await api("/api/auth/verify", {method: "POST", body: JSON.stringify({phone, code: byID("otp").value})});
    state.auth = body;
    sessionStorage.setItem(AUTH_KEY, JSON.stringify(body));
    byID("authForm").reset();
    byID("authPanel").hidden = true;
    byID("consentPanel").hidden = false;
    setStatus("认证完成，请阅读并明确确认三类用途。");
  } catch (error) { showError(error.message); }
}

async function submitData() {
  if (!state.consent || !state.preparation || state.pending) return;
  state.pending = true;
  byID("startAnalysis").disabled = true;
  clearError();
  try {
    await api("/api/consent", {method: "POST", body: JSON.stringify({version: "kuai-consent-v1"})});
    flowLogic.showExclusiveView(document, "uploadView");
    flowLogic.setUploadStage(document, 2);
    byID("actionBar").hidden = true;
    state.uploadDraft = flowLogic.uploadDraft(state.uploadDraft, state.preparation.preparation_id,
      () => globalThis.crypto.randomUUID());
    sessionStorage.setItem(UPLOAD_DRAFT_KEY, JSON.stringify(state.uploadDraft));
    const {body} = await api("/api/tasks", {method: "POST", body: JSON.stringify({
      preparation_id: state.preparation.preparation_id, idempotency_key: state.uploadDraft.idempotency_key
    })});
    flowLogic.setUploadStage(document, 3);
    sessionStorage.removeItem(UPLOAD_DRAFT_KEY);
    flowLogic.showUploadSuccess(document, body, state.selectedScope && state.selectedScope.label);
  } catch (error) {
    flowLogic.showExclusiveView(document, "flowView");
    flowLogic.showPreparedWorkflows(document, {authenticated: Boolean(state.auth)});
    flowLogic.placeSelectionWorkflow(document, state.selectedScope && state.selectedScope.key);
    showError(error.message);
    setStatus("上传未完成，可安全重试。");
    byID("startAnalysis").disabled = false;
  } finally { state.pending = false; }
}

function startNew() {
  sessionStorage.removeItem(UPLOAD_DRAFT_KEY);
  location.reload();
}

document.addEventListener("DOMContentLoaded", () => {
  const config = bootstrap();
  byID("serviceMode").textContent = config.service_mode === "mock" ? "本地 Mock · 数据不出机" : `可信服务 · ${config.service_host || "已配置"}`;
  try {
    const saved = JSON.parse(sessionStorage.getItem(AUTH_KEY));
    if (saved && typeof saved.bearer === "string") state.auth = saved;
  } catch (_) { sessionStorage.removeItem(AUTH_KEY); }
  try {
    const draft = JSON.parse(sessionStorage.getItem(UPLOAD_DRAFT_KEY));
    if (draft && typeof draft.preparation_id === "string" && typeof draft.idempotency_key === "string") {
      state.uploadDraft = draft;
      if (draft.preparation && draft.preparation.preparation_id === draft.preparation_id) {
        state.preparation = draft.preparation;
      }
    }
  } catch (_) { sessionStorage.removeItem(UPLOAD_DRAFT_KEY); }
  byID("prepareScope").addEventListener("click", prepareSelected);
  byID("sendOTP").addEventListener("click", requestOTP);
  byID("authForm").addEventListener("submit", verifyOTP);
  byID("consent").addEventListener("change", (event) => {
    state.consent = event.target.checked;
    byID("startAnalysis").disabled = !state.consent;
  });
  byID("startAnalysis").addEventListener("click", submitData);
  flowLogic.bindSuccessActions(byID("returnApplication"), byID("submitAgain"),
    () => { location.href = "/application"; }, startNew);
  loadScopes().then(() => {
    if (state.preparation && state.selectedScope) {
      renderScopes();
      renderPreparation(state.preparation);
      setStatus(state.auth ? "已恢复上传断点，可使用原安全重试标识继续。" : "已恢复本地准备结果，请完成认证。");
    }
  });
});
