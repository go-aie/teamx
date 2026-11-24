//go:build integration || e2e

package lark

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

// This uses the real Linux CLI/keychain backend with synthetic app secrets.
// Network is unnecessary; real user OAuth/refresh/DPoP acceptance is separate.
func TestLinuxNativeLarkCredentialStorage(t *testing.T) {
	bin := os.Getenv("TEAMX_LARK_STORAGE_TEST_BIN")
	if runtime.GOOS != "linux" || bin == "" {
		t.Skip("set TEAMX_LARK_STORAGE_TEST_BIN to lark-cli 1.0.99 on Linux")
	}
	t.Setenv("HOME", t.TempDir())
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
			t.Fatal("native synthetic application configuration failed", err)
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
	if err != nil || len(entries) != 0 {
		t.Fatal("native CLI wrote outside its context", err)
	}
}
