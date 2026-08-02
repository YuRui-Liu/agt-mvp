#!/bin/sh
set -eu

# Project build entrypoint. The release script supplies the target platform,
# architecture, version, and output path through environment variables.
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
output=${KUAI_BUILD_OUTPUT:-$root/kuai}
version=${KUAI_VERSION:-dev}

case "$version" in
  *[!A-Za-z0-9._+-]*)
    echo "kuai: invalid build version" >&2
    exit 1
    ;;
esac

mkdir -p "$(dirname -- "$output")"
CGO_ENABLED=${CGO_ENABLED:-0} \
GOOS=${GOOS:-$(go env GOOS)} \
GOARCH=${GOARCH:-$(go env GOARCH)} \
go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X main.version=$version" \
  -o "$output" "$root/cmd/kuai"
