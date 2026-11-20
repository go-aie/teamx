//go:build darwin || linux

package room

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var serviceStableVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type serviceReleaseClient struct {
	http                  *http.Client
	apiRoot, downloadRoot string
}

func officialServiceReleases() serviceReleaseClient {
	return serviceReleaseClient{
		http: &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 || req.URL.Scheme != "https" {
				return errors.New("发行下载只允许 HTTPS，最多 10 次跳转")
			}
			return nil
		}},
		apiRoot:      "https://api.github.com/repos/go-aie/teamx/releases",
		downloadRoot: "https://github.com/go-aie/teamx/releases/download",
	}
}

func (c serviceReleaseClient) get(ctx context.Context, url string, out io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "teamx/"+distributionVersion)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("下载 %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s: HTTP %d", url, resp.StatusCode)
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errors.New("发行文件超过允许大小")
	}
	return nil
}

// Resolve latest exactly once. All subsequent downloads use this fixed tag.
func (c serviceReleaseClient) resolve(ctx context.Context, version string) (string, error) {
	endpoint := "/latest"
	if version != "" && version != "latest" {
		if !serviceStableVersion.MatchString(version) {
			return "", errors.New("--version 需要 latest 或正式版本号，例如 v0.0.3")
		}
		endpoint = "/tags/" + version
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var body strings.Builder
	if err := c.get(ctx, c.apiRoot+endpoint, &body, 8<<20); err != nil {
		return "", err
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal([]byte(body.String()), &release); err != nil {
		return "", fmt.Errorf("无法读取发行信息: %w", err)
	}
	if release.Draft || release.Prerelease || !serviceStableVersion.MatchString(release.Tag) || (endpoint != "/latest" && release.Tag != version) {
		return "", errors.New("发行信息不是匹配的正式稳定版本")
	}
	return release.Tag, nil
}

func serviceArchiveDigest(sums, name string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(sums))
	digest := ""
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || fields[1] != name {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if digest != "" || err != nil || len(decoded) != sha256.Size {
			return "", errors.New("发行摘要存在重复或无效记录")
		}
		digest = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if digest == "" {
		return "", fmt.Errorf("发行摘要缺少本机 standard 包：%s", name)
	}
	return digest, nil
}

func (c serviceReleaseClient) download(ctx context.Context, version, directory string) (archive, digest string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	name := "teamx-" + version + "-" + runtime.GOOS + "-" + runtime.GOARCH + "-standard.tar.gz"
	base := c.downloadRoot + "/" + version + "/"
	var sums strings.Builder
	if err := c.get(ctx, base+"SHA256SUMS", &sums, 8<<20); err != nil {
		return "", "", err
	}
	digest, err = serviceArchiveDigest(sums.String(), name)
	if err != nil {
		return "", "", err
	}
	archive = filepath.Join(directory, name)
	f, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", "", err
	}
	hash := sha256.New()
	err = c.get(ctx, base+name, io.MultiWriter(f, hash), 20<<30)
	if err := errors.Join(err, f.Sync(), f.Close()); err != nil {
		return "", "", err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return "", "", errors.New("归档 SHA-256 不一致")
	}
	return archive, digest, nil
}

func selectedServiceVersion(r serviceRegistration) (string, error) {
	release, err := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
	if err != nil {
		return "", err
	}
	if filepath.Dir(release) != filepath.Join(r.Root, "releases") {
		return "", errors.New("当前版本目录不属于此安装")
	}
	body, err := os.ReadFile(filepath.Join(release, "manifest.json"))
	if err != nil {
		return "", err
	}
	var manifest struct {
		Version string `json:"version"`
		Profile string `json:"profile"`
		Target  string `json:"target"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return "", err
	}
	if manifest.Version == "" || manifest.Profile != "standard" || manifest.Target != runtime.GOOS+"-"+runtime.GOARCH {
		return "", errors.New("当前版本清单与本机 standard 安装不符")
	}
	return manifest.Version, nil
}

// Compare stable versions without integer overflow or lexical 9 > 10 mistakes.
func compareServiceVersions(a, b string) (int, bool) {
	if !serviceStableVersion.MatchString(a) || !serviceStableVersion.MatchString(b) {
		return 0, false
	}
	aa, bb := strings.Split(a[1:], "."), strings.Split(b[1:], ".")
	for i := range aa {
		if len(aa[i]) < len(bb[i]) {
			return -1, true
		}
		if len(aa[i]) > len(bb[i]) {
			return 1, true
		}
		if n := strings.Compare(aa[i], bb[i]); n != 0 {
			return n, true
		}
	}
	return 0, true
}

func prepareServiceOnlineUpgrade(ctx context.Context, r serviceRegistration, version string, stderr io.Writer, client serviceReleaseClient) (string, error) {
	current, err := selectedServiceVersion(r)
	if err != nil {
		return "", err
	}
	fmt.Fprintln(stderr, "正在查询 TeamX 正式版本…")
	target, err := client.resolve(ctx, version)
	if err != nil {
		return "", err
	}
	if cmp, stable := compareServiceVersions(current, target); stable && cmp >= 0 {
		if cmp == 0 {
			fmt.Fprintf(stderr, "TeamX %s 已是目标版本，无需升级；未下载应用包或停止服务。\n", current)
			return "", nil
		}
		return "", fmt.Errorf("当前版本 %s 新于目标 %s，未执行降级；恢复旧数据请使用 service restore", current, target)
	}
	fmt.Fprintf(stderr, "TeamX %s → %s（%s-%s）。正在下载并校验…\n", current, target, runtime.GOOS, runtime.GOARCH)
	directory, err := os.MkdirTemp(r.Root, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(directory)
	archive, digest, err := client.download(ctx, target, directory)
	if err != nil {
		return "", err
	}
	next, err := publishServiceArchive(r.Root, archive, digest)
	if err != nil {
		return "", err
	}
	expected := target + "-" + runtime.GOOS + "-" + runtime.GOARCH + "-standard-" + digest[:12]
	if filepath.Base(next) != expected {
		return "", errors.New("归档清单版本与目标发行不符，尚未切换版本")
	}
	return next, nil
}
