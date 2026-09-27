#!/bin/bash
# macOS double-click bootstrap: creates a local venv, installs GUI deps, and runs app.
set -euo pipefail
cd "$(dirname "$0")"
if [[ "$(uname -s)" != 'Darwin' ]]; then
  echo '此桌面工具需要 macOS。'
  read -r -p '按回车退出' _
  exit 1
fi
if ! command -v python3 >/dev/null; then
  echo '找不到 python3。请先安装 Python 3.11 或更新版本。'
  read -r -p '按回车退出' _
  exit 1
fi
python3 -m venv .venv
source .venv/bin/activate
python3 -m pip install -r requirements-app.txt
exec python3 src/app.py
