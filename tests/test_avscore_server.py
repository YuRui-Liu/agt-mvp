import json
import re
import subprocess
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

from avscore_server import (
    AnalysisBusyError,
    AnalysisCoordinator,
    AvscoreError,
    CommandRunner,
    atomic_write,
    build_report_model,
    load_sessions,
    normalize_sessions,
    parse_profile,
    render_report,
    render_selection,
    run_profile,
    safe_error,
)


class FakeRunner:
    def __init__(self, result):
        self.result = result
        self.args = None

    def run(self, args):
        self.args = args
        return self.result


DIMENSIONS = (
    "steering",
    "execution",
    "engineering",
    "planning",
    "product",
    "autonomy",
    "adaptation",
)


def profile_payload(score=50):
    return {
        "generated_at": "2026-07-27T09:00:00Z",
        "profile": {key: {"score": score} for key in DIMENSIONS},
        "archetype": {"primary": "系统设计者", "confidence": 0.8},
        "evolution": {"trend_prediction": "保持稳健", "key_shifts": []},
    }


class RecordingRunner:
    def __init__(self, results):
        self.results = iter(results)
        self.calls = []

    def run(self, args):
        self.calls.append(args)
        return next(self.results)


class SessionNormalizationTests(unittest.TestCase):
    def test_normalizes_sessions_grouped_by_agent(self):
        payload = {
            "sessions": [
                {
                    "id": "s-1",
                    "agent": "codex",
                    "project": "atr",
                    "display_name": "更新用户页面",
                    "message_count": 42,
                    "ended_at": "2026-07-27T08:20:00Z",
                }
            ]
        }

        groups = normalize_sessions(payload)

        self.assertEqual(groups[0]["agent"], "codex")
        self.assertEqual(groups[0]["sessions"][0]["project"], "atr")
        self.assertEqual(groups[0]["sessions"][0]["title"], "更新用户页面")
        self.assertEqual(groups[0]["sessions"][0]["messageCount"], 42)
        self.assertEqual(groups[0]["sessions"][0]["projectSessionCount"], 1)

    def test_drops_records_without_id_or_project(self):
        payload = {
            "sessions": [
                {"project": "atr", "agent": "codex"},
                {"id": "s-1", "agent": "codex"},
            ]
        }

        self.assertEqual(normalize_sessions(payload), [])

    def test_deduplicates_session_ids_globally_and_keeps_first_record(self):
        payload = {
            "sessions": [
                {
                    "id": "duplicate",
                    "agent": "codex",
                    "project": "first-project",
                    "display_name": "保留我",
                },
                {
                    "id": "duplicate",
                    "agent": "claude",
                    "project": "second-project",
                    "display_name": "丢弃我",
                },
                {
                    "id": "unique",
                    "agent": "claude",
                    "project": "second-project",
                },
            ]
        }

        groups = normalize_sessions(payload)
        sessions = [
            session for group in groups for session in group["sessions"]
        ]

        self.assertEqual([session["id"] for session in sessions], ["duplicate", "unique"])
        self.assertEqual(sessions[0]["title"], "保留我")
        self.assertEqual(sessions[0]["projectSessionCount"], 1)
        self.assertEqual(sessions[1]["projectSessionCount"], 1)

    def test_load_sessions_rejects_invalid_json(self):
        runner = FakeRunner(
            subprocess.CompletedProcess(args=[], returncode=0, stdout="{", stderr="")
        )

        with self.assertRaisesRegex(
            AvscoreError, "agentsview 返回了无效的 session JSON"
        ):
            load_sessions(runner)

        self.assertEqual(
            runner.args, ["session", "list", "--format", "json", "--limit", "500"]
        )

    def test_normalization_falls_back_title_sorts_and_counts_project_sessions(self):
        payload = {
            "sessions": [
                {
                    "id": "older",
                    "agent": "codex",
                    "project": "atr",
                    "first_message": "第一条消息",
                    "ended_at": "2026-07-26T08:20:00Z",
                },
                {
                    "id": "newer",
                    "agent": "codex",
                    "project": "atr",
                    "display_name": "",
                    "first_message": "",
                    "ended_at": "2026-07-27T08:20:00Z",
                },
            ]
        }

        groups = normalize_sessions(payload)

        self.assertEqual(
            [session["id"] for session in groups[0]["sessions"]], ["newer", "older"]
        )
        self.assertEqual(groups[0]["sessions"][0]["title"], "未命名会话")
        self.assertEqual(groups[0]["sessions"][1]["title"], "第一条消息")
        self.assertEqual(groups[0]["sessions"][0]["projectSessionCount"], 2)
        self.assertEqual(groups[0]["sessions"][1]["projectSessionCount"], 2)

    def test_nonzero_command_uses_redacted_bounded_error(self):
        runner = FakeRunner(
            subprocess.CompletedProcess(
                args=[],
                returncode=1,
                stdout="private session body",
                stderr="token=secret " + ("x" * 1000),
            )
        )

        with self.assertRaises(AvscoreError) as raised:
            load_sessions(runner)

        message = str(raised.exception)
        self.assertNotIn("private session body", message)
        self.assertNotIn("secret", message)
        self.assertLessEqual(len(message), 240)

    def test_rejects_non_list_sessions_schema(self):
        for sessions in (None, {}, "private"):
            with self.subTest(sessions=sessions):
                with self.assertRaisesRegex(
                    AvscoreError, "agentsview 返回了无效的 session JSON"
                ):
                    normalize_sessions({"sessions": sessions})

    def test_drops_non_mapping_and_non_string_identity_fields(self):
        payload = {
            "sessions": [
                "not a record",
                {"id": ["unhashable"], "project": "atr", "agent": "codex"},
                {"id": "s-1", "project": ["unhashable"], "agent": "codex"},
                {"id": "s-2", "project": "atr", "agent": {"not": "a string"}},
                {"id": "s-3", "project": "atr", "agent": "codex"},
            ]
        }

        groups = normalize_sessions(payload)

        by_agent = {group["agent"]: group["sessions"] for group in groups}
        self.assertEqual(set(by_agent), {"codex", "unknown"})
        self.assertEqual([item["id"] for item in by_agent["codex"]], ["s-3"])
        self.assertEqual([item["id"] for item in by_agent["unknown"]], ["s-2"])

    def test_mixed_ended_at_types_are_normalized_before_sorting(self):
        payload = {
            "sessions": [
                {
                    "id": "invalid-date",
                    "project": "atr",
                    "agent": "codex",
                    "ended_at": 123,
                },
                {
                    "id": "dated",
                    "project": "atr",
                    "agent": "codex",
                    "ended_at": "2026-07-27T08:20:00Z",
                },
            ]
        }

        sessions = normalize_sessions(payload)[0]["sessions"]

        self.assertEqual([item["id"] for item in sessions], ["dated", "invalid-date"])
        self.assertEqual(sessions[1]["ended_at"], "")

    def test_load_sessions_converts_runner_failures_to_safe_error(self):
        sensitive = "Authorization: Bearer secret"
        failures = (
            subprocess.TimeoutExpired(["agentsview", sensitive], 180),
            OSError(sensitive),
            UnicodeDecodeError("utf-8", b"\xff", 0, 1, sensitive),
        )

        for failure in failures:
            with self.subTest(failure=type(failure).__name__):
                runner = mock.Mock()
                runner.run.side_effect = failure

                with self.assertRaises(AvscoreError) as raised:
                    load_sessions(runner)

                self.assertNotIn("secret", str(raised.exception))


class CommandRunnerTests(unittest.TestCase):
    @mock.patch("avscore_server.subprocess.run")
    def test_run_uses_argument_array_and_safe_subprocess_options(self, run):
        expected = subprocess.CompletedProcess([], 0, "{}", "")
        run.return_value = expected

        result = CommandRunner("agentsview", timeout=12).run(["session", "list"])

        self.assertIs(result, expected)
        run.assert_called_once_with(
            ["agentsview", "session", "list"],
            capture_output=True,
            text=True,
            timeout=12,
            check=False,
        )

    @mock.patch("avscore_server.subprocess.run")
    def test_run_converts_process_failures_to_safe_error(self, run):
        sensitive = "token=secret"
        failures = (
            subprocess.TimeoutExpired(["agentsview", sensitive], 12),
            OSError(sensitive),
            UnicodeDecodeError("utf-8", b"\xff", 0, 1, sensitive),
        )

        for failure in failures:
            with self.subTest(failure=type(failure).__name__):
                run.side_effect = failure

                with self.assertRaises(AvscoreError) as raised:
                    CommandRunner("agentsview", timeout=12).run(["session", "list"])

                self.assertEqual(str(raised.exception), "无法执行 agentsview 命令")
                self.assertNotIn("secret", str(raised.exception))


class SafeErrorTests(unittest.TestCase):
    def test_safe_error_never_includes_stderr_body(self):
        error = safe_error("session list failed", "Authorization: Bearer secret")

        self.assertEqual(error, "session list failed")
        self.assertNotIn("secret", error)


class ProfileRunnerTests(unittest.TestCase):
    def result(self, returncode=0, stdout="", stderr=""):
        return subprocess.CompletedProcess([], returncode, stdout, stderr)

    def test_project_is_passed_as_one_literal_argument(self):
        project = "repo; touch /tmp/not-run $(whoami)"
        runner = RecordingRunner(
            [self.result(stdout=__import__("json").dumps(profile_payload()))]
        )

        profile, degraded = run_profile(runner, project)

        self.assertEqual(
            runner.calls,
            [
                [
                    "profile",
                    "--json",
                    "--engine",
                    "statistical",
                    "--project",
                    project,
                ]
            ],
        )
        self.assertFalse(degraded)
        self.assertEqual(profile["profile"]["steering"]["score"], 50)

    def test_falls_back_only_when_engine_option_is_unknown(self):
        for stderr in (
            "unknown flag: --engine",
            "unrecognized option '--engine'",
            "unknown option --engine",
        ):
            with self.subTest(stderr=stderr):
                runner = RecordingRunner(
                    [
                        self.result(returncode=2, stderr=stderr),
                        self.result(
                            stdout=__import__("json").dumps(profile_payload())
                        ),
                    ]
                )

                _, degraded = run_profile(runner, "atr")

                self.assertTrue(degraded)
                self.assertEqual(
                    runner.calls[1],
                    ["profile", "--json", "--project", "atr"],
                )

    def test_does_not_fallback_for_other_failures(self):
        for stderr in (
            "database is locked",
            "analysis failed",
            "unknown flag: --format",
            "engine initialization failed",
            "unknown flag: --engine-mode",
            "unrecognized option --engineX",
        ):
            with self.subTest(stderr=stderr):
                runner = RecordingRunner(
                    [self.result(returncode=1, stderr=stderr)]
                )

                with self.assertRaisesRegex(AvscoreError, "画像分析失败"):
                    run_profile(runner, "atr")

                self.assertEqual(len(runner.calls), 1)

    def test_fallback_failure_is_safe(self):
        runner = RecordingRunner(
            [
                self.result(returncode=2, stderr="unknown flag: --engine"),
                self.result(returncode=1, stderr="token=secret"),
            ]
        )

        with self.assertRaises(AvscoreError) as raised:
            run_profile(runner, "atr")

        self.assertNotIn("secret", str(raised.exception))


class ProfileParsingTests(unittest.TestCase):
    def test_accepts_nested_and_direct_dimension_shapes(self):
        nested = parse_profile(__import__("json").dumps(profile_payload()))
        direct_payload = profile_payload()
        direct_payload.update(direct_payload.pop("profile"))
        direct = parse_profile(__import__("json").dumps(direct_payload))

        self.assertEqual(nested["profile"]["adaptation"]["score"], 50)
        self.assertEqual(direct["adaptation"]["score"], 50)

    def test_rejects_invalid_json_and_invalid_core_scores(self):
        invalid_scores = [None, "50", float("nan"), float("inf"), -1, 101]
        with self.assertRaises(AvscoreError):
            parse_profile("{")

        for score in invalid_scores:
            with self.subTest(score=score):
                payload = profile_payload()
                payload["profile"]["steering"]["score"] = score
                with self.assertRaises(AvscoreError):
                    parse_profile(__import__("json").dumps(payload))

        payload = profile_payload()
        del payload["profile"]["steering"]
        with self.assertRaises(AvscoreError):
            parse_profile(__import__("json").dumps(payload))

    def test_rejects_nonstandard_json_constants_outside_scores(self):
        valid_profile = __import__("json").dumps(profile_payload())
        for constant in ("NaN", "Infinity", "-Infinity"):
            with self.subTest(constant=constant):
                raw = valid_profile[:-1] + f', "extra": {constant}' + "}"

                with self.assertRaises(AvscoreError):
                    parse_profile(raw)

    def test_build_report_model_has_stable_defaults_and_metadata(self):
        payload = {"profile": {key: {"score": 25} for key in DIMENSIONS}}

        model = build_report_model(payload, "atr", 7, True)

        self.assertEqual(model["project"], "atr")
        self.assertEqual(model["project_session_count"], 7)
        self.assertTrue(model["generated_at"])
        self.assertEqual(model["engine"], "compatibility")
        self.assertTrue(model["degraded"])
        self.assertEqual(
            model["archetype"],
            {
                "primary": "协作画像",
                "code": "7D",
                "title": "协作画像",
                "summary": "这份画像呈现当前项目中可观察到的 AI 协作倾向。",
                "traits": ["项目协作", "七维观察", "基于会话"],
                "confidence": 0,
            },
        )
        self.assertEqual(
            model["trend"], {"prediction": "暂无演化数据", "key_shifts": []}
        )
        self.assertEqual(
            [item["key"] for item in model["dimensions"]], list(DIMENSIONS)
        )
        self.assertTrue(
            all(item["summary"] and item["evidence"] for item in model["dimensions"])
        )

    def test_build_report_model_preserves_supported_profile_fields(self):
        model = build_report_model(profile_payload(), "atr", 3, False)

        self.assertEqual(model["generated_at"], "2026-07-27T09:00:00Z")
        self.assertEqual(model["engine"], "statistical")
        self.assertEqual(model["archetype"]["primary"], "系统设计者")
        self.assertEqual(model["trend"]["prediction"], "保持稳健")
        self.assertEqual(model["dimensions"][0]["score"], 50)

    def test_build_report_model_sanitizes_malformed_optional_fields(self):
        invalid_confidences = (-0.1, 1.1, float("nan"), float("inf"), "0.8", True)
        for confidence in invalid_confidences:
            with self.subTest(confidence=confidence):
                payload = profile_payload()
                payload["archetype"]["confidence"] = confidence

                model = build_report_model(payload, "atr", 3, False)

                self.assertEqual(model["archetype"]["confidence"], 0)

        payload = profile_payload()
        payload["evolution"]["key_shifts"] = [
            "",
            "  ",
            {"dimension": "steering"},
            42,
            "shift 1",
            " shift 2 ",
            "shift 3",
            "shift 4",
            "shift 5",
            "shift 6",
        ]

        model = build_report_model(payload, "atr", 3, False)

        self.assertEqual(
            model["trend"]["key_shifts"],
            ["shift 1", " shift 2 ", "shift 3", "shift 4", "shift 5"],
        )


class AnalysisCoordinatorTests(unittest.TestCase):
    def test_rejects_concurrent_work_and_releases_after_success(self):
        coordinator = AnalysisCoordinator()
        entered = threading.Event()
        release = threading.Event()

        def first():
            with coordinator:
                entered.set()
                release.wait(2)

        thread = threading.Thread(target=first)
        thread.start()
        self.assertTrue(entered.wait(1))
        with self.assertRaises(AnalysisBusyError):
            with coordinator:
                pass
        release.set()
        thread.join(2)
        self.assertFalse(thread.is_alive())
        with coordinator:
            pass

    def test_releases_after_exception(self):
        coordinator = AnalysisCoordinator()

        with self.assertRaisesRegex(RuntimeError, "boom"):
            with coordinator:
                raise RuntimeError("boom")

        with coordinator:
            pass


class AtomicWriteTests(unittest.TestCase):
    def test_creates_parent_and_replaces_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "nested" / "report.html"

            atomic_write(path, "new report")

            self.assertEqual(path.read_text(encoding="utf-8"), "new report")
            self.assertEqual(list(path.parent.iterdir()), [path])

    @mock.patch("avscore_server.os.replace", side_effect=OSError("replace failed"))
    def test_replace_failure_preserves_old_file_and_cleans_temp(self, replace):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.html"
            path.write_text("old report", encoding="utf-8")

            with self.assertRaises(OSError):
                atomic_write(path, "new report")

            self.assertEqual(path.read_text(encoding="utf-8"), "old report")
            self.assertEqual(list(path.parent.iterdir()), [path])
            replace.assert_called_once()


class SelectionRenderingTests(unittest.TestCase):
    def test_render_selection_embeds_parseable_bootstrap_data(self):
        groups = [
            {
                "agent": "codex",
                "sessions": [
                    {
                        "id": "s-1",
                        "project": "atr",
                        "title": "真实会话",
                        "ended_at": "2026-07-27T08:20:00Z",
                        "message_count": 42,
                        "projectSessionCount": 3,
                    }
                ],
            }
        ]

        rendered = render_selection(groups, "token-123")
        match = re.search(
            r'<script type="application/json" id="bootstrap-data">(.*?)</script>',
            rendered,
            re.DOTALL,
        )

        self.assertIsNotNone(match)
        self.assertEqual(
            json.loads(match.group(1)),
            {"groups": groups, "token": "token-123"},
        )

    def test_render_selection_prevents_script_breakout(self):
        attack = '</script><img src=x onerror="alert(1)">{{unknown}}'
        groups = [
            {
                "agent": attack,
                "sessions": [
                    {
                        "id": attack,
                        "project": attack,
                        "title": attack,
                        "projectSessionCount": 1,
                    }
                ],
            }
        ]

        rendered = render_selection(groups, attack)
        payload = re.search(
            r'<script type="application/json" id="bootstrap-data">(.*?)</script>',
            rendered,
            re.DOTALL,
        ).group(1)

        self.assertNotIn(attack, rendered)
        self.assertNotIn("<", payload)
        self.assertEqual(json.loads(payload), {"groups": groups, "token": attack})

    def test_render_selection_rejects_unknown_template_placeholders(self):
        with tempfile.TemporaryDirectory() as directory:
            template = Path(directory) / "selection.html.tmpl"
            for marker in (
                "{{UNKNOWN_PLACEHOLDER}}",
                "{{unknown}}",
                "{{ Mixed_Placeholder }}",
                "{{ spaces are unknown }}",
            ):
                with self.subTest(marker=marker):
                    template.write_text(
                        "{{BOOTSTRAP_JSON}} " + marker, encoding="utf-8"
                    )

                    with self.assertRaisesRegex(
                        AvscoreError, "unknown template placeholder"
                    ):
                        render_selection([], "token", template_path=template)

    def test_render_selection_rejects_template_markers_left_after_rendering(self):
        with tempfile.TemporaryDirectory() as directory:
            template = Path(directory) / "selection.html.tmpl"
            template.write_text(
                "{{BOOTSTRAP_JSON}} {{ malformed marker", encoding="utf-8"
            )

            with self.assertRaisesRegex(AvscoreError, "template marker"):
                render_selection([], "token", template_path=template)


class ReportRenderingTests(unittest.TestCase):
    def test_render_report_embeds_real_model_and_safe_return_url(self):
        payload = profile_payload()
        payload["archetype"].update(
            {
                "code": "REAL",
                "title": "真实画像",
                "summary": "项目 <b>摘要</b>",
                "traits": ["系统化", "<script>alert(1)</script>"],
            }
        )
        payload["profile"]["steering"].update(
            {"title": "引导 <强>", "summary": "不形成 <img>", "evidence": "证据"}
        )
        model = build_report_model(payload, "项目 <x>", 3, False)

        rendered = render_report(model, "a token&next=/x")

        self.assertNotRegex(rendered, r"{{.*?}}")
        self.assertNotIn("<script>alert(1)</script>", rendered)
        self.assertNotIn("<b>摘要</b>", rendered)
        self.assertNotIn("<img>", rendered)
        self.assertIn("/?token=a%20token%26next%3D%2Fx", rendered)
        match = re.search(
            r'<script type="application/json" id="report-data">(.*?)</script>',
            rendered,
            re.DOTALL,
        )
        self.assertIsNotNone(match)
        self.assertEqual(json.loads(match.group(1)), model)

    def test_render_report_rejects_unknown_or_missing_placeholders(self):
        model = build_report_model(profile_payload(), "atr", 3, False)
        with tempfile.TemporaryDirectory() as directory:
            template = Path(directory) / "report.html"
            template.write_text(
                "{{REPORT_JSON}} {{RETURN_URL}} {{UNKNOWN}}", encoding="utf-8"
            )
            with self.assertRaisesRegex(AvscoreError, "unknown template placeholder"):
                render_report(model, "token", template)

            template.write_text("{{REPORT_JSON}}", encoding="utf-8")
            with self.assertRaisesRegex(AvscoreError, "missing template placeholder"):
                render_report(model, "token", template)


if __name__ == "__main__":
    unittest.main()
