package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HealthProbe 周期性检查候选池健康，主动摘除坏 IP。
//
// 背景：6h 一次的全量扫描跟不上 Anycast 重路由。这个探针每 60s 用
// 1 次 ProbeWS 验证候选池里的 IP 是否还能 TLS+WS 握手；连续失败
// 2 次的 IP 从 cf-proxies.yaml 摘除（保留 ≥1 个避免全空）。
//
// 设计约束：
//   - 只做协议检查（1 次 ProbeWS），不做延迟采样（成本低）
//   - 失败阈值 2 次：避免瞬时抖动误摘
//   - 最终兜底：摘到只剩 1 个就停手（fallback 组还有原节点）
//   - 与 do_scan 并发安全：写 cf-proxies.yaml 走 AtomicWrite
type HealthProbe struct {
	workdir    string
	domain     string
	wspath     string
	interval   time.Duration
	failLimit  int
	stop       *StopEvent
	mu         sync.Mutex
	failCount  map[string]int
	lastResult map[string]time.Time
}

// NewHealthProbe 构造。interval ≤ 0 时用 60s；failLimit ≤ 0 时用 2。
func NewHealthProbe(workdir, domain, wsPath string, interval time.Duration, failLimit int) *HealthProbe {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if failLimit <= 0 {
		failLimit = 2
	}
	return &HealthProbe{
		workdir:    workdir,
		domain:     domain,
		wspath:     wsPath,
		interval:   interval,
		failLimit:  failLimit,
		stop:       NewStopEvent(),
		failCount:  map[string]int{},
		lastResult: map[string]time.Time{},
	}
}

// Start 启动后台循环（阻塞，调用方放 goroutine）。
// 每轮调用 onLog 记录日志（可为 nil）。
func (h *HealthProbe) Start(onLog func(string)) {
	log := onLog
	if log == nil {
		log = func(string) {}
	}
	log(fmt.Sprintf("健康探针启动：每 %s 检查候选池", h.interval))
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop.C():
			log("健康探针已停止")
			return
		case <-ticker.C:
			h.tick(log)
		}
	}
}

// Stop 停止探针。
func (h *HealthProbe) Stop() {
	h.stop.Set()
}

// tick 单轮健康检查。
func (h *HealthProbe) tick(log func(string)) {
	state, err := LoadState(h.workdir, false)
	if err != nil || state == nil {
		return
	}
	prevPath := filepath.Join(h.workdir, "cf-proxies.yaml")
	data, err := LoadYAML(prevPath)
	if err != nil {
		return
	}
	proxies, _ := data["proxies"].([]any)
	if len(proxies) <= 1 {
		return // 已到底线，不再摘
	}

	// 并发快速探测
	var wg sync.WaitGroup
	type res struct {
		ip  string
		ok  bool
		pop string
	}
	results := make([]res, len(proxies))
	for i, it := range proxies {
		pm, _ := it.(map[string]any)
		ip := fmt.Sprint(pm["server"])
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			_, pop, ok := ProbeWS(ip, state.Domain, state.Path, 3*time.Second)
			results[i] = res{ip: ip, ok: ok, pop: pop}
		}(i, ip)
	}
	wg.Wait()

	// 更新失败计数
	h.mu.Lock()
	bad := map[string]bool{}
	for _, r := range results {
		if r.ok {
			h.failCount[r.ip] = 0
			h.lastResult[r.ip] = time.Now()
		} else {
			h.failCount[r.ip]++
			if h.failCount[r.ip] >= h.failLimit {
				bad[r.ip] = true
			}
		}
	}
	h.mu.Unlock()

	if len(bad) == 0 {
		return
	}

	// 保留健康 IP（至少 1 个）
	var keep []string
	for _, r := range results {
		if !bad[r.ip] {
			keep = append(keep, r.ip)
		}
	}
	if len(keep) == 0 {
		// 全坏：保留失败次数最少的 1 个（兜底）
		bestIP, bestFail := "", int(1<<30)
		h.mu.Lock()
		for _, r := range results {
			if h.failCount[r.ip] < bestFail {
				bestIP, bestFail = r.ip, h.failCount[r.ip]
			}
		}
		h.mu.Unlock()
		if bestIP != "" {
			keep = append(keep, bestIP)
		}
		log("候选池全部异常，保留 1 个失败最少的 IP 等待重扫")
	}

	// 生成新的 cf-proxies.yaml
	prov, err := GenerateProvider(state, keep)
	if err != nil {
		log("健康探针生成候选失败：" + err.Error())
		return
	}
	text, err := YDump(prov)
	if err != nil {
		return
	}
	if err := AtomicWrite(prevPath, text); err != nil {
		log("健康探针写入失败：" + err.Error())
		return
	}
	log(fmt.Sprintf("健康探针摘除 %d 个坏 IP，剩余 %d 个", len(bad), len(keep)))
	_ = UpdatePrefs(h.workdir, func(p *Prefs) {
		p.LastStatus = fmt.Sprintf("健康探针摘除 %d 个坏 IP", len(bad))
	})
}

// Status 返回当前失败计数（供 UI 展示）。
func (h *HealthProbe) Status() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]int, len(h.failCount))
	for k, v := range h.failCount {
		out[k] = v
	}
	return out
}

// os is used above; silence unused if any
var _ = os.Getenv
