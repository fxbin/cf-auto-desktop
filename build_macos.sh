#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"
if [[ "$(uname -s)" != 'Darwin' ]]; then
  echo '必须在 macOS 上打包 .app；源码仍可在 macOS 上直接运行。'
  exit 1
fi
python3 -m venv .venv
source .venv/bin/activate
python3 -m pip install --upgrade pip
python3 -m pip install -r requirements.txt
python3 -m pytest -q tests
python3 -m PyInstaller --noconfirm --clean --windowed --onedir \
  --name 'CF Auto Desktop' \
  --osx-bundle-identifier 'com.local.cf-auto-desktop' \
  --paths src --collect-all PySide6 \
  src/app.py
printf '\n打包完成：%s/dist/CF Auto Desktop.app\n' "$PWD"
echo '个人本机打包 .app 未经 Apple Developer ID 签名/公证，不保证其他 Mac 直接打开。'
