//go:build integration || e2e

package room

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Deliberately serial: poison the inherited environment, then exercise two
// independent fixtures and the real preparation subprocesses concurrently.
func TestWorkspaceFixtureIsolatesGitHomesAndHostConfiguration(t *testing.T) {
	host := t.TempDir()
	hooks := filepath.Join(host, "hooks")
	template := filepath.Join(host, "template")
	for _, dir := range []string{hooks, template, filepath.Join(host, "git")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(host, "executed")
	// These hooks and helper would fail (and leave evidence) if inherited.
	trap := "#!/bin/sh\nprintf leaked > '" + marker + "'\nexit 1\n"
	for _, name := range []string{"pre-commit", "post-checkout"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte(trap), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(template, "host-template"), []byte("host"), 0600); err != nil {
		t.Fatal(err)
	}
	poison := []byte("[core]\n\thooksPath = " + hooks + "\n[credential]\n\thelper = !printf leaked > '" + marker + "'; exit 1\n[fixture]\n\tidentity = host\n[commit]\n\tgpgSign = true\n")
	for _, file := range []string{filepath.Join(host, ".gitconfig"), filepath.Join(host, "git", "config"), filepath.Join(host, "system")} {
		if err := os.WriteFile(file, poison, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{
		"HOME": host, "XDG_CONFIG_HOME": host,
		"GIT_CONFIG_GLOBAL": filepath.Join(host, ".gitconfig"),
		"GIT_CONFIG_SYSTEM": filepath.Join(host, "system"),
		"GIT_TEMPLATE_DIR":  template, "GIT_DIR": filepath.Join(host, "not-a-repository"),
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "fixture.identity", "GIT_CONFIG_VALUE_0": "inherited-inline",
	} {
		t.Setenv(key, value)
	}
	before := os.Environ()
	fixtures := []*workspaceFixture{newWorkspaceFixture(t), newWorkspaceFixture(t)}
	if !reflect.DeepEqual(before, os.Environ()) {
		t.Fatal("fixture construction changed the process environment")
	}
	for i, f := range fixtures {
		identity := []string{"first", "second"}[i]
		f.git(t, f.source, "config", "--global", "fixture.identity", identity)
		f.git(t, f.source, "config", "--global", "credential.helper", "!printf 'username="+identity+"\\npassword=offline\\n'")
	}
	results := make(chan error, len(fixtures))
	for _, f := range fixtures {
		go func() { _, err := f.ensure(true); results <- err }()
	}
	for range fixtures {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	for i, f := range fixtures {
		want := []string{"first", "second"}[i]
		got, err := f.run(context.Background(), f.member(0), nil, "git", "config", "fixture.identity")
		if err != nil || string(got) != want {
			t.Fatal("preparation inherited another home's configuration", string(got), err)
		}
		if f.git(t, f.source, "config", "fixture.identity") != want {
			t.Fatal("source query used another home's configuration")
		}
		cmd := exec.CommandContext(t.Context(), "git", "credential", "fill")
		cmd.Dir, cmd.Env = f.member(0), preparationEnvironment(f.environment(nil))
		cmd.Stdin = strings.NewReader("protocol=https\nhost=gitlab.invalid\n\n")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "username="+want+"\n") {
			t.Fatal("credential helper escaped private home", string(out), err)
		}
		for _, dir := range []string{f.source, f.member(0), f.member(1)} {
			if _, err := os.Stat(filepath.Join(dir, ".git", "host-template")); !os.IsNotExist(err) {
				t.Fatal("inherited host Git template", err)
			}
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("executed host helper or hook", err)
	}
	for _, file := range []string{filepath.Join(host, ".gitconfig"), filepath.Join(host, "git", "config"), filepath.Join(host, "system")} {
		if got, err := os.ReadFile(file); err != nil || string(got) != string(poison) {
			t.Fatal("wrote host configuration", file, err)
		}
	}
	if !reflect.DeepEqual(before, os.Environ()) {
		t.Fatal("preparation changed the process environment")
	}
}
