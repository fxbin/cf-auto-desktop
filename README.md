# CF Auto Desktop

[![CI](https://github.com/fxbin/cf-auto-desktop/actions/workflows/ci.yml/badge.svg)](https://github.com/fxbin/cf-auto-desktop/actions/workflows/ci.yml)

macOS 菜单栏小工具：对你自己的 Clash / Mihomo 本地配置做 Cloudflare 接入 IP 动态优选。只替换候选 IP，不动你的节点本身。

> **技术栈**：Go 1.22+ · Wails v2（WKWebView）· 前端原生 HTML/CSS/JS

## 功能

- 导入本地 Clash YAML，识别 `VLESS + WS + TLS` 节点
- **一键下载** 官方 [CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest)（HTTPS + SHA256 校验，只解压 `cfst` 与 `ip.txt`）
- 筛完候选后用你的节点域名做 TLS + WebSocket 校验
- 生成可直接导入 Clash Party 的配置，新增「CF动态容灾 / CF动态测速」策略组
- **订阅 URL**：外层配置可通过 `http://127.0.0.1:17653/<token>/clash-auto.yaml` 订阅，动态候选池走 HTTP 拉取
- 菜单栏常驻，可选开机自启、定时扫描（默认 6 小时）
- 扫描失败或候选不足时**不覆盖**旧节点池，支持一键回滚
- 本地 Provider 仅监听 `127.0.0.1`，凭据写入用户私有目录（`0600`）

## 快速开始

**要求**：macOS（Intel / Apple Silicon）、Go 1.22+、[Wails CLI](https://wails.io/docs/gettingstarted/installation)。

```bash
# 安装 Wails CLI（若未装）
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 开发模式（热重载）
wails dev

# 打包（产出 build/bin/CF Auto Desktop.app）
./build.sh
```

也可以手动：

```bash
wails build -platform darwin/arm64 -clean
open "build/bin/CF Auto Desktop.app"
```

## 使用

1. 点 **导入 YAML** — 选择你现在导入 Clash Party 的那份配置（含真实 UUID）
2. 点 **一键下载官方 cfst** — 自动拉取并 SHA256 校验
3. 选好原节点，点 **生成 Clash 配置** — 日志里会给订阅 URL
4. 在 Clash Party 里**订阅 / 导入 URL** 粘贴订阅链接，或手动导入生成的 YAML
5. 在 Clash Party 里切到「CF动态容灾」，App 留在菜单栏常驻

**候选池自动更新**：Clash 每 60s 通过 HTTP 拉取 `cf-proxies.yaml`，无需重导入。

## 项目结构

```
cf-auto-desktop/
├── main.go               Wails 入口 + 单实例锁
├── app.go                Wails bindings（暴露给前端 window.go.main.App.*）
├── internal/engine/      核心业务逻辑（Go）
│   ├── const.go          常量 / 扫描预设 / cfst 白名单
│   ├── yaml.go           YAML 处理 / 配置生成
│   ├── state.go          prefs/state 原子写
│   ├── probe.go          TLS + WebSocket 101 探活
│   ├── cfst.go           cfst 官方下载（SHA256 + zip-slip 拒绝）
│   ├── provider.go       127.0.0.1 HTTP Provider
│   ├── scan.go           扫描编排
│   ├── macos.go          LaunchAgents / flock / Finder
│   └── *_test.go         单元测试（15+）
├── frontend/
│   └── dist/             前端（index.html + style.css + app.js）
├── build/                Wails 构建元数据
└── build.sh              打包脚本
```

## 说明

- 需要你自己准备：原始 Clash YAML（含真实 UUID）。CloudflareSpeedTest 已内置官方下载
- **关窗不退出**：点窗口红 × 只是隐藏到 Dock，App 继续在后台跑 Provider 和定时扫描
- **唤回窗口**：点 Dock 上的 CF Auto Desktop 图标
- **真正退出**：`Cmd+Q` 或 Dock 图标右键 → 退出
- 本工具不上传任何配置，也不修改 Clash Party 现有配置
- 未做 Apple 签名 / 公证，仅建议自己使用

## 参考

- [Wails v2](https://wails.io/)
- [CloudflareSpeedTest](https://github.com/XIU2/CloudflareSpeedTest)
- [Mihomo HTTP Proxy Provider](https://wiki.metacubex.one/en/config/proxy-providers/)
