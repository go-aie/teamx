package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Root selects a release; all live resources use Release, never current.
// Archive validation and publication belong to the deployment command.
type serverLayout struct {
	Root, Release, Home string
}

func validateManagedListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	n, numberErr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || numberErr != nil || n < 0 || n > 65535 {
		return errors.New("managed service requires a loopback IP and port; use an HTTPS reverse proxy for remote access")
	}
	return nil
}

func validateManagedAccess(c startupConfig, address string) error {
	if configuredAccessMode(c) == accessLAN {
		return validateAccessListen(c, address)
	}
	return validateManagedListen(address)
}

func defaultServerDirectories(goos, userHome string) (root, home string, err error) {
	switch goos {
	case "linux":
		return "/opt/teamx", "/var/lib/teamx", nil
	case "darwin":
		base := filepath.Join(userHome, "Library", "Application Support", "TeamX")
		return filepath.Join(base, "server"), filepath.Join(base, "home"), nil
	default:
		return "", "", fmt.Errorf("server deployment is not supported on %s", goos)
	}
}

func resolveServerLayout(root, home string) (layout serverLayout, err error) {
	if home == "" {
		userHome, e := os.UserHomeDir()
		if e != nil {
			return layout, e
		}
		_, home, err = defaultServerDirectories(runtime.GOOS, userHome)
		if err != nil {
			return layout, err
		}
	}
	// Resolve the selected release before creating any user data. A broken
	// current link must not silently become a fresh portable installation.
	if root != "" {
		root, err = filepath.Abs(root)
		if err == nil {
			layout.Root, err = filepath.EvalSymlinks(root)
		}
		if err != nil {
			return layout, fmt.Errorf("server root: %w", err)
		}
		layout.Release, err = filepath.EvalSymlinks(filepath.Join(layout.Root, "current"))
		if err != nil {
			return layout, fmt.Errorf("current release: %w", err)
		}
		if filepath.Dir(layout.Release) != filepath.Join(layout.Root, "releases") {
			return layout, errors.New("current must select a direct version directory under server-root/releases")
		}
		var manifest struct {
			Schema  int    `json:"schema_version"`
			Version string `json:"version"`
			Profile string `json:"profile"`
			Target  string `json:"target"`
		}
		b, e := os.ReadFile(filepath.Join(layout.Release, "manifest.json"))
		if e != nil || json.Unmarshal(b, &manifest) != nil || manifest.Schema != 1 || manifest.Version == "" || manifest.Profile != "standard" || manifest.Target != runtime.GOOS+"-"+runtime.GOARCH {
			return layout, errors.New("current requires a standard manifest for this server platform")
		}
	}
	home, err = resolveFutureDirectory(home)
	if err != nil {
		return layout, err
	}
	if layout.Root != "" && pathWithin(layout.Root, home) {
		return layout, errors.New("server home must be outside the installation root")
	}
	if err = os.MkdirAll(home, 0700); err != nil {
		return layout, err
	}
	layout.Home, err = filepath.EvalSymlinks(home)
	if err == nil && layout.Root != "" && pathWithin(layout.Root, layout.Home) {
		return layout, errors.New("server home must be outside the installation root")
	}
	return layout, err
}

// Resolve existing ancestors before creating a home, including a symlinked
// parent of a directory that does not exist yet.
func resolveFutureDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// A dangling link is an invalid path rather than a missing directory.
	if info, statErr := os.Lstat(abs); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("directory is a dangling symlink: %s", abs)
	}
	parent, err := resolveFutureDirectory(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type serverInstallation struct {
	Version    int    `json:"version"`
	InstanceID string `json:"instance_id"`
	ServerRoot string `json:"server_root"`
	Home       string `json:"home"`
}

// Atomic create never replaces registration. Repeated starts verify the same
// roots, including a concurrent first registration from another home.
func registerServerInstallation(layout serverLayout) error {
	id, err := newOpaqueID()
	if err != nil {
		return err
	}
	data, err := json.Marshal(serverInstallation{1, id, layout.Root, layout.Home})
	if err != nil {
		return err
	}
	path := filepath.Join(layout.Root, "installation.json")
	if err := installMissing(path, data); err != nil {
		return err
	}
	var saved serverInstallation
	if err := readSnapshot(path, &saved, 16<<10); err != nil {
		return fmt.Errorf("installation registration: %w", err)
	}
	if saved.Version != 1 || saved.InstanceID == "" || saved.ServerRoot != layout.Root || saved.Home != layout.Home {
		return errors.New("installation registration does not match this server root and home")
	}
	return nil
}

func pinBundledRuntime(config *startupConfig, layout serverLayout) error {
	if config.Runtime == nil || layout.Release == "" {
		return nil
	}
	path := config.Runtime.DSH.BinaryPath
	// Only the managed default tracks current. Explicit external runtimes and
	// paths to previous releases keep their existing meaning.
	if path != filepath.Join(layout.Root, "current", "bin", "dsh") {
		return nil
	}
	real, err := filepath.EvalSymlinks(filepath.Join(layout.Release, "bin", "dsh"))
	if err != nil {
		return fmt.Errorf("bundled DSH: %w", err)
	}
	if !pathWithin(layout.Release, real) {
		return errors.New("bundled DSH must remain within the selected release")
	}
	config.Runtime.DSH.BinaryPath = real
	return nil
}
