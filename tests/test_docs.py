import re
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]
README = ROOT / "README.md"
SKILL = ROOT / "avscore.md"
LAUNCHER = ROOT / "avscore.sh"


class DocumentationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.readme = README.read_text(encoding="utf-8")
        cls.skill = SKILL.read_text(encoding="utf-8")
        cls.launcher = LAUNCHER.read_text(encoding="utf-8")

    def test_readme_states_real_project_level_scope(self):
        for phrase in (
            "选择会话",
            "分析所属项目",
            "查看画像",
            "不是单个 session",
        ):
            with self.subTest(phrase=phrase):
                self.assertIn(phrase, self.readme)

    def test_readme_documents_runtime_privacy_and_outputs(self):
        for phrase in (
            "macOS",
            "Linux",
            "Python 3",
            "curl",
            "127.0.0.1",
            "不会上传",
            "~/.agentsview/reports",
            "profile.json",
            "report.json",
            "report.html",
        ):
            with self.subTest(phrase=phrase):
                self.assertIn(phrase, self.readme)

    def test_local_mock_application_privacy_is_explicit(self):
        for document in (self.readme, self.skill):
            for phrase in (
                "本地 Mock",
                "验证码",
                "投递",
                "localStorage",
                "手机号不会离开浏览器",
            ):
                with self.subTest(phrase=phrase):
                    self.assertIn(phrase, document)

    def test_environment_variables_match_launcher(self):
        variables = (
            "AVSCORE_BINARY_PATH",
            "AVSCORE_OUTPUT_DIR",
            "AVSCORE_SKIP_SYNC",
            "AVSCORE_NO_BROWSER",
            "AVSCORE_RELEASE_URL",
            "AVSCORE_VERSION",
            "AVSCORE_SKIP_CHECKSUM",
        )
        for variable in variables:
            with self.subTest(variable=variable):
                self.assertIn(variable, self.launcher)
                self.assertIn(variable, self.readme)
                self.assertIn(variable, self.skill)

    def test_readme_binary_lookup_order_matches_launcher(self):
        function = self.launcher.split("find_agentsview_bin() {", 1)[1].split(
            "\n}", 1
        )[0]
        launcher_order = [
            function.index('if [ -n "$AVSCORE_BINARY_PATH" ]'),
            function.index('candidate="$script_dir/agentsview-${os}-${arch}"'),
            function.index("command -v agentsview"),
            function.index('"$HOME/.local/bin/agentsview"'),
            function.index('"/usr/local/bin/agentsview"'),
        ]
        self.assertEqual(launcher_order, sorted(launcher_order))
        readme_order = [
            self.readme.index("显式 `AVSCORE_BINARY_PATH`"),
            self.readme.index("脚本同目录的平台二进制"),
            self.readme.index("`PATH` 中的 `agentsview`"),
            self.readme.index("`~/.local/bin/agentsview`"),
            self.readme.index("`/usr/local/bin/agentsview`"),
        ]
        self.assertEqual(readme_order, sorted(readme_order))

    def test_release_source_contract_does_not_drift(self):
        match = re.search(
            r'^DEFAULT_RELEASE_URL="([^"]+)"$', self.launcher, re.MULTILINE
        )
        self.assertIsNotNone(match)
        default_url = match.group(1)
        for document in (self.readme, self.skill):
            urls = [
                url
                for url in re.findall(r"https?://[^\s`)>\"]+", document)
                if "127.0.0.1" not in url and "localhost" not in url
            ]
            self.assertTrue(all(url == default_url for url in urls))
            self.assertNotIn("github.com", document.lower())
            self.assertIn("AVSCORE_RELEASE_URL", document)

    def test_skill_has_strict_frontmatter_and_true_scope(self):
        frontmatter = re.match(r"\A---\n(.*?)\n---\n", self.skill, re.DOTALL)
        self.assertIsNotNone(frontmatter)
        lines = frontmatter.group(1).splitlines()
        self.assertEqual(len(lines), 3)
        self.assertEqual(lines[0], "name: avscore")
        self.assertRegex(lines[1], r"^description: \S.+$")
        self.assertRegex(
            lines[2],
            r'^version: "(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"$',
        )
        self.assertIn("所属项目", self.skill)
        self.assertIn("不是单个 session", self.skill)
        self.assertIn("unknown flag: --engine", self.skill)

    def test_skill_runs_repository_launcher_and_reports_progress(self):
        self.assertIn("bash avscore.sh", self.skill)
        self.assertIn("进度", self.skill)
        self.assertNotIn("复制以下 Python", self.skill)
        self.assertNotIn("复制以下Python", self.skill)
        self.assertNotRegex(self.skill, r"(?s)```python\b.*?```")
        self.assertNotRegex(self.skill, r"python3\s+-\s*<<")

    def test_skill_token_and_failure_rules_match_runtime(self):
        self.assertIn("不得在聊天", self.skill)
        self.assertIn("不得直接转发", self.skill)
        self.assertIn("用户明确请求", self.skill)
        self.assertIn("私密上下文", self.skill)
        self.assertNotIn("转达给用户", self.skill)
        self.assertNotIn("请用户访问该完整 URL", self.skill)
        self.assertIn("唯一允许的自动兼容降级", self.skill)
        self.assertIn("unknown flag: --engine", self.skill)
        self.assertRegex(self.skill, r"同步失败.*默认停止")
        self.assertNotRegex(self.skill, r"(缺少|没有|无)有效会话.{0,8}停止")
        self.assertIn("空状态页", self.skill)

    def test_documented_repository_files_exist(self):
        paths = (
            "avscore.sh",
            "avscore_server.py",
            "session-selection.html.tmpl",
            "avscore.html.tmpl",
            "tests/test_avscore_server.py",
            "tests/test_templates.py",
        )
        for relative_path in paths:
            with self.subTest(relative_path=relative_path):
                self.assertIn(relative_path, self.readme + self.skill)
                self.assertTrue((ROOT / relative_path).is_file())

    def test_no_common_documentation_typos(self):
        combined = self.readme + self.skill
        self.assertNotRegex(combined, r"\bREAME\b")
        self.assertNotRegex(combined, r"\bavsore\b")

    def test_documented_commands_match_launcher_entrypoint(self):
        self.assertIn("bash avscore.sh", self.readme)
        self.assertIn("bash avscore.sh", self.skill)
        self.assertIn('main "$@"', self.launcher)
        self.assertNotIn("python3 avscore_server.py", self.readme)
        self.assertNotIn("python3 avscore_server.py", self.skill)


if __name__ == "__main__":
    unittest.main()
