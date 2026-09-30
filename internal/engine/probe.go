package engine

import (
	"bufio"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"time"
)

// ProbeWS 对 ip:443 建 TLS（SNI=domain）+ 发起 WebSocket Upgrade，
// 要求返回 101 且带正确的 Sec-WebSocket-Accept。
// 返回 (握手耗时秒, CF 边缘 PoP 代码, ok)。
// PoP 从响应头 cf-ray 的后缀取（如 7a1b2c-HKG → "HKG"）。
func ProbeWS(ip, domain, wsPath string, timeout time.Duration) (float64, string, bool) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	start := time.Now()

	// 生成 WebSocket Key
	keyBytes := make([]byte, 16)
	if _, err := randRead(keyBytes); err != nil {
		return 0, "", false
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	h := sha1.New()
	h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	expected := base64.StdEncoding.EncodeToString(h.Sum(nil))

	dialer := &net.Dialer{Timeout: timeout}
	raw, err := dialer.Dial("tcp", net.JoinHostPort(ip, "443"))
	if err != nil {
		return 0, "", false
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(timeout))

	tlsCfg := &tls.Config{
		ServerName:         domain,
		NextProtos:         []string{"http/1.1"},
		InsecureSkipVerify: false,
		MinVersion:         tls.VersionTLS12,
	}
	conn := tls.Client(raw, tlsCfg)
	defer conn.Close()
	if err := conn.Handshake(); err != nil {
		return 0, "", false
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))

	req := fmt.Sprintf(
		"GET %s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Connection: Upgrade\r\n"+
			"Upgrade: websocket\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"Sec-WebSocket-Key: %s\r\n"+
			"User-Agent: CF-Auto-Desktop/1\r\n"+
			"\r\n",
		wsPath, domain, key,
	)
	if _, err := conn.Write([]byte(req)); err != nil {
		return 0, "", false
	}

	// 读响应头直到 \r\n\r\n
	reader := bufio.NewReader(conn)
	var header strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, "", false
		}
		header.WriteString(line)
		if line == "\r\n" || line == "\n" {
			break
		}
		if header.Len() > 16384 {
			return 0, "", false
		}
	}

	lines := strings.Split(strings.TrimRight(header.String(), "\r\n"), "\r\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "HTTP/1.1 101") {
		return 0, "", false
	}
	headers := map[string]string{}
	for _, ln := range lines[1:] {
		if i := strings.Index(ln, ":"); i > 0 {
			k := strings.ToLower(strings.TrimSpace(ln[:i]))
			v := strings.TrimSpace(ln[i+1:])
			headers[k] = v
		}
	}
	if headers["sec-websocket-accept"] != expected {
		return 0, "", false
	}
	pop := parseCFRayPoP(headers["cf-ray"])
	return time.Since(start).Seconds(), pop, true
}

// parseCFRayPoP 从 cf-ray 头提取 PoP 代码。
// 形如 "7a1b2c3d4e5f6789-HKG" → "HKG"；解析失败返回空串。
func parseCFRayPoP(cfRay string) string {
	if cfRay == "" {
		return ""
	}
	i := strings.LastIndex(cfRay, "-")
	if i < 0 || i == len(cfRay)-1 {
		return ""
	}
	return strings.ToUpper(cfRay[i+1:])
}

// CandidateResult 单个 IP 的探活结果（协议 + 性能解耦）。
type CandidateResult struct {
	IP        string  `json:"ip"`
	Success   int     `json:"success"`  // 协议+性能总成功次数
	Attempts  int     `json:"attempts"` // 总探测次数
	Median    float64 `json:"median"`   // P50（秒），0 表示无成功样本
	P90       float64 `json:"p90"`      // P90（秒），0 表示无成功样本
	HasMedian bool    `json:"has_median"`
	PoP       string  `json:"pop"` // 主要 PoP（多数成功的那一个）
	ProtoOK   bool    `json:"proto_ok"` // 协议是否通过（TLS+WS 101）
}

// CheckCandidate 协议/性能解耦探测。
//
//   - 协议阶段：1 次 ProbeWS 判定「TLS + WS 101 是否可用」（确定性）
//   - 性能阶段：协议通过后，再做 perfRepeat 次测延迟（P50/P90）
//
// 协议失败的 IP 只花 1 次探测（不再重复 3 次浪费）。
// perfRepeat ≤ 0 时只做协议探测，不测延迟。
func CheckCandidate(ip, domain, wsPath string, perfRepeat int, stop *StopEvent) CandidateResult {
	// ── 阶段 1：协议探测（1 次定生死） ──
	attempts := 1
	if stop.IsSet() {
		return CandidateResult{IP: ip, Attempts: 0}
	}
	sec, pop, ok := ProbeWS(ip, domain, wsPath, 5*time.Second)
	if !ok {
		// 协议不过 → 直接返回，省下后续探测
		return CandidateResult{IP: ip, Success: 0, Attempts: attempts, ProtoOK: false}
	}
	// 协议通过
	latencies := []float64{sec}
	popCount := map[string]int{}
	if pop != "" {
		popCount[pop]++
	}

	// ── 阶段 2：性能探测（perfRepeat 次测延迟） ──
	for i := 0; i < perfRepeat; i++ {
		if stop.IsSet() {
			break
		}
		attempts++
		s2, p2, ok2 := ProbeWS(ip, domain, wsPath, 5*time.Second)
		if ok2 {
			latencies = append(latencies, s2)
			if p2 != "" {
				popCount[p2]++
			}
		}
	}

	res := CandidateResult{
		IP:       ip,
		Success:  len(latencies),
		Attempts: attempts,
		ProtoOK:  true,
	}
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		res.Median = math.Round(percentile(latencies, 0.50)*1000) / 1000
		res.P90 = math.Round(percentile(latencies, 0.90)*1000) / 1000
		res.HasMedian = true
	}
	// 主要 PoP（出现最多的那一个）
	best, bestN := "", 0
	for p, n := range popCount {
		if n > bestN {
			best, bestN = p, n
		}
	}
	res.PoP = best
	return res
}

// percentile 计算排序后切片的 p 分位数（p ∈ [0,1]）。
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := p * float64(len(sorted)-1)
	lo := int(idx)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}
