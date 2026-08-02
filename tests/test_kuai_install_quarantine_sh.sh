#!/usr/bin/env bash
# Verifies that install.sh reports a macOS quarantine marker without clearing it.
#
# Real propagation path: the marker travels from the staged download to the
# installed file through `cp`, which preserves com.apple.quarantine. Marking an
# already-installed binary proves nothing, because the installer replaces it
# with a fresh inode via mktemp + mv.
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d "${TMPDIR:-/tmp}/kuai-quarantine-test.XXXXXX")
trap 'rm -rf "$TMP"' EXIT HUP INT TERM
mkdir -p "$TMP/fakebin" "$TMP/home path;'literal"

if ! command -v xattr >/dev/null 2>&1; then
  echo "kuai quarantine advisory tests skipped: xattr unavailable"
  exit 0
fi

cat >"$TMP/fakebin/uname" <<'EOF'
#!/bin/sh
case "$1" in
  -s) printf '%s\n' Darwin ;;
  -m) printf '%s\n' arm64 ;;
  *) exit 2 ;;
esac
EOF

# The stub marks the downloaded artifact when QUARANTINE_DOWNLOAD is set, which
# is exactly what a quarantine-aware downloader would do.
cat >"$TMP/fakebin/curl" <<'EOF'
#!/bin/sh
output=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output=$2; shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
case "$url" in
  */SHA256SUMS)
    printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  kuai-darwin-arm64\n' >"$output"
    ;;
  *)
    printf 'payload\n' >"$output"
    if [ "${QUARANTINE_DOWNLOAD:-0}" = 1 ]; then
      xattr -w com.apple.quarantine "0083;0;Safari;" "$output"
      xattr -w com.example.keep "keep-me" "$output"
    fi
    ;;
esac
EOF

cat >"$TMP/fakebin/shasum" <<'EOF'
#!/bin/sh
shift 2
printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  %s\n' "$1"
EOF

cat >"$TMP/fakebin/sha256sum" <<'EOF'
#!/bin/sh
printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  %s\n' "$1"
EOF
chmod +x "$TMP/fakebin/"*

run_install() {
  HOME="$TMP/home path;'literal" \
  PATH="$TMP/fakebin:/bin:/usr/bin" \
  KUAI_RELEASE_URL="https://kuai.example/releases/v1" \
  QUARANTINE_DOWNLOAD="${QUARANTINE_DOWNLOAD:-0}" \
  /bin/bash "$ROOT/install.sh" 2>"$TMP/stderr.log"
}

target="$TMP/home path;'literal/.local/bin/kuai"

# Case 1: an unmarked download must stay completely silent about quarantine.
run_install >/dev/null
if grep -qi quarantine "$TMP/stderr.log"; then
  echo "unexpected quarantine advisory on a clean install" >&2
  exit 1
fi
if xattr -p com.apple.quarantine "$target" >/dev/null 2>&1; then
  echo "clean install unexpectedly produced a quarantined binary" >&2
  exit 1
fi

# Case 2: a marked download propagates through cp and must be reported.
status=0
QUARANTINE_DOWNLOAD=1 run_install >/dev/null || status=$?
[ "$status" -eq 0 ] || {
  echo "quarantine advisory changed the installer exit code" >&2
  exit 1
}
grep -qi 'carries com.apple.quarantine' "$TMP/stderr.log" || {
  echo "installer did not report the quarantine marker" >&2
  cat "$TMP/stderr.log" >&2
  exit 1
}
remediation=$(sed -n 's/^kuai:   //p' "$TMP/stderr.log")
[ -n "$remediation" ] && [ "$(printf '%s\n' "$remediation" | wc -l | tr -d ' ')" -eq 1 ] || {
  echo "installer did not print the remediation command" >&2
  exit 1
}
grep -Fq 'macOS may block or prompt' "$TMP/stderr.log" || {
  echo "installer overstated the macOS quarantine outcome" >&2
  exit 1
}

# Case 3: the installer must only report, never clear, the marker.
xattr -p com.apple.quarantine "$target" >/dev/null 2>&1 || {
  echo "installer cleared the quarantine marker instead of only reporting it" >&2
  exit 1
}
xattr -p com.example.keep "$target" >/dev/null 2>&1 || {
  echo "installer removed an unrelated extended attribute" >&2
  exit 1
}

# Case 4: the printed command must be safely copyable even for shell metacharacters,
# and its narrow remediation must preserve unrelated xattrs.
/bin/sh -c "$remediation"
[ ! -e "$TMP/literal" ] || {
  echo "remediation command interpreted path metacharacters" >&2
  exit 1
}
xattr -p com.apple.quarantine "$target" >/dev/null 2>&1 && {
  echo "printed remediation command did not clear quarantine" >&2
  exit 1
}
xattr -p com.example.keep "$target" >/dev/null 2>&1 || {
  echo "narrow quarantine remediation removed an unrelated extended attribute" >&2
  exit 1
}

echo "kuai quarantine advisory tests passed"
