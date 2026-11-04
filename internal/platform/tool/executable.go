package tool

import (
	"os"
	"os/exec"
	"path/filepath"
)

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
