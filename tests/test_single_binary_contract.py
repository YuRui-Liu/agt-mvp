import pathlib
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]


class SingleBinaryContractTest(unittest.TestCase):
    def test_installers_and_skill_only_manage_kuai(self):
        for relative in ("install.sh", "install.ps1", "kuai.md"):
            text = (ROOT / relative).read_text(encoding="utf-8")
            self.assertNotIn("KUAI_AGENTSVIEW", text, relative)
            self.assertNotIn("安装 kuai 和 agentsview", text, relative)

    def test_readme_documents_the_single_binary_client(self):
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        kuai = readme.split("\n# avscore\n", 1)[0]
        for required in (
            "单一 Go 可执行文件",
            "Assessment Scope",
            "不默认选择",
            "本地脱敏",
            "HR-B",
            "CGO_ENABLED=0",
            "kuai status",
        ):
            self.assertIn(required, kuai)
        for forbidden in (
            "KUAI_AGENTSVIEW",
            "agentreview",
            "安装器通过 HTTPS 下载 `kuai` 与 `agentsview`",
            "扫描层展示 `agentsview`",
        ):
            self.assertNotIn(forbidden, kuai)

    def test_cli_does_not_find_or_execute_agentsview(self):
        text = (ROOT / "cmd/kuai/main.go").read_text(encoding="utf-8")
        self.assertNotIn('LookPath("agentsview")', text)
        self.assertNotRegex(text, r"exec\.Command[^\n]*agentsview")

    def test_release_build_is_static_and_versioned(self):
        text = (ROOT / "scripts/build-kuai-release.sh").read_text(encoding="utf-8")
        self.assertIn("CGO_ENABLED=0", text)
        self.assertIn("-X main.version=", text)
        for target in (
            "darwin/amd64",
            "darwin/arm64",
            "linux/amd64",
            "linux/arm64",
            "windows/amd64",
            "windows/arm64",
        ):
            self.assertIn(target, text)


if __name__ == "__main__":
    unittest.main()
