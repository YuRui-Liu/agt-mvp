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
    return scope && scope.selectable ? scope : current;
  }

  function restorePreparedScope(scopes, scopeKey) {
    if (typeof scopeKey !== "string") return null;
    return (Array.isArray(scopes) ? scopes : [])
      .find((scope) => scope && scope.selectable && scope.key === scopeKey) || null;
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
    formatScopeDetails, placeSelectionWorkflow, showPreparedWorkflows, hidePreparedWorkflows,
    scrollPreparedAuthentication};
});
