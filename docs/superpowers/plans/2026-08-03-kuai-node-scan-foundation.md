# kuAI Node.js 本地扫描基础实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 创建最低 Node.js 24 的纯 JavaScript npm 包基础，实现有界普通文件读取和可由父进程硬终止的 `node:sqlite` 独立子进程，为后续 CLI 与 adapters 迁移提供已验证边界。

**架构：** 在 `npm/cli` 建立零 runtime dependency 的 TypeScript/ESM 包，保留根 `package.json` 作为现有 Web 测试工程。文件扫描器从已打开 FD 有界读取；SQLite 父端通过 `process.execPath` 启动单用途子进程，使用固定 operation、有界 JSON 协议、只读事务和进程级超时。

**技术栈：** Node.js 24 ESM、TypeScript 5.9.3、`node:fs`、`node:child_process`、`node:sqlite`、`node:test`、GitHub Actions macOS/Windows/Linux runners、Python 3 标准库静态 workflow 测试。

---

## 范围边界

本计划只实现可独立验收的扫描基础。不迁移生产 CLI 命令、source adapters、本地 Web UI、脱敏、上传或 Skill。后续工作按顺序拆分为独立计划：

1. CLI 合同、source-neutral 模型、Registry 和 Go/JavaScript parity harness。
2. 文件型 adapters，然后 SQLite 与混合型 adapters。
3. 本地脱敏、上传、loopback UI 和完整 CLI 切换。
4. Skill 生命周期、README、最终 tarball、OIDC 发布与 `origin/main` PR。

## 文件结构

- 创建 `npm/cli/package.json`：公开 CLI 包 manifest，零 runtime dependencies，Node `>=24`。
- 创建 `npm/cli/package-lock.json`：锁定开发工具，不与根 Web 测试锁文件混合。
- 创建 `npm/cli/tsconfig.json`：编译 `src` 到 `dist`。
- 创建 `npm/cli/bin/node-version.js`：无业务依赖的 Node major 版本解析与门禁函数。
- 创建 `npm/cli/bin/kuai.js`：无业务 import 的 Node major 版本门禁和动态入口。
- 创建 `npm/cli/src/cli/main.ts`：本阶段最小 `version`/`help` 入口。
- 创建 `npm/cli/src/filesystem/identity.ts`：BigInt 文件身份和路径链快照。
- 创建 `npm/cli/src/filesystem/read-bounded-file.ts`：普通文件有界 FD 读取。
- 创建 `npm/cli/src/sqlite/protocol.ts`：子进程请求/响应类型和手写边界验证。
- 创建 `npm/cli/src/sqlite/operations.ts`：固定 operation 注册表与测试探针。
- 创建 `npm/cli/src/sqlite/child-main.ts`：`node:sqlite` 只读子进程入口。
- 创建 `npm/cli/src/sqlite/database-files.ts`：main/WAL/SHM 类型、身份与预算快照。
- 创建 `npm/cli/src/sqlite/run-operation.ts`：启动、协议、stdout/stderr 预算、硬超时、取消和一次重试。
- 创建 `npm/cli/scripts/run-tests.mjs`：跨 shell 枚举 `.test.mjs` 文件。
- 创建 `npm/cli/scripts/verify-package-policy.mjs`：检查 manifest、原生 magic、`.node`、symlink 和 lifecycle hooks。
- 创建 `npm/cli/scripts/verify-tarball.mjs`：对 `npm pack --json` 产物执行文件列表和 magic 审计。
- 创建 `npm/cli/test/**/*.test.mjs`：包、版本、文件读取、SQLite 协议、子进程和快照测试。
- 创建 `.github/workflows/kuai-js-foundation.yml`：Node 24 三平台基础门禁。
- 创建 `tests/test_kuai_js_foundation_workflow.py`：workflow 触发、权限、矩阵、命令和 Node 20 拒绝合同。
- 创建 `docs/superpowers/reports/2026-08-03-kuai-node-scan-foundation.md`：记录本阶段三平台证据和剩余风险。

### 任务 1：建立 Node 24 npm 包与入口门禁

**文件：**
- 创建：`npm/cli/package.json`
- 创建：`npm/cli/package-lock.json`
- 创建：`npm/cli/tsconfig.json`
- 创建：`npm/cli/bin/node-version.js`
- 创建：`npm/cli/bin/kuai.js`
- 创建：`npm/cli/src/cli/main.ts`
- 创建：`npm/cli/scripts/run-tests.mjs`
- 创建：`npm/cli/test/node-version.test.mjs`

- [ ] **步骤 1：编写失败的 Node 版本合同测试**

`node-version.test.mjs` 用 `spawnSync` 运行一个可注入版本的纯函数，断言 `23.11.0` 被拒绝、`24.0.0` 被接受，并断言 `bin/kuai.js` 在业务模块加载前调用该函数：

```javascript
assert.equal(supportsNode('23.11.0'), false);
assert.equal(supportsNode('24.0.0'), true);
assert.match(await readFile(binURL, 'utf8'), /await import\('\.\.\/dist\/cli\/main\.js'\)/);
```

- [ ] **步骤 2：运行测试确认入口不存在**

```bash
node --test npm/cli/test/node-version.test.mjs
```

预期：FAIL，无法导入 `npm/cli/bin/node-version.js`。

- [ ] **步骤 3：创建最小包骨架**

`package.json` 固定关键字段：

```json
{
  "name": "@kuai-ai/cli",
  "version": "0.0.0-dev",
  "type": "module",
  "engines": { "node": ">=24" },
  "bin": { "kuai": "bin/kuai.js" },
  "files": ["bin", "dist", "skill", "README.md", "LICENSE"],
  "scripts": {
    "build": "tsc -p tsconfig.json",
    "test": "npm run build && node scripts/run-tests.mjs",
    "verify:policy": "node scripts/verify-package-policy.mjs"
  },
  "dependencies": {},
  "optionalDependencies": {},
  "peerDependencies": {},
  "devDependencies": { "typescript": "5.9.3" }
}
```

`bin/node-version.js` 只解析 major；`bin/kuai.js` 先拒绝旧 Node，再动态 import 编译产物。`main.ts` 本阶段只实现 `version` 和 `help`，其他命令返回退出码 2 和稳定错误。

- [ ] **步骤 4：生成独立 lockfile 并运行测试**

```bash
npm install --prefix npm/cli --package-lock-only --ignore-scripts
npm ci --prefix npm/cli --ignore-scripts
npm --prefix npm/cli test
```

预期：Node 24 下 PASS；Node 23 或更低运行 bin 时 stderr 包含 `Node.js 24 or newer is required`。

- [ ] **步骤 5：提交包骨架**

```bash
git add npm/cli/package.json npm/cli/package-lock.json npm/cli/tsconfig.json \
  npm/cli/bin npm/cli/src/cli npm/cli/scripts/run-tests.mjs npm/cli/test/node-version.test.mjs
git commit -m "feat: scaffold Node 24 kuai package"
```

### 任务 2：实现零原生产物和零 lifecycle 策略

**文件：**
- 创建：`npm/cli/scripts/verify-package-policy.mjs`
- 创建：`npm/cli/scripts/verify-tarball.mjs`
- 创建：`npm/cli/test/package-policy.test.mjs`
- 创建：`npm/cli/test/tarball-policy.test.mjs`

- [ ] **步骤 1：编写 manifest、symlink 和 magic 失败测试**

测试通过临时包分别放入 `postinstall`、runtime dependency、`.node`、ELF、PE、八种 Mach-O/Universal magic 和 symlink，每一项都必须让 `verifyPackageDirectory()` 拒绝。tarball 测试先断言 `npm pack --json` 产物中不得出现 `src/`、`test/`、`tsconfig.json` 或未知顶级路径。

- [ ] **步骤 2：运行测试确认策略模块缺失**

```bash
npm --prefix npm/cli test
```

预期：FAIL，无法导入 `scripts/verify-package-policy.mjs`。

- [ ] **步骤 3：实现目录与 tarball 审计**

脚本必须：

- 拒绝 `preinstall`/`install`/`postinstall`/`prepare`。
- 拒绝非空 `dependencies`/`optionalDependencies`/`peerDependencies`。
- 不跟随 symlink，发现 symlink 立即失败。
- 读取每个普通文件前 4 字节，拒绝 ELF、MZ/PE、Mach-O 32/64 大小端和 Universal 32/64 大小端。
- 解析 `npm pack --json`的 `files` 清单，只允许 manifest `files` 字段列出的路径。

- [ ] **步骤 4：验证策略与实际 tarball**

```bash
npm --prefix npm/cli run verify:policy
npm pack --json
node scripts/verify-tarball.mjs kuai-ai-cli-0.0.0-dev.tgz
```

上述后两个命令从 `npm/cli` 目录运行。预期：三个命令退出 0，并输出 `pure-js package policy passed` 和 `tarball policy passed`。

- [ ] **步骤 5：提交供应链门禁**

```bash
git add npm/cli/scripts/verify-package-policy.mjs npm/cli/scripts/verify-tarball.mjs \
  npm/cli/test/package-policy.test.mjs npm/cli/test/tarball-policy.test.mjs
git commit -m "test: enforce pure JavaScript package policy"
```

### 任务 3：迁移有界普通文件读取

**文件：**
- 创建：`npm/cli/src/filesystem/identity.ts`
- 创建：`npm/cli/src/filesystem/read-bounded-file.ts`
- 创建：`npm/cli/test/filesystem/read-bounded-file.test.mjs`
- 创建：`npm/cli/test/filesystem/read-bounded-file-posix.test.mjs`
- 创建：`npm/cli/test/filesystem/read-bounded-file-windows.test.mjs`

- [ ] **步骤 1：先复制威胁合同测试，不复制实现**

从 `experiments/kuai-js-feasibility/test/safe-open*.test.mjs` 迁移并保留普通文件、超限、final/intermediate symlink、FIFO、ancestor replacement、final replacement、已打开 FD 内容和 AbortSignal 用例。再新增 relative path 中 `..`、Windows 绝对路径变体和 maxBytes 非安全整数测试。

- [ ] **步骤 2：运行测试确认 TypeScript 模块缺失**

```bash
npm --prefix npm/cli test
```

预期：FAIL，无法导入 `dist/filesystem/read-bounded-file.js`。

- [ ] **步骤 3：实现身份快照和 FD 读取**

公开类型固定为：

```typescript
export interface ReadBoundedOptions {
  maxBytes: number;
  signal?: AbortSignal;
  afterSnapshot?: () => Promise<void>;
  afterOpen?: () => Promise<void>;
}

export async function readBoundedFile(
  root: string,
  relative: string,
  options: ReadBoundedOptions,
): Promise<Uint8Array>;
```

实现顺序为路径链 `lstat({bigint:true})` 快照、`O_RDONLY | O_NOFOLLOW`、FD `stat`、第二次路径快照、`maxBytes + 1` 读取和最终 FD 复核。测试 hook 仅存在 options 中，生产调用者不传入。

- [ ] **步骤 4：在 Node 24 运行文件安全测试**

```bash
npm --prefix npm/cli test
```

预期：当前平台的适用 case 全部 PASS，其他平台 case 明确 SKIP；无 sleep，无挂起进程。

- [ ] **步骤 5：提交文件读取基础**

```bash
git add npm/cli/src/filesystem npm/cli/test/filesystem
git commit -m "feat: add bounded session file reads"
```

### 任务 4：定义 SQLite 有界协议和固定 operation

**文件：**
- 创建：`npm/cli/src/sqlite/protocol.ts`
- 创建：`npm/cli/src/sqlite/operations.ts`
- 创建：`npm/cli/test/sqlite/protocol.test.mjs`
- 创建：`npm/cli/test/sqlite/operations.test.mjs`

- [ ] **步骤 1：编写协议拒绝测试**

断言以下请求均被拒绝：缺 operation、未知 operation、相对 databasePath、额外 `sql` 字段、负预算、非安全整数、`timeoutMs > 5000`、`outputBytes > 16 MiB` 和请求正文超过 64 KiB。

- [ ] **步骤 2：运行测试确认 validator 缺失**

```bash
npm --prefix npm/cli test
```

预期：FAIL，无法导入 `dist/sqlite/protocol.js`。

- [ ] **步骤 3：实现不依赖第三方 schema 库的验证**

```typescript
export type SQLiteOperationName = 'probe.countEvents' | 'probe.longRead';

export interface SQLiteLimits {
  mainBytes: number;
  walBytes: number;
  shmBytes: number;
  totalBytes: number;
  rows: number;
  outputBytes: number;
  timeoutMs: number;
}

export interface SQLiteRequest {
  operation: SQLiteOperationName;
  databasePath: string;
  limits: SQLiteLimits;
}
```

`parseRequest()` 只允许上述精确 keys。`operations.ts` 注册两个基础探针：受行数限制的 `countEvents` 和仅用于超时验证的递归 `longRead`。后续 adapter operation 必须以新文件和独立测试扩展，不增加通用 SQL 入口。

- [ ] **步骤 4：运行协议与 operation 测试**

```bash
npm --prefix npm/cli test
```

预期：所有边界 case PASS，搜索 `rawQuery|executeSql` 无命中。

- [ ] **步骤 5：提交 SQLite 协议**

```bash
git add npm/cli/src/sqlite/protocol.ts npm/cli/src/sqlite/operations.ts \
  npm/cli/test/sqlite/protocol.test.mjs npm/cli/test/sqlite/operations.test.mjs
git commit -m "feat: define bounded SQLite child protocol"
```

### 任务 5：实现 `node:sqlite` 只读子进程

**文件：**
- 创建：`npm/cli/src/sqlite/child-main.ts`
- 创建：`npm/cli/src/sqlite/encode.ts`
- 创建：`npm/cli/test/sqlite/child-main.test.mjs`

- [ ] **步骤 1：编写真实 SQLite 子进程失败测试**

测试用 Node 24 `DatabaseSync` 建立 `events(id INTEGER PRIMARY KEY, value INTEGER)`，插入 `9007199254740993n`，然后通过 stdin 向编译后 child 发送 `probe.countEvents`。期望收到单一 JSON 响应，其 BigInt 编码为：

```json
{"ok":true,"value":{"count":{"$kuaiBigInt":"1"}}}
```

另断言未知 operation、额外 request key、损坏 DB 和写操作不返回 rows。

- [ ] **步骤 2：运行测试确认 child 入口缺失**

```bash
npm --prefix npm/cli test
```

预期：FAIL，无法启动 `dist/sqlite/child-main.js`。

- [ ] **步骤 3：实现只读 SQLite 执行器**

子进程只读取一个最大 64 KiB 的 stdin JSON，然后立即关闭 stdin 处理。数据库固定为：

```typescript
const db = new DatabaseSync(request.databasePath, {
  readOnly: true,
  allowExtension: false,
  defensive: true,
  readBigInts: true,
  timeout: 0,
  limits: sqliteLimits(request.limits),
});
```

启动时确认 `db.setAuthorizer` 存在，并通过构造一个带 `limits` 的内存数据库验证当前 Node 24.x 接受所需 limits 配置；任一能力缺失均返回 `unsupported_runtime`，不得静默省略安全限制。执行 `PRAGMA query_only = ON`、`BEGIN`，设置只允许 SELECT/READ/FUNCTION/TRANSACTION/RECURSIVE 的 authorizer，在 `finally` 中 ROLLBACK 和 close。

- [ ] **步骤 4：验证只读、BigInt 和稳定响应**

```bash
npm --prefix npm/cli test
```

预期：真实 SQLite 测试 PASS，子进程退出 0，stdout 只有一个 JSON 文档，stderr 为空。

- [ ] **步骤 5：提交 SQLite child**

```bash
git add npm/cli/src/sqlite/child-main.ts npm/cli/src/sqlite/encode.ts \
  npm/cli/test/sqlite/child-main.test.mjs
git commit -m "feat: add read-only SQLite child process"
```

### 任务 6：实现父进程硬超时和有界 IPC

**文件：**
- 创建：`npm/cli/src/sqlite/run-operation.ts`
- 创建：`npm/cli/test/sqlite/run-operation.test.mjs`
- 创建：`npm/cli/test/fixtures/sqlite-partial-child.mjs`
- 创建：`npm/cli/test/fixtures/sqlite-oversize-child.mjs`
- 创建：`npm/cli/test/fixtures/sqlite-stderr-child.mjs`

- [ ] **步骤 1：编写会在 Worker 架构下失败的子进程终止测试**

真实 `probe.longRead` 测试设置 `timeoutMs: 50`，断言 promise 在 2 秒内以 `timed_out` 失败，子进程 PID 不再存在，然后立即执行 `probe.countEvents` 并成功。独立断言 AbortSignal、stdout 超限、stderr 超限、部分 JSON、非零退出和多个 JSON 均丢弃全部结果。

- [ ] **步骤 2：运行测试确认 parent runner 缺失**

```bash
npm --prefix npm/cli test
```

预期：FAIL，无法导入 `dist/sqlite/run-operation.js`。

- [ ] **步骤 3：实现单次 settled 父进程状态机**

```typescript
export interface RunSQLiteOptions {
  signal?: AbortSignal;
  childEntry?: URL;
  onSpawn?: (pid: number) => void;
}

export async function runSQLiteOperation(
  request: SQLiteRequest,
  options?: RunSQLiteOptions,
): Promise<unknown>;
```

使用 `spawn(process.execPath, [fileURLToPath(childEntry)], {shell:false, stdio:['pipe','pipe','pipe'], env:filteredEnvironment()})`。父进程以 Buffer 累积 stdout/stderr，超过预算时立即 kill。timeout/abort/error 共用一个 idempotent `terminateAndReject`；直接子进程无子孙，终止后等待 `exit` 最多 1 秒。

- [ ] **步骤 4：在真实 Node 24 上验证硬终止**

```bash
npm --prefix npm/cli test
```

预期：`probe.longRead` 在 2 秒内失败并清理 PID；测试进程自行退出，无需外层 watchdog或人工 Ctrl-C。

- [ ] **步骤 5：提交硬超时 runner**

```bash
git add npm/cli/src/sqlite/run-operation.ts npm/cli/test/sqlite/run-operation.test.mjs \
  npm/cli/test/fixtures/sqlite-*-child.mjs
git commit -m "feat: enforce SQLite subprocess limits"
```

### 任务 7：加入 main/WAL/SHM 预算、并发变化和一次重试

**文件：**
- 创建：`npm/cli/src/sqlite/database-files.ts`
- 修改：`npm/cli/src/sqlite/run-operation.ts`
- 创建：`npm/cli/test/sqlite/database-files.test.mjs`
- 修改：`npm/cli/test/sqlite/run-operation.test.mjs`

- [ ] **步骤 1：编写预算和快照重试失败测试**

测试覆盖 main、WAL、SHM 单项和总大小超限，三者 symlink/非普通文件，main 身份替换，WAL 正常写入，WAL/SHM checkpoint 出现或消失。通过 `beforePostcheck` 测试 hook 确定修改：第一次变化应重试，第二次仍变化应返回 `snapshot_changed`。

- [ ] **步骤 2：运行测试确认 snapshot 模块缺失**

```bash
npm --prefix npm/cli test
```

预期：FAIL，无法导入 `dist/sqlite/database-files.js`。

- [ ] **步骤 3：实现前后快照与最多一次重试**

```typescript
export type SQLiteFailureCode =
  | 'budget_exceeded'
  | 'snapshot_changed'
  | 'unsupported_schema'
  | 'corrupt'
  | 'busy'
  | 'cancelled'
  | 'timed_out'
  | 'protocol_error';
```

main 身份前后必须一致。WAL/SHM 身份或存在性变化、任一大小超限时丢弃当次结果。只对 `snapshot_changed` 重试一次；超时、损坏、预算超限和取消不重试。

- [ ] **步骤 4：运行完整基础测试**

```bash
npm --prefix npm/cli test
npm --prefix npm/cli run verify:policy
```

预期：所有测试 PASS，包括 WAL 可见性、并发变化一次重试和第二次稳定失败。

- [ ] **步骤 5：提交 SQLite 快照预算**

```bash
git add npm/cli/src/sqlite/database-files.ts npm/cli/src/sqlite/run-operation.ts \
  npm/cli/test/sqlite/database-files.test.mjs npm/cli/test/sqlite/run-operation.test.mjs
git commit -m "feat: bound SQLite database snapshots"
```

### 任务 8：建立三平台基础 CI 并记录证据

**文件：**
- 创建：`.github/workflows/kuai-js-foundation.yml`
- 创建：`tests/test_kuai_js_foundation_workflow.py`
- 创建：`docs/superpowers/reports/2026-08-03-kuai-node-scan-foundation.md`

- [ ] **步骤 1：编写 workflow 静态失败测试**

Python 标准库测试断言：

- `pull_request` 和 `workflow_dispatch` 触发。
- matrix 精确包含 `macos-26`、`windows-2025`、`ubuntu-24.04`。
- 权限只有 `contents: read`，无 secrets 和 `id-token: write`。
- 主 matrix 使用 Node 24，执行 `npm ci --ignore-scripts`、`npm test`、策略扫描、`npm pack` 和 tarball 审计。
- 独立 Node 20 job 要求 `bin/kuai.js version` 退出非零并输出最低版本提示。
- job 有 10 分钟上限，无 `continue-on-error`。
- 历史 `kuai-js-feasibility.yml` 仍只有 `workflow_dispatch`。

- [ ] **步骤 2：运行静态测试确认 workflow 缺失**

```bash
python3 -m unittest tests.test_kuai_js_foundation_workflow -v
```

预期：FAIL，找不到 `.github/workflows/kuai-js-foundation.yml`。

- [ ] **步骤 3：创建最小权限三平台 workflow**

每个 matrix job 在 `npm/cli` 中执行：

```yaml
- uses: actions/setup-node@v4
  with:
    node-version: 24
- run: npm ci --ignore-scripts
  working-directory: npm/cli
- run: npm test
  working-directory: npm/cli
- run: npm run verify:policy
  working-directory: npm/cli
- run: npm pack --json
  working-directory: npm/cli
- run: node scripts/verify-tarball.mjs kuai-ai-cli-0.0.0-dev.tgz
  working-directory: npm/cli
```

Node 20 job 不执行 SQLite 测试，只验证入口早期拒绝。

- [ ] **步骤 4：运行本地完整验证**

```bash
python3 -m unittest tests.test_kuai_js_foundation_workflow tests.test_docs -v
npm --prefix npm/cli test
npm --prefix npm/cli run verify:policy
git diff --check
```

预期：本地命令全部 PASS。若本机 Node 低于 24，使用明确的 Node 24 可执行文件运行 `npm/cli/scripts/run-tests.mjs`，不把 Node 22 结果写成通过。

- [ ] **步骤 5：推送功能分支并运行 workflow**

```bash
git push -u origin agentsview_c
```

在 GitHub Actions 中记录三个 matrix job 和 Node 20 rejection job 的 run URL、commit SHA、Node 精确版本和测试计数。任一平台的子进程超时测试挂起、失败或留下 PID 都阻止本阶段完成。

- [ ] **步骤 6：编写基础证据报告**

报告必须包含：目标 commit、每个 runner、Node 版本、子进程终止时间、文件安全 case、SQLite case、tarball 内容和剩余 TOCTOU 声明。不得将调整后威胁模型描述为 Go 安全等价。

- [ ] **步骤 7：提交 CI 和报告**

```bash
git add .github/workflows/kuai-js-foundation.yml \
  tests/test_kuai_js_foundation_workflow.py \
  docs/superpowers/reports/2026-08-03-kuai-node-scan-foundation.md
git commit -m "ci: verify Node scan foundation"
```

## 完成条件

- `@kuai-ai/cli` manifest 要求 Node `>=24`，Node 20 在任何业务模块加载前失败。
- npm 包零 runtime dependencies、零 lifecycle hooks，tarball 无 kuAI 自带原生文件与 symlink。
- 普通文件扫描在调整后威胁模型下有界、可取消、从 FD 读取并拒绝静态链接/特殊文件。
- SQLite 只通过独立 Node 子进程执行，不使用 Worker thread、shell、任意 SQL 或扩展加载。
- 真实递归 SQLite 长查询在两秒内被父进程终止，后续正常查询成功，三平台无遗留 PID。
- main/WAL/SHM 为普通文件、分项和总大小有界，快照变化最多重试一次。
- 三平台 CI 实际通过并写入证据报告后，才为 CLI/模型/Registry 迁移创建下一份实现计划。
