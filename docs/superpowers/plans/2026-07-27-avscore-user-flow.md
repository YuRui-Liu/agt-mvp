# avscore 用户端两步流程实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 构建与参考 HTML 样式一致的“选择真实会话 → 分析所属项目 → 查看画像”本地流程，并同步完善 README 与 avscore skill。

**架构：** `avscore.sh` 继续负责平台检测、二进制准备和同步，然后启动一个仅依赖 Python 3 标准库的 localhost 服务。服务端读取真实 session、校验选择、按 project 调用 profile，并将安全映射后的数据注入参考页面模板；浏览器只负责交互和展示。

**技术栈：** Bash 3.2+、Python 3 标准库（`http.server`、`subprocess`、`json`、`tempfile`、`unittest`）、原生 HTML/CSS/JavaScript、随附 `agentsview` CLI。

---

## 文件结构

- 创建 `avscore_server.py`：session/profile 命令适配、数据校验、报告渲染、token 保护的本地 HTTP 服务。
- 创建 `session-selection.html.tmpl`：严格继承参考选择页的视觉结构，使用服务端注入的安全 JSON。
- 修改 `avscore.html.tmpl`：以参考结果页为基线，保留原视觉与雷达交互，将静态画像改为真实占位符。
- 修改 `avscore.sh`：稳健的二进制准备、同步、本地服务启动和浏览器打开。
- 创建 `avscore.md`：与实际脚本一致的正式 skill。
- 修改 `README.md`：完整的使用、限制、隐私、配置和排障文档。
- 创建 `tests/test_avscore_server.py`：Python 单元与 HTTP 集成测试。
- 创建 `tests/test_avscore_sh.sh`：Bash 平台检测、查找优先级和失败路径测试。
- 创建 `tests/test_templates.py`：模板品牌、占位符、样式基线和无 Mock 数据检查。

### 任务 1：真实 session 数据适配

**文件：**
- 创建：`avscore_server.py`
- 创建：`tests/test_avscore_server.py`

- [ ] **步骤 1：编写 session 规范化失败测试**

```python
class SessionNormalizationTests(unittest.TestCase):
    def test_normalize_sessions_groups_real_records(self):
        payload = {"sessions": [{
            "id": "s-1", "agent": "codex", "project": "atr",
            "display_name": "更新用户页面", "message_count": 42,
            "ended_at": "2026-07-27T08:20:00Z",
        }]}
        result = normalize_sessions(payload)
        self.assertEqual(result[0]["agent"], "codex")
        self.assertEqual(result[0]["sessions"][0]["project"], "atr")

    def test_normalize_sessions_rejects_missing_id_or_project(self):
        result = normalize_sessions({"sessions": [{"agent": "codex"}]})
        self.assertEqual(result, [])

    def test_load_sessions_rejects_invalid_json(self):
        runner = FakeRunner(stdout="{broken")
        with self.assertRaises(AvscoreError):
            load_sessions(runner)
```

- [ ] **步骤 2：运行测试确认失败**

运行：`python3 -m unittest tests.test_avscore_server.SessionNormalizationTests -v`

预期：FAIL，提示无法导入 `avscore_server` 或函数不存在。

- [ ] **步骤 3：实现命令运行器和 session 规范化**

```python
class AvscoreError(RuntimeError):
    pass

class CommandRunner:
    def __init__(self, binary, timeout=180):
        self.binary = binary
        self.timeout = timeout

    def run(self, args):
        return subprocess.run(
            [self.binary, *args], capture_output=True, text=True,
            timeout=self.timeout, check=False,
        )

def load_sessions(runner):
    result = runner.run(["session", "list", "--format", "json", "--limit", "500"])
    if result.returncode != 0:
        raise AvscoreError(safe_error(result.stderr, "无法读取会话"))
    try:
        return normalize_sessions(json.loads(result.stdout))
    except json.JSONDecodeError as exc:
        raise AvscoreError("agentsview 返回了无效的 session JSON") from exc
```

`normalize_sessions` 只保留非空 `id` 与 `project` 的记录；标题依次回退到 `display_name`、`first_message`、`未命名会话`，按 agent 分组并按结束时间倒序排列，同时计算每个 project 的 session 数。

- [ ] **步骤 4：运行 session 测试确认通过**

运行：`python3 -m unittest tests.test_avscore_server.SessionNormalizationTests -v`

预期：3 个测试 PASS。

- [ ] **步骤 5：提交**

```bash
git add avscore_server.py tests/test_avscore_server.py
git commit -m "feat: load real agent sessions"
```

### 任务 2：安全的项目级画像执行与报告数据

**文件：**
- 修改：`avscore_server.py`
- 修改：`tests/test_avscore_server.py`

- [ ] **步骤 1：编写 profile 降级、安全参数和并发测试**

```python
class ProfileRunnerTests(unittest.TestCase):
    def test_profile_passes_project_as_one_argument(self):
        runner = RecordingRunner([ok_profile()])
        run_profile(runner, "name; rm -rf nope")
        self.assertEqual(runner.calls[0][-2:], ["--project", "name; rm -rf nope"])

    def test_profile_falls_back_only_for_unknown_engine_flag(self):
        runner = RecordingRunner([
            result(1, "", "unknown flag: --engine"),
            result(0, json.dumps(ok_profile()), ""),
        ])
        profile, degraded = run_profile(runner, "atr")
        self.assertTrue(degraded)
        self.assertNotIn("--engine", runner.calls[1])

    def test_profile_does_not_fallback_for_analysis_failure(self):
        runner = RecordingRunner([result(1, "", "database is locked")])
        with self.assertRaises(AvscoreError):
            run_profile(runner, "atr")
        self.assertEqual(len(runner.calls), 1)
```

并加入 `AnalysisCoordinatorTests`，验证第二个并发请求得到 `AnalysisBusyError`，首个任务完成后锁会释放。

- [ ] **步骤 2：运行测试确认失败**

运行：`python3 -m unittest tests.test_avscore_server.ProfileRunnerTests tests.test_avscore_server.AnalysisCoordinatorTests -v`

预期：FAIL，提示 `run_profile` 或 `AnalysisCoordinator` 不存在。

- [ ] **步骤 3：实现 profile 执行、严格数据映射和原子写入**

```python
def run_profile(runner, project):
    args = ["profile", "--json", "--engine", "statistical", "--project", project]
    result = runner.run(args)
    degraded = False
    if result.returncode and is_unknown_engine_error(result.stderr):
        result = runner.run(["profile", "--json", "--project", project])
        degraded = True
    if result.returncode:
        raise AvscoreError(safe_error(result.stderr, "画像分析失败"))
    return parse_profile(result.stdout), degraded

def atomic_write(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(
        "w", encoding="utf-8", dir=path.parent, delete=False
    ) as handle:
        handle.write(content)
        temp_name = handle.name
    os.replace(temp_name, path)
```

`parse_profile` 要求七个核心分数均为 0–100 的有限数值；`build_report_model` 提供项目名、session 数、生成时间、引擎、原型、趋势和维度详情的安全默认值；`AnalysisCoordinator` 用非阻塞锁保证单任务执行。

- [ ] **步骤 4：运行画像与原子写入测试**

运行：`python3 -m unittest tests.test_avscore_server.ProfileRunnerTests tests.test_avscore_server.AnalysisCoordinatorTests tests.test_avscore_server.AtomicWriteTests -v`

预期：全部 PASS。

- [ ] **步骤 5：提交**

```bash
git add avscore_server.py tests/test_avscore_server.py
git commit -m "feat: analyze selected session project safely"
```

### 任务 3：与参考 HTML 一致的选择页

**文件：**
- 创建：`session-selection.html.tmpl`
- 修改：`avscore_server.py`
- 创建：`tests/test_templates.py`
- 修改：`tests/test_avscore_server.py`

- [ ] **步骤 1：编写选择页视觉基线与转义测试**

```python
class SelectionTemplateTests(unittest.TestCase):
    def test_selection_template_keeps_reference_tokens(self):
        html = TEMPLATE.read_text()
        for token in [
            "--orange:#ff6b12", ".agent-card", ".action-bar",
            ".session-option.selected", "backdrop-filter:blur(16px)",
        ]:
            self.assertIn(token, html)

    def test_selection_page_has_no_mock_sessions(self):
        html = TEMPLATE.read_text()
        self.assertNotIn("校园招聘 Demo 交互开发", html)
        self.assertNotIn("const MOCK", html)

    def test_selection_bootstrap_is_json_safe(self):
        html = render_selection([session_named("</script><img onerror=alert(1)>")], "token")
        self.assertNotIn("</script><img", html)
```

- [ ] **步骤 2：运行模板测试确认失败**

运行：`python3 -m unittest tests.test_templates.SelectionTemplateTests -v`

预期：FAIL，因为模板不存在。

- [ ] **步骤 3：从参考选择页创建真实数据模板**

复制参考页的 CSS、DOM 层级和组件类名，只做以下替换：

```html
<script type="application/json" id="bootstrap-data">{{BOOTSTRAP_JSON}}</script>
<script>
const bootstrap = JSON.parse(document.getElementById("bootstrap-data").textContent);
const groups = bootstrap.groups;
let selectedSession = null;
</script>
```

渲染时使用 DOM API 或 `escapeHtml` 处理用户数据；选择后显示 `project` 与 `projectSessionCount`；分析按钮调用带 `X-Avscore-Token` 的 `POST /api/analyze`；加入 loading、empty、error 与 retry 状态；保留参考页的颜色、字号、圆角、阴影和主布局，并补上同视觉体系的移动端规则与焦点样式。

- [ ] **步骤 4：运行选择页与渲染测试**

运行：`python3 -m unittest tests.test_templates.SelectionTemplateTests tests.test_avscore_server.SelectionRenderTests -v`

预期：全部 PASS。

- [ ] **步骤 5：提交**

```bash
git add session-selection.html.tmpl avscore_server.py tests/test_templates.py tests/test_avscore_server.py
git commit -m "feat: add real session selection page"
```

### 任务 4：与参考 HTML 一致的结果页

**文件：**
- 修改：`avscore.html.tmpl`
- 修改：`avscore_server.py`
- 修改：`tests/test_templates.py`
- 修改：`tests/test_avscore_server.py`

- [ ] **步骤 1：编写结果页视觉基线和占位符测试**

```python
class ProfileTemplateTests(unittest.TestCase):
    def test_profile_template_keeps_reference_structure(self):
        html = PROFILE_TEMPLATE.read_text()
        for token in [
            ".site-header", ".hero", ".radar-layout", ".dimension-tabs",
            "@media (max-width: 680px)", "@media (prefers-reduced-motion: reduce)",
        ]:
            self.assertIn(token, html)

    def test_rendered_report_has_no_unresolved_placeholders(self):
        report = render_report(valid_report_model())
        self.assertNotRegex(report, r"\{\{[A-Z0-9_]+\}\}")
        self.assertNotIn("KwAITI", report)
```

- [ ] **步骤 2：运行测试确认失败**

运行：`python3 -m unittest tests.test_templates.ProfileTemplateTests -v`

预期：FAIL，因为现有模板仍含静态内容或缺少新数据占位符。

- [ ] **步骤 3：将参考结果页完整视觉绑定到真实模型**

保留参考页全部核心 CSS、DOM 区块和雷达交互；将类型、标题、摘要、标签、七维分数、维度描述、趋势和元信息改为安全占位符。删除 `_kwaiti-mock.js`、手机号验证码和海报 Mock 逻辑；品牌改为 AITI；加入：

```html
<div class="header-note">{{PROJECT_NAME}} · {{SESSION_COUNT}} 个会话</div>
<a class="text-link" href="/?token={{TOKEN}}">重新选择会话</a>
```

报告落盘版本使用相对或内联数据，不依赖仍在运行的服务。

- [ ] **步骤 4：运行结果渲染测试**

运行：`python3 -m unittest tests.test_templates.ProfileTemplateTests tests.test_avscore_server.ReportRenderTests -v`

预期：全部 PASS。

- [ ] **步骤 5：提交**

```bash
git add avscore.html.tmpl avscore_server.py tests/test_templates.py tests/test_avscore_server.py
git commit -m "feat: render project profile from real scores"
```

### 任务 5：token 保护的 localhost HTTP 流程

**文件：**
- 修改：`avscore_server.py`
- 修改：`tests/test_avscore_server.py`

- [ ] **步骤 1：编写 HTTP 鉴权、请求校验与端到端测试**

```python
class HttpFlowTests(unittest.TestCase):
    def test_missing_token_is_forbidden(self):
        response = self.request("GET", "/")
        self.assertEqual(response.status, 403)

    def test_unknown_session_is_rejected(self):
        response = self.request(
            "POST", "/api/analyze",
            token=self.token, json={"session_id": "not-real"},
        )
        self.assertEqual(response.status, 400)

    def test_valid_session_returns_report_url(self):
        response = self.request(
            "POST", "/api/analyze",
            token=self.token, json={"session_id": "s-1"},
        )
        self.assertEqual(response.status, 200)
        self.assertEqual(response.json()["report_url"], "/report?token=" + self.token)
```

- [ ] **步骤 2：运行 HTTP 测试确认失败**

运行：`python3 -m unittest tests.test_avscore_server.HttpFlowTests -v`

预期：FAIL，因为 HTTP handler 尚不存在。

- [ ] **步骤 3：实现服务路由和安全响应**

实现 `GET /`、`POST /api/analyze`、`GET /report`、`GET /api/health`；查询参数或 `X-Avscore-Token` 必须与 `secrets.token_urlsafe(32)` 生成值恒定时间比较；请求体上限 16 KiB；仅接受 JSON；所有未知路由返回 JSON 404；服务固定绑定 `127.0.0.1` 和端口 `0`，启动后将完整 URL 输出为单行 JSON。

- [ ] **步骤 4：运行 HTTP 测试确认通过**

运行：`python3 -m unittest tests.test_avscore_server.HttpFlowTests -v`

预期：全部 PASS。

- [ ] **步骤 5：提交**

```bash
git add avscore_server.py tests/test_avscore_server.py
git commit -m "feat: serve token-protected local profile flow"
```

### 任务 6：加固启动器

**文件：**
- 修改：`avscore.sh`
- 创建：`tests/test_avscore_sh.sh`

- [ ] **步骤 1：编写 Bash 行为测试**

测试脚本通过临时 `PATH` 和 stub 命令覆盖：

```bash
test_detect_arch_arm64() {
  assert_eq "arm64" "$(AVSCORE_TEST_UNAME_M=arm64 detect_arch)"
}

test_browser_failure_prints_url() {
  output="$(OPEN_SHOULD_FAIL=1 run_launcher_with_stubs 2>&1)"
  assert_contains "$output" "请在浏览器打开：http://127.0.0.1:"
}

test_sync_failure_stops_by_default() {
  if run_launcher_with_failing_sync; then
    fail "sync failure must stop"
  fi
}
```

- [ ] **步骤 2：运行 Bash 测试确认失败**

运行：`bash tests/test_avscore_sh.sh`

预期：FAIL，现有脚本不支持测试注入且仍直接生成报告。

- [ ] **步骤 3：重构脚本并连接本地服务**

保留 Bash 3.2 兼容；支持 `AVSCORE_BINARY_PATH`、`AVSCORE_OUTPUT_DIR`、`AVSCORE_SKIP_SYNC=1`、`AVSCORE_NO_BROWSER=1`；下载时使用临时文件并在成功校验后原子安装；检查 `python3`；sync 失败默认停止；运行：

```bash
python3 "$script_dir/avscore_server.py" \
  --binary "$bin" \
  --selection-template "$script_dir/session-selection.html.tmpl" \
  --profile-template "$script_dir/avscore.html.tmpl" \
  --output-dir "$OUTPUT_DIR"
```

解析服务打印的启动 JSON 后打开浏览器；打开失败打印完整 URL；信号退出时终止子进程；不删除报告。

- [ ] **步骤 4：运行 shell 检查和 Bash 测试**

运行：`bash -n avscore.sh && bash tests/test_avscore_sh.sh`

预期：语法检查成功，全部 Bash 测试 PASS。

- [ ] **步骤 5：提交**

```bash
git add avscore.sh tests/test_avscore_sh.sh
git commit -m "feat: launch robust local avscore flow"
```

### 任务 7：README 与正式 skill

**文件：**
- 修改：`README.md`
- 创建：`avscore.md`
- 修改：`tests/test_templates.py`

- [ ] **步骤 1：编写文档一致性测试**

```python
class DocumentationTests(unittest.TestCase):
    def test_readme_documents_project_scope_and_privacy(self):
        text = README.read_text()
        self.assertIn("所属项目", text)
        self.assertIn("127.0.0.1", text)
        self.assertIn("AVSCORE_SKIP_SYNC", text)

    def test_skill_references_existing_files_and_real_commands(self):
        text = SKILL.read_text()
        for path in ["avscore.sh", "avscore_server.py",
                     "session-selection.html.tmpl", "avscore.html.tmpl"]:
            self.assertIn(path, text)
            self.assertTrue((ROOT / path).exists())
        self.assertNotIn("复制以下 Python", text)
```

- [ ] **步骤 2：运行文档测试确认失败**

运行：`python3 -m unittest tests.test_templates.DocumentationTests -v`

预期：FAIL，README 过于简略且 `avscore.md` 不存在。

- [ ] **步骤 3：重写 README 并创建 skill**

README 按“项目定位、真实流程、快速开始、系统要求、支持 Agent、画像范围、文件结构、输出目录、环境变量、隐私、安全、故障排查、skill 安装、开发验证”组织。skill 使用 YAML frontmatter，触发后优先运行仓库随附 `avscore.sh`，明确项目级范围、允许的降级、停止条件和用户可见进度，不内嵌容易漂移的大段实现代码。

- [ ] **步骤 4：运行文档一致性测试**

运行：`python3 -m unittest tests.test_templates.DocumentationTests -v`

预期：全部 PASS。

- [ ] **步骤 5：提交**

```bash
git add README.md avscore.md tests/test_templates.py
git commit -m "docs: complete avscore usage and skill"
```

### 任务 8：端到端与视觉验证

**文件：**
- 修改：仅修复本任务验证中发现的问题

- [ ] **步骤 1：运行完整自动化测试**

运行：

```bash
python3 -m unittest discover -s tests -v
bash tests/test_avscore_sh.sh
bash -n avscore.sh
git diff --check
```

预期：所有测试 PASS，shell 无语法错误，diff 无空白错误。

- [ ] **步骤 2：使用真实二进制启动无浏览器流程**

运行：

```bash
AVSCORE_BINARY_PATH=./agentsview-darwin-arm64 \
AVSCORE_SKIP_SYNC=1 \
AVSCORE_NO_BROWSER=1 \
bash avscore.sh
```

预期：终端打印 `http://127.0.0.1:<port>/?token=<随机值>`；选择页返回 200；真实 session 按 Agent 分组。

- [ ] **步骤 3：桌面视口视觉检查**

在 1440×1000 视口分别截图参考选择页、实现选择页、参考结果页和实现结果页。逐项核对颜色、字号、间距、圆角、阴影、主布局、折叠卡片、底部栏、Hero、雷达图和维度卡片；发现差异时只修改数据绑定相关最小范围，保持参考 CSS。

- [ ] **步骤 4：移动视口与交互检查**

在 390×844 视口验证无横向滚动、操作区不遮挡、session 标题可截断、雷达图适配；验证展开/收起、选择、隐私勾选、重复点击防护、失败重试和重新选择；验证 `prefers-reduced-motion`。

- [ ] **步骤 5：占位符和品牌扫描**

运行：

```bash
rg -n '\{\{[A-Z0-9_]+\}\}|const MOCK|校园招聘 Demo|KwAITI' \
  README.md avscore.md session-selection.html.tmpl avscore.html.tmpl
```

预期：模板只保留渲染器定义的占位符；无 Mock session 和 KwAITI 遗留。生成的 `report.html` 中不含任何 `{{...}}`。

- [ ] **步骤 6：最终提交**

```bash
git add README.md avscore.md avscore.sh avscore_server.py \
  session-selection.html.tmpl avscore.html.tmpl tests
git commit -m "test: verify avscore user flow"
```

