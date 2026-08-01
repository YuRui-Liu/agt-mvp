# kuAI 多智能体本地会话支持扩展设计

日期：2026-08-01
状态：已确认，等待书面规格审查

## 1. 背景与目标

kuAI 是单文件 Go 可执行程序。CLI 与 Skill 必须调用同一个 `kuai`，在本地发现 AI Agent 会话，以项目文件夹作为 Assessment Scope；只有用户明确选择项目后，客户端才逐 session 读取、过滤、递归脱敏并准备上传。

本次工作扩展和校正本地会话适配器，同时保持以下产品约束：

- 不依赖第二个 AgentsView 二进制。
- 不扫描未知目录或猜测未知格式。
- 不读取账号、认证、Token、Credential 等数据。
- 不把“检测到安装”误报成“能够可靠扫描”。
- 一个产品或 session 失败不得阻塞其他来源。

## 2. 支持范围

### 2.1 保持支持并回归验证

| 产品 | 目标状态 | 说明 |
| --- | --- | --- |
| Claude Code | `ready` | 保持现有适配器并执行契约回归。 |
| Codex | `ready` | 保持现有适配器并执行契约回归。 |
| Cursor | `ready` | 保持现有适配器并执行契约回归。 |
| Hermes Agent | `ready` | 保持现有适配器并执行契约回归。 |
| OpenClaw | `ready` | 保持现有适配器并以本机结构回归。 |
| CodeFlicker | `ready` | 保持现有适配器并执行契约回归。 |
| MyFlicker | `ready` | 保持现有适配器并执行契约回归。 |
| Kimi CLI | `ready` | 与 Kimi Code 保持为两个独立产品。 |
| Qwen Code | `ready` | 保持支持并补充当前复合消息结构覆盖。 |

### 2.2 升级现有适配器

| 产品 | 变更 |
| --- | --- |
| OpenCode | 增加当前 SQLite 存储格式，安全处理 WAL；保留仍可验证的旧 JSON 格式。 |
| WorkBuddy | 增加当前 `~/.workbuddy-ai` 项目、索引、JSONL/数据库格式；保留仍可验证的旧路径。 |
| GitHub Copilot for VS Code | 保持规范标识 `vscode-copilot`，增加提供者或响应者来源校验，禁止将普通 VS Code Chat 会话误判为 Copilot。 |

### 2.3 新增支持

| 产品 | 规范标识 | 能力与证据 |
| --- | --- | --- |
| Gemini CLI | `gemini-cli` | 迁移现有 AgentsView 的解析逻辑与脱敏 fixture；支持 JSON 与 JSONL、消息、思考和工具事件。当前机器未安装，必须标注“测试样例验证”。 |
| GitHub Copilot CLI | `copilot-cli` | 独立解析 `.copilot/session-state` 事件流，不与 VS Code Copilot 合并；当前机器未安装，必须标注“测试样例验证”。 |
| Cline | `cline` | 读取经过验证的 Cline 会话 JSON；先保证消息能力，工具能力仅在字段结构得到 fixture 验证后声明。大小写名称统一到同一标识。 |
| Kimi Code | `kimi-code` | 读取 session 索引、state 和 agent wire 事件，独立于 Kimi CLI。 |
| CodeBuddy CLI | `codebuddy-cli` | 读取项目目录下会话 JSONL，支持经过验证的消息和工具事件。 |
| 通义灵码 CLI | `tongyi-lingma-cli` | 读取 SharedClientCache 中经过验证的 execution JSONL。 |
| 通义灵码 IDE | `tongyi-lingma-ide` | 只读经过验证的聊天表，不读取用户、认证和账号表。 |
| Qoder CLI | `qoder-cli` | 读取项目 transcript JSONL。 |
| Qoder IDE | `qoder-ide` | 只读经过验证的聊天记录表，不读取无关数据库内容。 |
| aider | `aider` | 解析聊天历史 Markdown，仅声明 `messages`；状态行不得伪装成结构化工具调用。 |

### 2.4 需要产品导出

TRAE 使用规范标识 `trae`。其会话数据库为加密存储，kuAI 不破解、不猜测密钥，也不将输入历史或摘要当作完整会话。适配器只使用 TRAE 官方会话导出能力生成用户可见 Markdown，再在受控临时目录解析并删除临时文件。

发现 TRAE 但尚未取得导出内容时，状态为 `export_required`，不得显示为可直接上传的 `ready`。

### 2.5 暂不支持及原因

| 产品 | 状态 | 原因 |
| --- | --- | --- |
| Kiro | `detected_unsupported` | 本机已安装并运行过，但只发现空的 Shell 历史数据库，没有可验证的 AI 会话实例；候选格式缺少真实样本。 |
| CodeBuddy IDE | `detected_unsupported` | 只发现会话指针和数据库二进制字段，尚未验证稳定、完整的对话正文格式；CodeBuddy CLI 不受影响。 |
| TRAE Work | `detected_unsupported` | 尚未发现能与 TRAE 明确区分的本地会话格式和真实样本。 |
| Kimi Work | `detected_unsupported` | 尚未发现本地会话格式，不能使用 Kimi CLI 或 Kimi Code 格式代替。 |
| Qoder Work | `detected_unsupported` | 尚未验证出独立于 Qoder CLI/IDE 的稳定会话格式。 |

最终产品清单必须同时展示验证等级：`本机真实数据验证`、`测试样例验证`、`需要产品导出` 或 `暂不支持`。

## 3. 架构与组件边界

每个产品实现统一的 `Adapter` 接口，产品内部可包含多个明确的版本或格式解析器，但禁止用一个泛化解析器猜测不同产品的数据。

### 3.1 Discover

`Discover` 仅发现：

- 规范产品标识和展示名；
- 项目归属和安全展示名；
- session 标识、数量、更新时间；
- 经验证的能力集合；
- 来源状态和验证等级；
- 用于读取前复核的文件身份摘要。

`Discover` 不读取或返回会话正文。Assessment Scope 以项目文件夹为单位，界面只显示项目名称，不显示完整本地路径，且默认不选中任何项目。

### 3.2 Open

用户选择一个或多个项目后，客户端才调用 `Open`，逐 session 流式解析为统一事件：

- 用户、助手及明确标识的系统消息；
- 结构化工具调用；
- 与调用 ID 匹配的工具结果；
- 仅在来源明确标识时输出的 reasoning；
- 安全附件元数据，不读取附件正文。

`Open` 先复核 `Discover` 产生的身份摘要，防止选择后文件被替换。适配器只负责可信解析，输出继续进入现有禁止字段删除、Read 结果剔除与递归脱敏流程。

### 3.3 产品标识

以下产品必须保持独立：

- Kimi CLI 与 Kimi Code；
- GitHub Copilot CLI 与 GitHub Copilot for VS Code；
- 通义灵码 CLI 与 IDE；
- Qoder CLI 与 IDE。

名称别名只用于大小写和展示名归一化，不得跨产品合并来源。

## 4. 安全读取规则

- 只读取经过验证的会话根目录、文件名模式和数据库表。
- 拒绝符号链接逃逸、路径穿越、超大文件、超长行、过多记录以及扫描期间的文件替换。
- SQLite 来源使用只读一致性快照并兼容 WAL；SQL 必须显式列出允许的表和字段。
- 严禁读取认证、账号、Token、Credential、浏览器状态或其他无关表和配置。
- JSONL 容忍尚未写完的最后一行；单条损坏记录必须隔离并产生可诊断状态。
- 重复事件 ID 使用格式明确规定的规则处理；没有规则时不得静默猜测。
- TRAE 导出必须写入权限受限的受控临时文件，解析结束或失败后均执行清理。
- 本机验证和日志只输出产品、项目数、session 数、能力和错误类别，不输出正文或完整路径。

## 5. 状态、能力和降级

### 5.1 来源状态

| 状态 | 含义 |
| --- | --- |
| `ready` | 存在经过验证且能够安全解析的会话。 |
| `not_found` | 产品未安装或没有本地会话。 |
| `export_required` | 必须先通过产品官方能力导出。 |
| `format_unsupported` | 检测到数据，但当前版本或格式尚未验证。 |
| `read_error` | 权限、占用、内容损坏或安全校验失败。 |
| `detected_unsupported` | 明确检测到产品，但当前范围暂不支持。 |

界面显示用户可操作的原因，不显示内部路径、SQL、堆栈或敏感数据。版本变化导致结构不匹配时必须降级为 `format_unsupported`，不能猜测字段后继续上传。

### 5.2 能力

能力只能由经过验证的解析结果声明：

- `messages`
- `tools`
- `reasoning`
- `attachments`（只表示安全元数据）

单个产品失败不影响其他产品；单个 session 损坏不影响同一项目的其他 session。

## 6. 测试与验收

每个新增或升级的适配器必须提供脱敏 fixture 和统一契约测试，覆盖：

- 项目归属、session 数量和能力发现准确；
- Assessment Scope 默认不选中；
- 只导出用户明确选择的项目；
- 消息顺序、角色、工具调用与工具结果配对；
- 禁止字段删除、Read 结果剔除及递归脱敏；
- JSONL 尾行不完整、单条损坏、重复 ID 和空会话；
- SQLite WAL、并发写入和一致性快照；
- 文件替换、符号链接逃逸、路径穿越和资源上限；
- 认证、账号和凭据文件或表永不被读取；
- 单个来源失败不会阻断其他来源。

专项验收要求：

- OpenCode 和 WorkBuddy 使用本机当前格式进行真实结构验证。
- TRAE 在官方导出不可用时返回 `export_required`，不得回退到数据库解密。
- aider 只能声明 `messages`。
- Gemini CLI 和 GitHub Copilot CLI 使用迁移 fixture 验证，并注明未经过本机安装实例验证。
- Kiro 必须保持非 `ready`。
- VS Code Copilot 必须证明普通 VS Code 会话不会被误判为 Copilot。
- `go test ./...`、构建脚本测试、Skill 健壮性测试以及真正单文件 `kuai` 构建必须通过。

## 7. 文档交付

实现完成后同步更新：

- 仓库 README、CLI 帮助和支持矩阵；
- `/Users/liuyuxiang05/Projects/liu/2-Notes/Projects/HR-agentreview/数据基建/Agent本地会话格式.md`。

《Agent本地会话格式.md》对每个产品记录：规范标识、展示名、会话路径或存储类型、格式概要、项目归属方法、能力、来源状态、验证等级、限制和暂不支持原因。文档不得包含真实会话正文、完整用户路径或敏感数据。

## 8. 非目标

- 不实现 Kiro、CodeBuddy IDE、TRAE Work、Kimi Work 或 Qoder Work 的猜测性解析。
- 不破解 TRAE 或其他产品的加密数据库。
- 不上传本地附件正文。
- 不把安装检测、输入历史、摘要或项目快照当作完整会话。
- 不引入 AgentsView 或其他第二个运行时二进制依赖。

## 9. 完成定义

本设计完成的标准是：所有目标适配器按照统一接口注册到单文件 `kuai`；安全、脱敏和契约测试通过；产品状态及验证等级准确；README、CLI 和外部会话格式文档一致；最终向用户提供完整的支持与暂不支持清单及原因。
