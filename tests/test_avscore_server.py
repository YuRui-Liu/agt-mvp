import subprocess
import unittest
from unittest import mock

from avscore_server import (
    AvscoreError,
    CommandRunner,
    load_sessions,
    normalize_sessions,
    safe_error,
)


class FakeRunner:
    def __init__(self, result):
        self.result = result
        self.args = None

    def run(self, args):
        self.args = args
        return self.result


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
        self.assertEqual(groups[0]["sessions"][0]["projectSessionCount"], 1)

    def test_drops_records_without_id_or_project(self):
        payload = {
            "sessions": [
                {"project": "atr", "agent": "codex"},
                {"id": "s-1", "agent": "codex"},
            ]
        }

        self.assertEqual(normalize_sessions(payload), [])

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

                self.assertNotIn("secret", str(raised.exception))


class SafeErrorTests(unittest.TestCase):
    def test_safe_error_never_includes_stderr_body(self):
        error = safe_error("session list failed", "Authorization: Bearer secret")

        self.assertEqual(error, "session list failed")
        self.assertNotIn("secret", error)


if __name__ == "__main__":
    unittest.main()
