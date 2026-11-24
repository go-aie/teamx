// Command teamx-gitlab is an independent glab proxy with small Go extensions.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const glabVersion = "1.127.0"
const usagePrefix = "teamx tools run gitlab"

type extension struct {
	path, description string
	run               func(context.Context, *app, []string) error
}

// This is the only registry used for both dispatch and parent help.
var extensions = []extension{
	{"work-items view", "Read a work item (JSON)", workItemCommand("view")},
	{"work-items close", "Close a work item", workItemCommand("close")},
	{"work-items assignees", "Add, remove, replace or clear assignees", workItemCommand("assignees")},
	{"work-items note", "Comment or reply on a work item", workItemCommand("note")},
	{"work-items discussions list", "Read all work item discussions and notes", workItemCommand("discussions")},
	{"issue discussions list", "Read all issue discussions", issueCommand("list")},
	{"issue discussions reply", "Reply to an issue discussion", issueCommand("reply")},
}

type app struct {
	shared      bool
	in          io.Reader
	out, errOut io.Writer
	getenv      func(string) string
	transport   roundTripper
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := &app{shared: os.Getenv("TEAMX_GITLAB_API_ONLY") == "1", in: os.Stdin, out: os.Stdout, errOut: os.Stderr, getenv: os.Getenv}
	os.Exit(a.run(ctx, os.Args[1:]))
}

func (a *app) run(ctx context.Context, args []string) int {
	if len(args) > 0 && args[0] == "help" {
		args = append(append([]string{}, args[1:]...), "--help")
	}
	for _, e := range extensions {
		p := strings.Fields(e.path)
		if len(args) >= len(p) && equal(args[:len(p)], p) {
			if err := e.run(ctx, a, args[len(p):]); err != nil {
				fmt.Fprintln(a.errOut, err)
				if ctx.Err() != nil {
					return 130
				}
				return 1
			}
			return 0
		}
	}
	// Parent help is recognized structurally, never by scanning business values.
	if a.shared {
		if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") || len(args) == 2 && args[0] == "api" && (args[1] == "--help" || args[1] == "-h") {
			fmt.Fprintln(a.out, sharedHelp)
			return 0
		}
		checked, err := sharedNativeArguments(args, a.getenv("GITLAB_HOST"))
		if err != nil {
			fmt.Fprintln(a.errOut, err)
			return 2
		}
		return a.native(ctx, checked, false)
	}
	parent := args
	if len(parent) > 0 && (parent[len(parent)-1] == "--help" || parent[len(parent)-1] == "-h") {
		parent = parent[:len(parent)-1]
	}
	path := strings.Join(parent, " ")
	var children []extension
	for _, e := range extensions {
		if path == "" || strings.HasPrefix(e.path, path+" ") {
			children = append(children, e)
		}
	}
	if len(children) > 0 {
		pure := path != "" && path != "issue" && path != "work-items"
		code := 0
		if !pure {
			fmt.Fprintln(a.out, "Native help: replace the glab prefix with "+usagePrefix+".")
			code = a.native(ctx, append(append([]string{}, parent...), "--help"), true)
			if code == 130 {
				return code
			}
		}
		fmt.Fprintln(a.out, "\nAdditional commands:")
		for _, e := range children {
			fmt.Fprintf(a.out, "  %-30s %s\n", strings.TrimPrefix(e.path, path+" "), e.description)
		}
		fmt.Fprintf(a.out, "\nUsage: %s %s <command> [flags]\n", usagePrefix, path)
		return code
	}
	return a.native(ctx, args, false)
}

func equal(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

func (a *app) native(ctx context.Context, args []string, help bool) int {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version" || args[0] == "-v") {
		help = true
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--help" || arg == "-h" {
			help = true
		}
	}
	bin := resolveGlab(a.getenv("TEAMX_GLAB_BIN"))
	if a.shared {
		resolved, err := exec.LookPath(bin)
		if err != nil {
			fmt.Fprintln(a.errOut, "glab dependency unavailable")
			return 127
		}
		bin = resolved
	}
	config, err := os.MkdirTemp("", "teamx-glab-")
	if err != nil {
		fmt.Fprintln(a.errOut, "cannot prepare private glab config")
		return 1
	}
	defer os.RemoveAll(config)
	if a.shared && !help {
		body, err := sharedNativeConfig(a.getenv("GITLAB_HOST"))
		if err != nil || os.WriteFile(filepath.Join(config, "config.yml"), body, 0600) != nil {
			fmt.Fprintln(a.errOut, "cannot prepare selected GitLab API host")
			return 1
		}
		// glab performs Git probes during startup even for API requests. A
		// fixed refusal executable makes those probes side-effect free; the
		// real host Git and any workspace helper remain unreachable. glab is
		// resolved above, before narrowing PATH. No argv is passed to a shell.
		if os.Symlink("/usr/bin/false", filepath.Join(config, "git")) != nil {
			fmt.Fprintln(a.errOut, "cannot prepare Git refusal")
			return 1
		}
	}
	// Do not read the operator's glab accounts or inherit alternate token sources.
	env := []string{}
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		if a.shared && k == "PATH" {
			continue
		}
		if strings.HasPrefix(k, "GLAB_") || strings.HasPrefix(k, "GITLAB_") || k == "CI_JOB_TOKEN" || k == "OAUTH_TOKEN" {
			continue
		}
		env = append(env, v)
	}
	if a.shared {
		env = append(env, "PATH="+config)
	}
	// A fresh config would show the post-upgrade banner on every invocation.
	// It is separate from update checks and breaks JSON consumers merging stderr.
	env = append(env, "GLAB_CONFIG_DIR="+config, "GLAB_CHECK_UPDATE=false", "GLAB_SHOW_WHATS_NEW=false", "GLAB_SEND_TELEMETRY=false", "GLAB_NO_PROMPT=true")
	if !help {
		env = append(env, "GLAB_IS_OAUTH2=true")
		for _, k := range []string{"GITLAB_HOST", "GITLAB_TOKEN"} {
			if v := a.getenv(k); v != "" {
				env = append(env, k+"="+v)
			}
		}
	}
	version := exec.CommandContext(ctx, bin, "version")
	version.Env = env
	configureProcess(version)
	v, err := version.Output()
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(a.errOut, "command interrupted before glab execution")
			return 130
		}
		fmt.Fprintln(a.errOut, "glab dependency unavailable: require v"+glabVersion)
		a.dependencyGuidance()
		return 127
	}
	fields := strings.Fields(string(v))
	if len(fields) < 2 || fields[0] != "glab" || strings.TrimPrefix(fields[1], "v") != glabVersion {
		fmt.Fprintln(a.errOut, "unsupported glab: require v"+glabVersion)
		a.dependencyGuidance()
		return 127
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.Stdin = a.in
	// Use streaming pipes even when the caller supplies terminal *os.File values.
	// Alongside the detached session this prevents glab's color renderer from
	// falling back to querying inherited terminal descriptors. Bytes and exit
	// status remain unchanged, just as when launched through the TeamX service.
	cmd.Stdout = struct{ io.Writer }{a.out}
	cmd.Stderr = struct{ io.Writer }{a.errOut}
	configureProcess(cmd)
	err = cmd.Run()
	if ctx.Err() != nil {
		if help {
			fmt.Fprintln(a.errOut, "help interrupted")
		} else {
			fmt.Fprintln(a.errOut, "command interrupted; remote result unconfirmed; query before any retry")
		}
		return 130
	}
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.ExitCode() < 0 {
			fmt.Fprintln(a.errOut, "command terminated; remote result unconfirmed; query before any retry")
			return 130
		}
		return ee.ExitCode()
	}
	fmt.Fprintln(a.errOut, "cannot start glab")
	return 127
}

func (a *app) dependencyGuidance() {
	fmt.Fprintln(a.errOut, "Operator: install the required glab beside teamx-gitlab or on its PATH, or set TEAMX_GLAB_BIN to its absolute path. When using TeamX, configure this on the TeamX service and restart it; agent shell PATH/env changes do not affect server tools. Report this error; do not search local files or bypass the launcher.")
}

// resolveGlab returns the glab executable to run, in this order: the explicit
// TEAMX_GLAB_BIN override, a sibling named glab beside this program, then PATH.
// It never searches agent workspaces or temp trees, and an explicit override is
// authoritative even when broken (never substituted for another version).
func resolveGlab(override string) string {
	if override != "" {
		return override
	}
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), "glab")
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	return "glab"
}
