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

test("successful and failed fetch results produce testable states", () => {
  const selected = logic.selectSession(
    logic.createState(1), 0, 0, {id: "s-1"}
  );
  const running = logic.beginAnalysis(selected).state;
  const success = logic.completeAnalysis(running, true, {report_url: "/report"});
  assert.equal(success.ok, true);
  assert.equal(success.reportUrl, "/report");
  const failure = logic.completeAnalysis(running, false, {error: "<unsafe>"});
  assert.equal(failure.ok, false);
  assert.equal(failure.state.analyzing, false);
  assert.equal(failure.state.error, "<unsafe>");
  assert.equal(failure.state.selectedSession.id, "s-1");
});
