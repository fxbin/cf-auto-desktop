#!/usr/bin/env python3
"""CF Auto Desktop — macOS menu bar + window UI for local Clash Party configs.

Design language: quiet OpenAI / ChatGPT desktop aesthetic.
Palette: warm off-white canvas #F9F9F9 · white surfaces · hairline #E5E5E5
        · ink #0D0D0D · muted #6B7280 · accent #10A37F · monochrome primary.
Spacing: 8px grid, 24px outer padding, 12/16px rhythm. Cards 12px radius.
"""
from __future__ import annotations

import fcntl
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import threading
import time

from PySide6.QtCore import Qt, QThread, QTimer, Signal, QSize, Property
from PySide6.QtGui import (
    QAction, QBrush, QColor, QFont, QIcon, QPainter, QPen, QPixmap,
    QLinearGradient, QPalette,
)
from PySide6.QtWidgets import (
    QApplication, QCheckBox, QComboBox, QFileDialog, QFrame, QGridLayout,
    QHBoxLayout, QLabel, QMainWindow, QMenu, QMessageBox, QPushButton,
    QPlainTextEdit, QSizePolicy, QSystemTrayIcon, QVBoxLayout, QWidget,
)

import engine

LAUNCH_LABEL = "com.cf-auto-desktop.menubar"


# ─────────────────────────────────────────────────────────────────────────────
# Design tokens — single source of truth
# ─────────────────────────────────────────────────────────────────────────────
class T:
    canvas      = "#F9F9F9"
    surface     = "#FFFFFF"
    surface_alt = "#F0F0F0"
    ink         = "#0D0D0D"
    ink_muted   = "#6B7280"
    ink_faint   = "#9CA3AF"
    border      = "#E5E5E5"
    border_str  = "#D4D4D4"
    primary     = "#0D0D0D"   # monochrome primary action
    primary_hv  = "#262626"
    primary_dk  = "#000000"
    green       = "#10A37F"
    green_soft  = "#E6F4EF"
    warn        = "#D97706"
    warn_soft   = "#FEF3C7"
    danger      = "#DC2626"
    danger_soft = "#FEE2E2"
    log_bg      = "#1A1A1A"
    log_ink     = "#E5E5E5"
    log_dim     = "#8A8A8A"
    radius_card = 12
    radius_pill = 999
    radius_in   = 8


def _apply_dark_palette(app: QApplication) -> None:
    """Force a neutral light palette so dark-mode OS settings don't tint our surfaces."""
    pal = QPalette()
    pal.setColor(QPalette.ColorRole.Window, QColor(T.canvas))
    pal.setColor(QPalette.ColorRole.WindowText, QColor(T.ink))
    pal.setColor(QPalette.ColorRole.Base, QColor(T.surface))
    pal.setColor(QPalette.ColorRole.Text, QColor(T.ink))
    pal.setColor(QPalette.ColorRole.Button, QColor(T.surface))
    pal.setColor(QPalette.ColorRole.ButtonText, QColor(T.ink))
    pal.setColor(QPalette.ColorRole.Highlight, QColor(T.primary))
    pal.setColor(QPalette.ColorRole.HighlightedText, QColor("#FFFFFF"))
    app.setPalette(pal)


def icon() -> QIcon:
    """Quiet monogram — rounded square + CF wordmark, ChatGPT-style restraint."""
    pix = QPixmap(128, 128)
    pix.fill(Qt.GlobalColor.transparent)
    p = QPainter(pix)
    p.setRenderHint(QPainter.RenderHint.Antialiasing)
    # Deep charcoal gradient square (primary tone)
    grad = QLinearGradient(0, 0, 128, 128)
    grad.setColorAt(0.0, QColor("#1A1A1A"))
    grad.setColorAt(1.0, QColor("#0D0D0D"))
    p.setBrush(QBrush(grad))
    p.setPen(Qt.PenStyle.NoPen)
    p.drawRoundedRect(4, 4, 120, 120, 30, 30)
    p.setPen(QPen(QColor("#FFFFFF")))
    font = QFont("Arial", 40, QFont.Weight.Bold)
    p.setFont(font)
    p.drawText(pix.rect(), Qt.AlignmentFlag.AlignCenter, "CF")
    p.end()
    return QIcon(pix)


def login_path() -> Path:
    return Path.home() / "Library" / "LaunchAgents" / f"{LAUNCH_LABEL}.plist"


def login_enabled() -> bool:
    return login_path().exists()


def startup_command() -> list[str]:
    if getattr(sys, "frozen", False):
        exe = Path(sys.executable).resolve()
        if ".app" in str(exe):
            bundle = exe.parents[2]
            return ["/usr/bin/open", "-a", str(bundle), "--args", "--background"]
        return [str(exe), "--background"]
    return [sys.executable, str(Path(__file__).resolve()), "--background"]


def set_login(enabled: bool) -> None:
    if sys.platform != "darwin":
        raise RuntimeError("开机自启设置目前只适用于 macOS")
    path = login_path()
    if enabled:
        path.parent.mkdir(parents=True, exist_ok=True)
        payload = {"Label": LAUNCH_LABEL, "ProgramArguments": startup_command(),
                   "RunAtLoad": True, "KeepAlive": False,
                   "StandardOutPath": str(engine.app_home() / "launch.log"),
                   "StandardErrorPath": str(engine.app_home() / "launch-error.log")}
        engine.atomic_write(path, plistlib.dumps(payload).decode("utf-8"))
        # launchctl activation can open a second copy immediately. Single-instance
        # flock below prevents a second provider or scan service from starting.
        result = subprocess.run(["launchctl", "bootstrap", f"gui/{os.getuid()}", str(path)],
                                capture_output=True, text=True)
        if result.returncode and "already" not in result.stderr.lower():
            # Agent still exists on disk for next login, notify caller of activation failure.
            raise RuntimeError("已创建登录任务，但立即启用失败；重新登录后生效。" + result.stderr[:200])
    else:
        subprocess.run(["launchctl", "bootout", f"gui/{os.getuid()}", LAUNCH_LABEL],
                       capture_output=True, timeout=6)
        path.unlink(missing_ok=True)


# ─────────────────────────────────────────────────────────────────────────────
# Reusable chrome
# ─────────────────────────────────────────────────────────────────────────────
class StatusPill(QLabel):
    """Soft pill — green / amber / gray / red state dot + label. Signature moment."""

    def __init__(self, text: str = "待配置", state: str = "idle"):
        super().__init__()
        self._state = "idle"
        self._text = text
        self.setFixedHeight(26)
        self._apply(state, text)

    def _apply(self, state: str, text: str) -> None:
        self._state = state
        self._text = text
        colors = {
            "ok":     (T.green,       T.green_soft),
            "busy":   (T.warn,        T.warn_soft),
            "error":  (T.danger,      T.danger_soft),
            "idle":   (T.ink_faint,   T.surface_alt),
        }
        dot, bg = colors.get(state, colors["idle"])
        self.setText(f"●   {text}")
        self.setStyleSheet(
            f"QLabel {{ background: {bg}; color: {dot}; padding: 4px 12px;"
            f" border-radius: {T.radius_pill}px; font-size: 11px; font-weight: 500;"
            f" letter-spacing: 0.2px; }}"
        )

    def set_state(self, state: str, text: str) -> None:
        self._apply(state, text)


class Card(QFrame):
    def __init__(self, title: str, step: str | None = None):
        super().__init__()
        self.setObjectName("card")
        self._layout = QVBoxLayout(self)
        self._layout.setContentsMargins(20, 18, 20, 18)
        self._layout.setSpacing(12)
        head = QHBoxLayout()
        head.setSpacing(8)
        if step:
            badge = QLabel(step)
            badge.setObjectName("stepBadge")
            badge.setFixedSize(22, 22)
            badge.setAlignment(Qt.AlignmentFlag.AlignCenter)
            head.addWidget(badge)
        label = QLabel(title)
        label.setObjectName("cardTitle")
        head.addWidget(label)
        head.addStretch(1)
        self._layout.addLayout(head)

    def add(self, w: QWidget) -> None:
        self._layout.addWidget(w)

    def add_layout(self, lay) -> None:
        self._layout.addLayout(lay)


class Worker(QThread):
    update = Signal(str)
    completed = Signal(object)
    failed = Signal(str)

    def __init__(self, workdir: Path, dry_run=False):
        super().__init__()
        self.workdir = workdir
        self.dry_run = dry_run
        self.stop_event = threading.Event()

    def run(self):
        try:
            result = engine.do_scan(self.workdir, stop=self.stop_event,
                                    log=self.update.emit, dry_run=self.dry_run)
            self.completed.emit(result)
        except Exception as exc:
            self.failed.emit(str(exc))


class DownloadWorker(QThread):
    """官方 cfst 下载（engine.download_cfst 的 GUI 包装）。"""
    update = Signal(str)
    completed = Signal(str)   # 目标路径
    failed = Signal(str)

    def __init__(self, workdir: Path):
        super().__init__()
        self.workdir = workdir
        self.stop_event = threading.Event()

    def run(self):
        try:
            def progress(done, total):
                if total:
                    pct = int(done * 100 / max(total, 1))
                    self.update.emit(f"下载中… {done/1e6:.1f}/{total/1e6:.1f} MB ({pct}%)")
            path = engine.download_cfst(self.workdir, log=self.update.emit,
                                        progress=progress, stop=self.stop_event)
            self.completed.emit(str(path))
        except Exception as exc:
            self.failed.emit(str(exc))


# ─────────────────────────────────────────────────────────────────────────────
# Main window
# ─────────────────────────────────────────────────────────────────────────────
class MainWindow(QMainWindow):
    def __init__(self):
        super().__init__()
        self.workdir = engine.app_home()
        self.workdir.mkdir(parents=True, exist_ok=True)
        self.config_path: Path | None = None
        self.worker: Worker | None = None
        self.download_worker: DownloadWorker | None = None
        self.provider: engine.ProviderServer | None = None
        self.background = "--background" in sys.argv
        self._exiting = False
        self._first_hide = True
        self._loading = True
        self.setWindowTitle("CF Auto Desktop")
        self.setWindowIcon(icon())
        self.resize(900, 860)
        self.setMinimumSize(760, 760)
        self._build_ui()
        self._build_tray()
        self._load_session()
        self.schedule = QTimer(self)
        self.schedule.setInterval(60_000)
        self.schedule.timeout.connect(self._maybe_auto_scan)
        self.schedule.start()
        QTimer.singleShot(4000, self._maybe_auto_scan)
        if not self.background:
            self.show()

    # ── UI ────────────────────────────────────────────────────────────────
    def _button(self, title: str, callback, kind: str = "primary",
                min_w: int = 0, min_h: int = 38) -> QPushButton:
        btn = QPushButton(title)
        btn.setProperty("kind", kind)
        btn.setCursor(Qt.CursorShape.PointingHandCursor)
        if min_w:
            btn.setMinimumWidth(min_w)
        btn.setMinimumHeight(min_h)
        btn.setSizePolicy(QSizePolicy.Policy.Preferred, QSizePolicy.Policy.Fixed)
        btn.clicked.connect(callback)
        return btn

    def _build_ui(self):
        self.setStyleSheet(self._stylesheet())
        body = QWidget()
        self.setCentralWidget(body)
        outer = QVBoxLayout(body)
        outer.setContentsMargins(28, 22, 28, 20)
        outer.setSpacing(14)

        # ── Header ─────────────────────────────────────────────────────
        header = QHBoxLayout()
        header.setSpacing(12)
        title_wrap = QVBoxLayout()
        title_wrap.setSpacing(2)
        title = QLabel("CF Auto Desktop")
        title.setObjectName("title")
        subtitle = QLabel("Cloudflare 候选动态优选  ·  Clash Party 节点池")
        subtitle.setObjectName("subtitle")
        title_wrap.addWidget(title)
        title_wrap.addWidget(subtitle)
        header.addLayout(title_wrap)
        header.addStretch(1)
        self.status_pill = StatusPill("待配置", "idle")
        header.addWidget(self.status_pill, alignment=Qt.AlignmentFlag.AlignVCenter)
        outer.addLayout(header)

        # ── Card 1 · 配置源 ─────────────────────────────────────────────
        card1 = Card("配置源 · 原始 Clash YAML", "1")
        row1 = QHBoxLayout()
        row1.setSpacing(10)
        self.config_label = QLabel("未导入（文件需含真实 UUID，本工具不上传）")
        self.config_label.setObjectName("muted")
        self.config_label.setWordWrap(True)
        self.config_label.setSizePolicy(QSizePolicy.Policy.Expanding, QSizePolicy.Policy.Preferred)
        row1.addWidget(self.config_label, stretch=1)
        row1.addWidget(self._button("导入 YAML", self._choose_yaml, "ghost", min_w=110))
        card1.add_layout(row1)

        row2 = QHBoxLayout()
        row2.setSpacing(10)
        node_lbl = QLabel("原节点")
        node_lbl.setObjectName("muted")
        row2.addWidget(node_lbl)
        self.nodes = QComboBox()
        self.nodes.setMinimumWidth(220)
        row2.addWidget(self.nodes, stretch=1)
        row2.addWidget(self._button("生成 Clash 配置", self._generate, "primary", min_w=150))
        card1.add_layout(row2)
        outer.addWidget(card1)

        # ── Card 2 · 测速工具 ──────────────────────────────────────────
        card2 = Card("测速工具 · CloudflareSpeedTest", "2")
        row3 = QHBoxLayout()
        row3.setSpacing(10)
        self.cfst_label = QLabel("未安装（可一键下载官方版，或手动导入你信任的 cfst）")
        self.cfst_label.setObjectName("muted")
        self.cfst_label.setWordWrap(True)
        self.cfst_label.setSizePolicy(QSizePolicy.Policy.Expanding, QSizePolicy.Policy.Preferred)
        row3.addWidget(self.cfst_label, stretch=1)
        self.download_cfst_button = self._button("一键下载官方 cfst", self._download_cfst, "primary", min_w=150)
        row3.addWidget(self.download_cfst_button)
        row3.addWidget(self._button("手动导入", self._choose_cfst, "ghost", min_w=100))
        card2.add_layout(row3)
        outer.addWidget(card2)

        # ── Card 3 · 扫描设置（只放设置，不放操作按钮）──────────────────
        card3 = Card("扫描设置", "3")
        set_row1 = QHBoxLayout()
        set_row1.setSpacing(10)
        self.auto_check = QCheckBox("定时扫描并自动更新候选池")
        self.auto_check.toggled.connect(self._save_settings)
        set_row1.addWidget(self.auto_check)
        set_row1.addStretch(1)
        card3.add_layout(set_row1)

        set_row2 = QHBoxLayout()
        set_row2.setSpacing(10)
        interval_lbl = QLabel("扫描间隔")
        interval_lbl.setObjectName("muted")
        set_row2.addWidget(interval_lbl)
        self.interval = QComboBox()
        self.interval.setMinimumWidth(120)
        for value in engine.INTERVAL_CHOICES:
            self.interval.addItem(f"每 {value} 小时", value)
        self.interval.currentIndexChanged.connect(self._save_settings)
        set_row2.addWidget(self.interval)
        set_row2.addSpacing(18)
        mode_lbl = QLabel("扫描强度")
        mode_lbl.setObjectName("muted")
        set_row2.addWidget(mode_lbl)
        self.mode = QComboBox()
        self.mode.setMinimumWidth(180)
        self.mode.addItem("标准（更全面）", "standard")
        self.mode.addItem("轻量（省电省流量）", "light")
        self.mode.currentIndexChanged.connect(self._save_settings)
        set_row2.addWidget(self.mode)
        set_row2.addStretch(1)
        card3.add_layout(set_row2)

        self.login_check = QCheckBox("登录 Mac 后自动启动（菜单栏常驻）")
        self.login_check.toggled.connect(self._toggle_login)
        self.login_check.setEnabled(sys.platform == "darwin")
        card3.add(self.login_check)
        outer.addWidget(card3)

        # ── Card 4 · 手动操作（按钮独立一卡，避免被设置项挤压）────────
        card4 = Card("手动操作", "4")
        actions = QHBoxLayout()
        actions.setSpacing(10)
        self.scan_button = self._button("立即扫描并应用", self._start_scan, "primary", min_w=170)
        self.preview_button = self._button("仅预览结果", lambda: self._start_scan(preview=True), "ghost", min_w=130)
        self.stop_button = self._button("停止扫描", self._stop_scan, "ghost", min_w=110)
        self.stop_button.setEnabled(False)
        actions.addWidget(self.scan_button)
        actions.addWidget(self.preview_button)
        actions.addWidget(self.stop_button)
        actions.addStretch(1)
        card4.add_layout(actions)

        utilities = QHBoxLayout()
        utilities.setSpacing(8)
        utilities.addWidget(self._button("打开生成的配置", self._open_config, "quiet", min_w=130))
        utilities.addWidget(self._button("回滚节点池", self._rollback, "quiet", min_w=110))
        utilities.addWidget(self._button("打开工作目录", self._open_folder, "quiet", min_w=120))
        utilities.addStretch(1)
        card4.add_layout(utilities)
        outer.addWidget(card4)

        # ── Log panel ──────────────────────────────────────────────────
        log_head = QHBoxLayout()
        log_title = QLabel("运行日志")
        log_title.setObjectName("sectionTitle")
        log_head.addWidget(log_title)
        log_head.addStretch(1)
        log_hint = QLabel("不显示 UUID  ·  仅本地")
        log_hint.setObjectName("muted")
        log_head.addWidget(log_hint)
        outer.addLayout(log_head)

        self.logs = QPlainTextEdit()
        self.logs.setReadOnly(True)
        self.logs.setObjectName("logPanel")
        self.logs.setMinimumHeight(120)
        outer.addWidget(self.logs, stretch=1)

        # ── Footer note ────────────────────────────────────────────────
        footer = QLabel(
            "Provider 仅监听 127.0.0.1，URL 带随机令牌  ·  配置不上传  ·  "
            "测速时建议让 cfst 与本工具直连以避开 Clash Party 的 TUN"
        )
        footer.setObjectName("footer")
        footer.setWordWrap(True)
        outer.addWidget(footer)

    def _stylesheet(self) -> str:
        return f"""
        /* ── Canvas & type ─────────────────────────────────────── */
        QWidget {{
            background: {T.canvas};
            color: {T.ink};
            font-family: ".AppleSystemUIFont", "SF Pro Text", "PingFang SC",
                         "Helvetica Neue", "Microsoft YaHei", sans-serif;
            font-size: 13px;
            outline: none;
        }}
        QLabel#title    {{ font-size: 22px; font-weight: 600; color: {T.ink};
                          letter-spacing: -0.3px; background: transparent; }}
        QLabel#subtitle {{ font-size: 12px; color: {T.ink_muted}; background: transparent; }}
        QLabel#cardTitle{{ font-size: 14px; font-weight: 550; color: {T.ink};
                          background: transparent; }}
        QLabel#sectionTitle {{ font-size: 12px; font-weight: 600; color: {T.ink_muted};
                              letter-spacing: 0.3px; text-transform: uppercase;
                              background: transparent; }}
        QLabel#muted    {{ font-size: 12px; color: {T.ink_muted}; background: transparent; }}
        QLabel#footer   {{ font-size: 11px; color: {T.ink_faint}; background: transparent;
                          padding-top: 2px; }}
        QLabel#stepBadge {{
            background: {T.surface_alt}; color: {T.ink_muted};
            border-radius: 11px; font-size: 11px; font-weight: 600;
            background: transparent;
        }}

        /* ── Cards: hairline on off-white, almost no shadow ───── */
        QFrame#card {{
            background: {T.surface};
            border: 1px solid {T.border};
            border-radius: {T.radius_card}px;
        }}

        /* ── Buttons: three tiers (primary / ghost / quiet) ───── */
        QPushButton {{
            border: 1px solid transparent;
            border-radius: {T.radius_in}px;
            padding: 9px 16px;
            font-size: 13px;
            font-weight: 500;
            background: transparent;
            color: {T.ink};
        }}
        QPushButton[kind="primary"] {{
            background: {T.primary};
            color: #FFFFFF;
            border-radius: {T.radius_pill}px;
            padding: 10px 18px;
            font-weight: 550;
        }}
        QPushButton[kind="primary"]:hover    {{ background: {T.primary_hv}; }}
        QPushButton[kind="primary"]:pressed  {{ background: {T.primary_dk}; }}
        QPushButton[kind="primary"]:disabled {{ background: {T.border_str};
                                               color: {T.canvas}; }}

        QPushButton[kind="ghost"] {{
            background: {T.surface};
            color: {T.ink};
            border: 1px solid {T.border_str};
        }}
        QPushButton[kind="ghost"]:hover    {{ background: {T.surface_alt}; }}
        QPushButton[kind="ghost"]:pressed  {{ background: {T.border}; }}
        QPushButton[kind="ghost"]:disabled {{ color: {T.ink_faint};
                                              border-color: {T.border}; }}

        QPushButton[kind="quiet"] {{
            background: transparent;
            color: {T.ink_muted};
            border: 1px solid transparent;
            padding: 7px 12px;
            font-size: 12px;
        }}
        QPushButton[kind="quiet"]:hover {{
            background: {T.surface_alt};
            color: {T.ink};
        }}

        /* ── Inputs ───────────────────────────────────────────── */
        QComboBox {{
            background: {T.surface};
            color: {T.ink};
            border: 1px solid {T.border_str};
            border-radius: {T.radius_in}px;
            padding: 7px 10px;
            min-height: 18px;
        }}
        QComboBox:hover  {{ border-color: {T.ink_faint}; }}
        QComboBox:focus  {{ border-color: {T.ink}; }}
        QComboBox::drop-down {{ border: 0; width: 22px; }}
        QComboBox QAbstractItemView {{
            background: {T.surface};
            border: 1px solid {T.border};
            border-radius: 8px;
            selection-background-color: {T.surface_alt};
            selection-color: {T.ink};
            padding: 4px;
        }}

        QCheckBox {{ spacing: 8px; background: transparent; }}
        QCheckBox::indicator {{
            width: 16px; height: 16px;
            border: 1.5px solid {T.border_str};
            border-radius: 4px;
            background: {T.surface};
        }}
        QCheckBox::indicator:checked {{
            background: {T.primary};
            border-color: {T.primary};
        }}
        QCheckBox::indicator:hover {{ border-color: {T.ink_faint}; }}

        /* ── Log: quiet dark inset (signature) ────────────────── */
        QPlainTextEdit#logPanel {{
            background: {T.log_bg};
            color: {T.log_ink};
            border: 1px solid #2A2A2A;
            border-radius: 10px;
            padding: 12px;
            font-family: "SF Mono", Menlo, Monaco, "Cascadia Code", monospace;
            font-size: 11px;
            selection-background-color: #333333;
        }}

        /* ── Scrollbar ────────────────────────────────────────── */
        QScrollBar:vertical {{
            background: transparent;
            width: 10px; margin: 2px;
        }}
        QScrollBar::handle:vertical {{
            background: {T.border_str};
            border-radius: 5px; min-height: 24px;
        }}
        QScrollBar::handle:vertical:hover {{ background: {T.ink_faint}; }}
        QScrollBar::add-line, QScrollBar::sub-line {{ height: 0; }}
        QScrollBar:horizontal {{ height: 10px; margin: 2px; }}
        QScrollBar::handle:horizontal {{
            background: {T.border_str}; border-radius: 5px; min-width: 24px;
        }}
        """

    # ── Tray ────────────────────────────────────────────────────────────
    def _build_tray(self):
        self.tray = QSystemTrayIcon(icon(), self)
        menu = QMenu()
        menu.setStyleSheet(f"""
            QMenu {{ background: {T.surface}; color: {T.ink};
                     border: 1px solid {T.border}; border-radius: 8px;
                     padding: 6px; font-size: 13px; }}
            QMenu::item {{ padding: 8px 18px; border-radius: 6px; }}
            QMenu::item:selected {{ background: {T.surface_alt}; }}
            QMenu::separator {{ height: 1px; background: {T.border};
                                margin: 5px 10px; }}
        """)
        for name, handler in (("打开控制台", self._show_window),
                              ("立即扫描", self._start_scan),
                              ("停止扫描", self._stop_scan),
                              ("退出程序", self._quit_app)):
            action = QAction(name, self)
            action.triggered.connect(handler)
            menu.addAction(action)
        self.tray.setContextMenu(menu)
        self.tray.setToolTip("CF Auto Desktop — 等待配置")
        self.tray.activated.connect(lambda reason: self._show_window()
                            if reason == QSystemTrayIcon.ActivationReason.Trigger else None)
        if QSystemTrayIcon.isSystemTrayAvailable():
            self.tray.show()
        else:
            self._log("当前环境没有可用菜单栏托盘，窗口模式仍可运行。")
            self.background = False

    # ── State ───────────────────────────────────────────────────────────
    def _log(self, message: str):
        self.logs.appendPlainText(time.strftime("%H:%M:%S  ") + message)
        # Memory-friendly: keep only the last 200 blocks; earlier ones are noise.
        if self.logs.blockCount() > 200:
            self.logs.clear()
            self.logs.appendPlainText("[日志已自动截断]")

    # ── 隐私脱敏的标签 ─────────────────────────────────────────────────
    def _set_config_label(self, node_name: str, domain: str | None) -> None:
        """UI 上不显示真实 SNI 域名（截图/演示会泄露）；完整信息只放 tooltip。"""
        shown = f"已生成：{node_name}"
        tip = f"节点：{node_name}\nSNI 域名：{domain or '（未知）'}"
        self.config_label.setText(shown)
        self.config_label.setToolTip(tip)

    def _set_cfst_label(self, bundle_dir: Path | None, extra: str = "") -> None:
        """UI 上不显示绝对路径；完整路径只放 tooltip。"""
        if bundle_dir is None:
            self.cfst_label.setText("未安装（可一键下载官方版，或手动导入你信任的 cfst）")
            self.cfst_label.setToolTip("")
            return
        name = bundle_dir.name or "cfst-bundle"
        text = f"已安装 · {name}" + (f" · {extra}" if extra else "")
        self.cfst_label.setText(text)
        self.cfst_label.setToolTip(f"工作区位置：{bundle_dir}")

    def _status(self, msg: str, state: str = "idle"):
        self.status_pill.set_state(state, msg)
        self.tray.setToolTip("CF Auto Desktop — " + msg)

    def _load_session(self):
        prefs = engine.read_prefs(self.workdir)
        self._loading = True
        self.auto_check.setChecked(bool(prefs.get("auto_scan")))
        value = int(prefs.get("every_hours", 6))
        idx = self.interval.findData(value)
        self.interval.setCurrentIndex(idx if idx >= 0 else 1)
        mode_val = prefs.get("scan_mode", "standard")
        mode_idx = self.mode.findData(mode_val)
        self.mode.setCurrentIndex(mode_idx if mode_idx >= 0 else 0)
        self.login_check.setChecked(login_enabled())
        self._loading = False
        chosen = prefs.get("cfst") or ""
        if chosen and Path(chosen).is_file():
            self._set_cfst_label(Path(chosen).parent)
        state = engine.load_state(self.workdir, required=False)
        if state:
            self._set_config_label(state.get("node_name", "已配置"), state.get("domain"))
            self.nodes.addItem(state["node_name"])
            self._start_provider()
            self._log("已加载上次配置；可在 Clash Party 导入生成的 clash-auto.yaml。")
        else:
            self._status("待配置", "idle")
            self._log("请先导入 YAML 与 CloudflareSpeedTest。")
        self._log("关闭窗口会隐藏到菜单栏，后台任务继续运行。")

    def _start_provider(self):
        if self.provider:
            self.provider.stop()
        try:
            self.provider = engine.ProviderServer(self.workdir)
            self.provider.start()
            self._status("运行中 · 等待扫描", "ok")
            self._log(f"本地 Provider 已启动：127.0.0.1:{engine.PORT}")
        except Exception as exc:
            self.provider = None
            self._status("Provider 启动失败", "error")
            self._log(f"Provider 启动失败：{exc}（请检查是否仍运行旧 cf-auto serve）")

    # ── Actions ─────────────────────────────────────────────────────────
    def _choose_yaml(self):
        filename, _ = QFileDialog.getOpenFileName(self, "选择原始 Clash YAML", str(Path.home()),
                                                   "Clash 配置 (*.yaml *.yml);;所有文件 (*)")
        if not filename:
            return
        try:
            raw = engine.load_yaml(Path(filename))
            names = [n["name"] for n in engine.eligible_nodes(raw)]
            if not names:
                raise ValueError("没有找到直接写在 proxies 中的 VLESS + WS + TLS 节点")
            self.config_path = Path(filename)
            self.nodes.clear()
            self.nodes.addItems(names)
            # Current user may import earlier optimized YAML with CF-HKG-A/B.
            # Prefer its original DNS-address node over historical fixed-IP copies.
            preferred = next((n for n in engine.eligible_nodes(raw)
                              if not engine.ipv4(str(n.get("server", "")))), None)
            if preferred:
                self.nodes.setCurrentText(preferred["name"])
            self._set_config_label(self.nodes.currentText() or "已选择", None)
            self.config_label.setToolTip(f"来源文件：{filename}\n（未显示 SNI，仅展示节点名）")
            self._status("YAML 已导入", "ok")
            self._log("原始 YAML 验证成功。请选择节点，再点击「生成 Clash 配置」。")
        except Exception as exc:
            QMessageBox.warning(self, "导入失败", str(exc))

    def _download_cfst(self):
        """从官方 GitHub Releases 一键下载 cfst（含 SHA256 校验）。"""
        if self.download_worker and self.download_worker.isRunning():
            self._log("已有下载任务在进行。")
            return
        reply = QMessageBox.question(
            self, "官方下载确认",
            "将从 GitHub 官方仓库 XIU2/CloudflareSpeedTest 的 Releases 下载 cfst。\n"
            "· 仅使用 HTTPS，并校验 GitHub 提供的 SHA256\n"
            "· 只解压 cfst 与 ip.txt 两个文件，不执行任何脚本\n"
            "· 校验失败会中止，不会写入你的工作区\n\n"
            "继续吗？",
            QMessageBox.StandardButton.Yes | QMessageBox.StandardButton.No,
            QMessageBox.StandardButton.Yes,
        )
        if reply != QMessageBox.StandardButton.Yes:
            return
        self.download_cfst_button.setEnabled(False)
        self._status("正在下载 cfst…", "busy")
        self._log("已请求官方下载；详见下方日志。")
        self.download_worker = DownloadWorker(self.workdir)
        self.download_worker.update.connect(self._log)
        self.download_worker.completed.connect(self._download_complete)
        self.download_worker.failed.connect(self._download_failed)
        self.download_worker.finished.connect(self._download_finished)
        self.download_worker.start()

    def _download_complete(self, path: str):
        self._set_cfst_label(Path(path).parent, "官方")
        self._status("cfst 已安装", "ok")
        self._log("官方 cfst 安装完成。")
        if engine.load_state(self.workdir, required=False) and engine.read_prefs(self.workdir).get("auto_scan"):
            QTimer.singleShot(900, self._start_scan)

    def _download_failed(self, msg: str):
        self._status("cfst 下载失败 · 可改用手动导入", "error")
        self._log("下载失败：" + msg)
        QMessageBox.warning(self, "下载失败",
                            msg + "\n\n可改用「手动导入」从官方 Releases 页面下载后选择文件。")

    def _download_finished(self):
        self.download_cfst_button.setEnabled(True)
        self.download_worker = None

    def _choose_cfst(self):
        filename, _ = QFileDialog.getOpenFileName(self, "选择官方 CFST 可执行文件", str(Path.home()),
                                                   "可执行文件 (*);;全部文件 (*)")
        if not filename:
            return
        proceed = QMessageBox.question(self, "安全确认", "仅导入你信任的 CloudflareSpeedTest 文件。\n"
              "应用会复制 cfst + ip.txt 到私有目录，并在点击扫描时执行它。继续吗？")
        if proceed != QMessageBox.StandardButton.Yes:
            return
        try:
            target = engine.import_cfst(self.workdir, Path(filename))
            self._set_cfst_label(target.parent, "手动")
            self._log("测速程序已导入（没有在导入时执行）。")
            if engine.load_state(self.workdir, required=False) and engine.read_prefs(self.workdir).get("auto_scan"):
                QTimer.singleShot(900, self._start_scan)
        except Exception as exc:
            QMessageBox.warning(self, "导入失败", str(exc))

    def _generate(self):
        if not self.config_path:
            QMessageBox.information(self, "提示", "请点击「导入 YAML」选择原始配置文件。")
            return
        try:
            path = engine.setup(self.workdir, self.config_path, self.nodes.currentText())
            self._start_provider()
            self._log("已生成：" + str(path))
            self._log("请将该文件作为新配置导入 Clash Party，并选择「CF动态容灾」。")
            if engine.read_prefs(self.workdir).get("cfst") and engine.read_prefs(self.workdir).get("auto_scan"):
                QTimer.singleShot(900, self._start_scan)
            QMessageBox.information(self, "已生成", f"新配置位置：\n{path}\n\n"
                "请在 Clash Party 导入一次，并选择 CF动态容灾。\n"
                "不要继续叠加此前的 CF JavaScript 覆写。")
        except Exception as exc:
            QMessageBox.warning(self, "生成失败", str(exc))

    def _save_settings(self):
        if getattr(self, "_loading", False):
            return
        engine.update_prefs(self.workdir,
                            auto_scan=self.auto_check.isChecked(),
                            every_hours=self.interval.currentData() or 6,
                            scan_mode=self.mode.currentData() or "standard")
        self._log("设置已保存。")

    def _toggle_login(self, wanted: bool):
        if getattr(self, "_loading", False):
            return
        try:
            set_login(wanted)
            self._log("开机登录启动：" + ("已开启" if wanted else "已关闭"))
        except Exception as exc:
            QMessageBox.warning(self, "启动项提示", str(exc))
            self.login_check.blockSignals(True)
            self.login_check.setChecked(login_enabled())
            self.login_check.blockSignals(False)

    def _start_scan(self, checked=False, preview=False):
        if self.worker and self.worker.isRunning():
            return
        if not engine.load_state(self.workdir, required=False):
            self._log("请先生成 Clash 配置。")
            return
        prefs = engine.read_prefs(self.workdir)
        if not prefs.get("cfst") or not Path(prefs["cfst"]).is_file():
            self._log("请先导入 CloudflareSpeedTest。")
            return
        self.scan_button.setEnabled(False)
        self.preview_button.setEnabled(False)
        self.stop_button.setEnabled(True)
        self._status("扫描中" + ("（仅预览）" if preview else ""), "busy")
        self.worker = Worker(self.workdir, dry_run=preview)
        self.worker.update.connect(self._log)
        self.worker.completed.connect(self._scan_complete)
        self.worker.failed.connect(self._scan_failed)
        self.worker.finished.connect(self._scan_finished)
        self.worker.start()

    def _scan_complete(self, report: dict):
        count = len(report["chosen"])
        self._log(f"扫描结束：{report['qualified']} 个合格，暂选 {count} 个 IP。")
        self._status(f"完成 · 候选 {count} 个", "ok")
        if self.tray.isVisible():
            self.tray.showMessage("CF Auto Desktop", f"扫描结束，候选 {count} 个 IP。",
                                  QSystemTrayIcon.MessageIcon.Information, 4000)

    def _scan_failed(self, msg: str):
        engine.update_prefs(self.workdir, last_status="扫描异常：" + msg[:100])
        self._log("扫描失败：" + msg)
        self._status("扫描失败 · 保留旧池", "error")
        if self.tray.isVisible():
            self.tray.showMessage("CF Auto Desktop", "扫描失败；原节点池未覆盖。",
                                  QSystemTrayIcon.MessageIcon.Warning, 5000)

    def _scan_finished(self):
        self.scan_button.setEnabled(True)
        self.preview_button.setEnabled(True)
        self.stop_button.setEnabled(False)
        self.worker = None

    def _stop_scan(self):
        if self.worker and self.worker.isRunning():
            self.worker.stop_event.set()
            self._log("正在停止扫描（已有握手任务可能需要数秒结束）…")

    def _maybe_auto_scan(self):
        if self.worker and self.worker.isRunning():
            return
        prefs = engine.read_prefs(self.workdir)
        if not prefs.get("auto_scan") or not engine.load_state(self.workdir, required=False):
            return
        if not prefs.get("cfst") or not Path(prefs["cfst"]).is_file():
            return
        elapsed = time.time() - float(prefs.get("last_attempt") or 0)
        if elapsed >= int(prefs.get("every_hours", 6)) * 3600:
            self._log("到达定时扫描时间。")
            self._start_scan()

    def _rollback(self):
        if self.worker and self.worker.isRunning():
            self._log("请先停止扫描再回滚。")
            return
        if QMessageBox.question(self, "确认回滚", "恢复上一次候选池吗？现有配置将备份，可再次回滚切换。") \
                != QMessageBox.StandardButton.Yes:
            return
        try:
            if engine.rollback(self.workdir):
                self._log("回滚完成。Provider 将在 Clash Party 下次刷新时生效。")
            else:
                self._log("尚无可回滚历史。")
        except Exception as exc:
            QMessageBox.warning(self, "回滚失败", str(exc))

    def _open_folder(self):
        self.workdir.mkdir(parents=True, exist_ok=True)
        if sys.platform == "darwin":
            subprocess.Popen(["open", str(self.workdir)])
        else:
            self._log("工作目录：" + str(self.workdir))

    def _open_config(self):
        path = self.workdir / "clash-auto.yaml"
        if not path.is_file():
            self._log("请先生成 Clash 配置。")
            return
        QApplication.clipboard().setText(str(path))
        self._log("配置路径已复制到剪贴板。")
        self._open_folder()

    def _show_window(self):
        self.show()
        self.raise_()
        self.activateWindow()

    def _quit_app(self):
        if self.worker and self.worker.isRunning():
            self._stop_scan()
            self._log("停止任务后请再点击退出（避免留下测速进程）。")
            self._show_window()
            return
        self._exiting = True
        if self.provider:
            self.provider.stop()
        self.tray.hide()
        QApplication.instance().quit()

    def closeEvent(self, event):
        if not self._exiting and self.tray.isVisible():
            event.ignore()
            self.hide()
            if self._first_hide:
                self.tray.showMessage("CF Auto Desktop", "程序仍在菜单栏运行。右键菜单可退出。",
                                      QSystemTrayIcon.MessageIcon.Information, 3000)
                self._first_hide = False
        else:
            if self.provider:
                self.provider.stop()
            event.accept()
            QApplication.instance().quit()


def main():
    app = QApplication(sys.argv)
    app.setApplicationName("CF Auto Desktop")
    app.setQuitOnLastWindowClosed(False)
    app.setWindowIcon(icon())
    _apply_dark_palette(app)
    # Single instance prevents two providers, especially when launchctl bootstrap
    # starts another instance on login-item activation.
    directory = engine.app_home()
    directory.mkdir(parents=True, exist_ok=True)
    lock = (directory / "instance.lock").open("a+")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        QMessageBox.information(None, "CF Auto Desktop", "程序已经在运行。请查看 macOS 菜单栏。")
        return
    window = MainWindow()
    app.exec()
    fcntl.flock(lock, fcntl.LOCK_UN)
    lock.close()


if __name__ == "__main__":
    main()
