#!/usr/bin/env python3
"""CF Auto Desktop — macOS menu bar + window UI for local Clash Party configs."""
from __future__ import annotations

import fcntl
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import threading
import time

from PySide6.QtCore import Qt, QThread, QTimer, Signal, QSize
from PySide6.QtGui import QAction, QBrush, QColor, QFont, QIcon, QPainter, QPen
from PySide6.QtWidgets import (
    QApplication, QCheckBox, QComboBox, QFileDialog, QFrame, QGridLayout,
    QHBoxLayout, QLabel, QMainWindow, QMenu, QMessageBox, QPushButton,
    QPlainTextEdit, QSystemTrayIcon, QVBoxLayout, QWidget
)

import engine

LAUNCH_LABEL = "com.cf-auto-desktop.menubar"


def icon() -> QIcon:
    from PySide6.QtGui import QPixmap
    pix = QPixmap(128, 128)
    pix.fill(Qt.GlobalColor.transparent)
    p = QPainter(pix)
    p.setRenderHint(QPainter.RenderHint.Antialiasing)
    p.setBrush(QBrush(QColor("#168B83")))
    p.setPen(Qt.PenStyle.NoPen)
    p.drawRoundedRect(3, 3, 122, 122, 30, 30)
    p.setPen(QPen(QColor("#FFFFFF")))
    font = QFont("Arial", 44, QFont.Weight.Bold)
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


class MainWindow(QMainWindow):
    def __init__(self):
        super().__init__()
        self.workdir = engine.app_home()
        self.workdir.mkdir(parents=True, exist_ok=True)
        self.config_path: Path | None = None
        self.worker: Worker | None = None
        self.provider: engine.ProviderServer | None = None
        self.background = "--background" in sys.argv
        self._exiting = False
        self._first_hide = True
        self._loading = True
        self.setWindowTitle("CF Auto Desktop · Clash Party 动态优选")
        self.setWindowIcon(icon())
        self.resize(870, 720)
        self.setMinimumSize(740, 590)
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

    def _button(self, title: str, callback, secondary=False) -> QPushButton:
        btn = QPushButton(title)
        btn.setProperty("secondary", secondary)
        btn.setCursor(Qt.CursorShape.PointingHandCursor)
        btn.clicked.connect(callback)
        return btn

    def _build_ui(self):
        body = QWidget()
        self.setCentralWidget(body)
        body.setStyleSheet("""
        QWidget { background: #F6F8F8; color: #17343A; font-size: 13px; }
        QFrame#card { background: white; border: 1px solid #E1E8E8;
                      border-radius: 14px; }
        QLabel#title {font-size: 27px; font-weight: bold; color: #103C40;}
        QLabel#subtitle {font-size: 12px; color: #648082;}
        QLabel#heading {font-size: 15px; font-weight: bold; color: #1D484B;}
        QLabel#value {font-size: 13px; color: #346267;}
        QLabel#status {font-weight: bold; color: #117B65;}
        QPushButton { background: #127E78; color: white; border: 0;
                      border-radius: 8px; padding: 11px 15px; font-weight: 600; }
        QPushButton:hover {background:#0C6762;}
        QPushButton:disabled {background:#A7BDBC;}
        QPushButton[secondary="true"] {background: #E8F1F0; color: #146C68;}
        QPushButton[secondary="true"]:hover {background:#D2E7E4;}
        QPlainTextEdit {background: #102A32; color:#D7EAE7; border:0;
                         border-radius:10px; padding:12px;
                         font-family: Menlo, Monaco, monospace; font-size:11px;}
        QComboBox {background: white; padding:7px; border:1px solid #CCDADB; border-radius:6px;}
        QCheckBox {spacing:8px;}
        """)
        layout = QVBoxLayout(body)
        layout.setContentsMargins(25, 21, 25, 20)
        layout.setSpacing(13)
        title = QLabel("CF Auto Desktop")
        title.setObjectName("title")
        subtitle = QLabel("macOS 菜单栏常驻 · Cloudflare 候选筛选 · Clash Party 动态节点池")
        subtitle.setObjectName("subtitle")
        layout.addWidget(title)
        layout.addWidget(subtitle)
        self.status = QLabel("● 等待初始化")
        self.status.setObjectName("status")
        layout.addWidget(self.status)

        config_box = QFrame(); config_box.setObjectName("card")
        config_grid = QGridLayout(config_box)
        config_grid.setContentsMargins(18, 15, 18, 15)
        config_grid.setHorizontalSpacing(12)
        ctitle = QLabel("① 导入当前 Clash Party 配置")
        ctitle.setObjectName("heading")
        config_grid.addWidget(ctitle, 0, 0, 1, 3)
        self.config_label = QLabel("未导入 YAML（包含真实 UUID 的本地文件）")
        self.config_label.setObjectName("value")
        self.config_label.setWordWrap(True)
        config_grid.addWidget(self.config_label, 1, 0, 1, 2)
        config_grid.addWidget(self._button("导入 YAML", self._choose_yaml), 1, 2)
        config_grid.addWidget(QLabel("原节点："), 2, 0)
        self.nodes = QComboBox()
        config_grid.addWidget(self.nodes, 2, 1)
        config_grid.addWidget(self._button("生成 Clash 配置", self._generate), 2, 2)
        layout.addWidget(config_box)

        tool_box = QFrame(); tool_box.setObjectName("card")
        tool_layout = QGridLayout(tool_box)
        tool_layout.setContentsMargins(18, 15, 18, 15)
        heading = QLabel("② 导入 CloudflareSpeedTest")
        heading.setObjectName("heading")
        tool_layout.addWidget(heading, 0, 0, 1, 3)
        self.cfst_label = QLabel("未导入（请选择下载包中的 cfst，需同目录包含 ip.txt）")
        self.cfst_label.setObjectName("value")
        self.cfst_label.setWordWrap(True)
        tool_layout.addWidget(self.cfst_label, 1, 0, 1, 2)
        tool_layout.addWidget(self._button("导入测速程序", self._choose_cfst), 1, 2)
        layout.addWidget(tool_box)

        action_box = QFrame(); action_box.setObjectName("card")
        action_grid = QGridLayout(action_box)
        action_grid.setContentsMargins(18, 15, 18, 15)
        heading = QLabel("③ 自动运行")
        heading.setObjectName("heading")
        action_grid.addWidget(heading, 0, 0, 1, 3)
        self.auto_check = QCheckBox("定时扫描并自动更新候选池")
        self.auto_check.toggled.connect(self._save_settings)
        action_grid.addWidget(self.auto_check, 1, 0, 1, 2)
        self.interval = QComboBox()
        for value in engine.INTERVAL_CHOICES:
            self.interval.addItem(f"每 {value} 小时", value)
        self.interval.currentIndexChanged.connect(self._save_settings)
        action_grid.addWidget(self.interval, 1, 2)
        self.login_check = QCheckBox("登录 Mac 后自动启动（菜单栏常驻）")
        self.login_check.toggled.connect(self._toggle_login)
        self.login_check.setEnabled(sys.platform == "darwin")
        action_grid.addWidget(self.login_check, 2, 0, 1, 3)
        self.scan_button = self._button("立即扫描并应用", self._start_scan)
        self.preview_button = self._button("仅预览结果", lambda: self._start_scan(preview=True), True)
        self.stop_button = self._button("停止扫描", self._stop_scan, True)
        self.stop_button.setEnabled(False)
        action_grid.addWidget(self.scan_button, 3, 0)
        action_grid.addWidget(self.preview_button, 3, 1)
        action_grid.addWidget(self.stop_button, 3, 2)
        action_grid.addWidget(self._button("打开生成的配置", self._open_config, True), 4, 0)
        action_grid.addWidget(self._button("回滚节点池", self._rollback, True), 4, 1)
        action_grid.addWidget(self._button("打开工作目录", self._open_folder, True), 4, 2)
        layout.addWidget(action_box)

        logtitle = QLabel("运行日志（不显示 UUID）")
        logtitle.setObjectName("heading")
        layout.addWidget(logtitle)
        self.logs = QPlainTextEdit()
        self.logs.setReadOnly(True)
        self.logs.setMinimumHeight(112)
        layout.addWidget(self.logs, stretch=1)
        info = QLabel("安全：Provider 仅监听 127.0.0.1，随机 URL；不上传配置。测速时注意绕开 Clash Party 的 TUN。")
        info.setObjectName("subtitle")
        info.setWordWrap(True)
        layout.addWidget(info)

    def _build_tray(self):
        self.tray = QSystemTrayIcon(icon(), self)
        menu = QMenu()
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

    def _log(self, message: str):
        self.logs.appendPlainText(time.strftime("%H:%M:%S  ") + message)
        if self.logs.blockCount() > 400:
            self.logs.clear()
            self.logs.appendPlainText("[旧日志已清理]")

    def _status(self, msg: str):
        self.status.setText("● " + msg)
        self.tray.setToolTip("CF Auto Desktop — " + msg)

    def _load_session(self):
        prefs = engine.read_prefs(self.workdir)
        self._loading = True
        self.auto_check.setChecked(bool(prefs.get("auto_scan")))
        value = int(prefs.get("every_hours", 6))
        idx = self.interval.findData(value)
        self.interval.setCurrentIndex(idx if idx >= 0 else 1)
        self.login_check.setChecked(login_enabled())
        self._loading = False
        chosen = prefs.get("cfst") or ""
        if chosen and Path(chosen).is_file():
            self.cfst_label.setText("已导入：" + str(Path(chosen).parent))
        state = engine.load_state(self.workdir, required=False)
        if state:
            self.config_label.setText(f"已生成：{state['node_name']}  ·  {state['domain']}")
            self.nodes.addItem(state["node_name"])
            self._start_provider()
            self._log("已加载上次配置；可在 Clash Party 导入生成的 clash-auto.yaml。")
        else:
            self._status("请先导入 YAML 和测速程序")
        self._log("将窗口关闭会隐藏到菜单栏，不会结束后台任务。")

    def _start_provider(self):
        if self.provider:
            self.provider.stop()
        try:
            self.provider = engine.ProviderServer(self.workdir)
            self.provider.start()
            self._status("Provider 运行中 · 等待扫描")
            self._log(f"本地 Provider 已启动：127.0.0.1:{engine.PORT}")
        except Exception as exc:
            self.provider = None
            self._status("Provider 启动失败")
            self._log(f"Provider 启动失败：{exc}（请检查是否仍运行旧 cf-auto serve）")

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
            self.config_label.setText(filename)
            self._log("原始 YAML 验证成功。请选择节点，再点击「生成 Clash 配置」。")
        except Exception as exc:
            QMessageBox.warning(self, "导入失败", str(exc))

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
            self.cfst_label.setText("已导入：" + str(target.parent))
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
        engine.update_prefs(self.workdir, auto_scan=self.auto_check.isChecked(),
                            every_hours=self.interval.currentData() or 6)
        self._log("自动扫描设置已保存。")

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
        self._status("扫描中" + ("（仅预览）" if preview else ""))
        self.worker = Worker(self.workdir, dry_run=preview)
        self.worker.update.connect(self._log)
        self.worker.completed.connect(self._scan_complete)
        self.worker.failed.connect(self._scan_failed)
        self.worker.finished.connect(self._scan_finished)
        self.worker.start()

    def _scan_complete(self, report: dict):
        count = len(report["chosen"])
        self._log(f"扫描结束：{report['qualified']} 个合格，暂选 {count} 个 IP。")
        self._status(f"扫描完成 · 候选 {count} 个")
        if self.tray.isVisible():
            self.tray.showMessage("CF Auto Desktop", f"扫描结束，候选 {count} 个 IP。",
                                  QSystemTrayIcon.MessageIcon.Information, 4000)

    def _scan_failed(self, msg: str):
        engine.update_prefs(self.workdir, last_status="扫描异常：" + msg[:100])
        self._log("扫描失败：" + msg)
        self._status("扫描失败 · 保留旧节点池")
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
