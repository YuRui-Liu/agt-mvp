"""Load and normalize session data produced by the agentsview CLI."""

from collections import Counter
import json
import subprocess


class AvscoreError(RuntimeError):
    """An expected, user-safe avscore failure."""


class CommandRunner:
    def __init__(self, binary, timeout=180):
        self.binary = binary
        self.timeout = timeout

    def run(self, args):
        try:
            return subprocess.run(
                [self.binary, *args],
                capture_output=True,
                text=True,
                timeout=self.timeout,
                check=False,
            )
        except (subprocess.TimeoutExpired, OSError, UnicodeDecodeError):
            raise AvscoreError("无法执行 agentsview session 命令") from None


def safe_error(summary, stderr=""):
    """Return a bounded error summary without exposing command output."""

    del stderr
    return str(summary)[:240]


def load_sessions(runner):
    try:
        result = runner.run(
            ["session", "list", "--format", "json", "--limit", "500"]
        )
    except AvscoreError:
        raise
    except (subprocess.TimeoutExpired, OSError, UnicodeDecodeError):
        raise AvscoreError("无法执行 agentsview session 命令") from None

    if result.returncode != 0:
        raise AvscoreError(
            safe_error("无法从 agentsview 加载 session", result.stderr)
        )

    try:
        payload = json.loads(result.stdout)
    except (json.JSONDecodeError, TypeError, UnicodeDecodeError):
        raise AvscoreError("agentsview 返回了无效的 session JSON") from None

    return normalize_sessions(payload)


def normalize_sessions(payload):
    if not isinstance(payload, dict):
        raise AvscoreError("agentsview 返回了无效的 session JSON")

    records = payload.get("sessions", [])
    if not isinstance(records, list):
        raise AvscoreError("agentsview 返回了无效的 session JSON")

    valid = [
        dict(record)
        for record in records
        if (
            isinstance(record, dict)
            and _nonempty_string(record.get("id"))
            and _nonempty_string(record.get("project"))
        )
    ]
    project_counts = Counter(record["project"] for record in valid)
    groups = {}

    for record in valid:
        session = dict(record)
        session["agent"] = _nonempty_text(record.get("agent")) or "unknown"
        session["title"] = (
            _nonempty_text(record.get("display_name"))
            or _nonempty_text(record.get("first_message"))
            or "未命名会话"
        )
        if not isinstance(session.get("ended_at"), str):
            session["ended_at"] = ""
        session["projectSessionCount"] = project_counts[record["project"]]
        groups.setdefault(session["agent"], []).append(session)

    normalized = []
    for agent, sessions in groups.items():
        sessions.sort(key=lambda session: session.get("ended_at") or "", reverse=True)
        normalized.append({"agent": agent, "sessions": sessions})
    return normalized


def _nonempty_string(value):
    return isinstance(value, str) and bool(value.strip())


def _nonempty_text(value):
    return value if _nonempty_string(value) else ""
