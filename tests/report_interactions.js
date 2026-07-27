const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");
const vm = require("node:vm");

const template = fs.readFileSync("avscore.html.tmpl", "utf8");
const source = template.match(
  /\/\* report-logic:start \*\/([\s\S]*?)\/\* report-logic:end \*\//
)[1];
const context = {};
vm.createContext(context);
vm.runInContext(source, context);
const logic = context.AVScoreReportLogic;

test("real scores change radar polygon geometry", () => {
  const low = logic.shapePoints([10, 20, 30, 40, 50, 60, 70], 260, 172);
  const high = logic.shapePoints([90, 20, 30, 40, 50, 60, 70], 260, 172);
  assert.notEqual(low, high);
  assert.equal(low.split(" ").length, 7);
});

test("tab and metric selection share one active index", () => {
  const flags = logic.selectionFlags(7, 4);
  assert.deepEqual(Array.from(flags), [false, false, false, false, true, false, false]);
});
