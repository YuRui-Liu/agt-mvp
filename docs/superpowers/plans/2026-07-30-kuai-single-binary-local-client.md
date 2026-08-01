# kuAI 单文件本地客户端实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 将当前依赖外部 `agentsview` 的 kuAI Mock 重构为一个可扫描多种本地 Agent、生成 YC Upload Package v2、完成本地脱敏并通过 Mock 或正式 HTTP 客户端上传的单一 Go 可执行文件。

**架构：** 新建 `internal/source` 作为来源注册、发现和统一事件边界；各 Agent 适配器只读来源文件并输出统一 `Session` 流。`internal/upload` 负责 v2 包、工具摘要、递归脱敏、canonical digest；`internal/service` 统一 Mock 与 HTTPS HR-B 协议。现有页面改用 Assessment Scope，不再引用 Project-only 或外部 agentsview 概念。

**技术栈：** Go 1.26.5、Go 标准库、纯 Go SQLite 驱动 `modernc.org/sqlite`、嵌入式 HTML/CSS/JavaScript、shell/PowerShell 安装器。

---

## 文件结构

### 新建

- `internal/source/model.go`：来源、session、能力、Assessment Scope 公共模型。
- `internal/source/adapter.go`：适配器接口、注册表、来源隔离扫描。
- `internal/source/scope.go`：跨来源 Scope 聚合、稳定哈希和安全标签。
- `internal/source/stream.go`：统一 JSONL 事件编码和带限制的只读流辅助函数。
- `internal/source/claude/adapter.go`：Claude Code 发现及事件转换。
- `internal/source/codex/adapter.go`：Codex 发现及事件转换。
- `internal/source/cursor/adapter.go`：Cursor 发现及事件转换。
- `internal/source/opencode/adapter.go`：OpenCode 发现及事件转换。
- `internal/source/copilot/adapter.go`：VS Code Copilot 发现及事件转换。
- `internal/source/flicker/adapter.go`：MyFlicker/CodeFlicker JSONL 与只读 SQLite 发现。
- `internal/source/flicker/sqlite.go`：纯 Go SQLite 查询和 blob 读取。
- `internal/source/detect/adapter.go`：未验证来源的安全目录检测，只产生不可选择状态。
- `internal/source/testdata/`：迁移后的脱敏 fixture。
- `internal/upload/canonical.go`：v2 canonical serialization 与 SHA-256。
- `internal/upload/summarizer.go`：YC 风格工具输入摘要。
- `internal/service/client.go`：验证、授权、上传、任务及海报接口模型。
- `internal/service/http.go`：可信 HTTPS HR-B 客户端。
- `internal/service/mock.go`：现有 Mock 服务的接口适配。
- `internal/service/http_test.go`、`internal/service/mock_test.go`：服务契约测试。
- `tests/test_single_binary_contract.py`：仓库级单文件与文档契约测试。

### 修改

- `cmd/kuai/main.go`、`cmd/kuai/main_test.go`：注册内置来源并删除外部进程依赖。
- `internal/config/config.go`、`internal/config/config_test.go`：删除 agentsview 配置，增加显式服务模式和可信 HR-B URL。
- `internal/upload/model.go`、`internal/upload/exporter.go`、对应测试：升级 v2 包并直接读取 source adapter。
- `internal/upload/redactor.go`、`internal/upload/redactor_test.go`：补齐 YC secret、路径和 PII 规则。
- `internal/webapp/routes.go`、`internal/webapp/server.go`、对应测试：Project API 改为 Scope API，注入 ServiceClient。
- `internal/webapp/assets/index.html`、`app.js`、`styles.css`：Assessment Scope、服务域名提示、上传成功和直接下载。
- `internal/mocksvc/*`：保留 Mock 行为，通过 service adapter 暴露。
- `install.sh`、`install.ps1`、安装器测试：只下载 `kuai`。
- `scripts/build-kuai-release.sh`、构建测试：验证单文件与无外部依赖。
- `README.md`、`kuai.md`：统一单文件安装、运行和隐私说明。
- `go.mod`、`go.sum`：加入纯 Go SQLite 驱动。

### 删除

- `internal/agentsview/client.go`
- `internal/agentsview/client_test.go`
- `internal/agentsview/projects.go`
- `internal/agentsview/projects_test.go`
- 未跟踪且无引用的 `avsore.md`

`internal/agentsview` 中仍有价值的 Scope 聚合测试先迁移到 `internal/source`，再删除旧目录。

---

### 任务 1：建立 Source Adapter 与 Assessment Scope 公共契约

**文件：**
- 创建：`internal/source/model.go`
- 创建：`internal/source/adapter.go`
- 创建：`internal/source/scope.go`
- 测试：`internal/source/adapter_test.go`
- 测试：`internal/source/scope_test.go`

- [ ] **步骤 1：编写失败的来源隔离测试**

```go
func TestRegistryScanIsolatesAdapterFailures(t *testing.T) {
    registry := source.NewRegistry(
        fakeAdapter{product: "claude-code", sessions: []source.Session{{ID: "claude:1", Product: "claude-code", Scope: source.ScopeRef{Type: source.ScopeProject, Root: "/work/a"}}}},
        fakeAdapter{product: "cursor", err: errors.New("broken fixture")},
    )
    result := registry.Scan(context.Background())
    if len(result.Sessions) != 1 || result.Sessions[0].ID != "claude:1" {
        t.Fatalf("sessions = %#v", result.Sessions)
    }
    if result.Sources["cursor"].Status != source.StatusFailed {
        t.Fatalf("cursor status = %q", result.Sources["cursor"].Status)
    }
}
```

- [ ] **步骤 2：运行测试并确认因 `internal/source` 不存在而失败**

运行：`go test ./internal/source -run TestRegistryScanIsolatesAdapterFailures -count=1`

预期：FAIL，编译器报告 `source.NewRegistry` 或相关类型未定义。

- [ ] **步骤 3：实现最小公共类型与注册表**

```go
type Capability string
type ScopeType string
const (
    ScopeProject ScopeType = "project"
    ScopeWorkspace ScopeType = "workspace"
    ScopeConversationGroup ScopeType = "conversation_group"
    ScopeSessionCollection ScopeType = "session_collection"
)
type ScopeRef struct { Type ScopeType; Root, Label string }
type Session struct {
    ID, Product, FormatVersion, AdapterVersion string
    Capabilities []Capability
    Scope ScopeRef
    StartedAt, EndedAt time.Time
    MessageCount int
    OpaqueRef string
}
type Adapter interface {
    Product() string
    Capabilities() []Capability
    Discover(context.Context) ([]Session, error)
    Open(context.Context, Session) (io.ReadCloser, error)
}
```

`Registry.Scan` 必须逐个调用适配器、收集每个来源状态，且不能因一个错误丢弃其他来源。

- [ ] **步骤 4：编写并验证 Scope 聚合失败测试**

测试同一规范化文件夹下 Claude/Codex 合并为一个 `project`，上传 JSON 中不出现 `/Users/alice`；没有路径的 Hermes session 形成 `conversation_group`。

运行：`go test ./internal/source -run 'Test(GroupScopes|RegistryScan)' -count=1`

预期：首次 FAIL，完成 `scope.go` 后 PASS。

- [ ] **步骤 5：运行包测试并提交**

运行：`go test ./internal/source -count=1`

预期：PASS。

```bash
git add internal/source
git commit -m "feat: define local source adapter contracts"
```

---

### 任务 2：迁移 Claude Code、Codex、Cursor 只读适配器

**文件：**
- 创建：`internal/source/claude/adapter.go`
- 创建：`internal/source/claude/adapter_test.go`
- 创建：`internal/source/codex/adapter.go`
- 创建：`internal/source/codex/adapter_test.go`
- 创建：`internal/source/cursor/adapter.go`
- 创建：`internal/source/cursor/adapter_test.go`
- 创建：`internal/source/testdata/claude/valid.jsonl`
- 创建：`internal/source/testdata/codex/standard.jsonl`
- 创建：`internal/source/testdata/cursor/session.json`

- [ ] **步骤 1：迁移最小脱敏 fixture**

从以下已验证样本提取结构并替换所有真实文本、路径和 ID：

```text
/Users/liuyuxiang05/Projects/agentsview/internal/parser/testdata/claude/valid_session.jsonl
/Users/liuyuxiang05/Projects/agentsview/internal/parser/testdata/codex/standard_session.jsonl
/Users/liuyuxiang05/Projects/agentsview/internal/parser/cursor_test.go
```

fixture 只保留格式字段和虚构值 `/workspace/campus-app`、`session-1`。

- [ ] **步骤 2：编写三个适配器的失败契约测试**

每个测试必须验证：

```go
if got.Product != "claude-code" { t.Fatal(got.Product) }
if got.Scope.Type != source.ScopeProject || got.Scope.Root != "/workspace/campus-app" { t.Fatal(got.Scope) }
if !slices.Contains(got.Capabilities, source.CapabilityTool) { t.Fatal(got.Capabilities) }
events, err := readAllEvents(adapter, got)
if err != nil || len(events) == 0 { t.Fatalf("events=%d err=%v", len(events), err) }
```

Codex ID 使用 `codex:`，Cursor 使用 `cursor:`，Claude 使用 `claude-code:`。

- [ ] **步骤 3：运行失败测试**

运行：`go test ./internal/source/claude ./internal/source/codex ./internal/source/cursor -count=1`

预期：FAIL，三个 Adapter 尚未定义。

- [ ] **步骤 4：移植最小解析逻辑**

只迁移以下职责：

- Claude：`~/.claude/projects` JSONL 发现、cwd/project、消息、tool use/result。
- Codex：`~/.codex/sessions` 与 `~/.codex/archived_sessions` JSONL、`session_meta`、`response_item`。
- Cursor：可信 workspace storage/会话记录发现及消息转换。

不得迁移 agentsview DB、统计、价格、Web 或 sync engine。

- [ ] **步骤 5：增加损坏、截断和重复 ID 测试**

损坏 entry 被计入安全质量标记；无法产生最低 envelope 时该 session 不可用。相同文件重复发现必须去重且排序确定。

- [ ] **步骤 6：运行测试并提交**

运行：`go test ./internal/source/... -count=1`

预期：PASS。

```bash
git add internal/source
git commit -m "feat: embed claude codex and cursor sources"
```

---

### 任务 3：迁移 OpenCode 与 VS Code Copilot

**文件：**
- 创建：`internal/source/opencode/adapter.go`
- 创建：`internal/source/opencode/adapter_test.go`
- 创建：`internal/source/copilot/adapter.go`
- 创建：`internal/source/copilot/adapter_test.go`
- 创建：`internal/source/testdata/opencode/session.json`
- 创建：`internal/source/testdata/copilot/session.jsonl`

- [ ] **步骤 1：根据 YC normalizer 写失败测试**

参考：

```text
/Users/liuyuxiang05/Projects/paxel-extracted/rails/app/services/opencode_normalizer.rb
/Users/liuyuxiang05/Projects/paxel-extracted/rails/app/services/vscode_copilot_normalizer.rb
```

测试必须验证系统 prompt 不作为用户消息、tool use 与 tool result 顺序相邻、来源分别为 `opencode` 与 `vscode-copilot`。

- [ ] **步骤 2：运行测试确认失败**

运行：`go test ./internal/source/opencode ./internal/source/copilot -count=1`

预期：FAIL，Adapter 未定义。

- [ ] **步骤 3：实现格式签名与只读映射**

OpenCode 和 Copilot 必须先匹配各自固定元数据签名。未知版本只返回 detected/unsupported，不尝试宽松猜测。

- [ ] **步骤 4：运行全来源测试并提交**

运行：`go test ./internal/source/... -count=1`

预期：PASS。

```bash
git add internal/source
git commit -m "feat: embed opencode and copilot sources"
```

---

### 任务 4：迁移 CodeFlicker 与 MyFlicker，保持纯 Go 单文件

**文件：**
- 创建：`internal/source/flicker/adapter.go`
- 创建：`internal/source/flicker/sqlite.go`
- 创建：`internal/source/flicker/adapter_test.go`
- 创建：`internal/source/flicker/sqlite_test.go`
- 创建：`internal/source/testdata/flicker/session.jsonl`
- 修改：`go.mod`
- 修改：`go.sum`

- [ ] **步骤 1：写 JSONL 与只读 SQLite 失败测试**

SQLite 测试创建临时 `composer_data.sqlite`，插入 `KwaipilotKV` 的 `composerData:session-1` JSON blob，关闭写连接后再调用 adapter。断言：

```go
if got.Product != "codeflicker" { t.Fatal(got.Product) }
if got.ID != "codeflicker:session-1" { t.Fatal(got.ID) }
if fileChangedAfterScan(dbPath) { t.Fatal("read-only scan modified source database") }
```

JSONL fixture 断言来源为 `myflicker`，ID 命名空间不同。

- [ ] **步骤 2：运行测试确认失败**

运行：`go test ./internal/source/flicker -count=1`

预期：FAIL，SQLite 驱动和 adapter 未定义。

- [ ] **步骤 3：加入纯 Go SQLite 驱动**

运行：`go get modernc.org/sqlite`

只在 `sqlite.go` 中匿名导入驱动。连接字符串必须启用只读模式；执行查询仅允许：

```sql
SELECT key, updatedAt FROM KwaipilotKV WHERE key LIKE 'composerData:%'
SELECT value FROM KwaipilotKV WHERE key = ?
```

- [ ] **步骤 4：移植 Flicker blob/JSONL 最小解析**

参考：

```text
/Users/liuyuxiang05/Projects/agentsview/internal/parser/myflicker.go
/Users/liuyuxiang05/Projects/agentsview/internal/parser/myflicker_db.go
```

不得迁移 CGO `github.com/mattn/go-sqlite3`。

- [ ] **步骤 5：验证跨平台静态构建并提交**

运行：

```bash
go test ./internal/source/flicker -count=1
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/kuai-plan-linux ./cmd/kuai
```

预期：PASS，构建成功且不要求 C 编译器。

```bash
git add go.mod go.sum internal/source/flicker internal/source/testdata/flicker
git commit -m "feat: embed flicker source readers"
```

---

### 任务 5：增加长尾 Agent 安全检测注册表

**文件：**
- 创建：`internal/source/detect/adapter.go`
- 创建：`internal/source/detect/adapter_test.go`
- 创建：`internal/source/detect/catalog.go`

- [ ] **步骤 1：编写目录检测不等于可上传的失败测试**

目录目录表包含：

```go
[]Definition{
    {Product: "openclaw"}, {Product: "hermes-agent"}, {Product: "workbuddy"},
    {Product: "trae"}, {Product: "trae-work"},
    {Product: "kimi-cli"}, {Product: "kimi-work"}, {Product: "kimi-code"},
    {Product: "qwen-code"}, {Product: "tongyi-lingma"},
    {Product: "qoder"}, {Product: "qoder-work"},
    {Product: "codebuddy"},
}
```

测试创建虚构 home 下的来源目录，断言状态为 `detected_unsupported`、session 数为零且页面不可选择。

- [ ] **步骤 2：运行测试确认失败**

运行：`go test ./internal/source/detect -count=1`

预期：FAIL，catalog 不存在。

- [ ] **步骤 3：实现无正文读取的安全检测**

检测器只能检查已声明目录是否存在，不打开 session 正文。真实支持必须在后续任务中以 fixture 和专用 Adapter 替换 catalog 项。

- [ ] **步骤 4：运行并提交**

运行：`go test ./internal/source/detect ./internal/source/... -count=1`

预期：PASS。

```bash
git add internal/source/detect
git commit -m "feat: detect additional local agent sources safely"
```

---

### 任务 6：升级 YC Upload Package v2 与 canonical digest

**文件：**
- 修改：`internal/upload/model.go`
- 修改：`internal/upload/exporter.go`
- 修改：`internal/upload/exporter_test.go`
- 创建：`internal/upload/canonical.go`
- 创建：`internal/upload/canonical_test.go`

- [ ] **步骤 1：编写 v2 JSON 失败测试**

```go
func TestPackageV2IncludesClientScopeAndSource(t *testing.T) {
    pkg := upload.Package{SchemaVersion: 2, Client: upload.Client{Name: "kuai", Version: "1.0.0", Platform: "darwin-arm64"}}
    encoded, _ := json.Marshal(pkg)
    for _, key := range []string{`"client"`, `"scope"`, `"source"`, `"schema_version":2`} {
        if !bytes.Contains(encoded, []byte(key)) { t.Fatalf("missing %s: %s", key, encoded) }
    }
}
```

- [ ] **步骤 2：运行测试确认当前 v1 结构失败**

运行：`go test ./internal/upload -run 'TestPackageV2|TestCanonical' -count=1`

预期：FAIL，`Client`、`Scope`、`Source` 未定义。

- [ ] **步骤 3：实现 v2 模型**

`Package` 使用 `Client`、`Scope`、`Sessions`、`Redaction`、`CreatedAt`；每个 Session 使用 `Source{Product,FormatVersion,AdapterVersion,Capabilities}`。

- [ ] **步骤 4：实现确定性 canonical bytes 与 digest**

事件中 map 的 JSON 键必须确定性排序；禁止浮点 NaN/Inf。API：

```go
func CanonicalBytes(Package) ([]byte, error)
func Digest(Package) (hexDigest string, size int64, err error)
```

- [ ] **步骤 5：让 Exporter 读取 Adapter.Open**

删除外部 argv/process runner；Exporter 接收 `source.Registry`，按 Scope session 顺序逐个打开，并保留原有单行、session 和包大小限制。

- [ ] **步骤 6：运行并提交**

运行：`go test ./internal/upload -count=1`

预期：PASS。

```bash
git add internal/upload
git commit -m "feat: build deterministic upload package v2"
```

---

### 任务 7：迁移 YC 工具摘要与脱敏规则

**文件：**
- 创建：`internal/upload/summarizer.go`
- 创建：`internal/upload/summarizer_test.go`
- 修改：`internal/upload/redactor.go`
- 修改：`internal/upload/redactor_test.go`

- [ ] **步骤 1：先写泄漏失败测试**

表驱动样本覆盖：

```text
sk-ant-api03-abcdefghijklmnopqrstuvwxyz
sk-proj-abcdefghijklmnopqrstuvwxyz
AKIAABCDEFGHIJKLMNOP
ghp_abcdefghijklmnopqrstuvwxyz
eyJaaa.eyJbbb.signature
-----BEGIN PRIVATE KEY-----...-----END PRIVATE KEY-----
postgres://alice:secret@db.example/app
Bearer abcdefghijklmnopqrstuvwxyz
OPENAI_API_KEY=secret-value-123456
/Users/张三/campus-app
C:\Users\Alice\campus-app
13800138000
alice@example.edu
```

断言输出不含原值，且 replacements 计数大于零。

- [ ] **步骤 2：写工具摘要失败测试**

验证 Read result 变成 `[OMITTED_READ_RESULT]`；Write/Edit 不含 body；Task 不含 prompt；未知工具只包含键名。

- [ ] **步骤 3：运行测试并确认失败**

运行：`go test ./internal/upload -run 'Test(RedactYC|SummarizeTool)' -count=1`

预期：FAIL，当前规则未覆盖全部 YC 模式或 summarizer 不存在。

- [ ] **步骤 4：按固定顺序迁移规则**

规则来源：

```text
/Users/liuyuxiang05/Projects/paxel-extracted/rails/app/services/secret_scrubber.rb
/Users/liuyuxiang05/Projects/paxel-extracted/rails/app/services/decision_text_redactor.rb
/Users/liuyuxiang05/Projects/paxel-extracted/rails/app/services/tool_input_summarizer.rb
```

Go 实现不得依赖 Ruby 专有正则特性；每个转换都由对应测试固定。递归深度超限或非 UTF-8 必须返回错误。

- [ ] **步骤 5：运行泄漏扫描和包测试**

运行：`go test ./internal/upload -count=1`

预期：PASS。

- [ ] **步骤 6：提交**

```bash
git add internal/upload
git commit -m "feat: harden local session redaction"
```

---

### 任务 8：用内置 Registry 启动 kuai，删除 agentsview 运行时依赖

**文件：**
- 修改：`cmd/kuai/main.go`
- 修改：`cmd/kuai/main_test.go`
- 修改：`internal/config/config.go`
- 修改：`internal/config/config_test.go`
- 删除：`internal/agentsview/client.go`
- 删除：`internal/agentsview/client_test.go`
- 删除：`internal/agentsview/projects.go`
- 删除：`internal/agentsview/projects_test.go`

- [ ] **步骤 1：写 main 不解析外部二进制的失败测试**

测试 dependencies 中不再出现 `resolveExecutable`/`newSessionClient`；注入 fake registry 后能够启动 server。

- [ ] **步骤 2：写配置拒绝旧 flags 的测试**

```go
for _, args := range [][]string{{"--agentsview-path", "/tmp/bin"}, {"--skip-sync"}} {
    if _, err := config.Load(args); err == nil { t.Fatalf("%v unexpectedly accepted", args) }
}
```

- [ ] **步骤 3：运行测试确认失败**

运行：`go test ./cmd/kuai ./internal/config -count=1`

预期：FAIL，当前代码仍接受并要求 agentsview。

- [ ] **步骤 4：注册内置适配器并删除进程路径**

`productionDependencies` 创建 Registry；扫描结果直接构建 Scope map 和 Session map；Exporter 使用同一 Registry 打开选定 session。

- [ ] **步骤 5：删除旧包并验证没有运行时引用**

运行：

```bash
rg -n "resolveAgentsview|KUAI_AGENTSVIEW_PATH|--agentsview-path|--skip-sync|exec\\.Command.*agentsview" cmd internal
```

预期：无匹配。

- [ ] **步骤 6：运行并提交**

运行：`go test ./cmd/kuai ./internal/config ./internal/source/... ./internal/upload -count=1`

预期：PASS。

```bash
git add cmd/kuai internal/config internal/source internal/upload internal/agentsview
git commit -m "refactor: run kuai with embedded source adapters"
```

---

### 任务 9：将页面从 Project 升级为 Assessment Scope

**文件：**
- 修改：`internal/webapp/routes.go`
- 修改：`internal/webapp/server.go`
- 修改：`internal/webapp/flow_test.go`
- 修改：`internal/webapp/server_test.go`
- 修改：`internal/webapp/assets/index.html`
- 修改：`internal/webapp/assets/app.js`
- 修改：`internal/webapp/assets/styles.css`
- 修改：`internal/webapp/assets_test.go`
- 修改：`tests/session_selection_interactions.js`

- [ ] **步骤 1：写 Scope API 失败测试**

`GET /api/scopes` 返回 `type`、`key`、`label`、`agents`、`session_count`、`capabilities`、`selectable`，且响应不含 `/Users/`、`\Users\` 或 session ID。

- [ ] **步骤 2：写默认不选择及失败隔离交互测试**

JS 测试断言初始 `selectedScope === null`，detected unsupported 卡片 disabled，选择一个 Scope 后才调用 `/api/prepare`。

- [ ] **步骤 3：运行测试确认失败**

运行：

```bash
go test ./internal/webapp -count=1
node --test tests/session_selection_interactions.js
```

预期：FAIL，现有 API 和文案仍为 Project。

- [ ] **步骤 4：实现 Scope API 和已批准视觉**

使用暖米白 `#FBF5ED`、正文 `#252321`、品牌橙 `#FF6518`、辅助灰 `#8F8983`。页面显示 Scope 类型、Agent、session 数和能力摘要，不显示绝对路径。

- [ ] **步骤 5：保留 30 秒异步切换与直接下载**

海报按钮必须设置 `download` 或通过 Blob URL 触发下载；不得绑定 modal、lightbox 或 window.open。

- [ ] **步骤 6：运行并提交**

运行：

```bash
go test ./internal/webapp -count=1
node --test tests/session_selection_interactions.js tests/report_interactions.js
```

预期：PASS。

```bash
git add internal/webapp tests/session_selection_interactions.js tests/report_interactions.js
git commit -m "feat: select assessment scopes in local app"
```

---

### 任务 10：抽象 Mock 与正式 HTTPS ServiceClient

**文件：**
- 创建：`internal/service/client.go`
- 创建：`internal/service/mock.go`
- 创建：`internal/service/mock_test.go`
- 创建：`internal/service/http.go`
- 创建：`internal/service/http_test.go`
- 修改：`internal/config/config.go`
- 修改：`internal/config/config_test.go`
- 修改：`internal/webapp/server.go`
- 修改：`internal/webapp/routes.go`

- [ ] **步骤 1：写接口契约与 Mock 失败测试**

Mock 测试依次执行 `RequestOTP`、`VerifyOTP`、`SubmitConsent`、`CreateUpload`、`Upload`、`CompleteUpload`、`GetTask`、`DownloadPoster`，验证未授权时 `CreateUpload` 失败。

- [ ] **步骤 2：写 HTTPS 安全失败测试**

使用 `httptest` 验证：

- `http://` base URL 被拒绝；
- 重定向到另一 host 被拒绝；
- Authorization 不进入 URL；
- 同一 idempotency key 重试使用相同 task；
- digest/bytes/session/schema 完整提交。

- [ ] **步骤 3：运行测试确认失败**

运行：`go test ./internal/service -count=1`

预期：FAIL，package 不存在。

- [ ] **步骤 4：实现 ServiceClient**

正式模式只由显式 `--service-mode=http --service-url=https://...` 开启；单独设置环境变量不得切换模式。页面 bootstrap 显示当前模式和可信域名。

- [ ] **步骤 5：将 webapp 路由改为接口调用**

页面保持本地 prepared package，但验证、授权、创建上传、完成确认、任务查询和海报下载均通过 ServiceClient。

- [ ] **步骤 6：运行并提交**

运行：`go test ./internal/service ./internal/webapp ./internal/config -count=1`

预期：PASS。

```bash
git add internal/service internal/webapp internal/config
git commit -m "feat: add mock and trusted hr-b clients"
```

---

### 任务 11：只发布和安装一个 kuai

**文件：**
- 修改：`install.sh`
- 修改：`install.ps1`
- 修改：`tests/test_kuai_install_sh.sh`
- 修改：`tests/Test-KuaiInstall.ps1`
- 修改：`scripts/build-kuai-release.sh`
- 修改：`tests/test_build_kuai_release_sh.sh`
- 创建：`tests/test_single_binary_contract.py`

- [ ] **步骤 1：先改测试要求单文件**

shell/PowerShell 测试必须断言只请求 `KUAI_RELEASE_URL/SHA256SUMS` 和一个 `kuai-*`，不访问 agentsview URL、不创建 agentsview 文件。

仓库契约测试：

```python
for path in ("install.sh", "install.ps1", "kuai.md"):
    self.assertNotIn("KUAI_AGENTSVIEW", (ROOT / path).read_text())
self.assertNotRegex((ROOT / "cmd/kuai/main.go").read_text(), r"exec\\.Command.*agentsview")
```

- [ ] **步骤 2：运行测试确认失败**

运行：

```bash
bash tests/test_kuai_install_sh.sh
python3 -m unittest tests.test_single_binary_contract
```

预期：FAIL，当前安装器仍下载 agentsview。

- [ ] **步骤 3：简化安装器事务**

只保留 kuai 下载、checksum、原文件备份、原子替换和 PATH 更新。不得删除用户已安装的独立 agentsview；安装器只是不再管理它。

- [ ] **步骤 4：强化构建检查**

构建脚本完成后验证 dist 只有六个 kuai 和一个 SHA256SUMS；对二进制字符串/源码契约检查不得存在运行时 agentsview 路径。

- [ ] **步骤 5：运行并提交**

运行：

```bash
bash tests/test_kuai_install_sh.sh
python3 -m unittest tests.test_single_binary_contract
bash tests/test_build_kuai_release_sh.sh
```

预期：PASS。

```bash
git add install.sh install.ps1 scripts/build-kuai-release.sh tests
git commit -m "build: ship kuai as a single binary"
```

---

### 任务 12：更新 Skill、README 并删除旧 typo 文件

**文件：**
- 修改：`README.md`
- 修改：`kuai.md`
- 修改：`tests/test_docs.py`
- 删除：`avsore.md`

- [ ] **步骤 1：写文档失败测试**

新增 kuai 专用断言：

```python
for phrase in ("单一 Go 可执行文件", "Assessment Scope", "不默认选择", "本地脱敏", "HR-B"):
    self.assertIn(phrase, kuai_readme + kuai_skill)
for forbidden in ("Claude 和 agentreview", "安装 kuai 和 agentsview", "KUAI_AGENTSVIEW_PATH"):
    self.assertNotIn(forbidden, kuai_readme + kuai_skill)
```

旧 avscore 文档测试继续只约束 avscore 章节，不允许其要求影响 kuai。

- [ ] **步骤 2：运行测试确认失败**

运行：`python3 -m unittest tests.test_docs`

预期：FAIL，当前 kuai 文档仍描述双二进制和 agentreview。

- [ ] **步骤 3：更新文档和 Skill**

Skill 固定流程：

```text
检查 command -v kuai / Get-Command kuai
→ 用户同意后运行可信单文件安装器
→ 运行 kuai
→ 用户在本地页面选择一个 Assessment Scope
```

Skill 不复制扫描、脱敏或上传实现。

- [ ] **步骤 4：删除 `avsore.md` 并运行测试**

仅删除未跟踪、无引用的 `/Users/liuyuxiang05/atr/avsore.md`；不删除 `avscore.md`。

运行：`python3 -m unittest tests.test_docs`

预期：PASS。

- [ ] **步骤 5：提交**

```bash
git add README.md kuai.md tests/test_docs.py
git commit -m "docs: document single-binary kuai workflow"
```

---

### 任务 13：长尾来源 fixture 接入循环

**文件：**
- 创建：`internal/source/openclaw/adapter.go`、`adapter_test.go`
- 创建：`internal/source/hermes/adapter.go`、`adapter_test.go`
- 创建：`internal/source/workbuddy/adapter.go`、`adapter_test.go`
- 创建：`internal/source/trae/adapter.go`、`adapter_test.go`
- 创建：`internal/source/kimi/adapter.go`、`adapter_test.go`
- 创建：`internal/source/qwen/adapter.go`、`adapter_test.go`
- 创建：`internal/source/qoder/adapter.go`、`adapter_test.go`
- 创建：`internal/source/codebuddy/adapter.go`、`adapter_test.go`
- 创建：`internal/source/testdata/openclaw/`
- 创建：`internal/source/testdata/hermes-agent/`
- 创建：`internal/source/testdata/workbuddy/`
- 创建：`internal/source/testdata/trae/`、`internal/source/testdata/trae-work/`
- 创建：`internal/source/testdata/kimi-cli/`、`internal/source/testdata/kimi-work/`、`internal/source/testdata/kimi-code/`
- 创建：`internal/source/testdata/qwen-code/`、`internal/source/testdata/tongyi-lingma/`
- 创建：`internal/source/testdata/qoder/`、`internal/source/testdata/qoder-work/`
- 创建：`internal/source/testdata/codebuddy/`
- 修改：`internal/source/detect/catalog.go`

此任务对以下每个具体来源分别执行一次，不能把多个未验证格式合并成一次提交：

```text
openclaw
hermes-agent
workbuddy
trae
trae-work
kimi-cli
kimi-work
kimi-code
qwen-code
tongyi-lingma
qoder
qoder-work
codebuddy
```

- [ ] **步骤 1：取得真实脱敏 fixture**

fixture 必须由对应产品实际 session 生成；删除用户正文、绝对路径、账号和 secret，但保留格式字段、父子关系和工具结构。没有真实 fixture 时停止该来源的实现，保持 `detected_unsupported`。

- [ ] **步骤 2：写该来源失败契约测试**

测试固定产品名、格式签名、Scope 类型、事件顺序、能力声明、损坏/截断/超限行为和零泄漏。

- [ ] **步骤 3：运行测试确认因专用 Adapter 不存在而失败**

根据正在接入的来源运行对应命令：

```bash
go test ./internal/source/openclaw -count=1
go test ./internal/source/hermes -count=1
go test ./internal/source/workbuddy -count=1
go test ./internal/source/trae -count=1
go test ./internal/source/kimi -count=1
go test ./internal/source/qwen -count=1
go test ./internal/source/qoder -count=1
go test ./internal/source/codebuddy -count=1
```

预期：FAIL，专用 Adapter 尚未实现。

- [ ] **步骤 4：实现最小专用适配器**

只读取该 fixture 证明的字段；不可得字段声明 unavailable，不能用零值表示行为未发生。共享家族组件只在两个实际格式相同且测试证明后提取。

- [ ] **步骤 5：从 detect catalog 升级为可评估**

只有专用测试和全局泄漏测试均通过后，删除该产品的 `detected_unsupported` 项并注册真实 Adapter。

- [ ] **步骤 6：运行并按来源提交**

运行：

```bash
go test ./internal/source/... ./internal/upload -count=1
```

预期：PASS。

提交信息使用实际产品，例如：

```bash
git add internal/source
git commit -m "feat: support local openclaw sessions"
```

---

### 任务 14：完整验证与发版产物检查

**文件：**
- 修改：仅修复本计划引入的验证问题

- [ ] **步骤 1：运行格式和静态检查**

```bash
gofmt -w cmd/kuai internal/source internal/upload internal/service internal/webapp internal/config
go vet ./...
git diff --check
```

预期：全部成功，无输出错误。

- [ ] **步骤 2：运行完整自动化测试**

```bash
go test ./...
python3 -m unittest discover -s tests
node --test tests/session_selection_interactions.js tests/report_interactions.js tests/application_interactions.js
bash tests/test_kuai_install_sh.sh
bash tests/test_build_kuai_release_sh.sh
```

预期：全部 PASS。

- [ ] **步骤 3：构建正式产物**

```bash
sh scripts/build-kuai-release.sh
```

预期：`dist/` 中有六个 kuai 二进制和 `SHA256SUMS`，没有 agentsview。

- [ ] **步骤 4：运行单文件 smoke test**

使用临时 `KUAI_DATA_DIR` 和 fixture home 启动构建后的本机二进制，验证：

```text
启动成功
GET /api/scopes 返回至少一个可选择 Scope
页面响应不含绝对路径
Mock 完成验证、授权、上传和海报下载
进程列表不存在 agentsview
```

- [ ] **步骤 5：确认工作区只包含已审查变更**

运行：`git status --short`

预期：没有因 smoke test 生成的状态文件、fixture home、临时二进制或未审查产物；功能代码已在前述任务的精确提交中提交。
