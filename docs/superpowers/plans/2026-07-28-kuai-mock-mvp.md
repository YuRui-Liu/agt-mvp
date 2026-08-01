# kuAI 本地会话上传 Mock MVP 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 构建无需 Python、Node、Git 或 WSL 的 Go 版 `kuai` Mock MVP，跑通“扫描 Project → 导出受支持 session → 本地自动脱敏 → 手机验证 → kuAI ID → 用途授权 → Mock 上传与分析 → 海报 → 关联投递”闭环。

**架构：** `kuai` 作为原生本地客户端，使用现有 `agentsview` CLI 的 `sync`、`session list` 和 `session export` 能力，但不修改现有 avscore 流程。客户端将受支持 session 的 JSONL 导出为内存模型，递归删除禁止字段并脱敏文本，然后交给同进程 Mock 业务服务；本地 Web UI 只通过带启动令牌的 localhost API 操作服务端已知 Project。Mock 服务将认证、任务、海报和投递接口设计成未来远程服务可替换的边界。

**技术栈：** Go 1.24+、Go 标准库、`github.com/skip2/go-qrcode`、`go:embed`、原生 HTML/CSS/JavaScript、Bash 3.2+、PowerShell 5.1+、现有 `agentsview` CLI。

---

## 范围约束

本计划只实现 PRD 的“阶段一：Mock MVP”。以下内容不在本计划中：

- 将扫描能力链接进单一 `kuai` 二进制；
- 接入真实短信、公司域名、远程对象存储或招聘服务；
- 用户上传历史和自助删除页面；
- 正式模型评分；
- 未验证 Agent 的格式转换；
- 正式签名、公证和 Authenticode。
- 手机跨设备访问电脑 localhost 上的 Mock 投递页。

Mock MVP 仍必须做到：

- 用户运行时不依赖 Python 或 Node；
- macOS、Linux、Windows 均可构建；
- 正式界面只允许 Project 粒度选择；
- 单 session 入口只在 `--debug-session-upload` 下出现；
- 不向公网发送真实会话；
- Mock 海报不包含 `score`、分数、权重、阈值或排名。
- Mock 二维码编码真实的本地投递 URL，并提供同目标的“模拟扫码”链接；客户端仍只绑定 `127.0.0.1`，不得为了手机扫码开放局域网监听。

## 目标文件结构

- 创建 `go.mod`、`go.sum`：定义 Go 模块和二维码生成依赖。
- 创建 `cmd/kuai/main.go`：解析 CLI 配置、同步会话、启动本地服务并打开浏览器。
- 创建 `cmd/kuai/main_test.go`：验证 CLI 参数、启动事件和浏览器降级。
- 创建 `internal/config/config.go`：集中处理环境变量、数据目录、Mock 场景和调试开关。
- 创建 `internal/config/config_test.go`：验证安全默认值和无效配置。
- 创建 `internal/agentsview/client.go`：安全调用 `agentsview` 并加载 session。
- 创建 `internal/agentsview/projects.go`：规范化 session、按 Project 聚合并标记来源支持状态。
- 创建 `internal/agentsview/client_test.go`、`internal/agentsview/projects_test.go`：测试参数数组、格式校验和 Project 聚合。
- 创建 `internal/upload/model.go`：定义脱敏上传包、session 和统计数据结构。
- 创建 `internal/upload/exporter.go`：逐 session 流式执行 `agentsview session export`。
- 创建 `internal/upload/redactor.go`：删除禁止字段、排除完整文件读取结果并递归脱敏。
- 创建 `internal/upload/exporter_test.go`、`internal/upload/redactor_test.go`：覆盖导出限制和敏感数据。
- 创建 `internal/mocksvc/auth.go`：Mock 验证码、认证主体和唯一 `kuAI ID`。
- 创建 `internal/mocksvc/store.go`：权限受限的原子 JSON 状态存储和 30 天上传包清理。
- 创建 `internal/mocksvc/tasks.go`：用途授权、幂等上传、同步或异步分析及失败场景。
- 创建 `internal/mocksvc/poster.go`：生成不含真实分数的 SVG 海报和动态二维码。
- 创建 `internal/mocksvc/application.go`：签发并消费短期一次性投递令牌。
- 创建 `internal/mocksvc/auth_test.go`、`internal/mocksvc/store_test.go`、`internal/mocksvc/tasks_test.go`、`internal/mocksvc/poster_test.go`、`internal/mocksvc/application_test.go`：覆盖 Mock 业务规则。
- 创建 `internal/webapp/server.go`：localhost HTTP 服务、启动令牌和安全响应。
- 创建 `internal/webapp/routes.go`：Project、脱敏、认证、上传、任务、海报和投递 API。
- 创建 `internal/webapp/server_test.go`、`internal/webapp/flow_test.go`：HTTP 安全与完整流程测试。
- 创建 `internal/webapp/assets/index.html`：Project 选择、脱敏、认证和授权流程页面。
- 创建 `internal/webapp/assets/app.js`：浏览器状态机、API 调用和轮询。
- 创建 `internal/webapp/assets/styles.css`：kuAI 本地流程样式和可访问性状态。
- 创建 `internal/webapp/assets/poster.html`：海报展示和下载页。
- 创建 `internal/webapp/assets/application.html`：Mock 职位投递页。
- 创建 `internal/webapp/assets_test.go`：模板占位符、敏感文案、可访问性和无分数检查。
- 创建 `install.sh`、`install.ps1`：下载并校验 `kuai` 和阶段一所需的 `agentsview`。
- 创建 `tests/test_kuai_install_sh.sh`、`tests/Test-KuaiInstall.ps1`：安装器行为测试。
- 创建 `kuai.md`：所有 Agent 共用的 kuAI skill。
- 创建 `scripts/build-kuai-release.sh`：构建多平台 Mock MVP 产物和 `SHA256SUMS`。
- 创建 `.github/workflows/kuai-mock.yml`：Go、Shell、PowerShell、跨平台构建和产物检查。
- 修改 `README.md`：新增 kuAI Mock MVP 快速开始、隐私边界、调试方式和验证命令；保留现有 avscore 说明。

### 任务 1：建立 Go CLI 配置骨架

**文件：**
- 创建：`go.mod`
- 创建：`internal/config/config.go`
- 创建：`internal/config/config_test.go`
- 创建：`cmd/kuai/main.go`

- [ ] **步骤 1：编写配置默认值失败测试**

```go
package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadUsesPrivateLocalMockDefaults(t *testing.T) {
	t.Setenv("KUAI_DATA_DIR", t.TempDir())
	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BindAddress != "127.0.0.1:0" {
		t.Fatalf("bind = %q", cfg.BindAddress)
	}
	if cfg.AnalysisAsyncAfter != 30*time.Second {
		t.Fatalf("threshold = %s", cfg.AnalysisAsyncAfter)
	}
	if cfg.ServiceMode != "mock" || cfg.AllowNetwork {
		t.Fatalf("unsafe service defaults: %#v", cfg)
	}
	if filepath.Base(cfg.StatePath) != "state.json" {
		t.Fatalf("state path = %q", cfg.StatePath)
	}
}

func TestLoadRejectsUnknownMockScenario(t *testing.T) {
	t.Setenv("KUAI_MOCK_SCENARIO", "invented")
	if _, err := Load(nil); err == nil {
		t.Fatal("expected invalid scenario error")
	}
}
```

- [ ] **步骤 2：运行测试确认失败**

运行：`go test ./internal/config -run TestLoad -v`

预期：FAIL，提示 `go.mod` 或 `Load` 不存在。

- [ ] **步骤 3：创建模块并实现最小配置**

`go.mod`：

```go
module github.com/YuRui-Liu/agt-mvp

go 1.24

require github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
```

`internal/config/config.go` 的稳定接口：

```go
type Config struct {
	AgentsviewPath       string
	BindAddress          string
	DataDir              string
	StatePath            string
	NoBrowser            bool
	DebugSessionUpload   bool
	ServiceMode          string
	MockScenario         string
	AnalysisAsyncAfter   time.Duration
	AllowNetwork         bool
}

func Load(args []string) (Config, error)
```

允许的 Mock 场景固定为 `success`、`otp_error`、`upload_error`、`analysis_error`、`slow`、`ticket_error`。默认 `success`。数据目录依次使用 `KUAI_DATA_DIR`、`os.UserConfigDir()/kuai`；服务模式首版只接受 `mock`，`AllowNetwork` 始终为 `false`。

- [ ] **步骤 4：创建只负责依赖组装的 CLI 入口**

`cmd/kuai/main.go` 暂时只加载配置并将错误写入 stderr：

```go
func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load(args)
	if err != nil {
		fmt.Fprintln(stderr, "kuai:", err)
		return 2
	}
	fmt.Fprintf(stdout, "{\"type\":\"config-ready\",\"mode\":%q}\n", cfg.ServiceMode)
	return 0
}
```

- [ ] **步骤 5：运行测试并整理依赖**

运行：`go mod tidy`

运行：`go test ./internal/config ./cmd/kuai -v`

预期：配置测试 PASS；`go mod tidy` 生成 `go.sum`。

- [ ] **步骤 6：提交**

```bash
git add go.mod go.sum cmd/kuai/main.go internal/config/config.go internal/config/config_test.go
git commit -m "feat: scaffold kuai mock client"
```

### 任务 2：扫描 session 并按 Project 聚合

**文件：**
- 创建：`internal/agentsview/client.go`
- 创建：`internal/agentsview/projects.go`
- 创建：`internal/agentsview/client_test.go`
- 创建：`internal/agentsview/projects_test.go`

- [ ] **步骤 1：编写命令安全和 Project 聚合失败测试**

```go
func TestClientListsSessionsWithArgumentArray(t *testing.T) {
	runner := &recordingRunner{stdout: `{"sessions":[{"id":"s1","agent":"claude","project":"/work/atr"}]}`}
	client := NewClient("agentsview", runner)
	_, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agentsview", "session", "list", "--format", "json", "--limit", "500"}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("argv = %#v want %#v", runner.calls[0], want)
	}
}

func TestGroupProjectsSeparatesEligibilityFromDiscovery(t *testing.T) {
	projects := GroupProjects([]Session{
		{ID: "c1", Agent: "claude", Project: "/work/atr", EndedAt: "2026-07-28T10:00:00Z"},
		{ID: "x1", Agent: "cursor", Project: "/work/atr", EndedAt: "2026-07-27T10:00:00Z"},
		{ID: "x2", Agent: "cursor", Project: "/work/other"},
	})
	if len(projects) != 2 {
		t.Fatalf("projects = %#v", projects)
	}
	if !projects[0].UploadSupported || len(projects[0].SupportedSessionIDs) != 1 {
		t.Fatalf("atr eligibility = %#v", projects[0])
	}
	if projects[1].UploadSupported {
		t.Fatalf("unsupported project became selectable: %#v", projects[1])
	}
	if strings.Contains(projects[0].Label, "/work/") {
		t.Fatalf("label leaks path: %q", projects[0].Label)
	}
}
```

`client_test.go` 的 `recordingRunner` 实现 `Runner`，保存每次 argv，并返回测试配置的 stdout、stderr、退出码和 error；它不启动真实进程。

- [ ] **步骤 2：运行测试确认失败**

运行：`go test ./internal/agentsview -v`

预期：FAIL，提示 `NewClient`、`Session` 或 `GroupProjects` 不存在。

- [ ] **步骤 3：实现进程运行器和严格 JSON 校验**

稳定类型：

```go
type Session struct {
	ID        string `json:"id"`
	Agent     string `json:"agent"`
	Project   string `json:"project"`
	Title     string `json:"display_name"`
	EndedAt   string `json:"ended_at"`
	MessageCount int  `json:"message_count"`
}

type Runner interface {
	Run(ctx context.Context, argv []string) (stdout string, stderr string, exitCode int, err error)
}

type Client struct {
	binary string
	runner Runner
}

func (c *Client) Sync(ctx context.Context) error
func (c *Client) ListSessions(ctx context.Context) ([]Session, error)
```

真实运行器必须使用 `exec.CommandContext(ctx, argv[0], argv[1:]...)`，不得调用 shell。错误只返回固定安全摘要，不返回完整 stderr。

- [ ] **步骤 4：实现 Project 聚合和来源支持策略**

```go
type Project struct {
	Key                 string   `json:"key"`
	Label               string   `json:"label"`
	Agents              []string `json:"agents"`
	SessionCount        int      `json:"session_count"`
	SupportedCount      int      `json:"supported_count"`
	UploadSupported     bool     `json:"upload_supported"`
	UnsupportedAgents   []string `json:"unsupported_agents"`
	SupportedSessionIDs []string `json:"-"`
	RawProject          string   `json:"-"`
}
```

`IsSupportedAgent` 对大小写和 `-`、`_` 做规范化，仅接受 `claude`、`claude-code` 和 `agentreview`。`GroupProjects` 按真实 `project` 聚合；`Key` 使用 `sha256(project)` 的前 16 字节十六进制，`Label` 只使用路径 basename 或“未命名项目”，绝不把绝对路径注入浏览器。

- [ ] **步骤 5：运行扫描测试**

运行：`go test ./internal/agentsview -v`

预期：命令数组、非法 JSON、重复 session、空字段、排序、路径隐藏、支持与不支持来源测试全部 PASS。

- [ ] **步骤 6：提交**

```bash
git add internal/agentsview
git commit -m "feat: discover uploadable agent projects"
```

### 任务 3：导出 session、删除禁止内容并自动脱敏

**文件：**
- 创建：`internal/upload/model.go`
- 创建：`internal/upload/exporter.go`
- 创建：`internal/upload/redactor.go`
- 创建：`internal/upload/exporter_test.go`
- 创建：`internal/upload/redactor_test.go`

- [ ] **步骤 1：编写递归脱敏和禁止内容失败测试**

```go
func TestRedactEventRemovesForbiddenPayloadsAndKeepsDiff(t *testing.T) {
	event := map[string]any{
		"message": "email dev@example.com token sk-12345678901234567890",
		"attachment": map[string]any{"name": "resume.pdf", "data": "secret"},
		"file_content": "complete source",
		"diff": "@@ -1 +1 @@\n-old\n+new",
	}
	got, stats, err := RedactEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	text := string(encoded)
	for _, forbidden := range []string{"dev@example.com", "sk-12345678901234567890", "resume.pdf", "complete source"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("leaked %q in %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "@@ -1 +1 @@") || stats.Replacements < 2 || stats.DroppedFields < 2 {
		t.Fatalf("unexpected result: %s %#v", text, stats)
	}
}

func TestRedactEventOmitsReadToolOutputButKeepsPatchOutput(t *testing.T) {
	read := map[string]any{"tool_name": "Read", "output": "entire file"}
	patch := map[string]any{"tool_name": "apply_patch", "output": "*** Begin Patch"}
	gotRead, _, _ := RedactEvent(read)
	gotPatch, _, _ := RedactEvent(patch)
	if gotRead["output"] != "[OMITTED_FILE_CONTENT]" {
		t.Fatalf("read output = %#v", gotRead)
	}
	if gotPatch["output"] != "*** Begin Patch" {
		t.Fatalf("patch output = %#v", gotPatch)
	}
}
```

- [ ] **步骤 2：编写 session export 大小和 JSONL 校验失败测试**

```go
func TestExporterUsesKnownSessionIDsAndBuildsVersionedPackage(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{
		"s1": "{\"type\":\"user\",\"message\":\"hello\"}\n",
	}}
	exporter := NewExporter("agentsview", runner, Limits{
		MaxSessionBytes: 4 << 20,
		MaxPackageBytes: 25 << 20,
	})
	pkg, err := exporter.Build(context.Background(), agentsview.Project{
		Key: "p1", Label: "atr", SupportedSessionIDs: []string{"s1"},
	}, map[string]string{"s1": "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if pkg.SchemaVersion != 1 || len(pkg.Sessions) != 1 {
		t.Fatalf("package = %#v", pkg)
	}
}
```

- [ ] **步骤 3：运行测试确认失败**

运行：`go test ./internal/upload -v`

预期：FAIL，提示 `RedactEvent`、`NewExporter` 或上传模型不存在。

- [ ] **步骤 4：定义上传包稳定结构**

```go
type Package struct {
	SchemaVersion int       `json:"schema_version"`
	Project       Project   `json:"project"`
	Sessions      []Session `json:"sessions"`
	Redaction     Stats     `json:"redaction"`
	CreatedAt     time.Time `json:"created_at"`
}

type Project struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type Session struct {
	ID     string           `json:"id"`
	Agent  string           `json:"agent"`
	Events []map[string]any `json:"events"`
}

type Stats struct {
	Replacements  int `json:"replacements"`
	DroppedFields int `json:"dropped_fields"`
	OmittedReads  int `json:"omitted_reads"`
}
```

导出进程使用独立的流式接口，避免复用任务 2 中面向小 JSON 响应的 `Runner`：

```go
type ExportRunner interface {
	Open(ctx context.Context, argv []string) (
		stdout io.ReadCloser,
		wait func() error,
		err error,
	)
}

type Exporter struct {
	binary string
	runner ExportRunner
	limits Limits
}

type Limits struct {
	MaxLineBytes    int64
	MaxSessionBytes int64
	MaxPackageBytes int64
}
```

生产实现用 `exec.CommandContext` 和 `StdoutPipe`；`exporter_test.go` 的 `fakeRunner` 按 session ID 从 `outputs map[string]string` 取 JSONL，返回 `io.NopCloser(strings.NewReader(...))` 和记录 argv 的 `wait` 函数。

- [ ] **步骤 5：实现流式 JSONL 导出和限制**

`Exporter.Build` 对 Project 中每个服务端已知的 `SupportedSessionIDs` 执行：

```text
agentsview session export <session-id>
```

使用 `io.LimitedReader`、`bufio.Scanner` 的显式缓冲上限和 `json.Decoder` 解析每行。单 session 上限 4 MiB，整个包上限 25 MiB，单行上限 1 MiB。命令失败、非 JSON 行、非对象事件、UTF-8 错误或超限时整包失败；不得产生部分上传包。

- [ ] **步骤 6：实现递归脱敏**

删除键名规范化后匹配以下集合的字段：

```go
var droppedKeys = map[string]struct{}{
	"attachment": {}, "attachments": {}, "image": {}, "images": {},
	"binary": {}, "blob": {}, "file_content": {}, "file_contents": {},
}
```

当事件的 `tool_name` 或 `name` 为 `read`、`read_file`、`readfile`、`cat` 时，将 `content`、`output`、`result` 替换为 `[OMITTED_FILE_CONTENT]`；`apply_patch`、`patch`、`diff` 的文本结果保留并继续执行字符串脱敏。

字符串脱敏至少覆盖 Bearer Token、常见 API Key、PEM 私钥、邮箱、手机号、IPv4、本地绝对路径。占位符固定为 `[REDACTED_TOKEN]`、`[REDACTED_PRIVATE_KEY]`、`[REDACTED_EMAIL]`、`[REDACTED_PHONE]`、`[REDACTED_IP]`、`[REDACTED_PATH]`。

- [ ] **步骤 7：运行上传包测试**

运行：`go test ./internal/upload -v`

预期：递归对象、数组、嵌套工具结果、禁止字段、大小限制、非法 JSONL、无部分结果和统计测试全部 PASS。

- [ ] **步骤 8：提交**

```bash
git add internal/upload
git commit -m "feat: redact project session exports"
```

### 任务 4：实现 Mock 手机认证、唯一 kuAI ID 和私有状态存储

**文件：**
- 创建：`internal/mocksvc/auth.go`
- 创建：`internal/mocksvc/store.go`
- 创建：`internal/mocksvc/auth_test.go`
- 创建：`internal/mocksvc/store_test.go`

- [ ] **步骤 1：编写身份稳定性和手机号不落盘测试**

```go
func TestVerifyRestoresOneKuAIIDWithoutPersistingPhone(t *testing.T) {
	store := NewMemoryStore()
	auth := NewAuthenticator(store, []byte("test-mock-secret"), fixedClock())
	first, err := auth.Verify("+8613800138000", "246810")
	if err != nil {
		t.Fatal(err)
	}
	second, err := auth.Verify("+8613800138000", "246810")
	if err != nil {
		t.Fatal(err)
	}
	otherDevice := NewAuthenticator(NewMemoryStore(), []byte("test-mock-secret"), fixedClock())
	third, err := otherDevice.Verify("+8613800138000", "246810")
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity.KuAIID != second.Identity.KuAIID ||
		first.Identity.KuAIID != third.Identity.KuAIID ||
		first.Identity.SubjectID != second.Identity.SubjectID ||
		first.Identity.SubjectID != third.Identity.SubjectID {
		t.Fatalf("identity drift: %#v %#v %#v", first, second, third)
	}
	raw, _ := json.Marshal(store.Snapshot())
	if bytes.Contains(raw, []byte("13800138000")) {
		t.Fatalf("phone persisted: %s", raw)
	}
}

func TestVerifyRejectsWrongAndExpiredCode(t *testing.T) {
	auth := NewAuthenticator(NewMemoryStore(), []byte("secret"), fixedClock())
	if _, err := auth.Verify("+8613800138000", "000000"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("error = %v", err)
	}
}
```

测试文件中的时钟助手固定为：

```go
func fixedClock() func() time.Time {
	return func() time.Time {
		return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	}
}
```

- [ ] **步骤 2：编写文件权限和原子替换测试**

```go
func TestFileStoreUsesPrivateAtomicState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kuai", "state.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutIdentity(Identity{SubjectID: "sub-1", KuAIID: "KUAI-ABC123"}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}
```

- [ ] **步骤 3：运行测试确认失败**

运行：`go test ./internal/mocksvc -run 'TestVerify|TestFileStore' -v`

预期：FAIL，提示认证器或存储不存在。

- [ ] **步骤 4：实现 Mock 认证边界**

稳定接口：

```go
type Identity struct {
	SubjectID string    `json:"subject_id"`
	KuAIID    string    `json:"kuai_id"`
	CreatedAt time.Time `json:"created_at"`
}

type AuthSession struct {
	Token     string    `json:"token"`
	Identity  Identity  `json:"identity"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (a *Authenticator) RequestCode(phone string) error
func (a *Authenticator) Verify(phone, code string) (AuthSession, error)
func (a *Authenticator) Authenticate(token string) (Identity, error)
```

Mock 验证码固定为 `246810`。手机号只在认证方法的栈内短暂存在；`SubjectID` 使用 `HMAC-SHA256(mockSecret, normalizedPhone)` 生成，状态文件只保存 Subject 与 `kuAI ID`。Mock `kuAI ID` 使用 `HMAC-SHA256(mockIDSecret, SubjectID)` 的前 10 位 Base32 确定性生成，格式固定为 `KUAI-XXXXXXXXXX`，因此相同 Mock 密钥下的不同本地实例也会恢复相同 ID；正式服务仍以服务端持久映射为准。认证令牌随机生成、15 分钟过期、比较时使用 `subtle.ConstantTimeCompare`。

- [ ] **步骤 5：实现原子 JSON 存储**

状态目录权限为 `0700`，状态文件权限为 `0600`。写入流程必须是同目录临时文件、`Sync`、`Chmod(0600)`、`Rename`；损坏 JSON 启动时返回错误，不静默覆盖。存储对象不定义 Phone 字段。

- [ ] **步骤 6：运行认证和存储测试**

运行：`go test ./internal/mocksvc -run 'TestVerify|TestFileStore|TestAuthenticate' -v`

预期：身份稳定、错误验证码、令牌过期、手机号不落盘、原子写入和损坏状态测试全部 PASS。

- [ ] **步骤 7：提交**

```bash
git add internal/mocksvc/auth.go internal/mocksvc/auth_test.go internal/mocksvc/store.go internal/mocksvc/store_test.go
git commit -m "feat: add mock kuAI identity service"
```

### 任务 5：实现幂等上传、异步分析、海报和投递令牌

**文件：**
- 创建：`internal/mocksvc/tasks.go`
- 创建：`internal/mocksvc/poster.go`
- 创建：`internal/mocksvc/application.go`
- 创建：`internal/mocksvc/tasks_test.go`
- 创建：`internal/mocksvc/poster_test.go`
- 创建：`internal/mocksvc/application_test.go`

- [ ] **步骤 1：编写授权和幂等任务失败测试**

```go
func TestCreateTaskRequiresExactConsentAndIsIdempotent(t *testing.T) {
	service := newTestService("success")
	req := CreateTaskRequest{
		Identity: Identity{SubjectID: "sub-1", KuAIID: "KUAI-ABC123"},
		Package: upload.Package{SchemaVersion: 1},
		ConsentVersion: ConsentVersion,
		IdempotencyKey: "idem-1",
	}
	first, err := service.CreateTask(req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateTask(req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("duplicate tasks: %q %q", first.ID, second.ID)
	}
	req.IdempotencyKey = "idem-2"
	req.ConsentVersion = ""
	if _, err := service.CreateTask(req); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("error = %v", err)
	}
}
```

`tasks_test.go` 中的服务助手明确组装内存存储和固定时钟：

```go
func newTestService(scenario string) *TaskService {
	return NewTaskService(NewMemoryStore(), fixedClock(), scenario, 30*time.Second)
}
```

- [ ] **步骤 2：编写无分数海报和一次性投递令牌测试**

```go
func TestPosterContainsQualitativeCopyAndNoScores(t *testing.T) {
	encoder := &recordingQREncoder{png: []byte("png")}
	svg, err := RenderPoster(PosterModel{
		KuAIID: "KUAI-ABC123",
		Headline: "善于拆解复杂目标",
		Tags: []string{"目标引导", "工程协作", "持续迭代"},
		Encouragement: "继续保持好奇与行动。",
		ApplicationURL: "http://127.0.0.1:4000/application?ticket=t1",
	}, encoder)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"score", "分数", "权重", "排名", "阈值"} {
		if bytes.Contains(bytes.ToLower(svg), []byte(forbidden)) {
			t.Fatalf("poster leaked %q", forbidden)
		}
	}
	if !bytes.Contains(svg, []byte("<svg")) || !bytes.Contains(svg, []byte("data:image/png;base64,")) {
		t.Fatal("poster or QR missing")
	}
	if encoder.text != "http://127.0.0.1:4000/application?ticket=t1" {
		t.Fatalf("QR content = %q", encoder.text)
	}
}

func TestApplicationTicketIsShortLivedAndSingleUse(t *testing.T) {
	tickets := NewTicketService(fixedClock(), 10*time.Minute)
	value, _ := tickets.Issue("task-1", "sub-1")
	if _, err := tickets.Consume(value); err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.Consume(value); !errors.Is(err, ErrTicketUsed) {
		t.Fatalf("second consume = %v", err)
	}
}
```

测试二维码 fake：

```go
type recordingQREncoder struct {
	text string
	png  []byte
}

func (e *recordingQREncoder) Encode(text string) ([]byte, error) {
	e.text = text
	return e.png, nil
}
```

- [ ] **步骤 3：运行测试确认失败**

运行：`go test ./internal/mocksvc -run 'TestCreateTask|TestPoster|TestApplicationTicket' -v`

预期：FAIL，提示任务、海报或投递服务不存在。

- [ ] **步骤 4：实现任务模型和 Mock 场景**

```go
const ConsentVersion = "kuai-consent-v1"

type TaskStatus string

const (
	TaskQueued    TaskStatus = "queued"
	TaskAnalyzing TaskStatus = "analyzing"
	TaskComplete  TaskStatus = "complete"
	TaskFailed    TaskStatus = "failed"
)

type Analysis struct {
	Headline     string   `json:"headline"`
	Tags         []string `json:"tags"`
	Encouragement string  `json:"encouragement"`
}

type Task struct {
	ID        string      `json:"id"`
	SubjectID string      `json:"-"`
	KuAIID    string      `json:"kuai_id"`
	Status    TaskStatus  `json:"status"`
	Analysis  *Analysis   `json:"analysis,omitempty"`
	ErrorCode string      `json:"error_code,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type CreateTaskRequest struct {
	Identity       Identity
	Package        upload.Package
	ConsentVersion string
	IdempotencyKey string
}

type PosterModel struct {
	KuAIID        string
	Headline      string
	Tags          []string
	Encouragement string
	ApplicationURL string
}
```

`CreateTask` 以 `(SubjectID, IdempotencyKey)` 唯一索引防重。`success` 立即生成定性结果；`slow` 先返回 `queued`，后台任务在 35 秒后完成；`upload_error` 在创建任务前返回可重试错误；`analysis_error` 将任务置为 `failed`。任务 JSON 不得定义 `Score` 字段。

`LatestTask(subjectID)` 只返回该认证主体最新的一条任务，用于重新验证后恢复当前结果；不得提供分页列表或历史查询接口。

- [ ] **步骤 5：实现 30 天上传包清理**

上传包与 Task 分开存储。`Cleanup(now)` 删除 `CreatedAt <= now.Add(-30*24*time.Hour)` 的上传包，但保留 Task、Analysis、ConsentRecord 和 Identity。测试使用注入时钟，不等待真实时间。

- [ ] **步骤 6：实现 SVG 海报和动态二维码**

`RenderPoster` 使用 `html/template` 转义所有文案，输出固定尺寸 SVG。二维码边界为：

```go
type QREncoder interface {
	Encode(text string) ([]byte, error)
}

type GoQREncoder struct{}

func (GoQREncoder) Encode(text string) ([]byte, error) {
	return qrcode.Encode(text, qrcode.Medium, 256)
}

func RenderPoster(model PosterModel, encoder QREncoder) ([]byte, error)
```

生成的二维码 PNG 以 `data:image/png;base64,` 嵌入 SVG；测试 fake 记录传入编码器的内容，断言它等于受控的 localhost 投递 URL。海报只渲染 `KuAIID`、`Headline`、`Tags`、`Encouragement` 和二维码，不接收任何分数字段。

- [ ] **步骤 7：实现投递令牌**

令牌使用 `crypto/rand` 生成 32 字节随机值，存储 Task ID、Subject ID、过期时间和使用状态。默认 10 分钟过期；`Validate` 供 GET 投递页检查但不消费令牌，避免二维码预览或链接预取使令牌失效；只有成功的 `POST /api/applications` 调用 `Consume`，并使用互斥锁保证并发请求只有一次成功。`ticket_error` 场景签发一个已过期令牌。

- [ ] **步骤 8：运行 Mock 业务测试**

运行：`go test ./internal/mocksvc -v`

预期：授权、幂等、并发重试、全部场景、30 天清理、无分数海报、二维码、令牌过期和单次消费测试全部 PASS。

- [ ] **步骤 9：提交**

```bash
git add internal/mocksvc
git commit -m "feat: simulate kuAI analysis and application"
```

### 任务 6：实现受启动令牌保护的 localhost API

**文件：**
- 创建：`internal/webapp/server.go`
- 创建：`internal/webapp/routes.go`
- 创建：`internal/webapp/server_test.go`
- 创建：`internal/webapp/flow_test.go`

- [ ] **步骤 1：编写鉴权、路径和请求限制失败测试**

```go
func TestServerRequiresLaunchTokenAndSecurityHeaders(t *testing.T) {
	server := newHTTPTestServer(t)
	for _, path := range []string{"/", "/api/projects", "/api/health"} {
		resp := testRequest(t, server.URL+path, nil, nil)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s status = %d", path, resp.StatusCode)
		}
	}
	resp := testRequest(t, server.URL+"/api/health", nil, map[string]string{
		"X-Kuai-Token": "test-launch-token",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Cache-Control") != "no-store" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", resp.Header)
	}
}

func TestPrepareRejectsUnknownProjectAndOversizedBody(t *testing.T) {
	server := newHTTPTestServer(t)
	assertJSONStatus(t, server, "POST", "/api/prepare", `{"project_key":"unknown"}`, 400)
	assertOversizedStatus(t, server, "/api/prepare", 1<<20, 413)
}
```

`server_test.go` 定义 `testRequest`，使用 `http.NewRequest`、可选 JSON body 和 header map 发起请求，并在测试结束前关闭响应体。`assertJSONStatus` 与 `assertOversizedStatus` 只包装该助手，不绕过真实 HTTP handler。

- [ ] **步骤 2：编写完整 API 流程失败测试**

`internal/webapp/flow_test.go` 使用 `httptest.Server` 跑通：

```text
GET  /api/projects
POST /api/prepare
POST /api/auth/request-code
POST /api/auth/verify
POST /api/tasks
GET  /api/tasks/{taskID}
GET  /api/tasks/{taskID}/poster
GET  /application?ticket={ticket}
POST /api/applications
```

断言 Project 真实路径不出现在响应；手机号不出现在状态文件；没有用途授权时 `POST /api/tasks` 返回 400；相同幂等键返回相同 Task ID。

- [ ] **步骤 3：运行 HTTP 测试确认失败**

运行：`go test ./internal/webapp -v`

预期：FAIL，提示 server 和 routes 不存在。

- [ ] **步骤 4：实现 App 依赖边界**

```go
type App struct {
	LaunchToken string
	Projects    map[string]agentsview.Project
	Exporter    *upload.Exporter
	Prepared    *PreparedStore
	Auth        *mocksvc.Authenticator
	Tasks       *mocksvc.TaskService
	Tickets     *mocksvc.TicketService
}

func NewServer(address string, app *App) (*http.Server, net.Listener, error)
```

`App` 只保存 Project Key 到真实 Project 的服务端映射。浏览器的 `/api/prepare` 请求体只接受 `project_key`；真实路径和 session ID 均由 `App.Projects` 决定。

`PreparedStore` 使用随机 32 字节 `preparation_id` 在内存中保存脱敏后的 `upload.Package`，15 分钟过期：

```go
type PreparedStore struct {
	mu      sync.Mutex
	entries map[string]PreparedPackage
	clock   func() time.Time
}

type PreparedPackage struct {
	Package   upload.Package
	ExpiresAt time.Time
}

func (s *PreparedStore) Put(pkg upload.Package) (string, error)
func (s *PreparedStore) Get(id string) (upload.Package, error)
func (s *PreparedStore) Delete(id string)
```

`/api/prepare` 只向浏览器返回 `preparation_id` 和脱敏统计，不返回会话正文；`POST /api/tasks` 只接受该 ID、`ConsentVersion` 和幂等键。路由先用 `(SubjectID, IdempotencyKey)` 查询已有任务；命中时直接返回同一 Task，不再读取 preparation。首次创建成功后删除 preparation；创建失败时在过期前保留，供安全重试。

- [ ] **步骤 5：实现固定路由和请求契约**

允许路由：

```text
GET  /
GET  /poster
GET  /application
GET  /assets/app.js
GET  /assets/styles.css
GET  /api/health
GET  /api/projects
POST /api/prepare
POST /api/auth/request-code
POST /api/auth/verify
POST /api/tasks
GET  /api/tasks/{id}
GET  /api/tasks/latest
GET  /api/tasks/{id}/poster
POST /api/applications
```

本地控制页、资源、健康检查、Project、脱敏、认证和任务路由都验证启动令牌。首次 `GET /?token=...` 验证后设置 `HttpOnly; SameSite=Strict; Path=/` 的启动 cookie，并 303 重定向到不含 token 的 `/`；测试和非浏览器客户端可以使用 `X-Kuai-Token`。`/application?ticket=...` 和 `POST /api/applications` 以短期一次性 ticket 作为独立凭证，不要求也不暴露本地启动令牌。认证后的任务 API 还要求 `Authorization: Bearer <auth-token>`。JSON 请求体上限 64 KiB；未知字段、重复 `Content-Length`、`Transfer-Encoding`、未知路由和错误方法返回固定 JSON 错误。

- [ ] **步骤 6：增加安全响应与无敏感日志**

固定响应头：

```go
var securityHeaders = map[string]string{
	"Cache-Control": "no-store",
	"X-Content-Type-Options": "nosniff",
	"Referrer-Policy": "no-referrer",
	"Content-Security-Policy": "default-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
}
```

关闭默认请求目标日志。错误响应只返回短错误码和用户可操作摘要，不包含 subprocess stderr、绝对路径、手机号、验证码、会话正文或令牌。

- [ ] **步骤 7：运行 HTTP 测试**

运行：`go test ./internal/webapp -v`

预期：鉴权、未知路由、方法限制、大小限制、Project 可信映射、完整流程、异步轮询和敏感数据不泄漏测试全部 PASS。

- [ ] **步骤 8：提交**

```bash
git add internal/webapp/server.go internal/webapp/routes.go internal/webapp/server_test.go internal/webapp/flow_test.go
git commit -m "feat: expose protected kuAI mock API"
```

### 任务 7：实现 Project 选择、认证、海报和投递页面

**文件：**
- 创建：`internal/webapp/assets/index.html`
- 创建：`internal/webapp/assets/app.js`
- 创建：`internal/webapp/assets/styles.css`
- 创建：`internal/webapp/assets/poster.html`
- 创建：`internal/webapp/assets/application.html`
- 创建：`internal/webapp/assets_test.go`
- 修改：`internal/webapp/server.go`

- [ ] **步骤 1：编写页面契约失败测试**

```go
func TestAssetsUseProjectScopeAndExactConsent(t *testing.T) {
	index := readAsset(t, "assets/index.html")
	for _, required := range []string{
		"选择 Project",
		"生成分析海报",
		"候选人评估",
		"模型和分析算法改进",
		"暂不支持上传",
	} {
		if !strings.Contains(index, required) {
			t.Fatalf("missing %q", required)
		}
	}
	for _, forbidden := range []string{"数据不上传", "真实分数", "选择 session"} {
		if strings.Contains(index, forbidden) {
			t.Fatalf("forbidden copy %q", forbidden)
		}
	}
}

func TestPosterAndScriptExposeNoScoreContract(t *testing.T) {
	all := readAsset(t, "assets/poster.html") + readAsset(t, "assets/app.js")
	for _, forbidden := range []string{"score", "分数", "权重", "排名", "阈值"} {
		if strings.Contains(strings.ToLower(all), forbidden) {
			t.Fatalf("leaked %q", forbidden)
		}
	}
}
```

`assets_test.go` 定义：

```go
func readAsset(t *testing.T, name string) string {
	t.Helper()
	body, err := fs.ReadFile(assets, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
```

- [ ] **步骤 2：运行页面测试确认失败**

运行：`go test ./internal/webapp -run 'TestAssets|TestPosterAndScript' -v`

预期：FAIL，提示嵌入资源不存在。

- [ ] **步骤 3：实现浏览器状态机**

`app.js` 使用单一状态对象：

```javascript
const initialState = {
  phase: "projects",
  projects: [],
  selectedProject: null,
  preparation: null,
  phone: "",
  auth: null,
  consent: false,
  task: null,
  error: ""
};
```

允许的 phase 固定为 `projects`、`redacting`、`auth`、`ready`、`uploading`、`analyzing`、`complete`、`failed`。按钮禁用条件必须由状态派生；请求进行中禁止重复提交。所有动态内容使用 `textContent`，不得将 session 或服务端文案写入 `innerHTML`。

- [ ] **步骤 4：实现 Project 页面**

页面按来源状态显示 Project 卡片：

- 可上传：显示安全 Project 名称、Agent、session 数和“选择”按钮；
- 不可上传：显示“已检测，暂不支持上传”，按钮禁用；
- 未发现：显示已检查 Agent 和排障指引；
- 调试模式：只有 bootstrap 数据包含 `debug_session_upload: true` 时显示单 session 控件。

选择 Project 后调用 `/api/prepare`，展示 session 数、替换数量、删除字段数量和省略完整文件读取次数，不显示敏感原文。

- [ ] **步骤 5：实现手机号认证和用途授权**

认证只在脱敏完成后显示。验证码输入为 6 位；Mock 页面明确提示演示验证码 `246810`。用途授权使用一个默认未选中的复选框，旁边完整列出三类用途。未认证或未授权时上传按钮禁用。

- [ ] **步骤 6：实现任务轮询、海报和投递**

手机号验证成功后先请求 `/api/tasks/latest`；存在未完成或已完成的最新任务时恢复对应状态，不渲染历史列表。创建新任务后每秒轮询 `/api/tasks/{id}`。浏览器在 30 秒后将文案改为“分析已转为后台任务，可以稍后回来”，但继续轮询。完成后进入 `/poster`；海报页面使用普通下载链接：

```html
<a id="downloadPoster" download="kuAI-分析海报.svg">下载海报</a>
```

投递页提交姓名、邮箱、职位和 ticket 到 `/api/applications`，不允许重复提交同一 ticket；过期、已使用和篡改 ticket 显示安全错误。海报页同时提供一个指向相同 ticket URL 的“模拟扫码投递”链接，用于 localhost Mock 演示；不得声称手机可以访问电脑的回环地址。

- [ ] **步骤 7：增加响应式与可访问性**

确保：

- 所有输入有 `<label>`；
- 状态容器使用 `aria-live="polite"`；
- 错误容器使用 `role="alert"`；
- 键盘可完成 Project 选择、认证、上传、下载和投递；
- 360px 宽度无横向溢出；
- 支持 `prefers-reduced-motion`；
- 焦点样式清晰可见。

- [ ] **步骤 8：嵌入资源并运行测试**

在 `server.go` 使用：

```go
//go:embed assets/*
var assets embed.FS
```

运行：`go test ./internal/webapp -v`

预期：资源、文案、状态机、DOM 安全、无分数、可访问性和完整 HTTP 流程测试全部 PASS。

- [ ] **步骤 9：提交**

```bash
git add internal/webapp
git commit -m "feat: add kuAI project upload experience"
```

### 任务 8：完成 CLI 生命周期和跨平台安装器

**文件：**
- 修改：`cmd/kuai/main.go`
- 创建：`cmd/kuai/main_test.go`
- 创建：`install.sh`
- 创建：`install.ps1`
- 创建：`tests/test_kuai_install_sh.sh`
- 创建：`tests/Test-KuaiInstall.ps1`
- 创建：`scripts/build-kuai-release.sh`

- [ ] **步骤 1：编写 CLI 启动和浏览器降级失败测试**

```go
func TestRunSyncsAndPrintsSafeStartupEvent(t *testing.T) {
	deps := fakeDependencies()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--no-browser"}, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var event struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(&stdout).Decode(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "server-started" || !strings.HasPrefix(event.URL, "http://127.0.0.1:") {
		t.Fatalf("event = %#v", event)
	}
	if strings.Contains(stderr.String(), "token=") {
		t.Fatalf("token leaked to stderr: %s", stderr.String())
	}
}
```

`main_test.go` 的 `fakeDependencies` 返回实现扫描、监听、浏览器打开和信号等待接口的内存 fake；监听器固定返回 `127.0.0.1` 地址，避免测试启动真实浏览器或访问网络。

- [ ] **步骤 2：运行 CLI 测试确认失败**

运行：`go test ./cmd/kuai -v`

预期：FAIL，因为完整依赖组装和启动逻辑尚不存在。

- [ ] **步骤 3：完成 CLI 依赖组装**

`run` 的执行顺序固定为：

1. 解析配置；
2. 定位并验证可执行的 `agentsview`；
3. 除非 `--skip-sync`，执行 `agentsview sync`；
4. 加载 session 并聚合 Project；
5. 创建上传 Exporter、Mock Auth、Store、Task 和 Ticket 服务；
6. 启动 `127.0.0.1:0`；
7. 向 stdout 打印一行 `server-started` JSON；
8. 除非 `--no-browser`，按 Darwin、Linux、Windows 分别调用 `open`、`xdg-open`、`rundll32 url.dll,FileProtocolHandler`；
9. 等待 SIGINT 或 SIGTERM 并在 5 秒内关闭服务。

完整 URL 只输出到用户自己的 stdout；stderr 和后续日志不得重复打印启动令牌。

- [ ] **步骤 4：编写 Shell 安装器测试**

`tests/test_kuai_install_sh.sh` 使用临时 HOME 和假的 `curl`、`uname`、`sha256sum`，验证：

- Darwin/Linux 与 amd64/arm64 产物名称；
- 分别从受信任的 kuAI Release 和 agentsview Release 下载产物；
- `kuai` 和 `agentsview` 必须分别出现在各自 Release 的 `SHA256SUMS`；
- 任一校验失败不覆盖旧版本；
- 成功后原子安装到 `~/.local/bin`；
- 重复安装不会产生 PATH 重复项；
- 不支持平台在下载前停止。

- [ ] **步骤 5：实现 `install.sh`**

默认产物：

```text
kuai-{darwin|linux|windows}-{amd64|arm64}[.exe]
agentsview-{darwin|linux|windows}-{amd64|arm64}[.exe]
kuAI Release/SHA256SUMS
agentsview Release/SHA256SUMS
```

`kuai` 下载基址只从脚本内置受信任值或 `KUAI_RELEASE_URL` 读取，`agentsview` 下载基址只从内置受信任值或 `KUAI_AGENTSVIEW_RELEASE_URL` 读取；两个地址不能由本地网页覆盖。Mock 测试通过假的下载器覆盖，不访问公网。安装器先下载并验证两个产物，再使用 `mktemp -d`、同目录临时文件和 `mv` 原子替换；任一下载或校验失败时两个已安装版本都保持不变。

- [ ] **步骤 6：编写并实现 PowerShell 安装器**

`tests/Test-KuaiInstall.ps1` 验证 x64/arm64、`Get-FileHash -Algorithm SHA256`、旧版本保留、当前用户 PATH 去重和 `%LOCALAPPDATA%\kuai\bin` 安装。

`install.ps1` 只修改当前用户 PATH，不请求管理员权限；任一产物失败时同时保留现有 `kuai.exe` 和 `agentsview.exe`。

- [ ] **步骤 7：实现多平台构建脚本**

`scripts/build-kuai-release.sh` 使用显式矩阵：

```text
darwin/amd64
darwin/arm64
linux/amd64
linux/arm64
windows/amd64
windows/arm64
```

对 `./cmd/kuai` 设置 `CGO_ENABLED=0`，输出到 `dist/`，Windows 添加 `.exe`。脚本只生成 kuAI 自有产物和排序稳定的 `SHA256SUMS`；agentsview 继续使用其独立 Release 和校验清单。不得把 `dist/` 加入 Git。

- [ ] **步骤 8：运行 CLI 和安装器测试**

运行：`go test ./cmd/kuai -v`

运行：`bash tests/test_kuai_install_sh.sh`

运行（有 PowerShell 的环境）：`pwsh -NoProfile -File tests/Test-KuaiInstall.ps1`

运行：`bash scripts/build-kuai-release.sh`

预期：CLI 和 Shell 测试 PASS；PowerShell 测试在 Windows CI PASS；`dist/` 包含六个平台的 `kuai` 产物和 kuAI `SHA256SUMS`。

- [ ] **步骤 9：提交**

```bash
git add cmd/kuai install.sh install.ps1 tests/test_kuai_install_sh.sh tests/Test-KuaiInstall.ps1 scripts/build-kuai-release.sh
git commit -m "feat: distribute kuAI mock client"
```

### 任务 9：Skill、文档、CI 和最终验收

**文件：**
- 创建：`kuai.md`
- 修改：`README.md`
- 创建：`.github/workflows/kuai-mock.yml`
- 修改：`.gitignore`

- [ ] **步骤 1：编写文档契约失败测试**

在 `internal/webapp/assets_test.go` 增加仓库文档检查，要求 README 和 skill 同时包含：

```text
Project 粒度
本地自动脱敏
Mock 不访问公网
Claude
agentreview
kuAI ID
生成分析海报
候选人评估
模型和分析算法改进
```

并断言不存在“所有 Agent 均可上传”“数据完全不上传”“公开真实分数”等错误承诺。

- [ ] **步骤 2：运行文档测试确认失败**

运行：`go test ./internal/webapp -run TestDocumentationContract -v`

预期：FAIL，因为 `kuai.md` 和 README 新章节尚不存在。

- [ ] **步骤 3：编写统一 skill**

`kuai.md` 使用严格 frontmatter：

```yaml
---
name: kuai
description: 扫描本机 Agent Project，在本地脱敏后运行 kuAI Mock 上传、分析海报与关联投递流程
version: "0.1.0"
---
```

skill 的确定步骤：

1. 检查 `kuai` 命令；
2. 未安装时调用对应平台的统一安装器；
3. 告知用户即将扫描本机会话，但不会默认上传；
4. 运行 `kuai`；
5. 引导用户在本地页面选择 Project；
6. 明确 Mock 验证码和三类用途；
7. 不在聊天中复述包含启动令牌的完整 URL；
8. 不使用 `--debug-session-upload`，除非用户明确要求调试；
9. 不设置任意 `KUAI_RELEASE_URL` 或绕过校验。

- [ ] **步骤 4：更新 README**

保留现有 avscore 文档，在开头新增“kuAI Mock MVP”章节，包括：

- 产品定位；
- 一条命令和 skill 安装；
- `kuai` 启动流程；
- Project 粒度和格式支持边界；
- 本地脱敏和禁止上传内容；
- Mock 验证码；
- 三类数据用途；
- 30 天与长期数据策略是正式服务契约，Mock 仅在本地模拟；
- 异步任务恢复；
- 海报和二维码投递；
- 环境变量与调试场景；
- 完整验证命令。

- [ ] **步骤 5：创建 CI**

`.github/workflows/kuai-mock.yml` 包含：

- Ubuntu：`go test ./...`、`go vet ./...`、Shell 安装器测试、`git diff --check`；
- macOS：`go test ./...`、Shell 安装器测试；
- Windows：`go test ./...`、PowerShell 安装器测试；
- Ubuntu release job：运行构建脚本并检查六个平台产物和 `SHA256SUMS`；
- 所有 job 禁止运行真实安装 URL或真实短信服务。

- [ ] **步骤 6：更新忽略规则**

在 `.gitignore` 增加：

```gitignore
dist/
kuai
kuai.exe
```

不得忽略 `go.sum`、安装器、Mock fixture 或测试输出断言文件。

- [ ] **步骤 7：运行完整验证**

运行：

```bash
go test ./...
go vet ./...
bash tests/test_kuai_install_sh.sh
python3 -m unittest discover -s tests -v
bash tests/test_avscore_sh.sh
node --test tests/aiti_mock.test.js
bash -n avscore.sh
bash -n install.sh
git diff --check
```

在 Windows CI 运行：

```powershell
go test ./...
pwsh -NoProfile -File tests/Test-KuaiInstall.ps1
```

预期：新旧测试全部 PASS；Shell 语法检查通过；没有空白错误；Windows 安装器测试通过。

- [ ] **步骤 8：执行本地 Mock 冒烟验收**

使用临时数据目录和 fixture session 启动：

```bash
KUAI_DATA_DIR="$(mktemp -d)" KUAI_AGENTSVIEW_PATH="/absolute/path/to/fake-agentsview" go run ./cmd/kuai --no-browser
```

在私密终端打开启动 JSON 中的本地 URL，逐项验证：

1. 可选择受支持 Project；
2. 不支持 Agent 可见但不可选择；
3. 脱敏统计可见且无敏感原文；
4. 验证码 `246810` 创建或恢复同一 `kuAI ID`；
5. 未授权时无法上传；
6. `success` 返回可下载 SVG 海报，二维码编码受控本地投递 URL，“模拟扫码投递”链接可打开相同页面；
7. `upload_error` 可用同一幂等键重试；
8. `analysis_error` 不生成海报；
9. `slow` 在 30 秒后显示异步状态并最终完成；
10. `ticket_error` 安全拒绝投递。

- [ ] **步骤 9：提交**

```bash
git add kuai.md README.md .github/workflows/kuai-mock.yml .gitignore internal/webapp/assets_test.go
git commit -m "docs: complete kuAI mock MVP workflow"
```

## 计划完成后的验收门槛

执行者只有在以下证据全部存在时才能宣称 Mock MVP 完成：

1. `go test ./...` 和 `go vet ./...` 通过；
2. macOS/Linux Shell 安装器测试通过；
3. Windows PowerShell 安装器测试在 Windows CI 通过；
4. 现有 Python、Shell 和 Node 测试仍通过；
5. 六个平台的 `kuai` 产物可构建并有校验和；
6. 上传包测试证明附件、完整文件读取结果、二进制和脱敏前敏感文本不会进入 Mock 服务；
7. HTTP 测试证明浏览器不能提交任意 Project、session ID 或上传地址；
8. 身份测试证明手机号不落入业务状态；
9. 海报测试证明不存在真实分数、权重、排名、阈值或评分字段；
10. 冒烟测试证明正常流程与五种失败场景可演示。
