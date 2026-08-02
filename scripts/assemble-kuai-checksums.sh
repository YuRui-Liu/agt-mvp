#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
fragments=${1:-$root/dist}
output=${2:-$fragments/SHA256SUMS}

[ -d "$fragments" ] && [ ! -L "$fragments" ] || {
  echo "kuai: release inputs must be in a real directory" >&2
  exit 1
}
command -v python3 >/dev/null 2>&1 || {
  echo "kuai: python3 is required for atomic checksum publication" >&2
  exit 1
}
input_dir=$(CDPATH= cd -- "$fragments" && pwd -P)
output_dir=$(dirname -- "$output")
[ -d "$output_dir" ] && [ ! -L "$output_dir" ] || {
  echo "kuai: checksum output parent must be a real directory" >&2
  exit 1
}
output_dir_physical=$(CDPATH= cd -- "$output_dir" && pwd -P)
[ "$output_dir_physical" = "$input_dir" ] && [ "$(basename -- "$output")" = SHA256SUMS ] || {
  echo "kuai: checksum output must be SHA256SUMS in the input directory" >&2
  exit 1
}
output="$input_dir/SHA256SUMS"
[ ! -e "$output" ] && [ ! -L "$output" ] || {
  echo "kuai: checksum output already exists" >&2
  exit 1
}

expected='kuai-darwin-amd64
kuai-darwin-arm64
kuai-linux-amd64
kuai-linux-arm64
kuai-windows-amd64.exe
kuai-windows-arm64.exe'

entry_count=$(find "$input_dir" -mindepth 1 -maxdepth 1 -print | wc -l | tr -d ' ')
[ "$entry_count" -eq 12 ] || {
  echo "kuai: expected exactly six artifacts and six checksum fragments" >&2
  exit 1
}

manifest=$(mktemp "$input_dir/.SHA256SUMS.XXXXXX")
cleanup() { rm -f "$manifest"; }
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

printf '%s\n' "$expected" | while IFS= read -r artifact; do
  artifact_path="$input_dir/$artifact"
  fragment="$input_dir/$artifact.sha256"
  [ -f "$artifact_path" ] && [ ! -L "$artifact_path" ] || {
    echo "kuai: missing regular release artifact: $artifact" >&2
    exit 1
  }
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
    *[!0-9a-f]*) echo "kuai: invalid canonical SHA-256 digest: $artifact" >&2; exit 1 ;;
  esac
  fragment_line=$(cat "$fragment")
  [ "$fragment_line" = "$digest  $artifact" ] || {
    echo "kuai: non-canonical checksum fragment: $artifact" >&2
    exit 1
  }
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$artifact_path" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "$artifact_path" | awk '{print $1}')
  fi
  [ "$actual" = "$digest" ] || {
    echo "kuai: checksum does not match artifact: $artifact" >&2
    exit 1
  }
  printf '%s\n' "$fragment_line"
done | LC_ALL=C sort >"$manifest"

[ "$(wc -l <"$manifest" | tr -d ' ')" -eq 6 ] || {
  echo "kuai: checksum manifest validation failed" >&2
  exit 1
}

# Revalidate the fresh directory, excluding our private manifest inode.
[ -d "$input_dir" ] && [ ! -L "$input_dir" ] || {
  echo "kuai: checksum input directory changed during assembly" >&2
  exit 1
}
manifest_name=$(basename -- "$manifest")
entry_count=$(find "$input_dir" -mindepth 1 -maxdepth 1 ! -name "$manifest_name" -print | wc -l | tr -d ' ')
[ "$entry_count" -eq 12 ] || {
  echo "kuai: release input directory changed during assembly" >&2
  exit 1
}
case "${KUAI_CHECKSUM_TEST_LINK_RACE:-}" in
  '') ;;
  directory) mkdir "$output" ;;
  symlink)
    : "${KUAI_CHECKSUM_TEST_RACE_TARGET:?missing test race target}"
    ln -s "$KUAI_CHECKSUM_TEST_RACE_TARGET" "$output"
    ;;
  *) echo "kuai: invalid checksum link race test value" >&2; exit 1 ;;
esac

if ! KUAI_LINK_SOURCE="$manifest" KUAI_LINK_TARGET="$output" \
  python3 -c 'import os; os.link(os.environ["KUAI_LINK_SOURCE"], os.environ["KUAI_LINK_TARGET"], follow_symlinks=False)'
then
  echo "kuai: atomic SHA256SUMS creation failed" >&2
  exit 1
fi
rm -f "$manifest"
manifest=
trap - EXIT HUP INT TERM
echo "kuai: wrote $output"
