# kuAI 本地准备卡位置实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让 `02 · LOCAL EXPORT` 始终紧跟在所选 Assessment Scope 卡片之后，并明确区分 selected 与 hover/focus 状态。

**架构：** 保持顶部 `topWorkflow` 与认证、授权逻辑不变，只调整 `placeSelectionWorkflow` 的 DOM 插入方向。布局关系由真实 DOM 测试锁定，视觉分组和交互层级由现有样式表中的独立状态规则表达。

**技术栈：** 原生 JavaScript、CSS、Node.js test runner、JSDOM、Go 嵌入式 Web 应用测试。

---

## 文件结构

- 修改：`tests/session_selection_interactions.js` — 锁定长列表中“所选 Scope → 02”顺序和选中状态。
- 修改：`internal/webapp/assets/flow_logic.js` — 将本地准备工作流插入所选卡片之后。
- 修改：`internal/webapp/assets/styles.css` — 弱化 hover，强化 selected，并缩小所选卡与准备卡的组内间距。

### 任务 1：锁定所选卡片与本地准备卡的 DOM 顺序

**文件：**
- 修改：`tests/session_selection_interactions.js`
- 修改：`internal/webapp/assets/flow_logic.js`

- [ ] **步骤 1：把长列表测试改成要求准备卡紧随所选卡**

将测试名称和核心断言改为：

```js
test("long scope lists keep authentication at the top and preparation immediately after the selected card", () => {
  // 保留现有 24 个 Scope 的 JSDOM fixture 和顶部认证断言。
  flowLogic.showPreparedWorkflows(document, {authenticated: false});
  flowLogic.placeSelectionWorkflow(document, "scope-23");

  assert.equal(selectionWorkflow.parentElement, selectedCard.parentElement);
  assert.equal(selectedCard.nextElementSibling, selectionWorkflow);
  assert.equal(document.getElementById("progressPanel").hidden, false);
});
```

- [ ] **步骤 2：运行定向测试并确认失败**

运行：

```bash
node --test tests/session_selection_interactions.js
```

预期：FAIL，`selectedCard.nextElementSibling` 不是 `selectionWorkflow`，证明旧实现仍将准备卡放在所选卡之前。

- [ ] **步骤 3：做最小 DOM 修复**

在 `internal/webapp/assets/flow_logic.js` 中修改：

```js
selectedCard.after(workflow);
workflow.hidden = false;
return workflow;
```

保留“找不到 Scope 时隐藏工作流并返回 `null`”的现有分支。

- [ ] **步骤 4：运行定向测试并确认通过**

运行：

```bash
node --test tests/session_selection_interactions.js
```

预期：全部 PASS。

- [ ] **步骤 5：提交 DOM 顺序修复**

```bash
git add tests/session_selection_interactions.js internal/webapp/assets/flow_logic.js
git commit -m "fix: place local export after selected scope"
```

### 任务 2：区分选中、悬停状态并形成视觉分组

**文件：**
- 修改：`tests/session_selection_interactions.js`
- 修改：`internal/webapp/assets/styles.css`

- [ ] **步骤 1：增加样式契约测试**

在 `tests/session_selection_interactions.js` 顶部读取样式，并增加：

```js
const styles = fs.readFileSync("internal/webapp/assets/styles.css", "utf8");

test("selected scope is visually stronger than hover and grouped with local export", () => {
  assert.match(styles, /\.scope-card:not\(\.unsupported\):hover\{[^}]*border-color:var\(--line-strong\)/);
  assert.match(styles, /\.scope-card\.selected\{[^}]*border-color:#eda66e[^}]*background:var\(--soft\)/);
  assert.match(styles, /\.scope-card\.selected\+\.selection-workflow\{[^}]*margin-top:-4px/);
});
```

- [ ] **步骤 2：运行定向测试并确认失败**

运行：

```bash
node --test tests/session_selection_interactions.js
```

预期：FAIL，因为样式尚未包含独立 hover、selected 和相邻分组规则。

- [ ] **步骤 3：实现状态层级和相邻分组**

在 `:root` 增加较弱描边变量，并把合并规则拆开：

```css
:root{--line-strong:#d9c2ad}
.scope-card:not(.unsupported):hover{border-color:var(--line-strong);box-shadow:0 6px 18px rgba(91,52,22,.05)}
.scope-card.selected{border-color:#eda66e;background:var(--soft);box-shadow:0 10px 26px rgba(91,52,22,.08)}
.scope-card.selected+.selection-workflow{margin-top:-4px;margin-bottom:12px}
```

保留 `.selected .radio-mark` 规则，确保只有真正选中时圆点变为橙色。

- [ ] **步骤 4：运行前端全量测试**

运行：

```bash
npm test
```

预期：34 个以上测试全部 PASS，不出现失败或跳过。

- [ ] **步骤 5：提交视觉层级修复**

```bash
git add tests/session_selection_interactions.js internal/webapp/assets/styles.css
git commit -m "fix: clarify selected scope grouping"
```

### 任务 3：回归验证长列表与嵌入式页面

**文件：**
- 验证：`internal/webapp/assets/index.html`
- 验证：`internal/webapp/assets/app.js`
- 验证：`internal/webapp/assets/flow_logic.js`
- 验证：`internal/webapp/assets/styles.css`

- [ ] **步骤 1：运行前端与 Go Web 测试**

运行：

```bash
npm test
env GOCACHE=/private/tmp/kuai-local-export-placement-cache go test ./internal/webapp/... ./internal/server/...
```

预期：所有测试 PASS。

- [ ] **步骤 2：运行静态检查**

运行：

```bash
git diff --check
env GOCACHE=/private/tmp/kuai-local-export-placement-cache go vet ./internal/webapp/... ./internal/server/...
```

预期：无输出且退出码为 0。

- [ ] **步骤 3：浏览器核验真实长列表**

在本地页面选择列表中部和尾部 Scope，逐项确认：

```text
03 · VERIFY 位于整个 Assessment Scope 列表上方
所选 Scope 的单选圆点为橙色实心
02 · LOCAL EXPORT 是所选 Scope 的下一个可见卡片
hover 的未选卡片不会呈现 selected 背景或实心圆点
页面无横向溢出
```

- [ ] **步骤 4：记录最终仓库状态**

运行：

```bash
git status --short
git log -3 --oneline
```

预期：本次修改均已提交；仅保留已知的无关未跟踪 HR-B 检查清单，不暂存、不修改。
