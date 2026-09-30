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

// CandidateResult 单个 IP 的重复探活结果。
type CandidateResult struct {
	IP        string  `json:"ip"`
	Success   int     `json:"success"`
	Attempts  int     `json:"attempts"`
	Median    float64 `json:"median"` // 0 表示无成功样本
	HasMedian bool    `json:"has_median"`
	PoP       string  `json:"pop"` // 主要 PoP（多数成功的那一个）
}

// CheckCandidate 重复探活 repeat 次，聚合中位数 + PoP。
func CheckCandidate(ip, domain, wsPath string, repeat int, stop *StopEvent) CandidateResult {
	var ok []float64
	popCount := map[string]int{}
	attempts := 0
	for i := 0; i < repeat; i++ {
		if stop.IsSet() {
			break
		}
		attempts++
		if sec, pop, okv := ProbeWS(ip, domain, wsPath, 5*time.Second); okv {
			ok = append(ok, sec)
			if pop != "" {
				popCount[pop]++
			}
		}
	}
	res := CandidateResult{IP: ip, Success: len(ok), Attempts: attempts}
	if len(ok) > 0 {
		sort.Float64s(ok)
		med := ok[len(ok)/2]
		if len(ok)%2 == 0 {
			med = (ok[len(ok)/2-1] + ok[len(ok)/2]) / 2
		}
		res.Median = math.Round(med*1000) / 1000
		res.HasMedian = true
	}
	// 主要 PoP（出现最多的那一个）
	best, bestN := "", 0
	for pop, n := range popCount {
		if n > bestN {
			best, bestN = pop, n
		}
	}
	res.PoP = best
	return res
}
