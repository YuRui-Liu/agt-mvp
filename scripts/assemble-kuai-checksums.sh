#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
fragments=${1:-$root/dist}
output=${2:-$fragments/SHA256SUMS}

[ -d "$fragments" ] && [ ! -L "$fragments" ] || {
  echo "kuai: checksum fragments must be in a real directory" >&2
  exit 1
}
[ ! -L "$output" ] || {
  echo "kuai: checksum output must not be a symlink" >&2
  exit 1
}
output_dir=$(dirname -- "$output")
[ -d "$output_dir" ] && [ ! -L "$output_dir" ] || {
  echo "kuai: checksum output parent must be a real directory" >&2
  exit 1
}

expected='kuai-darwin-amd64
kuai-darwin-arm64
kuai-linux-amd64
kuai-linux-arm64
kuai-windows-amd64.exe
kuai-windows-arm64.exe'

fragment_count=$(find "$fragments" -mindepth 1 -maxdepth 1 -name '*.sha256' -print | wc -l | tr -d ' ')
[ "$fragment_count" -eq 6 ] || {
  echo "kuai: expected exactly six checksum fragments" >&2
  exit 1
}

manifest=$(mktemp "$output_dir/.SHA256SUMS.XXXXXX")
cleanup() { rm -f "$manifest"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

printf '%s\n' "$expected" | while IFS= read -r artifact; do
  fragment="$fragments/$artifact.sha256"
  [ -f "$fragment" ] && [ ! -L "$fragment" ] || {
    echo "kuai: missing regular checksum fragment for $artifact" >&2
    exit 1
  }
  [ "$(wc -l <"$fragment" | tr -d ' ')" -eq 1 ] || {
    echo "kuai: checksum fragment must contain exactly one line: $artifact" >&2
    exit 1
  }
  read -r digest filename extra <"$fragment" || {
    echo "kuai: unreadable checksum fragment: $artifact" >&2
    exit 1
  }
  [ -z "${extra:-}" ] && [ "$filename" = "$artifact" ] && [ "${#digest}" -eq 64 ] || {
    echo "kuai: malformed checksum fragment: $artifact" >&2
    exit 1
  }
  case "$digest" in
    *[!0-9A-Fa-f]*) echo "kuai: invalid SHA-256 digest: $artifact" >&2; exit 1 ;;
  esac
  printf '%s  %s\n' "$digest" "$artifact"
done | LC_ALL=C sort >"$manifest"

[ "$(wc -l <"$manifest" | tr -d ' ')" -eq 6 ] || {
  echo "kuai: checksum manifest validation failed" >&2
  exit 1
}
mv "$manifest" "$output"
trap - EXIT HUP INT TERM
echo "kuai: wrote $output"
