//go:build integration || e2e

package lark

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

// This uses the real Linux CLI/keychain backend with synthetic app secrets.
// Config init persists locally before its remote validation. Block that probe;
// real user OAuth/refresh/DPoP acceptance is separate.
func TestLinuxNativeLarkCredentialStorage(t *testing.T) {
	bin := os.Getenv("TEAMX_LARK_STORAGE_TEST_BIN")
	if runtime.GOOS != "linux" || bin == "" {
		t.Skip("set TEAMX_LARK_STORAGE_TEST_BIN to lark-cli 1.0.99 on Linux")
	}
	// Two physical releases of the accepted CLI, with no live credentials.
	payload, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	previousPayload := payload
	if previous := os.Getenv("TEAMX_LARK_STORAGE_PREVIOUS_BIN"); previous != "" {
		previousPayload, err = os.ReadFile(previous)
		if err != nil {
			t.Fatal(err)
		}
	}
	release := t.TempDir()
	oldBin, newBin := filepath.Join(release, "lark-a"), filepath.Join(release, "lark-b")
	for path, body := range map[string][]byte{oldBin: previousPayload, newBin: payload} {
		if err := os.WriteFile(path, body, 0700); err != nil {
			t.Fatal(err)
		}
	}
	bin = oldBin
	t.Setenv("HOME", t.TempDir())
	// Native config init probes the synthetic app after saving. Force an immediate
	// local connection refusal so this fixture cannot contact a real platform.
	for _, key := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "all_proxy"} {
		t.Setenv(key, "http://127.0.0.1:1")
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	factories := make([]*ClientFactory, 2)
	clients := make([]*Client, 2)
	keys := make([][32]byte, 2)
	for i := range factories {
		f, err := NewClientFactory(0, 0, 0).WithStorage(filepath.Join(t.TempDir(), "private-cli"), bin)
		if err != nil {
			t.Fatal(err)
		}
		p := platform.CLIProfile{Version: 1, Executable: bin, Profile: "fixture", Brand: "feishu", CLIVersion: CLIVersion}
		if err := f.storage.provision(p); err != nil {
			t.Fatal(err)
		}
		c, err := f.New(p)
		if err != nil {
			t.Fatal(err)
		}
		cmd := c.command(t.Context(), "", "config", "init", "--app-id", "cli_fixture", "--app-secret-stdin", "--brand", "feishu", "--name", p.Profile)
		cmd.Stdin = strings.NewReader("synthetic-app-secret-" + string(rune('a'+i)) + "\n")
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 3 {
				t.Fatal("native synthetic application configuration failed", err)
			}
			// A post-save probe may fail; the assertions below independently prove
			// that the CLI saved the correct config and encrypted native credentials.
		}
		data, err := c.run(t.Context(), "config", "show")
		var view struct{ Profile, AppID, Brand string }
		if err != nil || json.Unmarshal(data, &view) != nil || view.Profile != "fixture" || view.AppID != "cli_fixture" {
			t.Fatal("native config directory did not apply", err)
		}
		foundKey, encrypted := false, false
		err = filepath.WalkDir(c.dataDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0077 != 0 {
				t.Fatal("native private storage is accessible by other users")
			}
			if entry.IsDir() {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(body, []byte("synthetic-app-secret")) {
				t.Fatal("native credential persisted as plaintext")
			}
			if entry.Name() == "master.key" {
				foundKey = len(body) == 32
				keys[i] = sha256.Sum256(body)
			}
			if strings.HasSuffix(entry.Name(), ".enc") {
				encrypted = true
			}
			return nil
		})
		if err != nil || !foundKey || !encrypted {
			t.Fatal("native Linux keychain missing from selected data directory", err)
		}
		factories[i], clients[i] = f, c
	}
	if keys[0] == keys[1] {
		t.Fatal("spaces shared a native key")
	}
	if err := os.Remove(oldBin); err != nil {
		t.Fatal(err)
	}
	for i, original := range clients {
		metadata := filepath.Join(factories[i].storage.directory(original.profile.Profile), "profile.json")
		before, err := os.ReadFile(metadata)
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := factories[i].WithStorage(factories[i].storage.root, newBin)
		if err != nil {
			t.Fatal(err)
		}
		client, err := reopened.New(original.profile)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.authConfig(t.Context()); err != nil {
			t.Fatal("upgraded CLI cannot read original native storage", err)
		}
		discovery, err := reopened.Discover(t.Context(), "", "feishu")
		if err != nil || len(discovery.Profiles) != 1 {
			t.Fatal("upgraded native discovery failed", err)
		}
		after, err := os.ReadFile(metadata)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("native upgrade rewrote profile", err)
		}
		if client.profile.Executable != oldBin || client.executable != newBin || client.dataDir != original.dataDir {
			t.Fatal("native upgrade changed credential reference")
		}
		factories[i], clients[i] = reopened, client
	}
	bin = newBin
	if err := factories[0].Logout(t.Context(), clients[0].profile); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewClientFactory(0, 0, 0).WithStorage(factories[1].storage.root, bin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.New(clients[1].profile); err != nil {
		t.Fatal(err)
	}
	// Nothing may fall back to the service account's normal CLI configuration.
	entries, err := os.ReadDir(os.Getenv("HOME"))
	if err == nil && os.Getenv("TEAMX_LARK_STORAGE_TEST_EMULATED") == "1" && len(entries) == 1 && entries[0].Name() == ".cache" && entries[0].IsDir() {
		// Rosetta creates these empty directories even for an isolated native
		// executable. Allow only this observed emulator scaffolding, never CLI
		// configuration, credentials or arbitrary cache files in the service home.
		cache := filepath.Join(os.Getenv("HOME"), ".cache")
		items, cacheErr := os.ReadDir(cache)
		if cacheErr == nil && len(items) == 1 && items[0].Name() == "rosetta" && items[0].IsDir() {
			translated, cacheErr := os.ReadDir(filepath.Join(cache, "rosetta"))
			if cacheErr == nil && len(translated) == 0 {
				t.Log("service home contains only empty Rosetta cache directories")
				entries = nil
			}
		}
	}
	if err != nil || len(entries) != 0 {
		t.Fatal("native CLI wrote outside its context", err)
	}
}
