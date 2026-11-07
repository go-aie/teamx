// Package scion supplies connection evidence and private CLI execution inputs.
// Review commands and their output remain owned by the standalone native CLI.
package scion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/tool"
)

const CLIVersion = "0.1.13"

func failure(code string) error { return errors.New(code) }

// Executable names teamx-scion or the pinned native scn. Profile is an opaque
// service-owned directory, never a caller-selected credential file path.
func ValidProfile(p platform.CLIProfile) bool {
	return p.Version == 1 && p.Brand == "scion" && p.CLIVersion == CLIVersion && tool.OpaqueID(p.Profile) &&
		filepath.IsAbs(p.Executable) && filepath.Clean(p.Executable) == p.Executable && !strings.ContainsAny(p.Executable, "\x00\r\n")
}

func Definition(executable string) tool.Definition {
	return tool.Definition{Name: "scion", Provider: "scion", Executable: executable, Environment: map[string]string{
		"SCION_ENDPOINT": "instance", "SCION_HUB": "instance", "SCION_CREDENTIALS_FILE": "credentials_file",
		"SCION_CREDENTIALS": "credential_mode", "SCION_NO_DAEMON": "no_daemon", "SCION_CONFIG_FILE": "config_file",
	}}
}

// Snapshot pins the selected credential bytes for both whoami and the actual
// invocation. Replacing the source file cannot switch an admitted call's user.
// The CLI may refresh its private copy; it never mutates the managed profile.
func Snapshot(root, origin string, p platform.CLIProfile, private string) (string, error) {
	if !ValidProfile(p) {
		return "", failure("invalid_profile")
	}
	dir := filepath.Join(root, "scion-profiles")
	profile := filepath.Join(dir, p.Profile)
	for _, path := range []string{dir, profile} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return "", failure("credential_unavailable")
		}
	}
	path := filepath.Join(profile, "credentials.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1<<20 {
		return "", failure("credential_unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", failure("credential_unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", failure("credential_unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	var credentials map[string]string
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &credentials) != nil || !platform.ValidCredential(credentials[origin]) {
		return "", failure("credential_unavailable")
	}
	// The current POC uses a native PAT login. Export only the selected origin;
	// unrelated entries in a CLI credential file never become fallback accounts.
	data, _ = json.Marshal(map[string]string{origin: credentials[origin]})
	destination := filepath.Join(private, "scion-credentials.json")
	if os.WriteFile(destination, data, 0600) != nil {
		return "", failure("credential_unavailable")
	}
	return destination, nil
}

func Values(origin, path string) map[string]string {
	return map[string]string{"instance": origin, "credentials_file": path, "credential_mode": "file", "no_daemon": "1", "config_file": filepath.Join(filepath.Dir(path), "scion-config.json")}
}

// boundedOutput stops a misbehaving executable before unbounded allocation.
type boundedOutput struct {
	data     []byte
	cancel   context.CancelFunc
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 64<<10 {
		b.overflow = true
		b.cancel()
		return 0, failure("output_limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func VerifyFile(ctx context.Context, origin string, p platform.CLIProfile, path string) platform.Validation {
	return verifyWithRun(ctx, origin, p, path, nil)
}

// VerifyWithRun permits preparation callers to keep their durable process
// tracker. The supplied runner must isolate credentials and bound output.
func VerifyWithRun(ctx context.Context, origin string, p platform.CLIProfile, path string, run func(context.Context, ...string) ([]byte, error)) platform.Validation {
	return verifyWithRun(ctx, origin, p, path, run)
}
func verifyWithRun(ctx context.Context, origin string, p platform.CLIProfile, path string, run func(context.Context, ...string) ([]byte, error)) platform.Validation {
	result := platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: "invalid_profile"}
	if !ValidProfile(p) {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, stderr := &boundedOutput{cancel: cancel}, &boundedOutput{cancel: cancel}
	process := tool.Process{Definition: Definition(p.Executable), Output: out, Error: stderr}
	invoke := func(args ...string) int {
		if run == nil {
			return process.Run(ctx, args)
		}
		data, err := run(ctx, args...)
		out.data = data
		if err != nil {
			var exit interface{ ExitCode() int }
			if errors.As(err, &exit) {
				return exit.ExitCode()
			}
			return 1
		}
		return 0
	}
	code := invoke("--version")
	if code != 0 || strings.TrimSpace(string(out.data)) != "scn "+CLIVersion {
		result.ErrorCode = "cli_version_mismatch"
		if code == 127 {
			result.ErrorCode = "cli_unavailable"
		}
		if ctx.Err() != nil {
			result.ErrorCode = "cancelled"
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.ErrorCode = "timeout"
		}
		if out.overflow || stderr.overflow {
			result.ErrorCode = "output_limit"
		}
		return result
	}
	out.data, stderr.data = nil, nil
	process.Values = Values(origin, path)
	code = invoke("--json", "whoami", origin)
	switch {
	case out.overflow || stderr.overflow:
		result.ErrorCode = "output_limit"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.ErrorCode = "timeout"
	case ctx.Err() != nil:
		result.ErrorCode = "cancelled"
	case code == 127:
		result.ErrorCode = "cli_unavailable"
	case code == 5:
		result.ErrorCode = "credential_rejected"
	case code != 0:
		result.ErrorCode = "query_failed"
	default:
		var identity struct {
			Schema   string `json:"schema"`
			Endpoint string `json:"endpoint"`
			Subject  string `json:"subject"`
		}
		if json.Unmarshal(out.data, &identity) != nil || identity.Schema != "scion.cli.whoami/v1" || identity.Endpoint != origin ||
			strings.TrimSpace(identity.Subject) != identity.Subject || identity.Subject == "" || len(identity.Subject) > 1024 ||
			strings.EqualFold(identity.Subject, "anonymous") || strings.IndexFunc(identity.Subject, unicode.IsControl) >= 0 {
			result.ErrorCode = "invalid_response"
			return result
		}
		result.Status, result.ErrorCode, result.Username = "valid", "", identity.Subject
		result.Account = platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "scion", Instance: origin}, ID: platform.ExternalID{Namespace: "scion:subject", Value: identity.Subject}}
	}
	return result
}

func VerifyProfile(ctx context.Context, root, origin string, p platform.CLIProfile) platform.Validation {
	result := platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: "credential_unavailable"}
	private, err := os.MkdirTemp("", "teamx-scion-verify-")
	if err != nil {
		return result
	}
	defer os.RemoveAll(private)
	path, err := Snapshot(root, origin, p, private)
	if err != nil {
		result.ErrorCode = err.Error()
		return result
	}
	return VerifyFile(ctx, origin, p, path)
}

func Prepare(ctx context.Context, root, origin string, profile platform.CLIProfile, expected platform.AccountIdentity, private string) (map[string]string, error) {
	path, err := Snapshot(root, origin, profile, private)
	if err != nil {
		return nil, err
	}
	actual := VerifyFile(ctx, origin, profile, path)
	if actual.Status != "valid" {
		return nil, failure(actual.ErrorCode)
	}
	if actual.Account != expected {
		return nil, failure("identity_mismatch")
	}
	return Values(origin, path), nil
}
