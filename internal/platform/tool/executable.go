package tool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolveExecutableIn selects a physical program from trusted service inputs.
// A nonempty binDir denotes a complete installation: missing siblings and
// explicit overrides never fall back to PATH. Selection does not run a CLI.
func ResolveExecutableIn(name, override, binDir string, environment []string) (string, error) {
	self, _ := os.Executable()
	return resolveExecutableIn(name, override, binDir, self, environment)
}

// ResolveDependencyIn uses the selected wrapper, rather than the service binary,
// as the sibling authority. Complete installations never fall back to PATH.
func ResolveDependencyIn(name, override, wrapper string, installed bool, environment []string) (string, error) {
	binDir := ""
	if installed {
		binDir = filepath.Dir(wrapper)
	}
	return resolveExecutableIn(name, override, binDir, wrapper, environment)
}

func resolveExecutableIn(name, override, binDir, self string, environment []string) (string, error) {
	physical := func(path string) (string, error) {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
			return "", fmt.Errorf("invalid executable path")
		}
		resolved, err := filepath.EvalSymlinks(path)
		if os.IsNotExist(err) {
			// Preserve the selected location for per-call availability checks.
			parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
			if parentErr == nil {
				return filepath.Join(parent, filepath.Base(path)), nil
			}
			return path, nil
		}
		return resolved, err
	}
	if override != "" {
		return physical(override)
	}
	if binDir != "" {
		return physical(filepath.Join(binDir, name))
	}
	if real, err := filepath.EvalSymlinks(self); err == nil {
		candidate := filepath.Join(filepath.Dir(real), name)
		if ExecutableAvailable(candidate) {
			return physical(candidate)
		}
	}
	path, _ := dEnvironment(Definition{ProcessEnvironment: environment}, "PATH")
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) { // excludes empty and relative cwd entries
			continue
		}
		candidate := filepath.Join(dir, name)
		if ExecutableAvailable(candidate) {
			return physical(candidate)
		}
	}
	return "", exec.ErrNotFound
}

// Checked again for each discovery and execution; repairing a server install
// does not require rewriting member permissions.
func ExecutableAvailable(name string) bool {
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

type ExecutableDependency struct{ Name, OverrideEnvironment string }

func DependenciesAvailable(d Definition) bool {
	if len(d.Dependencies) > 0 && d.Executable == "" {
		return false // The outer composition could not bind the program pair.
	}
	self, _ := exec.LookPath(d.Executable)
	for _, dep := range d.Dependencies {
		override, _ := dEnvironment(d, dep.OverrideEnvironment)
		if !ExecutableAvailable(resolveExecutable(dep.Name, override, self)) {
			return false
		}
	}
	return true
}

// ResolveExecutable honors an explicit override, then looks beside the running
// program, then uses PATH. It never searches agent workspaces or temp trees.
func ResolveExecutable(name, override string) string {
	self, _ := os.Executable()
	return resolveExecutable(name, override, self)
}

func resolveExecutable(name, override, self string) string {
	if override != "" {
		return override
	}
	if self != "" {
		candidate := filepath.Join(filepath.Dir(self), name)
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return name
}

func dEnvironment(d Definition, key string) (string, bool) {
	if d.ProcessEnvironment == nil {
		return os.LookupEnv(key)
	}
	for _, entry := range d.ProcessEnvironment {
		if name, value, ok := strings.Cut(entry, "="); ok && name == key {
			return value, true
		}
	}
	return "", false
}
