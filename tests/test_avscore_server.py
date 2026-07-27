import json
import http.client
import io
import os
import re
import socket
import subprocess
import tempfile
import threading
import unittest
from contextlib import contextmanager
from pathlib import Path
from unittest import mock
from urllib.parse import quote

from avscore_server import (
    AnalysisBusyError,
    AnalysisCoordinator,
    AvscoreApp,
    AvscoreError,
    CommandRunner,
    ServerConfig,
    atomic_write,
    build_argument_parser,
    build_report_model,
    create_server,
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


class ServerRunner:
    def __init__(self, sessions):
        self.sessions = sessions
        self.calls = []

    def run(self, args):
        self.calls.append(args)
        if args[:2] == ["session", "list"]:
            return subprocess.CompletedProcess(
                args, 0, json.dumps({"sessions": self.sessions}), ""
            )
        return subprocess.CompletedProcess(args, 0, json.dumps(profile_payload()), "")


@contextmanager
def running_server(tmp_path, *, token="fixed secret", sessions=None, coordinator=None):
    sessions = sessions or [
        {
            "id": "s-1",
            "agent": "codex",
            "project": "server-project",
            "display_name": "Server session",
        }
    ]
    runner = ServerRunner(sessions)
    config = ServerConfig(
        binary="agentsview",
        selection_template=Path(__file__).parents[1] / "session-selection.html.tmpl",
        profile_template=Path(__file__).parents[1] / "avscore.html.tmpl",
        application_template=Path(__file__).parents[1] / "job-application.html.tmpl",
        assets_dir=Path(__file__).parents[1] / "assets",
        output_dir=Path(tmp_path),
        port=0,
    )
    server = create_server(
        config, runner=runner, token=token, coordinator=coordinator
    )
    thread = threading.Thread(target=server.serve_forever)
    thread.start()
    try:
        yield server, runner
    finally:
        server.shutdown()
        server.server_close()
        thread.join(2)


def request(server, method, path, *, body=None, headers=None):
    connection = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=2)
    connection.request(method, path, body=body, headers=headers or {})
    response = connection.getresponse()
    payload = response.read()
    result = response.status, dict(response.getheaders()), payload
    connection.close()
    return result


def raw_request(server, request_bytes):
    connection = socket.create_connection(("127.0.0.1", server.server_port), timeout=2)
    connection.sendall(request_bytes)
    connection.shutdown(socket.SHUT_WR)
    chunks = []
    while True:
        chunk = connection.recv(65536)
        if not chunk:
            break
        chunks.append(chunk)
    connection.close()
    return b"".join(chunks)


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
                "confidence": 0.73,
            }
        )
        payload["evolution"]["key_shifts"] = ["转变一", "转变二"]
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
        self.assertIn("/application?token=a%20token%26next%3D%2Fx", rendered)
        self.assertIn("/assets/poster.png?token=a%20token%26next%3D%2Fx", rendered)
        self.assertIn("/assets/aiti-qr.svg?token=a%20token%26next%3D%2Fx", rendered)
        self.assertIn("root.AITIMock = factory", rendered)
        self.assertNotIn("{{AITI_MOCK_JS}}", rendered)
        match = re.search(
            r'<script type="application/json" id="report-data">(.*?)</script>',
            rendered,
            re.DOTALL,
        )
        self.assertIsNotNone(match)
        self.assertEqual(json.loads(match.group(1)), model)
        self.assertIn('id="archetypePrimary"', rendered)
        self.assertIn("report.archetype.primary", rendered)
        self.assertIn('id="archetypeConfidence"', rendered)
        self.assertIn("report.archetype.confidence", rendered)
        self.assertIn('id="trendShifts"', rendered)
        self.assertIn("report.trend.key_shifts", rendered)
        self.assertRegex(
            rendered, r'id="archetypePrimary">系统设计者</div>'
        )
        self.assertRegex(
            rendered, r'id="archetypeConfidence">置信度：73%</span>'
        )
        self.assertRegex(
            rendered, r'id="trendShifts">\s*转变一 · 转变二\s*</p>'
        )

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

    def test_render_report_round_trips_literal_template_markers_in_real_data(self):
        payload = profile_payload()
        payload["archetype"].update(
            {
                "primary": "系统 {{设计者}}",
                "summary": "摘要 {{不是占位符}}",
                "traits": ["特征 {{一}}"],
            }
        )
        payload["evolution"]["key_shifts"] = ["变化 {{alpha}}"]
        model = build_report_model(payload, "项目 {{真实}}", 3, False)

        rendered = render_report(model, "token {{literal}}")

        self.assertNotIn("{{", rendered)
        report_json = re.search(
            r'<script type="application/json" id="report-data">(.*?)</script>',
            rendered,
            re.DOTALL,
        ).group(1)
        self.assertEqual(json.loads(report_json), model)
        self.assertIn("系统 &#123;&#123;设计者}}", rendered)
        self.assertIn("变化 &#123;&#123;alpha}}", rendered)
        self.assertIn("/?token=token%20%7B%7Bliteral%7D%7D", rendered)

    def test_render_report_script_and_url_injection_stays_inert(self):
        model = build_report_model(profile_payload(), "atr", 3, False)
        rendered = render_report(model, 'x"><script>alert(1)</script>{{bad}}')
        self.assertNotIn('x"><script>alert(1)</script>', rendered)
        self.assertNotIn("{{", rendered)
        self.assertIn(
            "/application?token=x%22%3E%3Cscript%3Ealert%281%29%3C%2Fscript%3E%7B%7Bbad%7D%7D",
            rendered,
        )


class HttpServerTests(unittest.TestCase):
    def test_application_and_fixed_assets_are_query_authenticated(self):
        with tempfile.TemporaryDirectory() as directory:
            with running_server(directory) as (server, _runner):
                token = quote("fixed secret")
                expected = {
                    "/application": ("text/html; charset=utf-8", b"AITI ID"),
                    "/assets/aiti-mock.js": (
                        "text/javascript; charset=utf-8",
                        b"AITIMock",
                    ),
                    "/assets/poster.png": ("image/png", (Path(__file__).parents[1] / "assets/poster.png").read_bytes()),
                    "/assets/aiti-qr.svg": ("image/svg+xml; charset=utf-8", b"<svg"),
                }
                for path, (mime, marker) in expected.items():
                    status, _, _ = request(server, "GET", path + "?token=wrong")
                    self.assertEqual(status, 403)
                    status, headers, body = request(server, "GET", path + "?token=" + token)
                    self.assertEqual(status, 200)
                    self.assertEqual(headers["Content-Type"], mime)
                    self.assertEqual(headers["Content-Length"], str(len(body)))
                    self.assertEqual(headers["Cache-Control"], "no-store")
                    if path.endswith(".png"):
                        self.assertEqual(body, marker)
                        self.assertIn("attachment", headers["Content-Disposition"])
                        self.assertIn("AITI-", headers["Content-Disposition"])
                    else:
                        self.assertIn(marker, body)

    def test_application_assets_head_and_traversal(self):
        with tempfile.TemporaryDirectory() as directory:
            with running_server(directory) as (server, _runner):
                token = quote("fixed secret")
                for path in (
                    "/application",
                    "/assets/aiti-mock.js",
                    "/assets/poster.png",
                    "/assets/aiti-qr.svg",
                ):
                    status, headers, body = request(server, "HEAD", path + "?token=" + token)
                    self.assertEqual(status, 200)
                    self.assertEqual(body, b"")
                    self.assertGreater(int(headers["Content-Length"]), 0)
                for path in ("/assets/../avscore_server.py", "/assets/%2e%2e/avscore_server.py"):
                    status, _, _ = request(server, "GET", path + "?token=" + token)
                    self.assertEqual(status, 404)
    def test_selection_health_authentication_and_security_headers(self):
        with tempfile.TemporaryDirectory() as directory:
            with running_server(directory) as (server, _runner):
                for path in ("/", "/api/health"):
                    status, _headers, body = request(server, "GET", path)
                    self.assertEqual(status, 403)
                    self.assertEqual(json.loads(body), {"message": "Forbidden"})

                status, headers, body = request(
                    server, "GET", "/?token=" + quote("fixed secret")
                )
                self.assertEqual(status, 200)
                self.assertIn(b"Server session", body)
                self.assertIn("text/html; charset=utf-8", headers["Content-Type"])
                self.assertEqual(headers["Cache-Control"], "no-store")
                self.assertEqual(headers["X-Content-Type-Options"], "nosniff")
                self.assertIn("default-src 'none'", headers["Content-Security-Policy"])
                self.assertIn("script-src 'unsafe-inline'", headers["Content-Security-Policy"])
                self.assertIn("img-src 'self' data:", headers["Content-Security-Policy"])

                status, _, body = request(
                    server,
                    "GET",
                    "/api/health",
                    headers={"X-Avscore-Token": "fixed secret"},
                )
                self.assertEqual(status, 200)
                self.assertEqual(json.loads(body), {"status": "ok"})

                status, _, body = request(
                    server,
                    "GET",
                    "/api/health?token=" + quote("fixed secret"),
                )
                self.assertEqual(status, 403)
                self.assertEqual(json.loads(body), {"message": "Forbidden"})

                status, _, body = request(
                    server,
                    "GET",
                    "/api/missing?token=" + quote("fixed secret"),
                )
                self.assertEqual(status, 403)
                self.assertEqual(json.loads(body), {"message": "Forbidden"})

    def test_unknown_route_and_disallowed_method_are_json(self):
        with tempfile.TemporaryDirectory() as directory:
            with running_server(directory) as (server, _runner):
                auth = {"X-Avscore-Token": "fixed secret"}
                status, _, body = request(server, "GET", "/missing", headers=auth)
                self.assertEqual(status, 404)
                self.assertEqual(json.loads(body), {"message": "Not found"})
                status, headers, body = request(server, "PUT", "/", headers=auth)
                self.assertEqual(status, 405)
                self.assertEqual(headers["Allow"], "GET")
                self.assertEqual(json.loads(body), {"message": "Method not allowed"})
                status, _, body = request(
                    server, "POST", "/missing", body="{}", headers=auth
                )
                self.assertEqual(status, 404)
                self.assertEqual(json.loads(body), {"message": "Not found"})
                status, headers, _ = request(
                    server, "GET", "/api/analyze", headers=auth
                )
                self.assertEqual(status, 405)
                self.assertEqual(headers["Allow"], "POST")
                status, headers, body = request(
                    server, "TRACE", "/", headers=auth
                )
                self.assertEqual(status, 405)
                self.assertIn("application/json", headers["Content-Type"])
                self.assertEqual(headers["Cache-Control"], "no-store")
                self.assertEqual(json.loads(body), {"message": "Method not allowed"})

    def test_analyze_rejects_bad_body_schema_and_unknown_session(self):
        cases = (
            ({}, None),
            ({"Content-Type": "text/plain", "Content-Length": "2"}, "{}"),
            ({"Content-Type": "application/json", "Content-Length": "wat"}, "{}"),
            ({"Content-Type": "application/json"}, "{"),
            (
                {"Content-Type": "application/json"},
                json.dumps({"session_id": "s-1", "project": "forged"}),
            ),
            (
                {"Content-Type": "application/json"},
                json.dumps({"session_id": "missing"}),
            ),
        )
        with tempfile.TemporaryDirectory() as directory:
            with running_server(directory) as (server, _runner):
                for headers, body in cases:
                    with self.subTest(headers=headers, body=body):
                        headers = {
                            **headers,
                            "X-Avscore-Token": "fixed secret",
                        }
                        status, _, response = request(
                            server, "POST", "/api/analyze", body=body, headers=headers
                        )
                        expected = 415 if headers.get("Content-Type") == "text/plain" else 400
                        self.assertEqual(status, expected)
                        self.assertEqual(set(json.loads(response)), {"message"})

                status, _, _ = request(
                    server,
                    "POST",
                    "/api/analyze",
                    body=b"x" * (16 * 1024 + 1),
                    headers={
                        "Content-Type": "application/json",
                        "X-Avscore-Token": "fixed secret",
                    },
                )
                self.assertEqual(status, 413)

    def test_rejects_transfer_encoding_and_duplicate_content_length(self):
        with tempfile.TemporaryDirectory() as directory:
            with running_server(directory) as (server, _runner):
                for framing in (
                    b"Transfer-Encoding: chunked\r\n",
                    b"Content-Length: 20\r\nContent-Length: 20\r\n",
                ):
                    raw = raw_request(
                        server,
                        b"POST /api/analyze HTTP/1.1\r\n"
                        b"Host: localhost\r\n"
                        b"X-Avscore-Token: fixed secret\r\n"
                        b"Content-Type: application/json\r\n"
                        + framing
                        + b"Connection: close\r\n\r\n"
                        + b'{"session_id":"s-1"}',
                    )
                    self.assertTrue(raw.startswith(b"HTTP/1.0 400"))
                    self.assertIn(b"application/json; charset=utf-8", raw)

    def test_head_returns_headers_without_a_response_body(self):
        with tempfile.TemporaryDirectory() as directory:
            with running_server(directory) as (server, _runner):
                raw = raw_request(
                    server,
                    b"HEAD / HTTP/1.1\r\n"
                    b"Host: localhost\r\n"
                    b"X-Avscore-Token: fixed secret\r\n"
                    b"Connection: close\r\n\r\n",
                )
                headers, separator, body = raw.partition(b"\r\n\r\n")
                self.assertEqual(separator, b"\r\n\r\n")
                self.assertTrue(headers.startswith(b"HTTP/1.0 405"))
                self.assertEqual(body, b"")
                self.assertIn(b"Cache-Control: no-store", headers)

    @mock.patch("avscore_server.run_profile")
    def test_valid_session_uses_server_project_and_publishes_three_files(self, profile):
        profile.return_value = (profile_payload(61), False)
        with tempfile.TemporaryDirectory() as directory:
            with running_server(directory, token="a token&") as (server, _runner):
                body = json.dumps({"session_id": "s-1"})
                status, _, response = request(
                    server,
                    "POST",
                    "/api/analyze",
                    body=body,
                    headers={
                        "Content-Type": "application/json",
                        "X-Avscore-Token": "a token&",
                    },
                )
                self.assertEqual(status, 200)
                self.assertEqual(
                    json.loads(response),
                    {"report_url": "/report?token=a%20token%26"},
                )
                profile.assert_called_once()
                self.assertEqual(profile.call_args.args[1], "server-project")
                output = Path(directory)
                self.assertEqual(
                    json.loads((output / "profile.json").read_text()),
                    profile_payload(61),
                )
                model = json.loads((output / "report.json").read_text())
                self.assertEqual(model["project"], "server-project")
                self.assertEqual(model["project_session_count"], 1)
                self.assertIn("report-data", (output / "report.html").read_text())

                status, headers, report = request(
                    server, "GET", "/report?token=" + quote("a token&")
                )
                self.assertEqual(status, 200)
                self.assertEqual(report, (output / "report.html").read_bytes())
                self.assertEqual(headers["Cache-Control"], "no-store")

    @unittest.skipIf(os.name == "nt", "POSIX permission bits required")
    def test_new_output_directory_and_published_files_are_private(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "private-output"
            config = ServerConfig(
                binary="agentsview",
                selection_template=Path(__file__).parents[1]
                / "session-selection.html.tmpl",
                profile_template=Path(__file__).parents[1] / "avscore.html.tmpl",
                output_dir=output,
            )
            app = AvscoreApp(config, ServerRunner([]), "token", [])
            app._publish(profile_payload(), {"safe": True}, "<html></html>")
            self.assertEqual(output.stat().st_mode & 0o777, 0o700)
            for name in ("profile.json", "report.json", "report.html"):
                with self.subTest(name=name):
                    self.assertEqual((output / name).stat().st_mode & 0o777, 0o600)

    @unittest.skipIf(os.name == "nt", "POSIX permission bits required")
    def test_server_initialization_tightens_existing_output_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "existing-output"
            output.mkdir(mode=0o755)
            config = ServerConfig(
                binary="agentsview",
                selection_template=Path(__file__).parents[1]
                / "session-selection.html.tmpl",
                profile_template=Path(__file__).parents[1] / "avscore.html.tmpl",
                output_dir=output,
            )
            AvscoreApp(config, ServerRunner([]), "token", [])
            self.assertEqual(output.stat().st_mode & 0o777, 0o700)

    def test_report_absent_busy_and_safe_failure_preserves_old_report(self):
        with tempfile.TemporaryDirectory() as directory:
            coordinator = AnalysisCoordinator()
            with running_server(directory, coordinator=coordinator) as (server, _runner):
                status, _, _ = request(
                    server, "GET", "/report?token=" + quote("fixed secret")
                )
                self.assertEqual(status, 404)

                coordinator._lock.acquire()
                try:
                    status, _, body = request(
                        server,
                        "POST",
                        "/api/analyze",
                        body=json.dumps({"session_id": "s-1"}),
                        headers={
                            "Content-Type": "application/json",
                            "X-Avscore-Token": "fixed secret",
                        },
                    )
                finally:
                    coordinator._lock.release()
                self.assertEqual(status, 409)
                self.assertEqual(set(json.loads(body)), {"message"})

                output = Path(directory)
                for name in ("profile.json", "report.json", "report.html"):
                    (output / name).write_text("old-success", encoding="utf-8")
                server.app.report_html = "old-success"
                with mock.patch(
                    "avscore_server.run_profile",
                    side_effect=AvscoreError("safe summary"),
                ):
                    status, _, body = request(
                        server,
                        "POST",
                        "/api/analyze",
                        body=json.dumps({"session_id": "s-1"}),
                        headers={
                            "Content-Type": "application/json",
                            "X-Avscore-Token": "fixed secret",
                        },
                    )
                self.assertEqual(status, 500)
                self.assertEqual(json.loads(body), {"message": "safe summary"})
                for name in ("profile.json", "report.json", "report.html"):
                    self.assertEqual((output / name).read_text(), "old-success")

    @mock.patch("avscore_server.run_profile")
    def test_publish_replace_failure_rolls_back_all_three_old_files(self, profile):
        profile.return_value = (profile_payload(70), False)
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            old = {
                "profile.json": '{"old":"profile"}',
                "report.json": '{"old":"report"}',
                "report.html": "old report html",
            }
            for name, content in old.items():
                (output / name).write_text(content, encoding="utf-8")
            real_replace = __import__("os").replace
            failed = False

            def fail_second_install(source, destination):
                nonlocal failed
                source_path = Path(source)
                destination_path = Path(destination)
                if (
                    not failed
                    and source_path.name == "report.json"
                    and destination_path == output / "report.json"
                ):
                    failed = True
                    raise OSError("injected publish failure")
                return real_replace(source, destination)

            with running_server(directory) as (server, _runner):
                with mock.patch(
                    "avscore_server.os.replace", side_effect=fail_second_install
                ):
                    status, _, _ = request(
                        server,
                        "POST",
                        "/api/analyze",
                        body=json.dumps({"session_id": "s-1"}),
                        headers={
                            "Content-Type": "application/json",
                            "X-Avscore-Token": "fixed secret",
                        },
                    )
                self.assertEqual(status, 500)
                for name, content in old.items():
                    self.assertEqual((output / name).read_text(), content)

    @mock.patch("avscore_server.run_profile")
    def test_rollback_continues_and_preserves_unrestored_backup(self, profile):
        profile.return_value = (profile_payload(70), False)
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            old = {
                "profile.json": '{"old":"profile"}',
                "report.json": '{"old":"report"}',
                "report.html": "old report html",
            }
            for name, content in old.items():
                (output / name).write_text(content, encoding="utf-8")
            real_replace = __import__("os").replace

            def fail_install_and_first_restore(source, destination):
                source_path = Path(source)
                destination_path = Path(destination)
                if (
                    source_path.name == "report.json"
                    and destination_path == output / "report.json"
                ):
                    raise OSError("install failed")
                if (
                    source_path.name == ".previous-report.json"
                    and destination_path == output / "report.json"
                ):
                    raise OSError("restore failed")
                return real_replace(source, destination)

            with running_server(directory) as (server, _runner):
                with mock.patch(
                    "avscore_server.os.replace",
                    side_effect=fail_install_and_first_restore,
                ):
                    status, _, body = request(
                        server,
                        "POST",
                        "/api/analyze",
                        body=json.dumps({"session_id": "s-1"}),
                        headers={
                            "Content-Type": "application/json",
                            "X-Avscore-Token": "fixed secret",
                        },
                    )
                self.assertEqual(status, 500)
                self.assertIn("备份已保留", json.loads(body)["message"])
                self.assertEqual((output / "profile.json").read_text(), old["profile.json"])
                self.assertEqual((output / "report.html").read_text(), old["report.html"])
                recoveries = list(output.glob(".avscore-recovery-*"))
                self.assertEqual(len(recoveries), 1)
                backup = recoveries[0] / ".previous-report.json"
                self.assertEqual(backup.read_text(), old["report.json"])

    @mock.patch("avscore_server.run_profile")
    def test_failed_cleanup_of_new_file_preserves_recovery_evidence(self, profile):
        profile.return_value = (profile_payload(70), False)
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            real_replace = __import__("os").replace

            def fail_install_and_cleanup(source, destination):
                source_path = Path(source)
                destination_path = Path(destination)
                if (
                    source_path.name == "report.json"
                    and destination_path == output / "report.json"
                ):
                    raise OSError("install failed")
                if (
                    source_path == output / "profile.json"
                    and destination_path.name == ".failed-profile.json"
                ):
                    raise OSError("cleanup failed")
                return real_replace(source, destination)

            with running_server(directory) as (server, _runner):
                with mock.patch(
                    "avscore_server.os.replace",
                    side_effect=fail_install_and_cleanup,
                ):
                    status, _, body = request(
                        server,
                        "POST",
                        "/api/analyze",
                        body=json.dumps({"session_id": "s-1"}),
                        headers={
                            "Content-Type": "application/json",
                            "X-Avscore-Token": "fixed secret",
                        },
                    )

                self.assertEqual(status, 500)
                self.assertIn("备份已保留", json.loads(body)["message"])
                self.assertTrue((output / "profile.json").is_file())
                recoveries = list(output.glob(".avscore-recovery-*"))
                self.assertEqual(len(recoveries), 1)
                self.assertTrue((recoveries[0] / "report.json").is_file())

    def test_wrong_token_and_request_log_do_not_expose_token(self):
        with tempfile.TemporaryDirectory() as directory:
            with running_server(directory, token="highly-sensitive") as (server, _):
                with mock.patch("sys.stderr") as stderr:
                    status, _, _ = request(
                        server,
                        "GET",
                        "/?token=highly-sensitive-wrong",
                    )
                self.assertEqual(status, 403)
                logged = "".join(str(call) for call in stderr.method_calls)
                self.assertNotIn("highly-sensitive", logged)


class ServerCliTests(unittest.TestCase):
    def test_parser_supports_server_configuration(self):
        args = build_argument_parser().parse_args(
            [
                "--binary", "bin",
                "--selection-template", "selection",
                "--profile-template", "profile",
                "--application-template", "application",
                "--assets-dir", "assets",
                "--output-dir", "out",
                "--port", "1234",
            ]
        )
        self.assertEqual(args.binary, "bin")
        self.assertEqual(args.selection_template, "selection")
        self.assertEqual(args.profile_template, "profile")
        self.assertEqual(args.application_template, "application")
        self.assertEqual(args.assets_dir, "assets")
        self.assertEqual(args.output_dir, "out")
        self.assertEqual(args.port, 1234)

    @mock.patch("avscore_server.signal.signal")
    @mock.patch("avscore_server.signal.getsignal", return_value=object())
    @mock.patch("avscore_server.create_server")
    def test_main_prints_parseable_start_event_before_serving(
        self, create_server_mock, _getsignal, _signal
    ):
        fake_server = mock.Mock()
        fake_server.server_port = 43210
        fake_server.app.token = "secret value&"
        create_server_mock.return_value = fake_server
        stdout = io.StringIO()

        with mock.patch("sys.stdout", stdout):
            from avscore_server import main
            result = main(
                [
                    "--binary", "bin",
                    "--selection-template", "selection",
                    "--profile-template", "profile",
                    "--application-template", "application",
                    "--assets-dir", "assets",
                    "--output-dir", "out",
                ]
            )

        self.assertEqual(result, 0)
        event = json.loads(stdout.getvalue())
        self.assertEqual(event["type"], "server-started")
        self.assertEqual(event["port"], 43210)
        self.assertEqual(
            event["url"], "http://127.0.0.1:43210/?token=secret%20value%26"
        )
        self.assertNotIn("token", event)
        fake_server.serve_forever.assert_called_once_with()
        fake_server.server_close.assert_called_once_with()


if __name__ == "__main__":
    unittest.main()
