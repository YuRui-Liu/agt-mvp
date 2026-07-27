"""Load and normalize session data produced by the agentsview CLI."""

from collections import Counter
import argparse
from dataclasses import dataclass
from datetime import datetime, timezone
import hmac
import html
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import math
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
from urllib.parse import parse_qs, quote, urlsplit


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
    required = {
        "REPORT_JSON",
        "RETURN_URL",
        "ARCHETYPE_PRIMARY",
        "ARCHETYPE_CONFIDENCE",
        "TREND_SHIFTS",
    }
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
    archetype = report_model["archetype"]
    rendered = rendered.replace(
        "{{ARCHETYPE_PRIMARY}}", _escape_template_text(archetype["primary"])
    )
    rendered = rendered.replace(
        "{{ARCHETYPE_CONFIDENCE}}",
        str(round(archetype["confidence"] * 100)),
    )
    shifts = report_model["trend"]["key_shifts"]
    shift_copy = " · ".join(shifts) if shifts else "暂无可用的阶段变化信号。"
    rendered = rendered.replace(
        "{{TREND_SHIFTS}}", _escape_template_text(shift_copy)
    )
    if "{{" in rendered:
        raise AvscoreError("template marker remains after rendering")
    return rendered


def _escape_template_text(value):
    """Escape HTML and literal template openers while preserving browser text."""

    return html.escape(str(value)).replace("{", "&#123;")


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


MAX_REQUEST_BODY = 16 * 1024
SECURITY_HEADERS = {
    "Cache-Control": "no-store",
    "X-Content-Type-Options": "nosniff",
    "Content-Security-Policy": (
        "default-src 'none'; style-src 'unsafe-inline'; "
        "script-src 'unsafe-inline'; img-src data:; "
        "connect-src 'self'; base-uri 'none'; form-action 'none'; "
        "frame-ancestors 'none'"
    ),
}


@dataclass(frozen=True)
class ServerConfig:
    binary: str
    selection_template: Path
    profile_template: Path
    output_dir: Path
    port: int = 0


class AvscoreApp:
    def __init__(self, config, runner, token, groups, coordinator=None):
        self.config = config
        self.runner = runner
        self.token = token
        self.groups = groups
        self.coordinator = coordinator or AnalysisCoordinator()
        self.sessions = {
            session["id"]: session
            for group in groups
            for session in group["sessions"]
        }
        self.selection_html = render_selection(
            groups, token, config.selection_template
        )
        report_path = Path(config.output_dir) / "report.html"
        self.report_html = (
            report_path.read_text(encoding="utf-8")
            if report_path.is_file()
            else None
        )

    def authenticated(self, supplied):
        return isinstance(supplied, str) and hmac.compare_digest(
            supplied, self.token
        )

    def analyze(self, session_id):
        session = self.sessions[session_id]
        project = session["project"]
        with self.coordinator:
            profile, degraded = run_profile(self.runner, project)
            model = build_report_model(
                profile, project, session["projectSessionCount"], degraded
            )
            report = render_report(
                model, self.token, self.config.profile_template
            )
            self._publish(profile, model, report)
            self.report_html = report

    def _publish(self, profile, model, report):
        """Stage complete files and roll back the set if publication fails."""
        output_dir = Path(self.config.output_dir)
        output_dir.mkdir(parents=True, exist_ok=True)
        staging = Path(tempfile.mkdtemp(prefix=".avscore-", dir=output_dir))
        backups = []
        installed = []
        try:
            payloads = {
                "profile.json": json.dumps(
                    profile, ensure_ascii=False, indent=2
                ) + "\n",
                "report.json": json.dumps(
                    model, ensure_ascii=False, indent=2
                ) + "\n",
                "report.html": report,
            }
            for name, content in payloads.items():
                (staging / name).write_text(content, encoding="utf-8")
            try:
                for name in payloads:
                    target = output_dir / name
                    backup = staging / (".previous-" + name)
                    if target.exists():
                        os.replace(target, backup)
                        backups.append((target, backup))
                    os.replace(staging / name, target)
                    installed.append(target)
            except Exception:
                for target in reversed(installed):
                    try:
                        os.replace(target, staging / (".failed-" + target.name))
                    except OSError:
                        pass
                for target, backup in reversed(backups):
                    if backup.exists():
                        os.replace(backup, target)
                raise
        finally:
            shutil.rmtree(staging, ignore_errors=True)


class AvscoreHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = False

    def __init__(self, address, app):
        self.app = app
        super().__init__(address, AvscoreRequestHandler)


class AvscoreRequestHandler(BaseHTTPRequestHandler):
    server_version = "avscore"
    sys_version = ""
    _GET_PATHS = frozenset(("/", "/report", "/api/health"))
    _POST_PATHS = frozenset(("/api/analyze",))
    _KNOWN_PATHS = _GET_PATHS | _POST_PATHS

    def log_message(self, format, *args):
        # Request targets can contain the bearer token, so request logging is off.
        return

    def __getattr__(self, name):
        # BaseHTTPRequestHandler otherwise emits an unauthenticated HTML 501 for
        # extension methods such as TRACE.
        if name.startswith("do_"):
            return self._method_not_allowed
        raise AttributeError(name)

    def do_GET(self):
        parsed = urlsplit(self.path)
        if parsed.path not in self._KNOWN_PATHS:
            if not self._authorized(parsed):
                return self._json(403, "Forbidden")
            return self._json(404, "Not found")
        if not self._authorized(parsed):
            return self._json(403, "Forbidden")
        if parsed.path not in self._GET_PATHS:
            return self._json(405, "Method not allowed", allow="POST")
        if parsed.path == "/":
            return self._html(200, self.server.app.selection_html)
        if parsed.path == "/api/health":
            return self._json_payload(200, {"status": "ok"})
        report = self.server.app.report_html
        if report is None:
            return self._json(404, "Report not found")
        return self._html(200, report)

    def do_POST(self):
        parsed = urlsplit(self.path)
        if parsed.path not in self._KNOWN_PATHS:
            if not self._authorized(parsed):
                return self._json(403, "Forbidden")
            return self._json(404, "Not found")
        if not self._authorized(parsed):
            return self._json(403, "Forbidden")
        if parsed.path not in self._POST_PATHS:
            return self._json(405, "Method not allowed", allow="GET")
        try:
            payload = self._read_json()
            if (
                not isinstance(payload, dict)
                or set(payload) != {"session_id"}
                or not _nonempty_string(payload.get("session_id"))
                or payload["session_id"] not in self.server.app.sessions
            ):
                raise ValueError
        except (ValueError, json.JSONDecodeError, UnicodeDecodeError):
            return self._json(400, "Invalid request")
        try:
            self.server.app.analyze(payload["session_id"])
        except AnalysisBusyError as error:
            return self._json(409, str(error))
        except AvscoreError as error:
            return self._json(500, safe_error(error))
        except Exception:
            return self._json(500, "分析失败")
        report_url = "/report?token=" + quote(self.server.app.token, safe="")
        return self._json_payload(200, {"report_url": report_url})

    def do_PUT(self):
        self._method_not_allowed()

    def do_DELETE(self):
        self._method_not_allowed()

    def do_PATCH(self):
        self._method_not_allowed()

    def do_HEAD(self):
        self._method_not_allowed()

    def do_OPTIONS(self):
        self._method_not_allowed()

    def _method_not_allowed(self):
        parsed = urlsplit(self.path)
        if not self._authorized(parsed):
            return self._json(403, "Forbidden")
        if parsed.path not in self._KNOWN_PATHS:
            return self._json(404, "Not found")
        allow = "POST" if parsed.path == "/api/analyze" else "GET"
        return self._json(405, "Method not allowed", allow=allow)

    def _authorized(self, parsed):
        supplied = self.headers.get("X-Avscore-Token")
        if (
            supplied is None
            and self.command == "GET"
            and parsed.path in ("/", "/report")
        ):
            values = parse_qs(parsed.query, keep_blank_values=True).get("token", [])
            supplied = values[0] if len(values) == 1 else None
        return self.server.app.authenticated(supplied)

    def _read_json(self):
        if self.headers.get_content_type() != "application/json":
            raise ValueError
        raw_length = self.headers.get("Content-Length")
        if raw_length is None:
            raise ValueError
        try:
            length = int(raw_length, 10)
        except (TypeError, ValueError):
            raise ValueError from None
        if not 0 < length <= MAX_REQUEST_BODY:
            raise ValueError
        return json.loads(self.rfile.read(length).decode("utf-8"))

    def _html(self, status, content):
        body = content.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self._finish_headers(body)
        self.wfile.write(body)

    def _json(self, status, message, allow=None):
        return self._json_payload(status, {"message": message}, allow=allow)

    def _json_payload(self, status, payload, allow=None):
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        if allow:
            self.send_header("Allow", allow)
        self._finish_headers(body)
        self.wfile.write(body)

    def _finish_headers(self, body):
        for name, value in SECURITY_HEADERS.items():
            self.send_header(name, value)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()


def create_server(config, runner=None, token=None, coordinator=None):
    runner = runner or CommandRunner(config.binary)
    groups = load_sessions(runner)
    token = token if token is not None else secrets.token_urlsafe(32)
    app = AvscoreApp(config, runner, token, groups, coordinator)
    return AvscoreHTTPServer(("127.0.0.1", config.port), app)


def build_argument_parser():
    parser = argparse.ArgumentParser(description="Serve the local avscore flow")
    parser.add_argument("--binary", required=True)
    parser.add_argument("--selection-template", required=True)
    parser.add_argument("--profile-template", required=True)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--port", type=int, default=0)
    return parser


def main(argv=None):
    args = build_argument_parser().parse_args(argv)
    config = ServerConfig(
        binary=args.binary,
        selection_template=Path(args.selection_template),
        profile_template=Path(args.profile_template),
        output_dir=Path(args.output_dir),
        port=args.port,
    )
    server = create_server(config)
    token = server.app.token
    url = "http://127.0.0.1:{}/?token={}".format(
        server.server_port, quote(token, safe="")
    )
    print(
        json.dumps(
            {"type": "server-started", "url": url, "port": server.server_port},
            separators=(",", ":"),
        ),
        flush=True,
    )
    previous_term = signal.getsignal(signal.SIGTERM)

    def stop(_signum, _frame):
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, stop)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        signal.signal(signal.SIGTERM, previous_term)
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
