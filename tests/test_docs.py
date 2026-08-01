import re
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]
README = ROOT / "README.md"
SKILL = ROOT / "kuai.md"
INSTALLER = ROOT / "install.sh"
RELEASE = ROOT / "scripts" / "build-kuai-release.sh"


class DocumentationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.readme = README.read_text(encoding="utf-8")
        cls.skill = SKILL.read_text(encoding="utf-8")
        cls.installer = INSTALLER.read_text(encoding="utf-8")
        cls.release = RELEASE.read_text(encoding="utf-8")

    def test_readme_documents_current_scope_and_privacy(self):
        for phrase in (
            "Assessment Scope",
            "不默认选择",
            "本地脱敏",
            "手机号验证",
            "数据用途授权",
            "HR-B",
            "30 秒",
            "直接下载图片海报",
        ):
            with self.subTest(phrase=phrase):
                self.assertIn(phrase, self.readme)

    def test_readme_and_skill_describe_one_binary(self):
        for document in (self.readme, self.skill):
            self.assertIn("单一 Go 可执行文件", document)
            self.assertIn("kuai", document)
            self.assertNotIn("KUAI_AGENTSVIEW_PATH", document)
            self.assertNotIn("agentreview", document.lower())
            self.assertNotIn("安装 kuai 和 agentsview", document.lower())

    def test_documented_commands_exist(self):
        for command in ("kuai start", "kuai scan", "kuai status"):
            with self.subTest(command=command):
                self.assertIn(command, self.readme)
                self.assertIn(command, self.skill)
        for path in (
            "install.sh",
            "install.ps1",
            "scripts/build-kuai-release.sh",
            "kuai.md",
        ):
            self.assertTrue((ROOT / path).is_file(), path)

    def test_verified_and_unsupported_sources_are_documented(self):
        for product in (
            "claude-code",
            "codex",
            "cursor",
            "opencode",
            "vscode-copilot",
            "codeflicker",
            "myflicker",
            "openclaw",
            "hermes-agent",
            "workbuddy",
            "kimi-cli",
            "qwen-code",
            "trae",
            "trae-work",
            "kimi-work",
            "tongyi-lingma",
            "qoder",
            "qoder-work",
            "codebuddy",
        ):
            with self.subTest(product=product):
                self.assertIn(f"`{product}`", self.readme)
        self.assertIn("仅检测、不可选择", self.readme)

    def test_release_contract_is_static_and_cross_platform(self):
        self.assertIn("CGO_ENABLED=0", self.readme)
        self.assertIn("modernc.org/sqlite", self.readme)
        self.assertIn("SHA256SUMS", self.readme)
        for target in (
            "darwin/amd64",
            "darwin/arm64",
            "linux/amd64",
            "linux/arm64",
            "windows/amd64",
            "windows/arm64",
        ):
            self.assertIn(target, self.release)

    def test_installation_is_verified_and_atomic(self):
        for document in (self.readme, self.skill):
            self.assertIn("SHA-256", document)
            self.assertIn("原子", document)
            self.assertRegex(document, r"(不要使用|不要运行).{0,12}`curl[^`]*\|\s*(?:sh|bash)`")
        self.assertIn("KUAI_INSTALL_DRY_RUN", self.installer)

    def test_status_does_not_claim_remote_task_access(self):
        for document in (self.readme, self.skill):
            self.assertIn("sessionStorage", document)
            self.assertRegex(document, r"(不查询|不能读取|无法读取|无权读取)")

    def test_no_common_documentation_typos_or_legacy_entrypoints(self):
        combined = self.readme + self.skill
        self.assertNotRegex(combined, r"\bREAME\b")
        self.assertNotRegex(combined, r"\bavsore\b")
        self.assertNotIn("bash avscore.sh", combined)
        self.assertNotIn("avscore_server.py", combined)

    def test_skill_frontmatter_is_strict(self):
        frontmatter = re.match(r"\A---\n(.*?)\n---\n", self.skill, re.DOTALL)
        self.assertIsNotNone(frontmatter)
        lines = frontmatter.group(1).splitlines()
        self.assertEqual(lines[0], "name: kuai")
        self.assertRegex(lines[1], r"^description: \S.+$")
        self.assertRegex(lines[2], r'^version: "\d+\.\d+\.\d+"$')


if __name__ == "__main__":
    unittest.main()
