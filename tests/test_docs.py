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

    def test_skill_has_valid_frontmatter_and_true_scope(self):
        frontmatter = re.match(r"\A---\n(.*?)\n---\n", self.skill, re.DOTALL)
        self.assertIsNotNone(frontmatter)
        metadata = frontmatter.group(1)
        self.assertRegex(metadata, r"(?m)^name:\s*avscore\s*$")
        self.assertRegex(metadata, r"(?m)^description:\s*.+$")
        self.assertRegex(metadata, r'(?m)^version:\s*["\']?[0-9]+\.[0-9]+\.[0-9]+')
        self.assertIn("所属项目", self.skill)
        self.assertIn("不是单个 session", self.skill)
        self.assertIn("unknown flag: --engine", self.skill)

    def test_skill_runs_repository_launcher_and_reports_progress(self):
        self.assertIn("bash avscore.sh", self.skill)
        self.assertIn("进度", self.skill)
        self.assertNotIn("复制以下 Python", self.skill)
        self.assertNotIn("复制以下Python", self.skill)

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
