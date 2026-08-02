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

case "$goos:$SIGN:$NOTARIZE" in
  darwin:true:true|darwin:true:false|darwin:false:false|windows:true:false|windows:false:false|linux:false:false) ;;
  darwin:false:true) echo "kuai: macOS notarization requires signing" >&2; exit 1 ;;
  windows:*:true) echo "kuai: Windows does not support NOTARIZE=true" >&2; exit 1 ;;
  linux:true:*) echo "kuai: Linux does not support SIGN=true" >&2; exit 1 ;;
  linux:*:true) echo "kuai: Linux does not support NOTARIZE=true" >&2; exit 1 ;;
  *) echo "kuai: invalid signing policy" >&2; exit 1 ;;
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

suffix=
[ "$goos" != windows ] || suffix=.exe
artifact_name="kuai-$goos-$goarch$suffix"
targets="$dist/targets"
if [ -L "$targets" ] || { [ -e "$targets" ] && [ ! -d "$targets" ]; }; then
  echo "kuai: dist/targets must be a real directory" >&2
  exit 1
fi
mkdir -p "$targets"

target_dir="$targets/$artifact_name"
[ ! -e "$target_dir" ] && [ ! -L "$target_dir" ] || {
  echo "kuai: immutable target pair already exists" >&2
  exit 1
}
stage=$(mktemp -d "$targets/.$artifact_name.stage.XXXXXX")
lock="$targets/.$artifact_name.lock"
lock_acquired=0
cleanup() {
  [ -z "$stage" ] || rm -rf "$stage"
  [ "$lock_acquired" -eq 0 ] || rm -f "$lock"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

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
    command -v jq >/dev/null 2>&1 || {
      echo "kuai: jq is required for notarization response validation" >&2
      exit 1
    }
    notary_timeout=${APPLE_NOTARY_TIMEOUT:-20m}
    notary_timeout_value=${notary_timeout%?}
    notary_timeout_unit=${notary_timeout#"$notary_timeout_value"}
    case "$notary_timeout_value" in
      ''|*[!0-9]*)
        echo "kuai: APPLE_NOTARY_TIMEOUT must be a positive duration such as 20m" >&2
        exit 1
        ;;
    esac
    case "$notary_timeout_value" in
      *[1-9]*) ;;
      *) echo "kuai: APPLE_NOTARY_TIMEOUT must be greater than zero" >&2; exit 1 ;;
    esac
    case "$notary_timeout_unit" in
      s|m|h) ;;
      *) echo "kuai: APPLE_NOTARY_TIMEOUT must end in s, m, or h" >&2; exit 1 ;;
    esac
    archive="$stage/$artifact_name.zip"
    ditto -c -k --keepParent "$artifact" "$archive"
    notary_response=$(xcrun notarytool submit "$archive" \
      --keychain-profile "$APPLE_NOTARY_PROFILE" --wait \
      --timeout "$notary_timeout" --output-format json)
    notary_status=$(printf '%s' "$notary_response" | jq -er \
      '.status | select(type == "string")') || {
      echo "kuai: notarization returned no unique status" >&2
      exit 1
    }
    [ "$notary_status" = Accepted ] || {
      echo "kuai: notarization was not accepted: $notary_status" >&2
      exit 1
    }
    rm -f "$archive"
    codesign --verify --strict --verbose=2 "$artifact"
  fi
elif [ "$SIGN" = true ] && [ "$goos" = windows ]; then
  : "${WINDOWS_SIGNING_PUBLISHER:?kuai: WINDOWS_SIGNING_PUBLISHER not set}"
  normalize_subject() {
    printf '%s' "$1" | tr '\r\n' '  ' | awk '{$1=$1; print}'
  }
  expected_publisher=$(normalize_subject "$WINDOWS_SIGNING_PUBLISHER")
  [ -n "$expected_publisher" ] || {
    echo "kuai: WINDOWS_SIGNING_PUBLISHER must not be blank" >&2
    exit 1
  }
  command -v jq >/dev/null 2>&1 || {
    echo "kuai: jq is required for Windows signing" >&2
    exit 1
  }
  command -v signtool >/dev/null 2>&1 || {
    echo "kuai: signtool is required for Windows signature verification" >&2
    exit 1
  }
  powershell_bin=
  for candidate in powershell.exe pwsh powershell; do
    if command -v "$candidate" >/dev/null 2>&1; then
      powershell_bin=$candidate
      break
    fi
  done
  [ -n "$powershell_bin" ] || {
    echo "kuai: PowerShell is required for signer subject verification" >&2
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
  signed_for_powershell=$signed
  if command -v cygpath >/dev/null 2>&1; then
    signed_for_powershell=$(cygpath -w "$signed")
  fi
  publisher_raw=$(KUAI_SIGNED_PATH="$signed_for_powershell" "$powershell_bin" \
    -NoProfile -NonInteractive -Command \
    '$signature = Get-AuthenticodeSignature -LiteralPath $env:KUAI_SIGNED_PATH; if ($signature.Status -ne "Valid" -or $null -eq $signature.SignerCertificate) { exit 2 }; [Console]::Out.Write($signature.SignerCertificate.Subject)') || {
    echo "kuai: could not read a valid Authenticode signer certificate" >&2
    exit 1
  }
  publisher=$(normalize_subject "$publisher_raw")
  [ -n "$publisher" ] && [ "$publisher" = "$expected_publisher" ] || {
    echo "kuai: leaf signer subject did not exactly match expectation" >&2
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

# The artifact and checksum are one transaction unit. Replacing their parent
# directory prevents a reader from observing a mixed old/new pair.
if [ -L "$dist" ] || [ ! -d "$dist" ] || [ -L "$targets" ] || [ ! -d "$targets" ]; then
  echo "kuai: release directories changed during build" >&2
  exit 1
fi

if (set -C; : >"$lock") 2>/dev/null; then
  lock_acquired=1
else
  echo "kuai: another publication owns the target lock" >&2
  exit 1
fi
[ ! -e "$target_dir" ] && [ ! -L "$target_dir" ] || {
  echo "kuai: immutable target pair already exists" >&2
  exit 1
}

mv "$stage" "$target_dir"
stage=

echo "kuai: built $target_dir/$artifact_name (version $version, sign=$SIGN, notarize=$NOTARIZE)"
