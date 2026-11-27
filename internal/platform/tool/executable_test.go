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

func TestServiceExecutableSelection(t *testing.T) {
	release, pathDir := t.TempDir(), t.TempDir()
	bin := filepath.Join(release, "lark-cli")
	fromPath := filepath.Join(pathDir, "lark-cli")
	for _, p := range []string{bin, fromPath} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(t.TempDir(), "current")
	if err := os.Symlink(release, alias); err != nil {
		t.Fatal(err)
	}
	physical, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	environment := []string{"PATH=.:relative::" + pathDir}
	got, err := ResolveExecutableIn("lark-cli", "", alias, environment)
	if err != nil || got != physical {
		t.Fatal(got, err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(pathDir, alias); err != nil {
		t.Fatal(err)
	}
	if got != physical {
		t.Fatal("binding tracked mutable alias")
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveExecutableIn("lark-cli", "", release, environment)
	if err != nil || got != physical {
		t.Fatal("broken install fell back to PATH", got, err)
	}
	override := filepath.Join(release, "missing")
	overridePhysical, _ := filepath.EvalSymlinks(release)
	got, err = ResolveExecutableIn("lark-cli", override, "", environment)
	if err != nil || got != filepath.Join(overridePhysical, "missing") {
		t.Fatal("broken override fell back", got, err)
	}
	for _, override := range []string{"lark-cli", "./lark-cli", release + "/../lark-cli"} {
		if _, err := ResolveExecutableIn("lark-cli", override, release, environment); err == nil {
			t.Fatal("invalid override accepted", override)
		}
	}
	got, err = ResolveExecutableIn("lark-cli", "", "", environment)
	pathPhysical, _ := filepath.EvalSymlinks(fromPath)
	if err != nil || got != pathPhysical {
		t.Fatal(got, err)
	}
	if _, err := ResolveExecutableIn("not-a-real-cli", "", "", []string{"PATH=.:relative:"}); err == nil {
		t.Fatal("relative PATH searched")
	}
}

func TestDependencySelectionUsesWrapperAuthority(t *testing.T) {
	wrapperDir, pathDir := t.TempDir(), t.TempDir()
	wrapper := filepath.Join(wrapperDir, "teamx-gitlab")
	sibling, fromPath := filepath.Join(wrapperDir, "glab"), filepath.Join(pathDir, "glab")
	for _, path := range []string{wrapper, sibling, fromPath} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	physicalSibling, _ := filepath.EvalSymlinks(sibling)
	physicalPath, _ := filepath.EvalSymlinks(fromPath)
	environment := []string{"PATH=.:relative::" + pathDir}
	for _, installed := range []bool{false, true} {
		got, err := ResolveDependencyIn("glab", "", wrapper, installed, environment)
		if err != nil || got != physicalSibling {
			t.Fatal(installed, got, err)
		}
	}
	if err := os.Remove(sibling); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveDependencyIn("glab", "", wrapper, false, environment)
	if err != nil || got != physicalPath {
		t.Fatal("standalone PATH selection", got, err)
	}
	got, err = ResolveDependencyIn("glab", "", wrapper, true, environment)
	if err != nil || got != physicalSibling || ExecutableAvailable(got) {
		t.Fatal("broken installation fell back to PATH", got, err)
	}
	for _, installed := range []bool{false, true} {
		got, err := ResolveDependencyIn("glab", physicalSibling, wrapper, installed, environment)
		if err != nil || got != physicalSibling || ExecutableAvailable(got) {
			t.Fatal("broken override fell back", got, err)
		}
		if _, err := ResolveDependencyIn("glab", "./glab", wrapper, installed, environment); err == nil {
			t.Fatal("relative override accepted")
		}
	}
}
