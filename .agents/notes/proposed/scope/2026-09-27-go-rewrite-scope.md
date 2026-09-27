# Agent Note: 不做全量 Go 重写，改走 Python 瘦身 + 可选 Go 守护混合

Status: proposed — Python 瘦身侧已交付；Go 守护混合方案待立项，尚未实施

## Problem

v0.1（Python 3.11 + PySide6）空闲 RSS ~120–150 MB、`.app` 未瘦身约 1.2 GB。用户提出「用 Go 重写以降低资源占用」，但全量 Go 重写牵涉 Qt 托盘 / 文件对话框 / macOS 系统集成重做、7 条 Python 测试翻译、UI 观感降级等隐性成本，必须先定「做不做 / 做到哪一层」再动。

## Decision

**不做**全量 Go 重写。分三层推进：

1. **Python 瘦身（已交付）**
   - `build_macos.sh` 移除 `--collect-all PySide6`，显式保留 `QtCore / QtGui / QtWidgets`，其余 54 个 Qt 绑定 `--exclude-module`
   - 构建后置清理：再剔除 `QtPdf / QtQuick / QtQml* / QtVirtualKeyboard*` 等无用 framework
   - `engine.SCAN_PRESETS` 加 `standard / light` 两档；日志缓冲 400 → 200
   - **结果**：`.app` **78 MB**（从 ~1.2 GB 缩 15×）；窗口态 Footprint ~78 MB，后台 tray 态 ~41 MB
2. **Go 守护混合（未来可选，3–5 天工作量）**
   - 新增 headless 守护 `cf-auto-daemon`（Go，约 300 行）：扫描调度 + cfst 子进程 + TLS/WS 探活 + Provider HTTP
   - Python GUI 仅作查看器，关闭即退出；通过 Unix socket / 本地 HTTP 与守护通信
   - **预期**：日常空闲内存 = Go 守护 ~20 MB + 可选轻量 systray
3. **全量 Go 重写（否决）**
   - 拒绝 `fyne / gioui` 全原生 GUI：UI 观感难达 macOS 原生，且本次刚做完的 Qt QSS 设计作废
   - 若真要 Go 全栈，只能走 `wails`（HTML/CSS 可复用设计 token），但仍要 2–3 周

**关键机制 / 边界 / 所有权**

- 资源大头是 **Qt 运行时**（Python + engine 本身仅 26 MB），不是 Python 解释器 → 瘦身优先于换语言
- GUI 所有权留在 Python/Qt；调度与网络下沉重到 Go（如立项）
- macOS launchd / `open` / 单实例锁等系统集成留 Python 侧，Go 守护不接管

## Alternatives considered

- **A. 保持 v0.1 现状不动**：`.app` 1.2 GB、空闲 RSS 120–150 MB，视觉陈旧。收益太低，否决。
- **B. Go + `getlantern/systray`（无窗口）**：空闲最低（~15 MB），但用户失去图形界面；对个人工具可用，对本产品不完整。保留为极简备选。
- **C. Go + `wails` Web UI**：观感尚可、可复用本次设计 token；但 2–3 周工作量且引入 webview 常驻（50–80 MB），相比混合方案收益不明显。否决。
- **D. Go + `fyne / gioui` 原生 GUI**：4–6 周工作量、观感不及 macOS 原生、测试全重写。否决。
- **E. 全量 Go 重写（含 Cocoa 绑定）**：理论最优（20–40 MB / .app 15 MB），但 macOS 原生菜单栏 + 对话框绑定复杂度极高、回归风险大、不匹配「个人工具、仅 macOS」的诉求。否决。

## Consequences

- **接受的代价**：空闲 RSS 仍 ~80–120 MB（Qt 底盘），未到「日常 < 30 MB」档
- **后续约束**：
  - 任何新增 GUI 组件必须继续走 Qt Widgets + QSS（design token 见 `src/app.py:T`）；禁止在 Python 侧引入 QtQuick/QML
  - `build_macos.sh` 的后置 prune 清单是打包体积真源；新增 Qt 绑定需同步补 `--exclude-module`
  - 若启动 Go 守护立项：Python `engine.do_scan` / `ProviderServer` 须下沉到守护，Python 侧保留 UI + 配置读写
- **再议条件（reintroduction / reopen）**：
  - 空闲 RSS 长期 > 150 MB 且 Qt 侧无法解释
  - 出现跨平台（Windows / Linux）或开源分发诉求
  - Go 守护混合上线后仍需更激进的体积压缩

## Verification

- `./build_macos.sh` 输出 `.app` 体积 ≤ 100 MB（当前 78 MB）且 `dist/CF Auto Desktop.app/Contents/Frameworks/PySide6/Qt/lib/` 仅剩 `QtCore / QtGui / QtWidgets / QtDBus / QtNetwork / QtOpenGL / QtSvg`
- `python3 -m pytest -q tests` 全绿（7/7）
- `python3 -m py_compile src/app.py src/engine.py` 通过
- `footprint $(pgrep -f "CF Auto Desktop")` 观测到窗口态 ≤ 100 MB、后台态 ≤ 60 MB
- `git ls-files` 不得包含 `README.md`、`docs/`、`.vidt/`（本项目当前把决策长文放 `.agents/notes/`，`docs/` 为本地参考、默认 ignore）
