# kuAI 本地准备卡位置修订

## 1. 问题

当前 `02 · LOCAL EXPORT` 插入所选 Scope 卡片之前。长列表中，准备卡上方可能紧邻另一张未选中的 Scope 卡，而真正所选卡位于准备卡下方甚至视口外，用户容易误判两者关系。

hover/focus 描边与选中状态同时存在时，这种误判会更加明显。

## 2. 已批准方案

采用方案 A：所选 Scope 卡片在前，本地准备卡紧随其后。

```text
其他 Scope
所选 Scope
02 · LOCAL EXPORT
其他 Scope
```

顶部流程保持不变：

- `03 · VERIFY` 在整个 Scope 列表上方；
- 认证成功后，`04 · CONSENT` 在顶部接替；
- 只有 `02 · LOCAL EXPORT` 跟随所选 Scope。

## 3. DOM 规则

- `placeSelectionWorkflow(document, scopeKey)` 将 `selectionWorkflow` 插入所选卡片之后。
- DOM 必须满足 `selectedCard.nextElementSibling === selectionWorkflow`。
- `selectionWorkflow` 不得被插到另一张 Scope 卡片之后。
- Scope 重绘、草稿恢复、上传失败返回时都必须重新建立同一关系。
- 找不到匹配且可选的 Scope 时隐藏准备卡，不能挂靠到最后一张卡片。

## 4. 视觉规则

- 所选卡继续使用明确的选中圆点和选中背景。
- hover/focus 只能表示交互反馈，不能与 selected 使用完全相同的视觉强度。
- 准备卡与所选卡的垂直间距小于普通 Scope 卡之间的间距，形成一个视觉分组。
- 准备卡之后恢复普通列表间距。
- 移动端保持相同阅读顺序，不使用绝对定位。

## 5. 状态规则

- 准备完成后，所选 Scope 卡保持 selected 状态。
- 更换 Scope 后，旧准备状态按现有规则失效，新准备卡不得停留在旧位置。
- 有效草稿恢复后，先恢复选中 Scope，再把准备卡放到它后面。
- 无效恢复隐藏准备卡并清理旧准备草稿。

## 6. 测试要求

实现前增加失败测试：

1. 真实 DOM 中 `selectedCard.nextElementSibling === selectionWorkflow`。
2. 长列表选择中间或尾部 Scope 时，准备卡都位于该卡之后。
3. 重绘与有效恢复后关系不变。
4. 找不到 Scope 时准备卡隐藏。
5. hover/focus 样式不得等同于 selected 的背景和圆点状态。

完成后运行完整前端、Go Web 测试，并使用真实长列表页面截图核验。
