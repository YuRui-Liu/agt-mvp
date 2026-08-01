#!/bin/sh
set -eu

KUAI_RELEASE_URL=${KUAI_RELEASE_URL:-https://github.com/YuRui-Liu/agt-mvp/releases/latest/download}

validate_release_url() {
  case "$1" in
    https://*) ;;
    *) echo "kuai: release URL must use HTTPS" >&2; return 1 ;;
  esac
  case "$1" in
    *'@'*|*[[:space:]]*) echo "kuai: release URL is invalid" >&2; return 1 ;;
  esac
  if printf %s "$1" | LC_ALL=C grep '[[:cntrl:]]' >/dev/null 2>&1; then
    echo "kuai: release URL is invalid" >&2
    return 1
  fi
}

validate_release_url "$KUAI_RELEASE_URL"

case "$(uname -s)" in
  Darwin) platform=darwin ;;
  Linux) platform=linux ;;
  *) echo "kuai: unsupported operating system" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) architecture=amd64 ;;
  arm64|aarch64) architecture=arm64 ;;
  *) echo "kuai: unsupported architecture" >&2; exit 1 ;;
esac

kuai_name="kuai-$platform-$architecture"
if [ "${KUAI_INSTALL_DRY_RUN:-0}" = 1 ]; then
  echo "Would download and verify:"
  echo "  $KUAI_RELEASE_URL/SHA256SUMS"
  echo "  $KUAI_RELEASE_URL/$kuai_name"
  echo "Would atomically install kuai to $HOME/.local/bin/kuai"
  exit 0
fi
stage=$(mktemp -d "${TMPDIR:-/tmp}/kuai-install.XXXXXX")
install_dir="$HOME/.local/bin"
profile="$HOME/.profile"
kuai_temp=
kuai_backup=
profile_backup="$stage/profile.old"
transaction_started=0

cleanup() {
  [ "$transaction_started" -eq 0 ] || rollback
  [ -z "$kuai_temp" ] || rm -f "$kuai_temp"
  rm -rf "$stage"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

download() {
  curl --proto '=https' --proto-redir '=https' -fsSL -o "$2" "$1"
}

download "$KUAI_RELEASE_URL/SHA256SUMS" "$stage/kuai-SHA256SUMS"
download "$KUAI_RELEASE_URL/$kuai_name" "$stage/$kuai_name"

expected_checksum() {
  awk -v target="$2" '
    $2 == target && $1 ~ /^[0-9A-Fa-f]+$/ && length($1) == 64 { checksum = $1; matches++ }
    END { if (matches != 1) exit 1; print checksum }
  ' "$1"
}
actual_checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "kuai: no SHA-256 tool available" >&2
    return 1
  fi
}
verify() {
  expected=$(expected_checksum "$1" "$2") || {
    echo "kuai: trusted checksum entry missing or ambiguous" >&2
    return 1
  }
  actual=$(actual_checksum "$3") || return 1
  [ "$(printf %s "$expected" | tr A-F a-f)" = "$(printf %s "$actual" | tr A-F a-f)" ] || {
    echo "kuai: checksum verification failed" >&2
    return 1
  }
}
verify "$stage/kuai-SHA256SUMS" "$kuai_name" "$stage/$kuai_name"

mkdir -p "$install_dir"
kuai_temp=$(mktemp "$install_dir/.kuai.new.XXXXXX")
kuai_backup=$(mktemp "$install_dir/.kuai.old.XXXXXX")
rm -f "$kuai_backup"
cp "$stage/$kuai_name" "$kuai_temp"
chmod 755 "$kuai_temp"

profile_existed=0
if [ -e "$profile" ]; then
  cp -p "$profile" "$profile_backup"
  profile_existed=1
fi
had_kuai=0
installed_kuai=0
transaction_started=1

rollback() {
  [ "$installed_kuai" -eq 0 ] || rm -f "$install_dir/kuai"
  [ "$had_kuai" -eq 0 ] || mv "$kuai_backup" "$install_dir/kuai" || true
  if [ "$profile_existed" -eq 1 ]; then
    cp -p "$profile_backup" "$profile" || true
  else
    rm -f "$profile"
  fi
  transaction_started=0
}

if [ -e "$install_dir/kuai" ]; then
  mv "$install_dir/kuai" "$kuai_backup"
  had_kuai=1
fi
if ! mv "$kuai_temp" "$install_dir/kuai"; then rollback; exit 1; fi
kuai_temp=
installed_kuai=1

path_line='export PATH="$HOME/.local/bin:$PATH"'
if [ ! -f "$profile" ] || ! grep -Fqx "$path_line" "$profile"; then
  if ! printf '\n%s\n' "$path_line" >>"$profile"; then
    rollback
    exit 1
  fi
fi

rm -f "$kuai_backup"
transaction_started=0
echo "Installed kuai to $install_dir"
