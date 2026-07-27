#!/bin/bash
# avscore - local AI coding behavior profile launcher (macOS/Linux)

set -euo pipefail

DEFAULT_RELEASE_URL="https://git.corp.kuaishou.com/ks-ep/ea-zonghe/zonghe-experimental/agentsview/releases/download"
RELEASE_BASE_URL="${AVSCORE_RELEASE_URL:-$DEFAULT_RELEASE_URL}"
VERSION="${AVSCORE_VERSION:-latest}"
OUTPUT_DIR="${AVSCORE_OUTPUT_DIR:-$HOME/.agentsview/reports}"
AVSCORE_BINARY_PATH="${AVSCORE_BINARY_PATH:-}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
info()  { printf '%b\n' "${GREEN}$1${NC}" >&2; }
warn()  { printf '%b\n' "${YELLOW}$1${NC}" >&2; }
error() { printf '%b\n' "${RED}$1${NC}" >&2; }

TMPDIR_AVSCORE=""
SERVER_PID=""
INSTALLED_BIN=""

cleanup() {
  if [ -n "$TMPDIR_AVSCORE" ] && [ -d "$TMPDIR_AVSCORE" ]; then
    rm -rf "$TMPDIR_AVSCORE"
  fi
}

forward_signal() {
  signal_name=$1
  signal_status=$2
  trap - TERM INT
  if [ -n "$SERVER_PID" ]; then
    kill "-$signal_name" "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  exit "$signal_status"
}

trap cleanup EXIT
trap 'forward_signal TERM 143' TERM
trap 'forward_signal INT 130' INT

detect_os() {
  os_name=$(uname -s)
  case "$os_name" in
    Darwin) printf '%s\n' darwin ;;
    Linux) printf '%s\n' linux ;;
    *) error "Unsupported OS: $os_name. avscore supports macOS and Linux."; return 1 ;;
  esac
}

detect_arch() {
  arch_name=$(uname -m)
  case "$arch_name" in
    x86_64|amd64) printf '%s\n' amd64 ;;
    aarch64|arm64) printf '%s\n' arm64 ;;
    *) error "Unsupported architecture: $arch_name"; return 1 ;;
  esac
}

fix_binary() {
  bin_path=$1
  chmod +x "$bin_path" 2>/dev/null || true
  xattr -d com.apple.quarantine "$bin_path" 2>/dev/null || true
}

find_agentsview_bin() {
  if [ -n "$AVSCORE_BINARY_PATH" ]; then
    if [ ! -f "$AVSCORE_BINARY_PATH" ]; then
      error "AVSCORE_BINARY_PATH 指向的文件不存在：$AVSCORE_BINARY_PATH"
      return 1
    fi
    fix_binary "$AVSCORE_BINARY_PATH"
    if [ ! -x "$AVSCORE_BINARY_PATH" ]; then
      error "AVSCORE_BINARY_PATH 不可执行：$AVSCORE_BINARY_PATH"
      return 1
    fi
    printf '%s\n' "$AVSCORE_BINARY_PATH"
    return 0
  fi

  script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
  os=$(detect_os) || return 1
  arch=$(detect_arch) || return 1
  candidate="$script_dir/agentsview-${os}-${arch}"
  if [ -f "$candidate" ]; then
    fix_binary "$candidate"
    if [ -x "$candidate" ]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  fi
  if command -v agentsview >/dev/null 2>&1; then
    command -v agentsview
    return 0
  fi
  for candidate in "$HOME/.local/bin/agentsview" "/usr/local/bin/agentsview"; do
    if [ -x "$candidate" ]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

resolve_version() {
  if [ "$VERSION" != latest ]; then
    case "$VERSION" in v*) printf '%s\n' "$VERSION" ;; *) printf 'v%s\n' "$VERSION" ;; esac
    return 0
  fi
  final_url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$RELEASE_BASE_URL/latest" 2>/dev/null || true)
  if printf '%s\n' "$final_url" | grep -Eq '/releases/tag/v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
    printf '%s\n' "${final_url##*/releases/tag/}"
    return 0
  fi
  json=$(curl -fsSL "$RELEASE_BASE_URL/latest.json" 2>/dev/null || true)
  ver=$(printf '%s\n' "$json" | sed -nE 's/.*"version"[[:space:]]*:[[:space:]]*"v?([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' | head -1)
  if printf '%s\n' "$ver" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
    printf 'v%s\n' "$ver"
    return 0
  fi
  error "无法解析最新版本号，请通过 AVSCORE_VERSION=x.y.z 指定版本"
  return 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    error "无法校验 SHA256：未找到 sha256sum 或 shasum"
    return 1
  fi
}

verify_download() {
  downloaded=$1
  release_name=$2
  checksums=$3
  if [ "${AVSCORE_SKIP_CHECKSUM:-0}" = 1 ]; then
    warn "已通过 AVSCORE_SKIP_CHECKSUM=1 显式跳过 SHA256 校验"
    return 0
  fi
  if ! curl -fsSL "$checksums_url" -o "$checksums"; then
    error "无法下载 SHA256SUMS，拒绝安装未校验文件"
    return 1
  fi
  expected=$(awk -v f="$release_name" '{name=$2; sub(/^\*/, "", name); if (name==f) {print $1; exit}}' "$checksums")
  if [ -z "$expected" ]; then
    error "SHA256SUMS 中未找到 ${release_name}，拒绝安装"
    return 1
  fi
  actual=$(sha256_file "$downloaded") || return 1
  if [ "$expected" != "$actual" ]; then
    error "SHA256 校验失败：$release_name"
    error "期望: $expected"
    error "实际: $actual"
    return 1
  fi
  info "SHA256 校验通过"
}

install_agentsview() {
  os=$1
  arch=$2
  version=$3
  version_num=${version#v}
  TMPDIR_AVSCORE=$(mktemp -d "${TMPDIR:-/tmp}/avscore-install.XXXXXX")
  install_dir="$HOME/.local/bin"
  final_bin="$install_dir/agentsview"
  raw_name="agentsview-${os}-${arch}"
  archive_name="agentsview_${version_num}_${os}_${arch}.tar.gz"
  checksums_url="$RELEASE_BASE_URL/$version/SHA256SUMS"
  checksums="$TMPDIR_AVSCORE/SHA256SUMS"
  download="$TMPDIR_AVSCORE/download"

  info "下载 agentsview $version ($os/$arch)..."
  if curl -fsSL "$RELEASE_BASE_URL/$version/$raw_name" -o "$download" 2>/dev/null; then
    verify_download "$download" "$raw_name" "$checksums" || return 1
  else
    archive_url="$RELEASE_BASE_URL/$version/$archive_name"
    if ! curl -fsSL "$archive_url" -o "$download"; then
      error "下载失败：裸二进制和归档均不可用"
      return 1
    fi
    verify_download "$download" "$archive_name" "$checksums" || return 1
    mkdir -p "$TMPDIR_AVSCORE/unpacked"
    tar -xzf "$download" -C "$TMPDIR_AVSCORE/unpacked"
    if [ ! -f "$TMPDIR_AVSCORE/unpacked/agentsview" ]; then
      error "下载归档中缺少 agentsview 二进制"
      return 1
    fi
    download="$TMPDIR_AVSCORE/unpacked/agentsview"
  fi

  mkdir -p "$install_dir"
  install_tmp="$install_dir/.agentsview.avscore.$$"
  if ! cp "$download" "$install_tmp"; then
    rm -f "$install_tmp"
    error "无法暂存 agentsview 二进制"
    return 1
  fi
  fix_binary "$install_tmp"
  if ! mv "$install_tmp" "$final_bin"; then
    rm -f "$install_tmp"
    error "无法原子安装 agentsview 二进制"
    return 1
  fi
  if [ "$os" = darwin ]; then
    codesign -s - "$final_bin" 2>/dev/null || true
  fi
  info "已安装到 $final_bin"
  INSTALLED_BIN="$final_bin"
}

require_file() {
  path=$1
  label=$2
  if [ ! -f "$path" ]; then
    error "缺少${label}：$path"
    return 1
  fi
}

extract_startup_url() {
  first_line=$1
  url=$(printf '%s\n' "$first_line" | sed -nE 's/^.*"type":"server-started".*"url":"([^"]+)".*$/\1/p')
  case "$url" in
    http://127.0.0.1:*|http://localhost:*) printf '%s\n' "$url" ;;
    *) return 1 ;;
  esac
}

open_browser() {
  url=$1
  if [ "${AVSCORE_NO_BROWSER:-0}" = 1 ]; then
    info "已通过 AVSCORE_NO_BROWSER=1 跳过自动打开浏览器"
    return 0
  fi
  if [ "$OS" = darwin ] && command -v open >/dev/null 2>&1; then
    open "$url" >/dev/null 2>&1 || warn "无法自动打开浏览器，请手动访问：$url"
  elif command -v xdg-open >/dev/null 2>&1; then
    xdg-open "$url" >/dev/null 2>&1 || warn "无法自动打开浏览器，请手动访问：$url"
  else
    warn "未找到浏览器打开命令，请手动访问：$url"
  fi
}

main() {
  info "🧬 avscore - 开发者画像一键分析"
  OS=$(detect_os)
  ARCH=$(detect_arch)
  info "平台：$OS/$ARCH"

  script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
  server_py="$script_dir/avscore_server.py"
  selection_template="$script_dir/session-selection.html.tmpl"
  profile_template="$script_dir/avscore.html.tmpl"
  application_template="$script_dir/job-application.html.tmpl"
  assets_dir="$script_dir/assets"
  command -v python3 >/dev/null 2>&1 || { error "启动 avscore 需要 python3，请先安装 Python 3"; return 1; }
  require_file "$server_py" "服务脚本" || return 1
  require_file "$selection_template" "会话选择模板" || return 1
  require_file "$profile_template" "画像模板" || return 1
  require_file "$application_template" "投递模板" || return 1
  require_file "$assets_dir/poster.png" "AITI 海报" || return 1
  require_file "$assets_dir/aiti-qr.svg" "AITI 二维码" || return 1

  if bin=$(find_agentsview_bin); then
    info "已检测到 agentsview：$bin"
  else
    if [ -n "$AVSCORE_BINARY_PATH" ]; then return 1; fi
    version=$(resolve_version) || return 1
    install_agentsview "$OS" "$ARCH" "$version" || return 1
    bin=$INSTALLED_BIN
  fi

  if [ "${AVSCORE_SKIP_SYNC:-0}" = 1 ]; then
    warn "已通过 AVSCORE_SKIP_SYNC=1 跳过 agentsview sync"
  else
    info "同步 agent session（首次可能较慢）..."
    if ! "$bin" sync; then
      error "agentsview sync 失败；修复后重试，或显式设置 AVSCORE_SKIP_SYNC=1"
      return 1
    fi
  fi

  if ! mkdir -p "$OUTPUT_DIR" || ! chmod 700 "$OUTPUT_DIR"; then
    error "无法创建私有报告目录：$OUTPUT_DIR"
    return 1
  fi
  TMPDIR_AVSCORE=$(mktemp -d "${TMPDIR:-/tmp}/avscore-server.XXXXXX")
  startup_log="$TMPDIR_AVSCORE/server.log"
  server_args=(
    "$server_py"
    --binary "$bin"
    --selection-template "$selection_template"
    --profile-template "$profile_template"
    --application-template "$application_template"
    --assets-dir "$assets_dir"
    --output-dir "$OUTPUT_DIR"
  )
  python3 "${server_args[@]}" >"$startup_log" 2>&1 &
  SERVER_PID=$!

  attempts=0
  first_line=""
  while [ "$attempts" -lt 200 ]; do
    if [ -s "$startup_log" ]; then
      IFS= read -r first_line < "$startup_log" || true
      break
    fi
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
      wait "$SERVER_PID" || server_status=$?
      error "avscore 服务启动失败（退出码 ${server_status:-0}）"
      sed -n '1,20p' "$startup_log" >&2
      SERVER_PID=""
      return 1
    fi
    sleep 0.05
    attempts=$((attempts + 1))
  done
  url=$(extract_startup_url "$first_line" || true)
  if [ -z "$url" ]; then
    error "无法解析 avscore 服务的启动 JSON"
    [ -n "$first_line" ] && error "首行输出：$first_line"
    kill -TERM "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
    SERVER_PID=""
    return 1
  fi

  printf '%s\n' "$url"
  open_browser "$url"
  set +e
  wait "$SERVER_PID"
  server_status=$?
  set -e
  SERVER_PID=""
  return "$server_status"
}

if [ "${AVSCORE_SOURCE_ONLY:-0}" != 1 ]; then
  main "$@"
fi
