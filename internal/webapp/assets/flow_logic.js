(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  root.KuaiFlowLogic = api;
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";
  function uploadDraft(current, preparationID, uuid) {
    if (current && current.preparation_id === preparationID && current.idempotency_key) return current;
    return {preparation_id: preparationID, idempotency_key: uuid()};
  }

  function selectScope(current, scope) {
    return scope && scope.selectable === true ? scope : current;
  }

  function restorePreparedScope(scopes, scopeKey) {
    if (typeof scopeKey !== "string") return null;
    return (Array.isArray(scopes) ? scopes : [])
      .find((scope) => scope && scope.selectable === true && scope.key === scopeKey) || null;
  }

  function restorePreparationState(state, scopes, storage, draftKey) {
    const selected = restorePreparedScope(scopes, state.uploadDraft && state.uploadDraft.scope_key);
    if (state.preparation && state.uploadDraft && selected) {
      state.selectedScope = selected;
      return selected;
    }
    state.selectedScope = null;
    state.preparation = null;
    state.uploadDraft = null;
    storage.removeItem(draftKey);
    return null;
  }

  function bindScopeSelection(button, scope, read, write, changed) {
    button.addEventListener("click", () => {
      const selected = selectScope(read(), scope);
      if (selected !== read()) {
        write(selected);
        changed(selected);
      }
    });
  }

  function showExclusiveView(document, activeID) {
    document.querySelectorAll("[data-flow-view]").forEach((view) => {
      view.hidden = view.id !== activeID;
    });
    return document.getElementById(activeID);
  }

  function showUploadSuccess(document, receipt = {}, scopeLabel = "") {
    const view = showExclusiveView(document, "successView");
    const scope = document.getElementById("successScope");
    const time = document.getElementById("successTime");
    const id = document.getElementById("successID");
    const title = document.getElementById("successTitle");
    if (scope) scope.textContent = scopeLabel || "已选范围";
    if (time) {
      const value = receipt.submitted_at || new Date().toISOString();
      const date = new Date(value);
      time.dateTime = Number.isNaN(date.getTime()) ? "" : date.toISOString();
      time.textContent = Number.isNaN(date.getTime()) ? "刚刚" :
        date.toLocaleString("zh-CN", {dateStyle: "medium", timeStyle: "short"});
    }
    if (id) {
      id.textContent = receipt.receipt_id || "";
    }
    if (title) title.focus();
    return view;
  }

  function bindSuccessActions(returnButton, submitAgainButton, navigate, reset) {
    returnButton.addEventListener("click", () => navigate("/application"));
    submitAgainButton.addEventListener("click", reset);
  }

  function setUploadStage(document, stage) {
    const current = Math.max(1, Math.min(3, Number(stage) || 1));
    const labels = ["已加密所选会话数据", "正在上传至远端存储", "正在校验提交结果"];
    const progress = document.getElementById("uploadProgress");
    if (progress) {
      progress.setAttribute("aria-valuenow", String(current));
      progress.setAttribute("aria-valuetext", labels[current - 1]);
    }
    document.querySelectorAll("#uploadStages li").forEach((item, index) => {
      const itemStage = index + 1;
      item.classList.toggle("completed", itemStage < current);
      item.classList.toggle("active", itemStage === current);
      if (itemStage === current) item.setAttribute("aria-current", "step");
      else item.removeAttribute("aria-current");
    });
    return current;
  }

  function formatScopeDetails(scope, formatTime = (value) => value || "") {
    const details = [
      `${scope.session_count} 个 session`,
      formatTime(scope.ended_at)
    ];
    if (Number.isFinite(scope.bytes) && scope.bytes > 0) {
      const units = ["B", "KB", "MB", "GB"];
      let amount = scope.bytes;
      let unit = 0;
      while (amount >= 1024 && unit < units.length - 1) {
        amount /= 1024;
        unit += 1;
      }
      details.push(`${unit === 0 ? amount : amount.toFixed(1)} ${units[unit]}`);
    }
    return details.filter(Boolean).join(" · ");
  }

  const SOURCE_STATUS_LABELS = Object.freeze({
    export_required: "需要官方导出",
    format_unsupported: "检测到暂不支持的会话格式",
    read_error: "读取失败，请重试",
    detected_unsupported: "已检测，暂不可评估",
    not_found: "未检测到本地会话",
  });
  const SOURCE_REASON_LABELS = Object.freeze({
    official_export_required: "需要通过产品官方导出后再提交",
    no_verified_session_schema: "尚未验证可靠的本地会话结构",
    no_verified_transcript_body: "尚未验证可评估的会话正文",
    no_distinct_local_format: "尚未发现可区分的本地会话格式",
  });
  const SOURCE_VERIFICATION_LABELS = Object.freeze({
    machine: "本机结构已验证",
    machine_verified: "本机结构已验证",
    fixture: "固定样例已验证",
    fixture_verified: "固定样例已验证",
    export_required: "需要官方导出验证",
    unsupported: "暂无可靠本地格式",
  });
  const SOURCE_CAPABILITY_LABELS = Object.freeze({
    messages: "消息",
    tools: "工具调用",
    reasoning: "推理过程",
  });
  const MAX_SOURCE_VIEWS = 64;

  function mappedLabel(map, key, fallback = "") {
    return typeof key === "string" && Object.hasOwn(map, key) ? map[key] : fallback;
  }

  function safeSourceName(value) {
    if (typeof value !== "string") return "未知 Agent";
    const name = value.trim();
    return name && name.length <= 80 ? name : "未知 Agent";
  }

  function sourceViewModel(source = {}) {
    if (!source || typeof source !== "object" || Array.isArray(source)) source = {};
    const state = typeof source.state === "string" ? source.state : "";
    let group = "attention";
    let statusLabel = mappedLabel(SOURCE_STATUS_LABELS, state, "暂不可评估");
    if (state === "ready") {
      if (source.selectable === true) {
        group = "available";
        statusLabel = "已可评估";
      } else {
        statusLabel = "未发现可评估会话";
      }
    } else if (state === "not_found") {
      group = "not_found";
    }
    const capabilities = Array.isArray(source.capabilities) ? source.capabilities : [];
    return Object.freeze({
      name: safeSourceName(source.display_name),
      group,
      statusLabel,
      reasonLabel: mappedLabel(SOURCE_REASON_LABELS, source.reason),
      verificationLabel: mappedLabel(SOURCE_VERIFICATION_LABELS, source.verification),
      capabilityLabels: capabilities
        .map((capability) => mappedLabel(SOURCE_CAPABILITY_LABELS, capability))
        .filter(Boolean),
    });
  }

  function boundedSources(sources) {
    const bounded = [];
    const products = new Set();
    for (const source of Array.isArray(sources) ? sources : []) {
      if (bounded.length >= MAX_SOURCE_VIEWS) break;
      const validSource = source && typeof source === "object" && !Array.isArray(source);
      const product = validSource && typeof source.product === "string" &&
        /^[a-z0-9][a-z0-9-]{0,63}$/.test(source.product) ? source.product : "";
      if (product && products.has(product)) continue;
      if (product) products.add(product);
      bounded.push(source);
    }
    return bounded;
  }

  function safeSourceInputs(sources) {
    return boundedSources(sources).map((source) => {
      if (!source || typeof source !== "object" || Array.isArray(source)) return {};
      const safe = {};
      if (typeof source.product === "string" && /^[a-z0-9][a-z0-9-]{0,63}$/.test(source.product)) {
        safe.product = source.product;
      }
      if (typeof source.display_name === "string") safe.display_name = source.display_name;
      if (typeof source.state === "string") safe.state = source.state;
      if (typeof source.selectable === "boolean") safe.selectable = source.selectable;
      if (typeof source.reason === "string") safe.reason = source.reason;
      if (typeof source.verification === "string") safe.verification = source.verification;
      if (Array.isArray(source.capabilities)) {
        safe.capabilities = source.capabilities.slice(0, 8).filter((value) => typeof value === "string");
      }
      return safe;
    });
  }

  function stableSortSourceViews(items) {
    return items.sort((left, right) => {
      if (left.name < right.name) return -1;
      if (left.name > right.name) return 1;
      if (left.statusLabel < right.statusLabel) return -1;
      if (left.statusLabel > right.statusLabel) return 1;
      return 0;
    });
  }

  function groupSourceViews(sources) {
    const groups = {available: [], attention: [], notFound: []};
    safeSourceInputs(sources).forEach((source) => {
      const view = sourceViewModel(source);
      if (view.group === "available") groups.available.push(view);
      else if (view.group === "not_found") groups.notFound.push(view);
      else groups.attention.push(view);
    });
    stableSortSourceViews(groups.available);
    stableSortSourceViews(groups.attention);
    stableSortSourceViews(groups.notFound);
    return groups;
  }

  function sourceDisplayNames(sources) {
    const names = Object.create(null);
    safeSourceInputs(sources).forEach((source) => {
      if (!source || typeof source !== "object" || Array.isArray(source)) return;
      if (typeof source.product !== "string" || !/^[a-z0-9][a-z0-9-]{0,63}$/.test(source.product)) return;
      if (Object.hasOwn(names, source.product)) return;
      names[source.product] = safeSourceName(source.display_name);
    });
    return names;
  }

  function sourceRow(document, source) {
    const row = document.createElement("div");
    row.className = `source-row source-${source.group}`;
    const name = document.createElement("strong");
    name.textContent = source.name;
    const status = document.createElement("span");
    status.className = "source-state";
    status.textContent = source.statusLabel;
    row.append(name, status);
    const details = [source.reasonLabel, source.verificationLabel, ...source.capabilityLabels].filter(Boolean);
    if (details.length) {
      const meta = document.createElement("small");
      meta.textContent = details.join(" · ");
      row.append(meta);
    }
    return row;
  }

  function renderSources(document, sources) {
    const grouped = groupSourceViews(sources);
    const bindings = [
      ["sourceReadyGroup", "sourceReadyList", grouped.available],
      ["sourceActionGroup", "sourceActionList", grouped.attention],
      ["sourceNotFoundGroup", "sourceNotFoundList", grouped.notFound],
    ];
    bindings.forEach(([groupID, listID, items]) => {
      const group = document.getElementById(groupID);
      const list = document.getElementById(listID);
      if (group) group.hidden = items.length === 0;
      if (list) list.replaceChildren(...items.map((item) => sourceRow(document, item)));
    });
    const missingSummary = document.getElementById("sourceNotFoundSummary");
    if (missingSummary) missingSummary.textContent = `${grouped.notFound.length} 个来源未检测到会话`;
    const summary = document.getElementById("sourceStatusSummary");
    if (summary) {
      summary.textContent = grouped.available.length
        ? `${grouped.available.length} 个 Agent 来源已可评估`
        : "尚无可评估的 Agent 来源";
    }
    return grouped;
  }

  function placeSelectionWorkflow(document, scopeKey) {
    const workflow = document.getElementById("selectionWorkflow");
    const cards = document.querySelectorAll("[data-scope-key]");
    const selectedCard = [...cards].find((card) => card.dataset.scopeKey === scopeKey);
    if (!workflow) return null;
    if (!selectedCard) {
      workflow.hidden = true;
      return null;
    }
    selectedCard.after(workflow);
    workflow.hidden = false;
    return workflow;
  }

  function showPreparedWorkflows(document, {authenticated = false} = {}) {
    const top = document.getElementById("topWorkflow");
    const local = document.getElementById("selectionWorkflow");
    const auth = document.getElementById("authPanel");
    const consent = document.getElementById("consentPanel");
    const progress = document.getElementById("progressPanel");
    if (top) top.hidden = false;
    if (local) local.hidden = false;
    if (auth) auth.hidden = Boolean(authenticated);
    if (consent) consent.hidden = !authenticated;
    if (progress) progress.hidden = false;
    return {top, local};
  }

  function hidePreparedWorkflows(document) {
    ["topWorkflow", "selectionWorkflow", "authPanel", "consentPanel", "progressPanel"]
      .forEach((id) => {
        const node = document.getElementById(id);
        if (node) node.hidden = true;
      });
  }

  function scrollPreparedAuthentication(document, environment) {
    try {
      const authPanel = document && document.getElementById("authPanel");
      if (!authPanel || authPanel.hidden || typeof authPanel.scrollIntoView !== "function") return false;
      if (!environment || typeof environment.matchMedia !== "function") return false;
      const reducedMotion = environment.matchMedia("(prefers-reduced-motion: reduce)").matches;
      authPanel.scrollIntoView({behavior: reducedMotion ? "auto" : "smooth", block: "start"});
      return true;
    } catch (_) {
      return false;
    }
  }

  return {uploadDraft, selectScope,
    restorePreparedScope, restorePreparationState,
    bindScopeSelection, showExclusiveView, showUploadSuccess, bindSuccessActions, setUploadStage,
    formatScopeDetails, safeSourceInputs, sourceViewModel, groupSourceViews, sourceDisplayNames, renderSources,
    placeSelectionWorkflow, showPreparedWorkflows, hidePreparedWorkflows,
    scrollPreparedAuthentication};
});
