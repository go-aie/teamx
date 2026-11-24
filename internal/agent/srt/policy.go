package srt

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-aie/teamx/agent"
)

type networkPolicy struct {
	AllowedDomains          []string `json:"allowedDomains"`
	DeniedDomains           []string `json:"deniedDomains"`
	StrictAllowlist         bool     `json:"strictAllowlist"`
	AllowAllUnixSockets     bool     `json:"allowAllUnixSockets"`
	AllowLocalBinding       bool     `json:"allowLocalBinding"`
	DeniedResolvedAddresses []string `json:"deniedResolvedAddresses"`
	RequireHostMatch        bool     `json:"requireHostMatch"`
}
type filesystemPolicy struct {
	DenyRead, AllowRead, AllowWrite, DenyWrite []string
}

func (p filesystemPolicy) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"denyRead": p.DenyRead, "allowRead": p.AllowRead, "allowWrite": p.AllowWrite, "denyWrite": p.DenyWrite})
}

type policy struct {
	routes     []endpointRoute
	Network    networkPolicy    `json:"network"`
	Filesystem filesystemPolicy `json:"filesystem"`
	BwrapPath  string           `json:"bwrapPath"`
	SocatPath  string           `json:"socatPath"`
	Ripgrep    struct {
		Command string `json:"command"`
	} `json:"ripgrep"`
	EnableWeakerNestedSandbox    bool `json:"enableWeakerNestedSandbox"`
	EnableWeakerNetworkIsolation bool `json:"enableWeakerNetworkIsolation"`
	AllowNestedUserNamespaces    bool `json:"allowNestedUserNamespaces"`
}

var systemReadPaths = []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/dev", "/proc", "/etc/ssl/certs", "/etc/alternatives", "/etc/ld.so.cache", "/etc/hosts", "/etc/resolv.conf", "/etc/protocols", "/etc/services", "/etc/nsswitch.conf"}

func (f *Factory) basePolicy() policy {
	p := policy{Network: networkPolicy{AllowedDomains: []string{}, DeniedDomains: []string{}, StrictAllowlist: true, RequireHostMatch: true,
		DeniedResolvedAddresses: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7"}},
		Filesystem: filesystemPolicy{DenyRead: []string{"/"}, AllowRead: slices.Clone(systemReadPaths), AllowWrite: []string{}, DenyWrite: []string{}},
		BwrapPath:  f.config.Bwrap.Path, SocatPath: f.config.Socat.Path}
	p.Ripgrep.Command = f.config.Ripgrep.Path
	for _, a := range append([]Artifact{f.config.Node, f.config.Bwrap, f.config.Socat, f.config.Ripgrep, f.config.Runtime}, f.config.ReadOnly...) {
		p.Filesystem.AllowRead = append(p.Filesystem.AllowRead, a.Path)
	}
	return p
}

func endpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(u.Hostname(), "*?\\") {
		return "", errors.New("SRT requires exact endpoint origins")
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		return "", errors.New("unapproved IP execution endpoint")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return "", errors.New("invalid SRT endpoint port")
	}
	return net.JoinHostPort(host, port), nil
}

func physical(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00*?[") {
		return "", errors.New("SRT requires canonical literal paths")
	}
	return filepath.EvalSymlinks(path)
}

func (f *Factory) policy(r agent.ExecutionRequest) (policy, error) {
	p := f.basePolicy()
	if r.BinaryPath != "" {
		binary, err := physical(r.BinaryPath)
		if err != nil {
			return p, errors.New("SRT execution binary is unavailable")
		}
		approved := binary == f.config.Node.Path
		for _, release := range f.config.ReadOnly {
			approved = approved || within(release.Path, binary)
		}
		if !approved {
			return p, errors.New("SRT binary is outside the pinned release")
		}
	}
	b := r.Binding
	if f.user == "" || f.space == "" || b.UserID != f.user || b.SpaceID != f.space {
		return p, errors.New("SRT execution space binding mismatch")
	}
	if !b.Probe && (b.RoomID == "" || b.RoomInstance == "" || b.AgentID == "" || b.MembershipID == "" || b.Generation != filepath.Base(r.StateDir)) {
		return p, errors.New("SRT requires the complete member and generation binding")
	}
	workspace, err := physical(r.Workspace)
	if err != nil || workspace != r.Workspace {
		return p, errors.New("SRT workspace must be a physical approved directory")
	}
	if err := checkWorkspaceMounts(workspace); err != nil {
		return p, err
	}
	state, err := physical(r.StateDir)
	if err != nil || state != r.StateDir || within(workspace, state) || within(state, workspace) {
		return p, errors.New("SRT state and workspace must be separate physical directories")
	}
	for _, read := range p.Filesystem.AllowRead {
		resolved, err := filepath.EvalSymlinks(read)
		if err == nil && (within(workspace, resolved) || within(resolved, workspace) || within(state, resolved) || within(resolved, state)) {
			return p, errors.New("SRT writable directories overlap a runtime read grant")
		}
	}
	for _, name := range []string{"home", "agents", "tmp"} {
		info, err := os.Lstat(filepath.Join(state, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return p, err
		}
		if err == nil && !info.IsDir() {
			return p, errors.New("SRT state child is not a physical directory")
		}
	}
	for _, root := range f.config.PrivateRoots {
		root, err := physical(root)
		if err != nil || within(workspace, root) {
			return p, errors.New("SRT workspace exposes a private root")
		}
		for _, read := range p.Filesystem.AllowRead {
			resolved, err := filepath.EvalSymlinks(read)
			if err == nil && (within(resolved, root) || within(root, resolved)) {
				return p, errors.New("SRT runtime read grant overlaps a private root")
			}
		}
	}
	// Reject pre-exposed private inodes; path hiding cannot protect existing
	// hardlinks. This bounded scan never follows workspace symlinks.
	count := 0
	err = filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		count++
		if count > 100000 {
			return errors.New("SRT workspace admission scan exceeds limit")
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if multipleLinks(info) {
				return errors.New("SRT workspace contains pre-existing hardlinks")
			}
		}
		return nil
	})
	if err != nil {
		return p, err
	}
	versionOnly := len(r.Args) > 0 && r.Args[len(r.Args)-1] == "--version"
	if len(r.Endpoints) == 0 && !versionOnly {
		return p, errors.New("SRT requires explicit execution endpoints")
	}
	allowed := map[string]bool{}
	routes := map[string]endpointRoute{}
	for _, raw := range f.config.Endpoints {
		original, err := endpoint(raw)
		if err != nil {
			return p, err
		}
		_ = original
		effective, route, err := routedEndpoint(raw)
		if err != nil {
			return p, err
		}
		target, err := endpoint(effective)
		if err != nil {
			return p, err
		}
		allowed[target] = true
		if route != nil {
			routes[target] = *route
		}
	}
	endpoints := r.Endpoints
	if versionOnly {
		endpoints = nil
	}
	for _, raw := range endpoints {
		effective, _, err := routedEndpoint(raw)
		if err != nil {
			return p, err
		}
		target, err := endpoint(effective)
		if err != nil {
			return p, err
		}
		if !allowed[target] {
			return p, errors.New("SRT endpoint is outside deployment authority")
		}
		p.Network.AllowedDomains = append(p.Network.AllowedDomains, target)
		if route, ok := routes[target]; ok {
			_, port, _ := net.SplitHostPort(target)
			p.Network.AllowedDomains = append(p.Network.AllowedDomains, net.JoinHostPort(route.Address, port))
			if !slices.Contains(p.routes, route) {
				p.routes = append(p.routes, route)
			}
		}
	}
	p.Network.AllowedDomains = sortedUnique(p.Network.AllowedDomains)
	for _, path := range []string{workspace, filepath.Join(state, "home"), filepath.Join(state, "agents"), filepath.Join(state, "tmp")} {
		p.Filesystem.AllowRead = append(p.Filesystem.AllowRead, path)
		p.Filesystem.AllowWrite = append(p.Filesystem.AllowWrite, path)
	}
	for _, name := range []string{"AGENTS.md", "cordis.patch.yml"} {
		p.Filesystem.DenyWrite = append(p.Filesystem.DenyWrite, filepath.Join(state, "home", name))
	}
	for _, name := range []string{"input_bridge.mjs", "input-execution.json", "probe.mjs"} {
		p.Filesystem.AllowRead = append(p.Filesystem.AllowRead, filepath.Join(state, name))
	}
	p.Filesystem.AllowRead = append(p.Filesystem.AllowRead, filepath.Join(state, "control"))
	p.Filesystem.AllowRead = sortedUnique(p.Filesystem.AllowRead)
	return p, nil
}

func (f *Factory) launch(r agent.ExecutionRequest, check bool, supervisorTemp ...string) (agent.ExecutionRequest, error) {
	p := f.basePolicy()
	var err error
	if !check {
		p, err = f.policy(r)
		if err != nil {
			return r, err
		}
		if err := os.MkdirAll(filepath.Join(r.StateDir, "tmp"), 0700); err != nil {
			return r, err
		}
	}
	config := struct {
		Runtime string
		Policy  policy
		Binary  string
		Args    []string
		Check   bool
		Temp    string
		Routes  []endpointRoute
	}{f.config.Runtime.Path, p, r.BinaryPath, r.Args, check, filepath.Join(r.StateDir, "tmp"), p.routes}
	if len(supervisorTemp) > 0 {
		// SRT binds the mux socket before installing its filesystem mounts.
		// Preserve this Session's bridge under denyRead=/ without granting the
		// workload any write access to the supervisor's directory.
		config.Policy.Filesystem.AllowRead = append(slices.Clone(config.Policy.Filesystem.AllowRead), supervisorTemp[0])
		config.Policy.Filesystem.DenyWrite = append(slices.Clone(config.Policy.Filesystem.DenyWrite), supervisorTemp[0])
	}
	data, err := json.Marshal(config)
	if err != nil {
		return r, err
	}
	// A private unlinked regular file avoids a blocked writer before spawn.
	// The child closes fd 3 before starting any sandboxed/untrusted process.
	file, err := os.CreateTemp("", "teamx-srt-control-*")
	if err != nil {
		return r, err
	}
	_ = os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return r, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		file.Close()
		return r, err
	}
	for _, name := range r.EnvironmentKeys {
		if strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_") || slices.Contains([]string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "NODE_USE_ENV_PROXY", "DSH_TEAMX_EXECUTION_PROXY", "DSH_TEAMX_INPUT_BINDING", "TEAMX_ADMIN_TOKEN", "TEAMX_ADMIN_TOKEN_FILE", "TEAMX_SESSION", "TEAMX_SESSION_COOKIE", "PATH", "HOME", "TMPDIR", "NODE_OPTIONS", "NODE_PATH", "BASH_ENV", "ENV", "SHELLOPTS", "PYTHONPATH", "PYTHONHOME", "RUBYOPT", "PERL5OPT", "GCONV_PATH"}, name) {
			file.Close()
			return r, errors.New("SRT credential binding uses a reserved interpreter environment variable")
		}
	}
	env := []string{"PATH=" + filepath.Dir(f.config.Node.Path) + ":" + filepath.Dir(f.config.Bwrap.Path) + ":" + filepath.Dir(f.config.Socat.Path) + ":/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	if !check {
		for _, item := range r.Environment {
			name, _, _ := strings.Cut(item, "=")
			if slices.Contains(r.EnvironmentKeys, name) || slices.Contains([]string{"DSH_HOME", "DSH_AGENTS_HOME", "DSH_PERMISSION_MODE", "DSH_TELEMETRY_DISABLED", "TEAMX_EXECUTION_TOKEN", "TEAMX_EXECUTION_AUTH", "TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID", "TEAMX_CLI", "TEAMX_INPUT_CONTRACT", "TEAMX_TOOL_CONTRACT"}, name) {
				env = append(env, item)
			}
		}
		env = append(env, "HOME="+filepath.Join(r.StateDir, "home"))
	}
	if len(supervisorTemp) > 0 {
		env = append(env, "TMPDIR="+supervisorTemp[0])
	}
	return agent.ExecutionRequest{BinaryPath: f.config.Node.Path, Args: []string{"--input-type=module", "-e", supervisor},
		Environment: env, Workspace: r.Workspace, StateDir: r.StateDir, Binding: r.Binding, Files: []*os.File{file}, Supervised: !check, RelayExecutables: []string{f.config.Socat.Path}}, nil
}
