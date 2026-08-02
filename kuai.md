---
name: kuai
description: Use when a user asks to run kuAI, scan local Agent sessions, create an assessment scope, check kuai CLI status, or securely submit redacted session data. Covers install verification, background UI startup, and read-only scan diagnostics.
version: "1.0.0"
---

# kuAI

`kuai` 是单一 Go 可执行文件：扫描、导出、脱敏和本地 Web UI 都由同一个命令提供。不要查找、安装或运行第二个二进制，也不要在 Skill 中复制这些实现。

## 0. 命令面与退出码

`kuai` 只接受以下四个命令。不带命令时等同 `kuai start`。

| 命令 | 是否阻塞 | stdout |
| --- | --- | --- |
| `kuai start [options]` | **阻塞常驻**，直到收到信号 | 单行 JSON，含 launch token |
| `kuai scan [options]` | 一次性 | 单行 JSON，scope 列表 |
| `kuai status` | 一次性 | 单行 JSON，本地诊断 |
| `kuai version`（或 `--version`） | 一次性 | `kuai <version>` |

`kuai --help`、`-h`、`help` 输出用法。

| 退出码 | 含义 | 处理 |
| --- | --- | --- |
| 0 | 成功 | 继续 |
| 1 | 运行期失败（扫描失败、服务配置不可用、本地服务无法启动） | 进入“故障诊断” |
| 2 | 参数错误（未知命令或未知 flag） | 修正命令，不要改用其他程序 |

可用 options：

| Option | 适用 | 说明 |
| --- | --- | --- |
| `--source-root product=/absolute/path` | `start`、`scan` | 指定 catalog 产品的显式根；按来源状态执行只读扫描或单目录存在性检测 |
| `--no-browser` | `start` | 启动本地 UI 但不自动打开浏览器 |
| `--service-mode mock\|http` | `start` | 默认 `mock` |
| `--service-url https://…` | `start` | 仅 `http` 模式使用且必填 |

约束（违反即退出码 2）：

- `--source-root` 的值必须是 `product=/absolute/path`；product 须存在于 catalog，路径必须是绝对路径且已规范化（不得含 `..` 或末尾斜杠）。
- `--service-mode mock` 不得同时传 `--service-url`；`--service-mode http` 必须传 `--service-url`。只设置环境变量不会切换服务模式。
- `scan` 只使用 `--source-root`；`--service-mode` 等组合校验仍会对它生效，因此给它传 `--service-mode http` 会因缺少 `--service-url` 而失败。`--no-browser` 虽被接受但对 `scan` 无任何效果，不要传。

## 1. 安装检查

1. macOS/Linux 运行 `command -v kuai && kuai version`；Windows 运行 `Get-Command kuai` 后再执行 `kuai version`。
2. 已安装但版本检查失败时，停止并进入“故障诊断”，不要静默换用其他程序。
3. 未安装时，先说明安装器行为：通过 HTTPS 下载当前平台的单个 `kuai` 文件，校验 `SHA256SUMS`，再原子安装到用户级目录并更新 PATH。取得用户明确同意后，才可运行可信仓库内的 `install.sh` 或 `install.ps1`。
4. 安装来源有两种，无论哪种都必须完成 SHA-256 校验，不得跳过：
   - 默认从仓库配置的 Release 地址下载。
   - 公司内网产物发布在 KCDN 时，由用户提供目录并通过 `KUAI_RELEASE_URL` 指定，例如 `KUAI_RELEASE_URL=https://<kcdn-host>/kuai/<version> sh ./install.sh`。该值必须是 HTTPS。
5. 想先确认将下载和安装的路径，运行 `KUAI_INSTALL_DRY_RUN=1 sh ./install.sh`。
6. 不要使用 `curl ... | sh`、不受信任镜像或跳过校验。无法确认安装脚本来源时，给出手动安全步骤：分别下载安装器、检查内容，再在本地运行。

## 2. 启动本地 UI

`kuai start` 会一直运行到收到信号，**不要在前台直接调用**，否则会话会阻塞到超时。必须后台启动、把 stdout 重定向到文件，再从文件读取地址。

启动前先确认没有已在运行的实例。`kuai` 不做单实例保护，重复调用会各自监听不同端口。

macOS/Linux：

```bash
log=$(mktemp -t kuai-start.XXXXXX); pidfile=$(mktemp -t kuai-pid.XXXXXX)
kuai start --no-browser >"$log" 2>"$log.err" &
echo $! >"$pidfile"
for _ in $(seq 1 30); do [ -s "$log" ] && break; sleep 1; done
# 只提取端口用于回显；token 不得出现在任何输出里
sed -n '1s/.*127\.0\.0\.1:\([0-9]*\).*/\1/p' "$log"
```

Windows PowerShell：

```powershell
$log = New-TemporaryFile
$proc = Start-Process kuai -ArgumentList 'start','--no-browser' `
  -RedirectStandardOutput $log -PassThru -NoNewWindow
1..30 | ForEach-Object { if ((Get-Item $log).Length -gt 0) { break }; Start-Sleep 1 }
((Get-Content $log -First 1 | ConvertFrom-Json).url) -replace '\?token=.*',''
```

stdout 首行形如 `{"type":"server-started","url":"http://127.0.0.1:<port>/?token=<token>"}`，是该进程写入的唯一一行。

停止：`kill "$(cat "$pidfile")"`，Windows 用 `Stop-Process -Id $proc.Id`。进程会走优雅关闭。

用完立刻删除日志文件，因为它的首行包含 token。不要把日志留在仓库内或任何可共享位置。

需要自动打开浏览器时省略 `--no-browser`，但仍必须后台运行。

## 3. 扫描与只读诊断

需要先查看发现结果时运行 `kuai scan`。输出是单行 JSON：

```json
{"scopes":[{"key":"…","type":"conversation_group","label":"…","session_count":24,
  "products":["…"],"capabilities":["messages","tools"],"started_at":"…","ended_at":"…",
  "status":"ready","selectable":true}]}
```

允许解析该 JSON，并基于 `selectable`、`status`、`session_count`、`products`、`label` 向用户做只读汇总，帮助判断是否存在可评估范围。这些字段是安全 label 与稳定哈希，不含绝对路径或本地用户名。

`key` 每次 `scan` 都会变化，因为每次调用都会重新派生 scope secret。因此：

- `key` 只能在同一次 `scan` 输出内部用于去重或引用。
- 禁止把 `key` 跨调用引用，也禁止拿它去匹配本地 UI 中的 scope —— 两者的 secret 不同，永远对不上。
- 需要引用某个范围时，用 `label` 与 `type` 向用户描述，让用户在 UI 中自行确认。

扫描只读访问受支持的本地 Agent session，不会修改来源数据。扫描为空时运行 `kuai scan` 查看各来源状态，不要扩大扫描目录；只能使用 `--source-root` 并由用户提供合法的绝对路径。

`kuai status` 只输出本地 CLI 版本、默认服务模式和重新打开本地 UI 的建议。它无法读取浏览器 `sessionStorage`，因此不报告远端任务状态。

## 4. Scope 选择边界

1. 浏览器打开后，由用户主动选择一个 **Assessment Scope**。范围可能是 project、workspace、conversation group 或明确确认的 session collection。
2. 页面不默认选择任何 Scope，Skill 也不得替用户选择或推荐默认项。
3. `selectable` 为 false 的来源可以展示，但不可选择或上传。不要把“检测到”描述成“可评估”。只有具备版本化脱敏 fixture、格式签名、只读发现与打开测试、损坏和超限回归测试的来源才能上传。
4. 没有可评估范围时，展示来源状态和安全诊断即可。

### 支持来源

Catalog 中有 22 个 ready 来源：`aider`、`claude-code`、`cline`、`codebuddy-cli`、`codeflicker`、`codex`、`copilot-cli`、`cursor`、`gemini-cli`、`hermes-agent`、`kimi-cli`、`kimi-code`、`myflicker`、`openclaw`、`opencode`、`qoder-cli`、`qoder-ide`、`qwen-code`、`tongyi-lingma-cli`、`tongyi-lingma-ide`、`vscode-copilot`、`workbuddy`。

其中 `copilot-cli` 与 `gemini-cli` 是 `fixture_verified`，其余 ready 来源是 `machine_verified`。`ready` 不等于本机可选择：只有本次扫描得到 ready session 且 `sessionCount > 0`，对应 Scope 才可 selectable。

| 仅检测、不可选择 | Verification | Unsupported reason |
| --- | --- | --- |
| `trae` | `export_required` | `official_export_required` |
| `trae-work` | `unsupported` | `no_distinct_local_format` |
| `kimi-work` | `unsupported` | `no_verified_session_schema` |
| `qoder-work` | `unsupported` | `no_distinct_local_format` |
| `codebuddy-ide` | `unsupported` | `no_verified_transcript_body` |
| `kiro` | `unsupported` | `no_verified_session_schema` |

`copilot-cli` 不等于 `vscode-copilot`：两者使用不同目录与格式，不能互换 product。`kimi-cli` 不等于 `kimi-code`：前者读取 `.kimi/sessions` 下的 `wire-v1`，后者读取 `.kimi-code` 下由 index、state 和 `wire-1.4` 共同验证的会话。

`--source-root` 只接受用户提供的合法绝对路径。22 个 ready 来源的显式根会由对应只读适配器扫描；6 个 unsupported 来源没有适配器，只对该目录执行一次 `Lstat` 存在性检测，用于报告 detected 状态，不读取 session 内容。不得猜测目录、扩大扫描范围或修改来源文件。

## 5. 脱敏预览与授权

1. 提醒用户先查看本地脱敏预览。`kuai` 会在本地删除禁止字段、Read 正文、附件和二进制，并递归处理 secret、绝对路径、用户名及 PII。
2. 不要把原始 session、脱敏前内容、手机号、OTP、启动 token 或完整本地 URL 复制到聊天中。
3. 只有用户确认所选 Scope、检查脱敏摘要，并明确完成手机号验证和数据用途授权后，才允许上传。
4. 不得暗示服务端在本地运行或在本地分析。`kuai` 只在本地扫描、导出和脱敏；授权后的脱敏包由 HR-B 接收，分析发生在服务端。

## 6. 提交成功回执

上传完成后，页面只展示服务端返回的提交成功回执。它是当前 C 端流程的终态，不轮询或展示后续分析、画像与海报，也不要向用户承诺分析完成时间。

用户可以返回校招职位或重新提交一个新的 Assessment Scope。不要声称 `kuai status` 能查询提交状态；浏览器中的认证信息和幂等上传草稿不会暴露给 CLI。

## 7. 禁止事项

- 前台阻塞式调用 `kuai start`。
- 把 `start` 的 stdout 原文、完整启动 URL 或 token 输出到聊天、工单、提交信息或共享日志。回显只给 `http://127.0.0.1:<port>/`，并说明完整 URL 需用户在自己的私密终端中获取。
- 保留含 token 的日志文件，或把它写到仓库内。
- 把 `scan` 的 `key` 跨调用引用，或与 UI 中的 scope 做匹配。
- 代替用户选择 Assessment Scope，或在没有明确授权的情况下触发上传。
- 因扫描为空而扩大扫描范围、做全盘扫描，或自行构造 `--source-root` 路径。
- 复制 CLI 的扫描、脱敏或上传实现，或改用另一个二进制绕开失败。
- 粘贴原始 payload、脱敏前副本或含凭据的日志。

## 8. 故障诊断

- `kuai: command not found`：检查用户级 bin 是否在 PATH，重新打开 shell；不要安装第二个二进制。
- 退出码 2：命令或 flag 不被接受。核对第 0 节的命令面，注意 `--service-mode` 与 `--service-url` 的配对约束，以及 `--source-root` 的绝对路径与规范化要求。
- `kuai version` 失败：确认文件与当前 OS/architecture 匹配，并重新核对 SHA-256。
- `kuai: session discovery failed`（退出码 1）：扫描阶段失败，查看来源状态；不要扩大扫描目录重试。
- `kuai: local server could not start`（退出码 1）：本地端口或状态目录不可用；确认没有异常残留进程后重试。
- `kuai: service configuration is unavailable`（退出码 1）：`--service-mode` 与 `--service-url` 组合非法。
- 等待 30 秒后日志文件仍为空：进程未成功写出启动事件，检查 `$log.err`，不要重复启动更多实例。
- 启动了多个实例：`kuai` 不做单实例保护，各实例监听不同端口。停掉多余进程，只保留一个。
- 浏览器未打开：使用 `--no-browser` 后按第 2 节取端口，只在用户的私密终端中使用完整启动 URL。
- 上传失败：在本地页面读取安全错误码；同一次准备会复用幂等键，按页面提示重试。`kuai status` 仅用于检查 CLI 安装和恢复入口。
