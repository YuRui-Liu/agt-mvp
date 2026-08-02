#!/bin/sh
set -eu

SOURCE_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/kuai-checksums-test.XXXXXX")
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM
mkdir -p "$TEST_ROOT/fragments"
mkdir "$TEST_ROOT/fakebin"
cat >"$TEST_ROOT/fakebin/ln" <<'EOF'
#!/bin/sh
if [ "${FAKE_LINK_RACE:-}" = directory ]; then
  mkdir "$2"
  exit 75
fi
exec /bin/ln "$@"
EOF
chmod +x "$TEST_ROOT/fakebin/ln"
export PATH="$TEST_ROOT/fakebin:/usr/bin:/bin"

artifacts='kuai-darwin-amd64 kuai-darwin-arm64 kuai-linux-amd64 kuai-linux-arm64 kuai-windows-amd64.exe kuai-windows-arm64.exe'
i=1
for artifact in $artifacts; do
  printf 'artifact %s\n' "$artifact" >"$TEST_ROOT/fragments/$artifact"
  hash=$(shasum -a 256 "$TEST_ROOT/fragments/$artifact" | awk '{print $1}')
  printf '%s  %s\n' "$hash" "$artifact" >"$TEST_ROOT/fragments/$artifact.sha256"
  i=$((i + 1))
done

"$SOURCE_ROOT/scripts/assemble-kuai-checksums.sh" \
  "$TEST_ROOT/fragments" "$TEST_ROOT/fragments/SHA256SUMS"
[ "$(wc -l <"$TEST_ROOT/fragments/SHA256SUMS" | tr -d ' ')" -eq 6 ]
[ "$(LC_ALL=C sort "$TEST_ROOT/fragments/SHA256SUMS" | cmp - "$TEST_ROOT/fragments/SHA256SUMS"; printf $?)" -eq 0 ]
rm "$TEST_ROOT/fragments/SHA256SUMS"

assert_rejected() {
  "$SOURCE_ROOT/scripts/assemble-kuai-checksums.sh" \
    "$TEST_ROOT/fragments" "$TEST_ROOT/fragments/SHA256SUMS" >/dev/null 2>&1 && {
    echo "$1 unexpectedly accepted" >&2
    exit 1
  }
  return 0
}

printf 'notes\n' >"$TEST_ROOT/fragments/notes.txt"
assert_rejected "extra notes file"
rm "$TEST_ROOT/fragments/notes.txt"

printf 'extra\n' >"$TEST_ROOT/fragments/kuai-linux-386"
assert_rejected "extra artifact"
rm "$TEST_ROOT/fragments/kuai-linux-386"

mkdir "$TEST_ROOT/fragments/extra-directory"
assert_rejected "extra directory"
rmdir "$TEST_ROOT/fragments/extra-directory"

printf 'tampered\n' >"$TEST_ROOT/fragments/kuai-linux-amd64"
assert_rejected "artifact digest mismatch"
printf 'artifact kuai-linux-amd64\n' >"$TEST_ROOT/fragments/kuai-linux-amd64"

mv "$TEST_ROOT/fragments/kuai-linux-arm64" "$TEST_ROOT/real-linux-arm64"
ln -s "$TEST_ROOT/real-linux-arm64" "$TEST_ROOT/fragments/kuai-linux-arm64"
assert_rejected "symlinked artifact"
rm "$TEST_ROOT/fragments/kuai-linux-arm64"
mv "$TEST_ROOT/real-linux-arm64" "$TEST_ROOT/fragments/kuai-linux-arm64"

mv "$TEST_ROOT/fragments/kuai-darwin-amd64" "$TEST_ROOT/real-darwin-amd64"
mkdir "$TEST_ROOT/fragments/kuai-darwin-amd64"
assert_rejected "directory artifact"
rmdir "$TEST_ROOT/fragments/kuai-darwin-amd64"
mv "$TEST_ROOT/real-darwin-amd64" "$TEST_ROOT/fragments/kuai-darwin-amd64"

rm "$TEST_ROOT/fragments/kuai-linux-arm64.sha256"
assert_rejected "missing fragment"
hash=$(shasum -a 256 "$TEST_ROOT/fragments/kuai-linux-arm64" | awk '{print $1}')
printf '%s  kuai-linux-arm64\n' "$hash" >"$TEST_ROOT/fragments/kuai-linux-arm64.sha256"

printf '%064d  stale\n' 7 >"$TEST_ROOT/fragments/stale.sha256"
assert_rejected "stale fragment"
rm "$TEST_ROOT/fragments/stale.sha256"

hash=$(shasum -a 256 "$TEST_ROOT/fragments/kuai-linux-amd64" | awk '{print $1}')
printf '%s  kuai-linux-amd64\n%s\n' "$hash" duplicate \
  >"$TEST_ROOT/fragments/kuai-linux-amd64.sha256"
assert_rejected "multi-line fragment"
printf '%s  kuai-linux-amd64\n' "$hash" >"$TEST_ROOT/fragments/kuai-linux-amd64.sha256"

printf 'xyz  kuai-linux-amd64\n' >"$TEST_ROOT/fragments/kuai-linux-amd64.sha256"
assert_rejected "invalid digest"

printf '%s  kuai-linux-amd64\n' "$hash" >"$TEST_ROOT/fragments/kuai-linux-amd64.sha256"
printf 'preexisting\n' >"$TEST_ROOT/fragments/SHA256SUMS"
assert_rejected "existing checksum output"
rm "$TEST_ROOT/fragments/SHA256SUMS"

mkdir "$TEST_ROOT/fragments/SHA256SUMS"
assert_rejected "directory checksum output"
rmdir "$TEST_ROOT/fragments/SHA256SUMS"
ln -s "$TEST_ROOT/external-manifest" "$TEST_ROOT/fragments/SHA256SUMS"
assert_rejected "symlinked checksum output"
rm "$TEST_ROOT/fragments/SHA256SUMS"

FAKE_LINK_RACE=directory \
  "$SOURCE_ROOT/scripts/assemble-kuai-checksums.sh" \
  "$TEST_ROOT/fragments" "$TEST_ROOT/fragments/SHA256SUMS" >/dev/null 2>&1 && {
  echo "checksum output race unexpectedly succeeded" >&2
  exit 1
}
[ -d "$TEST_ROOT/fragments/SHA256SUMS" ]

echo "checksum assembly tests passed"
