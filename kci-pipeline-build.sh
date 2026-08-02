#!/bin/bash
# 天琴（KDev）单目标构建和签名入口。
set -euo pipefail

SIGN="${1:-true}"
NOTARIZE="${2:-true}"
for value in "$SIGN" "$NOTARIZE"; do
  case "$value" in
    true|false) ;;
    *) echo "kuai: SIGN and NOTARIZE must be true or false" >&2; exit 1 ;;
  esac
done

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
build_script="$root/build.sh"
[ -f "$build_script" ] && [ ! -L "$build_script" ] && [ -x "$build_script" ] || {
  echo "kuai: executable project build.sh is required" >&2
  exit 1
}

: "${UPLOAD_PLATFORM:?kuai: UPLOAD_PLATFORM not set - check pipeline template}"
: "${UPLOAD_ARCH:?kuai: UPLOAD_ARCH not set - check pipeline template}"
: "${UPLOAD_PACKAGE_VERSION:?kuai: UPLOAD_PACKAGE_VERSION not set - release version is required}"

case "$UPLOAD_ARCH" in
  x64|x86_64|amd64) goarch=amd64 ;;
  arm64|aarch64) goarch=arm64 ;;
  *) echo "kuai: unsupported UPLOAD_ARCH: $UPLOAD_ARCH" >&2; exit 1 ;;
esac
case "$UPLOAD_PLATFORM" in
  darwin) goos=darwin ;;
  win32) goos=windows ;;
  linux) goos=linux ;;
  *) echo "kuai: unsupported UPLOAD_PLATFORM: $UPLOAD_PLATFORM" >&2; exit 1 ;;
esac

version=$UPLOAD_PACKAGE_VERSION
case "$version" in
  ''|*[!A-Za-z0-9._+-]*) echo "kuai: invalid build version" >&2; exit 1 ;;
esac

required_go_version=go1.26.5
set -- $(go version)
[ "${3:-}" = "$required_go_version" ] || {
  echo "kuai: release build requires $required_go_version" >&2
  exit 1
}

dist="$root/dist"
if [ -L "$dist" ] || { [ -e "$dist" ] && [ ! -d "$dist" ]; }; then
  echo "kuai: dist must be a real directory" >&2
  exit 1
fi
mkdir -p "$dist"

stage=$(mktemp -d "$root/.kci.stage.XXXXXX")
cleanup() { rm -rf "$stage"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

suffix=
[ "$goos" != windows ] || suffix=.exe
artifact_name="kuai-$goos-$goarch$suffix"
artifact="$stage/$artifact_name"

KUAI_BUILD_OUTPUT="$artifact" KUAI_VERSION="$version" \
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
  "$build_script"
[ -f "$artifact" ] && [ ! -L "$artifact" ] || {
  echo "kuai: build produced no regular artifact" >&2
  exit 1
}

if [ "$SIGN" = true ] && [ "$goos" = darwin ]; then
  signing_identity=${APPLE_SIGNING_IDENTITY:-Developer ID Application: Qrite Technology Limited (72LM77TJSN)}
  codesign --force --timestamp --options runtime --sign "$signing_identity" "$artifact"
  codesign --verify --strict --verbose=2 "$artifact"

  if [ "$NOTARIZE" = true ]; then
    : "${APPLE_NOTARY_PROFILE:?kuai: APPLE_NOTARY_PROFILE not set}"
    archive="$stage/$artifact_name.zip"
    ditto -c -k --keepParent "$artifact" "$archive"
    xcrun notarytool submit "$archive" \
      --keychain-profile "$APPLE_NOTARY_PROFILE" --wait
    rm -f "$archive"
    # A raw Mach-O is not a stapler target. notarytool acceptance is retained
    # in Apple's service; codesign verification above protects local bytes.
  fi
elif [ "$SIGN" = true ] && [ "$goos" = windows ]; then
  : "${WINDOWS_SIGNING_PUBLISHER:?kuai: WINDOWS_SIGNING_PUBLISHER not set}"
  command -v jq >/dev/null 2>&1 || {
    echo "kuai: jq is required for Windows signing" >&2
    exit 1
  }
  command -v signtool >/dev/null 2>&1 || {
    echo "kuai: signtool is required for Windows signature verification" >&2
    exit 1
  }

  sign_server=${WINDOWS_SIGNING_SERVER:-https://win32-codesign-server.corp.kuaishou.com}
  create_response=$(curl --proto '=https' -fsS -X POST "$sign_server/api/create" \
    -F "ruleName=default" -F "files=@$artifact")
  task_id=$(printf %s "$create_response" | jq -r '.data.task.id')
  case "$task_id" in
    ''|null|*[!0-9]*) echo "kuai: signing task creation failed" >&2; exit 1 ;;
  esac

  deadline=$((SECONDS + 900))
  while :; do
    [ "$SECONDS" -lt "$deadline" ] || {
      echo "kuai: signing timed out after 900s (task $task_id)" >&2
      exit 1
    }
    task_status=$(curl --proto '=https' -fsS -X POST "$sign_server/api/status" \
      -H "Content-Type: application/json" -d "{\"id\": $task_id}" |
      jq -r '.data.task.status')
    case "$task_status" in
      completed) break ;;
      pending|running) sleep 5 ;;
      failed) echo "kuai: signing failed (task $task_id)" >&2; exit 1 ;;
      *) echo "kuai: unknown signing status: $task_status" >&2; exit 1 ;;
    esac
  done

  signed="$stage/$artifact_name.signed"
  curl --proto '=https' -fsS -o "$signed" \
    "$sign_server/api/download?id=$task_id&file=0"
  [ -s "$signed" ] && [ ! -L "$signed" ] || {
    echo "kuai: signed artifact download was empty or invalid" >&2
    exit 1
  }
  verification=$(signtool verify /pa /all /v "$signed" 2>&1) || {
    printf '%s\n' "$verification" >&2
    echo "kuai: Authenticode verification failed" >&2
    exit 1
  }
  printf '%s\n' "$verification" | grep -F -- "$WINDOWS_SIGNING_PUBLISHER" >/dev/null || {
    echo "kuai: signed artifact publisher did not match expectation" >&2
    exit 1
  }
  mv "$signed" "$artifact"
fi

(
  cd "$stage"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$artifact_name" >"$artifact_name.sha256"
  else
    shasum -a 256 "$artifact_name" >"$artifact_name.sha256"
  fi
)

# Re-check immediately before publishing from the isolated stage. mv replaces
# an existing file or symlink entry; it never follows an artifact symlink.
if [ -L "$dist" ] || [ ! -d "$dist" ]; then
  echo "kuai: dist changed during build" >&2
  exit 1
fi
mv "$artifact" "$dist/$artifact_name"
mv "$stage/$artifact_name.sha256" "$dist/$artifact_name.sha256"

echo "kuai: built $dist/$artifact_name (version $version, sign=$SIGN, notarize=$NOTARIZE)"
