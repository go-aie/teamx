package testutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstalledDSHProcess(t *testing.T) {
	if os.Getenv("TEAMX_INSTALLATION_TEST_HELPER") != "1" {
		return
	}
	separator := 0
	for separator < len(os.Args) && os.Args[separator] != "--" {
		separator++
	}
	if separator == len(os.Args) || !reflect.DeepEqual(os.Args[separator+1:], []string{"/install with spaces/cli.mjs", "$(literal)", "--version"}) {
		os.Exit(2)
	}
	fmt.Print(os.Getenv("TEAMX_INSTALLATION_TEST_VERSION"))
	os.Exit(0)
}

func installationFixture(t *testing.T) (string, []string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestInstalledDSHProcess$", "--", "/install with spaces/cli.mjs", "$(literal)"}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEAMX_DSH_BINARY_PATH", binary)
	t.Setenv("TEAMX_DSH_BINARY_ARGS", string(encoded))
	t.Setenv("TEAMX_INSTALLATION_TEST_HELPER", "1")
	t.Setenv("TEAMX_INSTALLATION_TEST_VERSION", "3.4.5\n")
	return binary, args
}

func TestInstalledDSHRecordsActualLauncherAndPreservesArguments(t *testing.T) {
	binary, args := installationFixture(t)
	installation, err := InstalledDSH(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if installation.BinaryPath != resolved || !reflect.DeepEqual(installation.BinaryArgs, args) || installation.Version != "3.4.5" || installation.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("incorrect installed launcher evidence: %+v", installation)
	}
}

func TestInstalledDSHFindsInstallationOnPath(t *testing.T) {
	binary, _ := installationFixture(t)
	dir := t.TempDir()
	if err := os.Symlink(binary, filepath.Join(dir, "dsh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("TEAMX_DSH_BINARY_PATH", "")
	if installation, err := InstalledDSH(context.Background()); err != nil || installation.Version != "3.4.5" {
		t.Fatal(installation, err)
	}
}

func TestInstalledDSHRejectsInvalidArgumentsAndVersionOutput(t *testing.T) {
	for _, tc := range []struct{ name, key, value, want string }{
		{"missing executable", "TEAMX_DSH_BINARY_PATH", "/missing/dsh", "installed DSH unavailable"},
		{"shell arguments", "TEAMX_DSH_BINARY_ARGS", "cli.mjs --flag", "JSON string array"},
		{"null arguments", "TEAMX_DSH_BINARY_ARGS", "null", "JSON string array"},
		{"non-string argument", "TEAMX_DSH_BINARY_ARGS", "[1]", "JSON string array"},
		{"empty version", "TEAMX_INSTALLATION_TEST_VERSION", "", "one nonempty version"},
		{"multiline version", "TEAMX_INSTALLATION_TEST_VERSION", "1.2.3\nextra", "one nonempty version"},
		{"oversized version", "TEAMX_INSTALLATION_TEST_VERSION", strings.Repeat("x", 1025), "--version failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installationFixture(t)
			t.Setenv(tc.key, tc.value)
			if _, err := InstalledDSH(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal("unexpected validation result", err)
			}
		})
	}
}
