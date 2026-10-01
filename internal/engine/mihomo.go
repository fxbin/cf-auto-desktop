package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MihomoClient 查询 Clash/Mihomo external-controller 的被动延迟数据。
//
// 设计目标：把 Mihomo `url-test` / `fallback` 持续观测到的节点延迟
// 回传给 CF Auto Desktop，作为主动探测之外的被动信号。
//
// 硬边界：
//   - 只读（GET /proxies、GET /proxies/{name}），不改 Mihomo 状态
//   - 失败静默返回空 map（Mihomo 未运行/未开 API 不阻断扫描）
//   - 只信任本地回环地址（127.0.0.1 / ::1）
type MihomoClient struct {
	BaseURL string // 如 http://127.0.0.1:9090
	Secret  string // 可选；Bearer token
	Timeout time.Duration
}

// MihomoDelay 单节点的被动延迟观测。
type MihomoDelay struct {
	Name      string    `json:"name"`
	DelayMs   int       `json:"delay_ms"` // 最近一次 url-test 延迟（0 = 未测/失败）
	SampleN   int       `json:"sample_n"` // history 样本数
	LastSeen  time.Time `json:"last_seen"`
	Available bool      `json:"available"`
}

// NewMihomoClient 默认连 http://127.0.0.1:9090（Mihomo external-controller 默认端口）。
func NewMihomoClient() *MihomoClient {
	return &MihomoClient{
		BaseURL: "http://127.0.0.1:9090",
		Timeout: 2 * time.Second,
	}
}

// FetchDelays 拉取所有代理的被动延迟。Mihomo 不可用时返回空 map + 无错误。
func (c *MihomoClient) FetchDelays() map[string]MihomoDelay {
	out := map[string]MihomoDelay{}
	if c == nil || c.BaseURL == "" {
		return out
	}
	// 只信任回环
	if !strings.Contains(c.BaseURL, "127.0.0.1") && !strings.Contains(c.BaseURL, "::1") {
		return out
	}
	url := strings.TrimRight(c.BaseURL, "/") + "/proxies"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return out
	}
	if c.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	client := &http.Client{Timeout: c.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return out // Mihomo 未运行
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out
	}

	var payload struct {
		Proxies map[string]struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			History []struct {
				Time  string `json:"time"`
				Delay int    `json:"delay"`
			} `json:"history"`
		} `json:"proxies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return out
	}

	for name, p := range payload.Proxies {
		// 只关心我们的动态候选（CF-DYN-*）与原节点
		if !strings.HasPrefix(name, "CF-DYN-") {
			continue
		}
		d := MihomoDelay{Name: name, Available: true}
		if len(p.History) > 0 {
			last := p.History[len(p.History)-1]
			d.DelayMs = last.Delay
			d.SampleN = len(p.History)
			if t, err := time.Parse(time.RFC3339, last.Time); err == nil {
				d.LastSeen = t
			}
		}
		out[name] = d
	}
	return out
}

// BoostScore 把 Mihomo 被动延迟折算成主动探测的加权分。
// 返回 [0,1] 的权重：延迟越低权重越高；无被动数据时返回中性 0.5。
// 这样主动探测快但 Mihomo 观测慢的 IP 会被降权，反之升权。
func BoostScore(delayMs int, sampleN int) float64 {
	if sampleN == 0 || delayMs <= 0 {
		return 0.5 // 中性
	}
	// 延迟映射：≤50ms → 1.0，500ms → 0.1，>500ms → 0.05
	switch {
	case delayMs <= 50:
		return 1.0
	case delayMs <= 100:
		return 0.9
	case delayMs <= 200:
		return 0.7
	case delayMs <= 350:
		return 0.5
	case delayMs <= 500:
		return 0.3
	default:
		return 0.1
	}
}

// FetchControllerConfig 从 clash-auto.yaml 提取 external-controller 与 secret。
// 只做纯文本行扫描（避免引 YAML 依赖）。
func FetchControllerConfig(clashYAML []byte) (baseURL, secret string) {
	lines := strings.Split(string(clashYAML), "\n")
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "external-controller:") {
			v := strings.TrimSpace(strings.TrimPrefix(t, "external-controller:"))
			v = strings.Trim(v, `"'`)
			if v != "" && !strings.HasPrefix(v, "http") {
				v = "http://" + v
			}
			baseURL = v
		} else if strings.HasPrefix(t, "secret:") {
			v := strings.TrimSpace(strings.TrimPrefix(t, "secret:"))
			v = strings.Trim(v, `"'`)
			secret = v
		}
	}
	return baseURL, secret
}

// FormatDelayMs 人类可读延迟。
func FormatDelayMs(ms int) string {
	if ms <= 0 {
		return "-"
	}
	if ms >= 1000 {
		return fmt.Sprintf("%.2fs", float64(ms)/1000)
	}
	return strconv.Itoa(ms) + "ms"
}
