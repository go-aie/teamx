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
	team := testTeamDefinition(t)
	team.Runtime = nil
	raw, err := json.Marshal(team)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "share/teamx/teams/example/team.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestBundleDefaultHomePreservesExistingPortableData(t *testing.T) {
	for _, mode := range []string{"fresh", "portable", "explicit", "broken-portable-link"} {
		t.Run(mode, func(t *testing.T) {
			userHome := t.TempDir()
			t.Setenv("HOME", userHome)
			root := bundleTestRoot(t)
			personal := filepath.Join(userHome, ".teamx")
			legacy := filepath.Join(root, ".teamx")
			want := personal
			args := []string{"--bundle-root", root}
			if mode != "fresh" {
				// An unrelated personal config must not affect an old or explicit home.
				if err := os.Mkdir(personal, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(personal, "config.toml"), []byte("unrelated personal config"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "portable" || mode == "explicit" {
				if err := os.Mkdir(legacy, 0700); err != nil {
					t.Fatal(err)
				}
				if err := initializeBundle(root, legacy); err != nil {
					t.Fatal(err)
				}
				want = legacy
			}
			if mode == "explicit" {
				want = filepath.Join(t.TempDir(), "custom home")
				args = append(args, "--home", want)
			}
			if mode == "broken-portable-link" {
				if err := os.Symlink(filepath.Join(root, "missing"), legacy); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			err := runBundle("init", args, &output, &output)
			if mode == "broken-portable-link" {
				if err == nil {
					t.Fatal("broken old home silently replaced")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				c, err := readStartupConfig(filepath.Join(want, "config.toml"), true, false)
				if err != nil {
					t.Fatal(err)
				}
				if *c.Server.DataDir != filepath.Join(want, "data") || *c.Server.TeamsDir != filepath.Join(want, "teams") {
					t.Fatal("data escaped selected home")
				}
			}
			if mode == "fresh" {
				if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
					t.Fatal("fresh bundle created local data", err)
				}
			} else {
				data, err := os.ReadFile(filepath.Join(personal, "config.toml"))
				if err != nil || string(data) != "unrelated personal config" {
					t.Fatal("unrelated personal home changed", err)
				}
			}
		})
	}
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
		if err := runBundle("init", args, &output, &output); err == nil {
			t.Fatal("invalid user config must be reported without replacement")
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
	t.Setenv("HOME", "") // An explicit doctor config does not need a default home.
	var output bytes.Buffer
	if err := runBundle("doctor", []string{"--bundle-root", root, "--config", filepath.Join(home, "config.toml")}, &output, &output); err == nil {
		t.Fatal("unconfigured model was accepted")
	}
	for _, field := range []string{"网页完成验证", "node", "dsh", "git", "scion"} {
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

func unsetBundleCredential(t *testing.T, name string) {
	t.Helper()
	old, exists := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if exists {
			_ = os.Setenv(name, old)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func TestBundleRefreshesDefaultsWithoutRestoringRemovedUserTemplates(t *testing.T) {
	root := bundleTestRoot(t)
	home := t.TempDir()
	if err := initializeBundle(root, home); err != nil {
		t.Fatal(err)
	}
	role := filepath.Join(home, "teams/example/prompts/role.md")
	if err := os.Remove(role); err != nil {
		t.Fatal(err)
	}
	mustWriteResetFile(t, filepath.Join(root, "share/teamx/teams/example/prompts/role.md"), "next version")
	if err := initializeBundle(root, home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(role); !os.IsNotExist(err) {
		t.Fatal("removed user template restored", err)
	}
	if got := templateContents(t, filepath.Join(teamDefaultsPath(filepath.Join(home, "config.toml")), "example/prompts/role.md")); got != "next version" {
		t.Fatal(got)
	}
}
