package engine

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadYAML 读取 YAML 顶层必须是 map。
func LoadYAML(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("YAML 顶层必须是映射对象")
	}
	return m, nil
}

// YDump 序列化 YAML，保留字段顺序，UTF-8 原样输出。
func YDump(v any) (string, error) {
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	_ = enc.Close()
	return buf.String(), nil
}

// IPv4 校验 IPv4 字面值。
func IPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil && strings.Count(s, ":") == 0
}

// EligibleNodes 只取直接写在 proxies 里的 VLESS + WS + TLS 节点。
func EligibleNodes(config map[string]any) []map[string]any {
	raw, _ := config["proxies"].([]any)
	var out []map[string]any
	for _, it := range raw {
		p, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(p["type"]) != "vless" {
			continue
		}
		if fmt.Sprint(p["network"]) != "ws" {
			continue
		}
		// tls 必须为 true（bool 或字符串 "true"）
		switch t := p["tls"].(type) {
		case bool:
			if !t {
				continue
			}
		case string:
			if !strings.EqualFold(t, "true") {
				continue
			}
		default:
			continue
		}
		// 需要有 SNI / servername / Host 之一
		sni := extractSNI(p)
		if sni == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func extractSNI(p map[string]any) string {
	if s, _ := p["servername"].(string); s != "" {
		return s
	}
	if s, _ := p["sni"].(string); s != "" {
		return s
	}
	if opts, ok := p["ws-opts"].(map[string]any); ok {
		if hdrs, ok := opts["headers"].(map[string]any); ok {
			if s, _ := hdrs["Host"].(string); s != "" {
				return s
			}
			if s, _ := hdrs["host"].(string); s != "" {
				return s
			}
		}
	}
	return ""
}

func extractWSPath(p map[string]any) string {
	opts, ok := p["ws-opts"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := opts["path"].(string)
	return s
}

// GetNode 按名字取唯一 VLESS+WS+TLS 节点，返回 (node, domain, wsPath)。
func GetNode(config map[string]any, nodeName string) (map[string]any, string, string, error) {
	var found []map[string]any
	for _, p := range EligibleNodes(config) {
		if fmt.Sprint(p["name"]) == nodeName {
			found = append(found, p)
		}
	}
	if len(found) != 1 {
		return nil, "", "", fmt.Errorf("请选择唯一的 VLESS + WS + TLS 原始节点")
	}
	node := found[0]
	domain := extractSNI(node)
	path := extractWSPath(node)
	if domain == "" || !domainRe.MatchString(domain) {
		return nil, "", "", fmt.Errorf("节点缺少有效 SNI 域名")
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n") {
		return nil, "", "", fmt.Errorf("节点缺少有效 WebSocket 路径")
	}
	uuid := fmt.Sprint(node["uuid"])
	if uuid == "" || strings.EqualFold(uuid, "xxx") || strings.EqualFold(uuid, "replace_with_your_uuid") {
		return nil, "", "", fmt.Errorf("请导入带真实 UUID 的原始 YAML（不会在界面显示或上传 UUID）")
	}
	return node, domain, path, nil
}

// DeriveNode 由原始节点派生 IP 替换后的副本。
func DeriveNode(original map[string]any, ip, name, domain string) map[string]any {
	// 深拷贝（YAML 反序列化后再编码走一次保证结构独立）
	copied, ok := deepCopy(original).(map[string]any)
	if !ok {
		copied = map[string]any{}
	}
	out := copied
	out["name"] = name
	out["server"] = ip
	out["port"] = 443
	out["tls"] = true
	out["servername"] = domain
	out["network"] = "ws"
	out["skip-cert-verify"] = false
	out["alpn"] = []any{"http/1.1"}
	if _, ok := out["sni"]; ok {
		out["sni"] = domain
	}
	opts, _ := out["ws-opts"].(map[string]any)
	if opts == nil {
		opts = map[string]any{}
		out["ws-opts"] = opts
	}
	hdrs, _ := opts["headers"].(map[string]any)
	if hdrs == nil {
		hdrs = map[string]any{}
	}
	// 移除旧 Host（大小写不敏感），再塞入规范 Host
	for k := range hdrs {
		if strings.EqualFold(k, "Host") || strings.EqualFold(k, "host") {
			delete(hdrs, k)
		}
	}
	hdrs["Host"] = domain
	opts["headers"] = hdrs
	return out
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[k] = deepCopy(val)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, val := range t {
			s[i] = deepCopy(val)
		}
		return s
	default:
		return v
	}
}

// GenerateProvider 生成 cf-proxies.yaml（1–5 个不重复 IPv4）。
func GenerateProvider(state *State, ips []string) (map[string]any, error) {
	if len(ips) < 1 || len(ips) > 5 {
		return nil, fmt.Errorf("候选池需包含 1–5 个不重复 IPv4")
	}
	seen := map[string]bool{}
	for _, ip := range ips {
		if !IPv4(ip) {
			return nil, fmt.Errorf("候选池需包含 1–5 个不重复 IPv4")
		}
		if seen[ip] {
			return nil, fmt.Errorf("候选池需包含 1–5 个不重复 IPv4")
		}
		seen[ip] = true
	}
	proxies := make([]any, 0, len(ips))
	for i, ip := range ips {
		name := fmt.Sprintf("CF-DYN-%02d", i+1)
		proxies = append(proxies, DeriveNode(state.Node, ip, name, state.Domain))
	}
	return map[string]any{"proxies": proxies}, nil
}

// GenerateMain 生成主配置（含 Provider + 策略组）。
func GenerateMain(config map[string]any, state *State) (map[string]any, error) {
	out := deepCopy(config).(map[string]any)

	providers, _ := out["proxy-providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
		out["proxy-providers"] = providers
	}
	if _, exists := providers[Provider]; exists {
		return nil, fmt.Errorf("该 YAML 已包含本工具 Provider，请导入未修改的原始 YAML")
	}
	providers[Provider] = map[string]any{
		"type":     "http",
		"url":      fmt.Sprintf("http://127.0.0.1:%d/%s/cf-proxies.yaml", Port, state.Token),
		"path":     "./proxy_providers/cf-desktop-cache.yaml",
		"interval": 60,
		"proxy":    "DIRECT",
		"health-check": map[string]any{
			"enable":          true,
			"url":             "https://www.gstatic.com/generate_204",
			"interval":        300,
			"timeout":         6000,
			"lazy":            false,
			"expected-status": 204,
		},
	}

	groupsRaw, _ := out["proxy-groups"].([]any)
	if groupsRaw == nil {
		return nil, fmt.Errorf("YAML 缺少 proxy-groups")
	}
	// 定位「代理选择」select 组
	selectorIdx := -1
	for i, g := range groupsRaw {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprint(gm["name"]) == Chooser && fmt.Sprint(gm["type"]) == "select" {
			selectorIdx = i
			break
		}
	}
	if selectorIdx < 0 {
		return nil, fmt.Errorf("YAML 中需要名为「代理选择」的 select 策略组")
	}
	selector := groupsRaw[selectorIdx].(map[string]any)
	// 检查 selector.proxies 中包含原始节点
	selProxies, _ := selector["proxies"].([]any)
	hasNode := false
	for _, s := range selProxies {
		if fmt.Sprint(s) == state.NodeName {
			hasNode = true
			break
		}
	}
	if !hasNode {
		return nil, fmt.Errorf("代理选择组中没有原始节点，请检查配置")
	}
	// 检查同名策略组不存在
	for _, g := range groupsRaw {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		name := fmt.Sprint(gm["name"])
		if name == Auto || name == Fallback {
			return nil, fmt.Errorf("检测到同名动态策略组，请导入未处理的原始 YAML")
		}
	}

	// 追加动态策略组
	groupsRaw = append(groupsRaw,
		map[string]any{
			"name": Auto, "type": "url-test", "use": []any{Provider},
			"url": "https://www.gstatic.com/generate_204", "interval": 300,
			"tolerance": 100, "timeout": 6000, "lazy": false,
		},
		map[string]any{
			"name": Fallback, "type": "fallback", "use": []any{Provider},
			"url": "https://www.gstatic.com/generate_204", "interval": 300,
			"timeout": 6000, "lazy": false,
		},
	)
	out["proxy-groups"] = groupsRaw

	// selector.proxies 前插 FALLBACK / AUTO（保持用户已有选择在后）
	newProxies := []any{Fallback, Auto}
	seen := map[string]bool{Fallback: true, Auto: true}
	for _, s := range selProxies {
		if !seen[fmt.Sprint(s)] {
			newProxies = append(newProxies, s)
			seen[fmt.Sprint(s)] = true
		}
	}
	selector["proxies"] = newProxies
	return out, nil
}

// Setup 生成 clash-auto.yaml（对应 Python setup）。
// 稳定 token：重复生成时复用旧 token（YAML 重导入不换 URL）。
func Setup(workdir, configFile, nodeName string) (string, error) {
	raw, err := LoadYAML(configFile)
	if err != nil {
		return "", err
	}
	node, domain, path, err := GetNode(raw, nodeName)
	if err != nil {
		return "", err
	}

	prev, _ := LoadState(workdir, false)
	token := ""
	if prev != nil {
		token = prev.Token
	}
	if token == "" {
		token, err = NewToken()
		if err != nil {
			return "", err
		}
	}

	state := &State{
		Node:     node,
		Domain:   domain,
		Path:     path,
		Token:    token,
		NodeName: nodeName,
	}

	main, err := GenerateMain(raw, state)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return "", err
	}
	// 备份旧 clash-auto.yaml
	mainPath := filepath.Join(workdir, "clash-auto.yaml")
	if _, err := os.Stat(mainPath); err == nil {
		if data, err := os.ReadFile(mainPath); err == nil {
			prevPath := filepath.Join(workdir, "clash-auto.previous.yaml")
			if err := AtomicWrite(prevPath, string(data)); err == nil {
				_ = os.Chmod(prevPath, 0o600)
			}
		}
	}

	if err := SaveState(workdir, state); err != nil {
		return "", err
	}

	// 保留旧候选池仅当 domain/path/node 未变；否则重建种子
	priorPath := filepath.Join(workdir, "cf-proxies.yaml")
	unchanged := false
	if prev != nil && prev.Domain == state.Domain && prev.Path == state.Path {
		if eq, _ := yaml.Marshal(prev.Node); eq != nil {
			if eq2, _ := yaml.Marshal(state.Node); eq2 != nil {
				unchanged = string(eq) == string(eq2)
			}
		}
	}
	if _, err := os.Stat(priorPath); err != nil || !unchanged {
		prov, err := GenerateProvider(state, Seeds)
		if err != nil {
			return "", err
		}
		text, err := YDump(prov)
		if err != nil {
			return "", err
		}
		if err := AtomicWrite(priorPath, text); err != nil {
			return "", err
		}
	}

	text, err := YDump(main)
	if err != nil {
		return "", err
	}
	if err := AtomicWrite(mainPath, text); err != nil {
		return "", err
	}
	return mainPath, nil
}

// Rollback 恢复上一次候选池；返回是否成功。
func Rollback(workdir string) (bool, error) {
	last := filepath.Join(workdir, "cf-proxies.previous.yaml")
	now := filepath.Join(workdir, "cf-proxies.yaml")
	if _, err := os.Stat(last); err != nil {
		return false, nil
	}
	state, err := LoadState(workdir, true)
	if err != nil {
		return false, err
	}
	data, err := LoadYAML(last)
	if err != nil {
		return false, err
	}
	proxies, _ := data["proxies"].([]any)
	if len(proxies) < 1 || len(proxies) > 5 {
		return false, fmt.Errorf("历史候选与当前节点认证或域名不一致，禁止回滚")
	}
	for _, it := range proxies {
		pm, ok := it.(map[string]any)
		if !ok {
			return false, fmt.Errorf("历史候选与当前节点认证或域名不一致，禁止回滚")
		}
		if fmt.Sprint(pm["servername"]) != state.Domain {
			return false, fmt.Errorf("历史候选与当前节点认证或域名不一致，禁止回滚")
		}
		if fmt.Sprint(pm["uuid"]) != fmt.Sprint(state.Node["uuid"]) {
			return false, fmt.Errorf("历史候选与当前节点认证或域名不一致，禁止回滚")
		}
	}
	// 交换 now ↔ last
	nowData, err := os.ReadFile(now)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	lastData, err := os.ReadFile(last)
	if err != nil {
		return false, err
	}
	if err := AtomicWrite(now, string(lastData)); err != nil {
		return false, err
	}
	if nowData != nil {
		if err := AtomicWrite(last, string(nowData)); err != nil {
			return false, err
		}
	}
	_ = UpdatePrefs(workdir, func(p *Prefs) { p.LastStatus = "已手动回滚到上个节点池" })
	return true, nil
}
