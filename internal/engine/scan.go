package engine

import (
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ScanReport 扫描结果（对应 Python report dict）。
type ScanReport struct {
	Candidates int      `json:"candidates"`
	Qualified  int      `json:"qualified"`
	Chosen     []string `json:"chosen"`
	Measure    string   `json:"measure"`
}

// scored 单个候选 IP 的评分（供 clusterByPoP 用）。
type scored struct {
	ip    string
	med   float64
	okCnt int
	pop   string
}

// CSVIps 从 result.csv 读前 limit 个去重 IPv4。
func CSVIps(csvFile string, limit int) ([]string, error) {
	f, err := os.Open(csvFile)
	if err != nil {
		return nil, fmt.Errorf("CloudflareSpeedTest 未输出 result.csv")
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	// 跳过表头
	_, _ = r.Read()
	var out []string
	seen := map[string]bool{}
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		if len(rec) == 0 {
			continue
		}
		ip := strings.TrimSpace(rec[0])
		if !IPv4(ip) || seen[ip] {
			continue
		}
		seen[ip] = true
		out = append(out, ip)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// DoScan 扫描、二次校验、原子发布。失败保留旧节点池。
func DoScan(workdir string, stop *StopEvent, log func(string), dryRun bool,
	cfstOverride, csvOverride string, scanMode string) (*ScanReport, error) {
	if stop == nil {
		stop = NewStopEvent()
	}
	if log == nil {
		log = func(string) {}
	}
	state, err := LoadState(workdir, true)
	if err != nil {
		return nil, err
	}
	prefs, err := ReadPrefs(workdir)
	if err != nil {
		return nil, err
	}
	preset := ScanPresets[scanMode]
	if preset.PerfRepeat == 0 {
		preset = ScanPresets[prefs.ScanMode]
	}
	if preset.PerfRepeat == 0 {
		preset = ScanPresets["standard"]
	}

	cfst := cfstOverride
	if cfst == "" {
		cfst = prefs.Cfst
	}
	if csvOverride == "" {
		if cfst == "" {
			return nil, fmt.Errorf("请先导入包含 ip.txt 的 CloudflareSpeedTest 可执行文件")
		}
		if _, err := os.Stat(cfst); err != nil {
			return nil, fmt.Errorf("请先导入包含 ip.txt 的 CloudflareSpeedTest 可执行文件")
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(cfst), "ip.txt")); err != nil {
			return nil, fmt.Errorf("请先导入包含 ip.txt 的 CloudflareSpeedTest 可执行文件")
		}
	}

	if !dryRun {
		_ = UpdatePrefs(workdir, func(p *Prefs) {
			p.LastAttempt = float64(time.Now().Unix())
			p.LastStatus = "扫描中"
		})
	}

	var pool []string
	if csvOverride != "" {
		pool, err = CSVIps(csvOverride, preset.MaxCandidates)
		if err != nil {
			return nil, err
		}
	} else {
		output := filepath.Join(workdir, "cfst-latest.csv")
		_ = os.Remove(output)
		args := []string{
			"-f", "ip.txt",
			"-tp", "443",
			"-tl", fmt.Sprint(preset.CfstTL),
			"-t", fmt.Sprint(preset.CfstT),
			"-n", fmt.Sprint(preset.CfstN),
			"-dd",
			"-o", output,
			"-p", "0",
		}
		cmd := exec.Command(cfst, args...)
		cmd.Dir = filepath.Dir(cfst)
		// 清掉代理环境变量
		env := os.Environ()
		var filtered []string
		for _, kv := range env {
			k := strings.SplitN(kv, "=", 2)[0]
			switch strings.ToLower(k) {
			case "http_proxy", "https_proxy", "all_proxy":
				continue
			}
			filtered = append(filtered, kv)
		}
		cmd.Env = filtered
		logFile, err := os.Create(filepath.Join(workdir, "cfst-run.log"))
		if err != nil {
			return nil, err
		}
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		log("开始 CloudflareSpeedTest TCP 候选扫描（未使用代理环境变量）…")
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("CFST 启动失败：%v", err)
		}
		// 等待 + 超时/取消
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		start := time.Now()
		select {
		case err := <-done:
			if err != nil {
				return nil, fmt.Errorf("CFST 退出代码 %v，请查看本地 cfst-run.log", err)
			}
		case <-time.After(20 * time.Minute):
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("CFST 扫描超时，原节点池不变")
		}
		_ = start
		if stop.IsSet() {
			return nil, fmt.Errorf("已取消扫描，原节点池不变")
		}
		pool, err = CSVIps(output, preset.MaxCandidates)
		if err != nil {
			return nil, err
		}
	}

	// 合并历史 + 种子
	prevPath := filepath.Join(workdir, "cf-proxies.yaml")
	var old []string
	if data, err := LoadYAML(prevPath); err == nil {
		if proxies, ok := data["proxies"].([]any); ok {
			for _, it := range proxies {
				pm, ok := it.(map[string]any)
				if !ok {
					continue
				}
				ip := fmt.Sprint(pm["server"])
				if IPv4(ip) {
					old = append(old, ip)
				}
			}
		}
	}
	merged := map[string]bool{}
	var final []string
	for _, list := range [][]string{pool, old, Seeds} {
		for _, ip := range list {
			if !merged[ip] {
				merged[ip] = true
				final = append(final, ip)
			}
		}
	}

	if stop.IsSet() {
		return nil, fmt.Errorf("已取消扫描，原节点池不变")
	}
	log(fmt.Sprintf("找到 %d 个候选，开始域名 TLS + WebSocket 验证…", len(final)))

	// 并发探活
	results := make([]CandidateResult, len(final))
	var wg sync.WaitGroup
	sem := make(chan struct{}, preset.Workers)
	for i, ip := range final {
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if stop.IsSet() {
				return
			}
			results[i] = CheckCandidate(ip, state.Domain, state.Path, preset.PerfRepeat, stop)
		}(i, ip)
	}
	wg.Wait()
	if stop.IsSet() {
		return nil, fmt.Errorf("已取消扫描，原节点池不变")
	}

	// 合格筛选
	var passing []scored
	for _, r := range results {
		// 协议必须过；延迟 P50 ≤ 3.5s（性能门槛）
		if r.ProtoOK && r.HasMedian && r.Median <= 3.5 {
			passing = append(passing, scored{ip: r.IP, med: r.Median, okCnt: r.Success, pop: r.PoP})
		}
	}
	sort.Slice(passing, func(i, j int) bool {
		if passing[i].okCnt != passing[j].okCnt {
			return passing[i].okCnt > passing[j].okCnt
		}
		return passing[i].med < passing[j].med
	})

	// PoP 聚类选取：每个 PoP 最多保留 2 个 IP，优先保证拓扑多样性。
	// 5 个 IP 若同 PoP 等于 1 个；跨 PoP 才有真正的容灾价值。
	chosen := clusterByPoP(passing, preset.Keep, 2)

	// 日志前 10 快的
	printable := append([]scored{}, passing...)
	sort.Slice(printable, func(i, j int) bool { return printable[i].med < printable[j].med })
	for i, s := range printable {
		if i >= 10 {
			break
		}
		log(fmt.Sprintf("%s  握手 ok · P50 %.3fs · PoP=%s", s.ip, s.med, orDash(s.pop)))
	}

	report := &ScanReport{
		Candidates: len(final),
		Qualified:  len(passing),
		Chosen:     chosen,
		Measure:    "TLS + WebSocket 101; not full proxy bandwidth",
	}

	if len(chosen) < 2 {
		log("合格候选不足 2 个；未覆盖原节点池。请考虑检查整体线路。")
		if !dryRun {
			_ = UpdatePrefs(workdir, func(p *Prefs) {
				p.LastStatus = fmt.Sprintf("未更新：仅 %d 个候选合格", len(chosen))
			})
		}
		return report, nil
	}
	if dryRun {
		log("预览模式，不写入：" + strings.Join(chosen, ", "))
		return report, nil
	}

	// 原子发布
	if current, err := os.ReadFile(prevPath); err == nil {
		_ = AtomicWrite(filepath.Join(workdir, "cf-proxies.previous.yaml"), string(current))
	}
	prov, err := GenerateProvider(state, chosen)
	if err != nil {
		return nil, err
	}
	text, err := YDump(prov)
	if err != nil {
		return nil, err
	}
	if err := AtomicWrite(prevPath, text); err != nil {
		return nil, err
	}
	_ = UpdatePrefs(workdir, func(p *Prefs) {
		p.LastSuccess = float64(time.Now().Unix())
		p.LastStatus = fmt.Sprintf("已更新 %d 个候选", len(chosen))
	})
	log("更新成功。Clash Party 的 HTTP Provider 将在下次拉取时读取新节点。")
	return report, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// clusterByPoP 按 PoP 聚类选取候选 IP。
// 输入已按 (okCnt desc, med asc) 排好序；输出最多 maxTotal 个，
// 每个 PoP 最多 perPoP 个。PoP 为空的候选视为独立组（"unknown"）。
func clusterByPoP(items []scored, maxTotal, perPoP int) []string {
	if maxTotal <= 0 {
		return nil
	}
	poPCount := map[string]int{}
	var out []string
	for _, it := range items {
		if len(out) >= maxTotal {
			break
		}
		key := it.pop
		if key == "" {
			key = "unknown"
		}
		if poPCount[key] >= perPoP {
			continue
		}
		poPCount[key]++
		out = append(out, it.ip)
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
