#!/bin/bash

set -u

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
PASS=0
FAIL=0

pass() {
  PASS=$((PASS + 1))
  printf 'ok %d - %s\n' "$PASS" "$1"
}

fail() {
  FAIL=$((FAIL + 1))
  printf 'not ok - %s\n' "$1" >&2
}

assert_contains() {
  case "$1" in
    *"$2"*) return 0 ;;
  esac
  printf 'expected output to contain: %s\nactual: %s\n' "$2" "$1" >&2
  return 1
}

new_fixture() {
  unset STUB_OS STUB_ARCH
  FIXTURE=$(mktemp -d "${TMPDIR:-/tmp}/avscore-sh-test.XXXXXX")
  mkdir -p "$FIXTURE/bin" "$FIXTURE/app" "$FIXTURE/output"
  cp "$ROOT_DIR/avscore.sh" "$FIXTURE/app/avscore.sh"
  : > "$FIXTURE/app/avscore_server.py"
  : > "$FIXTURE/app/session-selection.html.tmpl"
  : > "$FIXTURE/app/avscore.html.tmpl"
  : > "$FIXTURE/app/job-application.html.tmpl"
  mkdir -p "$FIXTURE/app/assets"
  : > "$FIXTURE/app/assets/poster.png"
  : > "$FIXTURE/app/assets/aiti-qr.svg"
  cat > "$FIXTURE/bin/uname" <<'EOF'
#!/bin/sh
if [ "$1" = "-s" ]; then printf '%s\n' "${STUB_OS:-Linux}"; else printf '%s\n' "${STUB_ARCH:-x86_64}"; fi
EOF
  chmod +x "$FIXTURE/bin/uname"
}

write_agentsview() {
  cat > "$FIXTURE/agentsview custom" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CALLS"
if [ "${1:-}" = "sync" ]; then exit "${SYNC_STATUS:-0}"; fi
exit 0
EOF
  chmod +x "$FIXTURE/agentsview custom"
}

write_python() {
  cat > "$FIXTURE/bin/python3" <<'EOF'
#!/bin/sh
printf '%s\n' "$@" > "$SERVER_ARGS"
trap 'printf term > "$TERM_FILE"; exit 0' TERM INT
printf '%s\n' '{"type":"server-started","url":"http://127.0.0.1:43123/?token=a%20b","port":43123}'
if [ "${SERVER_STAY_ALIVE:-0}" = "1" ]; then
  while :; do sleep 1; done
fi
exit "${SERVER_STATUS:-0}"
EOF
  chmod +x "$FIXTURE/bin/python3"
}

run_launcher() {
  PATH="$FIXTURE/bin:/usr/bin:/bin" \
  HOME="$FIXTURE/home" \
  AVSCORE_BINARY_PATH="$FIXTURE/agentsview custom" \
  AVSCORE_OUTPUT_DIR="$FIXTURE/output dir" \
  CALLS="$FIXTURE/calls" \
  SERVER_ARGS="$FIXTURE/server-args" \
  TERM_FILE="$FIXTURE/term-file" \
  "$@" bash "$FIXTURE/app/avscore.sh" 2>&1
}

test_detection_and_binary_priority() {
  new_fixture
  write_agentsview
  write_python
  output=$(STUB_OS=Darwin STUB_ARCH=arm64 AVSCORE_NO_BROWSER=1 run_launcher env)
  status=$?
  if [ "$status" -eq 0 ] &&
     assert_contains "$output" "平台：darwin/arm64" &&
     assert_contains "$output" "$FIXTURE/agentsview custom"; then
    pass "detects platform and honors explicit binary path"
  else
    fail "detects platform and honors explicit binary path"
  fi
  rm -rf "$FIXTURE"
}

test_unsupported_platform_stops() {
  new_fixture
  write_agentsview
  write_python
  output=$(STUB_OS=Plan9 AVSCORE_NO_BROWSER=1 run_launcher env)
  status=$?
  arch_output=$(STUB_ARCH=sparc AVSCORE_NO_BROWSER=1 run_launcher env)
  arch_status=$?
  if [ "$status" -ne 0 ] &&
     assert_contains "$output" "Unsupported OS" &&
     [ "$arch_status" -ne 0 ] &&
     assert_contains "$arch_output" "Unsupported architecture"; then
    pass "rejects unsupported OS and architecture"
  else
    fail "rejects unsupported OS and architecture"
  fi
  rm -rf "$FIXTURE"
}

test_invalid_explicit_binary_never_downloads() {
  new_fixture
  write_python
  cat > "$FIXTURE/bin/curl" <<'EOF'
#!/bin/sh
printf called > "$CURL_CALLED"
exit 0
EOF
  chmod +x "$FIXTURE/bin/curl"
  output=$(CURL_CALLED="$FIXTURE/curl-called" \
    AVSCORE_BINARY_PATH="$FIXTURE/missing-agentsview" \
    AVSCORE_NO_BROWSER=1 run_launcher env)
  status=$?
  if [ "$status" -ne 0 ] &&
     assert_contains "$output" "AVSCORE_BINARY_PATH 指向的文件不存在" &&
     [ ! -e "$FIXTURE/curl-called" ]; then
    pass "does not download when explicit binary path is invalid"
  else
    fail "does not download when explicit binary path is invalid"
  fi
  rm -rf "$FIXTURE"
}

test_sync_failure_and_skip() {
  new_fixture
  write_agentsview
  write_python
  output=$(SYNC_STATUS=7 AVSCORE_NO_BROWSER=1 run_launcher env)
  status=$?
  skipped=$(SYNC_STATUS=7 AVSCORE_SKIP_SYNC=1 AVSCORE_NO_BROWSER=1 run_launcher env)
  skip_status=$?
  if [ "$status" -ne 0 ] &&
     assert_contains "$output" "agentsview sync 失败" &&
     [ "$skip_status" -eq 0 ] &&
     assert_contains "$skipped" "跳过 agentsview sync"; then
    pass "stops on sync failure unless skip is explicit"
  else
    fail "stops on sync failure unless skip is explicit"
  fi
  rm -rf "$FIXTURE"
}

test_python_and_template_errors() {
  new_fixture
  write_agentsview
  output=$({
      AVSCORE_SOURCE_ONLY=1 source "$FIXTURE/app/avscore.sh"
      command() {
        if [ "${1:-}" = "-v" ] && [ "${2:-}" = "python3" ]; then return 1; fi
        builtin command "$@"
      }
      main
    } 2>&1)
  status=$?
  write_python
  rm "$FIXTURE/app/session-selection.html.tmpl"
  missing=$(AVSCORE_NO_BROWSER=1 run_launcher env)
  missing_status=$?
  if [ "$status" -ne 0 ] &&
     assert_contains "$output" "需要 python3" &&
     [ "$missing_status" -ne 0 ] &&
     assert_contains "$missing" "缺少会话选择模板"; then
    pass "reports missing python and templates clearly"
  else
    fail "reports missing python and templates clearly"
  fi
  rm -rf "$FIXTURE"
}

test_server_args_and_browser_failure() {
  new_fixture
  write_agentsview
  write_python
  cat > "$FIXTURE/bin/xdg-open" <<'EOF'
#!/bin/sh
printf '%s\n' "$1" > "$BROWSER_URL"
exit 9
EOF
  chmod +x "$FIXTURE/bin/xdg-open"
  output=$(BROWSER_URL="$FIXTURE/browser-url" run_launcher env)
  status=$?
  args=$(cat "$FIXTURE/server-args")
  browser_url=$(cat "$FIXTURE/browser-url")
  if [ "$status" -eq 0 ] &&
     assert_contains "$args" "/app/avscore_server.py" &&
     assert_contains "$args" "$FIXTURE/agentsview custom" &&
     assert_contains "$args" "$FIXTURE/output dir" &&
     assert_contains "$args" "--application-template" &&
     assert_contains "$args" "/app/job-application.html.tmpl" &&
     assert_contains "$args" "--assets-dir" &&
     [ "$browser_url" = "http://127.0.0.1:43123/?token=a%20b" ] &&
     assert_contains "$output" "无法自动打开浏览器，请手动访问" &&
     assert_contains "$output" "http://127.0.0.1:43123/?token=a%20b"; then
    pass "quotes server arguments and preserves URL when browser fails"
  else
    fail "quotes server arguments and preserves URL when browser fails"
  fi
  rm -rf "$FIXTURE"
}

test_launcher_creates_private_output_directory() {
  new_fixture
  write_agentsview
  write_python
  output=$(umask 022; AVSCORE_NO_BROWSER=1 run_launcher env)
  status=$?
  permissions=$(
    stat -f '%Lp' "$FIXTURE/output dir" 2>/dev/null ||
      stat -c '%a' "$FIXTURE/output dir"
  )
  if [ "$status" -eq 0 ] && [ "$permissions" = "700" ]; then
    pass "creates report output directory with private permissions"
  else
    printf 'launcher output: %s\npermissions: %s\n' "$output" "$permissions" >&2
    fail "creates report output directory with private permissions"
  fi
  rm -rf "$FIXTURE"
}

test_signal_forwarding() {
  new_fixture
  write_agentsview
  write_python
  SERVER_STAY_ALIVE=1 AVSCORE_NO_BROWSER=1 \
    PATH="$FIXTURE/bin:/usr/bin:/bin" HOME="$FIXTURE/home" \
    AVSCORE_BINARY_PATH="$FIXTURE/agentsview custom" \
    AVSCORE_OUTPUT_DIR="$FIXTURE/output" CALLS="$FIXTURE/calls" \
    SERVER_ARGS="$FIXTURE/server-args" TERM_FILE="$FIXTURE/term-file" \
    bash "$FIXTURE/app/avscore.sh" > "$FIXTURE/output.log" 2>&1 &
  launcher_pid=$!
  attempts=0
  while [ ! -f "$FIXTURE/server-args" ] && [ "$attempts" -lt 50 ]; do
    sleep 0.05
    attempts=$((attempts + 1))
  done
  kill -TERM "$launcher_pid"
  wait "$launcher_pid"
  status=$?
  if [ "$status" -eq 143 ] && [ "$(cat "$FIXTURE/term-file" 2>/dev/null)" = "term" ]; then
    pass "forwards TERM to the server and exits"
  else
    fail "forwards TERM to the server and exits"
  fi
  rm -rf "$FIXTURE"
}

run_install_case() {
  checksum_mode=$1
  (
    export HOME="$FIXTURE/home"
    export AVSCORE_SOURCE_ONLY=1
    source "$ROOT_DIR/avscore.sh"
    curl() {
      output_path=""
      url=""
      while [ "$#" -gt 0 ]; do
        case "$1" in
          -o) output_path=$2; shift 2 ;;
          -*) shift ;;
          *) url=$1; shift ;;
        esac
      done
      case "$url" in
        */SHA256SUMS)
          case "$CHECKSUM_MODE" in
            missing) printf '%s  %s\n' goodhash another-file > "$output_path" ;;
            mismatch) printf '%s  %s\n' wronghash agentsview-linux-amd64 > "$output_path" ;;
            match) printf '%s  %s\n' goodhash agentsview-linux-amd64 > "$output_path" ;;
          esac
          ;;
        *) printf 'new-binary' > "$output_path" ;;
      esac
    }
    sha256_file() { printf '%s\n' goodhash; }
    CHECKSUM_MODE=$checksum_mode
    install_agentsview linux amd64 v1.2.3
  )
}

test_download_checksum_safety_and_atomic_install() {
  new_fixture
  mkdir -p "$FIXTURE/home/.local/bin"
  printf 'old-binary' > "$FIXTURE/home/.local/bin/agentsview"

  missing=$(run_install_case missing 2>&1)
  missing_status=$?
  after_missing=$(cat "$FIXTURE/home/.local/bin/agentsview")
  mismatch=$(run_install_case mismatch 2>&1)
  mismatch_status=$?
  after_mismatch=$(cat "$FIXTURE/home/.local/bin/agentsview")
  success=$(run_install_case match 2>&1)
  success_status=$?
  after_success=$(cat "$FIXTURE/home/.local/bin/agentsview")
  temp_count=$(find "$FIXTURE/home/.local/bin" -name '.agentsview.avscore.*' | wc -l | tr -d ' ')

  if [ "$missing_status" -ne 0 ] &&
     assert_contains "$missing" "SHA256SUMS 中未找到" &&
     [ "$after_missing" = "old-binary" ] &&
     [ "$mismatch_status" -ne 0 ] &&
     assert_contains "$mismatch" "SHA256 校验失败" &&
     [ "$after_mismatch" = "old-binary" ] &&
     [ "$success_status" -eq 0 ] &&
     assert_contains "$success" "SHA256 校验通过" &&
     [ "$after_success" = "new-binary" ] &&
     [ "$temp_count" -eq 0 ]; then
    pass "verifies downloads before atomically replacing the binary"
  else
    fail "verifies downloads before atomically replacing the binary"
  fi
  rm -rf "$FIXTURE"
}

test_resolve_version_rejects_malformed_semver_redirect() {
  (
    AVSCORE_SOURCE_ONLY=1 source "$ROOT_DIR/avscore.sh"
    VERSION=latest
    curl() {
      case "$*" in
        *latest.json*) return 1 ;;
        *) printf '%s' 'https://example.test/releases/tag/v1.2.3evil' ;;
      esac
    }
    resolve_version
  ) >/dev/null 2>&1
  status=$?
  if [ "$status" -ne 0 ]; then
    pass "rejects malformed semver redirects"
  else
    fail "rejects malformed semver redirects"
  fi
}

test_detection_and_binary_priority
test_unsupported_platform_stops
test_invalid_explicit_binary_never_downloads
test_sync_failure_and_skip
test_python_and_template_errors
test_server_args_and_browser_failure
test_launcher_creates_private_output_directory
test_signal_forwarding
test_download_checksum_safety_and_atomic_install
test_resolve_version_rejects_malformed_semver_redirect

printf '1..%d\n' "$((PASS + FAIL))"
[ "$FAIL" -eq 0 ]
