const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");

const script = fs.readFileSync("internal/webapp/assets/app.js", "utf8");
const page = fs.readFileSync("internal/webapp/assets/index.html", "utf8");
const styles = fs.readFileSync("internal/webapp/assets/styles.css", "utf8");
const flowLogic = require("../internal/webapp/assets/flow_logic.js");

function cssDeclarations(selector, stylesheet = styles) {
  const {JSDOM} = require("jsdom");
  const document = new JSDOM(`<style>${stylesheet}</style>`).window.document;
  const rules = Array.from(document.styleSheets[0].cssRules);
  const normalize = value => value.split(",").map(part => part.trim()).sort().join(",");
  const matchingRules = rules.filter(candidate =>
    candidate.selectorText && normalize(candidate.selectorText) === normalize(selector));
  assert.ok(matchingRules.length, `missing CSS rule for ${selector}`);

  const declarations = document.createElement("div").style;
  for (const rule of matchingRules) {
    for (const property of rule.style) {
      const priority = rule.style.getPropertyPriority(property);
      if (declarations.getPropertyPriority(property) === "important" && priority !== "important") {
        continue;
      }
      declarations.setProperty(property, rule.style.getPropertyValue(property), priority);
    }
  }
  return declarations;
}

test("CSS contract helper observes declarations from the last matching rule", () => {
  const declarations = cssDeclarations(
    ".scope-card.selected",
    `${styles}\n.scope-card.selected{background:#000;--cascade-probe:final}`,
  );

  assert.equal(declarations.getPropertyValue("--cascade-probe"), "final");
  assert.ok(declarations.getPropertyValue("background"));
  assert.notEqual(declarations.getPropertyValue("background"), "var(--soft)");
});

test("selection starts empty and only an explicit click enables preparation", () => {
  const button = {handler: null, addEventListener(_type, handler) { this.handler = handler; }, click() { this.handler(); }};
  let selected = null;
  let enabled = false;
  const scope = {key: "scope", selectable: true};
  flowLogic.bindScopeSelection(button, scope, () => selected, value => { selected = value; }, () => { enabled = true; });
  assert.equal(selected, null);
  assert.equal(enabled, false);
  button.click();
  assert.equal(selected, scope);
  assert.equal(enabled, true);
  assert.match(page, /id="prepareScope"[^>]*disabled/);
});

test("scope discovery groups safe metadata and creates controls only for selectable entries", () => {
  assert.match(script, /api\("\/api\/scopes"\)/);
  assert.match(script, /scope\.agents/);
  assert.match(script, /scope\.session_count/);
  assert.match(script, /scope\.ended_at/);
  assert.match(script, /scope\.bytes/);
  assert.match(script, /const selectable = scope && scope\.selectable === true/);
  assert.match(script, /const button = selectable \? document\.createElement\("button"\) : document\.createElement\("div"\)/);
  assert.match(script, /if \(selectable\) \{\s*flowLogic\.bindScopeSelection/s);
  assert.doesNotMatch(script, /scope\.path|session\.id/);
});

test("unknown byte counts are omitted instead of rendered as placeholder copy", () => {
  assert.doesNotMatch(script, /大小未知/);
  for (const bytes of [undefined, NaN, 0, -1]) {
    assert.equal(flowLogic.formatScopeDetails({
      session_count: 3,
      ended_at: "2026-07-31T10:00:00Z",
      bytes,
    }).includes("B"), false);
  }
  assert.match(flowLogic.formatScopeDetails({
    session_count: 3,
    ended_at: "2026-07-31T10:00:00Z",
    bytes: 1536,
  }), /1\.5 KB/);
});

test("authentication workflow stays above the scope title while preparation stays by the selected card", () => {
  const {JSDOM} = require("jsdom");
  const dom = new JSDOM(page);
  const {document} = dom.window;
  const topWorkflow = document.getElementById("topWorkflow");
  const sourceStatusPanel = document.getElementById("sourceStatusPanel");
  const scopeTitle = document.getElementById("scopeTitle");
  const scopeList = document.getElementById("scopeList");
  const selectionWorkflow = document.getElementById("selectionWorkflow");

  assert.equal(topWorkflow.nextElementSibling, sourceStatusPanel);
  assert.equal(sourceStatusPanel.nextElementSibling, scopeTitle);
  assert.ok(topWorkflow.compareDocumentPosition(scopeList) & dom.window.Node.DOCUMENT_POSITION_FOLLOWING);
  assert.ok(topWorkflow.contains(document.getElementById("authPanel")));
  assert.ok(topWorkflow.contains(document.getElementById("consentPanel")));
  assert.equal(topWorkflow.contains(document.getElementById("progressPanel")), false);
  assert.ok(selectionWorkflow.contains(document.getElementById("progressPanel")));
  assert.equal(selectionWorkflow.contains(document.getElementById("authPanel")), false);
  assert.equal(selectionWorkflow.contains(document.getElementById("consentPanel")), false);
});

test("source overview is compact, responsive, and independent from scope controls", () => {
  const {JSDOM} = require("jsdom");
  const document = new JSDOM(page).window.document;
  const panel = document.getElementById("sourceStatusPanel");
  assert.ok(panel);
  assert.equal(panel.closest("#scopeList"), null);
  assert.equal(panel.querySelectorAll("button, input, [data-scope-key]").length, 0);
  assert.equal(cssDeclarations(".source-list").getPropertyValue("grid-template-columns"), "repeat(auto-fit,minmax(220px,1fr))");
  assert.equal(cssDeclarations(".source-row strong").getPropertyValue("overflow-wrap"), "anywhere");
  assert.equal(cssDeclarations(".source-not-found summary").getPropertyValue("min-height"), "44px");
  assert.equal(cssDeclarations(".source-not-found summary:focus-visible").getPropertyValue("outline-color"), "rgb(154, 58, 0)");
  assert.equal(cssDeclarations(".source-not-found summary:before").getPropertyValue("content"), '"+"');
  assert.equal(cssDeclarations(".source-not-found[open] summary:before").getPropertyValue("content"), '"−"');
  assert.match(styles, /@media\(max-width:700px\).*\.source-list\{grid-template-columns:1fr\}/s);
});

test("scope UI treats only literal boolean true as supported", () => {
  assert.match(script, /scope\.selectable === true/);
  assert.doesNotMatch(script, /scope\.selectable \?/);
  assert.match(script, /state\.selectedScope\.selectable !== true/);
});

test("API sources are bounded and sanitized before entering application state", () => {
  assert.match(script, /state\.sources = flowLogic\.safeSourceInputs\(body && body\.sources\)/);
});

test("long scope lists keep authentication at the top and preparation immediately after the selected card", () => {
  const {JSDOM} = require("jsdom");
  const dom = new JSDOM(`<!doctype html><body>
    <section id="scopePanel">
      <div id="topWorkflow" hidden>
        <section id="authPanel" hidden></section>
        <section id="consentPanel" hidden></section>
      </div>
      <div id="scopeTitle"></div>
      <div id="scopeList">
        ${Array.from({length: 24}, (_, index) =>
          `<article data-scope-key="scope-${index}"></article>`).join("")}
      </div>
      <div id="selectionWorkflow" hidden>
        <section id="progressPanel" hidden></section>
      </div>
    </section>
  </body>`);
  const {document} = dom.window;
  const selectedCard = document.querySelector('[data-scope-key="scope-23"]');
  const topWorkflow = document.getElementById("topWorkflow");
  const selectionWorkflow = document.getElementById("selectionWorkflow");

  flowLogic.showPreparedWorkflows(document, {authenticated: false});
  flowLogic.placeSelectionWorkflow(document, "scope-23");

  assert.equal(topWorkflow.hidden, false);
  assert.equal(document.getElementById("authPanel").hidden, false);
  assert.ok(topWorkflow.compareDocumentPosition(document.getElementById("scopeList")) &
    dom.window.Node.DOCUMENT_POSITION_FOLLOWING);
  assert.equal(selectionWorkflow.parentElement, selectedCard.parentElement);
  assert.equal(selectedCard.nextElementSibling, selectionWorkflow);
  assert.equal(document.getElementById("progressPanel").hidden, false);
});

test("scope card styles reserve the strongest treatment and tight workflow grouping for selection", () => {
  const root = cssDeclarations(":root");
  const card = cssDeclarations(".scope-card");
  const weakSelector = ".scope-card:not(.unsupported):not(.selected):hover, " +
    ".scope-card:not(.unsupported):not(.selected):focus-within";
  const hover = cssDeclarations(weakSelector);
  const selected = cssDeclarations(".scope-card.selected");
  const selectedRadio = cssDeclarations(".selected .radio-mark");
  const groupedWorkflow = cssDeclarations(".scope-card.selected + .selection-workflow");

  assert.equal(root.getPropertyValue("--line-strong"), "#d9c2ad");
  assert.notEqual(card.getPropertyValue("background"), "var(--soft)");
  assert.equal(hover.getPropertyValue("border-color"), "var(--line-strong)");
  assert.equal(hover.getPropertyValue("background"), "");
  assert.ok(selected.getPropertyValue("border-color"));
  assert.notEqual(selected.getPropertyValue("border-color"), hover.getPropertyValue("border-color"));
  assert.ok(selected.getPropertyValue("box-shadow"));
  assert.notEqual(hover.getPropertyValue("box-shadow"), selected.getPropertyValue("box-shadow"));
  assert.equal(selected.getPropertyValue("background"), "var(--soft)");
  assert.equal(selectedRadio.getPropertyValue("border-color"), "var(--orange)");
  assert.equal(selectedRadio.getPropertyValue("background"), "var(--orange)");
  assert.equal(groupedWorkflow.getPropertyValue("margin-top"), "-4px");
  assert.equal(groupedWorkflow.getPropertyValue("margin-bottom"), "0px");
  assert.equal(groupedWorkflow.getPropertyValue("position"), "");
});

test("OTP and explicit consent independently gate upload", () => {
  assert.match(script, /\/api\/auth\/request-code/);
  assert.match(script, /\/api\/auth\/verify/);
  assert.match(script, /\/api\/consent/);
  assert.match(script, /state\.consent = event\.target\.checked/);
  assert.match(script, /if \(!state\.consent \|\| !state\.preparation/);
});

test("refresh recovery keeps auth and the idempotent upload draft, not a task", () => {
  assert.match(script, /sessionStorage\.setItem\(AUTH_KEY, JSON\.stringify\(body\)\)/);
  assert.doesNotMatch(script, /\bTASK_KEY\b|kuaiTaskID/);
  assert.match(script, /state\.uploadDraft\.scope_key = state\.selectedScope\.key/);
  assert.match(script, /restorePreparationState\(state, state\.scopes, sessionStorage, UPLOAD_DRAFT_KEY\)/);
  assert.match(script, /hidePreparedWorkflows\(document\)/);
  assert.doesNotMatch(script, /sessionStorage\.setItem\([^,]+,\s*phone/);
  assert.doesNotMatch(page, /launch-token|private-session|\/Users\//);
});

test("authentication is scrolled into view with reduced-motion respected", () => {
  assert.match(script, /flowLogic\.scrollPreparedAuthentication\(document,\s*globalThis\)/);
  assert.doesNotMatch(script, /function scrollAuthenticationIntoView/);
});
