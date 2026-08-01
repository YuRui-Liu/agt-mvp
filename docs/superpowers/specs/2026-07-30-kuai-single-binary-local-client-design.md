# kuAI 单文件本地客户端设计

## 1. 目标与范围

本项目只实现用户本地环境中的 `kuai` 客户端。正式发行物必须是一个原生 Go 可执行文件；Skill、CLI 和安装器统一检查、安装并运行这个文件。

客户端负责：

- 只读发现本地 Agent session；
- 将 session 聚合为一个或多个 Assessment Scope；
- 让用户主动选择一个 Scope，且不默认选中；
- 逐 session 流式读取、规范化、删除禁止字段并递归脱敏；
- 完成手机号验证和数据用途授权；
- 创建上传任务、上传脱敏包并确认完整性；
- 轮询任务状态，超过 30 秒进入异步等待；
- 完成后直接下载海报图片。

本项目不实现 HR-B、AgentsView 分析服务、服务端画像算法、对象存储或消息队列。它只实现这些服务所需的客户端协议及本地 Mock。

## 2. 核心决策

### 2.1 真正单文件

`kuai` 不安装、探测或启动外部 `agentsview`，不依赖任何来源 CLI，也不创建 agentsview 数据库。现有 agentsview 和 YC/Paxel 代码只作为解析规则、格式兼容性及 fixture 的迁移参考。

发布构建继续采用 `CGO_ENABLED=0`，生成：

- `kuai-darwin-amd64`
- `kuai-darwin-arm64`
- `kuai-linux-amd64`
- `kuai-linux-arm64`
- `kuai-windows-amd64.exe`
- `kuai-windows-arm64.exe`
- `SHA256SUMS`

安装器只下载并校验对应平台的 `kuai`，不再下载 `agentsview`。

### 2.2 本地与服务边界

本地 `kuai` 完成扫描、选择、导出、脱敏、验证授权和上传。HR-B 接收脱敏包、记录用户身份并创建分析任务。AgentsView 只存在于服务端异步分析链路，不参与本地扫描，也不是客户端依赖。

用户上传完成后立即看到上传成功。分析可继续异步运行；客户端通过 HR-B 任务接口获取状态和最终海报。

## 3. Assessment Scope

Assessment Scope 是一次评估和上传的唯一选择单位。一次正式流程只允许选择一个 Scope。

支持四种范围：

| 类型 | 使用场景 |
|---|---|
| `project` | 有可靠项目文件夹的编程 Agent |
| `workspace` | 有工作区但不是标准代码项目 |
| `conversation_group` | 来源自身提供稳定任务或对话分组 |
| `session_collection` | 无法可靠归组时，由用户明确确认的一组 session |

归组优先级为：

```text
Project → Workspace → Conversation Group → Session Collection
```

同一个规范化项目文件夹下，来自不同 Agent 的 session 可以合并为同一个 Scope。客户端只在本机使用绝对路径判断归属；页面和上传包只包含：

- 本地路径计算出的稳定哈希 `key`；
- 安全展示名 `label`；
- Scope 类型；
- 来源 Agent、session 数量和能力摘要。

绝对路径、原始用户名和 home 目录不得进入页面 JSON、上传包或日志。

## 4. 来源适配器架构

### 4.1 接口

每个来源通过独立只读适配器接入：

```go
type Adapter interface {
    Product() string
    Capabilities() []Capability
    Discover(context.Context) ([]Session, error)
    Open(context.Context, Session) (io.ReadCloser, error)
}
```

适配器职责仅包括：

- 声明产品、格式版本、适配器版本和能力；
- 识别可信本地目录及格式签名；
- 发现 session 及 Scope 归属信息；
- 以只读流打开指定 session；
- 将来源事件映射到统一事件结构。

适配器不得修改来源文件、调用来源 CLI、创建来源数据库或扫描未声明目录。一个适配器失败不得阻塞其他来源；页面必须区分“可评估”“已检测但格式未验证”和“扫描失败”。

### 4.2 产品家族

同一产品家族可以共享目录发现、格式探测和事件映射组件，但上传包必须声明具体运行形态：

| 家族 | 具体来源 |
|---|---|
| Flicker | `codeflicker`、`myflicker` |
| TRAE | `trae`、`trae-work` |
| Kimi | `kimi-cli`、`kimi-work`、`kimi-code` |
| Qwen | `qwen-code`、`tongyi-lingma` |
| Qoder | `qoder`、`qoder-work` |
| CodeBuddy | `codebuddy` |

若同一产品家族的格式签名不同，家族适配器必须显式分派，不能根据目录名称猜测格式。

### 4.3 接入批次

第一批迁移已有成熟解析逻辑和 fixture：

- Claude Code；
- Codex；
- Cursor；
- OpenCode；
- VS Code Copilot；
- CodeFlicker；
- MyFlicker。

Flicker 同时支持 JSONL 与 CodeFlicker 的 `composer_data.sqlite`。SQLite 必须使用纯 Go 驱动以保持 `CGO_ENABLED=0`，以只读方式打开，不执行迁移、写 WAL 或修改数据。

第二批新增海外及通用 Agent：

- OpenClaw；
- Hermes Agent；
- WorkBuddy。

第三批新增国产 Agent：

- TRAE、TRAE Work；
- Kimi CLI、Kimi Work、Kimi Code；
- Qwen Code、通义灵码；
- Qoder、QoderWork；
- CodeBuddy。

每个具体来源只有满足以下条件后才能标记为可评估：

1. 有真实且完成脱敏的版本化 fixture；
2. 有明确格式签名；
3. 正确发现 session 和 Scope；
4. 能保留事件顺序、tool call ID、时间戳及可用父子关系；
5. 正常、截断、损坏和超限样本通过回归测试；
6. 能力声明与实际输出一致；
7. 上传包中不存在 secret、绝对路径和禁止字段。

未满足条件的来源可以显示为已检测，但不可选择或上传。

## 5. 统一事件与上传包

### 5.1 Upload Package v2

客户端采用 YC/Paxel Upload Package v2：

```json
{
  "schema_version": 2,
  "client": {
    "name": "kuai",
    "version": "1.0.0",
    "platform": "darwin-arm64"
  },
  "scope": {
    "type": "project",
    "key": "stable-local-hash",
    "label": "campus-app"
  },
  "sessions": [
    {
      "id": "codex:stable-session-id",
      "source": {
        "product": "codex",
        "format_version": "2026-07",
        "adapter_version": "1.0.0",
        "capabilities": ["message", "tool", "task", "file", "git"]
      },
      "events": []
    }
  ],
  "redaction": {
    "replacements": 12,
    "omitted_reads": 3,
    "removed_fields": 4
  },
  "created_at": "2026-07-30T09:00:00Z"
}
```

Session ID 必须包含来源命名空间并在重传时保持稳定。字段不可得、来源不支持和行为未发生必须使用不同状态表达，不能全部编码为空值或零值。

### 5.2 统一事件

适配器输出工具无关的有序消息和事件，至少保留：

- session ID、来源和 Scope；
- message ID、sequence、timestamp、role；
- 脱敏文本；
- tool call ID、工具名和安全参数摘要；
- tool result ID、成功或错误标记及安全结果摘要；
- 可用的 parent session、triggered-by 和 subagent 关系；
- 来源稳定提供的 model、task、file、test 和 git 元数据。

系统注入提示不得作为真实用户 prompt。Malformed entry 可以跳过，但必须记录不含正文的质量计数；若跳过后无法满足最低数据契约，整个 Scope 必须停止上传。

## 6. 本地导出与脱敏

固定处理顺序：

```text
只读打开原始 session
→ 逐行限制大小并解析
→ 映射统一事件
→ 删除禁止字段
→ 工具参数安全摘要
→ 剔除 Read 返回正文
→ 递归 secret、路径和 PII 脱敏
→ 校验 UTF-8、嵌套深度和包大小
→ canonical serialization
→ SHA-256
→ 验证和授权完成后才允许上传
```

默认限制：

| 限制 | 默认值 |
|---|---:|
| 单 JSONL 行 | 1 MiB |
| 单 session | 4 MiB |
| 单 Assessment Scope 包 | 25 MiB |
| 单来源读取时限 | 2 分钟 |

任何读取、解析、脱敏、编码或大小校验错误均 fail closed，不上传部分包。

### 6.1 工具处理

- `Read`：保留工具名、路径占位符及合法数值 offset/limit，删除结果正文。
- `Write`、`MultiEdit`、`Edit`、`NotebookEdit`：保留文件类型、字节数和增删统计，不保留正文或完整 diff。
- `Bash`、测试和 Git：保留经过 secret 与路径处理的命令及结果摘要。
- `Task`、`Agent`：保留脱敏描述、子 Agent 类型、prompt 字节数和摘要哈希，不保留完整委派 prompt。
- `Grep`、`Glob`：保留脱敏 pattern、glob 和路径占位符。
- 未知工具：只保留工具名和参数键名，不保留参数值。

### 6.2 禁止数据

禁止进入上传包：

- Read 返回的完整文件内容；
- Write/Edit 的完整文件副本；
- 附件和二进制；
- 私钥、token、cookie、验证码和数据库凭据；
- 本地绝对路径、原始用户名和 home 目录；
- 未验证来源格式；
- 脱敏前副本；
- 原始手机号和 OTP；
- 启动 token 和完整海报 ticket。

Secret 规则参考 YC `SecretScrubber`，至少覆盖 Anthropic/OpenAI/AWS/GitHub/Google 等厂商 key、JWT、Bearer、PEM 私钥、带凭据数据库 URL、Stripe/Slack/HuggingFace/npm/PyPI、YC token、Twilio、OAuth、Azure/Cloudflare 及常见环境变量 secret。规则顺序必须通过回归测试固定。

页面、包和日志都必须通过路径与用户名泄漏测试。日志只允许计数、枚举、安全 ID 和错误代码。

### 6.3 完整性

客户端对 canonical package bytes 计算：

```text
package_digest = SHA-256(canonical_package_bytes)
```

完成确认提交 digest、字节数、session 数和 schema version。digest 不匹配时任务不得进入分析阶段。

## 7. 服务客户端

本项目实现同一接口的两个客户端：

```go
type ServiceClient interface {
    RequestOTP(context.Context, string) error
    VerifyOTP(context.Context, string, string) (Identity, error)
    SubmitConsent(context.Context, Identity, Consent) error
    CreateUpload(context.Context, UploadMetadata) (UploadTarget, error)
    Upload(context.Context, UploadTarget, io.Reader) error
    CompleteUpload(context.Context, string, Digest) (Task, error)
    GetTask(context.Context, string) (Task, error)
    DownloadPoster(context.Context, string) (io.ReadCloser, error)
}
```

### 7.1 Mock

Mock 是默认开发模式，完整模拟验证、授权、上传、分析状态、30 秒异步切换和海报下载，不访问公网。

### 7.2 正式 HTTP

正式模式连接可信 HR-B HTTPS 服务：

- 不能因存在环境变量而静默从 Mock 切换到正式上传；
- 页面必须展示目标域名及数据将离开本机的提示；
- 禁止 HTTP 降级、任意跨域重定向和 URL token；
- 上传使用幂等 task ID，安全重试不得重复创建任务；
- 身份、授权、task 和上传目标必须相互绑定；
- OTP、手机号和 session 正文不得写入日志；
- API 路径和 payload 集中在协议包，页面和来源适配器不得直接拼接 URL。

## 8. 页面流程与视觉

页面采用已批准的暖米白背景、深色正文和高饱和橙色强调风格。

流程状态：

```text
扫描
→ 选择 Assessment Scope
→ 本地脱敏
→ 手机验证
→ 数据用途授权
→ 上传
→ 上传成功
→ 分析等待
→ 完成
```

要求：

- 页面按 Agent 和 Scope 类型展示结果；
- 不默认选中任何 Scope；
- 显示 Scope 安全名称、类型、Agent、session 数和能力摘要；
- 不展示本地绝对路径；
- 扫描失败按来源隔离；
- 分析超过 30 秒显示异步等待，可关闭页面；
- 重新验证后可以恢复当前身份关联的最新任务；
- 海报入口直接下载图片文件，不打开预览、灯箱或放大层。

## 9. 错误恢复

- OTP 错误返回验证步骤；
- 授权不完整不得创建任务；
- 脱敏失败不得发起网络请求；
- 上传中断使用同一 task ID 重试；
- 轮询失败采用有上限的指数退避；
- 身份不匹配、授权过期、schema 不兼容、digest 不符和高风险泄漏为不可绕过错误；
- 未验证来源不得通过降低脱敏标准或伪造能力进行降级。

## 10. 测试与验收

### 10.1 测试层次

- 适配器契约测试：发现、Scope 归属、顺序、父子关系、能力和事件映射。
- 脱敏安全测试：YC secret 模式、国内及 Windows 路径、PII、Read/Write/Edit、未知工具和递归嵌套。
- 包契约测试：v2 schema、canonical bytes、SHA-256、大小限制和确定性。
- 服务客户端测试：OTP、授权、幂等上传、重试、digest、30 秒切换和任务恢复。
- 页面状态测试：默认不选择、Scope 类型、失败隔离、上传反馈和直接下载。
- 发版测试：六个平台只有 `kuai`，安装器只下载 `kuai`，运行时不查找或启动 `agentsview`。

### 10.2 安全不变量

```text
未选择 Scope            → 不得导出或上传
脱敏失败                → 不得发起上传请求
未验证或未完整授权      → 不得创建上传任务
未验证来源格式          → 不得选择
Read 结果正文           → 上传包中不存在
绝对路径和本机用户名    → 页面、包和日志中不存在
外部 agentsview 进程    → 永远不会启动
```

### 10.3 发布验收

```bash
go test ./...
go vet ./...
bash tests/test_kuai_install_sh.sh
bash tests/test_build_kuai_release_sh.sh
sh scripts/build-kuai-release.sh
```

构建目录中只允许六个平台的 `kuai`、Windows `.exe` 和 `SHA256SUMS`。

## 11. 迁移和删除

实现完成后：

- 删除 `cmd/kuai` 对外部 agentsview 的解析与进程调用；
- 删除 `KUAI_AGENTSVIEW_PATH`、`--agentsview-path`、`--skip-sync` 等外部依赖配置；
- 重构当前 `internal/agentsview` 为通用 Scope 模型和内置 source adapters，不保留误导性的运行时依赖命名；
- 更新 `README.md`、`kuai.md`、`install.sh`、`install.ps1` 和安装测试；
- 移除 `agentreview` 作为本地来源的支持与文案；
- 删除未跟踪且无引用的旧 `avsore.md`；
- 保留与新客户端无冲突的旧 avscore 历史文件，是否进一步删除由独立清理任务决定。
