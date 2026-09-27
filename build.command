#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"
./build_macos.sh
read -r -p '打包流程结束。按回车关闭终端窗口：' _
