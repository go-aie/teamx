package room

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// stdout is bounded and stderr is discarded: Git/API failures can echo secrets.
// Only fixed diagnostics cross the preparation boundary.
type preparationOutput struct {
	buffer   bytes.Buffer
	overflow bool
	limit    int
}

func (b *preparationOutput) Write(p []byte) (int, error) {
	limit := b.limit
	if limit == 0 {
		limit = 64 << 10
	}
	n := min(len(p), limit-b.buffer.Len())
	_, _ = b.buffer.Write(p[:n])
	b.overflow = b.overflow || n < len(p)
	return len(p), nil
}

type preparationExitError struct{ code int }

func (e preparationExitError) ExitCode() int { return e.code }

type preparationTrackerKey struct{}
type preparationTracker func(string, int) error

func trackPreparation(ctx context.Context, phase string, pid int) error {
	if track, ok := ctx.Value(preparationTrackerKey{}).(preparationTracker); ok {
		return track(phase, pid)
	}
	return nil
}

func (preparationExitError) Error() string {
	return "workspace preparation failed; preserve and inspect target"
}

func runPreparation(ctx context.Context, dir string, env map[string]string, executable string, args ...string) ([]byte, error) {
	return runPreparationCommand(ctx, dir, env, preparationCommandOptions{timeout: serviceShutdownTimeout, outputLimit: 64 << 10}, executable, args...)
}

type preparationCommandOptions struct {
	timeout     time.Duration
	outputLimit int
	isolated    bool
}

func runPreparationCommand(ctx context.Context, dir string, env map[string]string, options preparationCommandOptions, executable string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Env = preparationEnvironment(env)
	if options.isolated {
		cmd.Env = isolatedPreparationEnvironment(env)
	}
	configurePreparation(cmd)
	out := preparationOutput{limit: options.outputLimit}
	cmd.Stdout = &out
	// Intent precedes Start: a crash in the spawn/PID-record gap must remain
	// explicitly blocked rather than silently launching a second preparer.
	if err := trackPreparation(ctx, "starting", 0); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		if recordErr := trackPreparation(ctx, "settled", 0); recordErr != nil {
			return nil, recordErr
		}
		return nil, errors.New("workspace preparation could not start")
	}
	if err := trackPreparation(ctx, "running", cmd.Process.Pid); err != nil {
		if cmd.Cancel != nil {
			_ = cmd.Cancel()
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.Join(err, settlePreparation(cmd))
	}
	err := cmd.Wait()
	cleanupErr := settlePreparation(cmd)
	if cleanupErr != nil {
		return nil, cleanupErr
	}
	if err := trackPreparation(ctx, "settled", cmd.Process.Pid); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, errors.New("workspace preparation timed out or cancelled; preserve and inspect target")
	}
	if out.overflow {
		return nil, errors.New("workspace preparation output exceeded limit")
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, preparationExitError{code: exit.ExitCode()}
		}

		return nil, errors.New("workspace preparation failed; preserve and inspect target")
	}
	return bytes.TrimSpace(out.buffer.Bytes()), nil
}
func preparationEnvironment(env map[string]string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		k, v, _ := strings.Cut(entry, "=")
		values[k] = v
	}
	// Ignore inherited repository selection/inline configuration. Git-only rooms
	// retain the host's ordinary helpers; a GitLab binding overrides askpass.
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT"} {
		delete(values, k)
	}
	for k := range values {
		if strings.HasPrefix(k, "GIT_CONFIG_KEY_") || strings.HasPrefix(k, "GIT_CONFIG_VALUE_") {
			delete(values, k)
		}
	}
	values["GIT_TERMINAL_PROMPT"] = "0"
	for k, v := range env {
		values[k] = v
	}
	result := make([]string, 0, len(values))
	for k, v := range values {
		result = append(result, k+"="+v)
	}
	return result
}

// Scion preparation never inherits the operator login or unrelated secrets.
func isolatedPreparationEnvironment(env map[string]string) []string {
	values := map[string]string{}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "TEAMX_SCN_BIN"} {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	for k, v := range env {
		values[k] = v
	}
	result := make([]string, 0, len(values))
	for k, v := range values {
		result = append(result, k+"="+v)
	}
	return result
}
