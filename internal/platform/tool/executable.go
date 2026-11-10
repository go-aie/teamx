package tool

import (
	"os"
	"os/exec"
	"path/filepath"
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
		if !ExecutableAvailable(resolveExecutable(dep.Name, os.Getenv(dep.OverrideEnvironment), self)) {
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
