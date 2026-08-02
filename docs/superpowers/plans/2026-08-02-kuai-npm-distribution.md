# kuAI npm 四平台免签名分发实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 发布一个带 Agent Skill 的 `@kuai-ai/cli` 主包和四个 macOS/Windows 平台包，使用户只依赖 Node.js/npm 即可安装未签名 kuAI CLI。

**架构：** 主包注册 `kuai` Node launcher，并用精确 `optionalDependencies` 引用四个平台包；平台包只暴露 `./binary`，不注册 PATH 命令。发布脚本从已有 Go 产物生成隔离 staging、执行 tarball allowlist 验证，再按平台包 candidate → 四平台本地主包烟测 → 主包 latest → 四平台 registry 烟测的顺序发布。

**技术栈：** Node.js 24、npm、CommonJS launcher、Go 1.26.5、GitHub Actions OIDC/Trusted Publishing、MIT License。

---

## 前置条件

- 先完整执行 `docs/superpowers/plans/2026-08-02-kuai-skill-lifecycle.md`。
- `internal/skillasset/kuai/SKILL.md` 已成为 canonical Skill。
- `kuai skill install|status|upgrade|uninstall` 已通过 Go 与平台测试。
- 对已存在的五个包，npm 维护者已经逐包把 Trusted Publisher 绑定到本仓库和 `.github/workflows/kuai-npm-release.yml`；Trusted Publisher 不是 scope 级配置。没有任一绑定时，常规 publish 应失败，不回退长期 token。全新包名先执行本计划的 bootstrap runbook，由维护者在可信本机通过 2FA 完成首次发布，再逐包配置 Trusted Publisher。发布 job 使用 GitHub-hosted runner、Node.js 24 和 npm 11.5.1 或更高版本。
- GitHub 发布仓库固定为公开的 `https://github.com/YuRui-Liu/agt-mvp`，五个 manifest 的 `repository.url` 必须与它精确匹配；若仓库仍为 private，Trusted Publishing 可以工作但 provenance 契约不能满足，因此正式发布必须停止。

官方约束依据：

- npm Trusted Publishing：https://docs.npmjs.com/trusted-publishers/
- npm Staged Publishing：https://docs.npmjs.com/staged-publishing/
- GitHub macOS runner 标签：https://github.blog/changelog/2026-02-26-macos-26-is-now-generally-available-for-github-hosted-runners/
- GitHub Windows ARM runner 标签：https://github.blog/changelog/2025-08-07-arm64-hosted-runners-for-public-repositories-are-now-generally-available/

## 文件结构

- 创建 `LICENSE`：MIT 许可证，版权主体为 `kuAI contributors`。
- 创建 `npm/cli/package.template.json`：主包 manifest 模板。
- 创建 `npm/cli/bin/kuai.js`：平台选择、spawn 和退出语义。
- 创建 `npm/cli/README.md`：npm 包内安装、迁移和未签名边界。
- 创建 `npm/platform/package.template.json`：四个平台包 manifest 模板。
- 创建 `tests/npm_launcher.test.js`：Launcher 单元测试。
- 创建 `scripts/build-kuai-npm-packages.mjs`：从 `dist` 生成五个 staging 包。
- 创建 `scripts/verify-kuai-npm-packages.mjs`：manifest、allowlist、hash、权限验证。
- 创建 `tests/helpers/npm-fixtures.js`：npm packaging 测试共享 fixture 与子进程 helper。
- 创建 `tests/npm_packaging.test.js`：staging 和 tarball 测试。
- 创建 `scripts/build-kuai-npm-release.sh`：严格 SemVer 的本地候选构建入口。
- 创建 `tests/test_build_kuai_npm_release_sh.sh`：发布入口测试。
- 创建 `.github/workflows/kuai-npm-release.yml`：四个平台包 candidate、发布前烟测、主包 latest 和发布后烟测。
- 创建 `docs/runbooks/kuai-npm-bootstrap.md`：五个新包首次 2FA 引导发布与逐包 Trusted Publisher 配置。
- 创建 `docs/runbooks/kuai-npm-rollback.md`：维护者本机 2FA 回退和故障版本 deprecate 手册。
- 创建 `tests/test_npm_workflows.py`：发布顺序、runner 矩阵和无签名命令静态契约。
- 修改 `package.json`：加入 launcher、packaging 和 release 静态测试脚本，保持 `private: true`。
- 修改 `package-lock.json`：同步根测试工程 scripts/metadata，不加入运行时依赖。
- 修改 `.gitignore`：忽略 `/npm/staging/` 和 `/npm/tarballs/`。
- 修改 `README.md`：npm 成为 macOS/Windows 主安装路径；旧安装器作为兼容渠道。
- 修改 `internal/skillasset/kuai/SKILL.md`：未安装时指导 npm 安装并显式安装 Skill。
- 修改 `tests/test_docs.py`：npm、MIT、迁移和未签名边界契约。
- 修改 `tests/test_single_binary_contract.py`：npm launcher 仍只启动一个原生 `kuai`。
- 修改 `.github/workflows/kuai-mock.yml`：PR CI 加入 Node/npm 包测试。

## 任务 1：建立 MIT 许可和 npm 包模板

**文件：**
- 创建：`LICENSE`
- 创建：`npm/cli/package.template.json`
- 创建：`npm/cli/README.md`
- 创建：`npm/platform/package.template.json`
- 修改：`.gitignore`

- [ ] **步骤 1：写 manifest 模板静态失败测试**

在 `tests/npm_packaging.test.js` 创建：

```javascript
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const root = path.resolve(__dirname, '..');

test('npm templates are public MIT packages without lifecycle scripts', () => {
  for (const relative of ['npm/cli/package.template.json', 'npm/platform/package.template.json']) {
    const manifest = JSON.parse(fs.readFileSync(path.join(root, relative), 'utf8'));
    assert.equal(manifest.license, 'MIT');
    assert.equal(manifest.repository.url, 'git+https://github.com/YuRui-Liu/agt-mvp.git');
    assert.equal(manifest.publishConfig.access, 'public');
    for (const hook of ['preinstall', 'install', 'postinstall']) {
      assert.equal(manifest.scripts?.[hook], undefined);
    }
  }
});
```

- [ ] **步骤 2：运行测试确认模板不存在**

运行：`node --test tests/npm_packaging.test.js`

预期：FAIL，报错无法打开 `npm/cli/package.template.json`。

- [ ] **步骤 3：添加标准 MIT LICENSE**

`LICENSE` 使用完整 MIT 文本，版权行固定为：

```text
Copyright (c) 2026 kuAI contributors
```

- [ ] **步骤 4：创建具体模板**

`npm/cli/package.template.json`：

```json
{
  "name": "@kuai-ai/cli",
  "version": "0.0.0",
  "description": "Local-first AI coding session assessment CLI",
  "license": "MIT",
  "repository": { "type": "git", "url": "git+https://github.com/YuRui-Liu/agt-mvp.git" },
  "type": "commonjs",
  "bin": { "kuai": "bin/kuai.js" },
  "engines": { "node": ">=20.19.0" },
  "files": ["bin/kuai.js", "skill/kuai/SKILL.md", "README.md", "LICENSE"],
  "publishConfig": { "access": "public", "provenance": true }
}
```

`npm/platform/package.template.json`：

```json
{
  "name": "@kuai-ai/cli-platform",
  "version": "0.0.0",
  "description": "Platform binary for @kuai-ai/cli",
  "license": "MIT",
  "repository": { "type": "git", "url": "git+https://github.com/YuRui-Liu/agt-mvp.git" },
  "files": ["bin/kuai-native", "bin/kuai-native.exe", "README.md", "LICENSE"],
  "publishConfig": { "access": "public", "provenance": true }
}
```

模板中的 name/version/files 会由 staging 生成器收紧；模板本身不得包含 lifecycle script。

- [ ] **步骤 5：忽略生成目录并运行模板测试**

在 `.gitignore` 增加：

```gitignore
/npm/staging/
/npm/tarballs/
```

运行：`node --test tests/npm_packaging.test.js`

预期：PASS。

- [ ] **步骤 6：提交许可和模板**

```bash
git add LICENSE npm/cli/package.template.json npm/cli/README.md npm/platform/package.template.json .gitignore tests/npm_packaging.test.js
git commit -m "build: add MIT npm package templates"
```

## 任务 2：实现无 postinstall 的跨平台 Launcher

**文件：**
- 创建：`npm/cli/bin/kuai.js`
- 创建：`tests/npm_launcher.test.js`
- 修改：`package.json`
- 修改：`package-lock.json`

- [ ] **步骤 1：写平台映射和 spawn 失败测试**

```javascript
const assert = require('node:assert/strict');
const test = require('node:test');
const launcher = require('../npm/cli/bin/kuai.js');

test('selects exact platform package', () => {
  assert.equal(launcher.packageFor('darwin', 'arm64'), '@kuai-ai/cli-darwin-arm64');
  assert.equal(launcher.packageFor('darwin', 'x64'), '@kuai-ai/cli-darwin-x64');
  assert.equal(launcher.packageFor('win32', 'arm64'), '@kuai-ai/cli-win32-arm64');
  assert.equal(launcher.packageFor('win32', 'x64'), '@kuai-ai/cli-win32-x64');
  assert.throws(() => launcher.packageFor('linux', 'x64'), /unsupported platform linux-x64/);
});

test('reports an omitted optional dependency', () => {
  const messages = [];
  const code = launcher.main(['version'], {
    platform: 'darwin', arch: 'arm64', version: '1.2.3',
    resolve: () => { throw Object.assign(new Error('missing'), { code: 'MODULE_NOT_FOUND' }); },
    spawn: () => { throw new Error('must not spawn'); },
    stderr: { write: value => messages.push(value) },
  });
  assert.equal(code, 1);
  assert.match(messages.join(''), /npm install -g @kuai-ai\/cli@1\.2\.3/);
});
```

- [ ] **步骤 2：运行测试验证 launcher 不存在**

运行：`node --test tests/npm_launcher.test.js`

预期：FAIL，报错无法找到 `npm/cli/bin/kuai.js`。

- [ ] **步骤 3：实现可测试 launcher**

`npm/cli/bin/kuai.js` 必须使用：

```javascript
#!/usr/bin/env node
'use strict';

const childProcess = require('node:child_process');

const packages = Object.freeze({
  'darwin-arm64': '@kuai-ai/cli-darwin-arm64',
  'darwin-x64': '@kuai-ai/cli-darwin-x64',
  'win32-arm64': '@kuai-ai/cli-win32-arm64',
  'win32-x64': '@kuai-ai/cli-win32-x64',
});

function packageFor(platform, arch) {
  const value = packages[`${platform}-${arch}`];
  if (!value) throw new Error(`unsupported platform ${platform}-${arch}`);
  return value;
}

function main(args, runtime) {
  const packageName = packageFor(runtime.platform, runtime.arch);
  let binary;
  try { binary = runtime.resolve(`${packageName}/binary`); }
  catch {
    runtime.stderr.write(`kuai: missing ${packageName}; run npm install -g @kuai-ai/cli@${runtime.version}\n`);
    return 1;
  }
  const child = runtime.spawn(binary, args, { shell: false, stdio: 'inherit' });
  return runtime.wait(child);
}

module.exports = { packageFor, main };
```

生产入口必须使用异步 `spawn`；POSIX 至多转发一次 SIGINT/SIGTERM/SIGHUP，Windows 不模拟 POSIX signal；正常退出使用 child code，POSIX signal 退出映射为 `128 + signal number`，launcher 自身错误返回 1。

- [ ] **步骤 4：扩展测试覆盖参数、退出码和信号映射**

添加测试断言：参数数组逐项不变、`shell:false`、`stdio:'inherit'`、child code 23 返回 23、SIGTERM 返回 143、Windows Ctrl-C 不注册手工 kill handler、路径含空格仍作为单一 executable 参数传入。

- [ ] **步骤 5：把 launcher 测试加入根 npm test**

`package.json` 的 `test` 改为：

```json
"test": "node --test internal/webapp/assets/flow_logic.test.js internal/webapp/assets/flow_dom.test.js tests/session_selection_interactions.js tests/report_interactions.js tests/npm_launcher.test.js tests/npm_packaging.test.js"
```

运行 `npm install --package-lock-only --ignore-scripts --no-audit --no-fund` 更新 lockfile。

- [ ] **步骤 6：运行 Node 测试**

运行：`npm test`

预期：全部 PASS。

- [ ] **步骤 7：提交 launcher**

```bash
git add npm/cli/bin/kuai.js tests/npm_launcher.test.js package.json package-lock.json
git commit -m "feat: add kuai npm launcher"
```

## 任务 3：生成五个隔离 npm staging 包

**文件：**
- 创建：`scripts/build-kuai-npm-packages.mjs`
- 创建：`tests/helpers/npm-fixtures.js`
- 修改：`tests/npm_packaging.test.js`

- [ ] **步骤 1：先创建完整测试 helper**

`tests/helpers/npm-fixtures.js` 固定导出 `createFakeDist(t)`、`runBuilder(args)`、`readJSON(path)`、`canonicalSkill()`、`builtFixture(t)` 和 `runVerifier(out, dist)`。实现使用 `fs.mkdtempSync(path.join(os.tmpdir(), 'kuai-npm-test-'))` 创建 `dist`/`out`，在 `dist` 写入四个显式产物名；`runBuilder` 与 `runVerifier` 都用 `spawnSync(process.execPath, [absoluteScript, ...args], {encoding:'utf8', shell:false})`，并把 `status ?? 1`、stdout、stderr 原样返回；`readJSON` 与 `canonicalSkill` 分别读取 JSON 和 canonical `internal/skillasset/kuai/SKILL.md`；`builtFixture(t)` 先调用 `createFakeDist(t)`，再以 `1.2.3` 运行 builder，非零时抛出包含 stderr 的 Error。`runVerifier(out, dist)` 缺少任一参数时先抛出明确 TypeError，否则传入的参数数组固定为 `['--staging', out, '--dist', dist]`。所有临时目录由 `t.after()` 注册 `fs.rmSync(dir, {recursive:true, force:true})` 清理，因此这六个 helper 在引用前均已定义且没有隐式全局状态。

- [ ] **步骤 2：写 staging 内容测试**

```javascript
const {
  createFakeDist, runBuilder, readJSON, canonicalSkill,
} = require('./helpers/npm-fixtures.js');

test('builds five exact-version package directories', async (t) => {
  const fixture = await createFakeDist(t);
  const output = await runBuilder(['--version', '1.2.3', '--dist', fixture.dist, '--out', fixture.out]);
  assert.equal(output.code, 0, output.stderr);
  const cli = readJSON(path.join(fixture.out, 'cli/package.json'));
  assert.equal(cli.version, '1.2.3');
  assert.deepEqual(cli.optionalDependencies, {
    '@kuai-ai/cli-darwin-arm64': '1.2.3',
    '@kuai-ai/cli-darwin-x64': '1.2.3',
    '@kuai-ai/cli-win32-arm64': '1.2.3',
    '@kuai-ai/cli-win32-x64': '1.2.3',
  });
  assert.equal(fs.readFileSync(path.join(fixture.out, 'cli/skill/kuai/SKILL.md'), 'utf8'), canonicalSkill());
});
```

另写测试拒绝 `dev`、`1.2`、`v1.2.3`、缺少产物、symlink 产物、额外平台映射和 Skill frontmatter 版本不等于 `1.2.3`。

- [ ] **步骤 3：运行 staging 测试确认 builder 不存在**

运行：`node --test tests/npm_packaging.test.js`

预期：FAIL，无法执行 `scripts/build-kuai-npm-packages.mjs`。

- [ ] **步骤 4：实现显式平台映射**

```javascript
const platforms = [
  { dir: 'cli-darwin-arm64', name: '@kuai-ai/cli-darwin-arm64', os: 'darwin', cpu: 'arm64', source: 'kuai-darwin-arm64', target: 'bin/kuai-native' },
  { dir: 'cli-darwin-x64', name: '@kuai-ai/cli-darwin-x64', os: 'darwin', cpu: 'x64', source: 'kuai-darwin-amd64', target: 'bin/kuai-native' },
  { dir: 'cli-win32-arm64', name: '@kuai-ai/cli-win32-arm64', os: 'win32', cpu: 'arm64', source: 'kuai-windows-arm64.exe', target: 'bin/kuai-native.exe' },
  { dir: 'cli-win32-x64', name: '@kuai-ai/cli-win32-x64', os: 'win32', cpu: 'x64', source: 'kuai-windows-amd64.exe', target: 'bin/kuai-native.exe' },
];
```

生成器必须：规范化绝对输入；拒绝 out symlink；先写 sibling temp；复制 LICENSE、README、launcher、Skill 和四个二进制；POSIX target chmod 0755；生成 `exports:{"./binary":"./bin/kuai-native[.exe]"}`；校验 Skill version；最后原子替换 out。

- [ ] **步骤 5：运行 staging 测试**

运行：`node --test tests/npm_packaging.test.js`

预期：PASS，输出恰好五个目录。

- [ ] **步骤 6：提交 staging builder**

```bash
git add scripts/build-kuai-npm-packages.mjs tests/helpers/npm-fixtures.js tests/npm_packaging.test.js
git commit -m "build: generate kuai npm platform packages"
```

## 任务 4：验证 npm pack allowlist、hash 和权限

**文件：**
- 创建：`scripts/verify-kuai-npm-packages.mjs`
- 修改：`tests/npm_packaging.test.js`

- [ ] **步骤 1：写额外文件和 hash 漂移失败测试**

```javascript
const { builtFixture, runVerifier } = require('./helpers/npm-fixtures.js');

test('rejects files outside the package allowlist', async (t) => {
  const fixture = await builtFixture(t);
  fs.writeFileSync(path.join(fixture.out, 'cli/secret.txt'), 'no');
  const result = await runVerifier(fixture.out, fixture.dist);
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /unexpected package file package\/secret\.txt/);
});

test('rejects a platform binary that differs from dist', async (t) => {
  const fixture = await builtFixture(t);
  fs.appendFileSync(path.join(fixture.out, 'cli-darwin-arm64/bin/kuai-native'), 'changed');
  const result = await runVerifier(fixture.out, fixture.dist);
  assert.notEqual(result.code, 0);
  assert.match(result.stderr, /binary SHA-256 mismatch/);
});
```

- [ ] **步骤 2：运行验证测试并确认 verifier 不存在**

运行：`node --test tests/npm_packaging.test.js`

预期：FAIL，无法执行 `scripts/verify-kuai-npm-packages.mjs`。

- [ ] **步骤 3：实现真实 `npm pack --json` 验证**

Verifier 对五个 staging 目录分别执行：

```javascript
spawnSync('npm', ['pack', '--json', '--pack-destination', tarballDir], {
  cwd: packageDir,
  encoding: 'utf8',
  shell: false,
});
```

主包 allowlist 固定为：`package/package.json`、`package/bin/kuai.js`、`package/skill/kuai/SKILL.md`、`package/README.md`、`package/LICENSE`。平台包 allowlist 固定为 manifest、对应单个 native binary、README、LICENSE。验证 manifest name/version/os/cpu/exports、无 lifecycle scripts、无 symlink、POSIX executable mode、binary SHA-256 和 Skill SHA-256。

- [ ] **步骤 4：运行 packaging 测试**

运行：`node --test tests/npm_packaging.test.js`

预期：PASS。

- [ ] **步骤 5：提交 pack verifier**

```bash
git add scripts/verify-kuai-npm-packages.mjs tests/npm_packaging.test.js
git commit -m "test: verify kuai npm tarballs"
```

## 任务 5：创建严格 SemVer 的本地候选构建入口

**文件：**
- 创建：`scripts/build-kuai-npm-release.sh`
- 创建：`tests/test_build_kuai_npm_release_sh.sh`
- 修改：`package.json`

- [ ] **步骤 1：写 shell 入口失败测试**

测试必须覆盖：缺失 `KUAI_VERSION`、`dev`、`v1.2.3`、dirty fallback 均失败；`1.2.3` 调用现有 release build、staging builder 和 verifier；失败时保留旧 `npm/staging` 与 `npm/tarballs`。

核心断言：

```sh
KUAI_VERSION=dev run_release >/dev/null 2>&1 && fail "dev version accepted"
KUAI_VERSION=1.2.3 run_release >/dev/null
[ -f "$TEST_ROOT/repo/npm/tarballs/kuai-ai-cli-1.2.3.tgz" ]
```

- [ ] **步骤 2：运行测试确认入口不存在**

运行：`bash tests/test_build_kuai_npm_release_sh.sh`

预期：FAIL，找不到 `scripts/build-kuai-npm-release.sh`。

- [ ] **步骤 3：实现发布入口**

脚本必须 `set -eu`，只接受 npm SemVer：

```sh
case "$KUAI_VERSION" in
  ''|v*|*[!0-9A-Za-z.+-]*) echo "kuai: KUAI_VERSION must be npm SemVer" >&2; exit 1 ;;
esac
node -e 'const v=process.argv[1]; if (!/^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$/.test(v)) process.exit(1)' "$KUAI_VERSION"
```

随后运行现有 `scripts/build-kuai-release.sh`、staging builder 和 verifier。只有三步全部成功才原子替换 `npm/staging` 与 `npm/tarballs`。

- [ ] **步骤 4：加入 npm scripts 并运行测试**

`package.json` 增加：

```json
"test:npm-release": "bash tests/test_build_kuai_npm_release_sh.sh",
"build:npm-release": "bash scripts/build-kuai-npm-release.sh"
```

运行：

```bash
bash tests/test_build_kuai_npm_release_sh.sh
KUAI_VERSION=1.0.0 bash scripts/build-kuai-npm-release.sh
```

预期：PASS；生成五个 tarball，平台二进制 hash 与 `dist` 一致。

- [ ] **步骤 5：提交 release builder**

```bash
git add scripts/build-kuai-npm-release.sh tests/test_build_kuai_npm_release_sh.sh package.json package-lock.json
git commit -m "build: assemble kuai npm release"
```

## 任务 6：切换 README 和 Skill 到 npm 主安装路径

**文件：**
- 修改：`README.md`
- 修改：`npm/cli/README.md`
- 修改：`internal/skillasset/kuai/SKILL.md`
- 修改：`tests/test_docs.py`
- 修改：`tests/test_single_binary_contract.py`

- [ ] **步骤 1：先更新文档契约测试**

`tests/test_docs.py` 增加：

```python
def test_npm_is_primary_macos_windows_install(self):
    for phrase in (
        "npm install -g @kuai-ai/cli",
        "kuai skill install --target all",
        "Node.js",
        "不需要 Go",
        "未签名",
        "Gatekeeper",
        "SmartScreen",
    ):
        self.assertIn(phrase, self.readme)
        self.assertIn(phrase, self.skill)
    self.assertNotIn("postinstall", self.readme.lower())
```

并把旧测试从“README 和 Skill 都必须包含 SHA-256 安装说明”调整为“旧 install scripts 自身仍校验 SHA-256；npm 文档描述 registry integrity/provenance，不冒充代码签名”。

- [ ] **步骤 2：运行文档测试确认旧文档失败**

运行：`python3 -m unittest tests.test_docs tests.test_single_binary_contract -v`

预期：FAIL，缺少 npm 主安装命令。

- [ ] **步骤 3：更新用户文档和 Skill**

README 的 macOS/Windows 首选命令固定为：

```bash
npm install -g @kuai-ai/cli
kuai skill install --target all
kuai version
```

Skill 未安装 CLI 时先说明 npm 会安装当前平台 package，不运行 lifecycle script；取得用户同意后才运行 npm。迁移章节使用 `command -v -a kuai` 和 `where.exe kuai` 检查旧 PATH，不自动删除旧安装。明确写出 npm 降低浏览器下载摩擦但不保证绕过 EDR/Gatekeeper/SmartScreen。

- [ ] **步骤 4：运行文档契约**

运行：`python3 -m unittest tests.test_docs tests.test_single_binary_contract -v`

预期：PASS。

- [ ] **步骤 5：提交文档迁移**

```bash
git add README.md npm/cli/README.md internal/skillasset/kuai/SKILL.md tests/test_docs.py tests/test_single_binary_contract.py
git commit -m "docs: make npm the primary kuai installer"
```

## 任务 7：加入平台包先行发布、主包闸门和手工回退手册

**文件：**
- 创建：`.github/workflows/kuai-npm-release.yml`
- 创建：`docs/runbooks/kuai-npm-bootstrap.md`
- 创建：`docs/runbooks/kuai-npm-rollback.md`
- 创建：`tests/test_npm_workflows.py`
- 修改：`.github/workflows/kuai-mock.yml`

- [ ] **步骤 1：先在 PR CI 中加入本地 npm 验证**

在 `.github/workflows/kuai-mock.yml` 的 ubuntu job 加入：

```yaml
- uses: actions/setup-node@v4
  with:
    node-version: 24
    cache: npm
- run: npm install --global npm@11.5.1
- run: npm ci --ignore-scripts --no-audit --no-fund
- run: npm test
- run: bash tests/test_build_kuai_npm_release_sh.sh
```

- [ ] **步骤 2：创建 tag release 与手工 registry 验证 workflow**

workflow 触发器固定为：

```yaml
on:
  push:
    tags: ['v[0-9]+.[0-9]+.[0-9]+*']
  workflow_dispatch:
    inputs:
      verify_version:
        description: Existing exact version to smoke without publishing
        required: true
        type: string
      mode:
        description: build-artifacts, bootstrap-prepublish, or registry-postpublish
        required: true
        type: choice
        options: [build-artifacts, bootstrap-prepublish, registry-postpublish]
      artifact_run_id:
        description: Required only for bootstrap-prepublish
        required: false
        type: string
permissions:
  contents: read
concurrency:
  group: kuai-npm-release
  cancel-in-progress: false
```

`push` 事件运行完整 build/publish/smoke DAG。`workflow_dispatch` 用同一严格 SemVer 正则校验 `verify_version`：`build-artifacts` 只构建、验证并上传五个 tgz；`bootstrap-prepublish` 还必须校验纯数字 `artifact_run_id`，用只读 GitHub token 下载该 run 的同 SHA-256 主包 tgz，并在四平台执行步骤 5；`registry-postpublish` 只执行步骤 7。三个手工模式都明确跳过 publish job 且没有 OIDC 权限。

build matrix 使用真实 runner：

```yaml
include:
  - runner: macos-26
    artifact: kuai-darwin-arm64
  - runner: macos-26-intel
    artifact: kuai-darwin-amd64
  - runner: windows-11-arm
    artifact: kuai-windows-arm64.exe
  - runner: windows-2025
    artifact: kuai-windows-amd64.exe
```

每个 job 从 tag 提取无 `v` 版本，用对应 `GOOS/GOARCH` 调用 `build.sh`，原生执行 `version` 和 `status`，上传二进制与 SHA-256。不得执行 codesign、notarytool、signtool 或修改产物字节。publish-platforms 与 publish-main 两个 job 必须各自声明 job-level `permissions: {contents: read, id-token: write}` 并再次执行 `npm install --global npm@11.5.1`；其余 build/smoke job 不获得 `id-token: write`。所有 publish 都在 GitHub-hosted runner 上完成。

- [ ] **步骤 3：只先发布四个平台 candidate**

publish job 下载四个 artifact，运行 staging builder/verifier，然后按以下顺序执行：

```bash
npm publish npm/tarballs/kuai-ai-cli-darwin-arm64-1.2.3.tgz --provenance --access public --tag candidate
npm publish npm/tarballs/kuai-ai-cli-darwin-x64-1.2.3.tgz --provenance --access public --tag candidate
npm publish npm/tarballs/kuai-ai-cli-win32-arm64-1.2.3.tgz --provenance --access public --tag candidate
npm publish npm/tarballs/kuai-ai-cli-win32-x64-1.2.3.tgz --provenance --access public --tag candidate
```

workflow 实际使用从 tag 校验得到的 shell 变量替换示例版本；命令参数始终是本次生成的明确 tgz 路径，不使用 glob。主包 tgz 作为 artifact 传给烟测，但此 job 不发布主包。

- [ ] **步骤 4：等待四个平台精确版本在 registry 可见**

增加 registry-gate job。它对固定四包数组逐包执行查询；例如 darwin arm64 使用 `npm view @kuai-ai/cli-darwin-arm64@1.2.3 version` 和 `npm view @kuai-ai/cli-darwin-arm64 dist-tags.candidate`，只有两者都精确返回 `1.2.3` 才通过。每包最多尝试 12 次、每次间隔 10 秒，总等待有明确上限；命令错误或值不匹配时继续重试，超时打印包名与最后结果后失败。该 job 无 OIDC 权限，不执行 publish。

- [ ] **步骤 5：在主包发布前做四平台真实依赖烟测**

prepublish smoke matrix 使用与 build 相同四个 runner，下载本次主包 tgz。安装命令指向本地 tgz，但不携带平台 tgz；npm 因而必须从 registry 解析主包里精确版本的 `optionalDependencies`：

```bash
smoke_prefix=$(mktemp -d)
npm install -g --prefix "$smoke_prefix" ./kuai-ai-cli-1.2.3.tgz --ignore-scripts
"$smoke_prefix/bin/kuai" version
"$smoke_prefix/bin/kuai" status
"$smoke_prefix/bin/kuai" skill install --target all
"$smoke_prefix/bin/kuai" skill status --target all
```

Windows 使用 `$smokePrefix` 和 `Join-Path $smokePrefix 'kuai.cmd'`（按 npm 实际全局 prefix 布局校验路径）。安装前断言该 launcher 不存在，所有命令都使用这个绝对路径，不调用 runner PATH 上的裸 `kuai`。POSIX job 后台运行绝对路径的 `kuai start --no-browser`，等待 stdout 首行后发送 SIGTERM。Windows job 用 `Start-Process`、临时 stdout 文件和 `Stop-Process` 完成相同行为；Skill 目录、prefix 和含 token 的日志必须在 `if: always()`/`finally` 清理。

- [ ] **步骤 6：烟测通过后最后发布主包 latest**

只有四个 prepublish smoke job 全部成功，publish-main job 才使用 Trusted Publishing/OIDC 执行：

```bash
npm publish npm/tarballs/kuai-ai-cli-1.2.3.tgz --provenance --access public --tag latest
```

主包 publish job 必须依赖四个平台烟测，不得与平台包 publish 并行。workflow 中禁止 `npm dist-tag`、`npm deprecate`、`npm unpublish` 和长期 npm token secret；OIDC 只承担 `npm publish`。

- [ ] **步骤 7：发布后验证 latest 并从 registry 再做四平台烟测**

postpublish smoke matrix 必须依赖 publish-main。先以与步骤 4 相同的 12 次有界重试断言 `npm view @kuai-ai/cli dist-tags.latest` 精确等于本次版本，再使用新的干净 prefix 安装真实用户路径：

```bash
post_prefix=$(mktemp -d)
npm install -g --prefix "$post_prefix" @kuai-ai/cli@latest --ignore-scripts
"$post_prefix/bin/kuai" version
"$post_prefix/bin/kuai" status
```

随后复用步骤 5 的绝对 launcher、start、Skill 与 always/finally 清理检查。失败时 workflow 明确失败并提示执行新 patch 发布；它不尝试用 OIDC 修改 dist-tag。

- [ ] **步骤 8：编写五个新包首次发布 runbook**

`docs/runbooks/kuai-npm-bootstrap.md` 明确 Trusted Publisher 必须逐个已存在包配置，因此仅首次发布是维护者交互流程。先 dispatch `build-artifacts` 并记录 run ID 与五个 tgz 的 SHA-256；维护者下载同一 artifact，在可信本机执行 `npm login`/`npm whoami` 并逐命令完成 2FA，只先发布四个平台 tgz 到 `candidate`。因为模板默认开启 provenance，本机 bootstrap 的每条 publish 命令必须显式带 `--provenance=false`，且发布记录标注唯一例外。步骤 4 registry gate 通过后，dispatch `bootstrap-prepublish` 并传入该 run ID；四个真实 runner 必须都用 artifact 中同 SHA-256 主包 tgz通过步骤 5，维护者核对四项成功记录后才手工发布主包 `latest`。随后从 npmjs.com 的每个包 Settings 分别绑定同一 GitHub repo、`kuai-npm-release.yml` 和 `npm publish` 权限，再 dispatch `registry-postpublish` 验证四平台。runbook 禁止把登录凭据或 token 放进仓库/CI，并要求保存命令、版本、SHA-256 与 npm 返回结果作为发布记录。

bootstrap 首版是唯一允许没有 GitHub OIDC provenance 的版本，因为包存在前无法配置逐包 Trusted Publisher；README 和发布记录必须明确披露这一点。五包配置完成后，所有后续版本强制使用 Trusted Publishing 自动 provenance，任何本机常规 publish 都视为发布失败。

- [ ] **步骤 9：编写维护者 2FA 紧急回退手册**

`docs/runbooks/kuai-npm-rollback.md` 明确这不是 CI workflow，维护者必须在可信本机运行 `npm login` 并完成 2FA。用具体示例说明把故障 `1.2.4` 回退到 `1.2.3`：

```bash
npm dist-tag add @kuai-ai/cli@1.2.3 latest
npm deprecate @kuai-ai/cli@1.2.4 "Use @kuai-ai/cli@1.2.3 or a later fixed release"
```

平台包无需移动 `latest`，因为主包依赖的是精确版本。手册要求先运行 `npm whoami` 并确认对五包有维护权限，再读取 `npm view @kuai-ai/cli@1.2.3 optionalDependencies --json`，逐一用精确查询（例如 `npm view @kuai-ai/cli-darwin-arm64@1.2.3 version`）确认旧主包依赖仍存在。操作前后核对主包的 `versions`、`dist-tags` 和故障版本 `deprecated` 字段；若故障涉及平台包，也逐包核对这些字段后 deprecated 对应 `1.2.4`，然后尽快发布完整 `1.2.5` 五包集合。所有变更命令都在可信本机交互完成 2FA。禁止 `npm unpublish`，不得在仓库或 CI 保存 npm token。

`tests/test_npm_workflows.py` 必须解析 YAML 并断言四个 runner 标签、四个平台 candidate publish、registry gate 有界重试、主包只有在 prepublish smoke 后才发布 latest、postpublish smoke 安装 `@latest`、所有烟测使用独立 prefix 的绝对 launcher。逐 job 断言只有两个 publish job 有 `id-token: write`；workflow 不含 `dist-tag`/`deprecate`/token secret，且不存在 npm rollback workflow。它还断言 workflow 没有 `codesign`、`notarytool`、`signtool`、`xattr`、`Unblock-File` 或 `npm unpublish`。

- [ ] **步骤 10：运行 workflow 静态和本地测试**

运行：

```bash
npm test
bash tests/test_build_kuai_npm_release_sh.sh
python3 -m unittest tests.test_docs tests.test_single_binary_contract -v
python3 -m unittest tests.test_npm_workflows -v
git diff --check
```

预期：全部 PASS；workflow 中四个 runner、四个平台包先发、主包经过四平台闸门后最后发布的顺序可由静态测试检出。

- [ ] **步骤 11：提交 CI 发布链路和运维手册**

```bash
git add .github/workflows/kuai-mock.yml .github/workflows/kuai-npm-release.yml \
  docs/runbooks/kuai-npm-bootstrap.md docs/runbooks/kuai-npm-rollback.md tests/test_npm_workflows.py
git commit -m "ci: gate kuai npm trusted publishing"
```

## 任务 8：最终集成验证与候选演练

**文件：**
- 修改：仅修复本计划引入且被验证发现的问题，不做无关重构。

- [ ] **步骤 1：运行完整本地验证**

```bash
go test -race ./...
go vet ./...
npm ci --ignore-scripts --no-audit --no-fund
npm test
bash tests/test_kuai_install_sh.sh
bash tests/test_build_kuai_release_sh.sh
bash tests/test_build_kuai_npm_release_sh.sh
python3 -m unittest tests.test_docs tests.test_single_binary_contract -v
python3 -m unittest tests.test_npm_workflows -v
KUAI_VERSION=1.0.0 bash scripts/build-kuai-npm-release.sh
git diff --check
```

预期：全部 PASS；`npm/tarballs` 生成五个 1.0.0 tgz；没有 lifecycle script。

- [ ] **步骤 2：从本地 tgz 安装当前平台并烟测**

```bash
tmp_prefix=$(mktemp -d)
npm install -g --prefix "$tmp_prefix" --ignore-scripts --omit=optional \
  ./npm/tarballs/kuai-ai-cli-1.0.0.tgz \
  ./npm/tarballs/kuai-ai-cli-darwin-arm64-1.0.0.tgz
"$tmp_prefix/bin/kuai" version
"$tmp_prefix/bin/kuai" status
```

在 Intel macOS 使用 darwin-x64 tgz；Windows 使用对应 win32 tgz 和 `$tmpPrefix`。预期版本为 `kuai 1.0.0`，status 返回本地诊断 JSON。

- [ ] **步骤 3：检查工作区和发布内容**

运行：

```bash
git status --short
git diff --stat
npm pack --dry-run --json --prefix npm/staging/cli
```

预期：仅包含计划内源文件改动；生成目录被忽略；主包 dry-run 内容匹配 allowlist。

- [ ] **步骤 4：提交验证修复（仅在确有修复时）**

```bash
git add LICENSE .gitignore README.md package.json package-lock.json npm/cli npm/platform \
  scripts/build-kuai-npm-packages.mjs scripts/verify-kuai-npm-packages.mjs \
  scripts/build-kuai-npm-release.sh tests/npm_launcher.test.js tests/npm_packaging.test.js \
  tests/test_build_kuai_npm_release_sh.sh tests/test_docs.py tests/test_single_binary_contract.py \
  internal/skillasset/kuai/SKILL.md .github/workflows/kuai-mock.yml \
  .github/workflows/kuai-npm-release.yml docs/runbooks/kuai-npm-bootstrap.md \
  docs/runbooks/kuai-npm-rollback.md \
  tests/test_npm_workflows.py
git commit -m "test: complete kuai npm release verification"
```

若没有修复，不创建空 commit，并在交接中列出所有实际运行命令与输出结论。

## 完成条件

- `@kuai-ai/cli` 和四个平台包均为 MIT，无 lifecycle scripts。
- macOS arm64/x64、Windows arm64/x64 使用同一 npm 安装命令。
- Launcher 只通过平台包 `./binary` export 定位原生程序，并正确传递参数、退出码和终止事件。
- 主包 Skill 与 Go 内嵌 Skill 字节和版本一致。
- `npm pack` 内容、权限、hash、os/cpu、版本和精确依赖全部验证。
- 四个平台包先发布 candidate；四平台预发布烟测通过后，主包才最后发布为 latest，并再次从 registry 烟测。
- OIDC workflow 只执行 publish；紧急 dist-tag/deprecate 回退由维护者在可信本机通过 2FA 完成，不保存长期 npm token。
- 五个全新包的首次发布使用可信本机 2FA bootstrap；逐包配置 Trusted Publisher 后，后续发布不使用长期 token。
- npm integrity/provenance 不被描述为操作系统代码签名，文档明确保留 EDR/Gatekeeper/SmartScreen 风险。
