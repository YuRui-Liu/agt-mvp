# kuAI npm 免签名分发与 Agent Skill 安装设计

## 1. 目标

为 `kuai` 提供面向 macOS 和 Windows 的统一 npm 安装渠道，使终端用户只需要 Node.js/npm，不需要安装 Go 工具链，也不需要下载 `.dmg`、`.pkg` 或浏览器中的裸可执行文件。

用户安装和初始化命令为：

```bash
npm install -g @kuai-ai/cli
kuai skill install --target all
```

安装完成后，用户可以直接运行：

```bash
kuai start
```

Codex、Claude Code 和兼容 Agent 可以通过安装后的 `kuai` Skill 调用同一个 CLI。

本方案不对 macOS 或 Windows 二进制执行代码签名。npm 通过程序化下载和解包 tarball，通常不会进入浏览器下载触发的 Gatekeeper/SmartScreen 路径，但这不是 Apple 或 Microsoft 提供的免签名保证。企业 EDR、WDAC、AppLocker、Defender、Smart App Control 或更严格的 Gatekeeper 策略仍可能拦截未签名程序。产品文档必须如实保留这一边界。

## 2. 非目标

- 不承诺绕过或关闭操作系统安全机制。
- 不自动运行 `xattr`、`Unblock-File` 或修改系统安全设置。
- 不在 npm 安装阶段通过 `postinstall` 下载第二份二进制或修改用户 HOME。
- 不要求用户安装 Go、编译器、Docker 或 Python。
- 第一阶段 npm 渠道不支持 Linux；现有 Linux 安装方式保持不变。
- 不改变 `kuai` 的本地扫描、脱敏、授权和上传安全边界。

## 3. 总体架构

```text
npm registry
  ├── @kuai-ai/cli
  │     ├── bin/kuai.js
  │     ├── skill/kuai/SKILL.md
  │     └── 四个平台包的精确 optionalDependencies
  ├── @kuai-ai/cli-darwin-arm64
  ├── @kuai-ai/cli-darwin-x64
  ├── @kuai-ai/cli-win32-arm64
  └── @kuai-ai/cli-win32-x64

用户执行 kuai
  → Node launcher 检测 process.platform/process.arch
  → require.resolve 定位唯一平台包
  → shell:false 启动内部原生 kuai
  → 按平台约定传递参数、stdio、退出码和终止事件

用户执行 kuai skill install
  → 原生 kuai 读取内嵌的 canonical SKILL.md
  → 对目标目录进行预检和冲突判断
  → 原子安装 Skill 与管理标记
```

根仓库的 `package.json` 继续作为私有 Web 测试工程，不直接变为公开 npm 包。公开包源码和生成模板放在 `npm/` 下，发布时在临时 staging 目录组装 tarball，避免把仓库源码、测试、日志或凭据带入发布包。

## 4. npm 包结构

### 4.1 主包

包名固定为 `@kuai-ai/cli`，仅它注册全局命令：

```json
{
  "bin": {
    "kuai": "bin/kuai.js"
  }
}
```

主包允许的发布内容为：

```text
package.json
bin/kuai.js
skill/kuai/SKILL.md
README.md
LICENSE
```

主包通过 `optionalDependencies` 精确依赖四个平台包。所有依赖版本必须与主包完全相同，不允许使用 `^`、`~`、范围或 `latest`。

### 4.2 平台包

| npm 包 | `os` | `cpu` | 构建产物 |
| --- | --- | --- | --- |
| `@kuai-ai/cli-darwin-arm64` | `darwin` | `arm64` | `kuai-darwin-arm64` |
| `@kuai-ai/cli-darwin-x64` | `darwin` | `x64` | `kuai-darwin-amd64` |
| `@kuai-ai/cli-win32-arm64` | `win32` | `arm64` | `kuai-windows-arm64.exe` |
| `@kuai-ai/cli-win32-x64` | `win32` | `x64` | `kuai-windows-amd64.exe` |

平台包只包含 manifest、一个内部二进制、README 和 LICENSE，不声明 npm `bin`，因此不会与主包争抢 `kuai` 命令。内部文件使用不会进入 PATH 的稳定名称，例如 macOS 的 `bin/kuai-native` 和 Windows 的 `bin/kuai-native.exe`。

每个平台包必须通过 manifest 暴露稳定子路径。macOS 包使用：

```json
{
  "exports": {
    "./binary": "./bin/kuai-native"
  }
}
```

Windows 包使用：

```json
{
  "exports": {
    "./binary": "./bin/kuai-native.exe"
  }
}
```

Launcher 只通过 `require.resolve("<platform-package>/binary")` 获取路径，不加载该文件为 JavaScript，也不依赖平台包的 `main`。

npm 使用 `win32`/`x64`，Go 构建使用 `windows`/`amd64`。打包脚本必须维护显式映射，禁止通过字符串替换猜测。

### 4.3 Launcher

`bin/kuai.js` 的职责只有：

1. 根据 `process.platform` 和 `process.arch` 选择准确的平台包。
2. 使用 `require.resolve` 定位平台包导出的二进制路径，不手工拼接 `node_modules`。
3. 校验主包与平台包版本完全一致。
4. 使用 `shell: false`、继承 stdio 的异步子进程启动原生 CLI。
5. 原样传递用户参数，并按平台规则处理退出与终止。

POSIX 下，Launcher 将收到的 `SIGINT`、`SIGTERM` 和 `SIGHUP` 至多转发一次；子进程正常退出时返回其退出码，被信号终止时返回 `128 + signal number`。Windows 下，Launcher 与子进程共享控制台，不模拟 POSIX 信号；Ctrl-C 由控制台机制送达进程，子进程正常退出时返回其数值退出码。Launcher 自身的平台不支持、依赖缺失、版本错配或 spawn 失败统一返回 1，并将诊断写入 stderr。

Apple Silicon 上运行 x64 Node 时选择 x64 包；Windows ARM 上运行 x64 Node 时也选择 x64 包。这与当前 Node 进程的实际执行架构一致。

如果用户使用 `--omit=optional`、镜像缺包或平台依赖安装失败，npm 可能仍返回安装成功。Launcher 必须在首次执行时输出可行动错误，包括检测到的平台、缺失包名、主包版本以及重新安装命令，不得回退下载其他二进制。

## 5. Skill 资产与命令

### 5.1 单一来源

canonical Skill 文件放在：

```text
internal/skillasset/kuai/SKILL.md
```

该文件替代当前仓库根目录的 `kuai.md`，并同时用于：

- 通过 `go:embed` 嵌入原生 `kuai`；
- 打包时逐字节复制到主 npm 包的 `skill/kuai/SKILL.md`；
- 文档和契约测试。

构建测试必须验证嵌入内容与 npm tarball 中的 Skill 内容 SHA-256 完全一致，防止多份 Skill 漂移。

### 5.2 命令面

原生 CLI 增加以下命令：

```text
kuai skill install --target agents|claude|all|custom
kuai skill status --target agents|claude|all|custom
kuai skill upgrade --target agents|claude|all|custom
kuai skill uninstall --target agents|claude|all|custom
```

公共选项包括：

```text
--root <absolute-path>
--dry-run
--force
--allow-downgrade
```

`--root` 只允许与 `--target custom` 一起使用，语义固定为“包含各个 Skill 子目录的 skills root”；最终目标始终是 `<root>/kuai/SKILL.md`。它必须是绝对目录，规范化后不得等于文件系统根目录或用户 HOME。命令只能移动、替换或备份 `<root>/kuai` 子目录，绝不移动 `<root>` 本身。custom target 的备份只能创建为同一 root 下的 `.kuai.backup.<timestamp>`。`status`、`upgrade` 和 `uninstall` 使用完全相同的解析规则；`all` 不接受 `--root`。无 TTY 和自动化环境使用同一组非交互命令，不增加会隐式选择目标的 `setup` 命令。

### 5.3 目标目录

| target | macOS/Windows 用户目录下的目标 |
| --- | --- |
| `agents` | `~/.agents/skills/kuai/SKILL.md` |
| `claude` | `~/.claude/skills/kuai/SKILL.md` |
| `all` | 先预检以上两处，再按固定顺序分步提交并支持恢复 |
| `custom` | `<absolute-skills-root>/kuai/SKILL.md` |

Windows 使用系统路径 API 拼接目录，不手写 `/` 或 `\`。实现不自行猜测 `CODEX_HOME`、`CLAUDE_HOME` 等未在本项目中建立契约的环境变量。

### 5.4 Agent 调用

安装成功后，文档提供以下显式调用方式：

- Codex：`$kuai`
- Claude Code：`/kuai`
- 明确采用 `~/.agents/skills` 目录与当前 Skill frontmatter 格式的 Agent：按 Skill 名 `kuai` 调用
- 自然语言“运行 kuAI”作为辅助触发方式

本版本的支持矩阵仅包含 Codex/通用 Agent 约定的 `agents` target、Claude Code 的 `claude` target，以及用户显式提供的 `custom` target。未采用上述目录和格式的 Agent 不属于兼容范围。Skill 仍然只负责检查、启动和指导同一个 `kuai` CLI，不复制扫描、脱敏或上传实现。

## 6. Skill 安装安全模型

每个受管理的 Skill 目录包含 `.kuai-managed.json`，记录：

- 管理器标识；
- Skill 版本；
- `SKILL.md` 内容 SHA-256；
- 安装时间；
- 对应 CLI/npm 版本。

状态和行为如下：

| 当前状态 | 默认行为 |
| --- | --- |
| 目标不存在 | 原子安装 |
| 受管且内容与当前版本一致 | 成功 no-op，不改变 mtime |
| 受管旧版且内容未被修改 | 原子升级 |
| 受管但用户修改过 | 冲突并退出 |
| 无管理标记的文件或目录 | 冲突并退出 |
| 卸载目标不存在 | 成功 no-op |
| 卸载目标被修改或不受管 | 拒绝删除 |

`--force` 不静默覆盖。它必须先把未知或被修改的目录移动为带时间戳的备份，打印备份位置，再安装新版本。卸载时即使使用 `--force`，也必须先备份被修改的受管目录。任何情况下都不得删除 `.agents/skills` 或 `.claude/skills` 父目录。

安装流程为：

1. 校验嵌入 Skill 的 frontmatter、名称和版本。
2. 解析 target，逐级检查所有已存在路径组件，拒绝符号链接和 Windows reparse point；确认 skills root 由当前用户拥有或可由当前用户安全写入。
3. 读取管理标记和当前文件哈希，生成完整预检计划。
4. `--target all` 必须在任何写入前完成两个目标的预检。
5. 按规范化路径排序获取每个 skills root 的锁，在锁内重新校验路径组件身份，防止预检后的替换竞态。
6. POSIX 实现使用目录句柄和 no-follow/fd-relative 操作；Windows 实现使用带 `FILE_FLAG_OPEN_REPARSE_POINT` 的句柄并在替换前复核文件标识。
7. 在每个目标的同一文件系统创建 staging，写入 Skill 和管理标记。
8. `all` 在用户状态目录写入不含敏感信息的恢复日志，再按 agents、claude 的固定顺序逐个执行原子 rename/swap。普通错误触发 best-effort 逆序回滚；进程崩溃后，下一次 `kuai skill` 命令必须先根据日志恢复到旧版本或完成同一版本提交，然后才接受新操作。
9. 安装后重新读取和校验内容哈希，成功后删除恢复日志和本次 staging；启动时清理已由日志证明不再引用的陈旧 staging。

跨两个不同目录的 `all` 操作不宣称具备单文件系统事务的瞬时原子性；其保证是完整预检、每目标原子替换、可重入恢复和普通错误下的 best-effort 回滚。

退出码定义为：0 表示成功或 no-op，1 表示运行期/I/O 失败，2 表示参数错误，3 表示冲突或本地修改。

## 7. 安装与迁移体验

新用户执行：

```bash
npm install -g @kuai-ai/cli
kuai skill install --target all
kuai version
```

临时使用可以执行：

```bash
npx @kuai-ai/cli@latest status
```

从旧 `install.sh` 或 `install.ps1` 渠道迁移时，文档先要求用户检查 PATH 中所有 `kuai`：

```bash
command -v -a kuai
```

Windows 使用：

```powershell
where.exe kuai
```

如果旧二进制排在 npm launcher 之前，用户需要显式卸载旧版本或调整 PATH。npm 安装过程不得自动删除旧文件、修改旧安装目录或覆盖用户 PATH 顺序。

现有 `install.sh`、`install.ps1` 和裸 Release 继续作为兼容渠道，但 README 的主推荐路径切换为 npm。npm launcher 不调用旧安装器。

## 8. 版本与发布流程

发布版本唯一来源是受保护 tag：

```text
vX.Y.Z[-prerelease]
```

流水线提取 npm SemVer，并将完全相同的版本写入：

- 主包 manifest；
- 四个平台包 manifest；
- 四个精确 optional dependency；
- Go `KUAI_VERSION`；
- Skill frontmatter；
- 管理标记。

正式 npm 发布禁止使用 `git describe` fallback、`dev`、dirty 版本或版本范围。

发布步骤为：

1. 运行 Go、Node、安装器、文档和安全测试。
2. 构建四个未签名的 macOS/Windows 原生二进制。
3. 在真实目标机器执行 `kuai version` 和 `kuai status`。
4. 生成五个 npm staging 包并运行 `npm pack --json`。
5. 将 tarball 内容与固定 allowlist 精确比较，禁止额外文件、符号链接、源码、测试、日志、证书、token 和仓库 `.npmrc`。
6. 使用 npm Trusted Publishing/OIDC、provenance、最小权限和发布环境保护，将四个平台包发布到 `candidate` tag。发布 job 固定使用 GitHub-hosted runner、Node.js 24 和 npm 11.5.1 或更高版本。
7. 确认四个平台包的精确版本在 registry 可见；此时仍不发布主包。
8. 在干净的 macOS arm64、macOS x64、Windows arm64 和 Windows x64 环境安装本次本地主包 tarball。主包的精确 `optionalDependencies` 从 registry 取得对应平台包，由此在主包进入公开 registry 前验证真实依赖解析。
9. 验证 launcher、版本、status、start、Skill 安装/status/升级/卸载。
10. 全部通过后才使用同一个 Trusted Publishing/OIDC workflow 将主包直接发布到默认 `latest`；随后从 registry 再执行一次四平台安装烟测。

五个新 npm 包统一采用 MIT License，仓库根 `LICENSE` 使用标准 MIT 全文及 `Copyright (c) 2026 kuAI contributors`；每个 tarball 都必须包含该文件，manifest 的 `license` 固定为 `MIT`。公共发布前还必须确认 `@kuai-ai` scope 的发布权限；缺少 LICENSE、manifest 声明或 scope 权限中的任一项时，流水线必须在 `npm publish` 前失败。

## 9. 发布失败与回滚

- 任一平台包发布失败：停止，不发布主包。
- 主包发布前烟测失败：停止，不发布主包；已存在的平台候选版本保持不可变，后续修复使用新的 patch 版本。
- 主包发布后 registry 烟测失败：优先立即发布修复后的新 patch。需要紧急回退时，由 npm 维护者在本机登录并通过 2FA 手工把主包 `latest` 指回上一已知良好版本，再 deprecated 故障版本；该操作不放入只具备 OIDC Trusted Publishing 的 CI。
- npm 已发布版本不可覆盖；修复必须使用新的 patch 版本。
- 即使只有一个平台损坏，也使用同一个新的 patch 版本重新发布一整套五个包，保持版本集合一致；绝不覆盖故障旧版本。

npm Trusted Publishing 只用于 `npm publish`（以及可选的 `npm stage publish`），不得假设 OIDC 身份同时授权 `npm dist-tag` 或 `npm deprecate`。后续版本可选择 staged publishing，但它要求包已存在且由维护者使用 2FA 批准，因此首版仍使用上述“平台包先发、主包最后发”的引导流程。

由于 Trusted Publisher 是逐包在 npm 包设置中配置的，新名称首次发布不能预先依赖该配置。五个包的首次引导发布由维护者在可信本机登录、逐次完成 2FA，并严格复用相同的 tarball 验证和“平台包先发、主包最后发”顺序；发布后立即为五包分别绑定 Trusted Publisher，后续版本才走无长期 token 的 OIDC workflow。不得为引导发布把长期 npm token 写入 CI。

## 10. 测试策略

### 10.1 npm 包与 Launcher

- 五个 manifest 版本完全一致，optionalDependencies 使用精确版本。
- `os`/`cpu` 与 Go 目标映射正确。
- 所有公开包都不存在 lifecycle script。
- `npm pack --json` 内容严格匹配 allowlist。
- Unix 二进制保留可执行权限，所有 tarball 不含符号链接。
- 覆盖参数、stdin/stdout/stderr、退出码、信号、路径含空格、缺包、版本错配和未知平台。
- 全局安装、本地安装、`npx` 和 `--ignore-scripts` 均可运行。
- `--omit=optional` 后首次运行给出预期诊断。

### 10.2 Skill 生命周期

- agents、claude、all，以及通过 `--target custom --root <absolute-skills-root>` 指定的自定义 skills root。
- 首次安装、重复安装、升级、降级拒绝、允许降级。
- 受管修改冲突、无标记冲突、强制备份。
- 卸载当前版本、不存在目标、修改后拒绝、未知目录拒绝。
- staging 写入、rename、标记写入和第二目标失败时的 best-effort 回滚，以及模拟崩溃后的日志恢复。
- 并发锁、只读目录、磁盘不足、父路径替换竞态、POSIX symlink 和 Windows reparse point。
- 嵌入 Skill 与 npm tarball Skill 的内容哈希一致。

### 10.3 原生验收

四个目标必须在真实平台执行，交叉编译或模拟 `os/cpu` 不能替代原生烟测：

```text
npm install -g @kuai-ai/cli@1.2.3
kuai version
kuai status
kuai skill install --target all
kuai skill status --target all
kuai start --no-browser
```

测试记录必须明确报告 Gatekeeper、SmartScreen、Defender 和企业策略的实际表现。npm integrity 和 provenance 只证明 tarball 来源与内容，不能被描述为操作系统代码签名。

## 11. 验收标准

- macOS arm64/x64、Windows arm64/x64 可用同一 npm 命令安装。
- 用户机器不需要 Go 工具链，npm 安装不执行 lifecycle script。
- `kuai` launcher 只启动准确平台、准确版本的内部二进制。
- 主包包含合法 `skill/kuai/SKILL.md`，且与 Go 内嵌内容逐字节一致。
- Skill 安装显式、幂等、可升级、可回滚，不覆盖未知或用户修改的文件。
- Codex、Claude Code，以及明确采用 `~/.agents/skills` 与本 Skill 格式的 Agent 可调用安装后的 `kuai` Skill。
- 五个包通过 tarball allowlist、版本一致性、registry 安装和四平台原生烟测。
- 未签名限制在 README、Skill 和发布说明中被准确披露，不承诺完全绕过系统安全策略。
