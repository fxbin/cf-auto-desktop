package main

import (
	"embed"
	"log"

	"cf-auto-go/internal/engine"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS


func main() {
	// 单实例锁（防止 launchctl 启两个导致端口冲突）
	unlock, err := engine.SingleInstanceLock()
	if err != nil {
		log.Fatal(err)
	}
	defer unlock()

	app := NewApp()

	err = wails.Run(&options.App{
		Title:     "CF Auto Desktop",
		Width:     900,
		Height:    860,
		MinWidth:  760,
		MinHeight: 760,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 249, G: 249, B: 249, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		// 关窗 → 隐藏到 Dock（Wails v2 原生 HideWindowOnClose）。
		// 红 × 触发 [NSApp hide:]，窗口不销毁；Dock 点击 macOS 自动 unhide，
		// 窗口按隐藏前状态回来。Cmd+Q / Dock 菜单退出才真正结束进程。
		HideWindowOnClose: true,
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			About: &mac.AboutInfo{
				Title:   "CF Auto Desktop",
				Message: "Cloudflare 候选动态优选 · Clash Party 节点池",
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
