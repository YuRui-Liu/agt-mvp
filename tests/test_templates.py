import unittest
from pathlib import Path
import re
import subprocess


TEMPLATE = Path(__file__).parents[1] / "session-selection.html.tmpl"
REFERENCE = Path(__file__).parent / "fixtures" / "session-selection-reference.html"
INTERACTION_TEST = Path(__file__).parent / "session_selection_interactions.js"


def css_rules(document):
    css = re.search(r"<style>(.*?)</style>", document, re.DOTALL).group(1)
    css = css.split("@media", 1)[0]
    return {
        selector.strip(): {
            name.strip(): value.strip()
            for declaration in declarations.split(";")
            if ":" in declaration
            for name, value in [declaration.split(":", 1)]
        }
        for selector, declarations in re.findall(r"([^{}]+)\{([^{}]*)\}", css)
    }


class SessionSelectionTemplateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.template = TEMPLATE.read_text(encoding="utf-8")
        cls.reference = REFERENCE.read_text(encoding="utf-8")

    def test_preserves_reference_core_css_rules_and_declarations(self):
        reference_rules = css_rules(self.reference)
        template_rules = css_rules(self.template)
        selectors = (
            ":root",
            "body",
            ".header",
            ".brand",
            ".brand-mark",
            ".brand-text strong",
            ".brand-text span",
            ".main",
            ".hero-text h1",
            ".hero-text h1 span",
            ".hero-text p",
            ".section-label",
            ".section-label::after",
            "#sessionList",
            ".agent-card",
            ".agent-card:hover,.agent-card.expanded",
            ".agent-header",
            ".agent-header:hover",
            ".agent-chevron",
            ".agent-card.expanded .agent-chevron",
            ".s-icon",
            ".s-info",
            ".s-info strong",
            ".s-info span",
            ".s-meta",
            ".s-meta strong",
            ".agent-sessions",
            ".agent-card.expanded .agent-sessions",
            ".session-option",
            ".session-option:hover",
            ".session-option.selected",
            ".session-option input",
            ".radio-mark",
            ".session-option.selected .radio-mark",
            ".session-option.selected .radio-mark::after",
            ".session-copy",
            ".session-copy strong",
            ".session-copy span",
            ".session-meta",
            ".session-meta strong",
            ".action-bar",
            ".selection-summary",
            ".selection-summary strong",
            ".right-bar",
            ".privacy",
            ".privacy input",
            ".analyze-btn",
            ".analyze-btn:hover:not(:disabled)",
            ".analyze-btn:disabled",
        )

        for selector in selectors:
            with self.subTest(selector=selector):
                self.assertIn(selector, template_rules)
                for property_name, expected in reference_rules[selector].items():
                    self.assertEqual(
                        template_rules[selector].get(property_name),
                        expected,
                        f"{selector} changed {property_name}",
                    )

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
            "ResizeObserver",
            "--action-bar-height",
            "padding-bottom:calc(",
        ):
            with self.subTest(token=token):
                self.assertIn(token, self.template)

    def test_interaction_state_flows_execute_in_node(self):
        try:
            result = subprocess.run(
                ["node", "--test", str(INTERACTION_TEST)],
                cwd=TEMPLATE.parent,
                capture_output=True,
                text=True,
                check=False,
            )
        except FileNotFoundError:
            self.fail("Node.js is required for session selection visual acceptance")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_focus_targets_are_index_based_and_restored_after_render(self):
        for token in (
            'header.id = `agent-header-${groupIndex}`',
            'radio.id = `session-radio-${groupIndex}-${sessionIndex}`',
            "restoreFocus()",
        ):
            with self.subTest(token=token):
                self.assertIn(token, self.template)

    def test_session_row_secondary_text_combines_time_and_project(self):
        self.assertRegex(
            self.template,
            r"time\.textContent\s*=\s*`\$\{displayTime\(session\.ended_at\)\}"
            r"\s*·\s*\$\{text\(session\.project\)\}`",
        )

    def test_only_known_template_placeholder_is_present(self):
        self.assertEqual(
            set(re.findall(r"{{([A-Z0-9_]+)}}", self.template)),
            {"BOOTSTRAP_JSON"},
        )


if __name__ == "__main__":
    unittest.main()
