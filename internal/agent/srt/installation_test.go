package srt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledRuntimePinsPublishedReleaseAndHostHelpers(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(base, "release")
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"runtime/node/bin/node", "bin/dsh", "bin/teamx", "runtime/dsh/package.json"} {
		write(filepath.Join(release, file), []byte(file))
	}
	write(filepath.Join(release, "runtime/srt/package-lock.json"), dependencyLock)
	c := installedContract{Schema: 1, Target: "linux-amd64", Profile: "standard", Version: Version, LockSHA256: digest(dependencyLock)}
	for _, name := range []string{"bwrap", "socat", "rg"} {
		path := filepath.Join(base, "host", name)
		write(path, []byte(name))
		c.Tools = append(c.Tools, struct {
			Path    string   `json:"path"`
			SHA256  string   `json:"binary_sha256"`
			Args    []string `json:"version_args"`
			Version string   `json:"version_contains"`
		}{Path: path, SHA256: digest([]byte(name))})
	}
	// Ubuntu exposes socat as a package-owned alias of socat1. Pin the
	// actual program, so later alias changes cannot alter execution.
	socatPhysical := filepath.Join(base, "host", "socat1")
	if err := os.Rename(c.Tools[1].Path, socatPhysical); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socat1", c.Tools[1].Path); err != nil {
		t.Fatal(err)
	}
	contract := filepath.Join(release, "share/teamx/srt-runtime.json")
	save := func() { data, _ := json.Marshal(c); write(contract, data) }
	save()
	hostCalls := 0
	host := func(*installedContract) ([]string, error) { hostCalls++; return nil, nil }
	config, err := installedConfig(release, []string{filepath.Join(base, "private")}, []string{"http://127.0.0.1:8088", "https://model.example"}, host)
	if err != nil {
		t.Fatal(err)
	}
	if config.Runtime.Path != filepath.Join(release, "runtime/srt") || config.Node.SHA256 == "" || len(config.ReadOnly) != 2 || hostCalls != 1 || config.Socat.Path != socatPhysical {
		t.Fatal(config, hostCalls)
	}
	// Bad deployment authority is rejected before executing any host helper.
	if _, err := installedConfig(release, config.PrivateRoots, []string{"https://model.example/v1?token=private"}, host); err == nil || hostCalls != 1 {
		t.Fatal("invalid origin reached host", err)
	}
	write(c.Tools[0].Path, []byte("replacement"))
	if _, err := installedConfig(release, config.PrivateRoots, config.Endpoints, host); err == nil {
		t.Fatal("changed helper accepted")
	}
	write(c.Tools[0].Path, []byte("bwrap"))
	c.LockSHA256 = "changed"
	save()
	if _, err := installedConfig(release, config.PrivateRoots, config.Endpoints, host); err == nil {
		t.Fatal("different execution lock accepted")
	}
	c.LockSHA256 = digest(dependencyLock)
	save()
	if err := os.Remove(c.Tools[1].Path); err != nil {
		t.Fatal(err)
	}
	if _, err := installedConfig(release, config.PrivateRoots, config.Endpoints, host); err == nil {
		t.Fatal("missing helper accepted")
	}
}

func TestInstalledHostSelectsIndependentDistributionTools(t *testing.T) {
	data, err := os.ReadFile("../../../packaging/srt/runtime.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var original installedContract
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id, version, glibc, bwrap string
		valid                     bool
	}{
		{"ubuntu", "24.04", "glibc 2.39", "bubblewrap 0.9.0", true},
		{"ubuntu", "22.04", "glibc 2.35", "bubblewrap 0.6.1", true},
		{"ubuntu", "22.04", "glibc 2.39", "", false},
		{"ubuntu", "20.04", "glibc 2.31", "", false},
		{"debian", "12", "glibc 2.36", "", false},
	} {
		t.Run(test.id+test.version+test.glibc, func(t *testing.T) {
			c := original
			err := selectInstalledHost(&c, test.id, test.version, test.glibc)
			if (err == nil) != test.valid {
				t.Fatal(err)
			}
			if test.valid && (c.Tools[0].Version != test.bwrap || c.Tools[0].SHA256 == "") {
				t.Fatal("wrong host tools selected", c.Tools)
			}
		})
	}
}
