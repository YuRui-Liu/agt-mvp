#!/bin/sh
set -eu

SOURCE_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/kuai-build-test.XXXXXX")
trap 'rm -rf "$TEST_ROOT"' EXIT HUP INT TERM
mkdir -p "$TEST_ROOT/repo/scripts" "$TEST_ROOT/repo/cmd/kuai" "$TEST_ROOT/fakebin"
cp "$SOURCE_ROOT/scripts/build-kuai-release.sh" "$TEST_ROOT/repo/scripts/"

cat >"$TEST_ROOT/fakebin/go" <<'EOF'
#!/bin/sh
if [ "$1" = version ]; then
  echo "go version go1.26.5 test/fake"
  exit 0
fi
[ "$1" = build ] || exit 2
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then output=$2; shift 2; else shift; fi
done
if [ "${FAIL_BUILD_FOR:-}" = "$GOOS-$GOARCH" ]; then exit 9; fi
printf 'stable kuai %s/%s\n' "$GOOS" "$GOARCH" >"$output"
EOF
chmod +x "$TEST_ROOT/fakebin/go"

run_build() {
  PATH="$TEST_ROOT/fakebin:/bin:/usr/bin" \
    FAIL_BUILD_FOR="${FAIL_BUILD_FOR:-}" \
    "$TEST_ROOT/repo/scripts/build-kuai-release.sh"
}

mkdir "$TEST_ROOT/symlink-target"
printf 'keep\n' >"$TEST_ROOT/symlink-target/marker"
ln -s "$TEST_ROOT/symlink-target" "$TEST_ROOT/repo/dist"
run_build >/dev/null 2>&1 && {
  echo "dist symlink unexpectedly accepted" >&2
  exit 1
}
grep -qx keep "$TEST_ROOT/symlink-target/marker"
rm "$TEST_ROOT/repo/dist"

mkdir "$TEST_ROOT/repo/dist"
printf 'old\n' >"$TEST_ROOT/repo/dist/marker"
FAIL_BUILD_FOR=linux-arm64 run_build >/dev/null 2>&1 && {
  echo "failed build unexpectedly succeeded" >&2
  exit 1
}
grep -qx old "$TEST_ROOT/repo/dist/marker"
FAIL_BUILD_FOR=
export FAIL_BUILD_FOR

run_build >/dev/null
first=$(sha256sum "$TEST_ROOT/repo/dist/SHA256SUMS" | awk '{print $1}')
run_build >/dev/null
second=$(sha256sum "$TEST_ROOT/repo/dist/SHA256SUMS" | awk '{print $1}')
[ "$first" = "$second" ]
[ "$(find "$TEST_ROOT/repo/dist" -maxdepth 1 -type f | wc -l | tr -d ' ')" -eq 7 ]
[ "$(wc -l <"$TEST_ROOT/repo/dist/SHA256SUMS" | tr -d ' ')" -eq 6 ]

echo "kuai release build tests passed"
