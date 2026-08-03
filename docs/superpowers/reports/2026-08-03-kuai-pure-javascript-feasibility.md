# kuAI 纯 JavaScript 安全可行性报告

**日期：** 2026-08-03

**基准 commit：** `8bd154105ebcb19e22d0df5aae093768a0118834`

**范围：** Node.js 24 纯 JavaScript npm CLI 的文件安全与 SQLite 只读隔离门禁

## 裁决

```text
Decision: NO-GO
```

不按当前威胁模型继续将 kuAI 全量重写为纯 JavaScript。这个裁决不是因为 npm 分发或 macOS 签名失败，而是 Node.js 24 公开 API 无法证明保持当前 Go 实现的两个关键安全合同。

## 阻塞证据

### 1. SQLite 同步查询无法在两秒内终止

Node.js `v24.18.1` 下，Worker 进入同步 `node:sqlite` 递归 CTE 后，`worker.terminate()` 超过 60 秒仍未完成。原始运行得到 `5 pass / 1 cancelled` 并需要人工中断；加入两秒终止门禁后，明确错误为：

```text
Error: SQLite Worker did not terminate within two seconds
```

`AbortSignal` 使用相同的 Worker 终止路径，因此同样不满足合同。这是已批准计划中的独立 `NO-GO` 条件。

### 2. SQLite 路径打开存在 TOCTOU 窗口

实验实现在父线程对 main/WAL/SHM 做前置快照，然后由 Worker 使用路径构造 `DatabaseSync`，最后再复核快照。在前置快照和 Worker 实际打开之间，数据库可被换入；若在后置复核前换回，身份和大小复核不能证明 SQLite 查询读取的就是被验证文件。

当前 Go 实现保留已验证文件句柄，并在 SQLite 打开和事务 pin 周围复核句柄身份。`node:sqlite` 公开构造器按路径打开，实验未能建立等价绑定。

### 3. 文件路径遍历不等价于 Go `BoundRoot`

本地确定性测试能够拒绝测试钩子触发的 ancestor/final replacement、symlink 和 FIFO。但 Node 公开 `fs` API 没有与 Go `openat` 相当的跨平台、目录句柄相对遍历。逐组件 `lstat`、最终路径 `open` 和后续快照不是同一原子遍历，因此不能宣称保持现有 Go 安全边界。

## 已验证的局部能力

| 领域 | 环境 | 结果 |
|---|---|---|
| 零 runtime/peer/optional dependency 与零 lifecycle hooks | macOS，Node `v24.18.1` | PASS |
| ELF、PE、Mach-O/Universal magic、`.node` 与 symlink 拒绝 | macOS，Node `v24.18.1` | PASS，5 tests |
| 文件大小、FIFO、symlink、确定性替换和取消 | macOS，Node `v24.18.1` | 10 PASS，2 Windows-only SKIP |
| workflow 矩阵、Node 24、最小权限、禁 install/签名/绕过、2 分钟 job 上限 | Python 标准库静态测试 | PASS，6 tests |
| SQLite BigInt、操作白名单、WAL、main/WAL/SHM/总预算、sidecar 变化后丢弃结果 | Node `v24.18.1` | 5 PASS |
| SQLite timeout/abort 两秒内终止 | Node `v24.18.1` | FAIL |

## 三平台状态

未推送分支，也未运行 GitHub Actions 的 `macos-26`、`windows-2025`、`ubuntu-24.04` matrix，因此没有 run URL 可记录。这不会将结论降级为“待验证”：已触发与平台矩阵无关的固定 `NO-GO` 条件，不需要通过更多平台尝试覆盖该失败。workflow 因此只保留 `workflow_dispatch` 手动复现入口，不在每个 pull request 上重复执行已知失败门禁。

## 后续方案

当前不应开始纯 JavaScript 产品 CLI 和 source adapters 迁移。可选路径只有：

1. 保留 Go 安全实现，对分发的 macOS/Windows 原生产物做正式签名与公证。
2. 另建规格，由产品与安全负责人明确批准降低威胁模型。
3. 研究带签名的最小本地安全代理，但这将不再是“纯 JavaScript 免签”方案。

不删除失败测试，不通过增加无界等待、放宽两秒合同或引入未审核原生 npm 扩展将裁决改成 `GO`。

## 后续用户决策

2026-08-03，用户明确批准降低本地会话扫描的威胁模型：不防御同一用户权限下恶意进程精确制造的路径替换竞态，但仍要求只读、非管理员运行、路径和资源边界、本地脱敏与用户授权上传。

用户同时批准保留 SQLite 数据源，并选择通过 `process.execPath` 启动独立 Node 子进程、直接只读扫描原数据库。子进程的硬超时解决本报告中 Worker 无法终止的产品可用性问题，但不改变强对抗模型的历史 `NO-GO` 结论。新的实现边界见 [本地会话扫描与 SQLite 子进程设计](../specs/2026-08-03-kuai-pure-javascript-local-session-scan-design.md)。
