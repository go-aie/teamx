//go:build darwin || linux

package room

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/agent/srt"
)

func TestServiceExecutionDescriptorKeepsReleaseAndPrivatePaths(t *testing.T) {
	home := t.TempDir()
	r := serviceRegistration{Home: home, Root: filepath.Join(t.TempDir(), "server"), UID: os.Getuid(), GID: os.Getgid(), Execution: "srt"}
	serviceTestFile(t, filepath.Join(home, "config.toml"), "[server]\nlisten='127.0.0.1:8088'\n[deployment]\nmode='personal'\n[env]\nKEEP='value'\n", 0600)
	marker := filepath.Join(home, "data/retained-session")
	serviceTestFile(t, marker, "session-and-generation", 0600)
	for _, name := range []string{"original", "upgrade"} {
		release := filepath.Join(r.Root, "releases", name)
		config := srt.Config{Node: srt.Artifact{Path: filepath.Join(release, "runtime/node/bin/node"), SHA256: strings.Repeat("a", 64)}, PrivateRoots: []string{home}, Endpoints: []string{"http://127.0.0.1:8088"}}
		if err := writeServiceExecutionConfig(r, release, config); err != nil {
			t.Fatal(err)
		}
		manifest := filepath.Join(home, "execution", name+".json")
		got, err := srt.ReadConfig(manifest)
		if err != nil || got.Node != config.Node || got.PrivateRoots[0] != home {
			t.Fatal(got, err)
		}
		c, err := readStartupConfig(filepath.Join(home, "config.toml"), true, false)
		if err != nil || c.Deployment.Execution.Kind != "srt" || c.Deployment.Execution.Manifest != manifest || c.Env["KEEP"] != "value" {
			t.Fatal(c, err)
		}
		info, _ := os.Stat(manifest)
		if info.Mode().Perm() != 0600 {
			t.Fatal(info.Mode())
		}
	}
	if data, _ := os.ReadFile(marker); string(data) != "session-and-generation" {
		t.Fatal("Session evidence changed")
	}
	if _, err := os.Stat(filepath.Join(home, "execution/original.json")); err != nil {
		t.Fatal("old descriptor removed", err)
	}
}

func TestServiceExecutionNativeDoesNotInspectSRT(t *testing.T) {
	if err := configureServiceExecution(serviceRegistration{Home: "/nonexistent", Execution: "native"}, "/missing-release"); err != nil {
		t.Fatal(err)
	}
	if err := configureServiceExecution(serviceRegistration{Home: "/nonexistent"}, "/missing-release"); err != nil {
		t.Fatal(err)
	}
	if err := validateServiceExecutionSelection("native", []string{"https://example.com"}); err == nil {
		t.Fatal("native accepted SRT authority")
	}
	if err := configureServiceExecution(serviceRegistration{Execution: "srt", Platform: "linux", UID: 0}, "/missing-release"); err == nil {
		t.Fatal("root accepted")
	}
}

func TestServiceExecutionUnitHardeningOnlyWhenSelected(t *testing.T) {
	r := serviceRegistration{Platform: "linux", Root: "/opt/teamx", Home: "/var/lib/teamx", UserHome: "/home/teamx", User: "teamx", UID: 1001, GID: 1001}
	native, err := serviceTemplate(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(native), "NoNewPrivileges=") {
		t.Fatal("native execution changed")
	}
	r.Execution = "srt"
	isolated, err := serviceTemplate(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"NoNewPrivileges=yes", "CapabilityBoundingSet=\n", "AmbientCapabilities=\n", "User=1001"} {
		if !strings.Contains(string(isolated), setting) {
			t.Fatal("missing", setting)
		}
	}
}

func TestManagedExecutionRejectsNativeFallbackAndStaleRelease(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	release := filepath.Join(root, "releases", "selected")
	layout := serverLayout{Root: root, Home: home, Release: release}
	registration := serviceRegistration{Execution: "srt", SRTEndpoints: []string{"http://127.0.0.1:8088"}}
	if err := writeServiceJSON(filepath.Join(root, "service.json"), registration); err != nil {
		t.Fatal(err)
	}
	c := startupConfig{}
	if err := checkManagedExecution(c, layout); err == nil {
		t.Fatal("selected SRT fell back to native")
	}
	c.Deployment.Execution.Kind = "srt"
	c.Deployment.Execution.Manifest = filepath.Join(home, "execution", "selected.json")
	if err := os.MkdirAll(filepath.Dir(c.Deployment.Execution.Manifest), 0700); err != nil {
		t.Fatal(err)
	}
	descriptor := srt.Config{Node: srt.Artifact{Path: filepath.Join(release, "runtime/node/bin/node")}, Runtime: srt.Artifact{Path: filepath.Join(release, "runtime/srt")}, Endpoints: registration.SRTEndpoints, PrivateRoots: []string{home}}
	if err := writeServiceJSON(c.Deployment.Execution.Manifest, descriptor); err != nil {
		t.Fatal(err)
	}
	if err := checkManagedExecution(c, layout); err != nil {
		t.Fatal(err)
	}
	descriptor.Node.Path = filepath.Join(root, "releases", "old", "runtime/node/bin/node")
	if err := writeServiceJSON(c.Deployment.Execution.Manifest, descriptor); err != nil {
		t.Fatal(err)
	}
	if err := checkManagedExecution(c, layout); err == nil {
		t.Fatal("stale release accepted")
	}
	descriptor.Node.Path = filepath.Join(release, "runtime/node/bin/node")
	descriptor.Endpoints = []string{"https://extra.example"}
	if err := writeServiceJSON(c.Deployment.Execution.Manifest, descriptor); err != nil {
		t.Fatal(err)
	}
	if err := checkManagedExecution(c, layout); err == nil {
		t.Fatal("expanded endpoint authority accepted")
	}
}

func TestServiceExecutionRejectsUnsafeDescriptorDirectory(t *testing.T) {
	for _, alias := range []bool{false, true} {
		home := t.TempDir()
		external := t.TempDir()
		serviceTestFile(t, filepath.Join(home, "config.toml"), "[server]\nlisten='127.0.0.1:8088'\n", 0600)
		dir := filepath.Join(home, "execution")
		if alias {
			if err := os.Symlink(external, dir); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		r := serviceRegistration{Home: home, UID: os.Getuid(), GID: os.Getgid()}
		if err := writeServiceExecutionConfig(r, "/opt/teamx/releases/example", srt.Config{}); err == nil {
			t.Fatal("unsafe directory accepted")
		}
		entries, err := os.ReadDir(external)
		if err != nil || len(entries) != 0 {
			t.Fatal(entries, err)
		}
		data, _ := os.ReadFile(filepath.Join(home, "config.toml"))
		if strings.Contains(string(data), "manifest") {
			t.Fatal("config changed after refusal")
		}
	}
}
