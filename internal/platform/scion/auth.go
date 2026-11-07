package scion

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Discovery reads only TeamX-managed profiles, never the operator's global CLI
// login. Paths and credentials are private; the selected native account is public.
type ProfileOption struct {
	Profile  string `json:"profile"`
	Username string `json:"username"`
}
type Discovery struct {
	Executable  string          `json:"executable"`
	Profiles    []ProfileOption `json:"profiles"`
	Unavailable int             `json:"unavailable"`
}

// ResolveCLI accepts either the thin wrapper or native scn. Both report the
// same pinned version and run through TeamX's isolated process environment.
func ResolveCLI(ctx context.Context, executable string) (string, error) {
	if executable == "" {
		override := os.Getenv("TEAMX_SCION_BIN")
		if override != "" && !filepath.IsAbs(override) {
			return "", failure("cli_unavailable")
		}
		executable = tool.ResolveExecutable("teamx-scion", override)
		if executable == "teamx-scion" {
			if p, e := exec.LookPath(executable); e == nil {
				executable = p
			} else {
				override = os.Getenv("TEAMX_SCN_BIN")
				if override != "" && !filepath.IsAbs(override) {
					return "", failure("cli_unavailable")
				}
				executable = tool.ResolveExecutable("scn", override)
				if executable == "scn" {
					if p, e := exec.LookPath(executable); e == nil {
						executable = p
					}
				}
			}
		}
	}
	p := platform.CLIProfile{Version: 1, Brand: "scion", Profile: strings.Repeat("0", 32), CLIVersion: CLIVersion, Executable: executable}
	if !ValidProfile(p) {
		return "", failure("cli_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out := &boundedOutput{cancel: cancel}
	process := tool.Process{Definition: Definition(executable), Input: strings.NewReader(""), Output: out, Error: io.Discard}
	code := process.Run(ctx, []string{"--version"})
	if ctx.Err() != nil {
		return "", failure("timeout")
	}
	if code == 127 {
		return "", failure("cli_unavailable")
	}
	if code != 0 || strings.TrimSpace(string(out.data)) != "scn "+CLIVersion {
		return "", failure("cli_version_mismatch")
	}
	return executable, nil
}

func Discover(ctx context.Context, root, origin, executable string) (Discovery, error) {
	result := Discovery{Profiles: []ProfileOption{}}
	var err error
	result.Executable, err = ResolveCLI(ctx, executable)
	if err != nil {
		return result, err
	}
	parent := filepath.Join(root, "scion-profiles")
	info, err := os.Lstat(parent)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return result, failure("credential_unavailable")
	}
	dir, err := os.Open(parent)
	if err != nil {
		return result, failure("credential_unavailable")
	}
	defer dir.Close()
	entries, err := dir.ReadDir(129)
	if err != nil && err != io.EOF {
		return result, failure("credential_unavailable")
	}
	if len(entries) > 128 {
		return result, failure("capacity")
	}
	for _, entry := range entries {
		if _, e := os.Lstat(filepath.Join(parent, entry.Name(), ".login-pending")); e == nil {
			continue
		}
		if !tool.OpaqueID(entry.Name()) {
			continue
		}
		p := platform.CLIProfile{Version: 1, Brand: "scion", Profile: entry.Name(), CLIVersion: CLIVersion, Executable: result.Executable}
		validation := VerifyProfile(ctx, root, origin, p)
		if ctx.Err() != nil {
			return result, failure("timeout")
		}
		if validation.Status == "valid" {
			result.Profiles = append(result.Profiles, ProfileOption{Profile: p.Profile, Username: validation.Username})
		} else {
			result.Unavailable++
		}
	}
	sort.Slice(result.Profiles, func(i, j int) bool {
		if result.Profiles[i].Username == result.Profiles[j].Username {
			return result.Profiles[i].Profile < result.Profiles[j].Profile
		}
		return result.Profiles[i].Username < result.Profiles[j].Username
	})
	return result, nil
}

// Login creates a fresh managed profile and delegates token validation/storage
// to the pinned CLI. A failed or cancelled login removes only its own new dir.
// The PAT exists only in memory/stdin and the CLI-owned private credential file.
func Login(ctx context.Context, root, origin, executable, id string, pat platform.Secret) (profile platform.CLIProfile, validation platform.Validation, err error) {
	if !tool.OpaqueID(id) || !platform.ValidCredential(pat.Reveal()) {
		return profile, validation, failure("invalid_profile")
	}
	executable, err = ResolveCLI(ctx, executable)
	if err != nil {
		return
	}
	profile = platform.CLIProfile{Version: 1, Brand: "scion", Profile: id, CLIVersion: CLIVersion, Executable: executable}
	parent := filepath.Join(root, "scion-profiles")
	if e := os.Mkdir(parent, 0700); e != nil && !os.IsExist(e) {
		return profile, validation, failure("credential_unavailable")
	}
	info, e := os.Lstat(parent)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return profile, validation, failure("credential_unavailable")
	}
	dir := filepath.Join(parent, id)
	if os.Mkdir(dir, 0700) != nil {
		return profile, validation, failure("credential_unavailable")
	}
	if os.WriteFile(filepath.Join(dir, ".login-pending"), nil, 0600) != nil {
		_ = os.RemoveAll(dir)
		return profile, validation, failure("credential_unavailable")
	}
	completed := false
	defer func() {
		if !completed {
			_ = os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, "credentials.json")
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out := &boundedOutput{cancel: cancel}
	process := tool.Process{Definition: Definition(executable), Values: Values(origin, path), Input: strings.NewReader(pat.Reveal()), Output: out, Error: io.Discard}
	code := process.Run(ctx, []string{"--json", "login", origin, "--with-token"})
	if ctx.Err() != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return profile, validation, failure("timeout")
		}
		return profile, validation, failure("cancelled")
	}
	if code != 0 {
		return profile, validation, failure("login_failed")
	}
	var login struct{ Schema, Endpoint, Subject string }
	if json.Unmarshal(out.data, &login) != nil || login.Schema != "scion.cli.login/v1" || login.Endpoint != origin {
		return profile, validation, failure("invalid_response")
	}
	validation = VerifyProfile(ctx, root, origin, profile)
	if validation.Status != "valid" {
		return profile, validation, failure(validation.ErrorCode)
	}
	if validation.Username != login.Subject {
		return profile, validation, failure("identity_mismatch")
	}
	if ctx.Err() != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return profile, validation, failure("timeout")
		}
		return profile, validation, failure("cancelled")
	}
	if os.Remove(filepath.Join(dir, ".login-pending")) != nil {
		return profile, validation, failure("credential_unavailable")
	}
	completed = true
	return profile, validation, nil
}
