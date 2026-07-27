const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");
const vm = require("node:vm");

const template = fs.readFileSync("session-selection.html.tmpl", "utf8");
const match = template.match(
  /<script id="selection-logic">([\s\S]*?)<\/script>/
);
assert.ok(match, "selection-logic script is missing");
const context = {globalThis: {}};
vm.runInNewContext(match[1], context);
const logic = context.globalThis.SelectionLogic;

test("consent and selection jointly gate analysis", () => {
  let state = logic.createState(2);
  assert.equal(logic.buttonDisabled(state), true);
  state = logic.selectSession(state, 0, 1, {id: "s-1"});
  assert.equal(logic.buttonDisabled(state), false);
  state = logic.setConsent(state, false);
  assert.equal(logic.buttonDisabled(state), true);
});

test("accordion toggle records index-based focus target", () => {
  let state = logic.createState(2);
  state = logic.toggleAgent(state, 1);
  assert.deepEqual([...state.expandedAgents], [0, 1]);
  assert.equal(state.focusTarget, "agent-header-1");
  state = logic.toggleAgent(state, 0);
  assert.deepEqual([...state.expandedAgents], [1]);
});

test("selection records radio focus target", () => {
  const state = logic.selectSession(
    logic.createState(1), 0, 2, {id: "user-controlled-id"}
  );
  assert.equal(state.focusTarget, "session-radio-0-2");
  assert.equal(state.focusTarget.includes("user-controlled-id"), false);
});

test("duplicate submit is rejected while analysis is running", () => {
  const selected = logic.selectSession(
    logic.createState(1), 0, 0, {id: "s-1"}
  );
  const first = logic.beginAnalysis(selected);
  assert.equal(first.accepted, true);
  assert.equal(first.state.analyzing, true);
  const duplicate = logic.beginAnalysis(first.state);
  assert.equal(duplicate.accepted, false);
});

test("analysis freezes session, consent, and accordion until failure", () => {
  let state = logic.selectSession(
    logic.createState(2), 0, 0, {id: "A", title: "Session A"}
  );
  state = logic.beginAnalysis(state).state;
  const frozen = state;
  state = logic.selectSession(state, 1, 0, {id: "B", title: "Session B"});
  state = logic.setConsent(state, false);
  state = logic.toggleAgent(state, 0);
  assert.equal(state.selectedSession.id, "A");
  assert.equal(state.consent, true);
  assert.deepEqual([...state.expandedAgents], [...frozen.expandedAgents]);

  const failed = logic.completeAnalysis(state, false, {message: "retry"}).state;
  const changed = logic.selectSession(failed, 1, 0, {id: "B"});
  assert.equal(changed.selectedSession.id, "B");
  assert.equal(logic.setConsent(failed, false).consent, false);
  assert.notDeepEqual(
    [...logic.toggleAgent(failed, 0).expandedAgents],
    [...failed.expandedAgents]
  );
});

test("successful report remains bound to in-flight session A", () => {
  let state = logic.selectSession(
    logic.createState(1), 0, 0, {id: "A"}
  );
  state = logic.beginAnalysis(state).state;
  state = logic.selectSession(state, 0, 1, {id: "B"});
  const success = logic.completeAnalysis(
    state, true, {report_url: "/reports/A"}
  );
  assert.equal(success.state.selectedSession.id, "A");
  assert.equal(success.reportUrl, "/reports/A");
});

test("successful and failed fetch results produce testable states", () => {
  const selected = logic.selectSession(
    logic.createState(1), 0, 0, {id: "s-1"}
  );
  const running = logic.beginAnalysis(selected).state;
  const success = logic.completeAnalysis(running, true, {report_url: "/report"});
  assert.equal(success.ok, true);
  assert.equal(success.reportUrl, "/report");
  const failure = logic.completeAnalysis(
    running, false, {message: "画像分析失败", error: "legacy error"}
  );
  assert.equal(failure.ok, false);
  assert.equal(failure.state.analyzing, false);
  assert.equal(failure.state.error, "画像分析失败");
  assert.equal(failure.state.selectedSession.id, "s-1");
});

test("restoreFocus calls a found target and ignores a missing target", () => {
  let focused = 0;
  const document = {
    getElementById(id) {
      return id === "session-radio-0-1"
        ? {focus() { focused += 1; }} : null;
    }
  };
  assert.doesNotThrow(() => logic.restoreFocus(document, "missing"));
  logic.restoreFocus(document, "session-radio-0-1");
  assert.equal(focused, 1);
});
