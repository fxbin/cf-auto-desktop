package engine

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CfstAssetName 按当前 CPU 架构返回官方 Release 资产名。
func CfstAssetName() (string, error) {
	switch runtime.GOARCH {
	case "arm64":
		return "cfst_darwin_arm64.zip", nil
	case "amd64":
		return "cfst_darwin_amd64.zip", nil
	default:
		return "", fmt.Errorf("未支持的 CPU 架构：%s/%s（请手动导入 cfst）", runtime.GOOS, runtime.GOARCH)
	}
}

// AssertOfficialURL 严格 https + 域名白名单。
func AssertOfficialURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("URL 解析失败")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("只允许 https 下载")
	}
	host := strings.ToLower(u.Hostname())
	if !CfstAllowedHosts[host] {
		return fmt.Errorf("非官方下载源：%s", host)
	}
	return nil
}

// SHA256File 计算文件 SHA256（hex）。
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fetchJSON 拉取 GitHub API 元数据。
func fetchJSON(raw string, out any) error {
	if err := AssertOfficialURL(raw); err != nil {
		return err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest("GET", raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", CfstExpectedUA)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub API 状态 %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// downloadTo 流式下载到 dest。
func downloadTo(raw, dest string, progress func(done, total int64)) error {
	if err := AssertOfficialURL(raw); err != nil {
		return err
	}
	client := &http.Client{Timeout: 180 * time.Second}
	req, err := http.NewRequest("GET", raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", CfstExpectedUA)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载状态 %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 128*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if done == 0 {
		return fmt.Errorf("下载内容为空")
	}
	return nil
}

// ghAsset GitHub Release 资产元数据。
type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

type ghRelease struct {
	TagName string     `json:"tag_name"`
	Assets  []ghAsset  `json:"assets"`
}

// DownloadCfst 从 GitHub 官方 Releases 下载 cfst + ip.txt 并安装。
//
// 硬边界（与 Python 版一致）：
//   - 仅 https + CfstAllowedHostS
//   - 必须拿到 assets[].digest（sha256），无则 fail closed
//   - SHA256 校验失败 → 中止，工作区零写入
//   - 只解压 cfst / ip.txt，拒绝 zip-slip / 绝对路径 / 盘符开头
//   - 从不执行 cfst
func DownloadCfst(workdir string, log func(string), progress func(done, total int64), stop *StopEvent) (string, error) {
	if log == nil {
		log = func(string) {}
	}
	if stop == nil {
		stop = NewStopEvent()
	}

	want, err := CfstAssetName()
	if err != nil {
		return "", err
	}
	log("查询官方 Releases：" + CfstRepoOwner + "/" + CfstRepoName + " …")
	var meta ghRelease
	if err := fetchJSON(CfstReleaseAPI, &meta); err != nil {
		return "", err
	}
	var asset *ghAsset
	for i := range meta.Assets {
		if meta.Assets[i].Name == want {
			asset = &meta.Assets[i]
			break
		}
	}
	if asset == nil {
		return "", fmt.Errorf("官方 Releases 缺少 %s（当前 tag=%s）", want, meta.TagName)
	}
	if !strings.HasPrefix(asset.BrowserDownloadURL, "https://") {
		return "", fmt.Errorf("Release 资产缺少 https 下载地址")
	}
	if !strings.HasPrefix(asset.Digest, "sha256:") {
		return "", fmt.Errorf("官方 Release 缺少 sha256 digest，拒绝下载（fail closed）")
	}
	expected := strings.ToLower(strings.TrimPrefix(asset.Digest, "sha256:"))
	if len(expected) != 64 || !isHex(expected) {
		return "", fmt.Errorf("digest 格式非法")
	}
	log(fmt.Sprintf("官方版本 %s · 资产 %s · 大小 %.1f MB",
		meta.TagName, want, float64(asset.Size)/1e6))

	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return "", err
	}
	tmpDir, err := os.MkdirTemp(workdir, "cfst-dl-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, want)
	log("开始下载（未使用代理环境变量；走 HTTPS + SHA256 校验）…")
	if err := downloadTo(asset.BrowserDownloadURL, zipPath, progress); err != nil {
		return "", err
	}
	if stop.IsSet() {
		return "", fmt.Errorf("已取消下载")
	}

	actual, err := SHA256File(zipPath)
	if err != nil {
		return "", err
	}
	if actual != expected {
		return "", fmt.Errorf("SHA256 校验失败\n  期望 %s\n  实际 %s\n已中止，未写入工作区。", expected, actual)
	}
	log("SHA256 校验通过：" + actual[:16] + "…")

	// 只解压白名单
	allow := map[string]bool{"cfst": true, "CloudflareSpeedTest": true, "ip.txt": true}
	extracted := map[string]string{}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, f := range zr.File {
		// zip-slip / 绝对路径 / 盘符 检查（显式拒绝）
		name := f.Name
		if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") ||
			strings.Contains(name, ":") || hasDotDot(name) {
			return "", fmt.Errorf("压缩包条目路径非法：%s", name)
		}
		base := filepath.Base(filepath.FromSlash(name))
		if !allow[base] {
			continue
		}
		dst := filepath.Join(tmpDir, base)
		if err := extractZipEntry(f, dst); err != nil {
			return "", err
		}
		extracted[base] = dst
	}

	binPath := extracted["cfst"]
	if binPath == "" {
		binPath = extracted["CloudflareSpeedTest"]
	}
	if binPath == "" {
		return "", fmt.Errorf("压缩包缺少 cfst 可执行文件")
	}
	ipPath := extracted["ip.txt"]
	if ipPath == "" {
		return "", fmt.Errorf("压缩包缺少有效的 ip.txt")
	}
	if fi, err := os.Stat(ipPath); err != nil || fi.Size() == 0 {
		return "", fmt.Errorf("压缩包缺少有效的 ip.txt")
	}

	targetDir := filepath.Join(workdir, "cfst-bundle")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(targetDir, "cfst")
	if err := copyFile(binPath, dest); err != nil {
		return "", err
	}
	if err := copyFile(ipPath, filepath.Join(targetDir, "ip.txt")); err != nil {
		return "", err
	}
	if err := os.Chmod(dest, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Join(targetDir, "ip.txt"), 0o600); err != nil {
		return "", err
	}

	if err := UpdatePrefs(workdir, func(p *Prefs) { p.Cfst = dest }); err != nil {
		return "", err
	}
	log("已安装到：" + dest)
	return dest, nil
}

// ImportCfst 从本地目录导入（手动路径）。
func ImportCfst(workdir, chosen string) (string, error) {
	abs, err := filepath.Abs(chosen)
	if err != nil {
		return "", err
	}
	base := filepath.Base(abs)
	if base != "cfst" && base != "CloudflareSpeedTest" {
		return "", fmt.Errorf("请选择可信来源下载的 cfst 可执行文件")
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() {
		return "", fmt.Errorf("请选择可信来源下载的 cfst 可执行文件")
	}
	ipFile := filepath.Join(filepath.Dir(abs), "ip.txt")
	if fi, err := os.Stat(ipFile); err != nil || fi.Size() == 0 {
		return "", fmt.Errorf("cfst 所在目录必须同时包含有效的 ip.txt 文件")
	}

	targetDir := filepath.Join(workdir, "cfst-bundle")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(targetDir, "cfst")
	if abs != dest {
		if err := copyFile(abs, dest); err != nil {
			return "", err
		}
	}
	if err := copyFile(ipFile, filepath.Join(targetDir, "ip.txt")); err != nil {
		return "", err
	}
	if err := os.Chmod(dest, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Join(targetDir, "ip.txt"), 0o600); err != nil {
		return "", err
	}
	if err := UpdatePrefs(workdir, func(p *Prefs) { p.Cfst = dest }); err != nil {
		return "", err
	}
	return dest, nil
}

// helpers

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func hasDotDot(p string) bool {
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func extractZipEntry(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// randRead 抽象以方便 test 注入；默认用 crypto/rand。
var randRead = func(b []byte) (int, error) {
	return io.ReadFull(cryptoRandReader{}, b)
}

type cryptoRandReader struct{}

func (cryptoRandReader) Read(p []byte) (int, error) {
	return crandRead(p)
}
