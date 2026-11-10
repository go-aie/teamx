package room

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/scion"
)

// Preparation uses the same private profile snapshot and account boundary as
// member tools, while retaining the Workspace operation's PID/settlement tracker.
func (m *roomManager) scionWorkspaceExecute(owner RoomIdentity, w workspaceConfig, run platform.Execute) platform.Execute {
	return func(ctx context.Context, dir string, _ map[string]string, _ string, args ...string) (out []byte, err error) {
		m.mu.Lock()
		resolved, err := m.resolveWorkspacePlatformLocked(owner, w)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		useCtx, cancel := context.WithCancel(ctx)
		call := &platformCall{owner: owner, tool: "scion", platformID: resolved.record.ID, account: resolved.account, cancel: cancel, done: make(chan struct{})}
		if m.platformCalls == nil {
			m.platformCalls = map[*platformCall]bool{}
		}
		m.platformCalls[call] = true
		root := m.store.dir
		m.mu.Unlock()
		defer m.finishPlatformCall(call)
		private, err := os.MkdirTemp("", "teamx-scion-workspace-")
		if err != nil {
			return nil, ErrInvalid
		}
		defer os.RemoveAll(private)
		profile := resolved.record.CLIProfile
		if profile == nil {
			return nil, ErrIdentity
		}
		path, err := scion.Snapshot(root, resolved.record.URL, *profile, private)
		if err != nil {
			return nil, err
		}
		env := map[string]string{"HOME": private, "XDG_CONFIG_HOME": private, "XDG_CACHE_HOME": private, "TMPDIR": private, "SCION_ENDPOINT": resolved.record.URL, "SCION_HUB": resolved.record.URL, "SCION_CREDENTIALS_FILE": path, "SCION_CREDENTIALS": "file", "SCION_NO_DAEMON": "1", "SCION_CONFIG_FILE": filepath.Join(private, "config.json")}
		// Track version/identity processes as well as clone. All commands see the
		// same pinned credentials; a profile replaced mid-call cannot switch users.
		var verificationErr error
		validation := scion.VerifyWithRun(useCtx, resolved.record.URL, *profile, path, func(ctx context.Context, args ...string) ([]byte, error) {
			out, err := run(ctx, dir, env, profile.Executable, args...)
			if err != nil {
				verificationErr = err
			}
			return out, err
		})
		if verificationErr != nil {
			return nil, verificationErr
		}
		if validation.Status != "valid" || validation.Account != resolved.account.Identity {
			return nil, ErrIdentity
		}
		if err := scion.ValidateArguments(resolved.record.URL, args); err != nil {
			return nil, err
		}
		out, err = run(useCtx, dir, env, profile.Executable, args...)
		m.mu.Lock()
		checkErr := m.checkProjectPlatformLocked(resolved)
		m.mu.Unlock()
		if checkErr != nil {
			return nil, checkErr
		}
		if useCtx.Err() != nil {
			return nil, useCtx.Err()
		}
		return out, err
	}
}

func runScionPreparation(ctx context.Context, dir string, env map[string]string, executable string, args ...string) ([]byte, error) {
	return runPreparationCommand(ctx, dir, env, preparationCommandOptions{timeout: 5 * time.Minute, outputLimit: 64 << 10, isolated: true}, executable, args...)
}
