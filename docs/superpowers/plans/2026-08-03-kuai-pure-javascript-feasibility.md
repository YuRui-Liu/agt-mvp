# kuAI 纯 JavaScript 安全可行性实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在不引入原生 npm 扩展或安装脚本的前提下，用真实 macOS、Windows、Linux 证据判断 Node.js 24 能否保持 kuAI 当前安全文件读取和 SQLite 只读快照合同。

**架构：** 在隔离的 `experiments/kuai-js-feasibility` 目录实现两个最小探针：安全文件读取探针使用路径组件身份、最终文件描述符和前后复核抵抗 symlink/reparse/祖先替换；SQLite 探针在 Worker 中使用 `node:sqlite`，由父线程实施 sidecar 预算、超时和强制终止。三平台 workflow 是 go/no-go 的唯一裁决入口；任一安全断言失败就停止全面 JavaScript 迁移，不降低测试门槛。

**技术栈：** Node.js 24 ESM、`node:fs`、`node:sqlite`、`node:worker_threads`、`node:test`、GitHub Actions macOS/Windows/Linux runners、Python 3 workflow 静态测试。

---

## 范围与决策规则

本计划只验证两个阻塞性合同，不迁移产品 CLI、source adapters、Web UI、上传或 Skill。当前 Go 实现保持不变，并作为威胁模型依据：

- `internal/source/internal/safeopen/open.go`
- `internal/source/internal/safeopen/open_unix.go`
- `internal/source/internal/safeopen/open_windows.go`
- `internal/source/internal/safeopen/open_test.go`
- `internal/source/internal/safeopen/open_unix_test.go`
- `internal/source/internal/safeopen/open_windows_test.go`
- `internal/source/internal/sqliteread/readonly.go`
- `internal/source/internal/sqliteread/readonly_test.go`

裁决规则固定如下：

- **GO：** 三个平台全部通过普通文件、symlink/junction、祖先替换、文件替换、特殊文件、大小限额、SQLite read-only、WAL、sidecar 预算、BigInt、写操作拒绝和超时取消测试；实验包及其完整依赖树无 Mach-O/PE/ELF/`.node` 和 lifecycle scripts。
- **NO-GO：** 任一平台无法用 Node.js 24 公开 API 可靠拒绝攻击性 case，或 SQLite Worker 无法在两秒内终止有界测试查询，或必须引入原生扩展才能通过。
- **NO-GO 后续：** 停止纯 JavaScript 全量迁移；回到签名 Go 产物，或另开规格由用户明确批准降低威胁模型。不得删除失败测试来获得 GO。

## 文件结构

- 创建 `experiments/kuai-js-feasibility/package.json`：零依赖、零 lifecycle scripts 的实验 manifest。
- 创建 `experiments/kuai-js-feasibility/src/file-identity.mjs`：BigInt 文件身份和路径链快照。
- 创建 `experiments/kuai-js-feasibility/src/safe-open.mjs`：安全打开、有界读取和前后身份复核。
- 创建 `experiments/kuai-js-feasibility/src/sqlite-worker.mjs`：`node:sqlite` 只读 Worker。
- 创建 `experiments/kuai-js-feasibility/src/sqlite-snapshot.mjs`：sidecar 预算、Worker 超时和结果边界。
- 创建 `experiments/kuai-js-feasibility/test/safe-open.test.mjs`：平台中立文件攻击测试。
- 创建 `experiments/kuai-js-feasibility/test/safe-open-posix.test.mjs`：FIFO 与 POSIX symlink 测试。
- 创建 `experiments/kuai-js-feasibility/test/safe-open-windows.test.mjs`：junction/reparse point 测试。
- 创建 `experiments/kuai-js-feasibility/test/sqlite-snapshot.test.mjs`：SQLite/WAL/预算/取消测试。
- 创建 `experiments/kuai-js-feasibility/scripts/verify-package-policy.mjs`：无原生文件、依赖和 lifecycle 门禁。
- 创建 `.github/workflows/kuai-js-feasibility.yml`：Node.js 24 三平台验证。
- 创建 `tests/test_kuai_js_feasibility_workflow.py`：workflow 权限、矩阵和命令静态合同。
- 创建 `docs/superpowers/reports/2026-08-03-kuai-pure-javascript-feasibility.md`：三平台证据与最终 GO/NO-GO 记录。

## 任务 1：建立零原生依赖实验包和策略门禁

**文件：**
- 创建：`experiments/kuai-js-feasibility/package.json`
- 创建：`experiments/kuai-js-feasibility/scripts/verify-package-policy.mjs`
- 创建：`experiments/kuai-js-feasibility/test/package-policy.test.mjs`

- [ ] **步骤 1：编写失败的 manifest 合同测试**

创建 `experiments/kuai-js-feasibility/test/package-policy.test.mjs`：

```javascript
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const manifestURL = new URL('../package.json', import.meta.url);

test('experiment has no runtime dependencies or lifecycle scripts', async () => {
  const manifest = JSON.parse(await readFile(manifestURL, 'utf8'));
  assert.deepEqual(manifest.dependencies ?? {}, {});
  assert.deepEqual(manifest.optionalDependencies ?? {}, {});
  for (const hook of ['preinstall', 'install', 'postinstall', 'prepare']) {
    assert.equal(manifest.scripts?.[hook], undefined);
  }
  assert.equal(manifest.engines.node, '>=24');
});
```

- [ ] **步骤 2：运行测试确认 manifest 不存在**

运行：

```bash
node --test experiments/kuai-js-feasibility/test/package-policy.test.mjs
```

预期：FAIL，报错无法打开 `experiments/kuai-js-feasibility/package.json`。

- [ ] **步骤 3：创建最小 manifest**

```json
{
  "name": "kuai-js-feasibility",
  "version": "0.0.0",
  "private": true,
  "type": "module",
  "engines": { "node": ">=24" },
  "scripts": {
    "test": "node --test test/*.test.mjs",
    "verify:policy": "node scripts/verify-package-policy.mjs"
  }
}
```

- [ ] **步骤 4：实现文件 magic 与 manifest 门禁**

`verify-package-policy.mjs` 递归遍历实验目录，跳过 `.git`，拒绝 `.node`，并读取每个普通文件前 4 字节。拒绝以下 magic：

```javascript
const forbidden = [
  Buffer.from([0x7f, 0x45, 0x4c, 0x46]),
  Buffer.from([0x4d, 0x5a]),
  Buffer.from([0xfe, 0xed, 0xfa, 0xce]),
  Buffer.from([0xfe, 0xed, 0xfa, 0xcf]),
  Buffer.from([0xcf, 0xfa, 0xed, 0xfe]),
  Buffer.from([0xca, 0xfe, 0xba, 0xbe]),
];
```

脚本同时读取 manifest，要求 `dependencies`、`optionalDependencies` 为空，且不存在四个 lifecycle hook。任一违规写 stderr 并退出 1；通过时打印 `pure-js package policy passed`。

- [ ] **步骤 5：运行策略测试**

运行：

```bash
node --test experiments/kuai-js-feasibility/test/package-policy.test.mjs
node experiments/kuai-js-feasibility/scripts/verify-package-policy.mjs
```

预期：两个命令退出 0；第二个命令输出 `pure-js package policy passed`。

- [ ] **步骤 6：提交实验骨架**

```bash
git add experiments/kuai-js-feasibility
git commit -m "test: add pure JavaScript feasibility gate"
```

## 任务 2：验证 Node 文件身份与竞态防护

**文件：**
- 创建：`experiments/kuai-js-feasibility/src/file-identity.mjs`
- 创建：`experiments/kuai-js-feasibility/src/safe-open.mjs`
- 创建：`experiments/kuai-js-feasibility/test/safe-open.test.mjs`
- 创建：`experiments/kuai-js-feasibility/test/safe-open-posix.test.mjs`
- 创建：`experiments/kuai-js-feasibility/test/safe-open-windows.test.mjs`

- [ ] **步骤 1：编写平台中立失败测试**

`safe-open.test.mjs` 必须先覆盖：普通文件、有界读取、最终 symlink、祖先目录在检查和 open 之间被原子替换、最终文件在检查和 open 之间被替换、读取后路径替换不改变已打开 FD 内容、取消信号。

竞态测试通过注入 hook 确定触发，不依赖 sleep：

```javascript
await assert.rejects(
  readBoundedFile(root, 'session/events.jsonl', {
    maxBytes: 1024,
    afterSnapshot: async () => {
      await rename(join(root, 'session'), join(root, 'session.original'));
      await rename(join(root, 'attacker'), join(root, 'session'));
    },
  }),
  /path identity changed/,
);
```

- [ ] **步骤 2：运行测试确认模块不存在**

运行：

```bash
node --test experiments/kuai-js-feasibility/test/safe-open.test.mjs
```

预期：FAIL，报错无法导入 `src/safe-open.mjs`。

- [ ] **步骤 3：实现身份类型和路径链快照**

`file-identity.mjs` 使用 `lstat(path, { bigint: true })`，拒绝 symbolic link；目录组件必须 `isDirectory()`，最终组件必须 `isFile()`。身份固定为：

```javascript
export function identityOf(stats) {
  if (stats.dev === 0n || stats.ino === 0n) {
    throw new Error('stable file identity unavailable');
  }
  return `${stats.dev}:${stats.ino}`;
}
```

`snapshotPath(root, relative)` 要求 root 为绝对路径，relative 非绝对、规范化后不含 `..`，并返回从 root 到最终文件的 `{path, identity, kind}` 数组。`sameSnapshot(before, after)` 逐项比较路径、身份和类型。

- [ ] **步骤 4：实现基于 FD 的有界读取**

`safe-open.mjs` 按固定顺序执行：

1. `snapshotPath` 保存完整链与最终文件身份、大小。
2. 执行测试注入的 `afterSnapshot`。
3. 用 `O_RDONLY | O_NOFOLLOW` 打开最终文件；平台没有 `O_NOFOLLOW` 时使用 `O_RDONLY`，但仍必须通过最终 `fstat` 身份比较。
4. `fstat({bigint:true})` 必须是普通文件，身份等于步骤 1，大小不超过 `maxBytes`。
5. 第二次 `snapshotPath` 必须和第一次完全一致。
6. 从已打开 `FileHandle` 读取最多 `maxBytes + 1`；多一字节即拒绝。
7. 再次 `fstat`，身份和大小必须保持不变。
8. `finally` 关闭 handle；任何 AbortSignal 取消都拒绝并不返回数据。

公开函数签名固定为：

```javascript
export async function readBoundedFile(root, relative, {
  maxBytes,
  signal,
  afterSnapshot = async () => {},
} = {}) {}
```

- [ ] **步骤 5：加入 POSIX 特殊文件测试**

`safe-open-posix.test.mjs` 在 `process.platform !== 'win32'` 时运行。使用 `mkfifo` 创建 FIFO，断言探针在尝试读取前根据 `lstat` 拒绝；创建最终 symlink 和中间 symlink，均断言拒绝。测试不能从 FIFO 读取，避免阻塞。

- [ ] **步骤 6：加入 Windows junction 测试**

`safe-open-windows.test.mjs` 只在 `win32` 运行，使用：

```javascript
await symlink(attackerDir, junctionPath, 'junction');
```

断言 root、祖先和最终组件中的 junction/symlink 均被拒绝；同时断言 `dev`/`ino` 非零且 rename 前后身份可区分。若官方 GitHub runner 无法提供稳定身份，本任务必须失败为 NO-GO。

- [ ] **步骤 7：运行文件安全测试**

运行：

```bash
node --test experiments/kuai-js-feasibility/test/safe-open*.test.mjs
```

预期：当前平台全部适用 case PASS；无定时 sleep，无挂起进程。

- [ ] **步骤 8：提交文件安全探针**

```bash
git add experiments/kuai-js-feasibility/src/file-identity.mjs \
  experiments/kuai-js-feasibility/src/safe-open.mjs \
  experiments/kuai-js-feasibility/test/safe-open.test.mjs \
  experiments/kuai-js-feasibility/test/safe-open-posix.test.mjs \
  experiments/kuai-js-feasibility/test/safe-open-windows.test.mjs
git commit -m "test: probe Node safe file access"
```

## 任务 3：验证 node:sqlite 只读快照与可取消执行

**文件：**
- 创建：`experiments/kuai-js-feasibility/src/sqlite-worker.mjs`
- 创建：`experiments/kuai-js-feasibility/src/sqlite-snapshot.mjs`
- 创建：`experiments/kuai-js-feasibility/test/sqlite-snapshot.test.mjs`

- [ ] **步骤 1：编写失败测试**

测试必须覆盖：

- `DatabaseSync` 使用 `readOnly:true`、`allowExtension:false`、`defensive:true`、`readBigInts:true`。
- 固定 SELECT 返回 BigInt，不经过 JS Number 丢精度。
- INSERT、ATTACH、写 PRAGMA、多语句和 load extension 被拒绝。
- WAL 中已提交数据可见。
- main/WAL/SHM 任一或总大小超限时，Worker 启动前拒绝。
- 查询期间 sidecar 身份或大小变化时丢弃结果。
- 递归 CTE 长查询在 abort 或 timeout 后两秒内终止 Worker，父进程随后仍能完成一个正常查询。

长查询只用于取消验证：

```sql
WITH RECURSIVE cnt(x) AS (
  VALUES(0) UNION ALL SELECT x + 1 FROM cnt WHERE x < 1000000000
) SELECT max(x) FROM cnt
```

- [ ] **步骤 2：运行测试确认模块不存在**

运行：

```bash
node --test experiments/kuai-js-feasibility/test/sqlite-snapshot.test.mjs
```

预期：FAIL，报错无法导入 `src/sqlite-snapshot.mjs`。

- [ ] **步骤 3：实现只读 Worker**

`sqlite-worker.mjs` 只接受父线程传入的固定 operation 名和参数，不接受任意 SQL。operation 映射在模块内定义 prepared statement。数据库构造固定为：

```javascript
const db = new DatabaseSync(databasePath, {
  readOnly: true,
  allowExtension: false,
  defensive: true,
  readBigInts: true,
  timeout: 0,
});
```

打开后执行 `PRAGMA query_only = ON` 和 `BEGIN`，设置 `setAuthorizer` 拒绝除 SELECT/READ/FUNCTION/TRANSACTION 之外的 action，并在 `finally` ROLLBACK、close。Worker 只通过 structured clone 返回受控对象；BigInt 在父线程 canonical 层转换前保持 BigInt。

测试专用 `longRead` operation 使用上面的固定递归 CTE。生产 operation 不允许调用该 SQL。

- [ ] **步骤 4：实现父线程预算、身份复核和终止**

`sqlite-snapshot.mjs` 对 `db`、`db-wal`、`db-shm` 使用任务 2 的身份函数：存在的文件必须普通、非 symlink；分别和总大小不超过调用者预算。启动 Worker 前记录身份/大小，成功消息到达后重新记录并逐项比较；变化即终止 Worker并拒绝结果。

公开函数：

```javascript
export async function runReadonlyOperation(databasePath, operation, args, {
  maxMainBytes,
  maxWalBytes,
  maxShmBytes,
  maxTotalBytes,
  timeoutMs,
  signal,
} = {}) {}
```

父线程用一次性 settled 状态协调 `message`、`error`、`exit`、timeout 和 AbortSignal。timeout/abort 立即调用 `worker.terminate()`，并要求返回的 termination promise 在两秒测试预算内完成。任何失败都不返回部分 rows。

- [ ] **步骤 5：运行 SQLite 探针测试**

必须使用 Node.js 24：

```bash
node --version
node --test experiments/kuai-js-feasibility/test/sqlite-snapshot.test.mjs
```

预期：版本以 `v24.` 开头；全部测试 PASS；长查询取消后进程正常退出且后续查询成功。

- [ ] **步骤 6：提交 SQLite 探针**

```bash
git add experiments/kuai-js-feasibility/src/sqlite-worker.mjs \
  experiments/kuai-js-feasibility/src/sqlite-snapshot.mjs \
  experiments/kuai-js-feasibility/test/sqlite-snapshot.test.mjs
git commit -m "test: probe Node SQLite isolation"
```

## 任务 4：建立三平台 go/no-go workflow

**文件：**
- 创建：`.github/workflows/kuai-js-feasibility.yml`
- 创建：`tests/test_kuai_js_feasibility_workflow.py`

- [ ] **步骤 1：编写 workflow 静态失败测试**

Python 测试只使用标准库读取 workflow 文本，通过精确字符串与正则合同断言：

- `pull_request`、`workflow_dispatch` 触发。
- job matrix 精确包含 `macos-26`、`windows-2025`、`ubuntu-24.04`。
- job 权限只有 `contents: read`，没有 `id-token: write`。
- `actions/setup-node@v4` 使用 Node 24。
- 运行 experiment 的 `npm test` 与 `npm run verify:policy`。
- workflow 不含 `npm install`、`npm ci`、`curl`、签名命令、`xattr`、`Unblock-File` 或 secrets。

- [ ] **步骤 2：运行测试确认 workflow 不存在**

运行：

```bash
python3 -m unittest tests.test_kuai_js_feasibility_workflow -v
```

预期：FAIL，找不到 `.github/workflows/kuai-js-feasibility.yml`。

- [ ] **步骤 3：创建最小权限 workflow**

```yaml
name: kuai-js-feasibility

on:
  pull_request:
  workflow_dispatch:

permissions:
  contents: read

jobs:
  probe:
    strategy:
      fail-fast: false
      matrix:
        os: [macos-26, windows-2025, ubuntu-24.04]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 24
      - name: Verify pure JavaScript policy
        working-directory: experiments/kuai-js-feasibility
        run: npm run verify:policy
      - name: Run security probes
        working-directory: experiments/kuai-js-feasibility
        run: npm test
```

实验包零依赖，因此 workflow 不运行 install。三平台任一失败使 workflow 失败，不能 `continue-on-error`。

- [ ] **步骤 4：运行 workflow 静态测试**

```bash
python3 -m unittest tests.test_kuai_js_feasibility_workflow -v
```

预期：PASS。

- [ ] **步骤 5：提交 workflow**

```bash
git add .github/workflows/kuai-js-feasibility.yml tests/test_kuai_js_feasibility_workflow.py
git commit -m "ci: test pure JavaScript security feasibility"
```

## 任务 5：执行三平台验证并记录裁决

**文件：**
- 创建：`docs/superpowers/reports/2026-08-03-kuai-pure-javascript-feasibility.md`
- 修改：`docs/superpowers/specs/2026-08-03-kuai-pure-javascript-npm-distribution-design.md`（仅在裁决需要纠正规格时）

- [ ] **步骤 1：运行本地可用验证**

```bash
node --version
node experiments/kuai-js-feasibility/scripts/verify-package-policy.mjs
node --test experiments/kuai-js-feasibility/test/safe-open*.test.mjs
git diff --check
```

预期：策略与当前平台文件测试 PASS。本机若不是 Node 24，不把 SQLite 未运行描述为通过；SQLite 由 workflow Node 24 结果裁决。

- [ ] **步骤 2：运行或等待 GitHub workflow**

在推送分支或 PR 后执行 `kuai-js-feasibility` workflow。记录三个 matrix job 的 run URL、commit SHA、Node 精确版本、测试通过/失败数。只有三个 job 都成功才可标 GO。

- [ ] **步骤 3：编写证据报告**

报告必须包含：目标 commit、每个平台 runner、Node 版本、每个安全 case 结果、失败日志摘要、包策略结果和固定裁决。裁决只能是：

```text
Decision: GO
```

或：

```text
Decision: NO-GO
```

报告不能写“基本可行”“暂时通过”或省略失败平台。

- [ ] **步骤 4：按裁决处理**

GO：在设计规格的迁移策略前加入“安全可行性证据”章节，链接报告和三平台 workflow run；然后再为包基础、核心运行时、adapter 批次、Skill/发布切换分别创建实现计划。

NO-GO：在设计规格决策摘要后标记纯 JavaScript 全量迁移被安全门禁阻止，链接失败报告；停止后续实现，不创建绕过失败测试的 fallback。

- [ ] **步骤 5：提交报告和规格裁决**

```bash
git add docs/superpowers/reports/2026-08-03-kuai-pure-javascript-feasibility.md \
  docs/superpowers/specs/2026-08-03-kuai-pure-javascript-npm-distribution-design.md
git commit -m "docs: record pure JavaScript feasibility decision"
```

## 完成条件

- 实验包零 runtime dependencies、零 lifecycle scripts、无项目自带原生文件。
- 三个平台实际运行相同 Node.js 24 安全探针。
- 文件读取探针确定性拒绝 symlink/junction、祖先和最终文件替换、特殊文件与超限内容。
- SQLite 探针只读、禁 extension、防御模式、BigInt 安全、WAL 可见、sidecar 有界且可在两秒内通过 Worker 终止。
- 报告包含 commit、runner、Node 版本、逐项证据和唯一 GO/NO-GO 裁决。
- 任一失败保持失败并停止迁移，不通过降低威胁模型或引入原生依赖伪造 GO。
