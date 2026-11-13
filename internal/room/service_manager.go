//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"
)

//go:embed service_templates/*
var serviceTemplates embed.FS

type serviceRegistration struct {
	Version        int    `json:"version"`
	InstallationID string `json:"installation_id"`
	Root           string `json:"server_root"`
	Home           string `json:"home"`
	User           string `json:"user"`
	UserHome       string `json:"user_home"`
	UID            int    `json:"uid"`
	GID            int    `json:"gid"`
	Platform       string `json:"platform"`
	Unit           string `json:"unit"`
	LogRoot        string `json:"log_root"`
}

// All external commands use fixed binaries, argument arrays and a clean
// deployment environment; shell rc files and model Key exports are irrelevant.
type serviceRunner func(context.Context, string, ...string) ([]byte, error)

func runServiceCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return out, nil
}

func serviceManagerState(ctx context.Context, r serviceRegistration) (map[string]string, error) {
	state := map[string]string{}
	if r.Platform == "linux" {
		out, err := runServiceCommand(ctx, "/usr/bin/systemctl", "show", "teamx.service", "--property=ActiveState,SubState,UnitFileState,MainPID,ExecMainStatus")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				state[k] = v
			}
		}
		return state, nil
	}
	out, err := runServiceCommand(ctx, "/bin/launchctl", "print", r.domain()+"/com.teamx.server")
	if err != nil {
		if _, domainErr := runServiceCommand(ctx, "/bin/launchctl", "print", r.domain()); domainErr != nil {
			return nil, domainErr
		}
		state["state"] = "not loaded"
		return state, nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " = "); ok && (k == "state" || k == "pid" || k == "last exit code") {
			state[k] = v
		}
	}
	return state, nil
}

func newServiceRegistration(root, home, account string) (serviceRegistration, error) {
	var u *user.User
	var err error
	if runtime.GOOS == "linux" {
		if os.Geteuid() != 0 {
			return serviceRegistration{}, errors.New("Linux system service installation requires root; use --user with an existing dedicated ordinary account")
		}
		if account == "" {
			account = "teamx"
		}
		u, err = user.Lookup(account)
	} else {
		if os.Geteuid() == 0 {
			return serviceRegistration{}, errors.New("LaunchAgent must be installed by the logged-in ordinary user, without sudo")
		}
		u, err = user.Current()
		if err == nil && account != "" && account != u.Username {
			return serviceRegistration{}, errors.New("LaunchAgent account must be the current user")
		}
	}
	if err != nil {
		return serviceRegistration{}, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil || uid == 0 {
		return serviceRegistration{}, errors.New("service account must have a nonzero numeric UID")
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return serviceRegistration{}, err
	}
	r := serviceRegistration{Version: 1, Root: root, Home: home, User: u.Username, UserHome: u.HomeDir, UID: uid, GID: gid, Platform: runtime.GOOS}
	if r.Platform == "linux" {
		r.Unit = "/etc/systemd/system/teamx.service"
	} else {
		r.Unit = filepath.Join(u.HomeDir, "Library/LaunchAgents/com.teamx.server.plist")
		r.LogRoot = filepath.Join(u.HomeDir, "Library/Logs/TeamX")
	}
	return r, nil
}

func loadServiceRegistration(root string) (serviceRegistration, error) {
	var r serviceRegistration
	if err := readSnapshot(filepath.Join(root, "service.json"), &r, 32<<10); err != nil {
		return r, err
	}
	var installation serverInstallation
	if err := readSnapshot(filepath.Join(root, "installation.json"), &installation, 16<<10); err != nil {
		return r, err
	}
	if r.Version != 1 || r.Root != root || r.Platform != runtime.GOOS || r.Home != installation.Home || r.InstallationID != installation.InstanceID || installation.ServerRoot != root || r.UID == 0 {
		return r, errors.New("service registration does not match the installation")
	}
	if r.Platform == "linux" {
		if r.Unit != "/etc/systemd/system/teamx.service" || os.Geteuid() != 0 {
			return r, errors.New("system service operations require root and the registered system unit")
		}
	} else if r.UID != os.Getuid() || r.Unit != filepath.Join(r.UserHome, "Library/LaunchAgents/com.teamx.server.plist") {
		return r, errors.New("LaunchAgent does not belong to the current user")
	}
	if pathWithin(root, r.Home) || pathWithin(r.Home, root) {
		return r, errors.New("overlapping service roots")
	}
	return r, nil
}

func serviceTemplate(r serviceRegistration) ([]byte, error) {
	for _, value := range []string{r.Root, r.Home, r.UserHome, r.User} {
		if strings.ContainsAny(value, "\r\n\\") {
			return nil, errors.New("deployment paths and account names cannot contain line breaks or backslashes")
		}
	}
	name := "teamx.service.tmpl"
	if r.Platform == "darwin" {
		name = "com.teamx.server.plist.tmpl"
	}
	b, err := serviceTemplates.ReadFile("service_templates/" + name)
	if err != nil {
		return nil, err
	}
	// systemd specifiers and environment expansions are escaped independently
	// from argument quoting. XML uses its own encoder for LaunchAgent values.
	quote := func(s string) string {
		s = strings.ReplaceAll(s, "%", "%%")
		return strconv.Quote(s)
	}
	execQuote := func(s string) string { return quote(strings.ReplaceAll(s, "$", "$$")) }
	xmlQuote := func(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	t, err := template.New(name).Funcs(template.FuncMap{"q": quote, "execq": execQuote, "xml": xmlQuote, "raw": func(s string) string { return strings.ReplaceAll(s, "%", "%%") }}).Parse(string(b))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err = t.Execute(&out, r)
	return out.Bytes(), err
}

func serviceEnvironment(r serviceRegistration) []string {
	return []string{"HOME=" + r.UserHome, "USER=" + r.User, "LOGNAME=" + r.User, "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=en_US.UTF-8"}
}

func initializeServiceHome(ctx context.Context, r serviceRegistration, release string) error {
	if err := os.MkdirAll(r.Home, 0700); err != nil {
		return err
	}
	info, err := os.Stat(r.Home)
	if err != nil {
		return err
	}
	// Existing homes must already belong to this account. Never recursively
	// chown someone else's live data or follow links on the deployer's behalf.
	if st := info.Sys().(*syscall.Stat_t); int(st.Uid) != r.UID {
		entries, err := os.ReadDir(r.Home)
		if err != nil || len(entries) != 0 {
			return errors.New("existing home has a different owner")
		}
		if err := os.Chown(r.Home, r.UID, r.GID); err != nil {
			return err
		}
	}
	if err := os.Chmod(r.Home, 0700); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, filepath.Join(release, "bin/teamx"), "init", "--bundle-root", release, "--home", r.Home, "--server-root", r.Root)
	cmd.Env = serviceEnvironment(r)
	if os.Geteuid() == 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(r.UID), Gid: uint32(r.GID)}}
	}
	// init prepares files and authentication only; it never makes model calls.
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("service account initialization: %w: %s", err, bytes.TrimSpace(out))
	}
	// init has the portable default. Only a brand new managed home's runtime
	// is adjusted by the caller before init; existing config is never rewritten.
	return nil
}

func (r serviceRegistration) domain() string { return "gui/" + strconv.Itoa(r.UID) }

func serviceManager(ctx context.Context, run serviceRunner, r serviceRegistration, action string) error {
	if run == nil {
		run = runServiceCommand
	}
	call := func(name string, args ...string) error { _, err := run(ctx, name, args...); return err }
	if r.Platform == "linux" {
		switch action {
		case "register":
			return call("/usr/bin/systemctl", "daemon-reload")
		case "start":
			// reset-failed is explicit operator intent, not an automatic loop.
			if err := call("/usr/bin/systemctl", "reset-failed", "teamx.service"); err != nil {
				return err
			}
			return call("/usr/bin/systemctl", "start", "teamx.service")
		case "enable", "disable", "stop":
			return call("/usr/bin/systemctl", action, "teamx.service")
		case "unregister":
			if err := call("/usr/bin/systemctl", "disable", "teamx.service"); err != nil {
				return err
			}
			if err := os.Remove(r.Unit); err != nil {
				return err
			}
			return call("/usr/bin/systemctl", "daemon-reload")
		}
	} else {
		switch action {
		case "register":
			return nil // start bootstraps; plist provides login startup.
		case "enable":
			return call("/bin/launchctl", "enable", r.domain()+"/com.teamx.server")
		case "disable":
			return call("/bin/launchctl", "disable", r.domain()+"/com.teamx.server")
		case "start":
			out, err := run(ctx, "/bin/launchctl", "print", r.domain()+"/com.teamx.server")
			if err != nil {
				return call("/bin/launchctl", "bootstrap", r.domain(), r.Unit)
			}
			if !bytes.Contains(out, []byte("state = running")) {
				return call("/bin/launchctl", "kickstart", r.domain()+"/com.teamx.server")
			}
			return nil
		case "stop":
			if _, err := run(ctx, "/bin/launchctl", "print", r.domain()+"/com.teamx.server"); err != nil {
				// A missing job is stopped; verify the GUI domain exists so a
				// permission or login-session failure cannot masquerade as absence.
				return call("/bin/launchctl", "print", r.domain())
			}
			return call("/bin/launchctl", "bootout", r.domain()+"/com.teamx.server")
		case "unregister":
			if err := call("/bin/launchctl", "disable", r.domain()+"/com.teamx.server"); err != nil {
				return err
			}
			return os.Remove(r.Unit)
		}
	}
	return fmt.Errorf("unsupported manager action %s", action)
}

func checkServiceUnit(r serviceRegistration) error {
	want, err := serviceTemplate(r)
	if err != nil {
		return err
	}
	info, err := os.Lstat(r.Unit)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("registered service unit is not a regular file")
	}
	got, err := os.ReadFile(r.Unit)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return errors.New("service unit differs from this installation; refusing to control an unknown job")
	}
	return nil
}

// Inspect proves owner authentication and persistent installation identity. A
// listening port or public health response alone never counts as ready.
func authenticatedServiceStatus(ctx context.Context, r serviceRegistration) (map[string]any, error) {
	c, err := readStartupConfig(filepath.Join(r.Home, "config.toml"), true, false)
	if err != nil {
		return nil, err
	}
	if c.Access.LoginEnabled != nil && !*c.Access.LoginEnabled {
		return nil, errors.New("managed deployment requires login enabled")
	}
	listen := "127.0.0.1:8088"
	if c.Server.Listen != nil {
		listen = *c.Server.Listen
	}
	if err := validateManagedAccess(c, listen); err != nil {
		return nil, err
	}
	if strings.HasSuffix(listen, ":0") {
		return nil, errors.New("service manager requires a fixed loopback port")
	}
	token, err := readAdminTokenForUID(adminTokenPath(c), uint32(r.UID))
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	get := func(route string, result any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, internalAccessEndpoint(listen)+route, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("service check %s: HTTP %d", route, res.StatusCode)
		}
		return decodeServiceJSON(res.Body, result)
	}
	var session struct {
		Operator       string `json:"operator"`
		InstallationID string `json:"installation_id"`
	}
	if err := get("/v1/access/session", &session); err != nil {
		return nil, err
	}
	if session.InstallationID != r.InstallationID || session.Operator != c.Access.OperatorID {
		return nil, errors.New("endpoint belongs to a different installation or operator")
	}
	status := map[string]any{}
	if err := get("/healthz", &status); err != nil {
		return nil, err
	}
	status["authenticated"] = true
	status["installation_id"] = r.InstallationID
	if r.Platform == "linux" {
		status["platform_capabilities"] = map[string]string{"lark_send": "account_pinning_unavailable"}
	}
	if status["status"] == "running" {
		var models struct {
			ServiceEpoch string `json:"service_epoch"`
			Members      []struct {
				AgentID     string `json:"agent_id"`
				OperationID string `json:"operation_id"`
			} `json:"members"`
			Operations []struct {
				ID        string `json:"id"`
				Status    string `json:"status"`
				Committed bool   `json:"committed"`
			} `json:"operations"`
		}
		if err := get("/v1/models", &models); err != nil {
			return nil, err
		}
		if models.ServiceEpoch == "" {
			return nil, errors.New("business recovery has not reported a service epoch")
		}
		status["ready"], status["service_epoch"] = true, models.ServiceEpoch
		status["member_count"], status["model_operations"] = len(models.Members), models.Operations
	}
	return status, nil
}

func waitServiceStatus(ctx context.Context, r serviceRegistration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var last error
	for {
		status, err := authenticatedServiceStatus(ctx, r)
		if err == nil {
			return status, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("authenticated startup check: %w: %v", ctx.Err(), last)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func decodeServiceJSON(r io.Reader, value any) error {
	return json.NewDecoder(io.LimitReader(r, 4<<20)).Decode(value)
}
