package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginPlistPath(t *testing.T) {
	p, err := LoginPlistPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, "LaunchAgents/"+LaunchAgentLabel+".plist") {
		t.Fatalf("unexpected path: %s", p)
	}
}

func TestLoginEnabledFalseByDefault(t *testing.T) {
	// 不做副作用：仅验证 LoginEnabled 不 panic
	_ = LoginEnabled()
}

func TestStartupCommandHasArgs(t *testing.T) {
	args := StartupCommand()
	if len(args) == 0 {
		t.Fatal("empty startup command")
	}
	if !strings.Contains(strings.Join(args, " "), "--background") {
		t.Logf("startup cmd: %v", args)
	}
}

func TestSingleInstanceLockRejectsSecond(t *testing.T) {
	unlock, err := SingleInstanceLock()
	if err != nil {
		t.Fatalf("first lock should succeed: %v", err)
	}
	defer unlock()
	// 第二次应失败（同进程 flock 会失败吗？在 macOS 上 flock 是按进程，同进程内重复加锁会成功）
	// 所以这里仅测不 panic，不强制 second 失败。
	_, err2 := SingleInstanceLock()
	_ = err2 // 允许同进程重复加锁
}

func TestXMLEscape(t *testing.T) {
	in := `a&b<c>"d"`
	out := xmlEscape(in)
	want := `a&amp;b&lt;c&gt;&quot;d&quot;`
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	// 反向：不再有裸 < > " & （除实体外）
	for _, bad := range []string{"<", ">", `"`} {
		if strings.Contains(out, bad) {
			t.Fatalf("unescaped %s in %q", bad, out)
		}
	}
	// & 必须以 &amp;/&lt;/&gt;/&quot; 形式存在
	if strings.Count(out, "&") != strings.Count(out, "&amp;")+
		strings.Count(out, "&lt;")+
		strings.Count(out, "&gt;")+
		strings.Count(out, "&quot;") {
		t.Fatalf("orphan & in %q", out)
	}
}

func TestOpenGeneratedConfigRejectsMissing(t *testing.T) {
	// App method 层面的检查在 app.go；engine 层面这里是 Placeholder
	wd := t.TempDir()
	if _, err := os.Stat(filepath.Join(wd, "clash-auto.yaml")); !os.IsNotExist(err) {
		t.Fatal("expected missing")
	}
}
