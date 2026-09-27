"""Backend for CF Auto Desktop. No Qt dependency, safe to test headlessly.

CloudflareSpeedTest only produces candidate IPs. This module validates the real
TLS certificate + domain SNI + RFC6455 WebSocket upgrade before publishing.
Mihomo performs separate end-to-end proxy URL health checks after provider refresh.
"""
from __future__ import annotations

import base64
import copy
import csv
import hashlib
import http.server
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import ssl
import statistics
import subprocess
import tempfile
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
import urllib.parse
import yaml

APP_NAME = "CF Auto Desktop"
PROVIDER = "cf-dynamic-local"
PORT = 17653
AUTO = "CF动态测速"
FALLBACK = "CF动态容灾"
CHOOSER = "代理选择"
SEEDS = ["172.64.153.119", "104.19.155.174"]  # 仅历史种子，使用前需重新验证
INTERVAL_CHOICES = [3, 6, 12, 24]


def app_home() -> Path:
    return Path.home() / "Library" / "Application Support" / "CF Auto Desktop"


def atomic_write(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp = tempfile.mkstemp(dir=path.parent, prefix=".stage-")
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def ydump(value: dict) -> str:
    return yaml.safe_dump(value, allow_unicode=True, sort_keys=False, width=1000)


def ipv4(ip: str) -> bool:
    try:
        return isinstance(ipaddress.ip_address(ip), ipaddress.IPv4Address)
    except ValueError:
        return False


def load_yaml(path: Path) -> dict:
    value = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError("YAML 顶层必须是映射对象")
    return value


def eligible_nodes(config: dict) -> list[dict]:
    """Directly declared vless/ws/TLS nodes only; provider templates are separate."""
    return [
        p for p in config.get("proxies", [])
        if isinstance(p, dict) and p.get("type") == "vless"
        and p.get("network") == "ws" and p.get("tls") is True
        and (p.get("servername") or p.get("sni") or (p.get("ws-opts") or {}).get("headers", {}).get("Host"))
    ]


def get_node(config: dict, node_name: str) -> tuple[dict, str, str]:
    found = [p for p in eligible_nodes(config) if p.get("name") == node_name]
    if len(found) != 1:
        raise ValueError("请选择唯一的 VLESS + WS + TLS 原始节点")
    node = found[0]
    domain = node.get("servername") or node.get("sni") or node.get("ws-opts", {}).get("headers", {}).get("Host")
    path = str((node.get("ws-opts") or {}).get("path") or "")
    if not domain or not re.fullmatch(r"[A-Za-z0-9.-]+", domain):
        raise ValueError("节点缺少有效 SNI 域名")
    if not path.startswith("/") or "\r" in path or "\n" in path:
        raise ValueError("节点缺少有效 WebSocket 路径")
    if not node.get("uuid") or str(node["uuid"]).lower() in ("xxx", "replace_with_your_uuid"):
        raise ValueError("请导入带真实 UUID 的原始 YAML（不会在界面显示或上传 UUID）")
    return node, domain, path


def derive_node(original: dict, ip: str, name: str, domain: str) -> dict:
    node = copy.deepcopy(original)
    node.update({"name": name, "server": ip, "port": 443,
                 "tls": True, "servername": domain, "network": "ws",
                 "skip-cert-verify": False, "alpn": ["http/1.1"]})
    if "sni" in node:
        node["sni"] = domain
    opts = node.setdefault("ws-opts", {})
    headers = {k: v for k, v in opts.get("headers", {}).items() if k.lower() != "host"}
    headers["Host"] = domain
    opts["headers"] = headers
    return node


def generate_provider(state: dict, ips: list[str]) -> dict:
    if not 1 <= len(ips) <= 5 or len(set(ips)) != len(ips) or not all(ipv4(ip) for ip in ips):
        raise ValueError("候选池需包含 1–5 个不重复 IPv4")
    return {"proxies": [derive_node(state["node"], ip, f"CF-DYN-{n+1:02d}", state["domain"])
                        for n, ip in enumerate(ips)]}


def generate_main(config: dict, state: dict) -> dict:
    updated = copy.deepcopy(config)
    providers = updated.setdefault("proxy-providers", {})
    if PROVIDER in providers:
        raise ValueError("该 YAML 已包含本工具 Provider，请导入未修改的原始 YAML")
    providers[PROVIDER] = {
        "type": "http",
        "url": f"http://127.0.0.1:{PORT}/{state['token']}/cf-proxies.yaml",
        "path": "./proxy_providers/cf-desktop-cache.yaml",
        "interval": 60,
        "proxy": "DIRECT",
        "health-check": {"enable": True, "url": "https://www.gstatic.com/generate_204",
                         "interval": 300, "timeout": 6000, "lazy": False,
                         "expected-status": 204},
    }
    groups = updated.get("proxy-groups")
    if not isinstance(groups, list):
        raise ValueError("YAML 缺少 proxy-groups")
    selector = next((g for g in groups if isinstance(g, dict) and g.get("name") == CHOOSER
                     and g.get("type") == "select"), None)
    if selector is None:
        raise ValueError("YAML 中需要名为「代理选择」的 select 策略组")
    if state["node"]["name"] not in selector.get("proxies", []):
        raise ValueError("代理选择组中没有原始节点，请检查配置")
    if any(g.get("name") in (AUTO, FALLBACK) for g in groups):
        raise ValueError("检测到同名动态策略组，请导入未处理的原始 YAML")
    groups.extend([
        {"name": AUTO, "type": "url-test", "use": [PROVIDER],
         "url": "https://www.gstatic.com/generate_204", "interval": 300,
         "tolerance": 100, "timeout": 6000, "lazy": False},
        {"name": FALLBACK, "type": "fallback", "use": [PROVIDER],
         "url": "https://www.gstatic.com/generate_204", "interval": 300,
         "timeout": 6000, "lazy": False},
    ])
    selector["proxies"] = list(dict.fromkeys([FALLBACK, AUTO, *selector["proxies"]]))
    # Preserve current routing and do not silently switch an existing user's choices.
    return updated


def read_prefs(workdir: Path) -> dict:
    p = workdir / "prefs.json"
    if not p.exists():
        return {"cfst": "", "every_hours": 6, "auto_scan": True,
                "last_attempt": None, "last_success": None, "last_status": "尚未扫描"}
    return json.loads(p.read_text(encoding="utf-8"))


def update_prefs(workdir: Path, **kwargs) -> None:
    prefs = read_prefs(workdir)
    prefs.update(kwargs)
    atomic_write(workdir / "prefs.json", json.dumps(prefs, ensure_ascii=False, indent=2))


def setup(workdir: Path, config_file: Path, node_name: str) -> Path:
    raw = load_yaml(config_file)
    node, domain, path = get_node(raw, node_name)
    previous_state = load_state(workdir, required=False)
    # Stable URL during YAML reimport if already configured.
    state = {"node": node, "domain": domain, "path": path,
             "token": previous_state["token"] if previous_state else secrets.token_urlsafe(24),
             "node_name": node_name}
    main = generate_main(raw, state)
    # Security: state/provider/YAML contain auth credentials; private file perms.
    workdir.mkdir(parents=True, exist_ok=True)
    if (workdir / "clash-auto.yaml").exists():
        shutil.copy2(workdir / "clash-auto.yaml", workdir / "clash-auto.previous.yaml")
        os.chmod(workdir / "clash-auto.previous.yaml", 0o600)
    atomic_write(workdir / "state.json", json.dumps(state, ensure_ascii=False, indent=2))
    # Keep verified previous pool only if the node auth/SNI/path are unchanged;
    # otherwise old nodes could carry stale UUID, so recreate from seeds.
    prior = workdir / "cf-proxies.yaml"
    unchanged = previous_state and all(previous_state.get(k) == state.get(k)
                                       for k in ("domain", "path", "node"))
    if not unchanged or not prior.exists():
        atomic_write(prior, ydump(generate_provider(state, SEEDS)))
    atomic_write(workdir / "clash-auto.yaml", ydump(main))
    return workdir / "clash-auto.yaml"


def load_state(workdir: Path, required=True) -> dict | None:
    path = workdir / "state.json"
    if not path.exists():
        if required:
            raise ValueError("请先导入原始 YAML")
        return None
    return json.loads(path.read_text(encoding="utf-8"))


def import_cfst(workdir: Path, chosen: Path) -> Path:
    """Copy binary and its ip.txt together. Never execute at import time."""
    chosen = chosen.expanduser().resolve()
    if not chosen.is_file() or chosen.name not in ("cfst", "CloudflareSpeedTest"):
        raise ValueError("请选择可信来源下载的 cfst 可执行文件")
    ipfile = chosen.parent / "ip.txt"
    if not ipfile.is_file() or not ipfile.read_text(errors="replace").strip():
        raise ValueError("cfst 所在目录必须同时包含有效的 ip.txt 文件")
    target = workdir / "cfst-bundle"
    target.mkdir(parents=True, exist_ok=True)
    dest = target / "cfst"
    if chosen != dest.resolve():
        shutil.copy2(chosen, dest)
    shutil.copy2(ipfile, target / "ip.txt")
    os.chmod(dest, 0o700)
    os.chmod(target / "ip.txt", 0o600)
    update_prefs(workdir, cfst=str(dest))
    return dest


def csv_ips(csv_file: Path, limit: int) -> list[str]:
    if not csv_file.is_file():
        raise ValueError("CloudflareSpeedTest 未输出 result.csv")
    result = []
    with csv_file.open(newline="", encoding="utf-8-sig") as f:
        reader = csv.reader(f)
        next(reader, None)
        for row in reader:
            if row and ipv4(row[0].strip()) and row[0].strip() not in result:
                result.append(row[0].strip())
                if len(result) >= limit:
                    break
    return result


def probe_ws(ip: str, domain: str, path: str, timeout: float = 5) -> float | None:
    """Validate public edge cert + SNI and 101 with correct WebSocket accept hash."""
    start = time.monotonic()
    key = base64.b64encode(os.urandom(16)).decode("ascii")
    expected = base64.b64encode(hashlib.sha1(
        (key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()
    ).digest()).decode("ascii")
    try:
        ctx = ssl.create_default_context()
        ctx.set_alpn_protocols(["http/1.1"])
        with socket.create_connection((ip, 443), timeout=timeout) as connection:
            connection.settimeout(timeout)
            with ctx.wrap_socket(connection, server_hostname=domain) as tls:
                request = (f"GET {path} HTTP/1.1\r\nHost: {domain}\r\n"
                           "Connection: Upgrade\r\nUpgrade: websocket\r\n"
                           "Sec-WebSocket-Version: 13\r\n"
                           f"Sec-WebSocket-Key: {key}\r\nUser-Agent: CF-Auto-Desktop/1\r\n\r\n")
                tls.sendall(request.encode("ascii"))
                result = b""
                while b"\r\n\r\n" not in result and len(result) < 16384:
                    block = tls.recv(4096)
                    if not block:
                        return None
                    result += block
                lines = result.split(b"\r\n\r\n", 1)[0].decode("latin-1").split("\r\n")
                if not lines[0].startswith("HTTP/1.1 101"):
                    return None
                headers = {}
                for line in lines[1:]:
                    if ":" in line:
                        k, v = line.split(":", 1)
                        headers[k.strip().lower()] = v.strip()
                return time.monotonic() - start if headers.get("sec-websocket-accept") == expected else None
    except (OSError, ssl.SSLError, TimeoutError, ValueError):
        return None


def check_candidate(ip: str, domain: str, path: str, repeat: int, stop: threading.Event) -> dict:
    results = []
    for _ in range(repeat):
        if stop.is_set():
            break
        results.append(probe_ws(ip, domain, path))
    good = [v for v in results if v is not None]
    return {"ip": ip, "success": len(good), "attempts": len(results),
            "median": round(statistics.median(good), 3) if good else None}


def do_scan(workdir: Path, *, stop: threading.Event | None = None, log=None,
            dry_run: bool = False, cfst_override: Path | None = None,
            csv_override: Path | None = None, repeat: int = 3,
            max_candidates: int = 30, keep: int = 5) -> dict:
    """Scan, recheck, stage & atomic publish. On errors, leave active provider untouched."""
    stop = stop or threading.Event()
    log = log or (lambda msg: None)
    state = load_state(workdir)
    prefs = read_prefs(workdir)
    cfst = Path(cfst_override) if cfst_override else Path(prefs.get("cfst", ""))
    if not csv_override and (not cfst.is_file() or not (cfst.parent / "ip.txt").is_file()):
        raise ValueError("请先导入包含 ip.txt 的 CloudflareSpeedTest 可执行文件")
    now = time.time()
    if not dry_run:
        update_prefs(workdir, last_attempt=now, last_status="扫描中")
    if csv_override:
        pool = csv_ips(csv_override, max_candidates)
    else:
        output = workdir / "cfst-latest.csv"
        if output.exists():
            output.unlink()
        # -dd disables downloaded-file speed test; this is candidate discovery only.
        args = [str(cfst), "-f", "ip.txt", "-tp", "443", "-tl", "300",
                "-t", "4", "-n", "80", "-dd", "-o", str(output), "-p", "0"]
        env = os.environ.copy()
        for key in ("http_proxy", "https_proxy", "all_proxy", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"):
            env.pop(key, None)
        log("开始 CloudflareSpeedTest TCP 候选扫描（未使用代理环境变量）…")
        # No shell, no user-controlled command flags. Background cancel kills CFST.
        with (workdir / "cfst-run.log").open("w", encoding="utf-8") as logfile:
            p = subprocess.Popen(args, cwd=cfst.parent, env=env, stdout=logfile,
                                 stderr=subprocess.STDOUT)
            start = time.monotonic()
            while p.poll() is None:
                if stop.is_set() or time.monotonic() - start > 1200:
                    p.terminate()
                    try:
                        p.wait(timeout=4)
                    except subprocess.TimeoutExpired:
                        p.kill()
                        p.wait()
                    if stop.is_set():
                        raise RuntimeError("已取消扫描，原节点池不变")
                    raise RuntimeError("CFST 扫描超时，原节点池不变")
                time.sleep(0.4)
            if p.returncode:
                raise RuntimeError(f"CFST 退出代码 {p.returncode}，请查看本地 cfst-run.log")
        pool = csv_ips(output, max_candidates)
    prev = workdir / "cf-proxies.yaml"
    old = []
    if prev.exists():
        old = [n.get("server", "") for n in load_yaml(prev).get("proxies", [])
               if ipv4(str(n.get("server", "")))]
    pool = list(dict.fromkeys(pool + old + SEEDS))
    if stop.is_set():
        raise RuntimeError("已取消扫描，原节点池不变")
    log(f"找到 {len(pool)} 个候选，开始域名 TLS + WebSocket 验证…")
    results = []
    with ThreadPoolExecutor(max_workers=6) as executor:
        futures = [executor.submit(check_candidate, ip, state["domain"], state["path"], repeat, stop)
                   for ip in pool]
        for future in as_completed(futures):
            if stop.is_set():
                executor.shutdown(wait=False, cancel_futures=True)
                raise RuntimeError("已取消扫描，原节点池不变")
            results.append(future.result())
    passing = [item for item in results if item["success"] >= max(2, repeat - 1)
               and item["attempts"] == repeat and item["median"] is not None
               and item["median"] <= 3.5]
    passing.sort(key=lambda item: (-item["success"], item["median"]))
    chosen = [item["ip"] for item in passing[:keep]]
    for item in sorted(results, key=lambda x: x["median"] if x["median"] is not None else float("inf"))[:10]:
        log(f"{item['ip']}  握手 {item['success']}/{item['attempts']}，中位数 {item['median']}s")
    report = {"candidates": len(pool), "qualified": len(passing), "chosen": chosen,
              "measure": "TLS + WebSocket 101; not full proxy bandwidth"}
    if stop.is_set():
        raise RuntimeError("已取消扫描，原节点池不变")
    if len(chosen) < 2:
        log("合格候选不足 2 个；未覆盖原节点池。请考虑检查整体线路。")
        if not dry_run:
            update_prefs(workdir, last_status=f"未更新：仅 {len(chosen)} 个候选合格")
        return report
    if dry_run:
        log(f"预览模式，不写入：{', '.join(chosen)}")
        return report
    # Never publish after cancellation; preserve rollback copy.
    current = prev.read_text(encoding="utf-8") if prev.exists() else None
    if current is not None:
        atomic_write(workdir / "cf-proxies.previous.yaml", current)
    atomic_write(prev, ydump(generate_provider(state, chosen)))
    update_prefs(workdir, last_success=time.time(), last_status=f"已更新 {len(chosen)} 个候选")
    log("更新成功。Clash Party 的 HTTP Provider 将在下次拉取时读取新节点。")
    return report


def rollback(workdir: Path) -> bool:
    last = workdir / "cf-proxies.previous.yaml"
    now = workdir / "cf-proxies.yaml"
    if not last.is_file():
        return False
    state = load_state(workdir)
    data = load_yaml(last)
    nodes = data.get("proxies") or []
    if not 1 <= len(nodes) <= 5 or any(node.get("servername") != state["domain"]
                                      or node.get("uuid") != state["node"]["uuid"]
                                      for node in nodes):
        raise ValueError("历史候选与当前节点认证或域名不一致，禁止回滚")
    current = now.read_text(encoding="utf-8")
    old = last.read_text(encoding="utf-8")
    atomic_write(now, old)
    atomic_write(last, current)
    update_prefs(workdir, last_status="已手动回滚到上个节点池")
    return True


class ProviderServer:
    """Bound to loopback only, serves a random-token URL. No external requests."""
    def __init__(self, workdir: Path):
        self.workdir = workdir
        self.httpd = None
        self.thread = None

    def start(self):
        state = load_state(self.workdir)
        filename = self.workdir / "cf-proxies.yaml"
        token = state["token"]
        wanted = f"/{token}/cf-proxies.yaml"

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                if urllib.parse.urlsplit(self.path).path != wanted:
                    self.send_error(404)
                    return
                try:
                    data = filename.read_bytes()
                except OSError:
                    self.send_error(503)
                    return
                self.send_response(200)
                self.send_header("Content-Type", "application/yaml; charset=utf-8")
                self.send_header("Cache-Control", "private, no-store")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, fmt, *args):
                pass

        if self.httpd:
            return
        self.httpd = http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    def stop(self):
        if self.httpd:
            self.httpd.shutdown()
            self.httpd.server_close()
            self.httpd = None
            if self.thread:
                self.thread.join(timeout=3)
                self.thread = None
