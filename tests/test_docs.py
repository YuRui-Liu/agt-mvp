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
            "build.sh",
            "scripts/build-kuai-release.sh",
            "scripts/assemble-kuai-checksums.sh",
            "kuai.md",
        ):
            self.assertTrue((ROOT / path).is_file(), path)

    def test_verified_and_unsupported_sources_are_documented(self):
        ready = {
            "aider",
            "claude-code",
            "cline",
            "codebuddy-cli",
            "codeflicker",
            "codex",
            "copilot-cli",
            "cursor",
            "gemini-cli",
            "hermes-agent",
            "kimi-cli",
            "kimi-code",
            "myflicker",
            "openclaw",
            "opencode",
            "qoder-cli",
            "qoder-ide",
            "qwen-code",
            "tongyi-lingma-cli",
            "tongyi-lingma-ide",
            "vscode-copilot",
            "workbuddy",
        }
        unsupported = {
            "trae",
            "trae-work",
            "kimi-work",
            "qoder-work",
            "codebuddy-ide",
            "kiro",
        }
        self.assertEqual(len(ready), 22)
        self.assertEqual(len(unsupported), 6)
        readme_scope = self.readme.split("## Assessment Scope 与支持范围", 1)[1].split("## 隐私与 HR-B 边界", 1)[0]
        readme_ready, readme_unsupported = readme_scope.split("仅检测、不可选择", 1)
        row_products = lambda text: set(re.findall(r"^\| `([^`]+)` / [^|]+ \|", text, flags=re.MULTILINE))
        self.assertEqual(row_products(readme_ready), ready)
        self.assertEqual(row_products(readme_unsupported), unsupported)

        skill_sources = self.skill.split("### 支持来源", 1)[1].split("## 5. 脱敏预览与授权", 1)[0]
        ready_sentence = skill_sources.split("。", 1)[0]
        self.assertEqual(set(re.findall(r"`([^`]+)`", ready_sentence)), ready)
        self.assertEqual(
            set(re.findall(r"^\| `([^`]+)` \|", skill_sources, flags=re.MULTILINE)),
            unsupported,
        )

        for document_name, document in (("README", self.readme), ("skill", self.skill)):
            self.assertIn("`ready`", document)
            self.assertIn("不等于本机可选择", document)
            self.assertIn("仅检测、不可选择", document)
            for obsolete in ("tongyi-lingma", "qoder", "codebuddy"):
                with self.subTest(document=document_name, obsolete=obsolete):
                    self.assertNotIn(f"`{obsolete}`", document)

    def test_source_matrix_uses_catalog_verification_and_reason_codes(self):
        for document in (self.readme, self.skill):
            for value in (
                "machine_verified",
                "fixture_verified",
                "export_required",
                "unsupported",
                "official_export_required",
                "no_distinct_local_format",
                "no_verified_session_schema",
                "no_verified_transcript_body",
            ):
                with self.subTest(value=value):
                    self.assertIn(f"`{value}`", document)
        for product in ("copilot-cli", "gemini-cli"):
            self.assertRegex(
                self.readme,
                rf"(?m)^\| `{re.escape(product)}` / .* \| `fixture_verified` \|$",
            )
        reasons = {
            "trae": ("export_required", "official_export_required"),
            "trae-work": ("unsupported", "no_distinct_local_format"),
            "kimi-work": ("unsupported", "no_verified_session_schema"),
            "qoder-work": ("unsupported", "no_distinct_local_format"),
            "codebuddy-ide": ("unsupported", "no_verified_transcript_body"),
            "kiro": ("unsupported", "no_verified_session_schema"),
        }
        for product, (verification, reason) in reasons.items():
            for document_name, document in (("README", self.readme), ("skill", self.skill)):
                with self.subTest(document=document_name, product=product):
                    self.assertRegex(
                        document,
                        rf"(?m)^\| `{re.escape(product)}`(?: / [^|]+)? \| `{verification}` \| `{reason}` \|$",
                    )

    def test_source_matrix_keeps_distinct_products_and_read_only_root_semantics(self):
        for document in (self.readme, self.skill):
            self.assertIn("`copilot-cli` 不等于 `vscode-copilot`", document)
            self.assertIn("`kimi-cli` 不等于 `kimi-code`", document)
            self.assertIn("ready 来源", document)
            self.assertIn("只读适配器扫描", document)
            self.assertIn("unsupported 来源", document)
            self.assertIn("目录", document)
            self.assertIn("存在性检测", document)
            self.assertNotIn("只做浅层存在性检查", document)

    def test_skill_uses_portable_mktemp_templates(self):
        self.assertIn("mktemp -t kuai-start.XXXXXX", self.skill)
        self.assertIn("mktemp -t kuai-pid.XXXXXX", self.skill)

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

    def test_kci_release_docs_require_explicit_secure_inputs(self):
        for phrase in (
            "UPLOAD_PACKAGE_VERSION",
            "APPLE_NOTARY_PROFILE",
            "WINDOWS_SIGNING_PUBLISHER",
            "scripts/assemble-kuai-checksums.sh",
            "SIGN 与 NOTARIZE 只接受 true 或 false",
            "pair 目录",
            "重新计算六个产物的 SHA-256",
            "只接受 `Accepted`",
            "不可变 pair 目录",
            "精确 12 个条目",
            "硬链接",
            "Python 3",
            "O_EXCL",
        ):
            self.assertIn(phrase, self.readme)
        self.assertNotIn("APPLE_APP_SPECIFIC_PASSWORD", self.readme)
        self.assertNotIn("cat dist/*.sha256", self.readme)

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
