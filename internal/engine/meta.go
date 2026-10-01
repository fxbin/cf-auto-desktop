package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CandidateMeta 追踪候选 IP 的时效性元数据（TTL / 时间衰减用）。
//
// 文件：workdir/candidate-meta.json
// 结构：{ "<ip>": {"last_validated": <unix>, "last_mihomo_ms": <int>, "fail_count": <int>} }
//
// 用途：
//   - 选取时优先「最近验证过」的 IP（时间衰减）
//   - 超过 MetaTTL 的 IP 自动降权（Anycast 会重路由，旧观测过期）
//   - 健康探针持续维护 last_validated
type CandidateMetaEntry struct {
	LastValidated  int64 `json:"last_validated"`   // Unix 秒
	LastMihomoMs   int   `json:"last_mihomo_ms"`   // Mihomo 被动延迟（0=无）
	FailCount      int   `json:"fail_count"`       // 连续失败次数
	LastProbeOK    bool  `json:"last_probe_ok"`    // 最近一次主动探测是否成功
}

// CandidateMetaMap IP → 元数据。
type CandidateMetaMap map[string]CandidateMetaEntry

// MetaTTL 未验证 IP 的过期时长。超过此值，选取时降权到 0.3 倍。
const MetaTTL = 24 * time.Hour

// MetaPath 返回 candidate-meta.json 路径。
func MetaPath(workdir string) string {
	return filepath.Join(workdir, "candidate-meta.json")
}

// LoadCandidateMeta 读取元数据；文件缺失返回空 map。
func LoadCandidateMeta(workdir string) CandidateMetaMap {
	data, err := os.ReadFile(MetaPath(workdir))
	if err != nil {
		return CandidateMetaMap{}
	}
	var m CandidateMetaMap
	if err := json.Unmarshal(data, &m); err != nil {
		return CandidateMetaMap{}
	}
	if m == nil {
		m = CandidateMetaMap{}
	}
	return m
}

// SaveCandidateMeta 原子写元数据（0600）。
func SaveCandidateMeta(workdir string, m CandidateMetaMap) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(MetaPath(workdir), string(data))
}

// UpdateMetaFromProbe 把主动探测结果写入元数据。
// ok=true 时更新 last_validated；ok=false 时累加 fail_count。
func UpdateMetaFromProbe(workdir string, ip string, ok bool, mihomoMs int) {
	m := LoadCandidateMeta(workdir)
	e := m[ip]
	e.LastProbeOK = ok
	if mihomoMs > 0 {
		e.LastMihomoMs = mihomoMs
	}
	if ok {
		e.LastValidated = time.Now().Unix()
		e.FailCount = 0
	} else {
		e.FailCount++
	}
	m[ip] = e
	_ = SaveCandidateMeta(workdir, m)
}

// TimeDecayFactor 计算时间衰减系数。
//
// 返回 [0.3, 1.0]：
//   - 刚验证（age=0）→ 1.0
//   - 6h 未验证 → 0.7
//   - 12h → 0.5
//   - 24h+ → 0.3（下限，不归零——留机会给下次扫描重激活）
func TimeDecayFactor(lastValidated int64) float64 {
	if lastValidated == 0 {
		return 0.3 // 从未验证
	}
	age := time.Since(time.Unix(lastValidated, 0))
	switch {
	case age <= 1*time.Hour:
		return 1.0
	case age <= 6*time.Hour:
		return 0.85
	case age <= 12*time.Hour:
		return 0.7
	case age <= 24*time.Hour:
		return 0.5
	default:
		return 0.3
	}
}

// EffectiveScore 把 boost（Mihomo 被动）与 decay（时间衰减）合成最终排序分。
// 都是 [0,1] 权重，几何平均比算术平均更保守。
func EffectiveScore(boost, decay float64) float64 {
	if boost <= 0 {
		boost = 0.5
	}
	if decay <= 0 {
		decay = 0.3
	}
	// 几何平均：sqrt(boost * decay)
	return sqrtApprox(boost * decay)
}

// sqrtApprox 简单平方根（Newton 2 次迭代，误差 <1e-3，够用）。
func sqrtApprox(x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x == 1 {
		return 1
	}
	g := x
	for i := 0; i < 2; i++ {
		g = (g + x/g) / 2
	}
	return g
}

// FormatMetaAge 人类可读时长。
func FormatMetaAge(lastValidated int64) string {
	if lastValidated == 0 {
		return "从未验证"
	}
	age := time.Since(time.Unix(lastValidated, 0))
	switch {
	case age < time.Minute:
		return "刚刚"
	case age < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(age.Hours()))
	default:
		return fmt.Sprintf("%d 天前", int(age.Hours()/24))
	}
}
