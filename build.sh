#!/usr/bin/env bash
# CF Auto Desktop — macOS 构建脚本
#
# 用法：
#   ./build.sh                # arm64（Apple Silicon 默认）
#   ./build.sh universal      # 通用二进制（Intel + Apple Silicon）
#   ./build.sh dev            # 开发模式（热重载）
set -euo pipefail
cd "$(dirname "$0")"

if [[ "$(uname -s)" != 'Darwin' ]]; then
  echo '必须在 macOS 上构建。' >&2
  exit 1
fi

# 检查 Go
if ! command -v go >/dev/null; then
  echo '找不到 go。请安装 Go 1.22+：https://go.dev/dl/' >&2
  exit 1
fi

# 检查 / 自动装 Wails CLI
if ! command -v wails >/dev/null; then
  echo '找不到 wails CLI，尝试安装…'
  export PATH="$PATH:$(go env GOPATH)/bin"
  if ! command -v wails >/dev/null; then
    go install github.com/wailsapp/wails/v2/cmd/wails@latest
    export PATH="$PATH:$(go env GOPATH)/bin"
  fi
fi

# 国内加速（可选）
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export GOSUMDB="${GOSUMDB:-off}"

mode="${1:-arm64}"
case "$mode" in
  dev)
    echo '→ 开发模式（wails dev，热重载）'
    exec wails dev
    ;;
  universal)
    echo '→ 构建通用二进制（darwin/universal）'
    wails build -platform darwin/universal -clean
    ;;
  arm64|*)
    echo '→ 构建 Apple Silicon 二进制（darwin/arm64）'
    wails build -platform darwin/arm64 -clean
    ;;
esac

APP='build/bin/CF Auto Desktop.app'
if [[ -d "$APP" ]]; then
  SIZE=$(du -sh "$APP" | awk '{print $1}')
  printf '\n✅ 打包完成：%s\n   实际体积：%s\n' "$PWD/$APP" "$SIZE"
  echo '   未做 Apple 签名 / 公证，仅自己使用。'
  echo
  echo '   启动：open "build/bin/CF Auto Desktop.app"'
else
  echo '❌ 未找到 .app 输出' >&2
  exit 1
fi
