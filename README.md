# CF Auto Desktop

macOS 菜单栏小工具：对你自己的 Clash / Mihomo 本地配置做 Cloudflare 接入 IP 动态优选。只替换候选 IP，不动你的节点本身。

## 功能

- 导入本地 Clash YAML，识别 `VLESS + WS + TLS` 节点
- **一键下载** 官方 [CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest)（HTTPS + SHA256 校验，只解压 `cfst` 与 `ip.txt`）
- 筛完候选后用你的节点域名做 TLS + WebSocket 校验
- 生成可直接导入 Clash Party 的新配置，新增「CF动态容灾 / CF动态测速」策略组
- 菜单栏常驻，可选开机自启、定时扫描（默认 6 小时）
- 扫描失败或候选不足时**不覆盖**旧节点池，支持一键回滚
- 本地 Provider 仅监听 `127.0.0.1`，凭据写入用户私有目录（`0600`）

## 快速开始

```bash
# 运行（macOS，Python 3.11+）
./run.command

# 或手动
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt
python src/app.py
```

打包：

```bash
./build_macos.sh     # 产出 dist/CF Auto Desktop.app（约 78 MB）
```

## 说明

- 需要你自己准备：原始 Clash YAML（含真实 UUID）。CloudflareSpeedTest 已内置官方下载，也可手动导入
- 本工具不上传任何配置，也不修改 Clash Party 现有配置
- 未做 Apple 签名 / 公证，仅建议自己使用

## 参考

- [CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest)
- [Mihomo HTTP Proxy Provider](https://wiki.metacubex.one/en/config/proxy-providers/)
