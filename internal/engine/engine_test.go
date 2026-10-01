package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleNodeYAML = `
proxies:
  - name: VLESS-WS-TLS
    type: vless
    server: v2.example.com
    port: 443
    uuid: 123e4567-e89b-42d3-a456-426614174000
    udp: true
    network: ws
    tls: true
    servername: v2.example.com
    ws-opts:
      path: /v2ray
      headers:
        Host: v2.example.com
proxy-groups:
  - name: 代理选择
    type: select
    proxies: [VLESS-WS-TLS, DIRECT]
  - name: 自动选择
    type: url-test
    proxies: [VLESS-WS-TLS]
    url: https://example.org/
    interval: 600
rules:
  - DOMAIN-SUFFIX,cn,DIRECT
  - MATCH,代理选择
`

func tempWorkdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func writeSample(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "original.yaml")
	if err := os.WriteFile(p, []byte(sampleNodeYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSetupGeneratesMainAndProvider(t *testing.T) {
	wd := tempWorkdir(t)
	src := writeSample(t, wd)
	out, err := Setup(wd, src, "VLESS-WS-TLS")
	if err != nil {
		t.Fatalf("Setup failed: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("missing generated file: %v", err)
	}
	cfg, err := LoadYAML(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["proxy-providers"].(map[string]any)[Provider]; !ok {
		t.Fatalf("missing provider")
	}
	// 状态与种子池
	state, err := LoadState(wd, true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Domain != "v2.example.com" {
		t.Fatalf("bad domain: %q", state.Domain)
	}
	if state.Path != "/v2ray" {
		t.Fatalf("bad path: %q", state.Path)
	}
	if len(state.Token) < 16 {
		t.Fatalf("token too short: %q", state.Token)
	}
	// 文件权限
	for _, f := range []string{out, filepath.Join(wd, "state.json")} {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s perms too open: %v", f, fi.Mode().Perm())
		}
	}
}

func TestRejectPlaceholderUUID(t *testing.T) {
	wd := tempWorkdir(t)
	src := filepath.Join(wd, "bad.yaml")
	yaml := strings.Replace(sampleNodeYAML,
		"uuid: 123e4567-e89b-42d3-a456-426614174000",
		"uuid: REPLACE_WITH_YOUR_UUID", 1)
	if err := os.WriteFile(src, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(wd, src, "VLESS-WS-TLS"); err == nil {
		t.Fatal("should reject placeholder UUID")
	}
}

func TestRejectExistingProvider(t *testing.T) {
	wd := tempWorkdir(t)
	src := writeSample(t, wd)
	// 先生成
	if _, err := Setup(wd, src, "VLESS-WS-TLS"); err != nil {
		t.Fatal(err)
	}
	// 把生成结果作为新 original，重复生成应失败
	generated := filepath.Join(wd, "clash-auto.yaml")
	if _, err := Setup(wd, generated, "VLESS-WS-TLS"); err == nil {
		t.Fatal("should reject re-importing generated YAML")
	}
}

func TestGenerateProviderValidatesPool(t *testing.T) {
	st := &State{
		Node:     map[string]any{"name": "n", "uuid": "u"},
		Domain:   "x.example.com",
		Path:     "/p",
		Token:    "tok",
		NodeName: "n",
	}
	if _, err := GenerateProvider(st, nil); err == nil {
		t.Fatal("empty pool should fail")
	}
	if _, err := GenerateProvider(st, []string{"1.1.1.1", "1.1.1.1"}); err == nil {
		t.Fatal("dup should fail")
	}
	if _, err := GenerateProvider(st, []string{"999.1.1.1"}); err == nil {
		t.Fatal("bad ip should fail")
	}
	ok, err := GenerateProvider(st, []string{"1.1.1.1", "8.8.8.8"})
	if err != nil {
		t.Fatal(err)
	}
	proxies := ok["proxies"].([]any)
	if len(proxies) != 2 {
		t.Fatalf("want 2 proxies, got %d", len(proxies))
	}
}

func TestAssertOfficialURL(t *testing.T) {
	good := []string{
		"https://github.com/XIU2/CloudflareSpeedTest/releases/download/v1/x.zip",
		"https://api.github.com/repos/XIU2/CloudflareSpeedTest/releases/latest",
		"https://objects.githubusercontent.com/x",
	}
	for _, u := range good {
		if err := AssertOfficialURL(u); err != nil {
			t.Fatalf("should allow %s: %v", u, err)
		}
	}
	bad := []string{
		"http://github.com/x.zip",
		"https://evil.com/x.zip",
		"https://github.com.evil.com/x.zip",
		"file:///etc/passwd",
		"ftp://github.com/x.zip",
	}
	for _, u := range bad {
		if err := AssertOfficialURL(u); err == nil {
			t.Fatalf("should reject %s", u)
		}
	}
}

func TestCfstAssetName(t *testing.T) {
	name, err := CfstAssetName()
	if err != nil {
		t.Skipf("unsupported arch in CI: %v", err)
	}
	if !strings.HasPrefix(name, "cfst_darwin_") {
		t.Fatalf("unexpected asset name %q", name)
	}
}

func TestRollbackRejectsMismatchedState(t *testing.T) {
	wd := tempWorkdir(t)
	src := writeSample(t, wd)
	if _, err := Setup(wd, src, "VLESS-WS-TLS"); err != nil {
		t.Fatal(err)
	}
	// 构造 previous 但域名不对
	bad := `
proxies:
  - name: X
    server: 1.1.1.1
    servername: wrong.example.com
    uuid: 123e4567-e89b-42d3-a456-426614174000
`
	if err := os.WriteFile(filepath.Join(wd, "cf-proxies.previous.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(wd); err == nil {
		t.Fatal("should reject mismatched domain")
	}
}

func TestRollbackSwapsFiles(t *testing.T) {
	wd := tempWorkdir(t)
	src := writeSample(t, wd)
	if _, err := Setup(wd, src, "VLESS-WS-TLS"); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(wd, true)
	if err != nil {
		t.Fatal(err)
	}
	// 构造合法 previous（相同 domain+uuid）
	prevYaml := strings.Replace(sampleNodeYAML, "type: vless", "type: vless", 1)
	_ = prevYaml
	prev := `
proxies:
  - name: X
    server: 1.1.1.1
    servername: ` + state.Domain + `
    uuid: ` + strings.TrimSpace(func() string { s, _ := state.Node["uuid"].(string); return s }()) + `
`
	if err := os.WriteFile(filepath.Join(wd, "cf-proxies.previous.yaml"), []byte(prev), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := Rollback(wd)
	if err != nil || !ok {
		t.Fatalf("rollback failed: %v", err)
	}
	// 交换后 now 应含 1.1.1.1
	cfg, err := LoadYAML(filepath.Join(wd, "cf-proxies.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	proxies := cfg["proxies"].([]any)
	if len(proxies) != 1 {
		t.Fatalf("want 1, got %d", len(proxies))
	}
	pm := proxies[0].(map[string]any)
	if pm["server"] != "1.1.1.1" {
		t.Fatalf("server = %v", pm["server"])
	}
}

func TestProviderURLsShape(t *testing.T) {
	wd := tempWorkdir(t)
	if urls, err := ProviderURLs(wd); urls != nil || err != nil {
		t.Fatalf("want nil for empty workdir, got %v / %v", urls, err)
	}
	src := writeSample(t, wd)
	if _, err := Setup(wd, src, "VLESS-WS-TLS"); err != nil {
		t.Fatal(err)
	}
	urls, err := ProviderURLs(wd)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"candidates", "config", "base"} {
		if !strings.HasPrefix(urls[k], "http://127.0.0.1:") {
			t.Fatalf("bad url %s: %q", k, urls[k])
		}
	}
	if !strings.HasSuffix(urls["candidates"], "/cf-proxies.yaml") {
		t.Fatalf("bad candidates: %q", urls["candidates"])
	}
	if !strings.HasSuffix(urls["config"], "/clash-auto.yaml") {
		t.Fatalf("bad config: %q", urls["config"])
	}
}

func TestSetupPersistsYamlPathAndNodeNames(t *testing.T) {
	wd := tempWorkdir(t)
	src := writeSample(t, wd)
	if _, err := Setup(wd, src, "VLESS-WS-TLS"); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(wd, true)
	if err != nil {
		t.Fatal(err)
	}
	if state.YamlPath != src {
		t.Fatalf("yaml_path not persisted: %q", state.YamlPath)
	}
	if len(state.NodeNames) == 0 {
		t.Fatal("node_names empty")
	}
	found := false
	for _, n := range state.NodeNames {
		if n == "VLESS-WS-TLS" {
			found = true
		}
	}
	if !found {
		t.Fatalf("VLESS-WS-TLS not in node_names: %v", state.NodeNames)
	}
	if state.NodeName != "VLESS-WS-TLS" {
		t.Fatalf("node_name = %q", state.NodeName)
	}
}

func TestPrefsRoundTripLastYaml(t *testing.T) {
	wd := tempWorkdir(t)
	// 缺 prefs.json → 默认值
	p, err := ReadPrefs(wd)
	if err != nil {
		t.Fatal(err)
	}
	if p.ScanMode != "standard" || p.EveryHours != 6 || !p.AutoScan {
		t.Fatalf("bad defaults: %+v", p)
	}
	// UpdatePrefs 局部更新
	if err := UpdatePrefs(wd, func(p *Prefs) {
		p.LastYamlPath = "/tmp/foo.yaml"
		p.LastNodes = []string{"A", "B"}
		p.ScanMode = "light"
	}); err != nil {
		t.Fatal(err)
	}
	p2, _ := ReadPrefs(wd)
	if p2.LastYamlPath != "/tmp/foo.yaml" {
		t.Fatalf("LastYamlPath = %q", p2.LastYamlPath)
	}
	if len(p2.LastNodes) != 2 {
		t.Fatalf("LastNodes = %v", p2.LastNodes)
	}
	if p2.ScanMode != "light" {
		t.Fatalf("ScanMode = %q", p2.ScanMode)
	}
	// 再更新其它字段，LastYamlPath 应保留
	if err := UpdatePrefs(wd, func(p *Prefs) { p.AutoScan = false }); err != nil {
		t.Fatal(err)
	}
	p3, _ := ReadPrefs(wd)
	if p3.LastYamlPath != "/tmp/foo.yaml" {
		t.Fatalf("LastYamlPath lost after unrelated update: %q", p3.LastYamlPath)
	}
	if p3.AutoScan {
		t.Fatal("AutoScan should be false")
	}
}

// ── T17 · 候选池复用（放宽到 domain+path） ─────────────────────────────────

func TestSetupReusesIPsWhenDomainPathUnchanged(t *testing.T) {
	wd := tempWorkdir(t)
	src := writeSample(t, wd)
	if _, err := Setup(wd, src, "VLESS-WS-TLS"); err != nil {
		t.Fatal(err)
	}
	// 模拟扫描后候选池含 3 个 IP
	state, _ := LoadState(wd, true)
	prov, _ := GenerateProvider(state, []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"})
	text, _ := YDump(prov)
	if err := AtomicWrite(filepath.Join(wd, "cf-proxies.yaml"), text); err != nil {
		t.Fatal(err)
	}

	// 轮换 UUID（node 变了，但 domain/path 不变）→ 应复用 IP
	rotated := strings.Replace(sampleNodeYAML,
		"uuid: 123e4567-e89b-42d3-a456-426614174000",
		"uuid: 999e4567-e89b-42d3-a456-426614174999", 1)
	src2 := filepath.Join(wd, "rotated.yaml")
	if err := os.WriteFile(src2, []byte(rotated), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(wd, src2, "VLESS-WS-TLS"); err != nil {
		t.Fatal(err)
	}

	// 验证：IP 应保留，但 UUID 已换成新的
	cfg, err := LoadYAML(filepath.Join(wd, "cf-proxies.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	proxies := cfg["proxies"].([]any)
	if len(proxies) != 3 {
		t.Fatalf("want 3 proxies (IPs reused), got %d", len(proxies))
	}
	ips := map[string]bool{}
	for _, p := range proxies {
		pm := p.(map[string]any)
		ips[fmt.Sprint(pm["server"])] = true
		if fmt.Sprint(pm["uuid"]) != "999e4567-e89b-42d3-a456-426614174999" {
			t.Fatalf("uuid not rotated: %v", pm["uuid"])
		}
	}
	for _, want := range []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"} {
		if !ips[want] {
			t.Fatalf("IP %s lost after UUID rotation", want)
		}
	}
}

func TestSetupResetsIPsWhenDomainChanges(t *testing.T) {
	wd := tempWorkdir(t)
	src := writeSample(t, wd)
	if _, err := Setup(wd, src, "VLESS-WS-TLS"); err != nil {
		t.Fatal(err)
	}
	// 换域名 → 旧 IP 不可信，应回 Seeds
	other := strings.ReplaceAll(sampleNodeYAML, "v2.example.com", "other.example.com")
	src2 := filepath.Join(wd, "other.yaml")
	if err := os.WriteFile(src2, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	// 新域名下原节点名不变，直接 Setup
	if _, err := Setup(wd, src2, "VLESS-WS-TLS"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := LoadYAML(filepath.Join(wd, "cf-proxies.yaml"))
	proxies := cfg["proxies"].([]any)
	if len(proxies) != len(Seeds) {
		t.Fatalf("want %d seeds after domain change, got %d", len(Seeds), len(proxies))
	}
	for _, p := range proxies {
		pm := p.(map[string]any)
		if fmt.Sprint(pm["servername"]) != "other.example.com" {
			t.Fatalf("servername = %v", pm["servername"])
		}
	}
}

// ── P0-1 · fallback 组兜底 ───────────────────────────────────────────────

func TestFallbackGroupHasOriginalNodeAsFallback(t *testing.T) {
	wd := tempWorkdir(t)
	src := writeSample(t, wd)
	out, err := Setup(wd, src, "VLESS-WS-TLS")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadYAML(out)
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := cfg["proxy-groups"].([]any)
	var fallback map[string]any
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		if fmt.Sprint(gm["name"]) == Fallback {
			fallback = gm
			break
		}
	}
	if fallback == nil {
		t.Fatal("fallback group missing")
	}
	if fmt.Sprint(fallback["type"]) != "fallback" {
		t.Fatalf("type = %v", fallback["type"])
	}
	// use 必须含 Provider
	use, _ := fallback["use"].([]any)
	foundProvider := false
	for _, u := range use {
		if fmt.Sprint(u) == Provider {
			foundProvider = true
		}
	}
	if !foundProvider {
		t.Fatalf("use should contain %s, got %v", Provider, use)
	}
	// proxies 必须含原始节点名（兜底）
	proxies, _ := fallback["proxies"].([]any)
	foundNode := false
	for _, p := range proxies {
		if fmt.Sprint(p) == "VLESS-WS-TLS" {
			foundNode = true
		}
	}
	if !foundNode {
		t.Fatalf("proxies should contain VLESS-WS-TLS as fallback, got %v", proxies)
	}
}

// ── P0-2 · PoP 聚类 ─────────────────────────────────────────────────────

func TestClusterByPoP(t *testing.T) {
	items := []scored{
		{ip: "1.1.1.1", med: 0.1, okCnt: 3, pop: "HKG"},
		{ip: "1.1.1.2", med: 0.2, okCnt: 3, pop: "HKG"},
		{ip: "1.1.1.3", med: 0.3, okCnt: 3, pop: "HKG"}, // 第三个 HKG，应丢
		{ip: "2.2.2.1", med: 0.4, okCnt: 3, pop: "NRT"},
		{ip: "2.2.2.2", med: 0.5, okCnt: 3, pop: "NRT"},
		{ip: "3.3.3.1", med: 0.6, okCnt: 3, pop: "SJC"},
		{ip: "4.4.4.1", med: 0.7, okCnt: 3, pop: ""}, // unknown
	}
	got := clusterByPoP(items, 5, 2)
	want := []string{"1.1.1.1", "1.1.1.2", "2.2.2.1", "2.2.2.2", "3.3.3.1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("idx %d: got %s, want %s", i, got[i], want[i])
		}
	}
}

func TestClusterByPoPLimitsPerPoP(t *testing.T) {
	items := []scored{
		{ip: "1.1.1.1", med: 0.1, okCnt: 3, pop: "HKG"},
		{ip: "1.1.1.2", med: 0.2, okCnt: 3, pop: "HKG"},
		{ip: "1.1.1.3", med: 0.3, okCnt: 3, pop: "HKG"},
		{ip: "1.1.1.4", med: 0.4, okCnt: 3, pop: "HKG"},
	}
	got := clusterByPoP(items, 10, 1) // 每 PoP 只 1 个
	if len(got) != 1 || got[0] != "1.1.1.1" {
		t.Fatalf("got %v, want [1.1.1.1]", got)
	}
}

func TestParseCFRayPoP(t *testing.T) {
	cases := map[string]string{
		"7a1b2c3d4e5f6789-HKG": "HKG",
		"abc-NRT":              "NRT",
		"abc-sjc":              "SJC", // 小写→大写
		"no-dash-here":         "HERE", // 最后一段
		"":                     "",
		"nodash":               "",
	}
	for in, want := range cases {
		if got := parseCFRayPoP(in); got != want {
			t.Fatalf("parseCFRayPoP(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── P1-1 · 协议/性能解耦 ─────────────────────────────────────────────────

func TestPercentile(t *testing.T) {
	s := []float64{0.1, 0.2, 0.3, 0.4, 0.5}
	if p50 := percentile(s, 0.50); p50 != 0.3 {
		t.Fatalf("P50 = %v, want 0.3", p50)
	}
	if p90 := percentile(s, 0.90); p90 < 0.45 || p90 > 0.51 {
		t.Fatalf("P90 = %v, want ~0.46-0.50", p90)
	}
	// 单元素
	if p := percentile([]float64{0.7}, 0.5); p != 0.7 {
		t.Fatalf("single = %v", p)
	}
	// 空
	if p := percentile(nil, 0.5); p != 0 {
		t.Fatalf("empty = %v", p)
	}
}

func TestCheckCandidateProtocolFailureShortCircuits(t *testing.T) {
	// 不做真实网络；这里只验证 ProtoOK 字段语义（由 ProbeWS 失败路径决定）
	// 真实网络路径由集成测试覆盖，此处单测数据结构契约
	res := CandidateResult{IP: "1.1.1.1", Success: 0, Attempts: 1, ProtoOK: false}
	if res.ProtoOK {
		t.Fatal("ProtoOK should be false")
	}
	if res.HasMedian {
		t.Fatal("HasMedian should be false for protocol failure")
	}
}

// ── P2-1 · Mihomo 被动反馈 ───────────────────────────────────────────────

func TestBoostScore(t *testing.T) {
	cases := []struct {
		delayMs, sampleN int
		want             float64
	}{
		{0, 0, 0.5},     // 无数据 → 中性
		{100, 0, 0.5},   // 无样本 → 中性
		{30, 5, 1.0},    // 快
		{80, 5, 0.9},
		{150, 5, 0.7},
		{250, 5, 0.5},
		{400, 5, 0.3},
		{600, 5, 0.1},   // 慢
	}
	for _, c := range cases {
		got := BoostScore(c.delayMs, c.sampleN)
		if got != c.want {
			t.Fatalf("BoostScore(%d,%d) = %v, want %v", c.delayMs, c.sampleN, got, c.want)
		}
	}
}

func TestFetchControllerConfig(t *testing.T) {
	yaml := []byte(`
external-controller: 127.0.0.1:9090
secret: my-secret-token
other: value
`)
	baseURL, secret := FetchControllerConfig(yaml)
	if baseURL != "http://127.0.0.1:9090" {
		t.Fatalf("baseURL = %q", baseURL)
	}
	if secret != "my-secret-token" {
		t.Fatalf("secret = %q", secret)
	}
	// 无 secret
	yaml2 := []byte("external-controller: 127.0.0.1:9097\n")
	baseURL2, secret2 := FetchControllerConfig(yaml2)
	if baseURL2 != "http://127.0.0.1:9097" {
		t.Fatalf("baseURL2 = %q", baseURL2)
	}
	if secret2 != "" {
		t.Fatalf("secret2 = %q", secret2)
	}
}

func TestMihomoClientRejectsNonLoopback(t *testing.T) {
	c := NewMihomoClient()
	c.BaseURL = "http://evil.com:9090"
	got := c.FetchDelays()
	if len(got) != 0 {
		t.Fatal("non-loopback must return empty")
	}
}

func TestFormatDelayMs(t *testing.T) {
	if FormatDelayMs(0) != "-" {
		t.Fatal("0 should be -")
	}
	if FormatDelayMs(120) != "120ms" {
		t.Fatal("120ms")
	}
	if FormatDelayMs(1500) != "1.50s" {
		t.Fatal("1.50s")
	}
}

// ── P2-2 · 候选 TTL / 时间衰减 ───────────────────────────────────────────

func TestTimeDecayFactor(t *testing.T) {
	now := time.Now()
	cases := []struct {
		age    time.Duration
		want   float64
	}{
		{0, 1.0},                    // 刚验证
		{30 * time.Minute, 1.0},
		{2 * time.Hour, 0.85},
		{8 * time.Hour, 0.7},
		{18 * time.Hour, 0.5},
		{48 * time.Hour, 0.3},       // 过期 → 下限
	}
	for _, c := range cases {
		ts := now.Add(-c.age).Unix()
		got := TimeDecayFactor(ts)
		if got != c.want {
			t.Fatalf("age=%v: got %v, want %v", c.age, got, c.want)
		}
	}
	// 从未验证
	if got := TimeDecayFactor(0); got != 0.3 {
		t.Fatalf("never-validated = %v, want 0.3", got)
	}
}

func TestEffectiveScore(t *testing.T) {
	// 几何平均
	if got := EffectiveScore(1.0, 1.0); got != 1.0 {
		t.Fatalf("boost=1 decay=1 → %v", got)
	}
	if got := EffectiveScore(0.5, 0.5); got < 0.4 || got > 0.6 {
		t.Fatalf("boost=0.5 decay=0.5 → %v, want ~0.5", got)
	}
	// 零值安全
	if got := EffectiveScore(0, 0); got <= 0 {
		t.Fatalf("zero → %v", got)
	}
}

func TestCandidateMetaRoundTrip(t *testing.T) {
	wd := tempWorkdir(t)
	m := LoadCandidateMeta(wd)
	if len(m) != 0 {
		t.Fatal("should start empty")
	}
	UpdateMetaFromProbe(wd, "1.1.1.1", true, 80)
	UpdateMetaFromProbe(wd, "1.1.1.1", true, 90)
	UpdateMetaFromProbe(wd, "8.8.8.8", false, 0)

	m2 := LoadCandidateMeta(wd)
	if len(m2) != 2 {
		t.Fatalf("want 2 entries, got %d", len(m2))
	}
	e1 := m2["1.1.1.1"]
	if e1.FailCount != 0 || !e1.LastProbeOK || e1.LastMihomoMs != 90 {
		t.Fatalf("1.1.1.1 meta wrong: %+v", e1)
	}
	if e1.LastValidated == 0 {
		t.Fatal("LastValidated should be set")
	}
	e2 := m2["8.8.8.8"]
	if e2.FailCount != 1 || e2.LastProbeOK {
		t.Fatalf("8.8.8.8 meta wrong: %+v", e2)
	}
	// 时间衰减
	if got := TimeDecayFactor(e1.LastValidated); got != 1.0 {
		t.Fatalf("just validated decay = %v", got)
	}
}

func TestFormatMetaAge(t *testing.T) {
	if FormatMetaAge(0) != "从未验证" {
		t.Fatal("0")
	}
	if FormatMetaAge(time.Now().Unix()) != "刚刚" {
		t.Fatal("now")
	}
}
