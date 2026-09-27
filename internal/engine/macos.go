package engine

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// LaunchAgentLabel launchd 任务 Label（与 Python 版一致）。
const LaunchAgentLabel = "com.cf-auto-desktop.menubar"

// LoginPlistPath 返回 LaunchAgents plist 路径。
func LoginPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist"), nil
}

// LoginEnabled 判断是否已设置开机自启。
func LoginEnabled() bool {
	p, err := LoginPlistPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// StartupCommand 返回启动命令（.app 或开发模式）。
func StartupCommand() []string {
	exe, err := os.Executable()
	if err != nil {
		return []string{"/usr/bin/open", "-a", AppName}
	}
	// .app/Contents/MacOS/<bin> → .app
	dir := filepath.Dir(exe)
	if strings.HasSuffix(filepath.Dir(dir), "Contents") {
		bundle := filepath.Dir(filepath.Dir(dir))
		return []string{"/usr/bin/open", "-a", bundle, "--args", "--background"}
	}
	return []string{exe, "--background"}
}

// SetLogin 启用/禁用开机自启（通过 launchd）。
func SetLogin(enabled bool) error {
	path, err := LoginPlistPath()
	if err != nil {
		return err
	}
	if enabled {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		args := StartupCommand()
		// 生成 launchd plist
		home, _ := os.UserHomeDir()
		logPath := filepath.Join(home, "Library", "Application Support", AppName, "launch.log")
		errPath := filepath.Join(home, "Library", "Application Support", AppName, "launch-error.log")
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
		sb.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
		sb.WriteString(`<plist version="1.0"><dict>` + "\n")
		sb.WriteString(fmt.Sprintf("  <key>Label</key><string>%s</string>\n", LaunchAgentLabel))
		sb.WriteString("  <key>ProgramArguments</key><array>\n")
		for _, a := range args {
			sb.WriteString(fmt.Sprintf("    <string>%s</string>\n", xmlEscape(a)))
		}
		sb.WriteString("  </array>\n")
		sb.WriteString("  <key>RunAtLoad</key><true/>\n")
		sb.WriteString("  <key>KeepAlive</key><false/>\n")
		sb.WriteString(fmt.Sprintf("  <key>StandardOutPath</key><string>%s</string>\n", xmlEscape(logPath)))
		sb.WriteString(fmt.Sprintf("  <key>StandardErrorPath</key><string>%s</string>\n", xmlEscape(errPath)))
		sb.WriteString("</dict></plist>\n")

		if err := AtomicWrite(path, sb.String()); err != nil {
			return err
		}
		// 尝试立即 bootstrap（失败不致命，登录时生效）
		cmd := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path)
		out, err := cmd.CombinedOutput()
		if err != nil {
			s := strings.ToLower(string(out))
			if !strings.Contains(s, "already") {
				return fmt.Errorf("已创建登录任务，但立即启用失败；重新登录后生效：%s", strings.TrimSpace(string(out)))
			}
		}
		return nil
	}
	// disable
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), LaunchAgentLabel).Run()
	_ = os.Remove(path)
	return nil
}

// SingleInstanceLock 用 flock 保证单实例；返回解锁函数。
func SingleInstanceLock() (func(), error) {
	home, err := AppHome()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(home, "instance.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("程序已经在运行。请查看 macOS 菜单栏。")
	}
	unlock := func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
	return unlock, nil
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
