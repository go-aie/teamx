package scion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

const origin = "https://scion.invalid"

func profileFixture(t *testing.T) (string, platform.CLIProfile, string) {
	t.Helper()
	root := t.TempDir()
	p := platform.CLIProfile{Version: 1, Brand: "scion", Profile: strings.Repeat("a", 32), CLIVersion: CLIVersion, Executable: filepath.Join(t.TempDir(), "teamx-scion")}
	dir := filepath.Join(root, "scion-profiles", p.Profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "credentials.json")
	data, _ := json.Marshal(map[string]string{origin: "alice", "https://other.invalid": "unrelated-secret"})
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env -S python3 -S
import os,sys,json
if sys.argv[1:]==['--version']:
 print('scn 0.1.13');sys.exit()
assert 'SCION_TOKEN' not in os.environ
assert os.environ['SCION_CONFIG_FILE'].startswith(os.path.dirname(os.environ['SCION_CREDENTIALS_FILE']))
assert os.environ['SCION_HUB']==os.environ['SCION_ENDPOINT']
credentials=json.load(open(os.environ['SCION_CREDENTIALS_FILE']))
assert len(credentials)==1
subject=credentials[os.environ['SCION_ENDPOINT']]
print(json.dumps({'schema':'scion.cli.whoami/v1','endpoint':os.environ['SCION_ENDPOINT'],'subject':subject}))
`
	if err := os.WriteFile(p.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return root, p, file
}

func TestProfileEvidenceAndSnapshotIdentity(t *testing.T) {
	t.Setenv("SCION_TOKEN", "must-not-inherit")
	t.Setenv("SCION_CONFIG_FILE", "must-not-inherit")
	root, p, file := profileFixture(t)
	v := VerifyProfile(context.Background(), root, origin, p)
	if v.Status != "valid" || v.UserID != 0 || v.Account.ID.Namespace != "scion:subject" || v.Account.ID.Value != "alice" {
		t.Fatal(v)
	}
	private := t.TempDir()
	values, err := Prepare(context.Background(), root, origin, p, v.Account, private)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte(`{"https://scion.invalid":"bob"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// The admitted invocation still uses the same bytes whose subject was checked.
	if got := VerifyFile(context.Background(), origin, p, values["credentials_file"]); got.Account != v.Account {
		t.Fatal(got)
	}
	if _, err = Prepare(context.Background(), root, origin, p, v.Account, t.TempDir()); err == nil || err.Error() != "identity_mismatch" {
		t.Fatal(err)
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if got := VerifyProfile(context.Background(), root, origin, p); got.ErrorCode != "credential_unavailable" {
		t.Fatal(got)
	}
}

func TestProfileRejectsPathsPermissionsAndAnonymous(t *testing.T) {
	for _, mode := range []string{"traversal", "version", "binary-version", "brand", "directory-mode", "file-mode", "symlink-file", "symlink-directory", "anonymous", "bad-json"} {
		t.Run(mode, func(t *testing.T) {
			root, p, file := profileFixture(t)
			switch mode {
			case "traversal":
				p.Profile = "../other"
			case "version":
				p.CLIVersion = "0.2.0"
			case "binary-version":
				if err := os.WriteFile(p.Executable, []byte("#!/bin/sh\nprintf 'scn 0.2.0\\n'\n"), 0700); err != nil {
					t.Fatal(err)
				}
			case "brand":
				p.Brand = "feishu"
			case "directory-mode":
				if err := os.Chmod(filepath.Dir(file), 0755); err != nil {
					t.Fatal(err)
				}
			case "file-mode":
				if err := os.Chmod(file, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink-file":
				if err := os.Rename(file, file+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(file+".real", file); err != nil {
					t.Fatal(err)
				}
			case "symlink-directory":
				dir := filepath.Dir(file)
				if err := os.Rename(dir, dir+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+".real", dir); err != nil {
					t.Fatal(err)
				}
			case "anonymous":
				if err := os.WriteFile(file, []byte(`{"https://scion.invalid":"anonymous"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "bad-json":
				if err := os.WriteFile(file, []byte(`secret-malformed`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			v := VerifyProfile(context.Background(), root, origin, p)
			if v.Status != "failed" || v.Account.Valid() || strings.Contains(v.ErrorCode, "secret") {
				t.Fatal(v)
			}
		})
	}
}

func TestDestinationAndOperatorArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--endpoint", "https://evil.invalid", "review", "ls"},
		{"review", "ls", "--hub=https://evil.invalid"},
		{"--repo=https://evil.invalid/repo", "review", "ls"},
		{"repo", "info", "https://evil.invalid/repo"},
		{"repo", "info", "--", "https://evil.invalid/repo"},
		{"clone", "http://scion.invalid/repo", "dir"},
		{"remote", "set-url", "origin", "https://evil.invalid/repo"},
		{"--json", "login", origin}, {"token", "ls"}, {"config", "set", "x", "y"}, {"daemon", "run"},
		{"self-update", "--version", "v0.1.13"}, {"-qC", "/tmp", "login", origin},
	} {
		if Help(args) || ValidateArguments(origin, args) == nil {
			t.Fatal("unsafe arguments accepted", args)
		}
	}
	for _, args := range [][]string{
		{"--json", "--repo", "Owner/Repo", "review", "ls"},
		{"repo", "info", origin + "/Owner/Repo"},
		{"--endpoint=" + origin, "review", "show", "1"},
		{"-qC", "subdir", "status"},
		{"review", "comment", "1", "See https://external.invalid/doc", "--revision", "1"},
	} {
		if err := ValidateArguments(origin, args); err != nil {
			t.Fatal(args, err)
		}
	}
	for _, args := range [][]string{nil, {"--version"}, {"--json", "--version"}, {"review", "create", "--help"}, {"login", "--help"}} {
		if !Help(args) {
			t.Fatal(args)
		}
	}
}
