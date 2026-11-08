//go:build e2e && unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPinnedScnOffline(t *testing.T) {
	bin := os.Getenv("TEAMX_SCN_BIN")
	if bin == "" {
		t.Skip("set TEAMX_SCN_BIN to the verified scn 0.1.13 release")
	}
	for _, args := range [][]string{
		{"--version"}, {"--help"}, {"help", "review"}, {"--json", "review", "show", "--help"},
		{"login", "--help"}, {"review", "create", "--help"}, {"review", "comment", "--help"}, {"land", "--help"}, {"review", "land", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			a, out, stderr := testApp(bin)
			// Invalid profile/global config and unreachable endpoint must not be
			// needed by native help. A token must never reach native help output.
			a.environ = append(a.environ, "SCION_TOKEN=must-not-leak", "SCION_CREDENTIALS_FILE=/nonexistent/scion/credentials.json", "SCION_CONFIG_FILE=/nonexistent/config.json", "SCION_ENDPOINT=http://127.0.0.1:1", "SCION_HUB=http://127.0.0.1:1")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if code := a.run(ctx, args); code != 0 || out.Len() == 0 || strings.Contains(out.String()+stderr.String(), "must-not-leak") {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
		})
	}
	t.Run("usage-error", func(t *testing.T) {
		a, out, stderr := testApp(bin)
		if code := a.run(context.Background(), []string{"review", "nonexistent-command"}); code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "unrecognized subcommand") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("relative-C-and-native-JSON", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "subdir"), 0700); err != nil {
			t.Fatal(err)
		}
		a, out, stderr := testApp(bin)
		if code := a.run(context.Background(), []string{"-C", dir, "config", "set", "user.name", "Hidden operator"}); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
		// Config written to the temporary HOME above cannot affect the next call.
		out.Reset()
		stderr.Reset()
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(cwd, filepath.Join(dir, "subdir"))
		if err != nil {
			t.Fatal(err)
		}
		if code := a.run(context.Background(), []string{"-C", relative, "--json", "config", "list"}); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
		var got struct {
			Schema string         `json:"schema"`
			Values map[string]any `json:"values"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Schema != "scion.cli.config/v1" || len(got.Values) != 0 {
			t.Fatalf("native JSON changed or global config leaked: %q (%v)", out, err)
		}
	})
}
