package main

import (
	"context"
	"fmt"
	"runtime"
)

// App struct
type App struct {
	ctx context.Context
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// shutdown is called when the app is stopping
func (a *App) shutdown(ctx context.Context) {}

// Greet returns a greeting for the given name (PoC binding)
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, from Go %s on %s/%s",
		name, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// SystemInfo returns runtime info (PoC binding)
func (a *App) SystemInfo() map[string]string {
	return map[string]string{
		"go":   runtime.Version(),
		"os":   runtime.GOOS,
		"arch": runtime.GOARCH,
	}
}
