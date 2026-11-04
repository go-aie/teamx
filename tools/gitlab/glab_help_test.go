package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Offline integration with the pinned release binary. Ordinary tests use fake
// glab; explicitly provide this binary to run the real help coverage gate.
func TestPinnedGlabOfflineHelp(t *testing.T) {
	bin := os.Getenv("TEAMX_GLAB_BIN")
	if bin == "" {
		t.Skip("set TEAMX_GLAB_BIN to glab v" + glabVersion)
	}
	paths := []string{"", "api", "repo view", "mr view", "mr diff", "mr create", "mr close", "mr merge", "mr note", "mr note list", "mr note create", "issue", "issue view", "issue note", "issue update", "issue close", "issue delete", "todo", "todo list", "todo done", "work-items", "work-items create", "work-items update", "work-items delete", "work-items view", "work-items close", "work-items assignees", "work-items note", "work-items discussions", "work-items discussions list", "issue discussions", "issue discussions list", "issue discussions reply"}
	type result struct {
		Command  string `json:"command"`
		ExitCode int    `json:"exit_code"`
		SHA256   string `json:"stdout_sha256"`
		Stderr   string `json:"stderr"`
	}
	results := []result{}
	for _, path := range paths {
		a, out, errOut := testApp(t, nil)
		a.getenv = func(k string) string {
			if k == "TEAMX_GLAB_BIN" {
				return bin
			}
			t.Fatalf("help resolved %s", k)
			return ""
		}
		args := append(strings.Fields(path), "--help")
		code := a.run(context.Background(), args)
		sum := sha256.Sum256(out.Bytes())
		results = append(results, result{usagePrefix + " " + strings.Join(args, " "), code, hex.EncodeToString(sum[:]), errOut.String()})
		if code != 0 || out.Len() == 0 {
			t.Errorf("%s: code %d %s", path, code, errOut)
		}
	}
	if path := os.Getenv("TEAMX_GITLAB_HELP_EVIDENCE"); path != "" {
		if !filepath.IsAbs(path) {
			t.Fatal("TEAMX_GITLAB_HELP_EVIDENCE must be absolute")
		}
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
