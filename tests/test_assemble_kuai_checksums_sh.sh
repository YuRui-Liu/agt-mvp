#!/bin/sh
set -eu

SOURCE_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/kuai-checksums-test.XXXXXX")
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM
mkdir -p "$TEST_ROOT/fragments"

artifacts='kuai-darwin-amd64 kuai-darwin-arm64 kuai-linux-amd64 kuai-linux-arm64 kuai-windows-amd64.exe kuai-windows-arm64.exe'
i=1
for artifact in $artifacts; do
  hash=$(printf '%064x' "$i")
  printf '%s  %s\n' "$hash" "$artifact" >"$TEST_ROOT/fragments/$artifact.sha256"
  i=$((i + 1))
done

"$SOURCE_ROOT/scripts/assemble-kuai-checksums.sh" \
  "$TEST_ROOT/fragments" "$TEST_ROOT/SHA256SUMS"
[ "$(wc -l <"$TEST_ROOT/SHA256SUMS" | tr -d ' ')" -eq 6 ]
[ "$(LC_ALL=C sort "$TEST_ROOT/SHA256SUMS" | cmp - "$TEST_ROOT/SHA256SUMS"; printf $?)" -eq 0 ]

assert_rejected() {
  "$SOURCE_ROOT/scripts/assemble-kuai-checksums.sh" \
    "$TEST_ROOT/fragments" "$TEST_ROOT/rejected" >/dev/null 2>&1 && {
    echo "$1 unexpectedly accepted" >&2
    exit 1
  }
  return 0
}

rm "$TEST_ROOT/fragments/kuai-linux-arm64.sha256"
assert_rejected "missing fragment"
printf '%064x  kuai-linux-arm64\n' 4 >"$TEST_ROOT/fragments/kuai-linux-arm64.sha256"

printf '%064x  stale\n' 7 >"$TEST_ROOT/fragments/stale.sha256"
assert_rejected "stale fragment"
rm "$TEST_ROOT/fragments/stale.sha256"

printf '%064x  kuai-linux-amd64\n%s\n' 3 duplicate \
  >"$TEST_ROOT/fragments/kuai-linux-amd64.sha256"
assert_rejected "multi-line fragment"
printf '%064x  kuai-linux-amd64\n' 3 >"$TEST_ROOT/fragments/kuai-linux-amd64.sha256"

printf 'xyz  kuai-linux-amd64\n' >"$TEST_ROOT/fragments/kuai-linux-amd64.sha256"
assert_rejected "invalid digest"

echo "checksum assembly tests passed"
