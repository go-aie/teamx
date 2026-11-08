//go:build integration || e2e

package scion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestScionPATLoginDiscoveryAndIsolation(t *testing.T) {
	t.Setenv("SCION_TOKEN", "must-not-inherit")
	t.Setenv("SCION_CONFIG_FILE", "global-config")
	t.Setenv("MODEL_API_KEY", "must-not-inherit")
	for _, name := range []string{"scn", "teamx-scion"} {
		t.Run(name, func(t *testing.T) {
			exe := authCLI(t, name)
			root := t.TempDir()
			id := strings.Repeat("a", 32)
			empty, err := Discover(context.Background(), root, origin, exe)
			if err != nil || len(empty.Profiles) != 0 || empty.Executable != exe {
				t.Fatal(empty, err)
			}
			p, v, err := Login(context.Background(), root, origin, exe, id, platform.NewSecret("private-pat-alice"))
			if err != nil || v.Username != "alice" || v.Account.ID.Value != "alice" || p.Profile != id {
				t.Fatal(v, err)
			}
			found, err := Discover(context.Background(), root, origin, exe)
			b, _ := json.Marshal(found)
			if err != nil || len(found.Profiles) != 1 || found.Profiles[0].Username != "alice" || strings.Contains(string(b), "private-pat") || strings.Contains(string(b), "credentials.json") {
				t.Fatal(found, err)
			}
			path := filepath.Join(root, "scion-profiles", id, "credentials.json")
			info, e := os.Stat(path)
			if e != nil || info.Mode().Perm() != 0600 {
				t.Fatal("private credential permissions")
			}
			before, _ := os.ReadFile(path)
			if _, _, err := Login(context.Background(), root, origin, exe, id, platform.NewSecret("different-pat")); err == nil {
				t.Fatal("overwrote existing profile")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("existing credentials changed")
			}
			argv, _ := os.ReadFile(filepath.Join(filepath.Dir(exe), "argv"))
			if strings.Contains(string(argv), "private-pat") {
				t.Fatal("PAT in argv")
			}
		})
	}
}

func TestScionPATFailuresAndCancelledUnpublishedProfile(t *testing.T) {
	for _, mode := range []string{"reject", "mismatch", "version", "block"} {
		t.Run(mode, func(t *testing.T) {
			exe := authCLI(t, "scn")
			root := t.TempDir()
			id := strings.Repeat("b", 32)
			os.WriteFile(filepath.Join(filepath.Dir(exe), "mode"), []byte(mode), 0600)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, err := Login(ctx, root, origin, exe, id, platform.NewSecret("private-pat-alice"))
				done <- err
			}()
			if mode == "block" {
				started := filepath.Join(filepath.Dir(exe), "started")
				deadline := time.Now().Add(4 * time.Second)
				for {
					if _, e := os.Stat(started); e == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("login not started")
					}
					time.Sleep(5 * time.Millisecond)
				}
				found, err := Discover(context.Background(), root, origin, exe)
				if err != nil || len(found.Profiles) != 0 {
					t.Fatal("unfinished profile was discoverable", found, err)
				}
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || strings.Contains(err.Error(), "private-pat") {
					t.Fatal("unsafe login result", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("login not settled")
			}
			if _, err := os.Stat(filepath.Join(root, "scion-profiles", id)); !os.IsNotExist(err) {
				t.Fatal("failed profile not removed")
			}
		})
	}
}

func TestScionDiscoveryRejectsUnsafeRootAndExplicitExecutable(t *testing.T) {
	exe := authCLI(t, "scn")
	for _, mode := range []string{"symlink", "permissions"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "scion-profiles")
			if mode == "symlink" {
				os.Symlink(t.TempDir(), path)
			} else {
				os.Mkdir(path, 0755)
			}
			if _, err := Discover(context.Background(), root, origin, exe); err == nil {
				t.Fatal("unsafe root accepted")
			}
			if _, _, err := Login(context.Background(), root, origin, exe, strings.Repeat("a", 32), platform.NewSecret("private-pat-alice")); err == nil {
				t.Fatal("login accepted unsafe root")
			}
		})
	}
	if _, err := ResolveCLI(context.Background(), "scn"); err == nil {
		t.Fatal("relative explicit path accepted")
	}
	t.Setenv("TEAMX_SCION_BIN", "/missing/explicit-wrapper")
	t.Setenv("TEAMX_SCN_BIN", exe)
	if _, err := ResolveCLI(context.Background(), ""); err == nil {
		t.Fatal("explicit wrapper override fell back")
	}
}

func TestScionAutomaticCLIResolution(t *testing.T) {
	exe := authCLI(t, "scn")
	t.Setenv("TEAMX_SCION_BIN", exe)
	resolved, err := ResolveCLI(context.Background(), "")
	if err != nil || resolved != exe {
		t.Fatal("configured CLI not discovered", resolved, err)
	}
	t.Setenv("TEAMX_SCION_BIN", "")
	t.Setenv("TEAMX_SCN_BIN", exe)
	t.Setenv("PATH", "/usr/bin:/bin")
	resolved, err = ResolveCLI(context.Background(), "")
	if err != nil || resolved != exe {
		t.Fatal("native fallback not discovered", resolved, err)
	}
}
