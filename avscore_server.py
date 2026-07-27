"""Load and normalize session data produced by the agentsview CLI."""

from collections import Counter
from datetime import datetime, timezone
import json
import math
import os
from pathlib import Path
import re
import subprocess
import tempfile
import threading
from urllib.parse import quote


class AvscoreError(RuntimeError):
    """An expected, user-safe avscore failure."""


class AnalysisBusyError(AvscoreError):
    """Raised when an analysis is already running."""


DIMENSIONS = (
    ("steering", "引导"),
    ("execution", "执行"),
    ("engineering", "工程"),
    ("planning", "规划"),
    ("product", "产品"),
    ("autonomy", "自主"),
    ("adaptation", "适应"),
)

SELECTION_TEMPLATE = Path(__file__).with_name("session-selection.html.tmpl")
REPORT_TEMPLATE = Path(__file__).with_name("avscore.html.tmpl")
_TEMPLATE_PLACEHOLDER = re.compile(r"{{(.*?)}}", re.DOTALL)


def render_selection(groups, token, template_path=SELECTION_TEMPLATE):
    """Render session selection with JSON that cannot terminate its script node."""

    template = Path(template_path).read_text(encoding="utf-8")
    placeholders = set(_TEMPLATE_PLACEHOLDER.findall(template))
    unknown = placeholders - {"BOOTSTRAP_JSON"}
    if unknown:
        raise AvscoreError(
            "unknown template placeholder: " + ", ".join(sorted(unknown))
        )
    if "BOOTSTRAP_JSON" not in placeholders:
        raise AvscoreError("missing template placeholder: BOOTSTRAP_JSON")

    bootstrap = json.dumps(
        {"groups": groups, "token": token},
        ensure_ascii=False,
        separators=(",", ":"),
    )
    bootstrap = bootstrap.replace("<", "\\u003c").replace(
        "\u2028", "\\u2028"
    ).replace("\u2029", "\\u2029")
    bootstrap = bootstrap.replace("{{", "\\u007b\\u007b")
    rendered = template.replace("{{BOOTSTRAP_JSON}}", bootstrap)
    if "{{" in rendered:
        raise AvscoreError("template marker remains after rendering")
    return rendered


def render_report(report_model, token, template_path=REPORT_TEMPLATE):
    """Render a self-contained report with script-safe JSON data."""

    template = Path(template_path).read_text(encoding="utf-8")
    required = {"REPORT_JSON", "RETURN_URL"}
    placeholders = set(_TEMPLATE_PLACEHOLDER.findall(template))
    unknown = placeholders - required
    if unknown:
        raise AvscoreError(
            "unknown template placeholder: " + ", ".join(sorted(unknown))
        )
    missing = required - placeholders
    if missing:
        raise AvscoreError(
            "missing template placeholder: " + ", ".join(sorted(missing))
        )
    report_json = json.dumps(
        report_model, ensure_ascii=False, separators=(",", ":")
    )
    report_json = (
        report_json.replace("<", "\\u003c")
        .replace("\u2028", "\\u2028")
        .replace("\u2029", "\\u2029")
        .replace("{{", "\\u007b\\u007b")
    )
    rendered = template.replace("{{REPORT_JSON}}", report_json)
    rendered = rendered.replace(
        "{{RETURN_URL}}", "/?token=" + quote(str(token), safe="")
    )
    if "{{" in rendered:
        raise AvscoreError("template marker remains after rendering")
    return rendered


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
            raise AvscoreError("无法执行 agentsview 命令") from None


def safe_error(summary, stderr=""):
    """Return a bounded error summary without exposing command output."""

    del stderr
    return str(summary)[:240]


def run_profile(runner, project):
    """Run a project-scoped profile analysis with a narrow legacy fallback."""

    result = runner.run(
        ["profile", "--json", "--engine", "statistical", "--project", project]
    )
    degraded = False
    if result.returncode != 0 and _is_unknown_engine_option(result.stderr):
        result = runner.run(["profile", "--json", "--project", project])
        degraded = True
    if result.returncode != 0:
        raise AvscoreError(safe_error("画像分析失败", result.stderr))
    return parse_profile(result.stdout), degraded


def _is_unknown_engine_option(stderr):
    if not isinstance(stderr, str):
        return False
    return bool(
        re.search(
            r"(?:unknown|unrecognized)\s+(?:flag|option)(?::|\s)+"
            r"(?:['\"])?--engine(?![A-Za-z0-9_-])(?:['\"])?",
            stderr,
            re.IGNORECASE,
        )
    )


def parse_profile(raw):
    """Parse and validate the seven core profile scores."""

    try:
        payload = json.loads(raw, parse_constant=_reject_json_constant)
    except (json.JSONDecodeError, TypeError, UnicodeDecodeError, ValueError):
        raise AvscoreError("agentsview 返回了无效的画像 JSON") from None
    if not isinstance(payload, dict):
        raise AvscoreError("agentsview 返回了无效的画像 JSON")

    scores = payload.get("profile", payload)
    if not isinstance(scores, dict):
        raise AvscoreError("agentsview 返回了无效的画像 JSON")
    for key, _label in DIMENSIONS:
        dimension = scores.get(key)
        score = dimension.get("score") if isinstance(dimension, dict) else None
        if (
            isinstance(score, bool)
            or not isinstance(score, (int, float))
            or not math.isfinite(score)
            or not 0 <= score <= 100
        ):
            raise AvscoreError("agentsview 返回了无效的画像分数")
    return payload


def _reject_json_constant(value):
    raise ValueError(f"invalid JSON constant: {value}")


def build_report_model(profile, project, project_session_count, degraded):
    """Build the stable, template-facing subset of a validated profile."""

    scores = profile.get("profile", profile)
    archetype = profile.get("archetype")
    if not isinstance(archetype, dict):
        archetype = {}
    evolution = profile.get("evolution")
    if not isinstance(evolution, dict):
        evolution = {}

    generated_at = profile.get("generated_at")
    if not _nonempty_string(generated_at):
        generated_at = datetime.now(timezone.utc).isoformat()

    dimensions = []
    for key, label in DIMENSIONS:
        source = scores[key]
        dimensions.append(
            {
                "key": key,
                "label": label,
                "score": source["score"],
                "title": _nonempty_text(source.get("title")) or f"{label}倾向",
                "summary": (
                    _nonempty_text(source.get("summary"))
                    or "该维度反映当前项目中的协作倾向。"
                ),
                "evidence": (
                    _nonempty_text(source.get("evidence"))
                    or "基于当前项目会话中的可用信号生成。"
                ),
            }
        )

    primary = _nonempty_text(archetype.get("primary")) or "协作画像"
    title = _nonempty_text(archetype.get("title")) or primary
    code = _nonempty_text(archetype.get("code")) or "7D"
    summary = (
        _nonempty_text(archetype.get("summary"))
        or "这份画像呈现当前项目中可观察到的 AI 协作倾向。"
    )
    traits = archetype.get("traits")
    if not isinstance(traits, list):
        traits = []
    traits = [_nonempty_text(item) for item in traits]
    traits = [item for item in traits if item][:4]
    if not traits:
        traits = ["项目协作", "七维观察", "基于会话"]
    confidence = archetype.get("confidence", 0)
    if (
        isinstance(confidence, bool)
        or not isinstance(confidence, (int, float))
        or not math.isfinite(confidence)
        or not 0 <= confidence <= 1
    ):
        confidence = 0
    shifts = evolution.get("key_shifts")
    if not isinstance(shifts, list):
        shifts = []
    shifts = [
        shift for shift in shifts if isinstance(shift, str) and shift.strip()
    ][:5]
    return {
        "project": project,
        "project_session_count": project_session_count,
        "generated_at": generated_at,
        "engine": "compatibility" if degraded else "statistical",
        "degraded": bool(degraded),
        "archetype": {
            "primary": primary,
            "code": code,
            "title": title,
            "summary": summary,
            "traits": traits,
            "confidence": confidence,
        },
        "trend": {
            "prediction": (
                _nonempty_text(evolution.get("trend_prediction"))
                or "暂无演化数据"
            ),
            "key_shifts": shifts,
        },
        "dimensions": dimensions,
    }


def atomic_write(path, content):
    """Replace a file atomically, cleaning an incomplete temporary file."""

    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temp_name = None
    try:
        with tempfile.NamedTemporaryFile(
            "w", encoding="utf-8", dir=path.parent, delete=False
        ) as handle:
            temp_name = handle.name
            handle.write(content)
            handle.flush()
        os.replace(temp_name, path)
        temp_name = None
    finally:
        if temp_name is not None:
            try:
                os.unlink(temp_name)
            except OSError:
                pass


class AnalysisCoordinator:
    """Allow at most one analysis at a time per coordinator instance."""

    def __init__(self):
        self._lock = threading.Lock()

    def __enter__(self):
        if not self._lock.acquire(blocking=False):
            raise AnalysisBusyError("已有画像分析正在进行")
        return self

    def __exit__(self, exc_type, exc_value, traceback):
        self._lock.release()
        return False


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

    valid = []
    seen_ids = set()
    for record in records:
        if not (
            isinstance(record, dict)
            and _nonempty_string(record.get("id"))
            and _nonempty_string(record.get("project"))
        ):
            continue
        if record["id"] in seen_ids:
            continue
        seen_ids.add(record["id"])
        valid.append(dict(record))
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
        message_count = record.get("message_count", 0)
        session["messageCount"] = (
            message_count
            if isinstance(message_count, int) and not isinstance(message_count, bool)
            else 0
        )
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
