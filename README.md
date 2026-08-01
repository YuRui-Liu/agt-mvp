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

| 状态 | 来源 |
| --- | --- |
| 已验证、可评估 | `claude-code`、`codex`、`cursor`、`opencode`、`vscode-copilot`、`codeflicker`、`myflicker`、`openclaw`、`hermes-agent`、`workbuddy`、`kimi-cli`、`qwen-code` |
| 仅检测、不可选择 | `trae`、`trae-work`、`kimi-work`、`kimi-code`、`tongyi-lingma`、`qoder`、`qoder-work`、`codebuddy` |

“检测到”不等于“可评估”。只有具备版本化脱敏 fixture、格式签名、只读发现与打开测试、损坏和超限回归测试的来源才能上传。

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
| `--source-root product=/absolute/path` | 对指定产品的绝对目录只做浅层存在性检查；不递归、不读取内容 |
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
python3 -m unittest tests.test_docs tests.test_single_binary_contract -v
bash -n install.sh
KUAI_VERSION=1.0.0 sh ./scripts/build-kuai-release.sh
git diff --check
```

发布脚本使用 `CGO_ENABLED=0` 和纯 Go `modernc.org/sqlite`，生成 darwin/linux/windows 的 amd64/arm64 六个 `kuai` 产物及 `SHA256SUMS`：

```bash
KUAI_VERSION=1.0.0 sh ./scripts/build-kuai-release.sh
go version -m ./dist/kuai-darwin-arm64
```

Windows CI 还运行：

```powershell
go test ./...
pwsh -NoProfile -File tests/Test-KuaiInstall.ps1
```
