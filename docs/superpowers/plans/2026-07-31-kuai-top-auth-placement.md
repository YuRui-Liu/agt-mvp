# kuAI 顶部认证区实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 本地准备完成后，将手机号认证和用途授权固定显示在整个 Scope 列表上方，同时保留本地准备结果在所选 Scope 卡片正上方。

**架构：** 将现有 `selectionWorkflow` 拆为两个普通文档流容器：`topWorkflow` 承载认证与授权，固定放在 Scope 标题之前；`selectionWorkflow` 只承载准备进度并继续跟随所选 Scope。现有 API、草稿恢复和上传状态机保持不变。

**技术栈：** 原生 HTML/CSS/JavaScript、Node Test Runner、JSDOM、Go embedded assets。

---

## 文件职责

- 修改 `internal/webapp/assets/index.html`：拆分顶部操作区和就近准备区。
- 修改 `internal/webapp/assets/app.js`：分别控制两个区域的可见性、恢复和滚动。
- 修改 `internal/webapp/assets/flow_logic.js`：提供可测试的顶部工作流显示逻辑。
- 修改 `internal/webapp/assets/styles.css`：顶部区与列表间距、移动端布局。
- 修改 `tests/session_selection_interactions.js`：DOM 层级与长列表行为契约。
- 修改 `internal/webapp/assets/flow_dom.test.js`：准备、认证、授权的真实 DOM 状态转换。
- 修改 `internal/webapp/assets_test.go`：嵌入资产结构契约。

### 任务 1：建立失败的布局和状态测试

**文件：**
- 修改：`tests/session_selection_interactions.js`
- 修改：`internal/webapp/assets/flow_dom.test.js`
- 修改：`internal/webapp/assets_test.go`

- [ ] **步骤 1：编写容器边界测试**

在 `tests/session_selection_interactions.js` 解析真实页面并断言：

```js
const dom = new JSDOM(page);
const {document} = dom.window;
const topWorkflow = document.getElementById("topWorkflow");
const scopeTitle = document.getElementById("scopeTitle");
const scopeList = document.getElementById("scopeList");
const selectionWorkflow = document.getElementById("selectionWorkflow");

assert.equal(topWorkflow.nextElementSibling, scopeTitle);
assert.ok(topWorkflow.compareDocumentPosition(scopeList) & dom.window.Node.DOCUMENT_POSITION_FOLLOWING);
assert.ok(topWorkflow.contains(document.getElementById("authPanel")));
assert.ok(topWorkflow.contains(document.getElementById("consentPanel")));
assert.ok(selectionWorkflow.contains(document.getElementById("progressPanel")));
assert.equal(selectionWorkflow.contains(document.getElementById("authPanel")), false);
```

- [ ] **步骤 2：编写状态转换测试**

为 `flow_logic.js` 约定：

```js
logic.showPreparedWorkflows(document, {authenticated: false});
assert.equal(document.getElementById("topWorkflow").hidden, false);
assert.equal(document.getElementById("authPanel").hidden, false);
assert.equal(document.getElementById("consentPanel").hidden, true);
assert.equal(document.getElementById("selectionWorkflow").hidden, false);

logic.showPreparedWorkflows(document, {authenticated: true});
assert.equal(document.getElementById("authPanel").hidden, true);
assert.equal(document.getElementById("consentPanel").hidden, false);
```

同时覆盖 `logic.hidePreparedWorkflows(document)` 会隐藏两个容器和三个面板。

- [ ] **步骤 3：运行红灯**

运行：

```bash
node --test tests/session_selection_interactions.js internal/webapp/assets/flow_dom.test.js
go test ./internal/webapp -run TestSubmissionFlowAssets -count=1
```

预期：FAIL，原因是 `topWorkflow` 和状态 helper 尚不存在。

- [ ] **步骤 4：提交红灯测试**

```bash
git add tests/session_selection_interactions.js \
  internal/webapp/assets/flow_dom.test.js internal/webapp/assets_test.go
git commit -m "test: define top authentication workflow"
```

### 任务 2：拆分顶部认证与就近准备区域

**文件：**
- 修改：`internal/webapp/assets/index.html`
- 修改：`internal/webapp/assets/app.js`
- 修改：`internal/webapp/assets/flow_logic.js`
- 修改：`internal/webapp/assets/styles.css`
- 测试：任务 1 的测试文件

- [ ] **步骤 1：调整 DOM**

在 `scopePanel` 内改为：

```html
<div id="topWorkflow" class="top-workflow" hidden>
  <section id="authPanel">...</section>
  <section id="consentPanel">...</section>
</div>
<div class="section-label" id="scopeTitle">检测到的 Assessment Scope</div>
<div id="scopeList" class="scope-list"></div>
<div id="selectionWorkflow" class="selection-workflow" hidden>
  <section id="progressPanel">...</section>
</div>
```

`selectionWorkflow` 仍由定位 helper 移动到所选 Scope 卡片前。

- [ ] **步骤 2：实现可测试状态 helper**

在 `flow_logic.js` 导出：

```js
function showPreparedWorkflows(document, {authenticated}) {
  const top = document.getElementById("topWorkflow");
  const local = document.getElementById("selectionWorkflow");
  top.hidden = false;
  local.hidden = false;
  document.getElementById("authPanel").hidden = authenticated;
  document.getElementById("consentPanel").hidden = !authenticated;
  document.getElementById("progressPanel").hidden = false;
}

function hidePreparedWorkflows(document) {
  for (const id of ["topWorkflow", "selectionWorkflow", "authPanel", "consentPanel", "progressPanel"]) {
    document.getElementById(id).hidden = true;
  }
}
```

- [ ] **步骤 3：接入现有状态机**

- `renderPreparation` 调用 `showPreparedWorkflows`，随后仅定位 `selectionWorkflow`。
- `verifyOTP` 认证成功后，在 `topWorkflow` 原位置切换到 `consentPanel`。
- 无效恢复、重试清理和重新开始使用 `hidePreparedWorkflows`。
- 上传失败返回时恢复顶部授权区与就近准备区。
- 顶部认证显示后调用 `authPanel.scrollIntoView`；减少动态效果时使用即时行为。

- [ ] **步骤 4：添加布局样式**

```css
.top-workflow{display:grid;gap:16px;margin:18px 0 26px}
.top-workflow .panel{margin-top:0}
.selection-workflow{display:grid;gap:16px}
```

不得使用 `sticky`、`fixed` 或覆盖 Scope 列表的绝对定位。

- [ ] **步骤 5：运行绿灯**

运行：

```bash
npm test
go test ./internal/webapp -count=1
git diff --check
```

预期：全部 PASS。

- [ ] **步骤 6：提交**

```bash
git add internal/webapp/assets tests/session_selection_interactions.js \
  internal/webapp/assets_test.go
git commit -m "fix: keep authentication above scope list"
```

### 任务 3：长列表浏览器验证与完整回归

**文件：**
- 可能修改：`internal/webapp/assets/styles.css`
- 可能修改：`internal/webapp/assets/app.js`

- [ ] **步骤 1：启动本地 Mock**

```bash
KUAI_DATA_DIR=/private/tmp/kuai-top-auth \
  go run ./cmd/kuai start --no-browser
```

- [ ] **步骤 2：验证长列表**

在真实浏览器中选择列表深处的单 session Scope，完成本地准备，并检查：

- `03 · VERIFY` 位于 Scope 标题和整个列表上方；
- `02 · LOCAL EXPORT` 紧邻所选 Scope 卡片上方；
- 页面自动回到可见的认证卡；
- 不出现横向滚动。

- [ ] **步骤 3：验证认证切换**

使用 Mock 验证码完成认证，确认：

- `03 · VERIFY` 隐藏；
- `04 · CONSENT` 在同一个顶部位置显示；
- `02 · LOCAL EXPORT` 不移动。

- [ ] **步骤 4：执行完整验证**

```bash
npm test
go test -race ./... -count=1
go vet ./...
python3 -m unittest tests.test_docs tests.test_single_binary_contract -v
bash tests/test_kuai_install_sh.sh
bash tests/test_build_kuai_release_sh.sh
git diff --check
```

- [ ] **步骤 5：提交视觉修正**

若浏览器核验需要样式调整：

```bash
git add internal/webapp/assets
git commit -m "fix: polish top authentication layout"
```
