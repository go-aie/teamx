package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExecutableResolution(t *testing.T) {
	serverDir, pathDir := t.TempDir(), t.TempDir()
	name := "fixture-tool"
	sibling, fromPath := filepath.Join(serverDir, name), filepath.Join(pathDir, name)
	for _, path := range []string{sibling, fromPath} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", pathDir)
	self := filepath.Join(serverDir, "teamx")
	// Explicit configuration is authoritative, even when broken. Never silently
	// substitute another binary for an operator-selected version.
	override := filepath.Join(t.TempDir(), "missing")
	if got := resolveExecutable(name, override, self); got != override {
		t.Fatal(got)
	}
	if got := resolveExecutable(name, "", self); got != sibling {
		t.Fatal(got)
	}
	if err := os.Remove(sibling); err != nil {
		t.Fatal(err)
	}
	if got, err := exec.LookPath(resolveExecutable(name, "", self)); err != nil || got != fromPath {
		t.Fatal(got, err)
	}
	if got := resolveExecutable(name, "", ""); got != name {
		t.Fatal(got)
	}
}
