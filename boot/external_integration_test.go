//go:build integration || e2e

package boot_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExternalModuleLocalToolThroughPublicStartup(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the offline external module fixture")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), python, filepath.Join(root, "testdata/external-tool/acceptance.py"), "--teamx-root", root, "--output", filepath.Join(t.TempDir(), "external"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("external module acceptance: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}

func TestExternalModuleNotificationThroughPublicStartup(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the offline external module fixture")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), python, filepath.Join(root, "testdata/external-notification/acceptance.py"), "--teamx-root", root, "--output", filepath.Join(t.TempDir(), "external"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("external notification module acceptance: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}
