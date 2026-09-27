import copy
import json
from pathlib import Path
import socket
import tempfile
import threading
from unittest.mock import patch
import urllib.request
import urllib.error
import yaml
import sys
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))

import engine


NODE = {
    "name": "VLESS-WS-TLS", "type": "vless", "server": "v2.example.com",
    "port": 443, "uuid": "123e4567-e89b-42d3-a456-426614174000", "udp": True,
    "network": "ws", "tls": True, "servername": "v2.example.com",
    "ws-opts": {"path": "/example-path", "headers": {"Host": "v2.example.com"}},
}
CONFIG = {
    "proxies": [NODE],
    "proxy-groups": [
        {"name": "代理选择", "type": "select", "proxies": ["VLESS-WS-TLS", "DIRECT"]},
        {"name": "自动选择", "type": "url-test", "proxies": ["VLESS-WS-TLS"],
         "url": "https://example.org/", "interval": 600},
    ],
    "rules": ["DOMAIN-SUFFIX,cn,DIRECT", "MATCH,代理选择"],
}


def sample_setup(tmp, config=None):
    wd = tmp / "state"
    orig = tmp / "original.yaml"
    orig.write_text(engine.ydump(config or CONFIG), encoding="utf-8")
    return wd, engine.setup(wd, orig, "VLESS-WS-TLS")


def test_setup_provider_and_rules():
    with tempfile.TemporaryDirectory() as t:
        wd, generated = sample_setup(Path(t))
        config = engine.load_yaml(generated)
        assert config["rules"] == CONFIG["rules"]
        assert CONFIG["proxy-groups"][0]["proxies"] == ["VLESS-WS-TLS", "DIRECT"]
        assert config["proxy-groups"][0]["proxies"][0] == engine.FALLBACK
        assert config["proxy-providers"][engine.PROVIDER]["url"].startswith("http://127.0.0.1:17653/")
        assert "token" not in (wd / "cf-proxies.yaml").read_text()
        assert len(engine.load_yaml(wd / "cf-proxies.yaml")["proxies"]) == 2
        assert generated.stat().st_mode & 0o077 == 0
        assert (wd / "state.json").stat().st_mode & 0o077 == 0


def test_import_current_prior_yaml():
    # The user currently has old static CF-HKG-A / CF-HKG-B groups and nodes.
    with tempfile.TemporaryDirectory() as t:
        config = copy.deepcopy(CONFIG)
        for n in ("CF-HKG-A", "CF-HKG-B"):
            dup = copy.deepcopy(NODE)
            dup.update(name=n, server="172.64.153.119")
            config["proxies"].insert(0, dup)
        config["proxy-groups"].append({"name": "CF自动容灾", "type": "fallback",
                                        "proxies": ["CF-HKG-A", "CF-HKG-B"]})
        wd, path = sample_setup(Path(t), config)
        final = engine.load_yaml(path)
        assert engine.FALLBACK in final["proxy-groups"][0]["proxies"]
        assert "CF自动容灾" in [g["name"] for g in final["proxy-groups"]]
        assert engine.FALLBACK in [g["name"] for g in final["proxy-groups"]]


def test_no_provider_overwrite_if_too_few_good():
    with tempfile.TemporaryDirectory() as t:
        wd, _ = sample_setup(Path(t))
        old = (wd / "cf-proxies.yaml").read_text()
        csvfile = Path(t) / "result.csv"
        csvfile.write_text("IP 地址,平均延迟\n104.16.155.196,70\n104.17.120.141,82\n", encoding="utf-8")
        def fake_check(ip, domain, path, repeat, stop):
            return {"ip": ip, "success": 3 if ip == "104.16.155.196" else 0,
                    "attempts": 3, "median": 0.3 if ip == "104.16.155.196" else None}
        with patch.object(engine, "check_candidate", side_effect=fake_check):
            report = engine.do_scan(wd, csv_override=csvfile)
        assert len(report["chosen"]) == 1
        assert (wd / "cf-proxies.yaml").read_text() == old


def test_publish_and_rollback_and_preserve_uuid():
    with tempfile.TemporaryDirectory() as t:
        wd, _ = sample_setup(Path(t))
        old = (wd / "cf-proxies.yaml").read_text()
        csvfile = Path(t) / "result.csv"
        csvfile.write_text("IP 地址,平均延迟\n104.16.155.196,70\n104.17.120.141,82\n", encoding="utf-8")
        def fake_check(ip, domain, path, repeat, stop):
            good = ip in ("104.16.155.196", "104.17.120.141")
            return {"ip": ip, "success": 3 if good else 0,
                    "attempts": 3, "median": 0.3 if good else None}
        with patch.object(engine, "check_candidate", side_effect=fake_check):
            report = engine.do_scan(wd, csv_override=csvfile)
        assert len(report["chosen"]) == 2
        new = engine.load_yaml(wd / "cf-proxies.yaml")
        assert {p["server"] for p in new["proxies"]} == {"104.16.155.196", "104.17.120.141"}
        assert {p["uuid"] for p in new["proxies"]} == {NODE["uuid"]}
        assert engine.rollback(wd)
        assert (wd / "cf-proxies.yaml").read_text() == old
        assert engine.rollback(wd)
        assert engine.load_yaml(wd / "cf-proxies.yaml") == new


def test_preview_no_mutation():
    with tempfile.TemporaryDirectory() as t:
        wd, _ = sample_setup(Path(t))
        old = (wd / "cf-proxies.yaml").read_text()
        csvfile = Path(t) / "result.csv"
        csvfile.write_text("IP 地址\n104.16.155.196\n104.17.120.141\n", encoding="utf-8")
        def fake_check(ip, domain, path, repeat, stop):
            return {"ip": ip, "success": repeat, "attempts": repeat, "median": 0.25}
        with patch.object(engine, "check_candidate", side_effect=fake_check):
            engine.do_scan(wd, csv_override=csvfile, dry_run=True)
        assert (wd / "cf-proxies.yaml").read_text() == old
        assert not (wd / "cf-proxies.previous.yaml").exists()


def test_import_cfst_and_permissions():
    with tempfile.TemporaryDirectory() as t:
        root = Path(t)
        folder = root / "from"
        folder.mkdir()
        (folder / "cfst").write_text("dummy binary", encoding="utf-8")
        try:
            engine.import_cfst(root / "app", folder / "cfst")
            assert False, "should require ip.txt"
        except ValueError:
            pass
        (folder / "ip.txt").write_text("1.1.1.0/24\n", encoding="utf-8")
        target = engine.import_cfst(root / "app", folder / "cfst")
        assert target.exists() and (target.parent / "ip.txt").exists()
        assert target.stat().st_mode & 0o077 == 0


def test_loopback_provider_token():
    with tempfile.TemporaryDirectory() as t:
        wd, _ = sample_setup(Path(t))
        state = engine.load_state(wd)
        # Default port may be bound by earlier test environment; skip if busy.
        with socket.socket() as s:
            if s.connect_ex(("127.0.0.1", engine.PORT)) == 0:
                return
        srv = engine.ProviderServer(wd)
        srv.start()
        try:
            url = f"http://127.0.0.1:{engine.PORT}/{state['token']}/cf-proxies.yaml"
            with urllib.request.urlopen(url, timeout=2) as response:
                data = yaml.safe_load(response.read())
                assert len(data["proxies"]) == 2
            try:
                urllib.request.urlopen(f"http://127.0.0.1:{engine.PORT}/cf-proxies.yaml", timeout=2)
                assert False, "should protect URL with random token"
            except urllib.error.HTTPError as ex:
                assert ex.code == 404
        finally:
            srv.stop()
