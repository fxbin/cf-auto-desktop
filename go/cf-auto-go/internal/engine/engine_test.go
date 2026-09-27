package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
