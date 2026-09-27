// Package engine is the Go port of src/engine.py.
//
// 与 Python 版保持行为对齐：
//   - YAML 处理（VLESS+WS+TLS 识别、配置生成）
//   - TLS + RFC6455 WebSocket 101 探活
//   - CloudflareSpeedTest 官方下载（HTTPS 白名单 + SHA256 校验 + 白名单解压）
//   - 127.0.0.1 HTTP Provider（候选池 + 外壳 URL 订阅）
//   - prefs / state 原子写、0600 权限
//
// 硬边界（与 Python 版一致）：
//   - 只允许 https + CFST_ALLOWED_HOSTS 拉取 cfst
//   - 无 sha256 digest → fail closed
//   - 只解压 cfst / ip.txt，拒绝 zip-slip
//   - 从不执行 cfst，只落盘 + 更新 prefs
package engine

import (
	"os"
	"path/filepath"
	"regexp"
)

const (
	AppName   = "CF Auto Desktop"
	Provider  = "cf-dynamic-local"
	Port      = 17653
	Auto      = "CF动态测速"
	Fallback  = "CF动态容灾"
	Chooser   = "代理选择"
	TokenSize = 24 // 随机 token 字节数（URL-safe base64 后约 32 字符）
)

// Seeds 仅历史种子，使用前需重新验证。
var Seeds = []string{"172.64.153.119", "104.19.155.174"}

// IntervalChoices 扫描间隔可选小时数。
var IntervalChoices = []int{3, 6, 12, 24}

// ScanPreset 描述一次扫描的资源配额（对应 Python SCAN_PRESETS）。
type ScanPreset struct {
	Repeat        int
	MaxCandidates int
	Keep          int
	Workers       int
	CfstN         int
	CfstT         int
	CfstTL        int
}

var ScanPresets = map[string]ScanPreset{
	"standard": {Repeat: 3, MaxCandidates: 30, Keep: 5, Workers: 6, CfstN: 80, CfstT: 4, CfstTL: 300},
	"light":    {Repeat: 2, MaxCandidates: 12, Keep: 3, Workers: 3, CfstN: 30, CfstT: 3, CfstTL: 200},
}

// CloudflareSpeedTest 官方下载硬约束。
const (
	CfstRepoOwner   = "XIU2"
	CfstRepoName    = "CloudflareSpeedTest"
	CfstReleaseAPI  = "https://api.github.com/repos/" + CfstRepoOwner + "/" + CfstRepoName + "/releases/latest"
	CfstExpectedUA  = "CF-Auto-Desktop/1 (+local; personal use)"
)

// CfstAllowedHosts 域名白名单：严格 https + 精确匹配（拒绝 github.com.evil.com 等）。
var CfstAllowedHosts = map[string]bool{
	"github.com":                   true,
	"api.github.com":               true,
	"objects.githubusercontent.com": true,
}

// AppHome 返回 `~/Library/Application Support/CF Auto Desktop`。
// 与 Python 版路径一致，保证无缝迁移。
func AppHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", AppName), nil
}

var domainRe = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
