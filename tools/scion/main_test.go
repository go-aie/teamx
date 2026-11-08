//go:build unix

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeScn(t *testing.T, version, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scn")
	script := "#!/bin/sh\nif [ \"$1\" = '--version' ]; then\n" + version + "\nfi\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

const pinnedVersionScript = "printf 'scn 0.1.13\\n'; exit 0"

func testApp(bin string) (*app, *bytes.Buffer, *bytes.Buffer) {
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	return &app{in: strings.NewReader(""), out: out, errOut: stderr,
		environ: []string{"TEAMX_SCN_BIN=" + bin, "PATH=/usr/bin:/bin"}, executable: os.Executable}, out, stderr
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func parseEnv(s string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

func waitForFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) != 0 {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("child did not become ready")
	return nil
}

func assertGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d survived settlement", pid)
}

func TestOnlyStandardLibraryDependencies(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "github.com/go-aie/teamx/tools/scion" {
		t.Fatalf("standalone tool imported nonstandard dependencies: %s", out)
	}
}
