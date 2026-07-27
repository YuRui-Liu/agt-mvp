import unittest
from pathlib import Path


TEMPLATE = Path(__file__).parents[1] / "session-selection.html.tmpl"


class SessionSelectionTemplateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.template = TEMPLATE.read_text(encoding="utf-8")

    def test_preserves_reference_visual_tokens(self):
        for token in (
            "--orange:#ff6b12",
            ".agent-card",
            ".action-bar",
            ".session-option.selected",
            "backdrop-filter:blur(16px)",
        ):
            with self.subTest(token=token):
                self.assertIn(token, self.template)

    def test_uses_real_bootstrap_data_without_demo_copy(self):
        self.assertIn(
            '<script type="application/json" id="bootstrap-data">'
            "{{BOOTSTRAP_JSON}}</script>",
            self.template,
        )
        self.assertNotIn("const MOCK", self.template)
        self.assertNotIn("校园招聘 Demo 交互开发", self.template)
        self.assertNotIn("KwAITI", self.template)
        self.assertIn("AITI", self.template)

    def test_contains_required_interaction_and_state_contracts(self):
        for token in (
            "/api/analyze",
            "X-Avscore-Token",
            "session_id",
            "report_url",
            "aria-expanded",
            "loading-state",
            "empty-state",
            "error-state",
            "prefers-reduced-motion",
            "@media(max-width:600px)",
        ):
            with self.subTest(token=token):
                self.assertIn(token, self.template)

    def test_only_known_template_placeholder_is_present(self):
        import re

        self.assertEqual(
            set(re.findall(r"{{([A-Z0-9_]+)}}", self.template)),
            {"BOOTSTRAP_JSON"},
        )


if __name__ == "__main__":
    unittest.main()
