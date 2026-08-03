#!/usr/bin/env sh
# verify-install.sh — MVP 安装验证脚本
# 在另一台电脑安装 @kuai-ai/cli 后运行此脚本以验证安全属性和基本功能
# 用法：sh verify-install.sh
set -eu

PASS=0
FAIL=0

check() {
  label="$1"
  if eval "$2" >/dev/null 2>&1; then
    printf "  PASS  %s\n" "$label"
    PASS=$((PASS + 1))
  else
    printf "  FAIL  %s\n" "$label"
    FAIL=$((FAIL + 1))
  fi
}

echo ""
echo "=== @kuai-ai/cli MVP 安装验证 ==="
echo ""

# 1. kuai 命令存在
KUAI_PATH=""
if command -v kuai >/dev/null 2>&1; then
  KUAI_PATH=$(command -v kuai)
  printf "  PASS  kuai 命令可找到：%s\n" "$KUAI_PATH"
  PASS=$((PASS + 1))
else
  printf "  FAIL  kuai 命令未找到（请检查 npm global bin 是否在 PATH 中）\n"
  FAIL=$((FAIL + 1))
fi

# 2. macOS 安全属性检查（仅 macOS）
if [ "$(uname -s)" = "Darwin" ] && [ -n "$KUAI_PATH" ]; then
  if xattr -l "$KUAI_PATH" 2>/dev/null | grep -q "com.apple.quarantine"; then
    printf "  FAIL  检测到 com.apple.quarantine 标记（Gatekeeper 会拦截）\n"
    printf "        修复：xattr -c %s\n" "$KUAI_PATH"
    FAIL=$((FAIL + 1))
  else
    printf "  PASS  无 com.apple.quarantine（不触发 Gatekeeper）\n"
    PASS=$((PASS + 1))
  fi

  # 验证是 .js 脚本而非二进制
  if file "$KUAI_PATH" 2>/dev/null | grep -qE "(shell script|ASCII|text)"; then
    printf "  PASS  kuai 是文本脚本（非二进制，天然绕过签名要求）\n"
    PASS=$((PASS + 1))
  else
    # symlink 场景：跟随到真实文件
    REAL_PATH=$(readlink -f "$KUAI_PATH" 2>/dev/null || echo "$KUAI_PATH")
    if file "$REAL_PATH" 2>/dev/null | grep -qE "(shell script|ASCII|text|JavaScript)"; then
      printf "  PASS  kuai 指向文本脚本（symlink -> %s）\n" "$REAL_PATH"
      PASS=$((PASS + 1))
    else
      printf "  INFO  kuai 文件类型：%s\n" "$(file "$REAL_PATH" 2>/dev/null || echo "unknown")"
    fi
  fi
fi

# 3. 功能验证
if [ -n "$KUAI_PATH" ]; then
  VERSION_OUT=$(kuai version 2>/dev/null || true)
  if echo "$VERSION_OUT" | grep -q "mvp\|0\.1\.0"; then
    printf "  PASS  kuai version 输出：%s\n" "$VERSION_OUT"
    PASS=$((PASS + 1))
  else
    printf "  FAIL  kuai version 输出不符预期：%s\n" "$VERSION_OUT"
    FAIL=$((FAIL + 1))
  fi

  HELP_OUT=$(kuai help 2>/dev/null || true)
  if echo "$HELP_OUT" | grep -q "Usage: kuai"; then
    printf "  PASS  kuai help 输出正常\n"
    PASS=$((PASS + 1))
  else
    printf "  FAIL  kuai help 输出异常：%s\n" "$HELP_OUT"
    FAIL=$((FAIL + 1))
  fi
fi

# 4. Node 版本检查
NODE_VER=$(node --version 2>/dev/null || echo "not found")
NODE_MAJOR=$(echo "$NODE_VER" | sed 's/v//' | cut -d. -f1)
if [ "$NODE_MAJOR" -ge 18 ] 2>/dev/null; then
  printf "  PASS  Node.js 版本满足要求：%s\n" "$NODE_VER"
  PASS=$((PASS + 1))
else
  printf "  FAIL  Node.js 版本不足（需要 >=18，当前：%s）\n" "$NODE_VER"
  FAIL=$((FAIL + 1))
fi

echo ""
echo "=== 结果：${PASS} 通过  ${FAIL} 失败 ==="
echo ""

if [ "$FAIL" -eq 0 ]; then
  echo "✓ 全部验证通过，npm 安装路径正常，不触发安全限制"
  exit 0
else
  echo "✗ 有验证项失败，请检查上述输出"
  exit 1
fi
