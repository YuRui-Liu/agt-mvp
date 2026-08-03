import pathlib
import re
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github" / "workflows" / "kuai-js-feasibility.yml"


class KuaiJavaScriptFeasibilityWorkflowTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.text = WORKFLOW.read_text(encoding="utf-8")

    def test_manual_trigger_is_present_without_blocking_every_pull_request(self):
        self.assertRegex(self.text, r"(?m)^on:\s*$")
        self.assertRegex(self.text, r"(?m)^  workflow_dispatch:\s*$")
        self.assertNotRegex(self.text, r"(?m)^  pull_request:\s*$")

    def test_matrix_contains_exact_supported_runners(self):
        match = re.search(r"(?m)^\s+os:\s*\[([^]]+)\]\s*$", self.text)
        self.assertIsNotNone(match)
        runners = {item.strip() for item in match.group(1).split(",")}
        self.assertEqual(runners, {"macos-26", "windows-2025", "ubuntu-24.04"})

    def test_permissions_are_read_only(self):
        match = re.search(r"(?ms)^permissions:\s*\n((?:  [^\n]+\n)+)", self.text)
        self.assertIsNotNone(match)
        self.assertEqual(match.group(1).strip(), "contents: read")
        self.assertNotIn("id-token: write", self.text)

    def test_node_24_runs_policy_and_security_probes(self):
        self.assertIn("actions/setup-node@v4", self.text)
        self.assertRegex(self.text, r"(?m)^\s+node-version:\s*24\s*$")
        self.assertRegex(self.text, r"(?m)^\s+run:\s*npm run verify:policy\s*$")
        self.assertRegex(self.text, r"(?m)^\s+run:\s*npm test\s*$")
        self.assertGreaterEqual(
            self.text.count("working-directory: experiments/kuai-js-feasibility"),
            2,
        )

    def test_failed_native_probe_cannot_consume_the_default_six_hour_limit(self):
        self.assertRegex(self.text, r"(?m)^    timeout-minutes:\s*[1-5]\s*$")

    def test_workflow_has_no_install_bypass_signing_or_secrets(self):
        forbidden = [
            r"\bnpm\s+(?:install|ci)\b",
            r"\bcurl\b",
            r"\bcodesign\b",
            r"\bnotarytool\b",
            r"\bsigntool\b",
            r"\bxattr\b",
            r"\bUnblock-File\b",
            r"\bsecrets\.",
            r"continue-on-error",
        ]
        for pattern in forbidden:
            with self.subTest(pattern=pattern):
                self.assertNotRegex(self.text, pattern)


if __name__ == "__main__":
    unittest.main()
