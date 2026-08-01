# kuAI C 端提交闭环重设计实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 将 C 端终点改为“提交成功”，实现就近准备卡、独立安全上传页和参考图风格成功页，并彻底隐藏未知大小与旧分析入口。

**架构：** 保留现有 Go 上传 API，把浏览器状态机收敛为选择、准备、验证、授权、上传、成功六态。`index.html` 提供三个互斥视图，`app.js` 负责视图切换和将工作流卡插到所选 Scope 前方，`styles.css` 实现桌面/移动视觉；不增加虚假上传百分比，也不修改服务端异步分析。

**技术栈：** 原生 HTML/CSS/JavaScript、Node Test Runner、JSDOM、Go embed/static handler。

---

## 文件职责

- 修改 `internal/webapp/assets/index.html`：选择流、上传视图、成功视图及无障碍结构。
- 修改 `internal/webapp/assets/app.js`：安全元数据、就近插入、上传/成功状态机、错误恢复和按钮行为。
- 修改 `internal/webapp/assets/styles.css`：参考图风格、阶段进度、成功大卡及响应式。
- 修改 `tests/session_selection_interactions.js`：未知大小与就近插入契约。
- 修改 `tests/report_interactions.js`：上传终态、成功页和旧分析入口移除契约。
- 修改 `internal/webapp/assets/flow_dom.test.js`：真实 DOM 的互斥视图与按钮行为。
- 修改 `internal/webapp/assets_test.go`：嵌入页面的关键无障碍与终态文案。

### 任务 1：先建立失败的产品行为测试

**文件：**
- 修改：`tests/session_selection_interactions.js`
- 修改：`tests/report_interactions.js`
- 修改：`internal/webapp/assets/flow_dom.test.js`
- 修改：`internal/webapp/assets_test.go`

- [ ] **步骤 1：为未知大小和就近工作流编写失败测试**

在 `tests/session_selection_interactions.js` 增加：

```js
test("unknown scope size is omitted and preparation is inserted before selected scope", () => {
  assert.doesNotMatch(script, /大小未知/);
  assert.match(script, /Number\.isFinite\(scope\.bytes\) && scope\.bytes > 0/);
  assert.match(script, /selectedCard\.before\(byID\("selectionWorkflow"\)\)/);
  assert.match(page, /id="selectionWorkflow"/);
});
```

- [ ] **步骤 2：为上传页和提交成功终态编写失败测试**

用以下测试替换 `tests/report_interactions.js` 中旧画像与海报测试：

```js
test("upload uses a dedicated truthful staged view", () => {
  assert.match(page, /id="uploadView"[^>]*hidden/);
  assert.match(page, /正在安全.*上传数据/);
  assert.match(page, /加密所选会话数据/);
  assert.match(page, /上传至远端存储/);
  assert.match(page, /校验提交结果/);
  assert.match(page, /role="progressbar"/);
  assert.doesNotMatch(page, /\b38%\b/);
});

test("confirmed upload ends at submission success", () => {
  assert.match(page, /id="successView"[^>]*hidden/);
  assert.match(page, /提交成功/);
  assert.match(script, /function renderSubmissionSuccess\(task\)/);
  assert.doesNotMatch(script, /renderProfile|schedulePoll|pollTask|downloadPoster/);
  const success = page.split('id="successView"', 2)[1];
  assert.doesNotMatch(success, /分析|画像|评分|海报/);
});
```

- [ ] **步骤 3：为真实 DOM 状态切换编写失败测试**

在 `flow_dom.test.js` 增加最小 DOM，并断言：

```js
assert.equal(document.getElementById("flowView").hidden, true);
assert.equal(document.getElementById("uploadView").hidden, false);
assert.equal(document.getElementById("successView").hidden, true);
```

随后模拟成功状态，断言只有 `successView` 可见，且“返回校招职位”和“再次提交”按钮存在。

- [ ] **步骤 4：运行测试并确认红灯**

运行：

```bash
npm test
go test ./internal/webapp -count=1
```

预期：FAIL，失败原因分别是 `selectionWorkflow`、`uploadView`、`successView` 和 `renderSubmissionSuccess` 尚不存在，以及旧分析函数仍存在。

- [ ] **步骤 5：提交红灯测试**

```bash
git add tests/session_selection_interactions.js tests/report_interactions.js \
  internal/webapp/assets/flow_dom.test.js internal/webapp/assets_test.go
git commit -m "test: define submission-only web flow"
```

### 任务 2：实现安全元数据和就近准备工作流

**文件：**
- 修改：`internal/webapp/assets/index.html`
- 修改：`internal/webapp/assets/app.js`
- 修改：`internal/webapp/assets/styles.css`
- 测试：`tests/session_selection_interactions.js`

- [ ] **步骤 1：在 Scope 列表中增加工作流容器**

在 `scopeList` 内渲染 Scope 卡片；把现有 `progressPanel`、`authPanel` 和 `consentPanel` 包入：

```html
<div id="selectionWorkflow" class="selection-workflow" hidden>
  <!-- progressPanel, authPanel, consentPanel -->
</div>
```

该容器默认隐藏，仍位于 `scopePanel` 内。

- [ ] **步骤 2：只显示可靠大小**

把 `formatBytes` 改为：

```js
function formatBytes(value) {
  if (!Number.isFinite(value) || value <= 0) return "";
  const units = ["B", "KB", "MB", "GB"];
  let amount = value;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  return `${unit === 0 ? amount : amount.toFixed(1)} ${units[unit]}`;
}
```

构造详情时使用：

```js
const details = [
  `${scope.session_count} 个 session`,
  formatTime(scope.ended_at),
  Number.isFinite(scope.bytes) && scope.bytes > 0 ? formatBytes(scope.bytes) : ""
].filter(Boolean);
detail.textContent = details.join(" · ");
```

- [ ] **步骤 3：为 Scope 卡片提供稳定定位**

创建卡片时设置：

```js
card.dataset.scopeKey = scope.key;
```

新增：

```js
function placeSelectionWorkflow() {
  const selectedCard = [...byID("scopeList").querySelectorAll(".scope-card")]
    .find((card) => card.dataset.scopeKey === state.selectedScope?.key);
  if (!selectedCard) return;
  selectedCard.before(byID("selectionWorkflow"));
  byID("selectionWorkflow").hidden = false;
}
```

`renderPreparation` 在更新列表后调用 `placeSelectionWorkflow()`，并将按钮文案改为“确认并安全上传”。

- [ ] **步骤 4：实现最少样式**

增加 `.selection-workflow`、就近面板间距和选中卡视觉；移动端保持单列。不得使用固定高度或绝对定位覆盖 Scope 卡片。

- [ ] **步骤 5：运行目标测试并确认绿灯**

运行：

```bash
node --test tests/session_selection_interactions.js
go test ./internal/webapp -count=1
```

预期：PASS。

- [ ] **步骤 6：提交**

```bash
git add internal/webapp/assets/index.html internal/webapp/assets/app.js \
  internal/webapp/assets/styles.css tests/session_selection_interactions.js \
  internal/webapp/assets_test.go
git commit -m "feat: place preparation beside selected scope"
```

### 任务 3：实现独立上传页与提交成功页

**文件：**
- 修改：`internal/webapp/assets/index.html`
- 修改：`internal/webapp/assets/app.js`
- 修改：`internal/webapp/assets/styles.css`
- 测试：`tests/report_interactions.js`
- 测试：`internal/webapp/assets/flow_dom.test.js`

- [ ] **步骤 1：增加独立上传页**

在 `flowView` 后新增：

```html
<section id="uploadView" class="upload-view" role="status" aria-live="polite" hidden>
  <div class="upload-shell">
    <div class="upload-brand"><span class="brand-mark">K</span><strong>kuAI DATA SUBMISSION</strong></div>
    <div class="upload-symbol" aria-hidden="true"><span>K</span></div>
    <h1>正在安全<span>上传数据</span></h1>
    <p>正在提交你主动选择并已脱敏的会话，请保持页面打开。</p>
    <div id="uploadProgress" class="upload-progress" role="progressbar"
      aria-valuemin="0" aria-valuemax="3" aria-valuenow="1"
      aria-valuetext="正在加密所选会话数据"><i></i></div>
    <ol class="upload-steps">
      <li id="uploadEncrypt" class="active" aria-current="step">加密所选会话数据</li>
      <li id="uploadTransfer">上传至远端存储</li>
      <li id="uploadVerify">校验提交结果</li>
    </ol>
    <p class="privacy-note">仅上传你主动勾选并已脱敏的数据，用于本次提交。</p>
  </div>
</section>
```

- [ ] **步骤 2：增加提交成功页并删除旧画像 DOM**

用 `successView` 替换 `profileView`：

```html
<section id="successView" class="success-view" hidden>
  <div class="success-shell">
    <header>kuAI DATA CONTRIBUTION <span>✓ 提交成功</span></header>
    <div class="success-copy">
      <p>THANK YOU FOR YOUR CONTRIBUTION</p>
      <h1 id="successTitle" tabindex="-1">谢谢你，<span>提交成功！</span></h1>
      <p>你选择的会话数据已经安全送达。感谢你帮助我们更真实地理解人与 AI 的协作方式。</p>
      <dl>
        <div><dt>提交内容</dt><dd id="successScope">已选择的 Agent 会话</dd></div>
        <div><dt>状态</dt><dd>已安全送达</dd></div>
        <div><dt>提交时间</dt><dd id="successTime"></dd></div>
        <div><dt>提交编号</dt><dd id="successID"></dd></div>
      </dl>
      <div class="actions">
        <button id="returnApplication" type="button">返回校招职位</button>
        <button id="submitAgain" class="secondary" type="button">再次提交</button>
      </div>
    </div>
    <div class="success-art" aria-hidden="true"><span>K</span></div>
  </div>
</section>
```

- [ ] **步骤 3：把上传请求切到独立视图**

新增：

```js
function showUploadView() {
  byID("flowView").hidden = true;
  byID("uploadView").hidden = false;
  byID("successView").hidden = true;
}
```

`startAnalysis` 改名为 `submitData`，在 `/api/consent` 成功、请求 `/api/tasks` 前调用 `showUploadView()`；错误时恢复 `flowView` 并保留幂等草稿。

- [ ] **步骤 4：提交确认后直接进入成功终态**

删除前端 `acceptTask`、`schedulePoll`、`pollTask`、`resumeLatest`、`renderProfile` 和 `downloadPoster` 的可见入口。新增：

```js
function renderSubmissionSuccess(task) {
  state.task = task;
  byID("uploadView").hidden = true;
  byID("flowView").hidden = true;
  byID("successView").hidden = false;
  byID("successScope").textContent = state.selectedScope?.label || "已选择的 Agent 会话";
  byID("successTime").textContent = new Date().toLocaleString("zh-CN");
  byID("successID").textContent = `KW-${String(task.id || "").replace(/[^a-z0-9]/gi, "").slice(-8).toUpperCase()}`;
  byID("successTitle").focus();
}
```

`/api/tasks` 成功后直接调用 `renderSubmissionSuccess(body)`，不保存用于轮询的任务 ID。

- [ ] **步骤 5：绑定成功页操作**

```js
byID("returnApplication").addEventListener("click", () => { location.href = "/application"; });
byID("submitAgain").addEventListener("click", startNew);
```

`startNew` 清理草稿后重新载入初始流程。

- [ ] **步骤 6：实现参考图风格与响应式**

增加：

- `.upload-view` 和 `.success-view` 全屏居中；
- `.upload-shell` 最大宽度约 760px、圆角 32px；
- `.success-shell` 最大宽度约 1180px、左右 60/40 布局、圆角 32px；
- 黑/橙两段标题、绿色成功 pill、2×2 信息卡；
- 原创 CSS 橙色圆弧与 K 图形；
- `≤700px` 单列，`≤420px` 按钮纵向满宽；
- `prefers-reduced-motion` 停止上传动画。

- [ ] **步骤 7：运行目标测试并确认绿灯**

运行：

```bash
npm test
go test ./internal/webapp -count=1
```

预期：全部 PASS，且旧分析/画像/海报契约不再存在。

- [ ] **步骤 8：提交**

```bash
git add internal/webapp/assets/index.html internal/webapp/assets/app.js \
  internal/webapp/assets/styles.css internal/webapp/assets/flow_dom.test.js \
  tests/report_interactions.js internal/webapp/assets_test.go
git commit -m "feat: end local flow at submission success"
```

### 任务 4：视觉核验与完整回归

**文件：**
- 可能修改：`internal/webapp/assets/styles.css`
- 可能修改：`internal/webapp/assets/app.js`
- 测试：全部前端与 Go 测试

- [ ] **步骤 1：执行自动化验证**

运行：

```bash
npm ci --ignore-scripts --no-audit --no-fund
npm test
go test -race ./... -count=1
go vet ./...
python3 -m unittest tests.test_docs tests.test_single_binary_contract -v
bash tests/test_kuai_install_sh.sh
bash tests/test_build_kuai_release_sh.sh
git diff --check
```

预期：所有命令退出码为 0。

- [ ] **步骤 2：运行本地 Mock 服务并覆盖三种视图**

使用临时数据目录启动：

```bash
KUAI_DATA_DIR=/private/tmp/kuai-ui-redesign go run ./cmd/kuai start --no-browser
```

在浏览器中验证：

- 长 Scope 列表中工作流卡位于所选卡上方；
- 无可靠大小时不显示大小；
- 上传页没有数值百分比；
- 成功页不出现分析、画像、评分、海报；
- 返回校招职位与再次提交可用。

- [ ] **步骤 3：桌面与移动端截图核验**

桌面使用约 `1440×1000`，移动端使用约 `390×844`。检查：

- 无横向滚动；
- 成功页信息层级与参考图一致；
- 上传页三阶段清晰；
- 所有按钮最小高度 44px；
- 焦点、ARIA 和错误提示可见。

- [ ] **步骤 4：修复视觉问题并复跑测试**

只调整 `styles.css` 或必要的无障碍属性；每次调整后运行：

```bash
npm test
go test ./internal/webapp -count=1
git diff --check
```

- [ ] **步骤 5：最终提交**

```bash
git add internal/webapp/assets
git commit -m "fix: polish submission flow visuals"
```
