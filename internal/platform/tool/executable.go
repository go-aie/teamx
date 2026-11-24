package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
