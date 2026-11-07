//go:build e2e

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Read-only prerequisite. A passed probe is NOT the feature acceptance matrix.
// Never create fixtures until the target account/project has been verified.
func TestRealReadProbe(t *testing.T) {
	if os.Getenv("TEAMX_GITLAB_REAL") != "1" {
		t.Skip("set TEAMX_GITLAB_REAL=1 for the explicit real-instance probe")
	}
	repo, host := os.Getenv("TEAMX_GITLAB_TEST_REPO"), os.Getenv("GITLAB_HOST")
	file := os.Getenv("TEAMX_GITLAB_PROBE_EVIDENCE")
	if repo == "" || host == "" || os.Getenv("GITLAB_TOKEN") == "" || !filepath.IsAbs(file) {
		t.Fatal("explicit repo, host, token and absolute evidence file are required")
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a := &app{in: strings.NewReader(""), out: out, errOut: errOut, getenv: os.Getenv}
	args := []string{"api", "--hostname", host, "projects/" + url.PathEscape(repo)}
	code := a.run(ctx, args)
	sum := sha256.Sum256(out.Bytes())
	message := errOut.String()
	for _, key := range []string{"GITLAB_TOKEN"} {
		if v := os.Getenv(key); v != "" {
			message = strings.ReplaceAll(message, v, "[REDACTED]")
		}
	}
	status := "blocked"
	if code == 0 {
		status = "read_probe_passed_only"
	}
	evidence := map[string]any{"time": time.Now().UTC().Format(time.RFC3339), "target": "https://" + host + "/" + repo, "status": status, "command": args, "exit_code": code, "stderr": message, "stdout_sha256": hex.EncodeToString(sum[:]), "created_objects": []string{}, "feature_acceptance": "not_run"}
	if err := json.NewEncoder(f).Encode(evidence); err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("read probe blocked (exit %d); evidence: %s", code, file)
	}
}
