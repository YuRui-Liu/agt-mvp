# kuAI 纯 JavaScript npm 分发与 Agent Skill 设计

## 1. 决策摘要

> **2026-08-03 状态：已批准调整威胁模型并继续。** [可行性报告](../reports/2026-08-03-kuai-pure-javascript-feasibility.md) 的 `Decision: NO-GO` 仅对“完整保持 Go 强对抗安全边界”有效。用户已明确接受不防御同权限恶意进程的精确路径竞态，并批准使用独立 Node 子进程执行 SQLite 只读扫描。后续安全与 SQLite 合同以 [本地会话扫描设计](2026-08-03-kuai-pure-javascript-local-session-scan-design.md) 为准。

kuAI 将从随包分发 Go 原生二进制，迁移为由 Node.js 24 或更高版本直接执行的纯 JavaScript npm CLI。公开安装渠道为公共 npm registry；GitHub 保存源码、受保护 tag 和 GitHub Actions 发布流水线，并通过 npm OIDC Trusted Publishing 发布带 provenance 的包。

本设计取代 `2026-08-02-kuai-npm-unsigned-distribution-design.md` 中“JavaScript launcher 启动平台原生包”的方案。新包不得包含或下载 Mach-O、PE、ELF、`.node` 原生扩展或 Go 可执行文件，也不得在安装或运行时回退到原生 kuAI。

用户使用一条显式命令安装 CLI 与 Agent Skill：

```bash
npm install -g @kuai-ai/cli && kuai skill install --target all
```

npm 安装阶段不通过 `postinstall` 写入用户 HOME。Skill 安装是用户可见、可审计、可单独升级和卸载的显式操作。

## 2. 目标

- 以一个跨平台 `@kuai-ai/cli` 包支持 macOS、Windows 和 Linux。
- 不为 kuAI 产物申请或依赖 Apple Developer ID、notarization 或 Windows Authenticode。
- 消除 kuAI 随包原生程序触发 Gatekeeper/SmartScreen 原生发布者检查的问题。
- 保持当前 Go 客户端的 session 发现、Assessment Scope 聚合、本地脱敏、流式导出、loopback Web UI、授权和上传安全边界。
- 将 canonical Skill 与 CLI 放入同一 npm 包，以受管生命周期安装到 Agent 目录。
- 使用 GitHub Actions、npm Trusted Publishing、provenance、不可变 tag 和 tarball 内容审计形成可追踪发布链。
- 在 README 中提供 Node.js 24 安装、CLI/Skill 安装、升级、卸载和安全边界说明。

## 3. 非目标与安全承诺

- 不承诺绕过、关闭或修改 Gatekeeper、XProtect、SmartScreen、EDR、MDM 或企业 npm 策略。
- 不自动运行 `xattr -d`、`spctl --master-disable`、`Unblock-File` 或同类命令。
- 不把 npm provenance 描述为操作系统代码签名。
- 不使用 `curl | sh` 作为 README 首选安装方式。
- 不在 `preinstall`、`install`、`postinstall`、`prepare` 等 npm lifecycle 阶段下载代码、执行安装器或修改用户 HOME。
- 不在 npm 包中附带平台子包、原生 launcher、`.node` 扩展或安装后下载器。
- 不保证企业受管设备一定允许 Node、npm 或用户目录脚本运行。

产品可以准确声明：kuAI npm 包不携带项目自身的原生 macOS/Windows 可执行代码，因此 kuAI 包本身不需要 Apple Developer ID 或 Authenticode。产品不得声明“绝不会被安全软件拦截”。

## 4. 运行时与包结构

### 4.1 Node.js 基线

最低运行时为 Node.js 24：

```json
{
  "engines": {
    "node": ">=24"
  }
}
```

`bin/kuai.js` 在加载业务模块前检查实际 Node major version。版本不足时退出 1，并输出当前版本、最低版本和 README 中的平台安装命令。不能依赖 npm 的 `engines` 警告作为唯一门禁。

### 4.2 单包结构

```text
@kuai-ai/cli
├── bin/kuai.js
├── dist/
│   ├── cli/
│   ├── sources/
│   ├── filesystem/
│   ├── sqlite/
│   ├── scope/
│   ├── redaction/
│   ├── export/
│   ├── service/
│   ├── web/
│   └── skill/
├── web/
├── skill/kuai/SKILL.md
├── package.json
├── README.md
└── LICENSE
```

包名固定为 `@kuai-ai/cli`，只注册一个全局命令：

```json
{
  "bin": {
    "kuai": "bin/kuai.js"
  }
}
```

TypeScript 是开发语言，发布包只包含编译后的 ESM JavaScript、静态 Web 资源、canonical Skill 和文档。根仓库可以继续保留 Go 代码作为迁移期行为基准，但 Go 源码和产物不进入 npm tarball。

### 4.3 模块边界

- `cli`：参数解析、命令分派、稳定退出码和结构化输出。
- `sources`：各 Agent 的只读发现、格式签名和 session 解析。
- `filesystem`：路径规范化、no-follow 检查、文件大小上限和安全读取。
- `sqlite`：使用 Node.js 24 自带 `node:sqlite`；`DatabaseSync` 必须设置 `readOnly: true`、保持 `allowExtension: false` 与 `defensive: true`，并按输入预算设置运行时 limits。不得安装第三方原生 SQLite 扩展。
- `scope`：Project、Workspace、Conversation Group、Session Collection 聚合。
- `redaction`：本地脱敏、限额和安全 label。
- `export`：有界内存的流式导出。
- `service`：使用 Node 内置 `fetch` 与 HR-B 通信。
- `web`：使用 `node:http` 启动只绑定 loopback、受启动 token 保护的本地服务。
- `skill`：canonical Skill 校验、受管安装、升级、状态和卸载。

打开浏览器可以调用操作系统已有命令，但 npm 包不得携带辅助可执行文件。可选 WASM 只有在明确 allowlist、固定哈希、包内静态携带且没有原生 fallback 时才允许；第一阶段不依赖运行时下载 WASM。

## 5. CLI 行为兼容

纯 JavaScript CLI 保持当前用户命令和安全语义：

```text
kuai start
kuai scan
kuai status
kuai version
kuai skill install|status|upgrade|uninstall
```

迁移期间先固定 Go 版本的命令输出、JSON schema、退出码、source fixtures 和授权边界。每个 JavaScript 模块必须通过同 fixture 对照测试后才能替代对应 Go 能力。未知、损坏、截断或超限 source 返回 detected/unsupported 或明确错误，不能宽松猜测格式。

CLI 不得因为 JavaScript 能力缺失而静默执行 Go 二进制、下载原生模块或调用远程扫描服务。无法满足安全边界的功能必须 fail-closed。

## 6. Skill 生命周期与 Agent 调用

### 6.1 Canonical 资产

唯一 canonical Skill 位于 npm 包：

```text
skill/kuai/SKILL.md
```

构建和安装测试验证 canonical 文件与目标目录中的内容逐字节一致。Skill 版本与 npm 包版本来自同一个受保护 tag。

### 6.2 显式安装

README 的首选 POSIX 安装命令是：

```bash
npm install -g @kuai-ai/cli && kuai skill install --target all
```

PowerShell 使用等价的显式成功门禁：

```powershell
npm install -g @kuai-ai/cli; if ($LASTEXITCODE -eq 0) { kuai skill install --target all }
```

不使用默认或可选 `postinstall` 自动写 HOME。CLI 安装与 Skill 安装保持两个可审计事务，但文档提供一条可复制命令减少操作负担。

### 6.3 目标与管理标记

```text
agents  → ~/.agents/skills/kuai/SKILL.md
claude  → ~/.claude/skills/kuai/SKILL.md
all     → 以上两处
custom  → <absolute-skills-root>/kuai/SKILL.md
```

每个受管目录包含 `.kuai-managed.json`，记录管理器、CLI/Skill 版本、`SKILL.md` SHA-256 和安装时间。

- 目标不存在：同文件系统 staging 后原子安装。
- 内容和版本一致：成功 no-op，不改变 mtime。
- 旧版且未被用户修改：显式 `upgrade` 原子升级。
- 用户修改过或目标不受管：拒绝覆盖，退出 3。
- `--force`：先创建同级时间戳备份，再安装；不能直接删除未知内容。
- `all`：写入前完成两处完整预检；普通错误执行逆序 best-effort 回滚，并用恢复日志处理进程中断。
- 卸载：只删除未修改的受管目录；未知或修改内容必须先备份。

npm 卸载不自动删除 Skill，Skill 卸载也不卸载 npm 包。残留 Skill 在 CLI 不可用时提供修复命令，不尝试自行联网安装。

### 6.4 Agent 协议

Skill 只描述如何调用 PATH 中的 `kuai`：

1. 先检查 `kuai` 与 Node 版本。
2. 默认运行只读 `kuai status`。
3. 只有用户明确要求时才运行 `kuai start`。
4. 不在 Skill 中复制扫描、脱敏、授权或上传实现。
5. 不向对话、工单或共享日志输出启动 token、绝对路径或敏感 session 内容。
6. CLI 与 Skill 版本不一致时提示 `kuai skill upgrade --target all`。

支持调用约定：Codex 使用 `$kuai`，Claude Code 使用 `/kuai`；明确采用 `~/.agents/skills` 与兼容 frontmatter 的 Agent 按 Skill 名 `kuai` 调用。

## 7. README 信息架构

README 首屏按以下顺序编排：

1. 产品用途与本地安全边界。
2. Node.js 24 前置检查。
3. 各平台 Node.js 安装命令。
4. 一行 CLI + Skill 安装命令。
5. `kuai version`、Skill 状态和 `kuai start` 验证。
6. Agent 调用方式。
7. 升级、卸载与故障排查。
8. 纯 JavaScript分发与系统安全边界。

最低要求示例：

```bash
node --version
```

macOS 首选 Homebrew：

```bash
brew install node@24
```

Windows 首选 winget：

```powershell
winget install OpenJS.NodeJS.LTS
```

Linux 在已安装 nvm 时使用：

```bash
nvm install 24 && nvm alias default 24
```

在支持 Snap 的 Linux 上提供不执行远程 shell 的备选：

```bash
sudo snap install node --classic --channel=24
```

README 同时链接 Node.js 官方下载页，供没有上述包管理器的用户选择对应平台安装包。Node 安装命令必须在发布 CI 的目标环境验证；如果包管理器标识变化，文档测试应阻止发布并要求更新。

README 必须明确：

- kuAI 不携带项目自身的原生可执行程序，因此无需为 kuAI npm 产物做 Apple/Windows 代码签名。
- 用户安装和信任的 Node.js 运行时不属于 kuAI npm 包，必须来自可信渠道。
- npm integrity/provenance 不等于 Apple 或 Windows 代码签名。
- 普通设备预期可直接运行，但企业 EDR、MDM、Node/npm 策略仍可能限制。
- 不建议关闭系统安全机制或自动清除 quarantine。
- `npm uninstall -g @kuai-ai/cli` 与 `kuai skill uninstall --target all` 是两个显式操作。

## 8. 发布链路

### 8.1 版本来源

正式发布只接受受保护 tag：

```text
vX.Y.Z[-prerelease]
```

tag 提取出的 SemVer 同时写入 npm manifest、CLI `version` 输出、Skill frontmatter 和管理标记。正式发布禁止 `dev`、dirty 版本、版本范围和 `latest` 依赖。

### 8.2 GitHub Actions

流水线顺序：

1. 在 Node.js 24 的 macOS、Windows、Linux runner 运行单元、契约和安全测试。
2. 编译 TypeScript 并创建隔离 staging。
3. `npm pack --json` 生成 tarball。
4. 将 tarball 条目与固定 allowlist 精确比较。
5. 扫描 tarball 和完整生产依赖树，拒绝 Mach-O、PE、ELF、`.node`、额外可执行文件和 lifecycle script。
6. 在三平台从 tarball 进行干净全局安装，运行 CLI 与 Skill smoke tests。
7. 将唯一的最终版本 tarball 作为 CI artifact，在 macOS、Windows、Linux 上直接从该 tarball 安装并执行 smoke tests。
8. 只有 tarball gate 全通过后，publish job 才下载同一 artifact，使用最小权限和 OIDC Trusted Publishing 一次性发布为 `latest`，并启用 provenance。
9. 发布后从公共 registry 安装该不可变版本做验收 smoke；验收失败时立即 deprecate 该版本并停止后续发布，不尝试重发同一版本。

仓库和 CI 不保存长期 npm token。只有 publish job 获得 `id-token: write`；测试、构建和 smoke jobs 只有 `contents: read`。

## 9. 错误处理

- Node major 小于 24：退出 1，打印检测版本、最低版本和安装指导。
- Node 自带 SQLite 能力不可用：退出 1，不安装或下载 fallback。
- 参数错误：退出 2。
- Skill 冲突或本地修改：退出 3。
- source 不支持：保留只读诊断，不尝试格式猜测或修复原文件。
- Agent PATH 中无 `kuai`：Skill 输出 npm 安装命令，不运行 `npx` 隐式下载。
- 本地 Web 服务无法安全绑定 loopback 或生成 token：拒绝启动。
- 上传前验证、授权或脱敏失败：不发送任何 payload。

所有用户错误必须给出可执行的下一步，同时避免输出 token、凭据、绝对路径或原始 session 内容。

## 10. 迁移策略

迁移不是一次性替换：

1. 固定 Go CLI 合同、fixtures、JSON schema、退出码和安全测试。
2. 建立纯 JavaScript CLI 骨架和无原生依赖门禁。
3. 迁移 filesystem、SQLite、Scope、redaction、export、service 和 web 基础模块。
4. 按 source adapter 逐个完成 Go/JavaScript fixture parity。
5. 完成 Skill 生命周期与 Agent 协议。
6. 对唯一最终 tarball 在真实目标系统执行安装与运行验证。
7. 完成 `start`、`status`、上传授权和全部 ready source 后，通过 OIDC 将已验证的同一 tarball 直接发布为 `latest`。
8. npm 渠道稳定后，将 Go 分发标记为 legacy；它不能成为 JavaScript CLI 的运行时回退。

迁移期可以并存 Go 与 TypeScript 测试工程，但公开 npm tarball 始终只包含纯 JavaScript产物。

## 11. 验收与测试

### 11.1 功能与合同

- TypeScript 单元测试和 CLI 命令合同测试。
- 所有当前 ready source fixtures 的发现、解析和 Scope 对照测试。
- Go/JavaScript 对相同 fixture 的结构化输出一致性测试。
- `start`、`scan`、`status`、`version` 和上传授权端到端测试。
- Web UI 静态资源、loopback 绑定和 token 验证测试。

### 11.2 文件与数据安全

- symlink、Windows reparse point、路径穿越、竞态替换和超限输入测试。
- SQLite 只读打开、损坏数据库和锁定数据库测试。
- 脱敏 fixture、无原始敏感内容泄漏和有界内存导出测试。
- 未经用户选择和授权时零上传测试。

### 11.3 Skill

- `install`、no-op、`status`、`upgrade`、`uninstall`、`--dry-run` 和 `--force` 备份测试。
- agents、claude、all、custom 目标测试。
- 用户修改、未知目录、symlink/reparse point 和中断恢复测试。
- canonical Skill 与安装内容 SHA-256 一致性测试。
- CLI 缺失、Node 版本不足和版本错配的 Agent 指导测试。

### 11.4 包与发布

- tarball 固定 allowlist。
- tarball 和生产依赖树无 Mach-O、PE、ELF、`.node` 与 lifecycle scripts。
- macOS、Windows、Linux 的最终 tarball 安装和发布后 `latest` 安装 smoke tests。
- GitHub Actions 权限、OIDC、provenance、tarball gate 和一次性发布顺序静态测试。
- README 中 Node、CLI、Skill、升级和卸载命令的自动验证。

## 12. 完成标准

只有同时满足以下条件，纯 JavaScript npm 渠道才可替代当前 Go 公开分发：

- 当前面向用户的 CLI 功能和安全边界已迁移。
- 所有 ready source 通过 Go/JavaScript fixture parity。
- Agent 能通过受管 Skill 调用全局 `kuai`。
- npm 包和完整生产依赖树中不存在项目自带原生程序或安装后下载器。
- 三平台最终 tarball 安装 smoke tests 通过，并完成发布后 `latest` 验收。
- README 提供经过验证的 Node.js 24、CLI 与 Skill 安装命令及准确安全边界。
- 发布使用受保护 tag、最小权限 OIDC Trusted Publishing 和 provenance。

企业安全软件仍可能限制 Node/npm 的事实不阻止本设计完成，但必须在 README 和发布说明中持续披露。
