const assert = require("node:assert/strict");
const test = require("node:test");
const logic = require("./flow_logic.js");

test("upload draft reuses key for retries and rotates for new preparation", () => {
  let generated = 0;
  const uuid = () => `key-${++generated}`;
  const first = logic.uploadDraft(null, "prep-a", uuid);
  first.preparation = {preparation_id: "prep-a", session_count: 2};
  const restored = JSON.parse(JSON.stringify(first));
  const retry = logic.uploadDraft(restored, "prep-a", uuid);
  const next = logic.uploadDraft(retry, "prep-b", uuid);
  assert.equal(retry.idempotency_key, first.idempotency_key);
  assert.equal(retry.preparation.session_count, 2);
  assert.notEqual(next.idempotency_key, first.idempotency_key);
  assert.equal(generated, 2);
});

test("selection has no default and accepts only literal boolean true", () => {
  let selected = null;
  for (const selectable of [false, "false", 1, null, {}, undefined]) {
    selected = logic.selectScope(selected, {key: "blocked", selectable});
    assert.equal(selected, null);
  }
  const ready = {key: "ready", selectable: true};
  selected = logic.selectScope(selected, ready);
  assert.equal(selected, ready);
});

test("restored preparation selects only a currently selectable matching scope", () => {
  const scopes = [
    {key: "blocked", selectable: false, label: "private path"},
    {key: "ready", selectable: true, label: "Safe scope"}
  ];
  assert.equal(logic.restorePreparedScope(scopes, "ready"), scopes[1]);
  assert.equal(logic.restorePreparedScope(scopes, "blocked"), null);
  assert.equal(logic.restorePreparedScope(scopes, "missing"), null);
  assert.equal(logic.restorePreparedScope(scopes, undefined), null);
  for (const selectable of ["false", 1, null, {}, undefined]) {
    assert.equal(logic.restorePreparedScope([{key: "unsafe", selectable}], "unsafe"), null);
  }
});

test("invalid prepared scope recovery atomically clears the stale draft", () => {
  const removed = [];
  const state = {
    selectedScope: null,
    preparation: {preparation_id: "stale-prep"},
    uploadDraft: {preparation_id: "stale-prep", scope_key: "missing"}
  };
  const restored = logic.restorePreparationState(state, [{key: "ready", selectable: true}],
    {removeItem(key) { removed.push(key); }}, "kuaiUploadDraft");

  assert.equal(restored, null);
  assert.equal(state.selectedScope, null);
  assert.equal(state.preparation, null);
  assert.equal(state.uploadDraft, null);
  assert.deepEqual(removed, ["kuaiUploadDraft"]);

  state.selectedScope = logic.selectScope(state.selectedScope, {key: "ready", selectable: true});
  assert.equal(state.selectedScope.key, "ready");
  assert.equal(state.preparation, null);
});

test("minimal DOM harness executes selection", () => {
  const button = () => ({
    listeners: {},
    addEventListener(type, fn) { this.listeners[type] = fn; },
    click() { this.listeners.click(); }
  });
  let selected = null;
  let rendered = 0;
  const choice = button();
  const scope = {key: "ready", selectable: true};
  logic.bindScopeSelection(choice, scope, () => selected, (value) => { selected = value; }, () => { rendered += 1; });
  assert.equal(selected, null);
  choice.click();
  assert.equal(selected, scope);
  assert.equal(rendered, 1);

});

test("source view model fails closed and never exposes raw diagnostic fields", () => {
  const secret = "/Users/private/session-id-secret";
  const cases = [
    [{state: "ready", selectable: true}, "available", "已可评估"],
    [{state: "ready", selectable: false}, "attention", "未发现可评估会话"],
    [{state: "not_found"}, "not_found", "未检测到本地会话"],
    [{state: "export_required"}, "attention", "需要官方导出"],
    [{state: "format_unsupported"}, "attention", "检测到暂不支持的会话格式"],
    [{state: "read_error"}, "attention", "读取失败，请重试"],
    [{state: "detected_unsupported"}, "attention", "已检测，暂不可评估"],
    [{state: "brand-new-state", status: "ready", selectable: true}, "attention", "暂不可评估"],
  ];

  for (const [source, group, statusLabel] of cases) {
    const view = logic.sourceViewModel({
      product: "safe-product",
      display_name: "安全 Agent",
      code: secret,
      error: secret,
      path: secret,
      sessionID: secret,
      reason: secret,
      ...source,
    });
    assert.equal(view.group, group);
    assert.equal(view.statusLabel, statusLabel);
    assert.doesNotMatch(JSON.stringify(view), /private|session-id-secret|brand-new-state/);
  }
});

test("source allowlists are prototype-safe for status reason verification and capabilities", () => {
  for (const key of ["constructor", "toString", "__proto__", "prototype", "unknown-private"]) {
    const view = logic.sourceViewModel({
      state: key,
      reason: key,
      verification: key,
      capabilities: [key],
    });
    assert.equal(view.statusLabel, "暂不可评估");
    assert.equal(view.reasonLabel, "");
    assert.equal(view.verificationLabel, "");
    assert.deepEqual(view.capabilityLabels, []);
    assert.doesNotMatch(JSON.stringify(view), /function|native code|\[object Object\]|unknown-private/);
  }
});

test("source view model translates only allowlisted reason verification and capabilities", () => {
  const reasons = new Map([
    ["official_export_required", "需要通过产品官方导出后再提交"],
    ["no_verified_session_schema", "尚未验证可靠的本地会话结构"],
    ["no_verified_transcript_body", "尚未验证可评估的会话正文"],
    ["no_distinct_local_format", "尚未发现可区分的本地会话格式"],
  ]);
  for (const [reason, expected] of reasons) {
    assert.equal(logic.sourceViewModel({state: "detected_unsupported", reason}).reasonLabel, expected);
  }
  assert.equal(logic.sourceViewModel({state: "read_error", reason: "private_reason"}).reasonLabel, "");

  const view = logic.sourceViewModel({
    state: "ready", selectable: true, verification: "machine_verified",
    capabilities: ["messages", "private-capability", "tools", "reasoning"],
  });
  assert.equal(view.verificationLabel, "本机结构已验证");
  assert.deepEqual(view.capabilityLabels, ["消息", "工具调用", "推理过程"]);
  assert.equal(logic.sourceViewModel({verification: "fixture_verified"}).verificationLabel, "固定样例已验证");
  assert.equal(logic.sourceViewModel({verification: "export_required"}).verificationLabel, "需要官方导出验证");
  assert.equal(logic.sourceViewModel({verification: "unsupported"}).verificationLabel, "暂无可靠本地格式");
  assert.equal(logic.sourceViewModel({state: "ready", verification: "private-verification"}).verificationLabel, "");
});

test("source grouping is deterministic and display names are mapped by safe product keys", () => {
  const sources = [
    {product: "codex", display_name: "Codex", state: "ready", selectable: true},
    {product: "trae", display_name: "TRAE", state: "export_required"},
    {product: "missing", display_name: "未安装 Agent", state: "not_found"},
  ];
  const grouped = logic.groupSourceViews(sources);
  assert.deepEqual(grouped.available.map(item => item.name), ["Codex"]);
  assert.deepEqual(grouped.attention.map(item => item.name), ["TRAE"]);
  assert.deepEqual(grouped.notFound.map(item => item.name), ["未安装 Agent"]);
  assert.deepEqual({...logic.sourceDisplayNames(sources)}, {
    codex: "Codex", trae: "TRAE", missing: "未安装 Agent",
  });
});

test("malformed source entries fail closed without interrupting the source overview", () => {
  assert.doesNotThrow(() => logic.groupSourceViews([null, 42, "private-state"]));
  const grouped = logic.groupSourceViews([null]);
  assert.equal(grouped.attention[0].statusLabel, "暂不可评估");
  assert.equal(grouped.attention[0].name, "未知 Agent");
  const names = logic.sourceDisplayNames([null, {product: "__proto__", display_name: "unsafe"}]);
  assert.equal(Object.getPrototypeOf(names), null);
  assert.deepEqual(Object.keys(names), []);
});

test("source collection is bounded, stably sorted, and first-wins deduplicated", () => {
  const sources = [
    {product: "same", display_name: "Zulu", state: "ready", selectable: true},
    {product: "same", display_name: "Overwritten secret", state: "read_error"},
    {product: "alpha", display_name: "Alpha", state: "ready", selectable: true},
    ...Array.from({length: 80}, (_, index) => ({
      product: `agent-${index}`,
      display_name: `Agent ${String(index).padStart(2, "0")}`,
      state: "not_found",
    })),
  ];
  const grouped = logic.groupSourceViews(sources);
  assert.deepEqual(grouped.available.map(item => item.name), ["Alpha", "Zulu"]);
  assert.equal(grouped.attention.length, 0);
  assert.equal(grouped.available.length + grouped.attention.length + grouped.notFound.length, 64);
  assert.deepEqual(grouped.notFound.slice(0, 3).map(item => item.name), ["Agent 00", "Agent 01", "Agent 02"]);

  const names = logic.sourceDisplayNames(sources);
  assert.equal(Object.getPrototypeOf(names), null);
  assert.equal(names.same, "Zulu");
  assert.equal(names.alpha, "Alpha");
  assert.equal(Object.keys(names).length, 64);
  assert.doesNotMatch(JSON.stringify(names), /Overwritten secret/);
});

test("safe source inputs retain only rendering allowlist fields", () => {
  const safe = logic.safeSourceInputs([{
    product: "codex", display_name: "Codex", state: "ready", selectable: true,
    reason: "official_export_required", verification: "machine_verified",
    capabilities: ["messages"], code: "private-code", error: "/Users/private",
    path: "/Users/private", sessionID: "private-session",
  }]);
  assert.deepEqual(safe, [{
    product: "codex", display_name: "Codex", state: "ready", selectable: true,
    reason: "official_export_required", verification: "machine_verified",
    capabilities: ["messages"],
  }]);
  assert.doesNotMatch(JSON.stringify(safe), /private|\/Users/);
});
