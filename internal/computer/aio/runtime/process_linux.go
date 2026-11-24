//go:build linux

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-aie/teamx/internal/computer"
)

type ProcessDriver struct {
	root, run string
	contract  computer.Contract
	policy    Policy
	mu        sync.Mutex
	clients   map[string]*nativeClient
}

func NewProcessDriver(root, run string, c computer.Contract, p Policy) (*ProcessDriver, error) {
	if os.Getpid() == 1 {
		return nil, fmt.Errorf("run the optional container with --init so orphan screen descendants are reaped")
	}
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("AIO Runtime requires container root to manage dedicated screen users")
	}
	for _, path := range []string{root, filepath.Join(root, "profiles"), run, "/workspace/agents"} {
		if err := os.MkdirAll(path, 0711); err != nil {
			return nil, err
		}
		if err := os.Chmod(path, 0711); err != nil {
			return nil, err
		}
	}
	for _, path := range []string{"/tmp/.X11-unix", "/tmp/.ICE-unix"} {
		if err := os.MkdirAll(path, 01777); err != nil {
			return nil, err
		}
		if err := os.Chmod(path, os.ModeSticky|0777); err != nil {
			return nil, err
		}
	}
	if _, err := user.LookupGroup("screens"); err != nil {
		if out, err := exec.Command("groupadd", "-g", "22000", "screens").CombinedOutput(); err != nil {
			return nil, fmt.Errorf("create screen group: %w: %s", err, out)
		}
	}
	if err := os.MkdirAll("/workspace/shared", 02770); err != nil {
		return nil, err
	}
	if err := os.Chown("/workspace/shared", 0, 22000); err != nil {
		return nil, err
	}
	if err := os.Chmod("/workspace/shared", os.ModeSetgid|0770); err != nil {
		return nil, err
	}
	return &ProcessDriver{root: root, run: run, contract: c.Clone(), policy: p, clients: map[string]*nativeClient{}}, nil
}
func (p *ProcessDriver) Validate(tool string, args json.RawMessage) error {
	return p.policy.Validate(tool, args)
}
func uid(r Resource) int        { return 23000 + r.Index }
func display(r Resource) string { return ":" + strconv.Itoa(100+r.Index) }
func ports(r Resource) []int {
	base := 20000 + r.Index*8
	return []int{base, base + 1, base + 2, base + 3, base + 4}
}                                                  // VNC, CDP, worker, browser MCP, aiod
func (p *ProcessDriver) home(r Resource) string    { return filepath.Join(p.root, "profiles", r.ID) }
func (p *ProcessDriver) runtime(r Resource) string { return filepath.Join(p.run, r.ID) }
func owned(path string, owner int, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("screen directory is not a directory")
	}
	if err = os.Chown(path, owner, 22000); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}
func processes(r Resource) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var found []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "Uid:") {
				for _, field := range strings.Fields(line)[1:] {
					if field == strconv.Itoa(uid(r)) {
						found = append(found, pid)
						break
					}
				}
				break
			}
		}
	}
	return found, nil
}
func (p *ProcessDriver) Start(ctx context.Context, r Resource) error {
	procs, err := processes(r)
	if err != nil {
		return err
	}
	if len(procs) > 0 {
		return fmt.Errorf("screen UID still has processes")
	}
	name := "tx-screen-" + strconv.Itoa(r.Index)
	home := p.home(r)
	account, err := user.Lookup(name)
	if err != nil {
		output, createErr := exec.CommandContext(ctx, "useradd", "-u", strconv.Itoa(uid(r)), "-g", "screens", "-d", home, "-s", "/bin/bash", name).CombinedOutput()
		if createErr != nil {
			return fmt.Errorf("create screen account: %w: %s", createErr, output)
		}
	} else if account.Uid != strconv.Itoa(uid(r)) || account.HomeDir != home || account.Gid != "22000" {
		return fmt.Errorf("screen Unix identity mismatch")
	}
	// Clear only this verified stopped UID's stale display/ICE/profile locks.
	entries, _ := os.ReadDir("/tmp/.ICE-unix")
	for _, entry := range entries {
		path := filepath.Join("/tmp/.ICE-unix", entry.Name())
		if st, err := os.Lstat(path); err == nil && int(st.Sys().(*syscall.Stat_t).Uid) == uid(r) {
			if err = os.Remove(path); err != nil {
				return err
			}
		}
	}
	homeDirs := []string{home, filepath.Join(home, "profile"), filepath.Join(home, ".config"), filepath.Join(home, ".cache"), p.runtime(r), filepath.Join("/workspace/agents", r.ID), filepath.Join("/workspace/agents", r.ID, "downloads")}
	for _, path := range homeDirs {
		if err = owned(path, uid(r), 0700); err != nil {
			return err
		}
	}
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		if err = os.Remove(filepath.Join(home, "profile", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	prefs := filepath.Join(home, "profile/Default/Preferences")
	if _, err = os.Stat(prefs); errors.Is(err, os.ErrNotExist) {
		if err = owned(filepath.Dir(prefs), uid(r), 0700); err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]any{"download": map[string]any{"default_directory": filepath.Join("/workspace/agents", r.ID, "downloads"), "prompt_for_download": false}, "profile": map[string]any{"default_content_setting_values": map[string]int{"cookies": 1}}})
		if err = os.WriteFile(prefs, raw, 0600); err != nil {
			return err
		}
		if err = os.Chown(prefs, uid(r), 22000); err != nil {
			return err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command("dbus-run-session", "--", exe, "session", r.ID, strconv.Itoa(r.Index), strconv.FormatUint(r.Epoch, 10), p.root, p.run)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USER=" + name, "LOGNAME=" + name, "DISPLAY=" + display(r), "XAUTHORITY=" + filepath.Join(p.runtime(r), "Xauthority"), "XDG_RUNTIME_DIR=" + p.runtime(r), "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "LANG=C.UTF-8"}
	cmd.Dir = home
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid(r)), Gid: 22000, Groups: []uint32{}}, Setsid: true}
	log, err := os.OpenFile(filepath.Join(p.runtime(r), "session.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	cmd.Stdout = log
	cmd.Stderr = log
	err = cmd.Start()
	_ = log.Close()
	if err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	// Native tools/list alone does not prove Chromium or the GUI worker is ready.
	readiness := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, endpoint := range []string{fmt.Sprintf("http://127.0.0.1:%d/json/version", ports(r)[1]), fmt.Sprintf("http://127.0.0.1:%d/openapi.json", ports(r)[2])} {
		for {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if err != nil {
				return err
			}
			response, err := readiness.Do(request)
			ready := false
			if err == nil {
				_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
				_ = response.Body.Close()
				ready = response.StatusCode == 200 && readErr == nil
			}
			if ready {
				break
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("screen browser/worker readiness: %w", ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	client := &nativeClient{url: fmt.Sprintf("http://127.0.0.1:%d/mcp", ports(r)[4]), http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = client.initialize(probeCtx)
		cancel()
		if err == nil {
			p.mu.Lock()
			p.clients[r.ID] = client
			p.mu.Unlock()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("screen startup: %w", ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
}
func (p *ProcessDriver) client(r Resource) (*nativeClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.clients[r.ID]
	if c == nil {
		return nil, fmt.Errorf("screen native channel unavailable")
	}
	return c, nil
}
func (p *ProcessDriver) Tools(ctx context.Context, r Resource) (computer.Contract, error) {
	c, err := p.client(r)
	if err != nil {
		return computer.Contract{}, err
	}
	return awaitNativeCatalog(ctx, c, p.contract)
}

func (p *ProcessDriver) Call(ctx context.Context, r Resource, name string, args json.RawMessage) (json.RawMessage, error) {
	c, err := p.client(r)
	if err != nil {
		return nil, err
	}
	return c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
}
func (p *ProcessDriver) Stop(ctx context.Context, r Resource) error {
	// Give Chromium a chance to flush persistent cookies before closing D-Bus/X11.
	before, err := processes(r)
	if err != nil {
		return err
	}
	if len(before) > 0 {
		_ = closeBrowser(ctx, ports(r)[1])
		flushDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(flushDeadline) {
			live := false
			for _, pid := range before {
				name, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
				if strings.TrimSpace(string(name)) == "chrome" || strings.TrimSpace(string(name)) == "chromium" {
					live = true
				}
			}
			if !live {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}

	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		deadline := time.NewTimer(5 * time.Second)
		for {
			procs, err := processes(r)
			if err != nil {
				deadline.Stop()
				return err
			}
			if len(procs) == 0 {
				deadline.Stop()
				goto stopped
			}
			for _, pid := range procs {
				if err = syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
					deadline.Stop()
					return err
				}
			}
			select {
			case <-ctx.Done():
				deadline.Stop()
				return ctx.Err()
			case <-deadline.C:
				goto nextSignal
			case <-time.After(50 * time.Millisecond):
			}
		}
	nextSignal:
	}
	return fmt.Errorf("screen UID still has processes after SIGKILL")
stopped:
	// Check every reserved listener too; a UID check alone is insufficient.
	for _, port := range ports(r) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 150*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return fmt.Errorf("screen listener %d still reachable", port)
		}
	}
	for _, path := range []string{filepath.Join("/tmp", ".X"+strings.TrimPrefix(display(r), ":")+"-lock"), filepath.Join("/tmp/.X11-unix", "X"+strings.TrimPrefix(display(r), ":"))} {
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	p.mu.Lock()
	delete(p.clients, r.ID)
	p.mu.Unlock()
	return nil
}

// RunSession is invoked after dropping UID/GID and opening a dedicated D-Bus.
// No management token or inherited AIO connection configuration reaches it.
func RunSession(ctx context.Context, r Resource, root, run string) error {
	syscall.Umask(0007)
	home := filepath.Join(root, "profiles", r.ID)
	runtimeDir := filepath.Join(run, r.ID)
	ps := ports(r)
	env := append(os.Environ(), "AIO_RUST_LOG_LEVEL=error", "BROWSER_REMOTE_DEBUGGING_HOST=127.0.0.1", "BROWSER_REMOTE_DEBUGGING_PORT="+strconv.Itoa(ps[1]), "AIO_COMPUTER_USE_LISTEN=127.0.0.1:"+strconv.Itoa(ps[2]), "AIO_COMPUTER_USE_URL=http://127.0.0.1:"+strconv.Itoa(ps[2]), "AIO_RUNTIME_DIR="+filepath.Join(runtimeDir, "aiod"))
	extra, _ := json.Marshal(map[string]any{"browser": map[string]string{"url": fmt.Sprintf("http://127.0.0.1:%d/mcp", ps[3])}})
	env = append(env, "EXTRA_MCP_SERVERS="+string(extra))
	cookie := computer.NewID()
	auth := exec.CommandContext(ctx, "xauth", "-f", os.Getenv("XAUTHORITY"), "add", display(r), ".", cookie)
	if err := auth.Run(); err != nil {
		return err
	}
	failures := make(chan error, 8)
	launch := func(name string, args ...string) error {
		log, err := os.OpenFile(filepath.Join(runtimeDir, name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = env
		cmd.Stdout = log
		cmd.Stderr = log
		err = cmd.Start()
		_ = log.Close()
		if err != nil {
			return err
		}
		go func() {
			err := cmd.Wait()
			if err == nil {
				err = fmt.Errorf("%s exited", name)
			}
			failures <- err
		}()
		return nil
	}
	if err := launch("vnc", "Xvnc", display(r), "-geometry", "1280x900", "-depth", "24", "-localhost", "-rfbport", strconv.Itoa(ps[0]), "-SecurityTypes", "None", "-AlwaysShared", "-auth", os.Getenv("XAUTHORITY"), "-nolisten", "tcp"); err != nil {
		return err
	}
	for {
		check := exec.CommandContext(ctx, "xdpyinfo")
		check.Env = env
		if check.Run() == nil {
			break
		}
		select {
		case err := <-failures:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	commands := [][]string{
		{"desktop", "xfce4-session"},
		{"browser", "/usr/local/bin/browser", "--no-sandbox", "--disable-dev-shm-usage", "--disable-gpu", "--disable-backgrounding-occluded-windows", "--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--disable-background-networking", "--disable-infobars", "--no-first-run", "--no-default-browser-check", "--disable-session-crashed-bubble", "--force-renderer-accessibility", "--password-store=basic", "--use-mock-keychain", "--user-data-dir=" + filepath.Join(home, "profile"), "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=" + strconv.Itoa(ps[1]), "--window-size=1280,840", "--window-position=0,30", "about:blank"},
		{"worker", "computer-use"},
		{"browser-mcp", "mcp-server-browser", "--port", strconv.Itoa(ps[3]), "--host", "127.0.0.1", "--browser", "chrome", "--output-dir", filepath.Join("/workspace/agents", r.ID, "downloads"), "--cdp-endpoint", fmt.Sprintf("http://127.0.0.1:%d/json/version", ps[1]), "--viewport-size", "0,0"},
		{"aiod", "aiod", "start", "--host", "127.0.0.1", "--port", strconv.Itoa(ps[4])},
	}
	for _, cmd := range commands {
		if err := launch(cmd[0], cmd[1:]...); err != nil {
			return err
		}
	}
	select {
	case err := <-failures:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *ProcessDriver) OpenView(ctx context.Context, r Resource) (io.ReadWriteCloser, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ports(r)[0])))
}
func (p *ProcessDriver) ViewAsset(ctx context.Context, name string) ([]byte, error) {
	if !validAsset(name) {
		return nil, computer.Fail(computer.Invalid, "invalid viewer asset")
	}
	f, err := os.Open(filepath.Join("/opt/novnc", name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if len(b) > 1<<20 {
		return nil, computer.Fail(computer.Invalid, "viewer asset too large")
	}
	return b, err
}
