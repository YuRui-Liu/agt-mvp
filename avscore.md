---
name: avscore
description: 从本机 Agent 会话选择项目，运行本地 avscore 并生成七维 AI 协作画像
version: "1.0.0"
---

# avscore

当用户说“运行 avscore”“生成 AI 协作画像”“分析我的 Agent 使用方式”或要求从一次会话查看开发者画像时，使用本 skill。

## 真实范围

流程是“选择会话 → 分析所属项目 → 查看画像”。所选 session 只用于确定项目；结果汇总该项目中可分析的会话，**不是单个 session 的画像**。向用户展示进度和结果时必须明确说“正在分析所选会话的所属项目”。

## 执行

1. 定位包含 `avscore.sh` 的仓库根目录，确认以下随附文件存在：
   - `avscore.sh`
   - `avscore_server.py`
   - `session-selection.html.tmpl`
   - `avscore.html.tmpl`
2. 告知用户进度：“正在准备本地 avscore 并同步会话。”
3. 在仓库根目录直接运行：

   ```bash
   bash avscore.sh
   ```

   不要复制或内嵌大段 Python，也不要另写临时服务器；启动器负责平台检测、二进制定位或安装、同步、本地服务和浏览器打开。
4. 将终端打印的本地 URL 和当前阶段清楚地转达给用户。浏览器未自动打开时，请用户访问该完整 URL，不要省略 token。
5. 用户在页面选择 session 后，说明将分析其所属项目；生成结果后告知 `report.html` 的位置。

## 失败与降级规则

唯一允许的自动兼容降级是：首次画像命令明确返回 `unknown flag: --engine`（或等价的未知 `--engine` 参数错误）时，去掉 `--engine` 重试。不得因数据库锁、权限、超时、无效 JSON、同步失败或任意其他画像错误而降级；这些情况必须停止，保留原始报告文件，并用安全摘要说明下一步。

缺少 Python 3、必要模板、有效会话或可执行的 `agentsview` 时停止。同步失败默认停止；只有用户已理解可能使用旧数据并明确同意时，才可用 `AVSCORE_SKIP_SYNC=1` 重启。

## 环境变量

- `AVSCORE_BINARY_PATH`：指定 `agentsview` 可执行文件。
- `AVSCORE_OUTPUT_DIR`：报告目录，默认 `~/.agentsview/reports`。
- `AVSCORE_SKIP_SYNC=1`：显式跳过同步。
- `AVSCORE_NO_BROWSER=1`：不自动打开浏览器。
- `AVSCORE_RELEASE_URL`：受信任的 release 或镜像基础地址。
- `AVSCORE_VERSION`：固定下载版本，默认 `latest`。
- `AVSCORE_SKIP_CHECKSUM=1`：跳过 SHA-256 校验，会降低安全性。

需要覆盖变量时，把它们放在同一条启动命令前，例如：

```bash
AVSCORE_NO_BROWSER=1 AVSCORE_OUTPUT_DIR=/absolute/report/path bash avscore.sh
```

## 隐私与安全

分析只在本机运行，HTTP 服务仅绑定 `127.0.0.1`，会话和画像不会上传。不要在聊天、日志或公开渠道转发包含随机 token 的完整 URL。浏览器只能选择服务端已知的 session；项目名由服务端记录决定。

下载默认校验 release 提供的 `SHA256SUMS`。校验不匹配时必须停止；除非用户理解供应链风险并明确要求，否则不要设置 `AVSCORE_SKIP_CHECKSUM=1`。使用 `AVSCORE_RELEASE_URL` 时只接受用户认可的可信来源，不臆造 GitHub、公司或第三方下载地址。
