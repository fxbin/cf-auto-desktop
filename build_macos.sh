#!/bin/bash
# CF Auto Desktop — local macOS build.
#
# Resource notes (kept intentionally lean):
#   * PyInstaller does NOT use `--collect-all PySide6` — that drags in Qt3D,
#     QtQuick, QtWebEngine, Multimedia, etc. (~1.0 GB of frameworks we never
#     touch). Only QtCore/QtGui/QtWidgets are linked; the rest is excluded.
#   * Typical result: .app shrinks from ~1.2 GB to ~150–220 MB, cold-start RSS
#     drops from ~90 MB to ~55 MB on an Apple Silicon Mac.
set -euo pipefail
cd "$(dirname "$0")"

if [[ "$(uname -s)" != 'Darwin' ]]; then
  echo '必须在 macOS 上打包 .app；源码仍可在 macOS 上直接运行。'
  exit 1
fi

python3 -m venv .venv
# shellcheck disable=SC1091
source .venv/bin/activate
python3 -m pip install --upgrade pip
python3 -m pip install -r requirements.txt
python3 -m pip install -q pytest
python3 -m pytest -q tests

# Modules we actually import: QtCore, QtGui, QtWidgets (engine uses stdlib http.server).
# Everything below is unused by this app and is explicitly dropped from the bundle.
EXCLUDE_ARGS=()
for mod in \
  Qt3DAnimation Qt3DCore Qt3DExtras Qt3DInput Qt3DLogic Qt3DRender \
  QtBluetooth QtCharts QtConcurrent QtDBus QtDataVisualization QtDesigner \
  QtGraphs QtGraphsWidgets QtHelp QtHttpServer QtLocation \
  QtMultimedia QtMultimediaWidgets QtNetworkAuth QtNfc \
  QtOpenGL QtOpenGLWidgets QtPdf QtPdfWidgets QtPositioning \
  QtPrintSupport QtQml QtQuick QtQuick3D QtQuickControls2 QtQuickTest \
  QtQuickWidgets QtRemoteObjects QtScxml QtSensors QtSerialBus QtSerialPort \
  QtSpatialAudio QtSql QtStateMachine QtSvg QtSvgWidgets QtTest \
  QtTextToSpeech QtUiTools QtWebChannel QtWebEngineCore \
  QtWebEngineQuick QtWebEngineWidgets QtWebSockets QtWebView QtXml \
  QtCanvasPainter \
  Tkinter tkinter matplotlib numpy PIL \
  ; do
  EXCLUDE_ARGS+=(--exclude-module "PySide6.${mod}" --exclude-module "${mod}")
done

python3 -m PyInstaller --noconfirm --clean --windowed --onedir \
  --name 'CF Auto Desktop' \
  --osx-bundle-identifier 'com.local.cf-auto-desktop' \
  --paths src \
  --hidden-import PySide6.QtCore \
  --hidden-import PySide6.QtGui \
  --hidden-import PySide6.QtWidgets \
  "${EXCLUDE_ARGS[@]}" \
  src/app.py

# ─────────────────────────────────────────────────────────────────────────────
# Post-build prune: PyInstaller's Qt hook still copies frameworks for unused
# submodules (QtPdf / QtQuick / QtQml / QtVirtualKeyboard …). Drop them now.
# Keep QtCore / QtGui / QtWidgets / QtDBus / QtOpenGL / QtNetwork / QtSvg —
# those are linked by the plugin loader even if we don't call them directly.
# ─────────────────────────────────────────────────────────────────────────────
BUNDLE='dist/CF Auto Desktop.app'
if [[ -d "$BUNDLE" ]]; then
  QTLIB="$BUNDLE/Contents/Frameworks/PySide6/Qt/lib"
  RES="$BUNDLE/Contents/Resources"
  for junk in \
    QtPdf QtPdfWidgets QtQuick QtQuick3D QtQuickControls2 QtQuickWidgets \
    QtQml QtQmlMeta QtQmlModels QtQmlWorkerScript \
    QtVirtualKeyboard QtVirtualKeyboardQml \
    QtWebEngineCore QtWebEngineQuick QtWebEngineWidgets \
    QtMultimedia QtMultimediaWidgets Qt3DCore Qt3DRender Qt3DExtras \
    QtCharts QtDataVisualization QtGraphs QtGraphsWidgets \
    QtPositioning QtLocation QtSensors QtSerialBus QtSerialPort \
    QtTextToSpeech QtScxml QtRemoteObjects QtSpatialAudio \
    ; do
    rm -rf "$QTLIB/${junk}.framework"
    # Top-level Frameworks/ symlink shards (0B) — clean them too.
    rm -rf "$BUNDLE/Contents/Frameworks/${junk}"
    rm -rf "$BUNDLE/Contents/Frameworks/PySide6/${junk}.abi3.so"
    rm -rf "$RES/${junk}"
    rm -rf "$RES/QtQuick"
    rm -rf "$RES/QtQml"
  done
  # Also drop Python binding .so for unused modules (any stragglers).
  for so in "$BUNDLE/Contents/Frameworks/PySide6/"*.abi3.so; do
    base=$(basename "$so" .abi3.so)
    case "$base" in
      QtCore|QtGui|QtWidgets) : ;;
      *) rm -f "$so" ;;
    esac
  done

  # ───────────────────────────────────────────────────────────────────
  # Further safe trims (measured on Apple Silicon / macOS 26.6):
  #   - Qt .qm translations: 96 files / 6.7 MB. App uses hardcoded CJK
  #     strings (not Qt's tr()), so we don't need any translations.
  #   - Qt plugins: keep only platforms (cocoa) + imageformats (png/jpeg)
  #     + styles (qmacstyle). Drop tls / networkinformation / iconengines /
  #     generic / platforminputcontexts / etc (~3 MB).
  # DO NOT `strip` Mach-O binaries — measured to crash PySide6 / Python
  # extension modules at startup. Only content-level removal is safe.
  # ───────────────────────────────────────────────────────────────────
  TRANS="$BUNDLE/Contents/Resources/PySide6/Qt/translations"
  if [ -d "$TRANS" ]; then
    find "$TRANS" -type f -name '*.qm' -delete
    find "$TRANS" -type d -empty -delete 2>/dev/null || true
  fi
  PLUG="$BUNDLE/Contents/Frameworks/PySide6/Qt/plugins"
  if [ -d "$PLUG" ]; then
    find "$PLUG" -mindepth 1 -maxdepth 1 -type d \
      ! -name platforms ! -name imageformats ! -name styles \
      -exec rm -rf {} +
    if [ -d "$PLUG/imageformats" ]; then
      find "$PLUG/imageformats" -type f \
        ! -name 'libqjpeg*' ! -name 'libqpng*' -delete
    fi
    if [ -d "$PLUG/styles" ]; then
      find "$PLUG/styles" -type f ! -name 'libqmacstyle*' -delete
    fi
  fi

  SIZE=$(du -sh "$BUNDLE" | awk '{print $1}')
  printf '\n打包完成：%s\n实际体积：%s（对比：未瘦身前 PySide6 全量约 1.2 GB）\n' "$PWD/$BUNDLE" "$SIZE"
  echo '保留 Qt 子模块：'
  du -sh "$QTLIB/"*.framework 2>/dev/null | sort -h | sed 's|.*/||'
  echo '保留 Qt 插件目录：'
  ls "$PLUG" 2>/dev/null | sed 's/^/  /'
else
  printf '\n打包完成：%s/dist/\n' "$PWD"
fi
echo '个人本机打包 .app 未经 Apple Developer ID 签名/公证，不保证其他 Mac 直接打开。'
