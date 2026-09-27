package engine

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
)

// ProviderServer 只绑 127.0.0.1，随机 token URL；对应 Python ProviderServer。
//
// 端点：
//
//	GET /<token>/cf-proxies.yaml    → 动态候选池（供 proxy-providers 每 60s 拉取）
//	GET /<token>/clash-auto.yaml    → 完整外壳（供 Clash Party 以「订阅 URL」导入）
//	GET /<token>/clash-auto.yml     → 别名
type ProviderServer struct {
	workdir string
	srv     *http.Server
	mu      sync.Mutex
}

// NewProviderServer 构造。
func NewProviderServer(workdir string) *ProviderServer {
	return &ProviderServer{workdir: workdir}
}

// Start 启动 HTTP 服务（阻塞式 ListenAndServe 在 goroutine）。
func (p *ProviderServer) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.srv != nil {
		return nil
	}
	state, err := LoadState(p.workdir, true)
	if err != nil {
		return err
	}
	token := state.Token

	routes := map[string]string{
		"/" + token + "/cf-proxies.yaml": filepath.Join(p.workdir, "cf-proxies.yaml"),
		"/" + token + "/clash-auto.yaml": filepath.Join(p.workdir, "clash-auto.yaml"),
		"/" + token + "/clash-auto.yml":  filepath.Join(p.workdir, "clash-auto.yaml"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		u, err := url.Parse(r.URL.String())
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		target, ok := routes[u.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, err := os.ReadFile(target)
		if err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})

	p.srv = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", Port),
		Handler: mux,
	}
	go func() {
		_ = p.srv.ListenAndServe()
	}()
	return nil
}

// Stop 停止服务。
func (p *ProviderServer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.srv != nil {
		_ = p.srv.Close()
		p.srv = nil
	}
}

// ProviderURLs 给 UI 用的三条 URL（含 token）。
func ProviderURLs(workdir string) (map[string]string, error) {
	state, err := LoadState(workdir, false)
	if err != nil || state == nil {
		return nil, err
	}
	base := fmt.Sprintf("http://127.0.0.1:%d/%s", Port, state.Token)
	return map[string]string{
		"candidates": base + "/cf-proxies.yaml",
		"config":     base + "/clash-auto.yaml",
		"base":       base,
	}, nil
}
