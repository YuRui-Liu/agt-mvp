#!/bin/sh
set -eu

# Upgrade point: update this constant only with an intentional release toolchain change.
required_go_version=go1.26.5
set -- $(go version)
[ "${3:-}" = "$required_go_version" ] || {
  echo "kuai: release build requires $required_go_version" >&2
  exit 1
}

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=${KUAI_VERSION:-}
[ -n "$version" ] || {
  version=$(git -C "$root" describe --tags --always --dirty 2>/dev/null || printf dev)
}
case "$version" in
  *[!A-Za-z0-9._+-]*) echo "kuai: invalid release version" >&2; exit 1 ;;
esac
dist="$root/dist"
if [ -L "$dist" ] || { [ -e "$dist" ] && [ ! -d "$dist" ]; }; then
  echo "kuai: dist must be a real directory" >&2
  exit 1
fi

stage=$(mktemp -d "$root/.dist.stage.XXXXXX")
backup=
old_moved=0
cleanup() {
  [ -z "$stage" ] || rm -rf "$stage"
  if [ "$old_moved" -eq 1 ] && [ -n "$backup" ] && [ -d "$backup" ] && [ ! -e "$dist" ]; then
    mv "$backup" "$dist" || true
  fi
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

for target in \
  darwin/amd64 \
  darwin/arm64 \
  linux/amd64 \
  linux/arm64 \
  windows/amd64 \
  windows/arm64
do
  platform=${target%/*}
  architecture=${target#*/}
  suffix=
  [ "$platform" != windows ] || suffix=.exe
  output="$stage/kuai-$platform-$architecture$suffix"
  (
    cd "$root"
    CGO_ENABLED=0 GOOS="$platform" GOARCH="$architecture" \
      go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$version" \
      -o "$output" ./cmd/kuai
  )
done

for artifact in \
  kuai-darwin-amd64 \
  kuai-darwin-arm64 \
  kuai-linux-amd64 \
  kuai-linux-arm64 \
  kuai-windows-amd64.exe \
  kuai-windows-arm64.exe
do
  [ -f "$stage/$artifact" ] && [ ! -L "$stage/$artifact" ] || {
    echo "kuai: release artifact validation failed" >&2
    exit 1
  }
done
[ "$(find "$stage" -mindepth 1 -maxdepth 1 -type f | wc -l | tr -d ' ')" -eq 6 ] || {
  echo "kuai: unexpected release artifact" >&2
  exit 1
}
[ -z "$(find "$stage" -mindepth 1 -maxdepth 1 -type l -print)" ] || {
  echo "kuai: symlinked release artifact" >&2
  exit 1
}

(
  cd "$stage"
  if command -v sha256sum >/dev/null 2>&1; then
    for file in \
      kuai-darwin-amd64 \
      kuai-darwin-arm64 \
      kuai-linux-amd64 \
      kuai-linux-arm64 \
      kuai-windows-amd64.exe \
      kuai-windows-arm64.exe
    do
      sha256sum "$file"
    done >SHA256SUMS
  else
    for file in \
      kuai-darwin-amd64 \
      kuai-darwin-arm64 \
      kuai-linux-amd64 \
      kuai-linux-arm64 \
      kuai-windows-amd64.exe \
      kuai-windows-arm64.exe
    do
      shasum -a 256 "$file"
    done >SHA256SUMS
  fi
)
[ -f "$stage/SHA256SUMS" ] && [ ! -L "$stage/SHA256SUMS" ] &&
  [ "$(wc -l <"$stage/SHA256SUMS" | tr -d ' ')" -eq 6 ] &&
  [ "$(find "$stage" -mindepth 1 -maxdepth 1 -type f | wc -l | tr -d ' ')" -eq 7 ] || {
  echo "kuai: checksum manifest validation failed" >&2
  exit 1
}

backup=$(mktemp -d "$root/.dist.backup.XXXXXX")
rmdir "$backup"
if [ -d "$dist" ]; then
  mv "$dist" "$backup"
  old_moved=1
fi
if ! mv "$stage" "$dist"; then
  if [ "$old_moved" -eq 1 ] && mv "$backup" "$dist"; then
    old_moved=0
  fi
  exit 1
fi
stage=
if [ "$old_moved" -eq 1 ]; then
  rm -rf "$backup"
  old_moved=0
fi

echo "Built kuai release artifacts in $dist"
