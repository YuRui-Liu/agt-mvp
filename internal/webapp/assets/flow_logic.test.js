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

test("selection has no default and rejects unsupported scope", () => {
  let selected = null;
  selected = logic.selectScope(selected, {key: "blocked", selectable: false});
  assert.equal(selected, null);
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
