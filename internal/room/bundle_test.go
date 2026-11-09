package room

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func bundleTestRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "bundle ' with spaces")
	dir := filepath.Join(root, "share/teamx/teams/example/prompts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "role.md"), []byte("original role"), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestBundleInitPreservesUserFilesAndSerializesPaths(t *testing.T) {
	root := bundleTestRoot(t)
	for _, home := range []string{filepath.Join(root, ".teamx"), filepath.Join(t.TempDir(), "home ' quoted")} {
		var output bytes.Buffer
		args := []string{"--bundle-root", root, "--home", home, "--non-interactive"}
		if err := runBundle("init", args, &output, &output); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, "config.toml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var c startupConfig
		if err := toml.Unmarshal(data, &c); err != nil {
			t.Fatal(err)
		}
		if c.Runtime.Model.ModelID != "" || c.Runtime.Model.BaseURL != "" {
			t.Fatal("init must not invent model configuration")
		}
		if got := configPath(home, c.Runtime.DSH.BinaryPath); got != filepath.Join(root, "bin/dsh") {
			t.Fatal(got)
		}
		template := filepath.Join(home, "teams/example/prompts/role.md")
		for _, p := range []string{path, template} {
			if err := os.WriteFile(p, []byte("user content"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := runBundle("init", args, &output, &output); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{path, template} {
			b, _ := os.ReadFile(p)
			if string(b) != "user content" {
				t.Fatalf("overwrote %s", p)
			}
		}
	}
}

func TestBundleInitLockAndAtomicPublication(t *testing.T) {
	root := bundleTestRoot(t)
	home := t.TempDir()
	lock, err := lockBundleHome(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := runBundle("init", []string{"--bundle-root", root, "--home", home}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("concurrent home owner accepted")
	}
	if err := unlockDirectory(lock); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "atomic")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := installMissing(path, bytes.Repeat([]byte("a"), 4096)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	b, err := os.ReadFile(path)
	if err != nil || len(b) != 4096 {
		t.Fatal("partial initialization", err)
	}
}

func TestBundleDoctorListsExactMissingFieldsWithoutSecrets(t *testing.T) {
	root := bundleTestRoot(t)
	home := t.TempDir()
	if err := initializeBundle(root, home); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "private-key-do-not-print")
	var output bytes.Buffer
	if err := bundleDoctor(root, filepath.Join(home, "config.toml"), &output); err == nil {
		t.Fatal("unconfigured model was accepted")
	}
	for _, field := range []string{"runtime.model.base_url", "runtime.model.model_id", "node", "dsh", "git", "scion"} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("missing %s", field)
		}
	}
	if strings.Contains(output.String(), "private-key-do-not-print") {
		t.Fatal("credential leaked")
	}
}

func TestBundleDoctorProfileDependencies(t *testing.T) {
	for _, profile := range []string{"core", "standard", "full"} {
		t.Run(profile, func(t *testing.T) {
			root := bundleTestRoot(t)
			home := t.TempDir()
			if err := initializeBundle(root, home); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", t.TempDir())
			dependencies := map[string]map[string]string{
				"node": {"expected_version": "node-fixture"},
				"dsh":  {"expected_version": "dsh-fixture"},
			}
			if profile != "core" {
				dependencies["glab"] = map[string]string{"expected_version": "glab-fixture"}
				dependencies["lark-cli"] = map[string]string{"expected_version": "lark-fixture"}
			}
			if profile == "full" {
				dependencies["git"] = map[string]string{"expected_version": "git-fixture"}
			}
			writeManifest := func() {
				t.Helper()
				b, err := json.Marshal(map[string]any{"profile": profile, "dependencies": dependencies})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "manifest.json"), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeManifest()
			inspect := func() map[string]doctorCheck {
				t.Helper()
				var output bytes.Buffer
				// This fixture intentionally has no configured model or executables.
				_ = bundleDoctor(root, filepath.Join(home, "config.toml"), &output)
				var report struct{ Checks []doctorCheck }
				if err := json.Unmarshal(output.Bytes(), &report); err != nil {
					t.Fatal(err)
				}
				checks := make(map[string]doctorCheck)
				for _, check := range report.Checks {
					checks[check.Name] = check
				}
				return checks
			}
			checks := inspect()
			if _, invalid := checks["manifest"]; invalid {
				t.Fatal("valid profile manifest rejected")
			}
			for _, name := range []string{"glab", "lark-cli", "git"} {
				_, required := dependencies[name]
				if checks[name].Required != required || checks[name].Status != "missing" {
					t.Fatalf("unexpected %s check: %+v", name, checks[name])
				}
			}
			if profile == "standard" {
				delete(dependencies, "glab")
				writeManifest()
				if _, invalid := inspect()["manifest"]; !invalid {
					t.Fatal("standard manifest silently omitted glab")
				}
			}
		})
	}
}

func TestBundleInitRefusesTemplateDestinationSymlink(t *testing.T) {
	root := bundleTestRoot(t)
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "teams")); err != nil {
		t.Fatal(err)
	}
	if err := initializeBundle(root, home); err == nil {
		t.Fatal("followed a template destination symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("wrote outside the selected home", err)
	}
}

func TestDistributionVersionWithoutConfiguration(t *testing.T) {
	var output bytes.Buffer
	if err := Run([]string{"--version"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.String(), "teamx dev commit=unknown dirty=unknown") {
		t.Fatal(output.String())
	}
}

func TestBundleHomeLockAllowsDataInHome(t *testing.T) {
	home := t.TempDir()
	bundle, err := lockBundleHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockDirectory(bundle)
	data, err := lockDirectory(home)
	if err != nil {
		t.Fatal("home lock conflicts with valid data_dir home", err)
	}
	defer unlockDirectory(data)
}
