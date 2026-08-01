# kuAI 顶部认证区布局设计

## 1. 问题

当前 `selectionWorkflow` 整体插入所选 Scope 卡片正上方。这个策略适合展示该 Scope 的本地准备结果，但不适合手机号认证：当用户选择长列表深处的 Scope 时，认证卡会随所选卡片移动到底部，用户必须继续滚动才能完成下一步。

仅交换 `03 · VERIFY` 与 `02 · LOCAL EXPORT` 在同一个容器中的 DOM 顺序不能解决根因，因为二者仍共享同一个插入位置。

## 2. 已批准方案

采用方案 A：顶部认证区 + 就近准备反馈。

- `03 · VERIFY` 和 `04 · CONSENT` 属于顶部操作区。
- `02 · LOCAL EXPORT` 属于所选 Scope 的就近反馈区。
- 两个区域独立定位，不再共享 `selectionWorkflow` 容器。

## 3. 页面结构

Scope 区域按以下顺序组织：

```text
状态提示
顶部操作区
  03 · VERIFY（准备完成且未认证）
  04 · CONSENT（认证完成）
Assessment Scope 标题
Scope 列表
  ...
  02 · LOCAL EXPORT
  所选 Scope 卡片
  ...
```

### 3.1 顶部操作区

- 位于 `scopePanel` 内、Assessment Scope 标题和 `scopeList` 之前。
- 本地准备完成前隐藏。
- 本地准备完成且尚未认证时显示 `authPanel`。
- 认证完成后隐藏 `authPanel`，在同一位置显示 `consentPanel`。
- 不使用 `position: sticky` 或固定浮层，避免遮挡列表与移动端内容。

### 3.2 就近准备反馈区

- 只包含 `progressPanel`。
- 本地准备完成后插入所选 Scope 卡片正上方。
- Scope 列表再长，也不影响顶部认证区的位置。
- 更换 Scope、恢复失败或开始新流程时，必须清理或重新定位该反馈区。

## 4. 状态转换

### 初始状态

- 顶部操作区隐藏。
- 就近准备反馈区隐藏。
- Scope 列表正常展示。

### 准备完成

- 就近显示 `02 · LOCAL EXPORT`。
- 页面顶部显示 `03 · VERIFY`。
- 浏览器将顶部认证卡滚入可见区域，避免用户停留在长列表底部。

### 认证完成

- 顶部 `03 · VERIFY` 隐藏。
- 顶部 `04 · CONSENT` 显示。
- `02 · LOCAL EXPORT` 继续保留在所选 Scope 附近。

### 恢复流程

- 若草稿与当前可选 Scope 匹配，恢复顶部认证/授权状态并恢复就近准备反馈。
- 若草稿无效，两个区域都隐藏，并原子清理旧准备状态。

## 5. DOM 与组件边界

- 新增顶部容器 `topWorkflow`，只承载 `authPanel` 和 `consentPanel`。
- `selectionWorkflow` 改为只承载 `progressPanel`，继续由 `placeSelectionWorkflow(document, scopeKey)` 定位。
- `renderPreparation` 分别控制两个容器的显示状态，不能通过移动父容器改变认证位置。
- 手机认证成功后只切换顶部容器内部卡片，不移动 `progressPanel`。

## 6. 响应式和无障碍

- 桌面和移动端都保持顶部操作区位于列表之前。
- 顶部卡片使用普通文档流，不遮挡 Scope 标题。
- 显示认证卡后，将卡片滚入视口；遵循 `prefers-reduced-motion`，减少动画时使用即时滚动。
- 认证和授权标题继续通过 `aria-labelledby` 关联。
- 隐藏状态继续使用原生 `hidden`，避免不可见表单进入键盘焦点序列。

## 7. 错误处理

- 本地准备失败时不显示认证区。
- 认证失败时保留顶部认证卡并显示现有错误提示。
- 无效恢复不得显示旧认证、授权或准备结果。
- 上传失败返回流程页时，顶部授权区和就近准备反馈恢复到提交前状态。

## 8. 测试要求

实现必须先增加失败测试：

1. `topWorkflow` 在 DOM 中位于 Assessment Scope 标题和列表之前。
2. `topWorkflow` 只包含 `authPanel` 与 `consentPanel`。
3. `selectionWorkflow` 只包含 `progressPanel`。
4. 准备完成后，认证卡可见且位于整个 Scope 列表上方。
5. `progressPanel` 仍紧邻所选 Scope 卡片正上方。
6. 认证完成后，顶部位置由 `consentPanel` 接替。
7. 无效草稿恢复时两个工作流区域都隐藏。
8. 长 Scope 列表下，用户无需滚到所选卡片位置即可看到认证卡。

完成实现后运行完整前端和 Go Web 测试，并用真实浏览器检查长列表场景。

## 9. 非目标

- 不改变本地准备、手机号验证、用途授权或上传 API。
- 不修改 Scope 排序。
- 不把认证卡做成吸顶或固定浮层。
- 不移除所选 Scope 附近的本地准备反馈。
