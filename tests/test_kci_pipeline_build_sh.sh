#!/bin/sh
set -eu

SOURCE_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/kuai-kci-test.XXXXXX")
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM
mkdir -p "$TEST_ROOT/repo/cmd/kuai" "$TEST_ROOT/fakebin"
cp "$SOURCE_ROOT/build.sh" "$SOURCE_ROOT/kci-pipeline-build.sh" "$TEST_ROOT/repo/"
chmod +x "$TEST_ROOT/repo/build.sh" "$TEST_ROOT/repo/kci-pipeline-build.sh"

cat >"$TEST_ROOT/fakebin/go" <<'EOF'
#!/bin/sh
if [ "$1" = version ]; then
  echo "go version ${FAKE_GO_VERSION:-go1.26.5} test/fake"
  exit 0
fi
[ "$1" = build ] || exit 2
output=
args=$*
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then output=$2; shift 2; else shift; fi
done
printf '%s/%s|CGO=%s|%s\n' "$GOOS" "$GOARCH" "$CGO_ENABLED" "$args" >>"$FAKE_GO_LOG"
printf 'kuai %s/%s\n' "$GOOS" "$GOARCH" >"$output"
[ -z "${FAKE_BUILD_DELAY:-}" ] || sleep "$FAKE_BUILD_DELAY"
EOF
chmod +x "$TEST_ROOT/fakebin/go"

cat >"$TEST_ROOT/fakebin/curl" <<'EOF'
#!/bin/sh
output=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output=$2; shift 2 ;;
    http*) url=$1; shift ;;
    *) shift ;;
  esac
done
case "$url" in
  */api/create) printf '{"data":{"task":{"id":42}}}' ;;
  */api/status) printf '{"data":{"task":{"status":"%s"}}}' "${FAKE_SIGN_STATUS:-completed}" ;;
  */api/download*) printf 'signed windows binary\n' >"$output" ;;
  *) exit 9 ;;
esac
EOF
chmod +x "$TEST_ROOT/fakebin/curl"

cat >"$TEST_ROOT/fakebin/jq" <<'EOF'
#!/bin/sh
input=$(cat)
query=${2:-$1}
case "$query" in
  .data.task.id) printf '%s\n' 42 ;;
  .data.task.status) printf '%s\n' "${FAKE_SIGN_STATUS:-completed}" ;;
  *status*)
    [ "${FAKE_NOTARY_STATUS:-Accepted}" != MISSING ] || exit 2
    printf '%s\n' "${FAKE_NOTARY_STATUS:-Accepted}"
    ;;
  *) exit 2 ;;
esac
EOF
chmod +x "$TEST_ROOT/fakebin/jq"

cat >"$TEST_ROOT/fakebin/signtool" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_SIGN_LOG"
printf 'Signer Certificate:\n    Subject: CN=%s\nTimestamp Certificate:\n    Subject: CN=%s\nSuccessfully verified\n' \
  "${FAKE_SIGNTOOL_LEAF:-Unexpected Publisher}" "${WINDOWS_SIGNING_PUBLISHER:-Qrite Technology Limited}"
EOF
chmod +x "$TEST_ROOT/fakebin/signtool"

cat >"$TEST_ROOT/fakebin/powershell.exe" <<'EOF'
#!/bin/sh
printf '%s\n' "${FAKE_SIGN_PUBLISHER_OUTPUT:-CN=Qrite Technology Limited}"
EOF
chmod +x "$TEST_ROOT/fakebin/powershell.exe"

cat >"$TEST_ROOT/fakebin/codesign" <<'EOF'
#!/bin/sh
printf 'codesign %s\n' "$*" >>"$FAKE_SIGN_LOG"
EOF
chmod +x "$TEST_ROOT/fakebin/codesign"

cat >"$TEST_ROOT/fakebin/ditto" <<'EOF'
#!/bin/sh
printf 'ditto %s\n' "$*" >>"$FAKE_SIGN_LOG"
eval "output=\${$#}"
printf 'archive\n' >"$output"
EOF
chmod +x "$TEST_ROOT/fakebin/ditto"

cat >"$TEST_ROOT/fakebin/xcrun" <<'EOF'
#!/bin/sh
printf 'xcrun %s\n' "$*" >>"$FAKE_SIGN_LOG"
case "$*" in
  'notarytool submit '*)
    if [ "${FAKE_NOTARY_STATUS:-Accepted}" = MISSING ]; then
      printf '{}\n'
    else
      printf '{"status":"%s"}\n' "${FAKE_NOTARY_STATUS:-Accepted}"
    fi
    ;;
esac
EOF
chmod +x "$TEST_ROOT/fakebin/xcrun"

cat >"$TEST_ROOT/fakebin/rm" <<'EOF'
#!/bin/sh
if [ "${FAKE_FAIL_STAGE_CLEANUP:-}" = true ]; then
  case "$*" in *.stage.*) exit 74 ;; esac
fi
exec /bin/rm "$@"
EOF
chmod +x "$TEST_ROOT/fakebin/rm"

export FAKE_GO_LOG="$TEST_ROOT/go.log"
export FAKE_SIGN_LOG="$TEST_ROOT/sign.log"
export PATH="$TEST_ROOT/fakebin:/usr/bin:/bin"

run_kci() {
  UPLOAD_PLATFORM=$1 UPLOAD_ARCH=$2 UPLOAD_PACKAGE_VERSION=1.2.3 \
    "$TEST_ROOT/repo/kci-pipeline-build.sh" "${3:-false}" "${4:-false}"
}

for spec in \
  'darwin x64 kuai-darwin-amd64 darwin/amd64' \
  'darwin arm64 kuai-darwin-arm64 darwin/arm64' \
  'linux x86_64 kuai-linux-amd64 linux/amd64' \
  'linux aarch64 kuai-linux-arm64 linux/arm64' \
  'win32 amd64 kuai-windows-amd64.exe windows/amd64' \
  'win32 arm64 kuai-windows-arm64.exe windows/arm64'
do
  set -- $spec
  run_kci "$1" "$2" >/dev/null
  [ -f "$TEST_ROOT/repo/dist/targets/$3/$3" ]
  [ -f "$TEST_ROOT/repo/dist/targets/$3/$3.sha256" ]
  grep -q "^$4|CGO=0|.*-X main.version=1.2.3" "$FAKE_GO_LOG"
done

for args in 'maybe false' 'false maybe'; do
  set -- $args
  run_kci linux amd64 "$1" "$2" >/dev/null 2>&1 && {
    echo "invalid signing boolean unexpectedly accepted" >&2
    exit 1
  }
done

UPLOAD_PLATFORM=linux UPLOAD_ARCH=amd64 \
  "$TEST_ROOT/repo/kci-pipeline-build.sh" false false >/dev/null 2>&1 && {
  echo "missing release version unexpectedly accepted" >&2
  exit 1
}

FAKE_GO_VERSION=go1.25.0 run_kci linux amd64 >/dev/null 2>&1 && {
  echo "wrong Go version unexpectedly accepted" >&2
  exit 1
}

rm -rf "$TEST_ROOT/repo/dist"
mkdir "$TEST_ROOT/outside-dist"
printf 'outside\n' >"$TEST_ROOT/outside-dist/marker"
ln -s "$TEST_ROOT/outside-dist" "$TEST_ROOT/repo/dist"
run_kci linux amd64 >/dev/null 2>&1 && {
  echo "dist symlink unexpectedly accepted" >&2
  exit 1
}
grep -qx outside "$TEST_ROOT/outside-dist/marker"
[ ! -e "$TEST_ROOT/outside-dist/kuai-linux-amd64" ]
rm "$TEST_ROOT/repo/dist"

mkdir -p "$TEST_ROOT/repo/dist/targets" "$TEST_ROOT/outside-target"
ln -s "$TEST_ROOT/outside-target" \
  "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64"
run_kci linux amd64 >/dev/null 2>&1 && {
  echo "target pair symlink unexpectedly accepted" >&2
  exit 1
}
[ ! -e "$TEST_ROOT/outside-target/kuai-linux-amd64" ]
rm "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64"

mkdir "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64"
ln -s "$TEST_ROOT/outside-target" \
  "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64/kuai-linux-amd64"
printf 'old checksum\n' \
  >"$TEST_ROOT/repo/dist/targets/kuai-linux-amd64/kuai-linux-amd64.sha256"
run_kci linux amd64 >/dev/null 2>&1 && {
  echo "symlinked target artifact unexpectedly accepted" >&2
  exit 1
}
rm -rf "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64"

mkdir "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64"
printf 'old artifact\n' \
  >"$TEST_ROOT/repo/dist/targets/kuai-linux-amd64/kuai-linux-amd64"
mkdir "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64/kuai-linux-amd64.sha256"
run_kci linux amd64 >/dev/null 2>&1 && {
  echo "directory target fragment unexpectedly accepted" >&2
  exit 1
}
rm -rf "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64"

assert_one_concurrent_winner() {
  platform=$1 architecture=$2 artifact=$3
  FAKE_BUILD_DELAY=1 run_kci "$platform" "$architecture" >/dev/null 2>&1 & first_pid=$!
  FAKE_BUILD_DELAY=1 run_kci "$platform" "$architecture" >/dev/null 2>&1 & second_pid=$!
  successes=0
  if wait "$first_pid"; then successes=$((successes + 1)); fi
  if wait "$second_pid"; then successes=$((successes + 1)); fi
  [ "$successes" -eq 1 ]
  pair="$TEST_ROOT/repo/dist/targets/$artifact"
  [ "$(find "$pair" -mindepth 1 -maxdepth 1 -print | wc -l | tr -d ' ')" -eq 2 ]
  [ -z "$(find "$pair" -name '*.stage.*' -print)" ]
  [ ! -e "$TEST_ROOT/repo/dist/targets/.$artifact.lock" ]
}

assert_one_concurrent_winner linux amd64 kuai-linux-amd64
assert_one_concurrent_winner linux arm64 kuai-linux-arm64
old_artifact=$(cat "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64/kuai-linux-amd64")
old_checksum=$(cat "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64/kuai-linux-amd64.sha256")
run_kci linux amd64 >/dev/null 2>&1 && {
  echo "immutable target pair was overwritten" >&2
  exit 1
}
[ "$(cat "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64/kuai-linux-amd64")" = "$old_artifact" ]
[ "$(cat "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64/kuai-linux-amd64.sha256")" = "$old_checksum" ]

FAKE_FAIL_STAGE_CLEANUP=true run_kci linux amd64 >/dev/null 2>&1 && {
  echo "existing pair with forced cleanup failure unexpectedly succeeded" >&2
  exit 1
}
[ "$(cat "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64/kuai-linux-amd64")" = "$old_artifact" ]
[ "$(cat "$TEST_ROOT/repo/dist/targets/kuai-linux-amd64/kuai-linux-amd64.sha256")" = "$old_checksum" ]

run_kci linux amd64 true false >/dev/null 2>&1 && {
  echo "Linux signing unexpectedly accepted" >&2
  exit 1
}
run_kci win32 amd64 false true >/dev/null 2>&1 && {
  echo "Windows notarization unexpectedly accepted" >&2
  exit 1
}
run_kci darwin amd64 false true >/dev/null 2>&1 && {
  echo "unsigned macOS notarization unexpectedly accepted" >&2
  exit 1
}

WINDOWS_SIGNING_PUBLISHER='CN=Qrite Technology Limited' \
  run_kci win32 amd64 true false >/dev/null
grep -q '^verify /pa /all /v ' "$FAKE_SIGN_LOG"
/bin/rm -rf "$TEST_ROOT/repo/dist/targets/kuai-windows-amd64.exe"

FAKE_SIGN_PUBLISHER_OUTPUT='Unexpected Publisher' \
WINDOWS_SIGNING_PUBLISHER='CN=Qrite Technology Limited' \
  run_kci win32 amd64 true false >/dev/null 2>&1 && {
  echo "unexpected Windows signing publisher accepted" >&2
  exit 1
}

WINDOWS_SIGNING_PUBLISHER='   ' run_kci win32 amd64 true false >/dev/null 2>&1 && {
  echo "blank Windows signing publisher accepted" >&2
  exit 1
}

FAKE_SIGN_STATUS=surprise WINDOWS_SIGNING_PUBLISHER='CN=Qrite Technology Limited' \
  run_kci win32 amd64 true false >/dev/null 2>&1 && {
  echo "unknown Windows signing status accepted" >&2
  exit 1
}

: >"$FAKE_SIGN_LOG"
APPLE_NOTARY_PROFILE=kuai-notary \
  run_kci darwin arm64 true true >/dev/null
grep -q '^codesign --force --timestamp --options runtime ' "$FAKE_SIGN_LOG"
verify_count=$(grep -c '^codesign --verify --strict --verbose=2 ' "$FAKE_SIGN_LOG")
[ "$verify_count" -eq 2 ]
grep -q '^xcrun notarytool submit .* --keychain-profile kuai-notary --wait --timeout 20m --output-format json$' "$FAKE_SIGN_LOG"
first_verify=$(grep -n '^codesign --verify --strict --verbose=2 ' "$FAKE_SIGN_LOG" | head -1 | cut -d: -f1)
notary_line=$(grep -n '^xcrun notarytool submit ' "$FAKE_SIGN_LOG" | cut -d: -f1)
last_verify=$(grep -n '^codesign --verify --strict --verbose=2 ' "$FAKE_SIGN_LOG" | tail -1 | cut -d: -f1)
[ "$first_verify" -lt "$notary_line" ] && [ "$notary_line" -lt "$last_verify" ]
! grep -Eq -- '--password|stapler' "$FAKE_SIGN_LOG"
/bin/rm -rf "$TEST_ROOT/repo/dist/targets/kuai-darwin-arm64"

APPLE_NOTARY_TIMEOUT=0m APPLE_NOTARY_PROFILE=kuai-notary \
  run_kci darwin arm64 true true >/dev/null 2>&1 && {
  echo "unbounded notarization timeout accepted" >&2
  exit 1
}

for invalid_timeout in 00m 000s; do
  APPLE_NOTARY_TIMEOUT=$invalid_timeout APPLE_NOTARY_PROFILE=kuai-notary \
    run_kci darwin arm64 true true >/dev/null 2>&1 && {
    echo "zero notarization timeout accepted: $invalid_timeout" >&2
    exit 1
  }
done

: >"$FAKE_SIGN_LOG"
APPLE_NOTARY_TIMEOUT=5m APPLE_NOTARY_PROFILE=kuai-notary \
  run_kci darwin arm64 true true >/dev/null
grep -q -- '--timeout 5m --output-format json$' "$FAKE_SIGN_LOG"
/bin/rm -rf "$TEST_ROOT/repo/dist/targets/kuai-darwin-arm64"

FAKE_NOTARY_STATUS=Invalid APPLE_NOTARY_PROFILE=kuai-notary \
  run_kci darwin arm64 true true >/dev/null 2>&1 && {
  echo "invalid notarization status accepted" >&2
  exit 1
}

FAKE_NOTARY_STATUS=Unexpected APPLE_NOTARY_PROFILE=kuai-notary \
  run_kci darwin arm64 true true >/dev/null 2>&1 && {
  echo "unknown notarization status accepted" >&2
  exit 1
}

FAKE_NOTARY_STATUS=MISSING APPLE_NOTARY_PROFILE=kuai-notary \
  run_kci darwin arm64 true true >/dev/null 2>&1 && {
  echo "missing notarization status accepted" >&2
  exit 1
}

echo "kci pipeline build tests passed"
