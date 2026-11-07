package tool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Definition describes a standalone executable, never platform subcommands.
type Definition struct {
	InternalOnly bool              `json:"internal_only,omitempty"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Version      string            `json:"version"`
	Provider     string            `json:"provider,omitempty"`
	Skill        string            `json:"skill,omitempty"`
	Executable   string            `json:"-"`
	Environment  map[string]string `json:"-"` // child variable -> account field
}

func Catalog() []Definition {
	bin := ResolveExecutable("teamx-gitlab", os.Getenv("TEAMX_GITLAB_BIN"))
	return []Definition{{Name: "gitlab", Description: "GitLab CLI and WorkItem extensions; uses account permissions", Version: "glab 1.127.0", Provider: "gitlab", Executable: bin, Skill: "skills/gitlab/SKILL.md",
		Environment: map[string]string{"GITLAB_HOST": "instance", "GITLAB_TOKEN": "token"}}, {Name: "lark", Description: "飞书消息只读绑定；使用 teamx lark read --help 读取原消息或有界上下文", Provider: "lark", Version: "lark-cli 1.0.99", InternalOnly: true}}
}

// Help requests need no account or credential resolution. No business routing.
func Help(args []string) bool {
	if len(args) == 0 || args[0] == "help" || args[0] == "version" || len(args) == 1 && args[0] == "-v" {
		return true
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--help" || arg == "-h" || arg == "--version" {
			return true
		}
	}
	return false
}

type Process struct {
	Args          []string `json:"-"`
	Definition    Definition
	Directory     string
	Input         io.Reader
	Output, Error io.Writer
	Values        map[string]string `json:"-"`
}

func (Process) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "[REDACTED tool process]") }

// Run uses a small environment allowlist. Model credentials, operator tokens,
// global login config and Room identity never enter a tool process.
func (p Process) Run(ctx context.Context, args []string) int {
	private, err := os.MkdirTemp("", "teamx-tool-")
	if err != nil {
		io.WriteString(p.Error, "cannot prepare tool environment\n")
		return 127
	}
	defer os.RemoveAll(private)
	dir := p.Directory
	if dir == "" {
		dir = private
	}
	env := []string{"HOME=" + private, "XDG_CONFIG_HOME=" + private, "TMPDIR=" + private}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "TEAMX_GLAB_BIN"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for key, field := range p.Definition.Environment {
		if value := p.Values[field]; value != "" {
			env = append(env, key+"="+value)
		}
	}
	if p.Args != nil {
		args = p.Args
	}
	cmd := exec.CommandContext(ctx, p.Definition.Executable, args...)
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, env, p.Input, p.Output, p.Error
	configureToolProcess(cmd)
	err = cmd.Run()
	settleToolProcess(cmd)
	if ctx.Err() != nil {
		io.WriteString(p.Error, "tool interrupted; remote result unconfirmed; query before retry\n")
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
		io.WriteString(p.Error, "tool terminated; remote result unconfirmed; query before retry\n")
		return 130
	}
	// Never print exec errors containing paths or environment values.
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		io.WriteString(p.Error, "tool executable unavailable on TeamX server (exit 127). Operator: install the registered executable beside TeamX or on the server PATH, or fix its configured executable override, then restart TeamX. Agent shell PATH/env changes do not affect server tools. Report this error; do not search local files or bypass the launcher.\n")
		return 127
	}
	io.WriteString(p.Error, "tool executable unavailable or process I/O failed\n")
	return 127
}

// Binding is one explicitly selected tool and optional connection per member.
type Binding struct {
	AgentID              string `json:"agent_id"`
	MembershipID         string `json:"membership_id"`
	Tool                 string `json:"tool"`
	ConnectionID         string `json:"connection_id,omitempty"`
	ConnectionGeneration string `json:"connection_generation,omitempty"`
}

func OpaqueID(s string) bool { return len(s) == 32 && strings.Trim(s, "0123456789abcdef") == "" }

func ValidName(s string) bool {
	return s != "" && len(s) <= 64 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789-_") == ""
}
