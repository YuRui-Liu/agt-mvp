const assert = require("node:assert/strict");
const test = require("node:test");
const logic = require("./flow_logic.js");
const {JSDOM} = require("jsdom");

test("real DOM executes scope selection", () => {
  const dom = new JSDOM(`<!doctype html><body>
    <button id="scope"></button>
  </body>`);
  const {document} = dom.window;
  let selected = null;
  const scope = {key: "scope-a", selectable: true};
  logic.bindScopeSelection(document.getElementById("scope"), scope, () => selected,
    value => { selected = value; }, () => {});
  document.getElementById("scope").click();
  assert.equal(selected, scope);

});

test("flow views are mutually exclusive in a real DOM", () => {
  const dom = new JSDOM(`<!doctype html><body>
    <main>
      <section id="flowView" data-flow-view></section>
      <section id="uploadView" data-flow-view hidden></section>
      <section id="successView" data-flow-view hidden></section>
    </main>
  </body>`);
  const {document} = dom.window;

  for (const activeID of ["flowView", "uploadView", "successView"]) {
    logic.showExclusiveView(document, activeID);
    const visible = [...document.querySelectorAll("[data-flow-view]")]
      .filter(view => !view.hidden)
      .map(view => view.id);
    assert.deepEqual(visible, [activeID]);
  }
});

test("upload confirmation success leaves only successView visible", () => {
  const dom = new JSDOM(`<!doctype html><body>
    <main>
      <section id="flowView" data-flow-view></section>
      <section id="uploadView" data-flow-view></section>
      <section id="successView" data-flow-view hidden>
        <h1 id="successTitle" tabindex="-1">提交成功</h1>
        <span id="successScope"></span>
        <time id="successTime"></time>
        <code id="successID"></code>
      </section>
    </main>
  </body>`);
  const {document} = dom.window;
  const receipt = {
    receipt_id: "KW-0123456789ABCDEF0123456789ABCDEF",
    submitted_at: "2026-07-31T12:34:56Z",
  };
  const scopeLabel = "校招前端候选人";

  logic.showUploadSuccess(document, receipt, scopeLabel);

  assert.equal(document.getElementById("flowView").hidden, true);
  assert.equal(document.getElementById("uploadView").hidden, true);
  assert.equal(document.getElementById("successView").hidden, false);
  assert.equal(document.getElementById("successScope").textContent, scopeLabel);
  assert.match(document.getElementById("successTime").textContent, /2026/);
  assert.equal(document.getElementById("successID").textContent, receipt.receipt_id);
  assert.equal(document.activeElement, document.getElementById("successTitle"));
});

test("success actions navigate to applications or reset to selection", () => {
  const dom = new JSDOM(`<!doctype html><body>
    <button id="returnApplication"></button>
    <button id="submitAgain"></button>
  </body>`);
  const {document} = dom.window;
  const navigation = [];
  let resets = 0;

  logic.bindSuccessActions(
    document.getElementById("returnApplication"),
    document.getElementById("submitAgain"),
    path => navigation.push(path),
    () => { resets += 1; },
  );

  document.getElementById("returnApplication").click();
  assert.deepEqual(navigation, ["/application"]);
  assert.equal(resets, 0);

  document.getElementById("submitAgain").click();
  assert.deepEqual(navigation, ["/application"]);
  assert.equal(resets, 1);
});

test("upload stages reflect actual submission boundaries", () => {
  const dom = new JSDOM(`<!doctype html><body>
    <div id="uploadProgress" role="progressbar"></div>
    <ol id="uploadStages">
      <li></li><li></li><li></li>
    </ol>
  </body>`);
  const {document} = dom.window;
  const progress = document.getElementById("uploadProgress");
  const stages = [...document.querySelectorAll("#uploadStages li")];

  logic.setUploadStage(document, 2);
  assert.equal(progress.getAttribute("aria-valuenow"), "2");
  assert.equal(progress.getAttribute("aria-valuetext"), "正在上传至远端存储");
  assert.equal(stages[0].className, "completed");
  assert.equal(stages[1].getAttribute("aria-current"), "step");
  assert.equal(stages[2].className, "");

  logic.setUploadStage(document, 3);
  assert.equal(progress.getAttribute("aria-valuenow"), "3");
  assert.equal(progress.getAttribute("aria-valuetext"), "正在校验提交结果");
  assert.equal(stages[1].className, "completed");
  assert.equal(stages[1].hasAttribute("aria-current"), false);
  assert.equal(stages[2].getAttribute("aria-current"), "step");
});

test("prepared workflows switch only the top authentication panel in a real DOM", () => {
  const dom = new JSDOM(`<!doctype html><body>
    <div id="topWorkflow" hidden>
      <section id="authPanel" hidden></section>
      <section id="consentPanel" hidden></section>
    </div>
    <div id="selectionWorkflow" hidden>
      <section id="progressPanel" hidden></section>
    </div>
  </body>`);
  const {document} = dom.window;

  logic.showPreparedWorkflows(document, {authenticated: false});
  assert.equal(document.getElementById("topWorkflow").hidden, false);
  assert.equal(document.getElementById("authPanel").hidden, false);
  assert.equal(document.getElementById("consentPanel").hidden, true);
  assert.equal(document.getElementById("selectionWorkflow").hidden, false);
  assert.equal(document.getElementById("progressPanel").hidden, false);

  logic.showPreparedWorkflows(document, {authenticated: true});
  assert.equal(document.getElementById("topWorkflow").hidden, false);
  assert.equal(document.getElementById("authPanel").hidden, true);
  assert.equal(document.getElementById("consentPanel").hidden, false);
  assert.equal(document.getElementById("selectionWorkflow").hidden, false);
  assert.equal(document.getElementById("progressPanel").hidden, false);
});

test("invalid recovery cleanup hides both prepared workflow regions", () => {
  const dom = new JSDOM(`<!doctype html><body>
    <div id="topWorkflow">
      <section id="authPanel"></section>
      <section id="consentPanel"></section>
    </div>
    <div id="selectionWorkflow">
      <section id="progressPanel"></section>
    </div>
  </body>`);
  const {document} = dom.window;

  logic.hidePreparedWorkflows(document);

  for (const id of ["topWorkflow", "selectionWorkflow", "authPanel", "consentPanel", "progressPanel"]) {
    assert.equal(document.getElementById(id).hidden, true, `${id} should be hidden`);
  }
});

test("prepared authentication scrolling respects motion preference", () => {
  for (const [reducedMotion, expectedBehavior] of [[true, "auto"], [false, "smooth"]]) {
    const dom = new JSDOM(`<!doctype html><body>
      <section id="authPanel"></section>
    </body>`);
    const {document} = dom.window;
    const calls = [];
    document.getElementById("authPanel").scrollIntoView = (options) => calls.push(options);
    const environment = {
      matchMedia(query) {
        assert.equal(query, "(prefers-reduced-motion: reduce)");
        return {matches: reducedMotion};
      },
    };

    logic.scrollPreparedAuthentication(document, environment);

    assert.deepEqual(calls, [{behavior: expectedBehavior, block: "start"}]);
  }
});

test("hidden prepared authentication is not scrolled", () => {
  const dom = new JSDOM(`<!doctype html><body>
    <section id="authPanel" hidden></section>
  </body>`);
  const {document} = dom.window;
  let scrolls = 0;
  document.getElementById("authPanel").scrollIntoView = () => { scrolls += 1; };

  logic.scrollPreparedAuthentication(document, {matchMedia() { return {matches: false}; }});

  assert.equal(scrolls, 0);
});

test("missing prepared authentication scrolling APIs return safely", () => {
  const dom = new JSDOM(`<!doctype html><body>
    <section id="authPanel"></section>
  </body>`);
  const {document} = dom.window;

  assert.doesNotThrow(() => logic.scrollPreparedAuthentication(document, {}));
  document.getElementById("authPanel").scrollIntoView = undefined;
  assert.doesNotThrow(() => logic.scrollPreparedAuthentication(document, {
    matchMedia() { return {matches: false}; },
  }));
});

test("prepared authentication scrolling failures never interrupt preparation state", () => {
  const failures = [
    {matchMedia() { throw new Error("matchMedia unavailable"); }},
    {matchMedia() {
      return {get matches() { throw new Error("matches unavailable"); }};
    }},
    {matchMedia() { return {matches: false}; }, scrollThrows: true},
  ];

  for (const environment of failures) {
    const dom = new JSDOM(`<!doctype html><body>
      <div id="topWorkflow" hidden>
        <section id="authPanel" hidden></section>
        <section id="consentPanel" hidden></section>
      </div>
      <div id="selectionWorkflow" hidden>
        <section id="progressPanel" hidden></section>
      </div>
    </body>`);
    const {document} = dom.window;
    if (environment.scrollThrows) {
      document.getElementById("authPanel").scrollIntoView = () => {
        throw new Error("scroll unavailable");
      };
    } else {
      document.getElementById("authPanel").scrollIntoView = () => {};
    }
    let preparationContinued = false;

    assert.doesNotThrow(() => {
      logic.showPreparedWorkflows(document, {authenticated: false});
      logic.scrollPreparedAuthentication(document, environment);
      preparationContinued = true;
    });

    assert.equal(preparationContinued, true);
    assert.equal(document.getElementById("topWorkflow").hidden, false);
    assert.equal(document.getElementById("selectionWorkflow").hidden, false);
    assert.equal(document.getElementById("authPanel").hidden, false);
    assert.equal(document.getElementById("progressPanel").hidden, false);
  }
});

test("success receipt consumes only the server receipt fields", () => {
  const dom = new JSDOM(`<!doctype html><body>
    <section id="successView" data-flow-view hidden>
      <h1 id="successTitle" tabindex="-1"></h1>
      <span id="successScope"></span><time id="successTime"></time><code id="successID"></code>
    </section>
  </body>`);
  const {document} = dom.window;
  const receiptID = "KW-FEDCBA9876543210FEDCBA9876543210";
  logic.showUploadSuccess(document, {
    receipt_id: receiptID,
    submitted_at: "2026-07-31T12:34:56Z",
  });
  assert.equal(document.getElementById("successID").textContent, receiptID);
  assert.equal(document.getElementById("successID").attributes.length, 1);
});

test("source status renders as non-interactive groups with collapsed not-found details", () => {
  const dom = new JSDOM(`<!doctype html><body>
    <section id="sourceStatusPanel">
      <p id="sourceStatusSummary"></p>
      <section id="sourceReadyGroup"><div id="sourceReadyList"></div></section>
      <section id="sourceActionGroup"><div id="sourceActionList"></div></section>
      <details id="sourceNotFoundGroup"><summary><span id="sourceNotFoundSummary"></span></summary><div id="sourceNotFoundList"></div></details>
    </section>
  </body>`);
  const {document} = dom.window;
  const secret = "/Users/private/session-secret";
  logic.renderSources(document, [
    {product: "codex", display_name: "Codex", state: "ready", selectable: true,
      verification: "machine", capabilities: ["messages", "tools"], error: secret},
    {product: "trae", display_name: "TRAE", state: "export_required",
      reason: "official_export_required", code: secret},
    {product: "missing", display_name: "未安装 Agent", state: "not_found", path: secret},
  ]);

  const panel = document.getElementById("sourceStatusPanel");
  assert.equal(panel.querySelectorAll("button, input, select, textarea, a[href]").length, 0);
  assert.equal(document.getElementById("sourceNotFoundGroup").open, false);
  assert.match(document.getElementById("sourceNotFoundList").textContent, /未安装 Agent/);
  assert.match(document.getElementById("sourceReadyList").textContent, /Codex.*已可评估/s);
  assert.match(document.getElementById("sourceActionList").textContent, /TRAE.*需要官方导出/s);
  assert.doesNotMatch(panel.textContent, /private|session-secret/);
});
