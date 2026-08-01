---
name: kuai
description: Use when a user asks to run kuAI, scan local Agent sessions, create an assessment, or download an analysis poster.
version: "1.0.0"
---

# kuAI

`kuai` 是单一 Go 可执行文件：扫描、导出、脱敏和本地 Web UI 都由同一个命令提供。不要查找、安装或运行 `agentsview`，也不要在 Skill 中复制这些实现。

## 1. 安装检查

1. macOS/Linux 运行 `command -v kuai && kuai --version`；Windows 运行 `Get-Command kuai` 后再执行 `kuai --version`。
2. 如果已安装但版本检查失败，停止并进入“故障诊断”，不要静默换用其他程序。
3. 如果未安装，说明安装器将通过 HTTPS 下载当前平台的单个 `kuai` 文件，校验 `SHA256SUMS`，再原子安装到用户级目录并更新 PATH。取得用户明确同意后，才可运行可信仓库内的 `install.sh` 或 `install.ps1`。
4. 不要使用 `curl ... | sh`、不受信任镜像或跳过校验。无法确认安装脚本来源时，给出手动安全步骤：分别下载安装器、检查内容，再在本地运行。

## 2. 启动与选择范围

1. 运行 `kuai start`。需要先查看发现结果时运行 `kuai scan`；`kuai status` 只检查本地 CLI 版本、默认服务模式并给出重新打开本地 UI 的建议，无法读取浏览器 `sessionStorage`，因此不报告远端任务状态。
2. 告知用户扫描只读访问受支持的本地 Agent session，不会修改来源数据。
3. 浏览器打开后，由用户主动选择一个 **Assessment Scope**。范围可能是 project、workspace、conversation group 或明确确认的 session collection。
4. 页面不得默认选择任何 Scope，Skill 也不得替用户选择。若没有可评估范围，展示来源状态和安全诊断，不要扩大扫描目录。

## 3. 脱敏预览与授权

1. 提醒用户先查看本地脱敏预览。`kuai` 会在本地删除禁止字段、Read 正文、附件和二进制，并递归处理 secret、绝对路径、用户名及 PII。
2. 不要把原始 session、脱敏前内容、手机号、OTP、启动 token 或完整本地 URL复制到聊天中。
3. 只有用户确认所选 Scope、检查脱敏摘要，并明确完成手机号验证和数据用途授权后，才允许上传。
4. 不得暗示服务端在本地运行或在本地分析。`kuai` 只在本地扫描、导出和脱敏；授权后的脱敏包由 HR-B 接收，分析发生在服务端。

## 4. 异步任务与海报

上传完成即表示脱敏包已成功交付，不表示分析已经完成。前台轮询约 30 秒；仍未完成时告知用户任务已进入异步处理，应保持或重新打开同一个本地页面查看。不要声称 `kuai status` 能恢复浏览器中的任务 ID。

任务完成后，海报是可直接下载的图片；引导用户点击下载，不要要求复制图片地址、ticket 或启动 URL。

## 5. 故障诊断

- `kuai: command not found`：重新检查用户级 bin 是否在 PATH；不要安装第二个二进制。
- `kuai --version` 失败：确认文件与当前 OS/architecture 匹配，并重新核对 SHA-256。
- 扫描为空：运行 `kuai scan` 查看各来源状态；只使用文档支持的显式目录，不做全盘扫描。
- 来源显示不受支持：可以展示但不可选择或上传，不能把“检测到”描述成“可评估”。
- 本地页面未打开：运行 `kuai start --no-browser`，只在用户的私密终端中使用启动 URL。
- 上传或分析失败：在本地页面读取安全错误码；`kuai status` 仅用于检查 CLI 安装和恢复入口。不要粘贴原始 payload 或包含凭据的日志。
- 超过 30 秒：这是异步路径，不要重复上传；稍后查询同一任务。
