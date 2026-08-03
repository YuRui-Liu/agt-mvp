# kuAI 纯 JavaScript 本地会话扫描与 SQLite 子进程设计

## 1. 决策摘要

kuAI 继续向 Node.js 24 纯 JavaScript npm CLI 迁移，用于当前用户账户下的本地会话发现、只读扫描、本地脱敏和用户授权上传。该决策采用用户于 2026-08-03 明确批准的威胁模型调整：不要求防御同一用户权限下的恶意进程精确制造路径替换竞态。

SQLite 数据源必须支持，但不在 Worker thread 中执行同步 `node:sqlite` 查询。父 CLI 通过 `process.execPath` 启动独立 Node 子进程，子进程以只读事务直接打开原数据库，父进程在超时、取消、输出超限或协议错误时终止整个子进程。

本规格是 [纯 JavaScript npm 分发设计](2026-08-03-kuai-pure-javascript-npm-distribution-design.md) 的安全与 SQLite 补充决策。[安全可行性报告](../reports/2026-08-03-kuai-pure-javascript-feasibility.md) 中的 `Decision: NO-GO` 仍对“完整复制 Go `BoundRoot/openat` 强对抗边界”有效，但不再阻止本规格明确的产品模型。

## 2. 产品目标

- 通过单一 `@kuai-ai/cli` npm 包支持 macOS、Windows 和 Linux。
- 扫描 JSON、JSONL、Markdown、TXT 和 SQLite 会话数据源。
- kuAI 自身不建立持久 SQLite 数据库；SQLite 只是外部产品会话的输入格式。
- 本地完成解析、脱敏、聚合和预览，用户确认后才允许网络上传。
- npm 包不包含或下载 kuAI 自带的 Mach-O、PE、ELF、`.node` 或 Go 可执行文件。
- 随包提供 canonical Agent Skill，通过显式 CLI 命令管理用户 HOME 中的安装。

## 3. 威胁模型

### 3.1 必须防御

- 绝对路径与相对路径中的 `..` 越界。
- 静态 symlink、Windows junction/reparse point 和其他非普通文件。
- FIFO、socket、device 等可造成挂起或副作用的特殊文件。
- 损坏 SQLite、未知 schema、超大 DB/WAL/SHM、超多 rows 和超大输出。
- 同步 SQLite 长查询导致主 CLI 无法响应取消或退出。
- 子进程返回部分 JSON、多余字段、超限 stdout 或敏感 stderr。
- npm lifecycle 修改 HOME、下载代码或恢复原生执行文件。

### 3.2 明确接受的剩余风险

- 同一用户权限下的恶意进程在多次 `lstat`/`open` 之间精确换入、换回路径组件。
- SQLite 前置文件检查与 `DatabaseSync` 按路径打开之间的 TOCTOU 窗口。
- Node 公开 `fs` API 不提供与 Go `BoundRoot + openat` 完全等价的跨平台句柄相对遍历。

这些风险必须出现在 README 和安全文档中。产品不得宣称与 Go 强对抗边界等价。CLI 禁止以 root 或管理员身份运行，避免将上述风险扩大到更高权限。

## 4. 运行时架构

```text
kuai 父 CLI
├── 命令与配置
├── source registry
├── 文件扫描与有界 FD 读取
├── SQLite 子进程监督
├── canonical event 与本地脱敏
├── 结果预览与用户授权
├── 上传与 loopback Web UI
└── Skill 生命周期管理

SQLite 子进程
├── 有界 JSON request
├── operation -> 固定 SQL 映射
├── node:sqlite 只读事务
├── schema/row/field 限制
└── 有界 JSON response
```

父进程和 SQLite 子进程是唯一新增信任边界。子进程不启动 shell、不再启动子进程、不上网、不接受任意 SQL，也不修改源数据库。

## 5. 普通文件扫描

文件扫描器只接受产品 catalog 定义的 root 或用户明确指定的绝对 root。每次读取执行：

1. 验证 relative path，拒绝绝对路径与 `..`。
2. 用 `lstat({bigint:true})` 检查 root、父目录和最终文件，拒绝 symlink/junction 与错误类型。
3. 记录 dev/ino 或平台能提供的稳定身份。
4. 打开最终文件，通过 `fstat` 验证普通文件、身份和大小。
5. 从已打开 FD 读取最多 `maxBytes + 1`，超限立即失败。
6. 读取后复核 FD 身份和大小。
7. 变化时丢弃数据并最多重试一次。

所有预算按 UTF-8 字节计算，包含目录深度、目录项数、单文件、单行、单会话、单 source 总字节和会话数。一个 source 失败不中断其他 source。

## 6. SQLite 子进程

### 6.1 启动和环境

父进程固定使用：

```javascript
spawn(process.execPath, [sqliteWorkerPath], {
  shell: false,
  stdio: ['pipe', 'pipe', 'pipe'],
  env: filteredEnvironment,
});
```

`filteredEnvironment` 不包含 `NODE_OPTIONS`、`NODE_PATH` 或与 SQLite 扩展、动态加载有关的用户变量。子进程不使用 shell，不搜索 PATH 中的另一个 Node。

### 6.2 请求协议

请求只包含：

```json
{
  "operation": "opencode.listSessions",
  "databasePath": "/absolute/validated/path/opencode.db",
  "limits": {
    "mainBytes": 67108864,
    "walBytes": 67108864,
    "shmBytes": 8388608,
    "totalBytes": 134217728,
    "rows": 10000,
    "outputBytes": 16777216,
    "timeoutMs": 5000
  }
}
```

operation 在子进程内映射到固定 prepared statements。协议不存在 `executeSql`、`rawQuery` 或 SQL 字符串字段。请求大小、路径长度、数值范围和额外字段都必须校验。

### 6.3 SQLite 固定配置

```javascript
new DatabaseSync(databasePath, {
  readOnly: true,
  allowExtension: false,
  defensive: true,
  readBigInts: true,
  timeout: 0,
});
```

打开后启用 `PRAGMA query_only = ON` 和只读事务，并使用 authorizer 拒绝非读操作。每个 operation 先验证 schema、表、必要列和可接受版本，再执行固定 SQL。SQLite INTEGER 保持 BigInt，在协议边界转换为带类型标记的十进制字符串。

### 6.4 超时和输出边界

- 默认每个数据库 5 秒硬超时，可由内部 adapter 在全局上限内缩短，不由用户无界扩大。
- timeout 或 AbortSignal 触发时，父进程终止整个 SQLite 子进程。
- stdout 超过 `outputBytes`、协议非单一完整 JSON 或响应 schema 错误时，终止并丢弃所有部分数据。
- stderr 有独立小预算，不得包含会话正文或完整私有路径。
- 子进程退出后父进程才接受结果，不接受流式部分 rows。

### 6.5 并发写入

SQLite 只读事务提供查询期间的一致视图。父进程在启动前和返回后检查 main/WAL/SHM：

- 存在的文件必须仍是普通非 symlink 文件。
- 单文件和总大小必须在预算内。
- main 身份替换时丢弃结果。
- WAL/SHM 因正常 checkpoint 出现、消失或替换时，丢弃当次结果并最多重试一次。
- 第二次仍变化时返回 `snapshot_changed`，不阻断其他 source。

## 7. 数据源分类

- 文件型：Claude Code、Codex、Cursor、Gemini CLI、Kimi、Kimi Code、OpenClaw、Qwen、Aider、Cline、Copilot CLI、VS Code Copilot、WorkBuddy 等。
- SQLite 型：Lingma IDE、Qoder IDE。
- 混合型：OpenCode、Hermes、CodeFlicker，优先 SQLite，SQLite 不可用时才使用现有文件格式。
- Lingma CLI 和 Qoder CLI 保持 JSONL 读取，不与 IDE SQLite 路径混合。

每个 source 以结构化状态结束：`ready`、`not_found`、`permission_denied`、`snapshot_changed`、`budget_exceeded`、`unsupported_schema`、`corrupt`、`busy`、`cancelled`。

## 8. npm 与 Skill 分发

- 最低 Node.js 24，入口在加载业务模块前检查版本。
- 正式 npm tarball 不得包含 kuAI 自带原生文件、原生平台包、symlink 或 lifecycle hooks。
- 使用 GitHub Actions OIDC Trusted Publishing 和 provenance，不保存长期 npm token。
- 三平台安装和验证唯一最终 tarball 后，才发布同一 artifact。
- 公司对外产物平台若兼容 npm registry，发布同一已验证 tarball。
- GitHub 是源码、tag、workflow 和 release 信息渠道；用户安装优先使用 npm registry，不使用 `curl | sh`。

安装流程：

```bash
brew install node@24
npm install -g @kuai-ai/cli
kuai skill install --target all
```

Skill 支持 `install`、`status`、`upgrade`、`uninstall`，目标为 `codex`、`claude`、`all` 或自定义目录。它使用 managed marker、原子写入、用户修改检测和冲突备份，但不在 npm `postinstall` 中写 HOME。

## 9. 验证合同

### 9.1 SQLite 子进程

- 递归长查询超时后，子进程在硬上限内消失，父进程随后可完成正常查询。
- AbortSignal、stdout 超限、崩溃、部分 JSON 和非零退出都不返回部分数据。
- 覆盖 read-only、WAL、BigInt、未知 schema、损坏 DB、busy、main/WAL/SHM 预算与正常并发写入。
- macOS、Windows、Linux 运行相同的进程终止合同。

### 9.2 文件扫描

- 覆盖路径越界、symlink/junction、FIFO、父目录替换、最终文件替换、超限和取消。
- 竞态测试通过注入 hook 确定触发，不使用 sleep 猜测时序。
- 所有 adapter 对相同 fixture 与 Go oracle 做稳定字段和脱敏事件 parity。

### 9.3 包与 Skill

- 扫描 tarball 及完整依赖树的文件扩展名、magic、symlink 和 lifecycle hooks。
- 三平台从同一 tarball 安装，执行 CLI 与 Skill 安装/升级/卸载 smoke tests。
- 验证 npm 安装阶段不修改 HOME，Skill 冲突不静默覆盖用户文件。

## 10. 迁移顺序

1. 包骨架、文件读取、SQLite 子进程协议、资源预算和供应链策略。
2. JSON/JSONL/Markdown adapters 及 Go/JavaScript parity harness。
3. Lingma IDE、Qoder IDE SQLite adapters。
4. OpenCode、Hermes、CodeFlicker 混合 adapters。
5. CLI 命令、本地脱敏、上传授权和 loopback UI。
6. Skill 生命周期、README 和三平台最终 tarball 验收。

Go CLI 在 JavaScript 版本完成命令、adapter、脱敏和上传 parity 前保留，不提前删除。

## 11. 仓库与交付

目标仓库为 `https://github.com/YuRui-Liu/agt-mvp.git`。开发继续在当前 `agentsview_c` 功能分支完成，保留用户现有未提交文件。完成验证后推送功能分支，并向 `origin/main` 创建 PR；不直接将开发提交推入 main。

## 12. 对外安全表述

可以准确声明：

> kuAI npm 包不包含项目自身的原生 macOS/Windows/Linux 可执行代码，因此 kuAI 包本身不需要 Apple Developer ID 或 Authenticode。SQLite 由用户已安装的 Node.js 24 内置能力只读执行。

不得声明：

- 绝不会被 Gatekeeper、SmartScreen、XProtect、EDR、MDM 或企业 npm 策略拦截。
- npm provenance 等价于操作系统代码签名。
- JavaScript 文件扫描与 Go `BoundRoot/openat` 强对抗边界等价。
- 产品防御同一用户权限下恶意进程的精确路径竞态。
