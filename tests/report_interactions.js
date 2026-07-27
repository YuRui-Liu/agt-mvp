const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");
const vm = require("node:vm");

const template = fs.readFileSync("avscore.html.tmpl", "utf8");

class Element {
  constructor(tagName = "div") {
    this.tagName = tagName.toUpperCase();
    this.attributes = new Map();
    this.children = [];
    this.dataset = {};
    this.listeners = {};
    this.textContent = "";
    this.className = "";
    this.innerHTML = "";
    this.classList = {
      values: new Set(),
      toggle: (name, force) => {
        if (force) this.classList.values.add(name);
        else this.classList.values.delete(name);
      },
      add: name => this.classList.values.add(name),
      remove: name => this.classList.values.delete(name),
      contains: name => this.classList.values.has(name),
    };
  }
  setAttribute(name, value) {
    this.attributes.set(name, String(value));
    if (name === "data-index") this.dataset.index = String(value);
  }
  getAttribute(name) {
    return this.attributes.get(name) ?? null;
  }
  appendChild(child) {
    this.children.push(child);
    return child;
  }
  append(...children) {
    this.children.push(...children);
  }
  addEventListener(type, handler) {
    (this.listeners[type] ||= []).push(handler);
  }
  dispatch(type, init = {}) {
    const event = {
      key: init.key,
      preventDefault() {},
    };
    for (const handler of this.listeners[type] || []) handler(event);
  }
  scrollIntoView() {}
}

function executeReport(report, options = {}) {
  const rendered = template
    .replace("{{REPORT_JSON}}", JSON.stringify(report))
    .replace("{{AITI_MOCK_JS}}", fs.readFileSync("aiti-mock.js", "utf8"))
    .replaceAll("{{APPLICATION_URL}}", "/application?token=real")
    .replaceAll("{{POSTER_URL}}", "/assets/poster.png?token=real")
    .replaceAll("{{QR_URL}}", "/assets/aiti-qr.svg?token=real")
    .replaceAll("{{RETURN_URL}}", "/?token=real")
    .replace("{{ARCHETYPE_PRIMARY}}", report.archetype.primary)
    .replace("{{ARCHETYPE_CONFIDENCE}}", String(report.archetype.confidence * 100))
    .replace("{{TREND_SHIFTS}}", report.trend.key_shifts.join(" · "));
  const scripts = [...rendered.matchAll(/<script([^>]*)>([\s\S]*?)<\/script>/g)]
    .filter(match => !match[1].includes("application/json"))
    .map(match => match[2]);
  const embeddedReport = rendered.match(
    /<script type="application\/json" id="report-data">([\s\S]*?)<\/script>/
  )[1];
  const all = [];
  const ids = new Map();
  const register = (element) => {
    all.push(element);
    return element;
  };
  const requiredIds = [
    "headerNote", "typeCode", "archetypePrimary", "archetypeConfidence",
    "profile-title", "heroLead", "portraitCaption", "closingMonogram",
    "footerProfile", "traitRow", "metaProject", "metaSessions", "metaEngine",
    "metaGenerated", "trendSummary", "trendShifts", "radarChart",
    "radarRings", "radarAxes", "radarDots", "radarLabels", "radarShape",
    "dimensionTabs", "metricGrid", "insightEnglish", "insightTitle",
    "insightScore", "insightSummary", "insightEvidence",
    "aitiInlinePanel", "aitiInlineForm", "aitiIdentityResult", "aitiPhone",
    "aitiCode", "sendAitiCodeButton", "verifyAitiButton", "aitiStatus",
  ];
  for (const id of requiredIds) ids.set(id, register(new Element()));
  ids.set("report-data", register(new Element("script")));
  ids.get("report-data").textContent = embeddedReport;
  const insightPanel = register(new Element("aside"));
  const document = {
    title: "",
    getElementById: id => ids.get(id),
    createElement: tag => register(new Element(tag)),
    createElementNS: (_namespace, tag) => register(new Element(tag)),
    querySelectorAll: selector => selector === "[data-index]"
      ? all.filter(element => element.dataset.index !== undefined)
      : [],
    querySelector: selector => {
      if (selector === ".insight-panel") return insightPanel;
      if (selector === "[data-toggle-aiti-inline]") {
        if (!ids.has("aitiToggle")) ids.set("aitiToggle", register(new Element("button")));
        return ids.get("aitiToggle");
      }
      return null;
    },
  };
  ids.get("aitiInlinePanel").hidden = true;
  ids.get("aitiIdentityResult").hidden = true;
  ids.get("aitiPhone").value = "";
  ids.get("aitiCode").value = "";
  ids.get("aitiPhone").focus = () => {};
  const storage = new Map(Object.entries(options.storage || {}));
  const localStorage = {
    getItem: key => {
      if (options.storageThrows) throw new Error("storage unavailable");
      return storage.get(key) ?? null;
    },
    setItem: (key, value) => {
      if (options.storageThrows) throw new Error("storage unavailable");
      storage.set(key, String(value));
    },
  };
  const crypto = {getRandomValues: values => {
    for (let index = 0; index < values.length; index += 1) values[index] = index + 20;
    return values;
  }};
  const context = {document, console, Math, localStorage, crypto, Date, Uint8Array};
  context.globalThis = context;
  vm.createContext(context);
  for (const source of scripts) vm.runInContext(source, context);
  return {ids, context, storage};
}

const dimensions = [
  ["steering", "引导", 12],
  ["execution", "执行", 24],
  ["engineering", "工程", 36],
  ["planning", "规划", 48],
  ["product", "产品", 60],
  ["autonomy", "自主", 72],
  ["adaptation", "适应", 84],
].map(([key, label, score]) => ({
  key, label, score, title: `${label}标题`,
  summary: `${label}摘要`, evidence: `${label}证据`,
}));
const report = {
  project: "真实项目",
  project_session_count: 9,
  generated_at: "2026-07-27T09:00:00Z",
  engine: "statistical",
  degraded: false,
  archetype: {
    primary: "系统设计者", code: "REAL", title: "真实画像",
    summary: "真实摘要", traits: ["可靠"], confidence: 0.73,
  },
  trend: {prediction: "继续变化", key_shifts: ["变化一", "变化二"]},
  dimensions,
};

test("rendered report executes full DOM and SVG wiring", () => {
  const {ids} = executeReport(report);
  assert.equal(ids.get("headerNote").textContent, "真实项目 · 9 个会话");
  assert.equal(ids.get("profile-title").textContent, "真实画像");
  assert.equal(ids.get("radarShape").getAttribute("points").split(" ").length, 7);
  assert.match(ids.get("radarShape").getAttribute("points"), /^260,239\.36 /);
  assert.equal(ids.get("radarDots").children.length, 7);

  ids.get("radarDots").children[0].dispatch("click");
  assert.equal(ids.get("insightTitle").textContent, "引导标题");
  assert.equal(ids.get("dimensionTabs").children[0].getAttribute("aria-pressed"), "true");

  ids.get("dimensionTabs").children[4].dispatch("click");
  assert.equal(ids.get("insightSummary").textContent, "产品摘要");
  assert.equal(ids.get("metricGrid").children[4].getAttribute("aria-pressed"), "true");

  ids.get("metricGrid").children[6].dispatch("click");
  assert.equal(ids.get("insightEvidence").textContent, "适应证据");

  ids.get("radarDots").children[1].dispatch("keydown", {key: "Enter"});
  assert.equal(ids.get("insightScore").textContent, 24);
  assert.equal(ids.get("radarDots").children[1].getAttribute("aria-pressed"), "true");
  assert.equal(ids.get("radarDots").children[0].getAttribute("aria-pressed"), "false");
});

test("AITI identity flow expands, validates, generates, restores, and links with token", () => {
  const {ids, storage} = executeReport(report);
  ids.get("aitiToggle").dispatch("click");
  assert.equal(ids.get("aitiInlinePanel").hidden, false);

  ids.get("aitiPhone").value = "13800138000";
  ids.get("sendAitiCodeButton").dispatch("click");
  assert.match(ids.get("aitiStatus").textContent, /本地验证码/);

  ids.get("aitiCode").value = "wrong";
  ids.get("verifyAitiButton").dispatch("click");
  assert.match(ids.get("aitiStatus").textContent, /验证码错误/);

  ids.get("aitiCode").value = "123456";
  ids.get("verifyAitiButton").dispatch("click");
  assert.equal(ids.get("aitiIdentityResult").hidden, false);
  assert.match(ids.get("aitiIdentityResult").innerHTML, /138\*\*\*\*8000/);
  assert.match(ids.get("aitiIdentityResult").innerHTML, /\/assets\/aiti-qr\.svg\?token=real/);
  assert.match(ids.get("aitiIdentityResult").innerHTML, /\/application\?token=real&amp;aitiId=|\/application\?token=real&aitiId=/);
  assert.ok(storage.get("aiti-demo-last-generated-id"));
});

test("AITI restores the latest unassociated ID", () => {
  const state = {
    profile: {typeCode: "REAL", title: "真实画像", score: 88},
    ids: [{value: "A7TI8P", aitiId: "A7TI8P", phone: "138****0000", associated: false}],
    applications: [],
  };
  const {ids} = executeReport(report, {storage: {
    "aiti-demo-state-v1": JSON.stringify(state),
    "aiti-demo-last-generated-id": "A7TI8P",
  }});
  assert.equal(ids.get("aitiInlinePanel").hidden, false);
  assert.match(ids.get("aitiIdentityResult").innerHTML, /A7TI8P/);
});

test("unavailable localStorage does not stop report rendering", () => {
  const {ids} = executeReport(report, {storageThrows: true});
  assert.equal(ids.get("profile-title").textContent, "真实画像");
  assert.equal(ids.get("radarDots").children.length, 7);
});
