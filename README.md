# avscore

avscore 是一个本地开发者画像工具：从本机 Agent 会话中选择一次工作记录，分析该会话所属项目的全部可分析会话，再用浏览器展示七维 AI 协作画像。

真实流程是：**选择会话 → 分析所属项目 → 查看画像**。选择的 session 用于确定项目范围；当前画像是项目级汇总，**不是单个 session 的画像**。

## 快速开始

在仓库根目录运行：

```bash
bash avscore.sh
```

启动器会寻找或安装 `agentsview`、同步本机会话、启动本地页面并打开浏览器。选择一个 session、确认本地分析授权后即可生成该 session 所属项目的画像。

如果不希望自动打开浏览器：

```bash
AVSCORE_NO_BROWSER=1 bash avscore.sh
```

终端会打印完整的本地访问地址。

## 系统要求与支持范围

- macOS 或 Linux（`amd64`/`arm64`）。
- Bash、Python 3 和 `curl`。
- 支持能被 `agentsview` 识别并同步的 Agent；选择页会按实际检测到的 Agent 分组。若列表为空，请先确认对应 Agent 已产生本地会话。
- 浏览器仅用于访问本机 UI；报告生成后可离线打开。

## 文件结构

| 文件 | 用途 |
| --- | --- |
| `avscore.sh` | 唯一推荐入口；准备二进制、同步并启动本地服务 |
| `avscore_server.py` | 标准库本地 HTTP 服务、项目画像执行与报告写入 |
| `session-selection.html.tmpl` | 会话选择页模板 |
| `avscore.html.tmpl` | 七维画像结果页模板 |
| `avscore.md` | 可安装到代理环境的正式 skill |
| `tests/test_avscore_server.py` | 服务端单元与 HTTP 测试 |
| `tests/test_templates.py` | 页面结构与交互契约测试 |

默认输出目录是 `~/.agentsview/reports`：

- `profile.json`：`agentsview profile` 的项目级画像数据。
- `report.json`：渲染报告所用的结构化数据。
- `report.html`：可离线打开的最终画像页面。

## 环境变量

| 变量 | 默认值 / 作用 |
| --- | --- |
| `AVSCORE_BINARY_PATH` | 显式指定 `agentsview` 可执行文件，查找优先级最高 |
| `AVSCORE_OUTPUT_DIR` | `~/.agentsview/reports`；更改报告输出目录 |
| `AVSCORE_SKIP_SYNC=1` | 显式跳过启动前同步，使用已有本地数据 |
| `AVSCORE_NO_BROWSER=1` | 不自动打开浏览器，只打印访问地址 |
| `AVSCORE_RELEASE_URL` | 覆盖脚本内置的 release 基础地址，用于受信任镜像 |
| `AVSCORE_VERSION` | `latest`；指定下载版本，如 `1.2.3` |
| `AVSCORE_SKIP_CHECKSUM=1` | 跳过下载文件的 SHA-256 校验；仅应在理解风险时使用 |

二进制按实际脚本顺序查找：显式 `AVSCORE_BINARY_PATH` → 脚本同目录的平台二进制 → `PATH` 中的 `agentsview` → `~/.local/bin/agentsview` → `/usr/local/bin/agentsview`。显式路径不存在或不可执行时会直接停止，不会悄悄换用其他二进制。

## 隐私与安全

分析在本机完成，服务只监听 `127.0.0.1`，不会上传会话或画像数据。页面和 API 使用每次启动随机生成的 token；服务端只接受当前列表中真实存在的 session ID，并从服务端记录确定项目，浏览器不能注入任意项目或命令参数。

下载二进制时默认读取 `SHA256SUMS` 并校验 SHA-256。若校验值不匹配会停止；`AVSCORE_SKIP_CHECKSUM=1` 会降低安全性。只应通过 `AVSCORE_RELEASE_URL` 配置可信的内部 release 或镜像地址。

## 故障排查

- **找不到 Python 3 或 curl**：安装缺失命令后重新运行。交互流程不会降级为旧静态页面。
- **同步失败**：默认立即停止，先处理 `agentsview sync` 报错；确定本地缓存可用时才设置 `AVSCORE_SKIP_SYNC=1`。
- **没有可选会话**：先运行或检查 Agent 的本地会话，再确认 `agentsview session list --format json` 能返回数据。
- **无法下载 agentsview**：检查网络和版本；需要镜像时设置 `AVSCORE_RELEASE_URL`，需要固定版本时设置 `AVSCORE_VERSION`。
- **校验失败**：不要继续使用下载文件；检查 release 的 `SHA256SUMS` 和镜像完整性。
- **浏览器未打开**：复制终端打印的 `http://127.0.0.1:.../?token=...` 地址，或使用 `AVSCORE_NO_BROWSER=1` 明确进入手动模式。
- **画像失败**：仅旧版 CLI 明确报告不认识 `--engine` 时会自动降级；数据库、权限、超时、无效 JSON 或分析失败都会停止并显示错误。

## 安装与触发 skill

将 `avscore.md` 放入代理使用的 skills 目录（可按环境改名为 `SKILL.md`），并确保代理能访问本仓库。之后可说：

- “运行 avscore”
- “生成我的 AI 协作画像”
- “从会话选择一个项目做开发者画像”

skill 会直接运行仓库随附的 `bash avscore.sh`，不会复制临时 Python 实现。

## 开发验证

```bash
python3 -m unittest discover -s tests -v
bash tests/test_avscore_sh.sh
bash -n avscore.sh
git diff --check
```
