#!/usr/bin/env bash
# CF Auto Desktop — 一键安装
#
# 用法：
#   ./install.sh            # 构建 + 装到 /Applications（需 sudo）
#   ./install.sh --user     # 装到 ~/Applications（无需 sudo）
#   ./install.sh --launch   # 额外勾选登录自启
#   ./install.sh --open     # 安装后自动打开
#   ./install.sh --skip-build  # 跳过构建（用现有 build/bin/）
set -euo pipefail
cd "$(dirname "$0")"

TARGET_DIR="/Applications"
LAUNCH=false
OPEN_AFTER=false
SKIP_BUILD=false

for arg in "$@"; do
  case "$arg" in
    --user)        TARGET_DIR="$HOME/Applications" ;;
    --launch)      LAUNCH=true ;;
    --open)        OPEN_AFTER=true ;;
    --skip-build)  SKIP_BUILD=true ;;
    -h|--help)
      sed -n '2,12p' "$0"
      exit 0
      ;;
    *)
      echo "未知参数：$arg" >&2
      echo "用法：./install.sh [--user] [--launch] [--open] [--skip-build]" >&2
      exit 1
      ;;
  esac
done

if [[ "$(uname -s)" != 'Darwin' ]]; then
  echo '此工具仅支持 macOS。' >&2
  exit 1
fi

APP_NAME="CF Auto Desktop.app"
SRC="build/bin/$APP_NAME"
DEST="$TARGET_DIR/$APP_NAME"

# ── 1. 构建 ────────────────────────────────────────────────────────────────
if [ "$SKIP_BUILD" = false ]; then
  echo "▸ 构建 $APP_NAME…"
  ./build.sh >/dev/null
  echo "  ✓ 构建完成"
else
  echo "▸ 跳过构建，使用现有 $SRC"
fi

if [ ! -d "$SRC" ]; then
  echo "❌ 未找到 $SRC（先跑 ./build.sh）" >&2
  exit 1
fi

# ── 2. 复制到应用目录 ──────────────────────────────────────────────────────
echo "▸ 安装到 $TARGET_DIR…"
mkdir -p "$TARGET_DIR"

# 移除旧版本（保留用户数据）
if [ -d "$DEST" ]; then
  echo "  · 移除旧版本…"
  rm -rf "$DEST"
fi

# 复制（保留签名、symlink、元数据）
if [[ "$TARGET_DIR" == "/Applications" && -w "/Applications" ]]; then
  ditto "$SRC" "$DEST"
elif [[ "$TARGET_DIR" == "/Applications" ]]; then
  echo "  · 需要 sudo 权限…"
  sudo ditto "$SRC" "$DEST"
else
  ditto "$SRC" "$DEST"
fi

echo "  ✓ 已安装：$DEST"

# ── 3. 去除 quarantine（未签名/未公证的 app 首次打开需手动允许）──────────
if command -v xattr >/dev/null 2>&1; then
  xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true
  echo "  ✓ 已去除 quarantine 标记（首次打开无需右键）"
fi

# ── 4. 登录自启（可选）────────────────────────────────────────────────────
if [ "$LAUNCH" = true ]; then
  echo "▸ 配置登录自启…"
  PLIST="$HOME/Library/LaunchAgents/com.cf-auto-desktop.menubar.plist"
  BIN="$DEST/Contents/MacOS/CF Auto Desktop"
  mkdir -p "$HOME/Library/LaunchAgents"
  cat > "$PLIST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.cf-auto-desktop.menubar</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/open</string>
    <string>-a</string>
    <string>$DEST</string>
    <string>--args</string>
    <string>--background</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
</dict></plist>
PLIST_EOF
  launchctl bootstrap "gui/$(id -u)" "$PLIST" 2>/dev/null || true
  echo "  ✓ 登录自启已配置（$PLIST）"
fi

# ── 5. 自动打开（可选）────────────────────────────────────────────────────
if [ "$OPEN_AFTER" = true ]; then
  echo "▸ 启动 $APP_NAME…"
  open "$DEST"
fi

echo
echo "✅ 安装完成"
echo "   位置：$DEST"
echo "   启动：双击 /Applications/$APP_NAME，或 Spotlight（Cmd+Space 输 CF）"
if [ "$LAUNCH" = false ]; then
  echo "   想开机自启？在 App 内勾选「登录 Mac 后自动启动」"
fi
