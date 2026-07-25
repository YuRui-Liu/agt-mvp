#!/bin/bash
# avscore - one-shot AI coding behavior profile launcher (mac/linux)
# Usage: bash avscore.sh
#   or:  curl -fsSL <url>/install.sh | bash

set -euo pipefail

# --- 配置（可通过环境变量覆盖）---
DEFAULT_RELEASE_URL="https://git.corp.kuaishou.com/ks-ep/ea-zonghe/zonghe-experimental/agentsview/releases/download"
RELEASE_BASE_URL="${AVSCORE_RELEASE_URL:-$DEFAULT_RELEASE_URL}"
VERSION="${AVSCORE_VERSION:-latest}"
OUTPUT_DIR="${AVSCORE_OUTPUT_DIR:-$HOME/.agentsview/reports}"
AVSCORE_BINARY_PATH="${AVSCORE_BINARY_PATH:-}"

# --- 颜色输出 ---
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
info()  { echo -e "${GREEN}$1${NC}" >&2; }
warn()  { echo -e "${YELLOW}$1${NC}" >&2; }
error() { echo -e "${RED}$1${NC}" >&2; }

# --- 临时文件清理 ---
TMPDIR_AVSCORE=""
cleanup() {
  if [ -n "$TMPDIR_AVSCORE" ] && [ -d "$TMPDIR_AVSCORE" ]; then
    rm -rf "$TMPDIR_AVSCORE"
  fi
}
trap cleanup EXIT INT TERM

# --- 检测 OS/arch ---
detect_os() {
  case "$(uname -s)" in
    Darwin) echo "darwin" ;;
    Linux)  echo "linux" ;;
    *) error "Unsupported OS: $(uname -s). avscore supports macOS and Linux."; exit 1 ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) error "Unsupported architecture: $(uname -m)"; exit 1 ;;
  esac
}

# --- 修复文件权限和 quarantine（微信/钉钉传输后必要）---
fix_binary() {
  local bin="$1"
  # 确保可执行权限
  chmod +x "$bin" 2>/dev/null || true
  # 移除 macOS quarantine 标记，避免 Gatekeeper 拦截
  xattr -d com.apple.quarantine "$bin" 2>/dev/null || true
}

# --- 检测 agentsview 是否已装 ---
find_agentsview_bin() {
  # 优先：与 avscore.sh 同目录的 agentsview-{os}-{arch} 二进制
  local script_dir
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  local os arch
  os=$(detect_os)
  arch=$(detect_arch)
  local candidate="$script_dir/agentsview-${os}-${arch}"
  if [ -f "$candidate" ]; then
    # 自动修复权限和 quarantine（无论是否可执行都先修复）
    fix_binary "$candidate"
    if [ -x "$candidate" ]; then
      echo "$candidate"
      return 0
    fi
  fi
  # 兜底：用户通过 AVSCORE_BINARY_PATH 强制指定路径
  if [ -n "${AVSCORE_BINARY_PATH:-}" ]; then
    if [ -f "$AVSCORE_BINARY_PATH" ]; then
      fix_binary "$AVSCORE_BINARY_PATH"
    fi
    if [ -x "$AVSCORE_BINARY_PATH" ]; then
      echo "$AVSCORE_BINARY_PATH"
      return 0
    else
      warn "AVSCORE_BINARY_PATH=$AVSCORE_BINARY_PATH 不可执行或不存在，忽略"
    fi
  fi
  if command -v agentsview &>/dev/null; then
    echo "$(command -v agentsview)"
    return 0
  fi
  # 检查默认安装路径
  for candidate in "$HOME/.local/bin/agentsview" "/usr/local/bin/agentsview"; do
    if [ -x "$candidate" ]; then
      echo "$candidate"
      return 0
    fi
  done
  return 1
}

# --- 解析 latest 版本号 ---
resolve_version() {
  if [ "$VERSION" != "latest" ]; then
    echo "v$VERSION"
    return 0
  fi
  local url="$RELEASE_BASE_URL/latest"
  local final_url
  final_url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$url" 2>/dev/null || true)
  if echo "$final_url" | grep -qE '/releases/tag/v[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "${final_url##*/releases/tag/}"
    return 0
  fi
  # fallback: 尝试 latest.json
  local json_url="$RELEASE_BASE_URL/latest.json"
  local json
  json=$(curl -fsSL "$json_url" 2>/dev/null || true)
  if [ -n "$json" ]; then
    local ver
    ver=$(echo "$json" | grep -oE '"version"\s*:\s*"v?[0-9]+\.[0-9]+\.[0-9]+"' | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')
    if [ -n "$ver" ]; then
      echo "v$ver"
      return 0
    fi
  fi
  error "无法解析最新版本号，请通过 AVSCORE_VERSION=x.y.z 指定版本"
  exit 1
}

# --- 下载并安装 agentsview ---
install_agentsview() {
  local os="$1"
  local arch="$2"
  local version="$3"
  local version_num="${version#v}"

  info "📥 下载 agentsview $version ($os/$arch)..."
  TMPDIR_AVSCORE=$(mktemp -d)
  local install_dir="$HOME/.local/bin"
  mkdir -p "$install_dir"

  local bin_ext=""
  [ "$os" = "windows" ] && bin_ext=".exe"
  local raw_filename="agentsview-${os}-${arch}${bin_ext}"
  local raw_url="$RELEASE_BASE_URL/$version/$raw_filename"

  local archive_filename="agentsview_${version_num}_${os}_${arch}.tar.gz"
  local archive_url="$RELEASE_BASE_URL/$version/$archive_filename"

  local checksums_url="$RELEASE_BASE_URL/$version/SHA256SUMS"
  local checksums="$TMPDIR_AVSCORE/SHA256SUMS"
  local final_bin="$install_dir/agentsview${bin_ext}"

  # 优先尝试裸二进制下载（Claude Code 模式，无解压，无 quarantine 传播）
  if curl -fsSLI -o /dev/null "$raw_url" 2>/dev/null; then
    info "   检测到裸二进制 release，直接下载..."
    if curl -fsSL "$raw_url" -o "$final_bin"; then
      fix_binary "$final_bin"
      # 校验
      if [ "${AVSCORE_SKIP_CHECKSUM:-0}" != "1" ]; then
        info "🔐 校验 SHA256..."
        if curl -fsSL "$checksums_url" -o "$checksums" 2>/dev/null; then
          local expected
          expected=$(awk -v f="$raw_filename" '{gsub(/^\*/, "", $2); if ($2==f) {print $1; exit}}' "$checksums")
          if [ -n "$expected" ]; then
            local actual
            if command -v sha256sum &>/dev/null; then
              actual=$(sha256sum "$final_bin" | cut -d' ' -f1)
            else
              actual=$(shasum -a 256 "$final_bin" | cut -d' ' -f1)
            fi
            if [ "$expected" != "$actual" ]; then
              error "SHA256 校验失败！可能被篡改"
              error "  期望: $expected"
              error "  实际: $actual"
              exit 1
            fi
            info "   校验通过"
          else
            warn "SHA256SUMS 中未找到 $raw_filename，跳过校验"
          fi
        else
          warn "无法下载 SHA256SUMS，跳过校验"
        fi
      fi
      if [ "$os" = "darwin" ]; then
        codesign -s - "$final_bin" 2>/dev/null || true
      fi
      info "✅ 已安装到 $final_bin"
      echo "$final_bin"
      return 0
    fi
  fi

  # Fallback：tar.gz 模式（旧 release 兼容）
  info "   裸二进制不可用，回退到 tar.gz 模式..."
  local archive="$TMPDIR_AVSCORE/agentsview.tar.gz"
  if ! curl -fsSL "$archive_url" -o "$archive"; then
    error "下载失败：$raw_url 和 $archive_url 均不可用"
    error "请检查网络或通过 AVSCORE_RELEASE_URL 指定镜像源"
    exit 1
  fi

  if [ "${AVSCORE_SKIP_CHECKSUM:-0}" != "1" ]; then
    info "🔐 校验 SHA256..."
    if curl -fsSL "$checksums_url" -o "$checksums" 2>/dev/null; then
      local expected
      expected=$(awk -v f="$archive_filename" '{gsub(/^\*/, "", $2); if ($2==f) {print $1; exit}}' "$checksums")
      if [ -n "$expected" ]; then
        local actual
        if command -v sha256sum &>/dev/null; then
          actual=$(sha256sum "$archive" | cut -d' ' -f1)
        else
          actual=$(shasum -a 256 "$archive" | cut -d' ' -f1)
        fi
        if [ "$expected" != "$actual" ]; then
          error "SHA256 校验失败！可能被篡改"
          exit 1
        fi
        info "   校验通过"
      else
        warn "SHA256SUMS 中未找到 $archive_filename，跳过校验"
      fi
    else
      warn "无法下载 SHA256SUMS，跳过校验"
    fi
  fi

  info "📦 解压..."
  tar -xzf "$archive" -C "$TMPDIR_AVSCORE"
  mv "$TMPDIR_AVSCORE/agentsview" "$final_bin"
  fix_binary "$final_bin"

  if [ "$os" = "darwin" ]; then
    codesign -s - "$final_bin" 2>/dev/null || true
  fi

  info "✅ 已安装到 $final_bin"
  echo "$final_bin"
}

# --- 提取 JSON 字段（不依赖 jq）---
extract_str() {
  grep -oE "$1" "$PROFILE_JSON" | head -1 | sed -E 's/.*:"([^"]*)".*/\1/'
}

# --- 主流程 ---
main() {
  info "🧬 avscore - 开发者画像一键分析"
  echo

  local os arch
  os=$(detect_os)
  arch=$(detect_arch)
  info "平台：$os/$arch"

  # 1. 检测/安装 agentsview
  local bin
  if bin=$(find_agentsview_bin); then
    info "✓ 已检测到 agentsview：$bin"
  else
    local version
    version=$(resolve_version)
    bin=$(install_agentsview "$os" "$arch" "$version")
  fi

  # 2. agentsview sync
  echo
  info "🔄 同步 agent session（首次可能较慢）..."
  if ! "$bin" sync 2>&1; then
    error "agentsview sync 失败"
    exit 1
  fi

  # 3. agentsview profile --json
  echo
  info "📊 计算画像..."
  mkdir -p "$OUTPUT_DIR"
  PROFILE_JSON="$OUTPUT_DIR/profile.json"
  if ! "$bin" profile --json --engine statistical > "$PROFILE_JSON" 2>/dev/null; then
    if ! "$bin" profile --json > "$PROFILE_JSON" 2>/dev/null; then
      error "agentsview profile 失败"
      exit 1
    fi
    warn "   当前 agentsview 版本较旧，建议升级以获得 --engine 支持"
  fi
  info "   JSON 已写入 $PROFILE_JSON"

  # 4. 提取字段并填模板
  echo
  info "🎨 生成 HTML..."
  local template
  local script_dir
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  template="$script_dir/avscore.html.tmpl"
  if [ ! -f "$template" ]; then
    local ver
    ver=$("$bin" --version 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1)
    if [ -z "$ver" ]; then ver="latest"; fi
    template="$OUTPUT_DIR/avscore.html.tmpl"
    if ! curl -fsSL "$RELEASE_BASE_URL/$ver/avscore.html.tmpl" -o "$template" 2>/dev/null; then
      error "找不到 HTML 模板（同目录无，且无法从 release 下载）"
      error "请将 avscore.html.tmpl 放到与 avscore.sh 相同的目录"
      exit 1
    fi
  fi

  local generated_at
  generated_at=$(extract_str '"generated_at":"[^"]*"')
  if [ -z "$generated_at" ]; then
    generated_at=$(date '+%Y-%m-%d %H:%M:%S')
  fi

  local session_count
  session_count=$(grep -oE '"session_count":[0-9]+' "$PROFILE_JSON" | tail -1 | grep -oE '[0-9]+')
  [ -z "$session_count" ] && session_count=0

  local agent_count=0

  local steering execution engineering planning product autonomy adaptation
  if command -v python3 &>/dev/null; then
    read -r steering execution engineering planning product autonomy adaptation <<< "$(python3 -c "
import json,sys
d=json.load(open('$PROFILE_JSON'))
p=d.get('profile',{})
print(p.get('steering',{}).get('score',0), p.get('execution',{}).get('score',0),
      p.get('engineering',{}).get('score',0), p.get('planning',{}).get('score',0),
      p.get('product',{}).get('score',0), p.get('autonomy',{}).get('score',0),
      p.get('adaptation',{}).get('score',0))
" 2>/dev/null || echo "0 0 0 0 0 0 0")"
  else
    steering=$(grep -A1 '"steering"' "$PROFILE_JSON" | grep -oE '"score":[0-9]+' | head -1 | grep -oE '[0-9]+')
    execution=$(grep -A1 '"execution"' "$PROFILE_JSON" | grep -oE '"score":[0-9]+' | head -1 | grep -oE '[0-9]+')
    engineering=$(grep -A1 '"engineering"' "$PROFILE_JSON" | grep -oE '"score":[0-9]+' | head -1 | grep -oE '[0-9]+')
    planning=$(grep -A1 '"planning"' "$PROFILE_JSON" | grep -oE '"score":[0-9]+' | head -1 | grep -oE '[0-9]+')
    product=$(grep -A1 '"product"' "$PROFILE_JSON" | grep -oE '"score":[0-9]+' | head -1 | grep -oE '[0-9]+')
    autonomy=$(grep -A1 '"autonomy"' "$PROFILE_JSON" | grep -oE '"score":[0-9]+' | head -1 | grep -oE '[0-9]+')
    adaptation=$(grep -A1 '"adaptation"' "$PROFILE_JSON" | grep -oE '"score":[0-9]+' | head -1 | grep -oE '[0-9]+')
  fi
  [ -z "$steering" ] && steering=0
  [ -z "$execution" ] && execution=0
  [ -z "$engineering" ] && engineering=0
  [ -z "$planning" ] && planning=0
  [ -z "$product" ] && product=0
  [ -z "$autonomy" ] && autonomy=0
  [ -z "$adaptation" ] && adaptation=0

  local archetype_primary archetype_confidence
  archetype_primary=$(extract_str '"primary":"[^"]*"')
  [ -z "$archetype_primary" ] && archetype_primary="未知"
  archetype_confidence=$(grep -oE '"confidence":[0-9.]+' "$PROFILE_JSON" | head -1 | grep -oE '[0-9.]+')
  if [ -n "$archetype_confidence" ]; then
    archetype_confidence=$(awk "BEGIN { printf \"%d\", $archetype_confidence * 100 }")
  else
    archetype_confidence=0
  fi

  local trend_prediction
  trend_prediction=$(extract_str '"trend_prediction":"[^"]*"')
  [ -z "$trend_prediction" ] && trend_prediction="暂无演化数据"

  local shifts=("" "" "" "" "")
  if command -v python3 &>/dev/null; then
    local shift_output
    shift_output=$(python3 -c "
import json
d=json.load(open('$PROFILE_JSON'))
shifts=d.get('evolution',{}).get('key_shifts',[])[:5]
for s in shifts:
    print(f\"{s['direction']} {s['dimension']}: {s['from']}→{s['to']} (stage {s['at_stage']})\")
for _ in range(5-len(shifts)):
    print('')
" 2>/dev/null || true)
    local i=0
    while IFS= read -r line; do
      if [ $i -lt 5 ]; then
        shifts[$i]="$line"
        i=$((i+1))
      fi
    done <<< "$shift_output"
    while [ $i -lt 5 ]; do
      shifts[$i]=""
      i=$((i+1))
    done
  fi

  local report_html="$OUTPUT_DIR/report.html"
  cp "$template" "$report_html"
  local s1 s2 s3 s4 s5
  s1=$(printf '%s\n' "${shifts[0]}" | sed 's/[&/\]/\\&/g')
  s2=$(printf '%s\n' "${shifts[1]}" | sed 's/[&/\]/\\&/g')
  s3=$(printf '%s\n' "${shifts[2]}" | sed 's/[&/\]/\\&/g')
  s4=$(printf '%s\n' "${shifts[3]}" | sed 's/[&/\]/\\&/g')
  s5=$(printf '%s\n' "${shifts[4]}" | sed 's/[&/\]/\\&/g')
  sed -i.bak \
    -e "s/{{GENERATED_AT}}/$generated_at/g" \
    -e "s/{{SESSION_COUNT}}/$session_count/g" \
    -e "s/{{AGENT_COUNT}}/$agent_count/g" \
    -e "s/{{STEERING_SCORE}}/$steering/g" \
    -e "s/{{EXECUTION_SCORE}}/$execution/g" \
    -e "s/{{ENGINEERING_SCORE}}/$engineering/g" \
    -e "s/{{PLANNING_SCORE}}/$planning/g" \
    -e "s/{{PRODUCT_SCORE}}/$product/g" \
    -e "s/{{AUTONOMY_SCORE}}/$autonomy/g" \
    -e "s/{{ADAPTATION_SCORE}}/$adaptation/g" \
    -e "s/{{ARCHETYPE_PRIMARY}}/$archetype_primary/g" \
    -e "s/{{ARCHETYPE_CONFIDENCE}}/$archetype_confidence/g" \
    -e "s/{{TREND_PREDICTION}}/$trend_prediction/g" \
    -e "s/{{SHIFT_1}}/$s1/g" \
    -e "s/{{SHIFT_2}}/$s2/g" \
    -e "s/{{SHIFT_3}}/$s3/g" \
    -e "s/{{SHIFT_4}}/$s4/g" \
    -e "s/{{SHIFT_5}}/$s5/g" \
    "$report_html"
  rm -f "$report_html.bak"

  cp "$PROFILE_JSON" "$OUTPUT_DIR/report.json"

  info "✅ HTML 已生成：$report_html"

  echo
  info "🌐 打开浏览器..."
  if command -v open &>/dev/null; then
    open "$report_html" || warn "无法自动打开浏览器，请手动访问：$report_html"
  elif command -v xdg-open &>/dev/null; then
    xdg-open "$report_html" || warn "无法自动打开浏览器，请手动访问：$report_html"
  else
    warn "未找到浏览器打开命令，请手动访问：$report_html"
  fi
}

main "$@"
