package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"cf-auto-go/internal/engine"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

)

// App struct — Wails 绑定到前端 window.go.main.App.*
type App struct {
	ctx         context.Context
	provider    *engine.ProviderServer
	providerMu  sync.Mutex
	scanStop    *engine.StopEvent
	scanRunning bool
	scanMu      sync.Mutex
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// 自动拉起 Provider：只要 state.json 存在（说明生成过配置），
	// 就让 HTTP Provider 监听 127.0.0.1:17653，供 Clash 每 60s 拉取。
	// 失败只记日志不阻断启动（可能端口被旧实例占用等）。
	go func() {
		wd, err := a.workdir()
		if err != nil {
			return
		}
		if state, _ := engine.LoadState(wd, false); state == nil {
			// 未生成过配置，不起 Provider
			return
		}
		if err := a.ensureProvider(); err != nil {
			a.emitLog("Provider 启动失败：" + err.Error() + "（检查是否旧实例占用 17653）")
		} else {
			a.emitLog(fmt.Sprintf("Provider 已启动：127.0.0.1:%d", engine.Port))
		}
	}()
}

func (a *App) shutdown(ctx context.Context) {
	a.providerMu.Lock()
	if a.provider != nil {
		a.provider.Stop()
		a.provider = nil
	}
	a.providerMu.Unlock()
}

// ── PoC bindings（保留以向后兼容） ────────────────────────────────────────

// Greet returns a greeting for the given name.
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, from Go %s on %s/%s",
		name, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// SystemInfo returns runtime info.
func (a *App) SystemInfo() map[string]string {
	return map[string]string{
		"go":   runtime.Version(),
		"os":   runtime.GOOS,
		"arch": runtime.GOARCH,
	}
}

// ── 桥接 helper ─────────────────────────────────────────────────────────

func (a *App) workdir() (string, error) {
	return engine.AppHome()
}

// Confirm 弹系统 Yes/No 对话框（WKWebView 的 window.confirm 不可靠，走 Wails 原生）。
func (a *App) Confirm(title, message string) (bool, error) {
	if a.ctx == nil {
		return false, fmt.Errorf("app not ready")
	}
	res, err := wailsruntime.MessageDialog(a.ctx, wailsruntime.MessageDialogOptions{
		Type:          wailsruntime.QuestionDialog,
		Title:         title,
		Message:       message,
		Buttons:       []string{"确认", "取消"},
		DefaultButton: "确认",
		CancelButton:  "取消",
	})
	if err != nil {
		return false, err
	}
	return res == "确认", nil
}

// Alert 弹系统信息对话框。
func (a *App) Alert(title, message string) error {
	if a.ctx == nil {
		return fmt.Errorf("app not ready")
	}
	_, err := wailsruntime.MessageDialog(a.ctx, wailsruntime.MessageDialogOptions{
		Type:    wailsruntime.InfoDialog,
		Title:   title,
		Message: message,
		Buttons: []string{"好"},
	})
	return err
}

// ── 状态 / 配置查询 ─────────────────────────────────────────────────────

// GetStatus 供前端启动时读取 UI 初值。
// 返回值包含 UI 恢复所需的一切：yaml_path、节点列表、订阅 URL、prefs、cfst。
func (a *App) GetStatus() map[string]any {
	wd, err := a.workdir()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	out := map[string]any{"workdir": wd}

	prefs, _ := engine.ReadPrefs(wd)
	out["cfst"] = ""
	if prefs.Cfst != "" {
		out["cfst"] = filepath.Base(filepath.Dir(prefs.Cfst))
		out["cfst_path"] = prefs.Cfst
	}
	out["auto_scan"] = prefs.AutoScan
	out["every_hours"] = prefs.EveryHours
	out["scan_mode"] = prefs.ScanMode
	out["last_status"] = prefs.LastStatus

	if state, _ := engine.LoadState(wd, false); state != nil {
		out["configured"] = true
		out["node_name"] = state.NodeName
		out["yaml_path"] = state.YamlPath
		out["node_names"] = state.NodeNames
		// 不把 domain / uuid 泄露给前端（隐私脱敏，与 Python 版一致）
	} else {
		out["configured"] = false
		// 即便未生成配置，也把最近导入的 YAML 恢复出来
		out["yaml_path"] = prefs.LastYamlPath
		out["node_names"] = prefs.LastNodes
	}

	if urls, _ := engine.ProviderURLs(wd); urls != nil {
		out["subscribe_url"] = urls["config"]
		out["candidates_url"] = urls["candidates"]
	}
	return out
}

// CopySubscribeURL 返回订阅 URL（前端负责写剪贴板）。
func (a *App) CopySubscribeURL() (string, error) {
	wd, err := a.workdir()
	if err != nil {
		return "", err
	}
	urls, err := engine.ProviderURLs(wd)
	if err != nil {
		return "", err
	}
	if urls == nil {
		return "", fmt.Errorf("请先生成 Clash 配置")
	}
	return urls["config"], nil
}

// ── YAML 导入 / 生成 ─────────────────────────────────────────────────────

// OpenYAMLDialog 打开系统文件对话框让用户选 YAML；取消时返回空路径。
func (a *App) OpenYAMLDialog() (string, error) {
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择原始 Clash YAML",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Clash 配置 (*.yaml, *.yml)", Pattern: "*.yaml;*.yml"},
			{DisplayName: "所有文件", Pattern: "*"},
		},
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// OpenCfstDialog 打开系统文件对话框让用户选 cfst 可执行；取消时返回空路径。
func (a *App) OpenCfstDialog() (string, error) {
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "选择官方 CloudflareSpeedTest 可执行文件",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "可执行文件 (cfst, CloudflareSpeedTest)", Pattern: "cfst;CloudflareSpeedTest;*"},
		},
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// ImportYAML 校验 YAML 并返回可选节点列表（前端后续调用 GenerateConfig）。
// 导入成功即把路径与节点列表写入 prefs，重启后 UI 可恢复（即使尚未生成配置）。
func (a *App) ImportYAML(path string) (map[string]any, error) {
	if path == "" {
		return nil, fmt.Errorf("未选择文件")
	}
	cfg, err := engine.LoadYAML(path)
	if err != nil {
		return nil, err
	}
	nodes := engine.EligibleNodes(cfg)
	if len(nodes) == 0 {
		return nil, fmt.Errorf("没有找到直接写在 proxies 中的 VLESS + WS + TLS 节点")
	}
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		name, _ := n["name"].(string)
		names = append(names, name)
	}
	// 优先 DNS 节点（非 IPv4）
	preferred := ""
	for _, n := range nodes {
		srv := fmt.Sprint(n["server"])
		if !engine.IPv4(srv) {
			preferred, _ = n["name"].(string)
			break
		}
	}

	// 持久化到 prefs（重启后 UI 可恢复）
	wd, err := a.workdir()
	if err == nil {
		_ = engine.UpdatePrefs(wd, func(p *engine.Prefs) {
			p.LastYamlPath = path
			p.LastNodes = names
		})
	}

	return map[string]any{
		"nodes":     names,
		"preferred": preferred,
		"path":      path,
	}, nil
}

// GenerateConfig 生成 clash-auto.yaml 并启动 Provider。
func (a *App) GenerateConfig(yamlPath, nodeName string) (map[string]any, error) {
	wd, err := a.workdir()
	if err != nil {
		return nil, err
	}
	out, err := engine.Setup(wd, yamlPath, nodeName)
	if err != nil {
		return nil, err
	}
	if err := a.ensureProvider(); err != nil {
		return nil, err
	}
	urls, _ := engine.ProviderURLs(wd)
	return map[string]any{
		"generated":     out,
		"subscribe_url": urls["config"],
	}, nil
}

func (a *App) ensureProvider() error {
	a.providerMu.Lock()
	defer a.providerMu.Unlock()
	if a.provider != nil {
		a.provider.Stop()
	}
	wd, err := a.workdir()
	if err != nil {
		return err
	}
	p := engine.NewProviderServer(wd)
	if err := p.Start(); err != nil {
		return err
	}
	a.provider = p
	return nil
}

// StartProvider 手动启动 HTTP Provider（前端可在检测到未监听时调用）。
func (a *App) StartProvider() (map[string]any, error) {
	if err := a.ensureProvider(); err != nil {
		return nil, err
	}
	urls, _ := engine.ProviderURLs(mustWorkdir())
	out := map[string]any{"ok": true}
	if urls != nil {
		out["url"] = urls
	}
	return out, nil
}

// ProviderStatus 查询 Provider 是否在监听。
func (a *App) ProviderStatus() map[string]any {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", engine.Port), 200*time.Millisecond)
	if err != nil {
		return map[string]any{"listening": false}
	}
	_ = conn.Close()
	urls, _ := engine.ProviderURLs(mustWorkdir())
	out := map[string]any{"listening": true}
	if urls != nil {
		out["url"] = urls
	}
	return out
}

func mustWorkdir() string {
	wd, _ := engine.AppHome()
	return wd
}

// ── cfst 下载 / 导入 ─────────────────────────────────────────────────────

// DownloadCfst 官方下载；进度通过 runtime EventsEmit 推给前端。
func (a *App) DownloadCfst() (map[string]any, error) {
	wd, err := a.workdir()
	if err != nil {
		return nil, err
	}
	log := func(msg string) {
		a.emitLog(msg)
	}
	progress := func(done, total int64) {
		if total > 0 && done*10%total == 0 {
			a.emitLog(fmt.Sprintf("下载中… %.1f/%.1f MB", float64(done)/1e6, float64(total)/1e6))
		}
	}
	dest, err := engine.DownloadCfst(wd, log, progress, engine.NewStopEvent())
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": dest}, nil
}

// ImportCfst 手动导入本地 cfst + ip.txt。
func (a *App) ImportCfst(path string) (map[string]any, error) {
	wd, err := a.workdir()
	if err != nil {
		return nil, err
	}
	dest, err := engine.ImportCfst(wd, path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": dest}, nil
}

// ── 扫描 ───────────────────────────────────────────────────────────────

// StartScan 后台扫描；完成后通过 EventsEmit 推送结果。
func (a *App) StartScan(dryRun bool) (map[string]any, error) {
	a.scanMu.Lock()
	if a.scanRunning {
		a.scanMu.Unlock()
		return nil, fmt.Errorf("已有扫描在进行")
	}
	wd, err := a.workdir()
	if err != nil {
		a.scanMu.Unlock()
		return nil, err
	}
	if state, _ := engine.LoadState(wd, false); state == nil {
		a.scanMu.Unlock()
		return nil, fmt.Errorf("请先生成 Clash 配置")
	}
	if prefs, _ := engine.ReadPrefs(wd); prefs.Cfst == "" {
		a.scanMu.Unlock()
		return nil, fmt.Errorf("请先导入或下载 CloudflareSpeedTest")
	}
	a.scanStop = engine.NewStopEvent()
	a.scanRunning = true
	a.scanMu.Unlock()

	go func() {
		defer func() {
			a.scanMu.Lock()
			a.scanRunning = false
			a.scanStop = nil
			a.scanMu.Unlock()
		}()
		log := func(msg string) { a.emitLog(msg) }
		report, err := engine.DoScan(wd, a.scanStop, log, dryRun, "", "", "")
		if err != nil {
			a.emitEvent("scan-failed", map[string]any{"error": err.Error()})
			return
		}
		a.emitEvent("scan-completed", map[string]any{
			"chosen":    report.Chosen,
			"qualified": report.Qualified,
			"candidates": report.Candidates,
		})
	}()
	return map[string]any{"started": true}, nil
}

// StopScan 停止扫描。
func (a *App) StopScan() map[string]any {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if a.scanStop != nil {
		a.scanStop.Set()
		return map[string]any{"stopped": true}
	}
	return map[string]any{"stopped": false}
}

// ── 其它 ──────────────────────────────────────────────────────────────

// Rollback 恢复上一次候选池。
func (a *App) Rollback() (map[string]any, error) {
	wd, err := a.workdir()
	if err != nil {
		return nil, err
	}
	ok, err := engine.Rollback(wd)
	if err != nil {
		return nil, err
	}
	return map[string]any{"rolled": ok}, nil
}

// ── macOS 集成 ─────────────────────────────────────────────────────────

// LoginEnabled 查询是否设置登录自启。
func (a *App) LoginEnabled() bool {
	return engine.LoginEnabled()
}

// SetLogin 开关登录自启。
func (a *App) SetLogin(enabled bool) (map[string]any, error) {
	if err := engine.SetLogin(enabled); err != nil {
		return nil, err
	}
	return map[string]any{"enabled": engine.LoginEnabled()}, nil
}

// OpenConfigFolder 在 Finder 中打开工作目录。
func (a *App) OpenConfigFolder() error {
	wd, err := a.workdir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(wd, 0o700); err != nil {
		return err
	}
	return exec.Command("open", wd).Start()
}

// OpenGeneratedConfig 复制路径到剪贴板 + 打开工作目录。
func (a *App) OpenGeneratedConfig() (map[string]any, error) {
	wd, err := a.workdir()
	if err != nil {
		return nil, err
	}
	p := filepath.Join(wd, "clash-auto.yaml")
	if _, err := os.Stat(p); err != nil {
		return nil, fmt.Errorf("请先生成 Clash 配置")
	}
	// 前端负责写剪贴板，这里只返回路径
	if err := exec.Command("open", wd).Start(); err != nil {
		return nil, err
	}
	return map[string]any{"path": p}, nil
}

// emitLog / emitEvent 帮前端接 EventsOn("log" / "scan-*")。
func (a *App) emitLog(msg string) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "log", msg)
	}
}

func (a *App) emitEvent(name string, data map[string]any) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, name, data)
	}
}

// ── 窗口控制（托盘集成用） ───────────────────────────────────────────────

// ShowWindow 显示并前置主窗口（托盘 / Dock 唤回）。
//
// 走 app 级 runtime.Show（NSApp unhide + activateIgnoringOtherApps），
// 而非 window 级 WindowShow —— 后者只 makeKeyAndOrderFront 对 orderOut 的
// 窗口无效（Wails v2 AppDelegate 缺 applicationShouldHandleReopen）。
func (a *App) ShowWindow() {
	if a.ctx != nil {
		wailsruntime.Show(a.ctx) // NSApp unhide + activateIgnoringOtherApps
		wailsruntime.WindowUnminimise(a.ctx)
		wailsruntime.WindowShow(a.ctx) // makeKeyAndOrderFront（兜底）
	}
}

// HideWindow 隐藏应用到 Dock（关窗 → 后台常驻）。
//
// 走 app 级 runtime.Hide（NSApp hide:），让 Dock 图标变暗；
// Dock 点击时 macOS 自动 unhide，窗口按隐藏前状态回来。
// 不用 WindowHide（orderOut）：那会让 Dock 点击唤不回窗口。
func (a *App) HideWindow() {
	if a.ctx != nil {
		wailsruntime.Hide(a.ctx) // NSApp hide:
	}
}

// QuitApp 真正退出应用（托盘「退出程序」）。
// 先停 Provider 与扫描，再让 Wails 主循环结束。
func (a *App) QuitApp() {
	a.scanMu.Lock()
	if a.scanStop != nil {
		a.scanStop.Set()
	}
	a.scanMu.Unlock()

	a.providerMu.Lock()
	if a.provider != nil {
		a.provider.Stop()
		a.provider = nil
	}
	a.providerMu.Unlock()

	if a.ctx != nil {
		wailsruntime.Quit(a.ctx)
	}
}
