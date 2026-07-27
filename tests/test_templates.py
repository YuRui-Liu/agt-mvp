import unittest
from pathlib import Path
import re
import subprocess


TEMPLATE = Path(__file__).parents[1] / "session-selection.html.tmpl"
REPORT_TEMPLATE = Path(__file__).parents[1] / "avscore.html.tmpl"
REPORT_INTERACTION_TEST = Path(__file__).parent / "report_interactions.js"
REPORT_REFERENCE = Path(__file__).parent / "fixtures" / "user-profile-reference.html"
APPLICATION_TEMPLATE = Path(__file__).parents[1] / "job-application.html.tmpl"
APPLICATION_REFERENCE = Path(__file__).parent / "fixtures" / "job-application-reference.html"
APPLICATION_INTERACTION_TEST = Path(__file__).parent / "application_interactions.js"
REFERENCE = Path(__file__).parent / "fixtures" / "session-selection-reference.html"
INTERACTION_TEST = Path(__file__).parent / "session_selection_interactions.js"


def css_rules(document):
    css = re.search(r"<style>(.*?)</style>", document, re.DOTALL).group(1)
    while "@media" in css:
        start = css.index("@media")
        opening = css.index("{", start)
        depth = 1
        cursor = opening + 1
        while depth:
            depth += (css[cursor] == "{") - (css[cursor] == "}")
            cursor += 1
        css = css[:start] + css[cursor:]
    rules = {}
    for selector, declarations in re.findall(r"([^{}]+)\{([^{}]*)\}", css):
        declaration_map = {
            name.strip(): value.strip()
            for declaration in declarations.split(";")
            if ":" in declaration
            for name, value in [declaration.split(":", 1)]
        }
        normalized_selector = ",".join(part.strip() for part in selector.split(","))
        rules[normalized_selector] = declaration_map
        for part in selector.split(","):
            rules[part.strip()] = declaration_map
    return rules


def media_css_rules(document, query):
    css = re.search(r"<style>(.*?)</style>", document, re.DOTALL).group(1)
    marker = f"@media {query}"
    start = css.index(marker)
    opening = css.index("{", start)
    depth = 1
    cursor = opening + 1
    while depth:
        depth += (css[cursor] == "{") - (css[cursor] == "}")
        cursor += 1
    return css_rules("<style>" + css[opening + 1 : cursor - 1] + "</style>")


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

    def test_explains_project_scope_local_privacy_and_progress(self):
        for copy in (
            "选择一个真实会话",
            "所属项目",
            "全部可分析会话",
            "本地",
            "数据不上传",
            "正在分析项目",
            "计算 7D 画像",
        ):
            with self.subTest(copy=copy):
                self.assertIn(copy, self.template)

    def test_empty_state_lists_supported_agents_and_custom_directories(self):
        for token in (
            "Claude Code",
            "Codex",
            "Cursor",
            "Gemini",
            "Copilot",
            "OpenCode",
            "Kimi",
            "CLAUDE_PROJECTS_DIR",
            "CODEX_SESSIONS_DIR",
            "CURSOR_PROJECTS_DIR",
        ):
            with self.subTest(token=token):
                self.assertIn(token, self.template)

    def test_frontend_prefers_server_message_contract(self):
        self.assertIn("result.message", self.template)
        self.assertRegex(
            self.template,
            r"result\s*&&\s*result\.message[\s\S]*result\.error",
        )

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
            "header.disabled = state.analyzing",
            'radio.id = `session-radio-${groupIndex}-${sessionIndex}`',
            "radio.disabled = state.analyzing",
            "consent.disabled = state.analyzing",
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

    def test_bootstrap_failure_keeps_consent_disabled(self):
        catch = self.template.split("} catch (_) {", 1)[1]
        self.assertLess(catch.index("updateButton();"), catch.index("consent.disabled = true;"))


class ReportTemplateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.template = REPORT_TEMPLATE.read_text(encoding="utf-8")
        cls.reference = REPORT_REFERENCE.read_text(encoding="utf-8")

    def test_matches_reference_core_css_declarations(self):
        reference_rules = css_rules(self.reference)
        template_rules = css_rules(self.template)
        excluded = ("kwaiti-", ".poster-link", ".hero-actions button")
        selectors = [
            selector
            for selector in reference_rules
            if not any(token in selector for token in excluded)
        ]
        self.assertGreater(len(selectors), 75)
        for selector in selectors:
            with self.subTest(selector=selector):
                self.assertIn(selector, template_rules)
                for name, value in reference_rules[selector].items():
                    if selector == ".portrait-frame::before" and name == "content":
                        continue
                    self.assertEqual(template_rules[selector].get(name), value)

    def test_matches_reference_responsive_and_reduced_motion_rules(self):
        excluded = ("kwaiti-", ".poster-link", ".hero-actions button")
        for query in (
            "(max-width: 980px)",
            "(max-width: 680px)",
            "(prefers-reduced-motion: reduce)",
        ):
            reference_rules = media_css_rules(self.reference, query)
            template_rules = media_css_rules(self.template, query)
            for selector, declarations in reference_rules.items():
                if any(token in selector for token in excluded):
                    continue
                with self.subTest(query=query, selector=selector):
                    self.assertIn(selector, template_rules)
                    for name, value in declarations.items():
                        self.assertEqual(template_rules[selector].get(name), value)

    def test_preserves_reference_core_dom_hierarchy(self):
        for pattern in (
            r'<header class="site-header">[\s\S]*?<a class="brand"',
            r'<main>[\s\S]*?<section class="hero"',
            r'<section class="section" id="seven-d"[\s\S]*?<div class="radar-layout">',
            r'<div class="panel radar-panel">[\s\S]*?id="radarChart"',
            r'<aside class="panel insight-panel"[\s\S]*?id="dimensionTabs"',
            r'id="metricGrid"[\s\S]*?<div class="type-breakdown"',
            r'<section class="need-section"[\s\S]*?<div class="need-grid">',
            r'<section class="closing"[\s\S]*?<div class="closing-card">',
        ):
            with self.subTest(pattern=pattern):
                self.assertRegex(self.template, pattern)

    def test_preserves_full_visual_and_responsive_contract(self):
        for token in (
            ".site-header",
            ".hero",
            ".radar-layout",
            ".radar-panel",
            ".dimension-tabs",
            ".need-section",
            ".closing",
            "@media (max-width: 980px)",
            "@media (max-width: 680px)",
            "@media (prefers-reduced-motion: reduce)",
            'id="radarShape"',
            'id="dimensionTabs"',
            'id="metricGrid"',
        ):
            with self.subTest(token=token):
                self.assertIn(token, self.template)

    def test_uses_only_safe_report_placeholders_and_no_demo_dependencies(self):
        self.assertEqual(
            set(re.findall(r"{{([A-Z0-9_]+)}}", self.template)),
            {
                "AITI_MOCK_JS",
                "APPLICATION_URL",
                "POSTER_URL",
                "QR_URL",
                "REPORT_JSON",
                "RETURN_URL",
                "ARCHETYPE_PRIMARY",
                "ARCHETYPE_CONFIDENCE",
                "TREND_SHIFTS",
            },
        )
        for forbidden in ("KwAITI", "_kwaiti-mock.js", "fetch("):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, self.template)
        self.assertIn('id="report-data"', self.template)
        self.assertIn("JSON.parse", self.template)
        for identifier in (
            'id="archetypePrimary"',
            'id="archetypeConfidence"',
            'id="trendShifts"',
        ):
            self.assertIn(identifier, self.template)

    def test_restores_aiti_identity_dom_and_reference_css_contract(self):
        reference_rules = css_rules(self.reference)
        template_rules = css_rules(self.template)
        identity_selectors = [
            selector for selector in reference_rules
            if "kwaiti-" in selector or selector == ".poster-link"
            or selector == ".poster-link:hover"
        ]
        self.assertGreater(len(identity_selectors), 25)
        for selector in identity_selectors:
            aiti_selector = selector.replace("kwaiti-", "aiti-")
            with self.subTest(selector=aiti_selector):
                self.assertIn(aiti_selector, template_rules)
                self.assertEqual(template_rules[aiti_selector], reference_rules[selector])
        for identifier in (
            'id="aitiInlinePanel"', 'id="aitiInlineForm"',
            'id="aitiIdentityResult"', 'id="aitiPhone"', 'id="aitiCode"',
            'id="sendAitiCodeButton"', 'id="verifyAitiButton"',
            'id="aitiStatus"',
        ):
            self.assertIn(identifier, self.template)

    def test_poster_is_unintercepted_native_download(self):
        match = re.search(r'<a class="poster-link"([^>]*)>', self.template)
        self.assertIsNotNone(match)
        attributes = match.group(1)
        self.assertIn('href="{{POSTER_URL}}"', attributes)
        self.assertIn('download="AITI-专属海报.png"', attributes)
        for forbidden in ("onclick", "target=", "data-download", "preview", "lightbox"):
            self.assertNotIn(forbidden, attributes.lower())
        self.assertNotRegex(
            self.template,
            r'(poster|download)[\s\S]{0,100}\.addEventListener\(\s*["\']click',
        )
        for forbidden in ("createObjectURL", "window.open", "fetch("):
            self.assertNotIn(forbidden, self.template)

    def test_report_radar_and_selection_logic_execute_in_node(self):
        result = subprocess.run(
            ["node", "--test", str(REPORT_INTERACTION_TEST)],
            cwd=REPORT_TEMPLATE.parent,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


class ApplicationTemplateTests(unittest.TestCase):
    def test_preserves_reference_core_css_and_dom(self):
        template = APPLICATION_TEMPLATE.read_text(encoding="utf-8")
        reference = APPLICATION_REFERENCE.read_text(encoding="utf-8")
        reference_rules = css_rules(reference)
        template_rules = css_rules(template)
        for selector, declarations in reference_rules.items():
            branded = selector.replace("kwaiti", "aiti")
            self.assertIn(branded, template_rules)
            self.assertEqual(template_rules[branded], declarations)
        for identifier in (
            "recommend", "location", "resume", "basic", "education",
            "experience", "project", "skills", "submitApplication",
        ):
            self.assertIn(f'id="{identifier}"', template)

    def test_safe_local_application_contract_executes(self):
        template = APPLICATION_TEMPLATE.read_text(encoding="utf-8")
        self.assertEqual(
            set(re.findall(r"{{([A-Z0-9_]+)}}", template)),
            {"AITI_MOCK_JS", "RETURN_URL"},
        )
        self.assertNotIn("innerHTML", template)
        self.assertNotIn("fetch(", template)
        self.assertNotIn("KwAITI", template)
        result = subprocess.run(
            ["node", str(APPLICATION_INTERACTION_TEST)],
            cwd=APPLICATION_TEMPLATE.parent,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
