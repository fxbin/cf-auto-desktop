package engine

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Prefs 用户偏好 / 状态（对应 Python prefs.json）。
type Prefs struct {
	Cfst        string  `json:"cfst"`
	EveryHours  int     `json:"every_hours"`
	AutoScan    bool    `json:"auto_scan"`
	ScanMode    string  `json:"scan_mode"`
	LastAttempt float64 `json:"last_attempt"`
	LastSuccess float64 `json:"last_success"`
	LastStatus  string  `json:"last_status"`
}

// State 生成配置时保存的凭据（对应 Python state.json）。
// 含真实 UUID、SNI 域名、WebSocket 路径、Provider 随机 token。
type State struct {
	Node     map[string]any `json:"node"`
	Domain   string         `json:"domain"`
	Path     string         `json:"path"`
	Token    string         `json:"token"`
	NodeName string         `json:"node_name"`
}

// AtomicWrite 原子写文本，权限 0600（对应 Python atomic_write）。
func AtomicWrite(path, text string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".stage-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op after rename
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.WriteString(text); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ReadPrefs 读取用户偏好；缺文件返回默认值。
func ReadPrefs(workdir string) (Prefs, error) {
	p := Prefs{
		Cfst:       "",
		EveryHours: 6,
		AutoScan:   true,
		ScanMode:   "standard",
		LastStatus: "尚未扫描",
	}
	data, err := os.ReadFile(filepath.Join(workdir, "prefs.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	// 补齐默认值（防止旧 prefs.json 缺字段）
	if p.EveryHours == 0 {
		p.EveryHours = 6
	}
	if p.ScanMode == "" {
		p.ScanMode = "standard"
	}
	return p, nil
}

// UpdatePrefs 局部更新偏好（未传字段保留原值）。
func UpdatePrefs(workdir string, mutate func(*Prefs)) error {
	p, err := ReadPrefs(workdir)
	if err != nil {
		return err
	}
	mutate(&p)
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(workdir, "prefs.json"), string(data))
}

// LoadState 读取 state.json。required=false 时缺失返回 nil 而非报错。
func LoadState(workdir string, required bool) (*State, error) {
	path := filepath.Join(workdir, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if required {
				return nil, fmt.Errorf("请先导入原始 YAML")
			}
			return nil, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SaveState 写 state.json（0600）。
func SaveState(workdir string, s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(workdir, "state.json"), string(data))
}

// NewToken 生成 URL-safe 随机 token（24 字节 → 32 字符）。
func NewToken() (string, error) {
	buf := make([]byte, TokenSize)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
