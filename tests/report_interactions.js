const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");

const script = fs.readFileSync("internal/webapp/assets/app.js", "utf8");
const page = fs.readFileSync("internal/webapp/assets/index.html", "utf8");
const styles = fs.readFileSync("internal/webapp/assets/styles.css", "utf8");

test("upload success enters only the success view", () => {
  assert.match(script, /flowLogic\.showUploadSuccess\(document,\s*body,\s*state\.selectedScope\s*&&\s*state\.selectedScope\.label\)/,
    "the successful /api/tasks response must enter the success terminal state");
  assert.doesNotMatch(script, /\brenderProfile\b|\bschedulePoll\b|\bpollTask\b|\bdownloadPoster\b/);
});

test("success view is submission-only and offers the two next actions", () => {
  const match = page.match(/<section\b[^>]*id="successView"[\s\S]*?<\/section>/);
  assert.ok(match, "index must contain a successView section");
  const successView = match[0];
  assert.doesNotMatch(successView, /分析|画像|评分|海报/);
  assert.match(successView, /返回校招职位/);
  assert.match(successView, /再次提交/);
});

test("success actions are real buttons wired through flow logic", () => {
  assert.match(page, /<button\b[^>]*id="returnApplication"[^>]*>/,
    "success view must expose a returnApplication button");
  assert.match(page, /<button\b[^>]*id="submitAgain"[^>]*>/,
    "success view must expose a submitAgain button");
  assert.match(script, /flowLogic\.bindSuccessActions\(/,
    "DOMContentLoaded must bind the success actions through flow logic");
  assert.match(script, /byID\(["']returnApplication["']\)/,
    "the production binding must use returnApplication");
  assert.match(script, /byID\(["']submitAgain["']\)/,
    "the production binding must use submitAgain");
  assert.match(script, /["']\/application["']/,
    "the return action must target /application");
  assert.match(script, /\b(?:startNew|resetSubmission)\b/,
    "the submit-again action must use the production reset path");
});

test("successful task submission invokes the visible success terminal state", () => {
  assert.match(script, /api\(["'`]\/api\/tasks/,
    "submission must still create the task through /api/tasks");
  assert.match(script, /flowLogic\.showUploadSuccess\(document,\s*body,\s*state\.selectedScope\s*&&\s*state\.selectedScope\.label\)/,
    "the receipt response and local scope label must be passed to showUploadSuccess");
  assert.doesNotMatch(script, /\bacceptTask\(|\bschedulePoll\(|\bpollTask\(/,
    "submission-only foreground code must not enter report polling");
});

test("success view does not retain the former report controls", () => {
  assert.doesNotMatch(page, /id="profileView"|id="downloadPoster"|id="traitRow"/);
  assert.doesNotMatch(page, /下载画像海报|进入投递页面|分析另一个范围/);
});

test("upload progress exposes three semantic stages without fake percentage", () => {
  const match = page.match(/<section\b[^>]*id="uploadView"[\s\S]*?<\/section>/);
  assert.ok(match);
  const uploadView = match[0];
  assert.match(uploadView, /id="uploadProgress"[^>]*role="progressbar"[^>]*aria-valuemin="0"[^>]*aria-valuemax="3"/);
  assert.match(script, /setUploadStage\(document,\s*2\)[\s\S]*api\(["'`]\/api\/tasks/);
  assert.match(script, /api\(["'`]\/api\/tasks[\s\S]*setUploadStage\(document,\s*3\)[\s\S]*showUploadSuccess/);
  assert.doesNotMatch(uploadView, /aria-valuemax="100"|38%|62%/);
  for (const copy of ["加密所选会话数据", "上传至远端存储", "校验提交结果",
    "仅上传你主动勾选并已脱敏的数据，用于本次提交"]) {
    assert.match(uploadView, new RegExp(copy));
  }
  assert.match(uploadView, /正在安全<span>上传数据<\/span>/);
});

test("success view uses the approved split receipt composition", () => {
  assert.match(page, /class="success-layout"/);
  assert.match(page, /class="success-content"/);
  assert.match(page, /class="success-art"[^>]*aria-hidden="true"/);
  assert.match(page, /class="success-status-pill"/);
  assert.match(page, /谢谢你，[\s\S]*<span>提交成功！<\/span>/);
  assert.match(page, /class="receipt-grid"/);
});

test("mobile success keeps content before decoration", () => {
  assert.ok(page.indexOf('class="success-content"') < page.indexOf('class="success-art"'),
    "success content must precede its decorative art in DOM order");
  assert.doesNotMatch(styles, /\.success-art\{[^}]*order\s*:\s*-1/,
    "mobile CSS must not move decorative art ahead of the content");
});

test("customer flow promises secure submission rather than a local report", () => {
  assert.doesNotMatch(page, /上传并开始分析|生成分析海报|下载画像海报/);
  assert.match(page, />确认并安全上传</);
  assert.match(page, /用于本次数据提交、扫码投递后的候选人评估，以及模型和算法改进/);
  assert.match(page, /安全提交/);
});
