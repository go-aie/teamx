package srt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type installedContract struct {
	node       string
	Schema     int    `json:"schema_version"`
	Target     string `json:"target"`
	Profile    string `json:"profile"`
	Version    string `json:"version"`
	LockSHA256 string `json:"npm_lock_sha256"`
	installedHost
	AdditionalHosts []installedHost `json:"additional_hosts,omitempty"`
}

type installedHost struct {
	System struct {
		ID      string `json:"id"`
		Version string `json:"version_id"`
		Glibc   string `json:"glibc"`
	} `json:"system"`
	Tools []struct {
		Path    string   `json:"path"`
		SHA256  string   `json:"binary_sha256"`
		Args    []string `json:"version_args"`
		Version string   `json:"version_contains"`
	} `json:"host_tools"`
}

// InstalledConfig uses only the published release and fixed host prerequisites.
// It acquires no execution resources and does not change a Session or policy.
func InstalledConfig(release string, roots, endpoints []string) (Config, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return Config{}, errors.New("installed SRT requires Linux amd64 standard")
	}
	return installedConfig(release, roots, endpoints, checkInstalledHost)
}

func installedConfig(release string, roots, endpoints []string, checkHost func(*installedContract) ([]string, error)) (Config, error) {
	var c Config
	physicalRelease, err := filepath.EvalSymlinks(release)
	if err != nil || physicalRelease != release || !filepath.IsAbs(release) {
		return c, errors.New("SRT installation requires the physical release path")
	}
	var contract installedContract
	data, err := os.ReadFile(filepath.Join(release, "share/teamx/srt-runtime.json"))
	if err != nil {
		return c, fmt.Errorf("SRT installation contract: %w", err)
	}
	if json.Unmarshal(data, &contract) != nil || contract.Schema != 1 || contract.Target != "linux-amd64" || contract.Profile != "standard" || contract.Version != Version || contract.LockSHA256 != digest(dependencyLock) || len(contract.Tools) != 3 {
		return c, errors.New("SRT installation contract differs from execution contract")
	}
	if len(endpoints) == 0 || len(roots) == 0 {
		return c, errors.New("SRT installation needs explicit endpoints and private roots")
	}
	for _, raw := range endpoints {
		if _, err := endpoint(raw); err != nil {
			return c, err
		}
		// This installation input is an origin, never a model URL or credential.
		if strings.Contains(strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://"), "/") {
			return c, errors.New("SRT installation endpoints must be origins without paths")
		}
	}
	lock, err := os.ReadFile(filepath.Join(release, "runtime/srt/package-lock.json"))
	if err != nil || digest(lock) != contract.LockSHA256 {
		return c, errors.New("installed SRT npm lock differs")
	}
	contract.node = filepath.Join(release, "runtime/node/bin/node")
	libraries, err := checkHost(&contract)
	if err != nil {
		return c, err
	}
	pin := func(path string) Artifact {
		if err != nil {
			return Artifact{}
		}
		var sha string
		sha, err = DigestPath(path)
		return Artifact{Path: path, SHA256: sha}
	}
	c.Node = pin(filepath.Join(release, "runtime/node/bin/node"))
	c.Runtime = pin(filepath.Join(release, "runtime/srt"))
	for _, tool := range contract.Tools {
		physical, resolveErr := filepath.EvalSymlinks(tool.Path)
		if resolveErr != nil {
			return c, resolveErr
		}
		a := pin(physical)
		if a.SHA256 != tool.SHA256 {
			return c, errors.New("SRT host helper digest mismatch: " + tool.Path)
		}
		switch filepath.Base(tool.Path) {
		case "bwrap":
			c.Bwrap = a
		case "socat":
			c.Socat = a
		case "rg":
			c.Ripgrep = a
		default:
			return c, errors.New("unknown SRT host helper")
		}
	}
	if c.Bwrap.Path == "" || c.Socat.Path == "" || c.Ripgrep.Path == "" {
		return c, errors.New("incomplete SRT host helpers")
	}
	for _, path := range append([]string{filepath.Join(release, "bin"), filepath.Join(release, "runtime/dsh")}, libraries...) {
		c.ReadOnly = append(c.ReadOnly, pin(path))
	}
	if err != nil {
		return c, err
	}
	for _, root := range roots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
			return c, errors.New("SRT requires canonical private roots")
		}
		for _, read := range append([]Artifact{c.Node, c.Bwrap, c.Socat, c.Ripgrep, c.Runtime}, c.ReadOnly...) {
			if within(root, read.Path) || within(read.Path, root) {
				return c, errors.New("SRT runtime overlaps private root")
			}
		}
	}
	c.PrivateRoots, c.Endpoints = roots, endpoints
	return c, nil
}

func selectInstalledHost(c *installedContract, id, version, glibc string) error {
	for _, host := range append([]installedHost{c.installedHost}, c.AdditionalHosts...) {
		if host.System.ID == id && host.System.Version == version && host.System.Glibc == glibc {
			if len(host.Tools) != 3 {
				return errors.New("incomplete SRT host tool contract")
			}
			c.installedHost = host
			return nil
		}
	}
	return fmt.Errorf("SRT host baseline is not in the release contract: %s %s, %s", id, version, glibc)
}

func checkInstalledHost(c *installedContract) ([]string, error) {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = strings.Trim(value, `"`)
		}
	}
	run := func(name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	glibc, err := run("/usr/bin/getconf", "GNU_LIBC_VERSION")
	if err != nil {
		return nil, errors.New("SRT host glibc version unavailable")
	}
	if err := selectInstalledHost(c, values["ID"], values["VERSION_ID"], strings.TrimSpace(glibc)); err != nil {
		return nil, err
	}
	var libraries, binaries []string
	for _, tool := range c.Tools {
		// Do not execute an unexpected or unpinned host program, including ldd.
		if tool.Path != "/usr/bin/bwrap" && tool.Path != "/usr/bin/socat" && tool.Path != "/usr/bin/rg" {
			return nil, errors.New("invalid SRT host helper path")
		}
		physical, resolveErr := filepath.EvalSymlinks(tool.Path)
		if resolveErr != nil {
			return nil, errors.New("SRT host helper is unavailable: " + tool.Path)
		}
		if filepath.Dir(physical) != "/usr/bin" {
			return nil, errors.New("SRT host helper link leaves /usr/bin")
		}
		info, err := os.Lstat(physical)
		sha, digestErr := DigestPath(physical)
		if err != nil || digestErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 || sha != tool.SHA256 {
			return nil, fmt.Errorf("SRT requires the locked non-setuid host helper: %s", tool.Path)
		}
		if out, err := run(physical, tool.Args...); err != nil || !strings.Contains(out, tool.Version) {
			return nil, errors.New("SRT host helper version mismatch: " + tool.Path)
		}
		binaries = append(binaries, physical)
	}
	binaries = append(binaries, c.node)
	for _, binary := range binaries {
		out, err := run("/usr/bin/ldd", binary)
		if err != nil || strings.Contains(out, "not found") {
			return nil, errors.New("SRT host dynamic library closure missing: " + binary)
		}
		for _, line := range strings.Split(out, "\n") {
			for _, field := range strings.Fields(line) {
				if filepath.IsAbs(field) {
					resolved, err := filepath.EvalSymlinks(field)
					if err != nil {
						return nil, err
					}
					libraries = append(libraries, resolved)
				}
			}
		}
	}
	return sortedUnique(libraries), nil
}
