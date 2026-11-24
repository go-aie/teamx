// Package docker manages one explicitly owned local Computer container. It has
// no knowledge of members, tools, execution settlement, or Agent scheduling.
package docker

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

//go:embed image.lock.json
var imageLock []byte

type Image struct {
	Reference string `json:"reference"`
	ID        string `json:"image_id"`
	ConfigID  string `json:"config_digest"`
	Platform  string `json:"platform"`
}

func DefaultImage() Image { var v Image; _ = json.Unmarshal(imageLock, &v); return v }

type Limits struct {
	CPUs      int `json:"cpus"`
	MemoryGiB int `json:"memory_gib"`
}

func (l Limits) Validate() error {
	if l.CPUs < 1 || l.CPUs > 64 || l.MemoryGiB < 2 || l.MemoryGiB > 256 {
		return errors.New("CPU 应为 1–64，内存应为 2–256 GiB。")
	}
	return nil
}

// State is a private durable receipt, never a client-supplied Docker spec.
// The caller must persist NewState BEFORE Step, then persist every Step result,
// including errors. Lost responses are reconciled through ownership labels.
type State struct {
	Owner          string `json:"owner"`
	Creation       string `json:"creation"`
	Name           string `json:"name"`
	Context        string `json:"context,omitempty"`
	Host           string `json:"host,omitempty"`
	Daemon         string `json:"daemon,omitempty"`
	Architecture   string `json:"architecture,omitempty"`
	Image          Image  `json:"image"`
	ImageReady     bool   `json:"image_ready"`
	ResolvedImage  string `json:"resolved_image_id,omitempty"`
	Limits         Limits `json:"limits"`
	KeyRef         string `json:"credential_ref"`
	StateVolume    bool   `json:"state_volume"`
	FilesVolume    bool   `json:"files_volume"`
	Container      string `json:"container_id,omitempty"`
	TokenInstalled bool   `json:"token_installed"`
	Endpoint       string `json:"endpoint,omitempty"`
	Instance       string `json:"instance_id,omitempty"`
	Stopped        bool   `json:"stopped"`
}

var hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var hex32 = regexp.MustCompile(`^[a-f0-9]{32}$`)

func NewState(owner, creation, key string, limits Limits) State {
	return State{Owner: owner, Creation: creation, Name: "teamx-computer-" + owner[:20], KeyRef: key, Image: DefaultImage(), Limits: limits}
}
func (s State) Validate() error {
	if !hex64.MatchString(s.Owner) || !hex32.MatchString(s.Creation) || s.Name != "teamx-computer-"+s.Owner[:20] || s.KeyRef == "" || s.Limits.Validate() != nil {
		return errors.New("托管电脑资源记录无效。")
	}
	if s.Image.Platform != "linux/amd64" || !strings.HasPrefix(s.Image.Reference, "ghcr.io/go-aie/teamx/computer:") || !strings.HasPrefix(s.Image.ID, "sha256:") || !hex64.MatchString(strings.TrimPrefix(s.Image.ID, "sha256:")) {
		return errors.New("托管电脑固定镜像记录无效。")
	}
	if s.Image.ConfigID != "" && (!strings.HasPrefix(s.Image.ConfigID, "sha256:") || !hex64.MatchString(strings.TrimPrefix(s.Image.ConfigID, "sha256:"))) {
		return errors.New("托管电脑镜像配置摘要无效。")
	}
	if s.Host != "" && !localHost(s.Host) {
		return errors.New("托管电脑只支持本机 Docker socket。")
	}
	if (s.Host == "") != (s.Daemon == "") || s.ImageReady && (s.Host == "" || !strings.HasPrefix(s.ResolvedImage, "sha256:") || !hex64.MatchString(strings.TrimPrefix(s.ResolvedImage, "sha256:"))) {
		return errors.New("托管电脑引擎或镜像记录不完整。")
	}
	if s.Container != "" && !hex64.MatchString(s.Container) {
		return errors.New("托管电脑容器身份无效。")
	}
	return nil
}

type Result struct {
	Out, Err []byte
	Code     int
}
type Runner func(context.Context, string, []string, io.Reader) (Result, error)
type Client struct{ Run Runner }

func New() *Client { return &Client{Run: runCLI} }

// Limit retained output even when docker pull emits unbounded progress. Never
// forward raw stderr: it can contain local paths or credential helper output.
type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 2<<20 {
		left := (2 << 20) - b.Len()
		_, _ = b.Buffer.Write(p[:min(len(p), left)])
	}
	return n, nil
}
func runCLI(ctx context.Context, host string, args []string, input io.Reader) (Result, error) {
	path, err := exec.LookPath("docker")
	if err != nil {
		return Result{}, errors.New("未找到 Docker CLI，请先安装 Docker。")
	}
	if host != "" {
		args = append([]string{"--host", host}, args...)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	// The released Computer image is public. Avoid a desktop credential helper
	// waiting for keychain access in a background TeamX service.
	publicPull := len(args) > 2 && args[0] == "--host" && args[2] == "pull"
	if publicPull {
		dir, err := os.MkdirTemp("", "teamx-computer-docker-")
		if err != nil {
			return Result{}, errors.New("无法准备 Docker 镜像客户端。")
		}
		defer os.RemoveAll(dir)
		if err = os.WriteFile(dir+"/config.json", []byte(`{"auths":{}}`), 0600); err != nil {
			return Result{}, errors.New("无法准备 Docker 镜像客户端。")
		}
		cmd.Env = append(cmd.Env, "DOCKER_CONFIG="+dir)
	}
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if k != "DOCKER_HOST" && k != "DOCKER_CONTEXT" && k != "DOCKER_TLS_VERIFY" && k != "DOCKER_CERT_PATH" && !(k == "DOCKER_CONFIG" && publicPull) {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Stdin = input
	var out, diagnostic boundedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	err = cmd.Run()
	result := Result{Out: out.Bytes(), Err: diagnostic.Bytes()}
	var status *exec.ExitError
	if errors.As(err, &status) {
		result.Code = status.ExitCode()
		return result, nil
	}
	return result, err
}
func localHost(host string) bool {
	u, err := url.Parse(host)
	return err == nil && u.Scheme == "unix" && u.Host == "" && strings.HasPrefix(u.Path, "/") && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}
func (c *Client) command(ctx context.Context, host string, args ...string) ([]byte, error) {
	r, err := c.Run(ctx, host, args, nil)
	if err != nil {
		return nil, err
	}
	if r.Code != 0 {
		return nil, errors.New("Docker 操作失败，请检查 daemon、服务账号权限及可用资源后重试。")
	}
	return r.Out, nil
}

type Environment struct {
	Context      string `json:"context"`
	Host         string `json:"-"`
	Daemon       string `json:"-"`
	Architecture string `json:"architecture"`
	Emulated     bool   `json:"emulated"`
}

func (c *Client) Check(ctx context.Context) (Environment, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return Environment{}, errors.New("本机托管仅支持 macOS 和 Linux 宿主。")
	}
	e := Environment{}
	if host := os.Getenv("DOCKER_HOST"); host != "" && os.Getenv("DOCKER_CONTEXT") == "" {
		e.Host = host
	} else {
		e.Context = os.Getenv("DOCKER_CONTEXT")
		if e.Context == "" {
			raw, err := c.command(ctx, "", "context", "show")
			if err != nil {
				return e, err
			}
			e.Context = strings.TrimSpace(string(raw))
		}
		raw, err := c.command(ctx, "", "context", "inspect", e.Context)
		if err != nil {
			return e, err
		}
		var contexts []struct {
			Endpoints map[string]struct{ Host string }
		}
		if json.Unmarshal(raw, &contexts) != nil || len(contexts) != 1 {
			return e, errors.New("无法解析 Docker context。")
		}
		e.Host = contexts[0].Endpoints["docker"].Host
	}
	if !localHost(e.Host) {
		return e, errors.New("本机托管只接受本机 Unix socket，不支持远端 Docker context。")
	}
	info, err := c.engine(ctx, e.Host, "")
	if err != nil {
		return e, err
	}
	e.Daemon = info.ID
	e.Architecture = info.Architecture
	e.Emulated = info.Architecture != "x86_64" && info.Architecture != "amd64"
	return e, nil
}

type engineInfo struct {
	ID, OSType, Architecture string
	NCPU                     int
	MemTotal                 int64
}

func (c *Client) engine(ctx context.Context, host, expected string) (engineInfo, error) {
	raw, err := c.command(ctx, host, "info", "--format", "{{json .}}")
	var info engineInfo
	if err != nil {
		return info, errors.New("无法连接本机 Docker，请启动 Docker 并检查 TeamX 服务账号权限。")
	}
	if json.Unmarshal(raw, &info) != nil || info.ID == "" || info.OSType != "linux" {
		return info, errors.New("需要可运行 Linux 容器的本机 Docker。")
	}
	if expected != "" && expected != info.ID {
		return info, errors.New("Docker daemon 身份已改变，保留原资源等待核对。")
	}
	return info, nil
}

// Step performs at most one durable preparation step. Image pulling can take
// minutes; the owner runs it asynchronously and can cancel it on disable.
func (c *Client) Step(ctx context.Context, s *State, key string) (bool, error) {
	if err := s.Validate(); err != nil {
		return false, err
	}
	if s.Host == "" {
		e, err := c.Check(ctx)
		if err != nil {
			return false, err
		}
		s.Context = e.Context
		s.Host = e.Host
		s.Daemon = e.Daemon
		s.Architecture = e.Architecture
		return false, nil
	}
	info, err := c.engine(ctx, s.Host, s.Daemon)
	if err != nil {
		return false, err
	}
	if info.NCPU < s.Limits.CPUs || info.MemTotal < int64(s.Limits.MemoryGiB)<<30 {
		return false, errors.New("Docker 可用 CPU 或内存低于电脑配置，请调整 Docker 资源或电脑设置。")
	}
	if !s.ImageReady {
		r, err := c.Run(ctx, s.Host, []string{"image", "inspect", s.Image.Reference}, nil)
		if err != nil {
			return false, err
		}
		if r.Code != 0 {
			if !strings.Contains(string(r.Err), "No such image") {
				return false, errors.New("无法检查托管电脑镜像。")
			}
			locked := strings.SplitN(s.Image.Reference, ":", 2)[0] + "@" + s.Image.ID
			r, err = c.Run(ctx, s.Host, []string{"image", "inspect", locked}, nil)
			if err != nil {
				return false, err
			}
			if r.Code != 0 {
				if _, err = c.command(ctx, s.Host, "pull", "--platform", s.Image.Platform, locked); err != nil {
					return false, errors.New("无法拉取固定电脑镜像，请检查网络、仓库访问权限与镜像发布状态。")
				}
			}
			r, err = c.Run(ctx, s.Host, []string{"image", "inspect", locked}, nil)
			if err != nil {
				return false, err
			}
		}
		var images []struct {
			ID, Os, Architecture string
			RepoDigests          []string
		}
		if r.Code != 0 || json.Unmarshal(r.Out, &images) != nil || len(images) != 1 || images[0].Os != "linux" || images[0].Architecture != "amd64" {
			return false, errors.New("电脑镜像身份或架构与固定发布清单不符。")
		}
		matched := images[0].ID == s.Image.ID || s.Image.ConfigID != "" && images[0].ID == s.Image.ConfigID
		for _, digest := range images[0].RepoDigests {
			if digest == strings.SplitN(s.Image.Reference, ":", 2)[0]+"@"+s.Image.ID {
				matched = true
			}
		}
		if !matched {
			return false, errors.New("电脑镜像摘要与固定发布清单不符。")
		}
		s.ResolvedImage = images[0].ID
		s.ImageReady = true
		return false, nil
	}
	for _, volume := range []struct {
		suffix string
		ready  *bool
	}{{"state", &s.StateVolume}, {"files", &s.FilesVolume}} {
		if err = c.volume(ctx, s, volume.suffix, *volume.ready); err != nil {
			return false, err
		}
		if !*volume.ready {
			*volume.ready = true
			return false, nil
		}
	}
	v, exists, err := c.inspect(ctx, s)
	if err != nil {
		return false, err
	}
	if !exists {
		if s.Container != "" {
			return false, errors.New("原托管容器已被删除，保留数据和执行记录等待核对。")
		}
		args := []string{"create", "--name", s.Name, "--init", "--platform", s.Image.Platform, "--restart", "no", "--cpus", strconv.Itoa(s.Limits.CPUs), "--memory", strconv.Itoa(s.Limits.MemoryGiB) + "g", "--shm-size", "1g", "--security-opt", "no-new-privileges", "--mount", "type=volume,src=" + s.Name + "-state,dst=/var/lib/teamx-computer", "--mount", "type=volume,src=" + s.Name + "-files,dst=/workspace", "--publish", "127.0.0.1::18090"}
		args = append(args, labelArgs(s)...)
		args = append(args, s.ResolvedImage, "-listen", "0.0.0.0:18090", "-token-file", "/run/computer-token", "-max-screens", "256")
		// Do not trust create stdout. Inspect is also the lost-receipt path.
		if _, err = c.command(ctx, s.Host, args...); err != nil {
			return false, err
		}
		v, exists, err = c.inspect(ctx, s)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, errors.New("尚未确认创建的电脑容器。")
		}
	}
	if s.Container == "" {
		s.Container = v.ID
		return false, nil
	}
	if !s.TokenInstalled {
		if v.State.Running {
			return false, errors.New("凭据注入前容器已运行，等待核对。")
		}
		var content bytes.Buffer
		tw := tar.NewWriter(&content)
		if err = tw.WriteHeader(&tar.Header{Name: "computer-token", Mode: 0600, Uid: 0, Gid: 0, Size: int64(len(key))}); err == nil {
			_, err = tw.Write([]byte(key))
		}
		if err == nil {
			err = tw.Close()
		}
		if err != nil {
			return false, err
		}
		r, err := c.Run(ctx, s.Host, []string{"cp", "-", s.Container + ":/run/"}, &content)
		if err != nil {
			return false, err
		}
		if r.Code != 0 {
			return false, errors.New("无法写入电脑私有管理凭据。")
		}
		s.TokenInstalled = true
		return false, nil
	}
	if !v.State.Running {
		if _, err = c.command(ctx, s.Host, "start", s.Container); err != nil {
			return false, errors.New("无法启动电脑容器，请检查镜像架构模拟支持、资源和 Docker 状态。")
		}
		s.Stopped = false
		return false, nil
	}
	ports := v.NetworkSettings.Ports["18090/tcp"]
	if len(ports) != 1 || ports[0].HostIP != "127.0.0.1" {
		return false, errors.New("电脑管理端口映射异常。")
	}
	port, err := strconv.Atoi(ports[0].HostPort)
	if err != nil || port < 1 || port > 65535 {
		return false, errors.New("电脑管理端口无效。")
	}
	s.Endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
	s.Stopped = false
	return true, nil
}

const ownerLabel = "io.teamx.computer.owner"
const creationLabel = "io.teamx.computer.creation"

func labelArgs(s *State) []string {
	return []string{"--label", ownerLabel + "=" + s.Owner, "--label", creationLabel + "=" + s.Creation}
}
func labelsMatch(s *State, labels map[string]string) bool {
	return labels[ownerLabel] == s.Owner && labels[creationLabel] == s.Creation
}
func (c *Client) volume(ctx context.Context, s *State, suffix string, recorded bool) error {
	name := s.Name + "-" + suffix
	r, err := c.Run(ctx, s.Host, []string{"volume", "inspect", name}, nil)
	if err != nil {
		return err
	}
	if r.Code != 0 {
		if !strings.Contains(string(r.Err), "no such volume") && !strings.Contains(string(r.Err), "No such volume") {
			return errors.New("无法检查电脑持久卷。")
		}
		if recorded {
			return errors.New("原电脑持久卷已丢失，禁止自动创建空数据替代。")
		}
		args := append([]string{"volume", "create"}, labelArgs(s)...)
		args = append(args, name)
		if _, err = c.command(ctx, s.Host, args...); err != nil {
			return err
		}
		r, err = c.Run(ctx, s.Host, []string{"volume", "inspect", name}, nil)
		if err != nil {
			return err
		}
	}
	var volumes []struct {
		Name, Driver string
		Labels       map[string]string
		Options      map[string]string
	}
	if r.Code != 0 || json.Unmarshal(r.Out, &volumes) != nil || len(volumes) != 1 || volumes[0].Name != name || volumes[0].Driver != "local" || len(volumes[0].Options) != 0 || !labelsMatch(s, volumes[0].Labels) {
		return errors.New("电脑持久卷归属或挂载参数不匹配。")
	}
	return nil
}

type containerInfo struct {
	ID, Image string
	State     struct {
		Running, Paused, Restarting bool
		Pid                         int
	}
	Config struct {
		Labels               map[string]string
		Entrypoint, Cmd, Env []string
	}
	HostConfig struct {
		Init                       *bool
		Privileged                 bool
		NetworkMode, PidMode       string
		RestartPolicy              struct{ Name string }
		SecurityOpt, Binds, CapAdd []string
		PortBindings               map[string][]struct{ HostIP, HostPort string }
		Memory, NanoCpus, ShmSize  int64
	}
	Mounts []struct {
		Type, Name, Destination string
		RW                      bool
	}
	NetworkSettings struct {
		Ports map[string][]struct{ HostIP, HostPort string }
	}
}

func (c *Client) inspect(ctx context.Context, s *State) (containerInfo, bool, error) {
	var v containerInfo
	r, err := c.Run(ctx, s.Host, []string{"container", "inspect", s.Name}, nil)
	if err != nil {
		return v, false, err
	}
	if r.Code != 0 {
		if strings.Contains(string(r.Err), "No such container") || strings.Contains(string(r.Err), "No such object") {
			return v, false, nil
		}
		return v, false, errors.New("无法检查电脑容器。")
	}
	var values []containerInfo
	if json.Unmarshal(r.Out, &values) != nil || len(values) != 1 {
		return v, false, errors.New("电脑容器检查结果无效。")
	}
	v = values[0]
	if !hex64.MatchString(v.ID) || s.Container != "" && v.ID != s.Container || v.Image != s.ResolvedImage || !labelsMatch(s, v.Config.Labels) || v.HostConfig.Init == nil || !*v.HostConfig.Init || v.HostConfig.Privileged || v.HostConfig.RestartPolicy.Name != "no" || v.State.Paused || v.State.Restarting || len(v.HostConfig.Binds) != 0 || len(v.HostConfig.CapAdd) != 0 || v.HostConfig.Memory != int64(s.Limits.MemoryGiB)<<30 || v.HostConfig.NanoCpus != int64(s.Limits.CPUs)*1e9 || v.HostConfig.ShmSize != 1<<30 {
		return v, false, errors.New("电脑容器身份或部署参数已改变，禁止自动操作。")
	}
	if len(v.Config.Entrypoint) != 1 || v.Config.Entrypoint[0] != "/usr/local/bin/teamx-computer" || strings.Join(v.Config.Cmd, "\x00") != "-listen\x000.0.0.0:18090\x00-token-file\x00/run/computer-token\x00-max-screens\x00256" {
		return v, false, errors.New("电脑容器启动合同已改变。")
	}
	secure := false
	for _, o := range v.HostConfig.SecurityOpt {
		if o == "no-new-privileges" || o == "no-new-privileges=true" {
			secure = true
		}
	}
	if !secure || len(v.HostConfig.SecurityOpt) != 1 || len(v.Mounts) != 2 || v.HostConfig.PidMode != "" || v.HostConfig.NetworkMode != "default" && v.HostConfig.NetworkMode != "bridge" {
		return v, false, errors.New("电脑容器安全或持久挂载配置异常。")
	}
	expected := map[string]string{"/var/lib/teamx-computer": s.Name + "-state", "/workspace": s.Name + "-files"}
	for _, m := range v.Mounts {
		if m.Type != "volume" || !m.RW || expected[m.Destination] != m.Name {
			return v, false, errors.New("电脑容器持久卷不匹配。")
		}
		delete(expected, m.Destination)
	}
	if len(expected) != 0 {
		return v, false, errors.New("电脑容器缺少持久卷。")
	}
	p := v.HostConfig.PortBindings["18090/tcp"]
	if len(v.HostConfig.PortBindings) != 1 || len(p) != 1 || p[0].HostIP != "127.0.0.1" {
		return v, false, errors.New("电脑容器端口配置异常。")
	}
	return v, true, nil
}

// Stop must ONLY be called after the owner's screen/input/Session settlement
// gate has completed. Docker process exit is not a business action receipt.
func (c *Client) Stop(ctx context.Context, s *State) error {
	if s.Host == "" {
		s.Stopped = true
		return nil
	}
	if _, err := c.engine(ctx, s.Host, s.Daemon); err != nil {
		return err
	}
	for _, v := range []struct {
		suffix string
		ready  bool
	}{{"state", s.StateVolume}, {"files", s.FilesVolume}} {
		if v.ready {
			if err := c.volume(ctx, s, v.suffix, true); err != nil {
				return err
			}
		}
	}
	v, exists, err := c.inspect(ctx, s)
	if err != nil {
		return err
	}
	if !exists {
		if s.Container != "" {
			return errors.New("原电脑容器已丢失，停止状态待核对。")
		}
		s.Stopped = true
		return nil
	}
	if s.Container == "" {
		s.Container = v.ID
	}
	if v.State.Running {
		if _, err = c.command(ctx, s.Host, "stop", "--timeout", "45", s.Container); err != nil {
			return err
		}
	}
	v, exists, err = c.inspect(ctx, s)
	if err != nil {
		return err
	}
	if !exists || v.State.Running || v.State.Pid != 0 {
		return errors.New("尚未确认电脑容器实际停止。")
	}
	s.Stopped = true
	return nil
}
