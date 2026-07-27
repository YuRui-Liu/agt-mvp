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
        return subprocess.run(
            [self.binary, *args],
            capture_output=True,
            text=True,
            timeout=self.timeout,
            check=False,
        )


def safe_error(summary, stderr=""):
    """Return a bounded error summary without exposing command output."""

    del stderr
    return str(summary)[:240]


def load_sessions(runner):
    result = runner.run(
        ["session", "list", "--format", "json", "--limit", "500"]
    )
    if result.returncode != 0:
        raise AvscoreError(
            safe_error("无法从 agentsview 加载 session", result.stderr)
        )

    try:
        payload = json.loads(result.stdout)
    except (json.JSONDecodeError, TypeError):
        raise AvscoreError("agentsview 返回了无效的 session JSON")

    return normalize_sessions(payload)


def normalize_sessions(payload):
    records = payload.get("sessions", []) if isinstance(payload, dict) else []
    valid = [
        dict(record)
        for record in records
        if isinstance(record, dict) and record.get("id") and record.get("project")
    ]
    project_counts = Counter(record["project"] for record in valid)
    groups = {}

    for record in valid:
        session = dict(record)
        session["agent"] = record.get("agent") or "unknown"
        session["title"] = (
            record.get("display_name")
            or record.get("first_message")
            or "未命名会话"
        )
        session["projectSessionCount"] = project_counts[record["project"]]
        groups.setdefault(session["agent"], []).append(session)

    normalized = []
    for agent, sessions in groups.items():
        sessions.sort(key=lambda session: session.get("ended_at") or "", reverse=True)
        normalized.append({"agent": agent, "sessions": sessions})
    return normalized
