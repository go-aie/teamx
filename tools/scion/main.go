// Command teamx-scion runs the pinned native Scion CLI without TeamX dependencies.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed scn.lock.json
var lockJSON []byte

var dependency struct {
	Version string `json:"version"`
}

func init() {
	if err := json.Unmarshal(lockJSON, &dependency); err != nil || dependency.Version == "" {
		panic("invalid embedded scn.lock.json")
	}
}

type app struct {
	in          io.Reader
	out, errOut io.Writer
	environ     []string
	executable  func() (string, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	a := app{in: os.Stdin, out: os.Stdout, errOut: os.Stderr, environ: os.Environ(), executable: os.Executable}
	code := a.run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

func (a *app) run(ctx context.Context, args []string) int {
	private, err := os.MkdirTemp("", "teamx-scn-")
	if err != nil {
		fmt.Fprintln(a.errOut, "cannot prepare private scn environment")
		return 127
	}
	defer os.RemoveAll(private)

	// The probe never loads an operator's credentials or configuration. Native
	// help/version also work without a profile; scn itself parses those requests.
	env := nativeEnvironment(a.environ, private, false)
	bin, err := resolveScn(envValue(a.environ, "TEAMX_SCN_BIN"), a.executable, envValue(env, "PATH"))
	if err != nil {
		a.dependencyGuidance("scn dependency unavailable")
		return 127
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	probe := exec.CommandContext(probeCtx, bin, "--version")
	probe.Env = env
	configureProcess(probe)
	version, err := probe.Output()
	settleProcess(probe)
	cancel()
	if ctx.Err() != nil {
		fmt.Fprintln(a.errOut, "interrupted before scn execution")
		return 130
	}
	if err != nil {
		a.dependencyGuidance("scn dependency unavailable or version check failed")
		return 127
	}
	if strings.TrimSpace(string(version)) != "scn "+dependency.Version {
		a.dependencyGuidance("unsupported scn version")
		return 127
	}

	// Do not scan arguments for help flags: a comment body or file name may
	// contain exactly that text. All arguments and native parsing stay intact.
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = nativeEnvironment(a.environ, private, true)
	cmd.Stdin = a.in
	// Forward bytes through pipes even when called from an interactive terminal.
	cmd.Stdout = struct{ io.Writer }{a.out}
	cmd.Stderr = struct{ io.Writer }{a.errOut}
	configureProcess(cmd)
	err = cmd.Run()
	settleProcess(cmd)
	if ctx.Err() != nil {
		fmt.Fprintln(a.errOut, "scn interrupted; check any remote write before retry; no automatic retry was made")
		return 130
	}
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if exit.ExitCode() >= 0 {
			return exit.ExitCode()
		}
		fmt.Fprintln(a.errOut, "scn terminated; check any remote write before retry; no automatic retry was made")
		return 130
	}
	// Exec errors may contain private paths; don't echo them or the arguments.
	fmt.Fprintln(a.errOut, "cannot start scn or forward process I/O")
	return 127
}

func (a *app) dependencyGuidance(reason string) {
	fmt.Fprintf(a.errOut, "%s: require scn %s (exit 127). Operator: install the verified release beside teamx-scion or on the service PATH, or set TEAMX_SCN_BIN to its absolute path. See tools/scion/README.zh.md and scn.lock.json. Explicit overrides never fall back. Configure the TeamX service environment and restart it; agent shell PATH/env changes do not affect server tools.\n", reason, dependency.Version)
}

func envValue(env []string, key string) string {
	// Last wins, as with os/exec's environment handling.
	for i := len(env) - 1; i >= 0; i-- {
		k, value, ok := strings.Cut(env[i], "=")
		if ok && k == key {
			return value
		}
	}
	return ""
}

func nativeEnvironment(source []string, private string, runtime bool) []string {
	env := []string{
		"HOME=" + private, "XDG_CONFIG_HOME=" + private, "XDG_CACHE_HOME=" + private, "TMPDIR=" + private,
		"SCION_CONFIG_FILE=" + filepath.Join(private, "config.json"),
		"SCION_CREDENTIALS=file", "SCION_CREDENTIALS_FILE=" + filepath.Join(private, "credentials.json"),
		"SCION_NO_DAEMON=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		if value := envValue(source, key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	if runtime {
		// These are explicit standalone inputs; S2 will resolve and inject them
		// from a member's selected connection. Never forward SCION_TOKEN or a
		// global config, keychain, daemon, Git credential or model environment.
		for _, key := range []string{"SCION_ENDPOINT", "SCION_HUB", "SCION_REPO", "SCION_AUTHOR", "SCION_CREDENTIALS_FILE"} {
			if value := envValue(source, key); value != "" {
				env = append(env, key+"="+value)
			}
		}
	}
	return env
}

// resolveScn never searches a workspace. An explicit override is authoritative.
func resolveScn(override string, executable func() (string, error), pathEnv string) (string, error) {
	if override != "" {
		if !filepath.IsAbs(override) {
			return "", errors.New("TEAMX_SCN_BIN must be absolute")
		}
		return override, nil
	}
	if self, err := executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), "scn")
		if _, err := os.Stat(candidate); err == nil {
			// A broken sibling must produce an installation error, not select an
			// unrelated native CLI from PATH.
			return candidate, nil
		}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if !filepath.IsAbs(dir) {
			continue // Do not execute scn from cwd or relative PATH entries.
		}
		candidate := filepath.Join(dir, "scn")
		if file, err := os.Stat(candidate); err == nil && !file.IsDir() && file.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}
