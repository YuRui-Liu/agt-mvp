# kuAI 单文件本地客户端

kuAI 以一个单一 Go 可执行文件 `kuai` 提供本地 session 扫描、Assessment Scope 聚合、流式导出、本地脱敏和回环 Web UI。客户端不查找或运行第二个二进制。Mock 模式不访问公网；正式 HTTP 模式只在用户完成验证与授权后，将脱敏包交给 HR-B，分析在服务端进行。

## 安装与启动

从可信来源检出本仓库后，macOS/Linux 使用统一安装器的一条命令：

```bash
sh ./install.sh
```

安装器通过 HTTPS 下载当前平台的一个 `kuai` 产物和 `SHA256SUMS`，完成 SHA-256 校验后原子安装到用户级 bin 目录。Windows 使用可信仓库根目录中的 `install.ps1`，行为相同。先下载并审查脚本再执行；不要使用 `curl | sh`、不受信任镜像或绕过校验。`KUAI_INSTALL_DRY_RUN=1 sh ./install.sh` 可只查看将下载和安装的路径。

安装后运行准确命令：

```bash
kuai start
```

`kuai start` 扫描本机会话并自动打开受启动 token 保护的本地页面。不要把包含 token 的完整 URL 粘贴到聊天、工单或共享日志。页面不默认选择或上传任何内容；用户必须主动选择一个 Assessment Scope。

## Skill 方式

必须先进入可信仓库根目录，再把 [`kuai.md`](kuai.md) 复制为独立的 `<skills-root>/kuai/SKILL.md`。复制前确认目标路径和已有文件，不要覆盖仓库根目录的 `SKILL.md`。

macOS/Linux 上，Codex 或通用 Agent 使用：

```bash
mkdir -p "$HOME/.agents/skills/kuai"
test ! -e "$HOME/.agents/skills/kuai/SKILL.md" || { echo "目标 SKILL.md 已存在，请先确认" >&2; exit 1; }
cp ./kuai.md "$HOME/.agents/skills/kuai/SKILL.md"
```

Claude Code 使用：

```bash
mkdir -p "$HOME/.claude/skills/kuai"
test ! -e "$HOME/.claude/skills/kuai/SKILL.md" || { echo "目标 SKILL.md 已存在，请先确认" >&2; exit 1; }
cp ./kuai.md "$HOME/.claude/skills/kuai/SKILL.md"
```

Windows PowerShell 上，Codex 或通用 Agent 使用：

```powershell
New-Item -ItemType Directory -Force "$HOME\.agents\skills\kuai"
if (Test-Path -LiteralPath "$HOME\.agents\skills\kuai\SKILL.md") { throw "目标 SKILL.md 已存在，请先确认" }
Copy-Item -LiteralPath ".\kuai.md" -Destination "$HOME\.agents\skills\kuai\SKILL.md"
```

Claude Code 使用：

```powershell
New-Item -ItemType Directory -Force "$HOME\.claude\skills\kuai"
if (Test-Path -LiteralPath "$HOME\.claude\skills\kuai\SKILL.md") { throw "目标 SKILL.md 已存在，请先确认" }
Copy-Item -LiteralPath ".\kuai.md" -Destination "$HOME\.claude\skills\kuai\SKILL.md"
```

之后可说“运行 kuAI”“扫描 Agent 项目”或“生成分析海报”。skill 只检查并调用同一个统一安装器和 `kuai` 客户端，不另写扫描器、脱敏器或上传器。

## Assessment Scope 与支持范围

Scope 归组优先级为 Project → Workspace → Conversation Group → Session Collection。同一项目可聚合多个 Agent 的 session；页面只显示安全 label、稳定哈希 key、来源和计数，不暴露绝对路径或本地用户名，并且不默认选择任何 Scope。

下表中的 capabilities 使用 `M`（messages）、`T`（tools）、`R`（reasoning）缩写。默认位置均相对用户目录或对应平台的应用数据目录，不展示真实本地路径。

| Product / 展示名 | 默认存储与已验证格式 | Scope / Capabilities | Verification |
| --- | --- | --- | --- |
| `aider` / aider | `.aider.chat.history.md`；`markdown-v1` | project 或 session collection / M | `machine_verified` |
| `claude-code` / Claude Code | `.claude/projects`；`jsonl` | project 或 session collection / M,T | `machine_verified` |
| `cline` / Cline | `.cline/data/sessions`；成对 JSON `v1` | project 或 session collection / M,T,R | `machine_verified` |
| `codebuddy-cli` / CodeBuddy CLI | `.codebuddy/projects`；`jsonl-v1` | project 或 session collection / M,T,R | `machine_verified` |
| `codeflicker` / CodeFlicker | SQLite `sqlite`；兼容项目 JSONL `jsonl` | project 或 session collection / M,T | `machine_verified` |
| `codex` / Codex | `.codex/sessions`、`.codex/archived_sessions`；`jsonl` | project 或 session collection / M,T | `machine_verified` |
| `copilot-cli` / GitHub Copilot CLI | `.copilot/session-state`；`flat-v1`、`directory-v2` | project 或 session collection / M,T,R | `fixture_verified` |
| `cursor` / Cursor | `.cursor/projects/*/agent-transcripts`；`jsonl`、`txt` | project 或 session collection / M,T | `machine_verified` |
| `gemini-cli` / Gemini CLI | `.gemini/tmp/*/chats`；`object-v1`、`stream-v1` | project 或 session collection / M,T,R | `fixture_verified` |
| `hermes-agent` / Hermes Agent | `.hermes/state.db` 与 `.hermes/sessions`；`state-db-v1`、`jsonl-v1`、`json-v1` | conversation group / M,T | `machine_verified` |
| `kimi-cli` / Kimi CLI | `.kimi/sessions/*/*/wire.jsonl`；`wire-v1` | session collection / M,T | `machine_verified` |
| `kimi-code` / Kimi Code | `.kimi-code` 的 index + state + main wire；`wire-1.4` | project / M,T,R | `machine_verified` |
| `myflicker` / MyFlicker | `.myflicker/projects`；`jsonl` | project 或 session collection / M,T | `machine_verified` |
| `openclaw` / OpenClaw | `.openclaw/agents/*/sessions`；`jsonl-v1` | project 或 session collection / M,T | `machine_verified` |
| `opencode` / OpenCode | 平台数据目录的 `opencode.db` 或 `storage`；`db-v2`、`storage-v1` | project 或 session collection / M,T | `machine_verified` |
| `qoder-cli` / Qoder CLI | `.qoder/projects/*/transcript`；`transcript-v1` | project / M,R | `machine_verified` |
| `qoder-ide` / Qoder IDE | `Qoder/SharedClientCache/cache/db/local.db`；`sharedclient-db-v1` | project / M | `machine_verified` |
| `qwen-code` / Qwen Code | `.qwen/projects/*/chats`；`chat-jsonl-v1` | project 或 session collection / M,T | `machine_verified` |
| `tongyi-lingma-cli` / 通义灵码 CLI | `Lingma/SharedClientCache/cli/projects`；`execution-v1` | project / M | `machine_verified` |
| `tongyi-lingma-ide` / 通义灵码 IDE | `Lingma/SharedClientCache/cache/db/local.db`；`sharedclient-db-v1` | project / M | `machine_verified` |
| `vscode-copilot` / GitHub Copilot for VS Code | VS Code `User/workspaceStorage`；schema `v3` | workspace 或 conversation group / M,T | `machine_verified` |
| `workbuddy` / WorkBuddy | `.workbuddy-ai/projects`、`.workbuddy/projects`；`jsonl-v1` | project 或 session collection / M,T | `machine_verified` |

仅检测、不可选择的来源没有默认扫描目录和 capabilities：

| Product / 展示名 | Verification | Unsupported reason |
| --- | --- | --- |
| `trae` / TRAE | `export_required` | `official_export_required` |
| `trae-work` / TRAE Work | `unsupported` | `no_distinct_local_format` |
| `kimi-work` / Kimi Work | `unsupported` | `no_verified_session_schema` |
| `qoder-work` / QoderWork | `unsupported` | `no_distinct_local_format` |
| `codebuddy-ide` / CodeBuddy IDE | `unsupported` | `no_verified_transcript_body` |
| `kiro` / Kiro | `unsupported` | `no_verified_session_schema` |

`ready` 不等于本机可选择：来源必须在本次只读扫描中实际得到 ready session，且 `sessionCount > 0`，才能成为 selectable Assessment Scope。“检测到”也不等于“可评估”。只有具备版本化脱敏 fixture、格式签名、只读发现与打开测试、损坏和超限回归测试的来源才能上传。

`copilot-cli` 不等于 `vscode-copilot`：前者读取 Copilot CLI 的 session-state，后者只接受带 GitHub Copilot provenance 的 VS Code v3 会话。`kimi-cli` 不等于 `kimi-code`：前者读取 `.kimi/sessions` 的 `wire-v1`，后者要求 `.kimi-code` 的 index、state 与 `wire-1.4` 共同互证。

## 隐私与 HR-B 边界

- 用户选择 Scope 后，`kuai` 在本地流式解析，删除禁止字段、Read 正文、附件、二进制和完整文件副本，再递归处理 secret、绝对路径、用户名与 PII。
- 用户必须查看本地脱敏摘要，完成手机号验证和数据用途授权，客户端才允许上传。
- Mock 模式在本地模拟协议且不访问公网。HTTP 模式下，HR-B 接收脱敏包、保存授权并创建异步任务；服务端分析不是“本地分析”。
- 手机号、OTP、启动 token、完整 poster ticket、原始 session 和脱敏前副本不得进入上传包或日志。

## Mock 流程与恢复

完整流程为：扫描 → 主动选择一个 Assessment Scope → 本地脱敏预览 → 手机验证 → 用途授权 → 上传 → 异步分析 → 直接下载图片海报。前台轮询约 30 秒，超时后保留或重新打开同一个本地页面继续查看。

`kuai status` 只输出本地 CLI 版本、默认服务模式和重新运行 `kuai start` 的建议。浏览器任务 ID 保存在 `sessionStorage`，CLI 无权读取，因此 `status` 不声称查询或恢复当前远端任务。

## 配置、调试和失败场景

| 配置 | 作用 |
| --- | --- |
| `kuai scan` | 只读发现 session 并输出来源状态 |
| `--source-root product=/absolute/path` | 指定 catalog 产品的显式根；ready 来源由只读适配器扫描，unsupported 来源只做单目录存在性检测 |
| `kuai start --no-browser` | 启动本地 UI 但不自动打开浏览器 |
| `kuai status` | 输出本地安装/模式诊断和 UI 恢复建议，不查询远端任务 |
| `--service-mode=http --service-url=https://…` | 显式启用可信 HR-B HTTPS origin；只设置环境变量不会切换 |

## 完整验证

```bash
go test -race ./...
go vet ./...
npm ci --ignore-scripts --no-audit --no-fund
npm test
bash tests/test_kuai_install_sh.sh
bash tests/test_build_kuai_release_sh.sh
bash tests/test_kci_pipeline_build_sh.sh
bash tests/test_assemble_kuai_checksums_sh.sh
python3 -m unittest tests.test_docs tests.test_single_binary_contract -v
bash -n install.sh build.sh kci-pipeline-build.sh scripts/build-kuai-release.sh scripts/assemble-kuai-checksums.sh
KUAI_VERSION=1.0.0 sh ./scripts/build-kuai-release.sh
git diff --check
```

发布脚本使用 `CGO_ENABLED=0` 和纯 Go `modernc.org/sqlite`，生成 darwin/linux/windows 的 amd64/arm64 六个 `kuai` 产物及 `SHA256SUMS`：

```bash
KUAI_VERSION=1.0.0 sh ./scripts/build-kuai-release.sh
go version -m ./dist/kuai-darwin-arm64
```

`scripts/build-kuai-release.sh` 不直接编译业务代码，而是为每个平台调用项目根目录的自定义 `build.sh`。如需调整编译参数、注入构建步骤或替换编译实现，应修改 `build.sh`；发布脚本仍负责平台矩阵、产物命名和 SHA-256 清单。

## 天琴流水线签名发版

正式分发的产物需要代码签名：macOS 未签名会被 Gatekeeper 拦截，Windows 会触发 SmartScreen 未知发布者提示，Linux 没有系统级签名机制因而无需签名。签名在天琴（`kdev.corp.kuaishou.com`）物理机上完成，入口是 `kci-pipeline-build.sh`。

每个平台一条独立流水线，且必须绑定对应平台的物理机资源池（默认资源池不可用）：

| 流水线 | 资源池 | 产物 | 签名方式 |
| --- | --- | --- | --- |
| macOS ARM | macOS M 芯片物理机池 | `kuai-darwin-arm64` | codesign + notarytool |
| macOS x64 | macOS Intel 物理机池 | `kuai-darwin-amd64` | codesign + notarytool |
| Windows ARM | Windows ARM64 物理机池 | `kuai-windows-arm64.exe` | HTTPS EV 签名服务 + Authenticode 验证 |
| Windows x64 | Windows x64 物理机池 | `kuai-windows-amd64.exe` | HTTPS EV 签名服务 + Authenticode 验证 |
| Linux ARM | Linux ARM64 物理机池 | `kuai-linux-arm64` | 无需签名 |
| Linux x64 | Linux x64 物理机池 | `kuai-linux-amd64` | 无需签名 |

`kci-pipeline-build.sh` 不自行编译，而是把天琴注入的 `UPLOAD_PLATFORM` 与 `UPLOAD_ARCH` 翻译成 `GOOS`/`GOARCH` 后调用 `build.sh`，因此版本注入与 `-trimpath` 与本地发布链路完全一致。天琴的 `UPLOAD_ARCH` 使用 `x64` 命名，脚本内映射为 `amd64`。`UPLOAD_PLATFORM`、`UPLOAD_ARCH` 和 `UPLOAD_PACKAGE_VERSION` 都必须显式提供；流水线还要求与本地发布链路相同的 Go 1.26.5，缺失或不匹配立即失败。每个目标先在 `dist/targets` 下的隔离 staging 中生成和签名，发布前以 POSIX noclobber/O_EXCL 原子取得 per-target 锁，在锁内重查目标不存在后，再把二进制与 `.sha256` 作为一个不可变 pair 目录首次原子发布到 `dist/targets/<artifact>/`。目标或锁已经存在时均失败；trap 只删除本进程成功取得的锁。

本地可模拟天琴环境变量验证：

```bash
UPLOAD_PLATFORM=darwin UPLOAD_ARCH=arm64 UPLOAD_PACKAGE_VERSION=1.0.0 \
  bash ./kci-pipeline-build.sh false false
./dist/targets/kuai-darwin-arm64/kuai-darwin-arm64 status
```

两个位置参数分别是是否签名与是否公证，默认均为 `true`；SIGN 与 NOTARIZE 只接受 true 或 false，其他值会失败。参数组合按平台 fail-closed：macOS 只有签名后才能公证；Windows 必须 `NOTARIZE=false`；Linux 必须使用 `false false`。正式 macOS 流水线使用 `true true`，并预先在构建机钥匙串创建 notarytool profile，再通过 `APPLE_NOTARY_PROFILE` 传入 profile 名；密码不作为命令行参数传递。`APPLE_NOTARY_TIMEOUT` 是带 `s`、`m` 或 `h` 的正数，默认 `20m`。notarytool 使用 JSON 输出，状态只接受 `Accepted`；提交前后各执行一次严格 codesign 验证。裸 Mach-O 不执行 stapler。可通过 `APPLE_SIGNING_IDENTITY` 覆盖默认签名身份。

正式 Windows 流水线使用 `true false`，并通过非空的 `WINDOWS_SIGNING_PUBLISHER` 指定期望的完整 leaf `SignerCertificate.Subject`，例如 `CN=Qrite Technology Limited`。远程签名下载完成后先通过 `signtool verify /pa /all /v`，再由 PowerShell 结构化读取 leaf subject；规范化空白后必须与期望值精确相等，时间戳证书或证书链中的文本不能代替 leaf 匹配。签名服务的未知状态立即失败。Linux 流水线显式使用 `false false`。签名后不得再修改二进制字节，否则签名失效。

每条流水线产出自己的 `dist/targets/<artifact>/` pair 目录。发布汇总任务从六条流水线收集 pair 内容到一个全新的真实扁平目录，例如 `release-inputs`，并要求汇总机提供 Python 3，再生成 `install.sh` 所需的唯一 `SHA256SUMS`。输入目录必须精确 12 个条目：六个规范分片和对应六个普通、非 symlink 产物，不允许 notes、额外 artifact、子目录或既有 `SHA256SUMS`。汇总脚本重新计算六个产物的 SHA-256 并与分片精确匹配，最后由 Python `os.link(..., follow_symlinks=False)` 直接调用硬链接（link(2)）原子创建此前不存在的 `SHA256SUMS`；并发出现的文件、目录或 symlink 会返回 `EEXIST`，不会被当作目录跟随或覆盖：

```bash
./scripts/assemble-kuai-checksums.sh ./release-inputs ./release-inputs/SHA256SUMS
```

产物上传 KCDN 后，安装时通过 `KUAI_RELEASE_URL` 指向对应目录；该变量默认指向 GitHub Release，必须是 HTTPS：

```bash
KUAI_RELEASE_URL=https://<kcdn-host>/kuai/1.0.0 sh ./install.sh
```

Windows CI 还运行：

```powershell
go test ./...
pwsh -NoProfile -File tests/Test-KuaiInstall.ps1
```
