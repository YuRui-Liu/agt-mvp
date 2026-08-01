# kuAI 多智能体本地会话支持扩展实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在真正单文件 `kuai` 中可靠扫描、选择并安全导出已批准的国内外 Agent 本地会话，同时准确展示验证等级和暂不支持原因。

**架构：** 保持 `source.Adapter` 为产品隔离边界，每个产品内部只解析经过 fixture 或本机结构验证的格式；`Registry` 统一拥有运行状态，`catalog` 统一拥有产品元数据和验证等级。`Discover` 只允许有界流式识别，不返回、记录或长期缓存正文；用户选择 Assessment Scope 后，`Open` 重新校验快照并输出统一事件流，继续由上传层过滤和递归脱敏。

**技术栈：** Go 1.24、`modernc.org/sqlite`、嵌入式 HTML/CSS/JavaScript、Node.js DOM 测试、Python `unittest`、Shell 构建测试。

---

## 文件结构与并行边界

任务 1-2 是串行共享底座。之后可以分四个互不编辑对方文件的并行组：任务 3-4、任务 5-6、任务 7、任务 8-9。任务 10-13 再串行汇总。

当前工作树中 `README.md`、`tests/test_docs.py`、构建脚本及若干未跟踪文件已有用户修改；执行者必须保留它们，不得清理、覆盖或顺带提交。

### 任务 1：统一来源状态、验证等级与错误分类

**文件：**
- 修改：`internal/source/model.go`
- 修改：`internal/source/adapter.go`
- 修改：`internal/source/adapter_test.go`
- 修改：`internal/source/catalog/catalog.go`
- 修改：`internal/source/catalog/catalog_test.go`

- [ ] **步骤 1：编写 Registry 状态分类失败测试**

```go
func TestRegistryClassifiesSourceStates(t *testing.T) {
    cases := []struct {
        name string
        sessions []Session
        err error
        want SourceState
    }{
        {name: "not found", want: SourceNotFound},
        {name: "format", err: NewDiscoveryError(SourceFormatUnsupported, errors.New("private schema")), want: SourceFormatUnsupported},
        {name: "export", err: NewDiscoveryError(SourceExportRequired, errors.New("private path")), want: SourceExportRequired},
        {name: "read", err: errors.New("/private/file"), want: SourceReadError},
        {name: "ready", sessions: []Session{{ID: "s"}}, want: SourceReady},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got, err := NewRegistry(testAdapter{product: "agent", sessions: tc.sessions, err: tc.err}).Scan(context.Background())
            if err != nil { t.Fatal(err) }
            status := got.Sources["agent"]
            if status.State != tc.want { t.Fatalf("status=%#v", status) }
            if strings.Contains(status.Code, "private") { t.Fatalf("leaked status=%#v", status) }
        })
    }
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/source -run TestRegistryClassifiesSourceStates -count=1`
预期：FAIL，缺少新状态与 `NewDiscoveryError`。

- [ ] **步骤 3：实现统一类型和 fail-closed 映射**

在 `model.go` 添加：

```go
const (
    CapabilityMessages Capability = "messages"
    CapabilityTools Capability = "tools"
    CapabilityReasoning Capability = "reasoning"
    CapabilityAttachments Capability = "attachments"
)
type Verification string
const (
    VerificationMachine Verification = "machine_verified"
    VerificationFixture Verification = "fixture_verified"
    VerificationExport Verification = "export_required"
    VerificationUnsupported Verification = "unsupported"
)
```

在 `adapter.go` 添加 `ready/not_found/export_required/format_unsupported/read_error/detected_unsupported` 常量、`DiscoveryError` 和 `NewDiscoveryError`。零 session 映射为 `not_found`；只允许适配器声明 format/export；未知错误统一为 `read_error/read_failed`，绝不暴露原始错误。

- [ ] **步骤 4：扩展 catalog Definition 并测试**

```go
type Definition struct {
    Product string `json:"product"`
    DisplayName string `json:"displayName"`
    Status source.SourceState `json:"status"`
    Verification source.Verification `json:"verification"`
    Capabilities []source.Capability `json:"capabilities,omitempty"`
    Reason string `json:"reason,omitempty"`
    // 保留 EnvVar、DefaultDirs、Supported、Enabled、Detected、Dirs。
}
```

原因仅允许固定码：`official_export_required`、`no_verified_session_schema`、`no_verified_transcript_body`、`no_distinct_local_format`。

- [ ] **步骤 5：验证并提交**

运行：`go test ./internal/source ./internal/source/catalog -count=1`
预期：PASS。

```bash
git add internal/source/model.go internal/source/adapter.go internal/source/adapter_test.go internal/source/catalog/catalog.go internal/source/catalog/catalog_test.go
git commit -m "feat: model local source support states"
```

### 任务 2：补齐共享文件与 SQLite 安全边界

**文件：**
- 修改：`internal/source/internal/safeopen/open_unix.go`
- 修改：`internal/source/internal/safeopen/open_test.go`
- 创建：`internal/source/internal/sqliteread/readonly.go`
- 创建：`internal/source/internal/sqliteread/readonly_test.go`
- 修改：`internal/source/internal/adaptertest/contract.go`
- 创建：`internal/source/internal/adaptertest/contract_test.go`

- [ ] **步骤 1：编写符号链接根与 WAL 只读事务失败测试**

```go
func makeWALFixture(t *testing.T) (string, string) {
    t.Helper()
    root := t.TempDir()
    path := filepath.Join(root, "sessions.db")
    writer, err := sql.Open("sqlite", path)
    if err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = writer.Close() })
    if _, err := writer.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE message(id TEXT); INSERT INTO message VALUES ('m1')`); err != nil {
        t.Fatal(err)
    }
    return root, path
}

func TestWithReadOnlyTxSeesCommittedWALAndCannotWrite(t *testing.T) {
    root, path := makeWALFixture(t)
    err := WithReadOnlyTx(context.Background(), root, path, 8<<20, func(tx *sql.Tx) error {
        var count int
        if err := tx.QueryRow(`SELECT count(*) FROM message`).Scan(&count); err != nil || count != 1 { t.Fatal(err) }
        if _, err := tx.Exec(`DELETE FROM message`); err == nil { t.Fatal("write succeeded") }
        return nil
    })
    if err != nil { t.Fatal(err) }
}
```

另在 `open_test.go` 添加 `TestOpenRejectsSymlinkRoot`。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/source/internal/safeopen ./internal/source/internal/sqliteread -count=1`
预期：FAIL，Unix root 仍可能跟随 symlink，且 sqliteread 不存在。

- [ ] **步骤 3：实现安全读取工具**

Unix 打开 root 使用 `O_NOFOLLOW|O_DIRECTORY`。实现：

```go
func WithReadOnlyTx(ctx context.Context, root, path string, maxBytes int64, fn func(*sql.Tx) error) error
```

顺序固定为 safeopen 校验、`file:` URI `mode=ro&immutable=0`、单连接、`PRAGMA query_only=ON`、只读一致事务、回滚和关闭。产品 SQL 必须显式列出表和列，禁止 `SELECT *`。

- [ ] **步骤 4：扩展通用授权与隐私契约**

```go
func AuthorizationContract(t *testing.T, adapter source.Adapter, mutate func(), forged source.Session)
func AssertCanonicalEvents(t *testing.T, r io.Reader, allowed map[string]bool)
func AssertNoPrivateFields(t *testing.T, value any)
```

覆盖跨实例伪造、快照变化、取消、绝对路径和禁止字段；保留 JSONL 专用 `SafetyContract`。

- [ ] **步骤 5：验证并提交**

运行：`go test ./internal/source/internal/... -count=1`
预期：PASS。

```bash
git add internal/source/internal/safeopen internal/source/internal/sqliteread internal/source/internal/adaptertest
git commit -m "fix: harden local session source reads"
```

### 任务 3：升级 OpenCode 当前 SQLite 格式

**文件：**
- 修改：`internal/source/opencode/adapter.go`
- 创建：`internal/source/opencode/sqlite.go`
- 修改：`internal/source/opencode/adapter_test.go`
- 创建：`internal/source/testdata/opencode/sqlite-v2.sql`

- [ ] **步骤 1：编写 SQLite schema、WAL、旧格式去重和凭据隔离测试**

测试库创建 `project/session/message/part` 以及诱饵 `account/credential/token` 表，插入 user、assistant、tool call/result；断言 scope、配对、`FormatVersion == "db-v2"`，并确认没有查询诱饵表。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/source/opencode -run 'TestSQLite|TestWAL|TestCredential' -count=1`
预期：FAIL，现有实现只读取 legacy `storage` JSON。

- [ ] **步骤 3：实现显式 SQLite 查询**

只读 `session/message/part/project` 必需列；复用现有 part 规范化规则。缺表或缺列返回 `format_unsupported`；输出规范事件后计算 SnapshotID，`Open` 重查并比较。DB 与 legacy 同 session ID 时选择 DB。

- [ ] **步骤 4：验证并提交**

运行：`go test ./internal/source/opencode -count=1`
预期：PASS。

```bash
git add internal/source/opencode internal/source/testdata/opencode
git commit -m "feat: scan current OpenCode sqlite sessions"
```

### 任务 4：升级 WorkBuddy、Qwen Code 与 VS Code Copilot

**文件：**
- 修改：`internal/source/workbuddy/adapter.go`
- 修改：`internal/source/workbuddy/adapter_test.go`
- 创建：`internal/source/testdata/workbuddy/current-v2.jsonl`
- 修改：`internal/source/qwen/adapter.go`
- 修改：`internal/source/qwen/adapter_test.go`
- 创建：`internal/source/testdata/qwen/current-v2.jsonl`
- 修改：`internal/source/copilot/adapter.go`
- 修改：`internal/source/copilot/adapter_test.go`

- [ ] **步骤 1：编写当前格式与 Copilot 来源签名测试**

覆盖 WorkBuddy 新默认根 `.workbuddy-ai/projects` 与旧根兼容；Qwen 明确处理 parts/thinking/tool 并忽略 systemPayload/snapshot/uiEvent；普通 VS Code Chat 拒绝，带 GitHub Copilot provider/responder fixture 的 v3 会话接受。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/source/workbuddy ./internal/source/qwen ./internal/source/copilot -count=1`
预期：FAIL，新 WorkBuddy 根和 Copilot provenance 用例失败。

- [ ] **步骤 3：实现最小格式变体**

WorkBuddy 显式 roots 不追加默认目录，默认 roots 去重且不扫描 connectors/plugins/cache。Qwen 不导出 telemetry。Copilot 在事件生成前调用：

```go
func hasCopilotProvenance(session sess) bool
```

只有 fixture 验证的 provider/responder 签名通过；目录名不是充分条件。

- [ ] **步骤 4：验证并提交**

运行：`go test ./internal/source/workbuddy ./internal/source/qwen ./internal/source/copilot -count=1`
预期：PASS。

```bash
git add internal/source/workbuddy internal/source/qwen internal/source/copilot internal/source/testdata/workbuddy internal/source/testdata/qwen
git commit -m "feat: update verified local agent formats"
```

### 任务 5：新增 Gemini CLI 与 GitHub Copilot CLI

**文件：**
- 创建：`internal/source/gemini/adapter.go`
- 创建：`internal/source/gemini/adapter_test.go`
- 创建：`internal/source/testdata/gemini/object-v1.json`
- 创建：`internal/source/testdata/gemini/stream-v1.jsonl`
- 创建：`internal/source/copilotcli/adapter.go`
- 创建：`internal/source/copilotcli/adapter_test.go`
- 创建：`internal/source/testdata/copilotcli/flat-v1.jsonl`
- 创建：`internal/source/testdata/copilotcli/directory-v2/events.jsonl`
- 参考只读：`/Users/liuyuxiang05/Projects/agentsview/internal/parser/gemini.go`
- 参考只读：`/Users/liuyuxiang05/Projects/agentsview/internal/parser/copilot.go`

- [ ] **步骤 1：迁移为人工脱敏 fixture 并写失败测试**

Gemini 覆盖 `.gemini/tmp/<project>/chats/session-*.json|jsonl`、thought、tool/result、partial tail、重复 ID last-wins、项目 hash 映射和排除 antigravity。Copilot CLI 覆盖 `.copilot/session-state/<uuid>.jsonl` 与 `<uuid>/events.jsonl`、cwd、消息、reasoning、toolRequests/result 配对和目录格式优先。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/source/gemini ./internal/source/copilotcli -count=1`
预期：FAIL，适配器尚不存在。

- [ ] **步骤 3：实现两个独立适配器**

Gemini 只读取 chats 和安全项目映射，不读取 auth/config；Copilot CLI 只读取 `session-state`。规范标识固定为 `gemini-cli` 与 `copilot-cli`，不得复用 `vscode-copilot`。两者均执行 Discover→Open 摘要授权，不缓存正文。

- [ ] **步骤 4：验证并提交**

运行：`go test ./internal/source/gemini ./internal/source/copilotcli -count=1`
预期：PASS。

```bash
git add internal/source/gemini internal/source/copilotcli internal/source/testdata/gemini internal/source/testdata/copilotcli
git commit -m "feat: add Gemini and Copilot CLI sources"
```

### 任务 6：新增 Cline 与 aider

**文件：**
- 创建：`internal/source/cline/adapter.go`
- 创建：`internal/source/cline/adapter_test.go`
- 创建：`internal/source/testdata/cline/session-v1.json`
- 创建：`internal/source/testdata/cline/messages-v1.json`
- 创建：`internal/source/aider/adapter.go`
- 创建：`internal/source/aider/adapter_test.go`
- 创建：`internal/source/testdata/aider/markdown-v1.md`

- [ ] **步骤 1：编写 Cline 与 aider 失败测试**

Cline 只接受 `<id>/<id>.json` 与 `<id>.messages.json` ID 一致的会话，忽略 system_prompt 且不读 db/settings/providers。aider fixture 含两个 chat-start 段；`#### ` 为 user，`> ` 状态行丢弃，其余为 assistant；能力严格为 messages，一个物理文件产生两个逻辑 session。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/source/cline ./internal/source/aider -count=1`
预期：FAIL，包尚不存在。

- [ ] **步骤 3：实现两个适配器**

aider 使用逻辑段绑定：

```go
type sessionBinding struct {
    ID string
    Path string
    Digest string
    Segment int
}
```

只检查可信 root 直接子文件 `.aider.chat.history.md`；禁止读取 input history、tags cache、LLM history 和配置。默认 home 命中使用稳定 `session_collection`，显式项目 root 才使用 project scope。

- [ ] **步骤 4：验证并提交**

运行：`go test ./internal/source/cline ./internal/source/aider ./internal/source/internal/safeopen -count=1`
预期：PASS。

```bash
git add internal/source/cline internal/source/aider internal/source/testdata/cline internal/source/testdata/aider
git commit -m "feat: add Cline and aider session sources"
```

### 任务 7：新增 Kimi Code 与 CodeBuddy CLI

**文件：**
- 创建：`internal/source/kimicode/adapter.go`
- 创建：`internal/source/kimicode/adapter_test.go`
- 创建：`internal/source/testdata/kimicode/state-v1.json`
- 创建：`internal/source/testdata/kimicode/wire-v1.jsonl`
- 创建：`internal/source/codebuddycli/adapter.go`
- 创建：`internal/source/codebuddycli/adapter_test.go`
- 创建：`internal/source/testdata/codebuddycli/session-v1.jsonl`

- [ ] **步骤 1：编写 Kimi Code 索引/state/wire 失败测试**

模拟 `session_index.jsonl`、`sessions/wd_*/session_*/state.json`、`agents/main/wire.jsonl`。索引只用于定位，state 只取 times/workDir，wire 只接受明确的消息、thinking、usage 和工具协议；排除 credentials/oauth/device/config/logs/user-history。

- [ ] **步骤 2：编写 CodeBuddy CLI 失败测试**

模拟 `.codebuddy/projects/<cwd-encoded>/<uuid>.jsonl`，覆盖 message/reasoning/function_call/result、scope、坏尾行与快照变化。断言不打开 tool-results、snapshot backup 或 IDE VSCDB。

- [ ] **步骤 3：运行测试验证失败**

运行：`go test ./internal/source/kimicode ./internal/source/codebuddycli -count=1`
预期：FAIL，包尚不存在。

- [ ] **步骤 4：实现、验证并提交**

两个包分别使用 `kimi-code`、`codebuddy-cli`，不得与 Kimi CLI 或 CodeBuddy IDE 合并；能力只由 fixture 证明，未知协议事件不得猜测。

运行：`go test ./internal/source/kimicode ./internal/source/codebuddycli -count=1`
预期：PASS。

```bash
git add internal/source/kimicode internal/source/codebuddycli internal/source/testdata/kimicode internal/source/testdata/codebuddycli
git commit -m "feat: add Kimi Code and CodeBuddy CLI sources"
```

### 任务 8：新增通义灵码与 Qoder CLI/IDE

**文件：**
- 创建：`internal/source/internal/sharedclient/jsonl.go`
- 创建：`internal/source/internal/sharedclient/sqlite.go`
- 创建：`internal/source/internal/sharedclient/sharedclient_test.go`
- 创建：`internal/source/lingma/adapter.go`
- 创建：`internal/source/lingma/adapter_test.go`
- 创建：`internal/source/testdata/lingma/execution-v1.jsonl`
- 创建：`internal/source/qoder/adapter.go`
- 创建：`internal/source/qoder/adapter_test.go`
- 创建：`internal/source/testdata/qoder/transcript-v1.jsonl`

- [ ] **步骤 1：编写 CLI JSONL 与 IDE SQLite 失败测试**

Lingma CLI 路径为 `SharedClientCache/cli/projects/<cwd>/<task>.session.execution.jsonl`；Qoder CLI 为 `.qoder/projects/<cwd>/transcript/<uuid>.jsonl`。IDE 临时库创建经过验证的 chat_session/chat_record/chat_message/chat_snapshot 以及诱饵账号、Token、goal、notification 表。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/source/internal/sharedclient ./internal/source/lingma ./internal/source/qoder -count=1`
预期：FAIL，包尚不存在。

- [ ] **步骤 3：实现共享格式层和四个产品标识**

```go
func ParseExecutionJSONL(data []byte) (Metadata, []Event, error)
func ReadChatTables(ctx context.Context, root, dbPath string, product Schema) ([]Conversation, error)
```

`Schema` 显式白名单表和列；SQL 使用固定模板，禁止 `SELECT *` 和从 DB 内容拼接 SQL。Lingma/Qoder 各自维护 adapter、root、ID、scope，产品标识固定为 `tongyi-lingma-cli`、`tongyi-lingma-ide`、`qoder-cli`、`qoder-ide`。未知 schema 返回 `format_unsupported`。

- [ ] **步骤 4：验证 WAL 和凭据隔离并提交**

运行：`go test ./internal/source/internal/sharedclient ./internal/source/lingma ./internal/source/qoder -count=1`
预期：PASS，WAL 消息可见且诱饵表未查询。

```bash
git add internal/source/internal/sharedclient internal/source/lingma internal/source/qoder internal/source/testdata/lingma internal/source/testdata/qoder
git commit -m "feat: add Lingma and Qoder local sources"
```

### 任务 9：实现 TRAE 导出要求和暂不支持原因

**文件：**
- 创建：`internal/source/trae/adapter.go`
- 创建：`internal/source/trae/adapter_test.go`
- 创建：`internal/source/testdata/trae/export-v1.md`
- 修改：`internal/source/catalog/catalog.go`
- 修改：`internal/source/catalog/catalog_test.go`

- [ ] **步骤 1：编写 TRAE fail-closed 测试**

检测到产品目录但没有官方导出时返回 `export_required`；SQLCipher DB、memory 摘要、input history、snapshot 一律不得打开。显式提供 TRAE 官方导出目录时只解析 Markdown fixture，并在源变化后拒绝 `Open`。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/source/trae ./internal/source/catalog -count=1`
预期：FAIL，TRAE 尚无状态适配器且 Kiro 尚未进入 catalog。

- [ ] **步骤 3：实现 TRAE 和 unsupported 定义**

`trae.New` 不连接加密库；默认检测只返回 export_required，只有显式绝对、清洁的官方导出 root 才扫描 Markdown。Catalog 增加：

```go
{Product: "kiro", Status: source.SourceDetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_session_schema"}
{Product: "codebuddy-ide", Status: source.SourceDetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_transcript_body"}
{Product: "trae-work", Status: source.SourceDetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_distinct_local_format"}
{Product: "kimi-work", Status: source.SourceDetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_session_schema"}
{Product: "qoder-work", Status: source.SourceDetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_distinct_local_format"}
```

- [ ] **步骤 4：验证并提交**

运行：`go test ./internal/source/trae ./internal/source/catalog -count=1`
预期：PASS，Kiro 永不成为 ready。

```bash
git add internal/source/trae internal/source/testdata/trae internal/source/catalog
git commit -m "feat: report TRAE export and unsupported sources"
```

### 任务 10：注册全部适配器并统一 CLI、API 与页面状态

**文件：**
- 修改：`cmd/kuai/main.go`
- 修改：`cmd/kuai/main_test.go`
- 修改：`internal/webapp/routes.go`
- 修改：`internal/webapp/flow_test.go`
- 修改：`internal/webapp/assets/index.html`
- 修改：`internal/webapp/assets/app.js`
- 修改：`internal/webapp/assets/styles.css`
- 修改：`internal/webapp/assets_test.go`
- 修改：`tests/session_selection_interactions.js`

- [ ] **步骤 1：编写 registry 完整性与 root 隔离测试**

以集合比较替换硬编码 ready 数量，断言所有目标标识恰好注册一次；重点断言 `copilot-cli != vscode-copilot`、`kimi-cli != kimi-code`，roots 不串线。

```go
want := []string{
    "aider", "claude-code", "cline", "codebuddy-cli", "codeflicker",
    "codex", "copilot-cli", "cursor", "gemini-cli", "hermes-agent",
    "kimi-cli", "kimi-code", "myflicker", "openclaw", "opencode",
    "qoder-cli", "qoder-ide", "qwen-code", "tongyi-lingma-cli",
    "tongyi-lingma-ide", "trae", "vscode-copilot", "workbuddy",
}
```

该集合同时作为 Claude Code、Codex、Cursor、Hermes Agent、OpenClaw、CodeFlicker、MyFlicker 和 Kimi CLI 的回归门禁；不能因新增适配器丢失原有产品。

- [ ] **步骤 2：编写 CLI/API 状态测试**

source 输出必须包含 `state/verification/capabilities/reason/selectable`，不得包含路径、session ID 或底层错误。`selectable` 仅在有 session 且 runtime 状态 ready 时为 true。

- [ ] **步骤 3：编写前端状态测试**

模拟 not_found、export_required、format_unsupported、detected_unsupported；断言来源清单显示固定动作/原因，不可用来源不能 prepare，Assessment Scope 仍默认不选中且手机号认证布局不回退。

- [ ] **步骤 4：运行测试验证失败**

运行：`go test ./cmd/kuai ./internal/webapp -count=1 && node --test tests/session_selection_interactions.js`
预期：FAIL，新包未注册且前端仍丢弃 `body.sources`。

- [ ] **步骤 5：注册并合并状态**

`productionDependencies().newRegistry` 逐个使用自己的 root key。`safeScanOutput` 与 `/api/scopes` 用 runtime scan 状态覆盖 catalog 默认状态，但 verification/capabilities/reason 来自 catalog。

- [ ] **步骤 6：渲染来源状态区**

`app.js` state 增加 `sources`，`loadScopes` 同时保存两个数组；`index.html` 在 Assessment Scope 标题附近增加 `sourceStatusList`。前端只按 reason code 映射固定中文：

```js
const sourceReason = {
  official_export_required: "需要先从产品官方功能导出会话",
  no_verified_session_schema: "暂未验证可靠的会话格式",
  no_verified_transcript_body: "未验证完整会话正文",
  no_distinct_local_format: "未验证独立的本地会话格式"
};
```

- [ ] **步骤 7：验证并提交**

运行：`go test ./cmd/kuai ./internal/webapp -count=1 && npm test`
预期：PASS。

```bash
git add cmd/kuai internal/webapp tests/session_selection_interactions.js
git commit -m "feat: expose verified local source support"
```

### 任务 11：验证导出过滤与逐 session 隔离

**文件：**
- 修改：`internal/upload/stream_exporter_test.go`
- 修改：`internal/upload/redactor_test.go`
- 修改：`internal/webapp/flow_test.go`
- 仅在测试证明缺口时修改：`internal/upload/stream_exporter.go`
- 仅在测试证明缺口时修改：`internal/upload/stream_helpers.go`
- 仅在测试证明缺口时修改：`internal/upload/redactor.go`

- [ ] **步骤 1：增加多产品规范事件隐私测试**

构造含 message、thinking、tool_use、Read/tool_result、路径、手机号、Token 的多 session adapter；断言只导出选中 scope、Read 结果剔除、其余递归脱敏，单 session 失败不留下半成品 artifact。

- [ ] **步骤 2：运行测试验证行为**

运行：`go test ./internal/upload ./internal/webapp -run 'Test.*(Redact|Read|Scope|Session)' -count=1`
预期：新测试若暴露缺口则 FAIL；若现有实现满足则 PASS 且不改生产文件。

- [ ] **步骤 3：修复测试证明的最小缺口并提交**

禁止为特定产品在上传层加分支；只修规范事件的过滤/清理失败路径。

运行：`go test ./internal/upload ./internal/webapp -count=1`
预期：PASS。

```bash
git add internal/upload internal/webapp/flow_test.go
git commit -m "test: verify multi-source export privacy"
```

### 任务 12：同步 README、Skill 与会话格式文档

**文件：**
- 修改：`README.md`
- 修改：`kuai.md`
- 修改：`tests/test_docs.py`
- 修改：`/Users/liuyuxiang05/Projects/liu/2-Notes/Projects/HR-agentreview/数据基建/Agent本地会话格式.md`

- [ ] **步骤 1：先扩展文档契约测试**

断言 README 与 `kuai.md` 包含全部规范标识、四种验证等级、五个暂不支持原因，并明确 Copilot/Kimi 产品不合并；保留用户已加入的 `build.sh` 断言。

- [ ] **步骤 2：运行测试验证失败**

运行：`python3 -m unittest tests.test_docs -v`
预期：FAIL，矩阵尚未同步。

- [ ] **步骤 3：更新仓库文档并保护用户修改**

先运行 `git diff -- README.md tests/test_docs.py scripts/build-kuai-release.sh tests/test_build_kuai_release_sh.sh`。只修改 README 支持矩阵/扫描说明、`kuai.md` 扫描/故障诊断、测试中的产品契约；不得覆盖 build/release 内容或编辑两个构建脚本。

- [ ] **步骤 4：同步外部笔记**

对每个产品写规范标识、展示名、相对路径/存储类型、格式、scope、能力、状态、验证等级、限制及 unsupported 原因。路径统一用 `~`，不得写真实正文、用户名、项目绝对路径、Token。该文件在 workspace 外，执行前申请权限，再使用 `apply_patch`。

- [ ] **步骤 5：验证并只提交本功能 hunk**

运行：`python3 -m unittest tests.test_docs tests.test_single_binary_contract -v`
预期：PASS。

对 `README.md`、`tests/test_docs.py` 使用 `git add -p`，只选择支持矩阵/文档契约 hunk，拒绝用户原有 build/release hunk；完整暂存 `kuai.md`，检查 cached diff 后提交。

```bash
git add kuai.md
git commit -m "docs: document local agent session formats"
```

### 任务 13：全量验证真正单文件 kuai

**文件：**
- 不新增生产文件；仅验证上述结果。

- [ ] **步骤 1：格式化与边界检查**

运行：`gofmt -w cmd/kuai/*.go internal/source/*.go internal/source/*/*.go internal/source/internal/*/*.go internal/upload/*.go internal/webapp/*.go && git diff --check && git status --short`
预期：无格式错误；用户原有文件仍存在且未被删除或意外暂存。

- [ ] **步骤 2：运行 Go 测试、竞态检查和 vet**

```bash
go test ./internal/source/... -count=1
go test ./internal/upload ./internal/webapp ./cmd/kuai -count=1
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
```

预期：全部 PASS。

- [ ] **步骤 3：运行前端、文档、安装和 Skill 测试**

```bash
npm test
python3 -m unittest discover -s tests -v
bash tests/test_kuai_install_sh.sh
bash tests/test_build_kuai_release_sh.sh
```

预期：全部 PASS，Skill 与 CLI 仍调用同一个 `kuai`。

- [ ] **步骤 4：构建并检查单文件产物**

```bash
KUAI_VERSION=dev KUAI_BUILD_OUTPUT=/private/tmp/kuai-expanded-support ./build.sh
/private/tmp/kuai-expanded-support/kuai version
/private/tmp/kuai-expanded-support/kuai scan
```

预期：生成真正单文件 `kuai`；scan 只输出产品、scope、session 数、能力、状态和验证等级，不输出正文、完整路径或凭据。

- [ ] **步骤 5：执行本机隐私 smoke test**

只记录 `product/verification/project_count/session_count/capabilities/state` 汇总；确认 OpenCode/WorkBuddy 为本机结构验证，Gemini/Copilot CLI 为 fixture 验证，TRAE 为 export_required，Kiro 非 ready。不得保存或提交 smoke 输出。

- [ ] **步骤 6：最终提交检查**

运行：`git diff --cached --check && git status --short && git log --oneline -12`
预期：无真实会话 fixture、外部笔记、构建产物或用户无关文件进入提交。最终向用户交付支持清单和每个暂不支持原因。
