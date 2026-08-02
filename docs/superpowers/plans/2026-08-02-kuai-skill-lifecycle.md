# kuAI Agent Skill 生命周期实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 将 kuAI Skill 嵌入原生 CLI，并实现安全、幂等、可恢复的 `kuai skill install|status|upgrade|uninstall` 命令。

**架构：** `internal/skillasset` 保存唯一 Skill 源并通过 `go:embed` 提供字节；`internal/skillmgr` 负责目标解析、状态分类、安全文件操作、管理标记、锁与恢复日志；`cmd/kuai` 只解析命令并把请求交给 manager。单目标替换在目标文件系统内原子完成，`all` 使用完整预检、分步提交、best-effort 回滚和下次命令恢复，不宣称跨目录瞬时原子。

**技术栈：** Go 1.26.5、`go:embed`、`golang.org/x/sys/unix`、`golang.org/x/sys/windows`、Python unittest 文档契约测试。

---

## 文件结构

- 创建 `internal/skillasset/asset.go`：嵌入并校验 canonical Skill。
- 移动 `kuai.md` → `internal/skillasset/kuai/SKILL.md`：唯一 Skill 内容源。
- 创建 `internal/skillasset/asset_test.go`：frontmatter、版本和内容哈希测试。
- 创建 `internal/skillmgr/types.go`：公开请求、结果、状态和错误码类型。
- 创建 `internal/skillmgr/paths.go`：agents、claude、all、custom 目标解析。
- 创建 `internal/skillmgr/marker.go`：`.kuai-managed.json` 编解码和内容分类。
- 创建 `internal/skillmgr/semver.go`：不依赖外部库的 SemVer 2.0.0 precedence 比较。
- 创建 `internal/skillmgr/semver_test.go`：正式版和 prerelease 比较测试。
- 创建 `internal/skillmgr/safeops.go`：安全文件操作接口。
- 创建 `internal/skillmgr/safeops_unix.go`：POSIX no-follow/fd-relative 实现。
- 创建 `internal/skillmgr/safeops_windows.go`：Windows reparse-point-safe 实现。
- 创建 `internal/skillmgr/lock_unix.go`：POSIX 跨进程 root 锁。
- 创建 `internal/skillmgr/lock_windows.go`：Windows 跨进程 root 锁。
- 创建 `internal/skillmgr/manager.go`：单目标 install/status/upgrade/uninstall。
- 创建 `internal/skillmgr/transaction.go`：多目标锁、恢复日志和恢复入口。
- 创建 `internal/skillmgr/manager_test.go`：生命周期与故障注入测试。
- 创建 `internal/skillmgr/safeops_unix_test.go`：POSIX 链接和替换竞态测试。
- 创建 `internal/skillmgr/safeops_windows_test.go`：Windows reparse point 测试。
- 创建 `cmd/kuai/skill_command.go`：`skill` 子命令解析和 JSON/人类输出。
- 创建 `cmd/kuai/skill_command_test.go`：命令面和退出码测试。
- 修改 `cmd/kuai/main.go`：在扫描/UI 初始化前分派 `skill` 命令。
- 修改 `cmd/kuai/main_test.go`：help 和无运行时初始化契约。
- 修改 `tests/test_docs.py`：canonical Skill 新路径和命令文档契约。
- 修改 `tests/test_single_binary_contract.py`：Skill 仍只调用同一个 `kuai`。
- 修改 `README.md`：记录 Skill 生命周期命令，但保留 npm 安装说明给下一份计划完成。

## 任务 1：建立 canonical Skill 资产

**文件：**
- 创建：`internal/skillasset/asset.go`
- 创建：`internal/skillasset/asset_test.go`
- 移动：`kuai.md` → `internal/skillasset/kuai/SKILL.md`
- 修改：`tests/test_docs.py`
- 修改：`tests/test_single_binary_contract.py`

- [ ] **步骤 1：先写失败的嵌入资产测试**

```go
package skillasset

import (
    "bytes"
    "testing"
)

func TestEmbeddedSkillIsCanonical(t *testing.T) {
    body := Bytes()
    if !bytes.HasPrefix(body, []byte("---\nname: kuai\n")) {
        t.Fatalf("invalid frontmatter: %q", body[:min(len(body), 80)])
    }
    if Version() != "1.0.0" {
        t.Fatalf("version=%q", Version())
    }
    first := Bytes()
    first[0] = 'x'
    if bytes.Equal(first, Bytes()) {
        t.Fatal("Bytes returned mutable embedded storage")
    }
}
```

- [ ] **步骤 2：运行测试并确认因 API 不存在而失败**

运行：`go test ./internal/skillasset -run TestEmbeddedSkillIsCanonical -count=1`

预期：FAIL，编译器报告 `undefined: Bytes` 和 `undefined: Version`。

- [ ] **步骤 3：移动现有 Skill 并实现最小嵌入 API**

`internal/skillasset/asset.go`：

```go
package skillasset

import (
    "bytes"
    "embed"
    "regexp"
)

//go:embed kuai/SKILL.md
var files embed.FS

var versionLine = regexp.MustCompile(`(?m)^version: "([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)"$`)

func Bytes() []byte {
    value, err := files.ReadFile("kuai/SKILL.md")
    if err != nil {
        panic(err)
    }
    return bytes.Clone(value)
}

func Version() string {
    match := versionLine.FindSubmatch(Bytes())
    if len(match) != 2 {
        panic("kuai: embedded skill has invalid version")
    }
    return string(match[1])
}
```

移动时保留当前 `kuai.md` 的完整内容，不重新生成或丢弃工作区已有修改。

- [ ] **步骤 4：更新文档契约使用唯一新路径**

在 `tests/test_docs.py` 中改为：

```python
SKILL = ROOT / "internal" / "skillasset" / "kuai" / "SKILL.md"
```

在 `tests/test_single_binary_contract.py` 中把文件列表改为：

```python
for relative in (
    "install.sh",
    "install.ps1",
    "internal/skillasset/kuai/SKILL.md",
):
```

- [ ] **步骤 5：运行资产和文档测试**

运行：

```bash
go test ./internal/skillasset -count=1
python3 -m unittest tests.test_docs tests.test_single_binary_contract -v
```

预期：全部 PASS；仓库中只有 `internal/skillasset/kuai/SKILL.md` 承担 canonical Skill 内容。

- [ ] **步骤 6：提交 canonical 资产**

```bash
git add internal/skillasset/asset.go internal/skillasset/asset_test.go internal/skillasset/kuai/SKILL.md kuai.md tests/test_docs.py tests/test_single_binary_contract.py
git commit -m "refactor: embed canonical kuai skill"
```

## 任务 2：定义 Skill 请求、目标路径和冲突类型

**文件：**
- 创建：`internal/skillmgr/types.go`
- 创建：`internal/skillmgr/paths.go`
- 创建：`internal/skillmgr/manager_test.go`

- [ ] **步骤 1：写路径解析失败测试**

```go
func TestResolveTargets(t *testing.T) {
    home := t.TempDir()
    cases := []struct {
        name string
        req  Request
        want []string
        fail bool
    }{
        {"agents", Request{Target: TargetAgents}, []string{filepath.Join(home, ".agents", "skills")}, false},
        {"claude", Request{Target: TargetClaude}, []string{filepath.Join(home, ".claude", "skills")}, false},
        {"all", Request{Target: TargetAll}, []string{filepath.Join(home, ".agents", "skills"), filepath.Join(home, ".claude", "skills")}, false},
        {"custom", Request{Target: TargetCustom, Root: filepath.Join(home, "custom-skills")}, []string{filepath.Join(home, "custom-skills")}, false},
        {"custom relative", Request{Target: TargetCustom, Root: "relative"}, nil, true},
        {"custom home", Request{Target: TargetCustom, Root: home}, nil, true},
        {"root with all", Request{Target: TargetAll, Root: filepath.Join(home, "custom")}, nil, true},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got, err := ResolveTargets(tc.req, home)
            if tc.fail != (err != nil) || !reflect.DeepEqual(got, tc.want) {
                t.Fatalf("got=%v err=%v", got, err)
            }
        })
    }
}
```

- [ ] **步骤 2：运行测试确认类型不存在**

运行：`go test ./internal/skillmgr -run TestResolveTargets -count=1`

预期：FAIL，编译器报告 `Request`、`TargetAgents` 和 `ResolveTargets` 未定义。

- [ ] **步骤 3：实现固定类型和解析规则**

`internal/skillmgr/types.go`：

```go
package skillmgr

type Action string
type Target string

const (
    ActionInstall   Action = "install"
    ActionStatus    Action = "status"
    ActionUpgrade   Action = "upgrade"
    ActionUninstall Action = "uninstall"
    TargetAgents    Target = "agents"
    TargetClaude    Target = "claude"
    TargetAll       Target = "all"
    TargetCustom    Target = "custom"
)

type Request struct {
    Action         Action
    Target         Target
    Root           string
    DryRun         bool
    Force          bool
    AllowDowngrade bool
}

type Result struct {
    Action  Action `json:"action"`
    Target  Target `json:"target"`
    Path    string `json:"path"`
    Version string `json:"version"`
    Status  string `json:"status"`
    Backup  string `json:"backup,omitempty"`
}

type ConflictError struct{ Path, Reason string }
func (e *ConflictError) Error() string { return "kuai skill conflict at " + e.Path + ": " + e.Reason }
```

`internal/skillmgr/paths.go` 必须返回 skills root，而不是最终 `kuai` 目录；最终目录统一由 `filepath.Join(root, "kuai")` 得到。custom root 规范化后拒绝卷根、`/` 和用户 HOME。

- [ ] **步骤 4：运行路径测试**

运行：`go test ./internal/skillmgr -run TestResolveTargets -count=1`

预期：PASS。

- [ ] **步骤 5：提交类型和路径边界**

```bash
git add internal/skillmgr/types.go internal/skillmgr/paths.go internal/skillmgr/manager_test.go
git commit -m "feat: define kuai skill targets"
```

## 任务 3：实现管理标记和状态分类

**文件：**
- 创建：`internal/skillmgr/marker.go`
- 创建：`internal/skillmgr/semver.go`
- 创建：`internal/skillmgr/semver_test.go`
- 修改：`internal/skillmgr/manager_test.go`

- [ ] **步骤 1：写完整状态表测试**

```go
func TestClassifyManagedSkill(t *testing.T) {
    current := []byte("current")
    old := []byte("old")
    cases := []struct{
        name string
        input Snapshot
        want State
    }{
        {"missing", Snapshot{}, StateMissing},
        {"unmanaged", Snapshot{Exists: true, Skill: old}, StateUnmanaged},
        {"current", Snapshot{Exists: true, Skill: current, Marker: markerFor(current, "1.0.0")}, StateCurrent},
        {"upgradeable", Snapshot{Exists: true, Skill: old, Marker: markerFor(old, "0.9.0")}, StateUpgradeable},
        {"modified", Snapshot{Exists: true, Skill: current, Marker: markerFor(old, "0.9.0")}, StateModified},
    }
    for _, tc := range cases {
        if got := Classify(tc.input, current, "1.0.0"); got != tc.want {
            t.Fatalf("%s: got=%s want=%s", tc.name, got, tc.want)
        }
    }
}

func markerFor(content []byte, version string) *Marker {
    sum := sha256.Sum256(content)
    return &Marker{Manager: "kuai", SkillVersion: version, ContentSHA256: hex.EncodeToString(sum[:])}
}
```

`semver_test.go` 必须包含以下 precedence 表：

```go
func TestCompareSemver(t *testing.T) {
    ordered := []string{
        "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
        "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0",
    }
    for i := 0; i < len(ordered)-1; i++ {
        if got, err := compareSemver(ordered[i], ordered[i+1]); err != nil || got >= 0 {
            t.Fatalf("compare %q %q = %d, %v", ordered[i], ordered[i+1], got, err)
        }
    }
    for _, invalid := range []string{"v1.0.0", "1.0", "01.0.0", "1.0.0-"} {
        if _, err := compareSemver(invalid, "1.0.0"); err == nil { t.Fatalf("accepted %q", invalid) }
    }
}
```

- [ ] **步骤 2：运行测试验证 API 缺失**

运行：`go test ./internal/skillmgr -run TestClassifyManagedSkill -count=1`

预期：FAIL，报告 `Snapshot`、`State` 和 `Classify` 未定义。

- [ ] **步骤 3：实现 marker 和分类**

```go
type Marker struct {
    Manager        string    `json:"manager"`
    SkillVersion   string    `json:"skillVersion"`
    ContentSHA256  string    `json:"contentSHA256"`
    InstalledAt    time.Time `json:"installedAt"`
    PackageVersion string    `json:"sourcePackageVersion"`
}

type Snapshot struct {
    Exists bool
    Skill  []byte
    Marker *Marker
}

type State string
const (
    StateMissing     State = "missing"
    StateUnmanaged   State = "unmanaged"
    StateCurrent     State = "current"
    StateUpgradeable State = "upgradeable"
    StateModified    State = "modified"
)
```

`compareSemver` 必须实现 SemVer 2.0.0 的 numeric core 和 prerelease precedence，忽略 build metadata 的 precedence；禁止 `v` 前缀和 core leading zero。`Classify` 必须先用 marker 中的 SHA-256 判断用户是否修改，再比较 SemVer；无法解析的 marker 归入 `StateModified`，不得自动覆盖。

- [ ] **步骤 4：运行分类测试和 race**

运行：`go test -race ./internal/skillmgr -run 'TestClassifyManagedSkill|TestCompareSemver' -count=1`

预期：PASS。

- [ ] **步骤 5：提交状态模型**

```bash
git add internal/skillmgr/marker.go internal/skillmgr/semver.go internal/skillmgr/semver_test.go internal/skillmgr/manager_test.go
git commit -m "feat: classify managed kuai skills"
```

## 任务 4：实现平台安全文件操作

**文件：**
- 创建：`internal/skillmgr/safeops.go`
- 创建：`internal/skillmgr/safeops_unix.go`
- 创建：`internal/skillmgr/safeops_windows.go`
- 创建：`internal/skillmgr/safeops_unix_test.go`
- 创建：`internal/skillmgr/safeops_windows_test.go`

- [ ] **步骤 1：写 POSIX symlink 拒绝测试**

```go
//go:build !windows

func TestOpenRootRejectsSymlinkComponent(t *testing.T) {
    base := t.TempDir()
    realRoot := filepath.Join(base, "real")
    if err := os.Mkdir(realRoot, 0o755); err != nil { t.Fatal(err) }
    linked := filepath.Join(base, "linked")
    if err := os.Symlink(realRoot, linked); err != nil { t.Fatal(err) }
    if _, err := newSafeRoot(linked); err == nil {
        t.Fatal("symlink root accepted")
    }
}
```

- [ ] **步骤 2：写 Windows reparse point 拒绝测试**

```go
//go:build windows

func TestOpenRootRejectsReparsePoint(t *testing.T) {
    base := t.TempDir()
    target := filepath.Join(base, "target")
    if err := os.Mkdir(target, 0o755); err != nil { t.Fatal(err) }
    link := filepath.Join(base, "link")
    if err := os.Symlink(target, link); err != nil { t.Skip(err) }
    if _, err := newSafeRoot(link); err == nil {
        t.Fatal("reparse point accepted")
    }
}
```

- [ ] **步骤 3：运行本平台测试并确认失败**

运行：`go test ./internal/skillmgr -run 'TestOpenRootRejects' -count=1`

预期：FAIL，报告 `newSafeRoot` 未定义。

- [ ] **步骤 4：定义统一最小接口**

```go
type safeRoot interface {
    ReadSkill() (Snapshot, error)
    Stage(skill []byte, marker Marker) (string, error)
    Swap(staged string, backup string) error
    RestoreBackup(backup string) error
    RemoveManaged(backup string) error
    VerifyIdentity() error
    Close() error
}

type rootLock interface { Release() error }
func acquireRootLock(ctx context.Context, stateDir, canonicalRoot string) (rootLock, error)
```

POSIX 实现必须逐组件 `Lstat`，打开 root 目录时使用 `unix.Open(..., O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC, 0)`，子项操作使用 `Openat/Renameat/Unlinkat`。Windows 实现使用 `windows.CreateFile`、`FILE_FLAG_OPEN_REPARSE_POINT|FILE_FLAG_BACKUP_SEMANTICS`，并在 swap 前重新比较 `BY_HANDLE_FILE_INFORMATION` 的卷序列号和文件索引。`RestoreBackup` 必须是受身份复核保护的明确反向操作，不能把 `Swap` 隐式倒用。

锁文件位于 `filepath.Join(stateDir, "skill-locks", sha256(canonicalRoot)+".lock")`，文件名不含用户路径。POSIX 使用 advisory `flock`，Windows 使用 `LockFileEx`；同一 root 跨进程互斥，不同 root 可并行。等待每 50ms 检查 `ctx.Done()`，context 取消或 30 秒超时返回 I/O 错误；`Release` 幂等。测试必须覆盖同 root 第二持有者阻塞/取消、不同 root 并行、逆序释放和进程退出后可重新获取。

- [ ] **步骤 5：验证两个目标都能交叉编译**

运行：

```bash
go test ./internal/skillmgr -count=1
GOOS=darwin GOARCH=arm64 go test -c ./internal/skillmgr -o /tmp/skillmgr-darwin.test
GOOS=windows GOARCH=amd64 go test -c ./internal/skillmgr -o /tmp/skillmgr-windows.test.exe
```

预期：本机测试 PASS，两个 test binary 成功生成；实现中没有跟随目标 symlink/reparse point 的路径。

- [ ] **步骤 6：提交安全操作层**

```bash
git add internal/skillmgr/safeops.go internal/skillmgr/safeops_unix.go internal/skillmgr/safeops_windows.go internal/skillmgr/lock_unix.go internal/skillmgr/lock_windows.go internal/skillmgr/safeops_unix_test.go internal/skillmgr/safeops_windows_test.go
git commit -m "feat: add safe skill filesystem operations"
```

## 任务 5：实现单目标生命周期

**文件：**
- 创建：`internal/skillmgr/manager.go`
- 修改：`internal/skillmgr/manager_test.go`

- [ ] **步骤 1：写 install/status/upgrade/uninstall 表驱动测试**

```go
func TestSingleTargetLifecycle(t *testing.T) {
    root := filepath.Join(t.TempDir(), "skills")
    manager := newTestManager(t, root, []byte("v1"), "1.0.0")
    results, err := manager.Run(context.Background(), Request{Action: ActionInstall, Target: TargetCustom, Root: root})
    assertResult(t, results, err, "installed")
    results, err = manager.Run(context.Background(), Request{Action: ActionInstall, Target: TargetCustom, Root: root})
    assertResult(t, results, err, "current")
    manager.Skill, manager.SkillVersion = []byte("v2"), "1.1.0"
    results, err = manager.Run(context.Background(), Request{Action: ActionUpgrade, Target: TargetCustom, Root: root})
    assertResult(t, results, err, "upgraded")
    results, err = manager.Run(context.Background(), Request{Action: ActionUninstall, Target: TargetCustom, Root: root})
    assertResult(t, results, err, "uninstalled")
    results, err = manager.Run(context.Background(), Request{Action: ActionUninstall, Target: TargetCustom, Root: root})
    assertResult(t, results, err, "missing")
}

func newTestManager(t *testing.T, root string, skill []byte, version string) *Manager {
    t.Helper()
    return &Manager{
        HomeDir: t.TempDir(), StateDir: filepath.Join(t.TempDir(), "state"),
        Skill: skill, SkillVersion: version, PackageVersion: version,
        Now: func() time.Time { return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC) },
    }
}

func assertResult(t *testing.T, results []Result, err error, want string) {
    t.Helper()
    if err != nil || len(results) != 1 || results[0].Status != want {
        t.Fatalf("results=%#v err=%v want=%q", results, err, want)
    }
}
```

另写 `TestModifiedSkillConflicts`、`TestForceBacksUpBeforeReplace`、`TestDowngradeRequiresFlag` 和 `TestDryRunDoesNotWrite`，明确断言 `ConflictError`、备份路径和文件内容。

- [ ] **步骤 2：运行生命周期测试并确认失败**

运行：`go test ./internal/skillmgr -run 'TestSingleTargetLifecycle|TestModified|TestForce|TestDowngrade|TestDryRun' -count=1`

预期：FAIL，报告 `Manager.Run` 未定义。

- [ ] **步骤 3：实现 Manager API**

```go
type Manager struct {
    HomeDir        string
    StateDir       string
    Skill          []byte
    SkillVersion   string
    PackageVersion string
    Now            func() time.Time
    openRoot       func(string) (safeRoot, error)
    beforeSwap     func(string) error
}

func (m *Manager) Run(ctx context.Context, req Request) ([]Result, error)
```

实现必须遵守状态表：未知/修改内容默认冲突；`--force` 先备份；重复安装不改 mtime；卸载未知目录不删除；降级只有 `AllowDowngrade` 才允许。`DryRun` 只返回计划结果，不创建 root、锁、staging、marker 或备份。

- [ ] **步骤 4：运行 manager 测试和 race**

运行：`go test -race ./internal/skillmgr -count=1`

预期：PASS。

- [ ] **步骤 5：提交单目标生命周期**

```bash
git add internal/skillmgr/manager.go internal/skillmgr/manager_test.go
git commit -m "feat: manage kuai skill lifecycle"
```

## 任务 6：实现 all 的锁、恢复日志和崩溃恢复

**文件：**
- 创建：`internal/skillmgr/transaction.go`
- 修改：`internal/skillmgr/manager.go`
- 修改：`internal/skillmgr/manager_test.go`

- [ ] **步骤 1：写第二目标失败回滚和崩溃恢复测试**

```go
func TestAllRollsBackFirstTargetWhenSecondFails(t *testing.T) {
    manager, agentsRoot, claudeRoot := newAllManager(t)
    manager.beforeSwap = func(root string) error {
        if root == claudeRoot { return errors.New("injected second-target failure") }
        return nil
    }
    _, err := manager.Run(context.Background(), Request{Action: ActionInstall, Target: TargetAll})
    if err == nil { t.Fatal("expected failure") }
    assertMissing(t, filepath.Join(agentsRoot, "kuai"))
    assertMissing(t, filepath.Join(claudeRoot, "kuai"))
}

func TestNextCommandRecoversJournal(t *testing.T) {
    manager, agentsRoot, _ := newAllManager(t)
    writeInterruptedJournal(t, manager.StateDir, agentsRoot)
    if _, err := manager.Run(context.Background(), Request{Action: ActionStatus, Target: TargetAll}); err != nil {
        t.Fatal(err)
    }
    assertNoRecoveryJournal(t, manager.StateDir)
    got, err := os.ReadFile(filepath.Join(agentsRoot, "kuai", "SKILL.md"))
    if err != nil || string(got) != "old-skill" { t.Fatalf("skill=%q err=%v", got, err) }
}

func newAllManager(t *testing.T) (*Manager, string, string) {
    t.Helper()
    home := t.TempDir()
    state := filepath.Join(t.TempDir(), "state")
    agents := filepath.Join(home, ".agents", "skills")
    claude := filepath.Join(home, ".claude", "skills")
    return &Manager{
        HomeDir: home, StateDir: state, Skill: []byte("skill-v1"),
        SkillVersion: "1.0.0", PackageVersion: "1.0.0",
        Now: func() time.Time { return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC) },
    }, agents, claude
}

func assertMissing(t *testing.T, path string) {
    t.Helper()
    if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
        t.Fatalf("path %s still exists: %v", path, err)
    }
}

func assertNoRecoveryJournal(t *testing.T, stateDir string) {
    t.Helper()
    path := filepath.Join(stateDir, "skill-transactions", "current.json")
    if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
        t.Fatalf("journal still exists: %v", err)
    }
}
```

`writeInterruptedJournal` 必须使用本任务定义的 `recoveryJournal` JSON schema，在 agents root 创建已提交目标和 backup，再把日志原子写入 `filepath.Join(manager.StateDir, "skill-transactions", "current.json")`；不得直接篡改 manager 内部状态来伪造恢复成功。

- [ ] **步骤 2：运行恢复测试并确认失败**

运行：`go test ./internal/skillmgr -run 'TestAllRollsBack|TestNextCommandRecovers' -count=1`

预期：FAIL，因为 recovery journal 尚未实现。

- [ ] **步骤 3：实现固定日志格式和恢复顺序**

```go
type recoveryJournal struct {
    SchemaVersion  int            `json:"schemaVersion"`
    TransactionID  string         `json:"transactionId"`
    PackageVersion string         `json:"packageVersion"`
    Action         Action         `json:"action"`
    Entries        []journalEntry `json:"entries"`
}

type journalEntry struct {
    Target       Target `json:"target"`
    Root         string `json:"root"`
    Stage        string `json:"stage,omitempty"`
    Backup       string `json:"backup,omitempty"`
    Phase        string `json:"phase"`
    PreIdentity  string `json:"preIdentity,omitempty"`
    PostIdentity string `json:"postIdentity,omitempty"`
}

func writeInterruptedJournal(t *testing.T, stateDir, root string) {
    t.Helper()
    target := filepath.Join(root, "kuai")
    backup := filepath.Join(root, ".kuai.backup-test")
    writeManagedTree(t, target, []byte("new-skill"), "1.1.0")
    writeManagedTree(t, backup, []byte("old-skill"), "1.0.0")
    preIdentity, err := managedTreeIdentity(backup)
    if err != nil { t.Fatal(err) }
    postIdentity, err := managedTreeIdentity(target)
    if err != nil { t.Fatal(err) }
    journal := recoveryJournal{
        SchemaVersion: 1, TransactionID: "00000000000000000000000000000001",
        PackageVersion: "1.1.0", Action: ActionUpgrade,
        Entries: []journalEntry{{Target: TargetAgents, Root: root, Backup: backup,
            Phase: "committed", PreIdentity: preIdentity, PostIdentity: postIdentity}},
    }
    data, err := json.Marshal(journal)
    if err != nil { t.Fatal(err) }
    dir := filepath.Join(stateDir, "skill-transactions")
    if err := os.MkdirAll(dir, 0o700); err != nil { t.Fatal(err) }
    temp, final := filepath.Join(dir, "current.json.tmp"), filepath.Join(dir, "current.json")
    if err := os.WriteFile(temp, data, 0o600); err != nil { t.Fatal(err) }
    if err := os.Rename(temp, final); err != nil { t.Fatal(err) }
}

func writeManagedTree(t *testing.T, dir string, skill []byte, version string) {
    t.Helper()
    if err := os.MkdirAll(dir, 0o700); err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), skill, 0o600); err != nil { t.Fatal(err) }
    marker := markerFor(skill, version)
    marker.PackageVersion = version
    data, err := json.Marshal(marker)
    if err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(dir, ".kuai-managed.json"), data, 0o600); err != nil { t.Fatal(err) }
}
```

日志路径固定由 `filepath.Join(Manager.StateDir, "skill-transactions", "current.json")` 生成。`TransactionID` 是 128-bit 随机 hex；`Phase` 只能按 `prepared → backed-up → committed → cleaned` 前进。`managedTreeIdentity` 对规范化 root 身份、`SKILL.md` SHA-256 和 marker SHA-256 的定长编码再做 SHA-256。恢复时先解析日志，再按规范化 root 字符串排序获取全部锁，在锁内逐项重算 pre/post identity：只在当前内容与日志声明的某一合法 phase 完全匹配时调用明确的 `RestoreBackup` 或清理同 package version 的残留；任何字段、路径身份或内容不匹配都返回冲突且不继续新操作。锁释放顺序相反。

持久化顺序必须固定：stage 文件和目录先 flush；写 `prepared` journal 时在 state 目录创建 no-follow 临时文件、写完整 JSON、flush 文件、原子 rename 为 `current.json`、再 flush state 目录；目标改名为 backup 后 flush root，再以同样流程持久化 `backed-up`；stage 改名为目标后 flush root，再持久化 `committed`；删除 backup/stage 后 flush root，持久化 `cleaned`，最后删除 journal 并再次 flush state 目录。POSIX 分别使用 `fsync` 文件描述符和目录描述符；Windows 对文件使用 `FlushFileBuffers`，原子替换使用带 `MOVEFILE_WRITE_THROUGH` 的 `MoveFileEx`，并用以 `FILE_FLAG_BACKUP_SEMANTICS` 打开的父目录 handle 执行可用的 flush/写穿语义。任何 flush 失败都保留 journal 并返回错误，不报告成功。

故障注入表必须在每次“journal flush、目标/backup rename、root flush、journal rename、state-dir flush”之后模拟进程中断，然后用新 Manager 实例恢复并断言旧内容或完整新版本、绝无混合树；另测 Windows `FlushFileBuffers`/`MoveFileEx` 错误与 POSIX `fsync` 错误会保留 journal。

- [ ] **步骤 4：运行故障注入和并发测试**

运行：`go test -race ./internal/skillmgr -run 'TestAll|TestNextCommand|TestConcurrent|TestRootLock|TestRecoveryFailure' -count=10`

预期：PASS，没有数据竞争；第二目标普通错误回滚第一目标；模拟崩溃能由下一条命令恢复；身份不匹配和恢复失败保持 journal 并返回冲突；同 root 锁互斥而不同 root 可并行。

- [ ] **步骤 5：提交可恢复多目标操作**

```bash
git add internal/skillmgr/transaction.go internal/skillmgr/manager.go internal/skillmgr/manager_test.go
git commit -m "feat: recover multi-target skill installs"
```

## 任务 7：接入 CLI、退出码和文档

**文件：**
- 创建：`cmd/kuai/skill_command.go`
- 创建：`cmd/kuai/skill_command_test.go`
- 修改：`cmd/kuai/main.go`
- 修改：`cmd/kuai/main_test.go`
- 修改：`internal/skillasset/kuai/SKILL.md`
- 修改：`README.md`
- 修改：`tests/test_docs.py`

- [ ] **步骤 1：写 CLI 分派失败测试**

```go
func TestSkillCommandDoesNotInitializeSessionRuntime(t *testing.T) {
    var runtimeCalls []string
    deps := commandDependencies(&runtimeCalls)
    deps.runSkill = func(context.Context, []string, io.Writer, io.Writer) int { return 3 }
    var stdout, stderr bytes.Buffer
    if code := run(context.Background(), []string{"skill", "status", "--target", "all"}, &stdout, &stderr, deps); code != 3 {
        t.Fatalf("code=%d", code)
    }
    if len(runtimeCalls) != 0 { t.Fatalf("runtime calls=%v", runtimeCalls) }
}
```

在 `skill_command_test.go` 覆盖四个 action、四个 target、`custom` 必须配 `--root`、`all` 禁止 root、未知 flag 返回 2、冲突返回 3、I/O 返回 1。

- [ ] **步骤 2：运行命令测试并确认失败**

运行：`go test ./cmd/kuai -run 'TestSkillCommand' -count=1`

预期：FAIL，`dependencies.runSkill` 未定义或 skill 被当作 start 参数处理。

- [ ] **步骤 3：实现 CLI 适配层**

在 `dependencies` 增加：

```go
runSkill func(context.Context, []string, io.Writer, io.Writer) int
```

在 `run` 的最前部识别 `args[0] == "skill"` 并立即调用 `deps.runSkill`。`productionDependencies` 用 `skillasset.Bytes()`、`skillasset.Version()` 和 CLI `version` 构造 `skillmgr.Manager`。`skill_command.go` 将 `ConflictError` 映射为 3，参数错误映射为 2，其余错误映射为 1。

- [ ] **步骤 4：更新 help、Skill 和 README**

help 必须新增：

```text
  kuai skill install --target agents|claude|all|custom [--root ABSOLUTE_PATH]
  kuai skill status --target agents|claude|all|custom [--root ABSOLUTE_PATH]
  kuai skill upgrade --target agents|claude|all|custom [--root ABSOLUTE_PATH]
  kuai skill uninstall --target agents|claude|all|custom [--root ABSOLUTE_PATH]
```

help 紧邻说明：`custom` 必须同时提供绝对 `--root`，`all` 禁止 `--root`。`main_test.go` 必须逐项断言四个 target 与两条 root 规则都出现在 help。Skill 安装检查改为优先执行 `command -v kuai && kuai version` 或 Windows `Get-Command kuai`，并记录 `kuai skill` 退出码 3 的冲突处理。README 此阶段记录命令语义，但仍保留旧二进制安装主路径，下一计划再切换 npm。

- [ ] **步骤 5：运行聚焦测试**

运行：

```bash
go test -race ./cmd/kuai ./internal/skillasset ./internal/skillmgr -count=1
python3 -m unittest tests.test_docs tests.test_single_binary_contract -v
```

预期：全部 PASS。

- [ ] **步骤 6：运行全量验证**

运行：

```bash
go test -race ./...
go vet ./...
npm test
bash tests/test_kuai_install_sh.sh
pwsh -NoProfile -File tests/Test-KuaiInstall.ps1
git diff --check
```

预期：全部 PASS。若当前系统没有 PowerShell，仅记录该命令由 Windows CI 执行，不伪造本地通过结果。

- [ ] **步骤 7：提交 CLI 与文档集成**

```bash
git add cmd/kuai/main.go cmd/kuai/main_test.go cmd/kuai/skill_command.go cmd/kuai/skill_command_test.go internal/skillasset/kuai/SKILL.md README.md tests/test_docs.py
git commit -m "feat: expose kuai skill management"
```

## 完成条件

- `kuai skill` 在扫描/UI 运行时初始化之前分派。
- canonical Skill 只有一个内容源，并被原生二进制嵌入。
- agents、claude、all、custom 的路径语义与规格一致。
- single target 具备原子替换；all 具备完整预检、锁、恢复日志和可重入恢复。
- 未管理或被修改的 Skill 不会被静默覆盖或删除。
- POSIX symlink、Windows reparse point 和父路径替换竞态测试通过。
- 全量 Go、Node、安装器和文档测试通过。
