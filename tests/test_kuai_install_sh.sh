#!/usr/bin/env bash
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=${TMPDIR:-/tmp}/kuai-install-test.$$
trap 'rm -rf "$TMP"' EXIT HUP INT TERM
mkdir -p "$TMP/fakebin" "$TMP/home"

cat >"$TMP/fakebin/uname" <<'EOF'
#!/bin/sh
case "$1" in
  -s) printf '%s\n' "${FAKE_OS:-Linux}" ;;
  -m) printf '%s\n' "${FAKE_ARCH:-x86_64}" ;;
  *) exit 2 ;;
esac
EOF

cat >"$TMP/fakebin/curl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$CURL_ARGS_LOG"
output=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output=$2; shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
printf '%s\n' "$url" >>"$DOWNLOAD_LOG"
case "$url" in
  */SHA256SUMS)
    case "$url" in
      "$KUAI_RELEASE_URL"/*) prefix=kuai ;;
      *) exit 44 ;;
    esac
    os=${FAKE_OS_NAME:-linux}
    arch=${FAKE_ARCH_NAME:-amd64}
    printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  %s-%s-%s\n' "$prefix" "$os" "$arch" >"$output"
    ;;
  *)
    printf 'new:%s\n' "$url" >"$output"
    ;;
esac
EOF

cat >"$TMP/fakebin/sha256sum" <<'EOF'
#!/bin/sh
if [ "${FAIL_CHECKSUM_FOR:-}" != "" ] && printf '%s\n' "$1" | grep -q "$FAIL_CHECKSUM_FOR"; then
  printf 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  %s\n' "$1"
else
  printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  %s\n' "$1"
fi
EOF

cat >"$TMP/fakebin/shasum" <<'EOF'
#!/bin/sh
shift 2
exec sha256sum "$@"
EOF
chmod +x "$TMP/fakebin/"*

run_install() {
  HOME="$TMP/home" \
  PATH="$TMP/fakebin:/bin:/usr/bin" \
  DOWNLOAD_LOG="$TMP/download.log" \
  CURL_ARGS_LOG="$TMP/curl-args.log" \
  KUAI_RELEASE_URL="${KUAI_RELEASE_URL:-https://kuai.example/releases/v1}" \
  FAKE_OS="${FAKE_OS:-Linux}" FAKE_ARCH="${FAKE_ARCH:-x86_64}" \
  FAKE_OS_NAME="${FAKE_OS_NAME:-linux}" FAKE_ARCH_NAME="${FAKE_ARCH_NAME:-amd64}" \
  FAIL_CHECKSUM_FOR="${FAIL_CHECKSUM_FOR:-}" \
  KUAI_INSTALL_DRY_RUN="${KUAI_INSTALL_DRY_RUN:-}" \
  /bin/bash "$ROOT/install.sh"
}

: >"$TMP/download.log"
dry_output=$(KUAI_INSTALL_DRY_RUN=1 run_install)
printf '%s\n' "$dry_output" | grep -q 'kuai-linux-amd64'
[ ! -s "$TMP/download.log" ]
[ ! -e "$TMP/home/.local/bin/kuai" ]

: >"$TMP/download.log"
: >"$TMP/curl-args.log"
run_install
grep -q 'https://kuai.example/releases/v1/kuai-linux-amd64' "$TMP/download.log"
[ "$(wc -l <"$TMP/download.log" | tr -d ' ')" -eq 2 ]
grep -q '^new:https://kuai.example/' "$TMP/home/.local/bin/kuai"
[ ! -e "$TMP/home/.local/bin/agentsview" ]
grep -q -- "--proto =https --proto-redir =https" "$TMP/curl-args.log"

run_install
[ "$(grep -Fxc 'export PATH="$HOME/.local/bin:$PATH"' "$TMP/home/.profile")" -eq 1 ]

printf 'old-kuai\n' >"$TMP/home/.local/bin/kuai"
printf 'old-agentsview\n' >"$TMP/home/.local/bin/agentsview"
FAIL_CHECKSUM_FOR=kuai run_install >/dev/null 2>&1 && {
  echo "checksum failure unexpectedly succeeded" >&2
  exit 1
}
grep -qx 'old-kuai' "$TMP/home/.local/bin/kuai"
grep -qx 'old-agentsview' "$TMP/home/.local/bin/agentsview"

: >"$TMP/download.log"
FAKE_OS=Plan9 run_install >/dev/null 2>&1 && {
  echo "unsupported platform unexpectedly succeeded" >&2
  exit 1
}
[ ! -s "$TMP/download.log" ]

: >"$TMP/download.log"
KUAI_RELEASE_URL=http://kuai.example/releases/v1 run_install >/dev/null 2>&1 && {
  echo "insecure release URL unexpectedly succeeded" >&2
  exit 1
}
[ ! -s "$TMP/download.log" ]

for tuple in \
  'Darwin x86_64 darwin amd64' \
  'Darwin arm64 darwin arm64' \
  'Linux aarch64 linux arm64'
do
  set -- $tuple
  : >"$TMP/download.log"
  FAKE_OS=$1 FAKE_ARCH=$2 FAKE_OS_NAME=$3 FAKE_ARCH_NAME=$4 run_install
  grep -q "kuai-$3-$4" "$TMP/download.log"
  ! grep -q "agentsview-$3-$4" "$TMP/download.log"
done

printf 'old-kuai\n' >"$TMP/home/.local/bin/kuai"
printf 'old-agentsview\n' >"$TMP/home/.local/bin/agentsview"
printf 'original profile\n' >"$TMP/home/.profile"
chmod 400 "$TMP/home/.profile"
run_install >/dev/null 2>&1 && {
  echo "profile write failure unexpectedly succeeded" >&2
  exit 1
}
chmod 600 "$TMP/home/.profile"
grep -qx 'old-kuai' "$TMP/home/.local/bin/kuai"
grep -qx 'old-agentsview' "$TMP/home/.local/bin/agentsview"
grep -qx 'original profile' "$TMP/home/.profile"

echo "kuai install.sh tests passed"
