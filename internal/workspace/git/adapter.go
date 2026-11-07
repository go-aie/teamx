// Package git implements local Git working copy preparation. Platform access
// and process lifecycle are supplied by the caller.
package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/workspace"
)

type Adapter struct {
	Source workspace.Source
	// CloneURL is the verified canonical URL, when different from Source.URL.
	CloneURL string
	Run      workspace.Execute
}

var _ workspace.Repository = Adapter{}

func (a Adapter) cloneURL() string {
	if a.CloneURL != "" {
		return a.CloneURL
	}
	return a.Source.URL
}

func (a Adapter) Clone(ctx context.Context, parent, target string, env map[string]string) error {
	args := []string{"clone"}
	if a.Source.Branch != "" {
		args = append(args, "--branch", a.Source.Branch)
	}
	args = append(args, "--", a.cloneURL(), target)
	_, err := a.git(ctx, parent, env, args...)
	return err
}

func (a Adapter) Configure(ctx context.Context, target string, initial bool, access workspace.Access) error {
	if access.Author == nil {
		return nil
	}
	env := access.Env

	// Authentication is room-specific. These contain references only, never tokens.
	if initial {
		for _, kv := range [][2]string{{"credential.helper", ""}, {"core.askPass", env["GIT_ASKPASS"]}} {
			if _, err := a.git(ctx, target, env, "config", "--local", "--replace-all", kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	if initial {
		if _, err := a.git(ctx, target, env, "config", "--local", "--replace-all", "credential.useHttpPath", "true"); err != nil {
			return err
		}
	} else {
		value, err := a.git(ctx, target, env, "config", "--local", "--get", "credential.useHttpPath")
		if err != nil || string(value) != "true" {
			return errors.New("Git credential path scope differs from connection binding")
		}
	}

	if !initial {
		askpass, err := a.git(ctx, target, env, "config", "--local", "--get", "core.askPass")
		if err != nil || string(askpass) != env["GIT_ASKPASS"] && string(askpass) != filepath.Join(access.ResourceDir, "git-askpass.sh") {
			return errors.New("Git askpass differs from room binding; explicit application required")
		}
		helpers, err := a.git(ctx, target, env, "config", "--local", "--get-all", "credential.helper")
		if err != nil || len(helpers) != 0 {
			return errors.New("Git credential helpers differ from room binding; explicit application required")
		}
	}
	name, err := a.git(ctx, target, env, "config", "--get", "user.name")
	if err != nil {
		if !missingGitConfig(err) {
			return err
		}
		name = nil
	}
	email, err := a.git(ctx, target, env, "config", "--get", "user.email")
	if err != nil {
		if !missingGitConfig(err) {
			return err
		}
		email = nil
	}
	if len(name) > 0 && len(email) > 0 {
		return nil
	}
	if !initial {
		return errors.New("Git author missing; explicit workspace application required")
	}
	userName, userEmail, err := access.Author(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(userName) == "" || strings.TrimSpace(userEmail) == "" || !utf8.ValidString(userName+userEmail) || strings.ContainsAny(userName+userEmail, "\r\n\x00") {
		return errors.New("invalid workspace author")
	}
	for _, kv := range [][2]string{{"user.name", userName}, {"user.email", userEmail}} {
		if kv[0] == "user.name" && len(name) > 0 || kv[0] == "user.email" && len(email) > 0 {
			continue
		}
		if _, err := a.git(ctx, target, env, "config", "--local", "--replace-all", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

func (a Adapter) git(ctx context.Context, dir string, env map[string]string, args ...string) ([]byte, error) {
	prefix := []string{"-c", "http.followRedirects=false"}
	if env["GIT_ASKPASS"] != "" {
		prefix = append(prefix, "-c", "credential.helper=", "-c", "credential.useHttpPath=true")
	}
	return a.Run(ctx, dir, env, "git", append(prefix, args...)...)
}

func (a Adapter) Check(ctx context.Context, target string, initial bool) (bool, error) {
	matchesURL := func(value []byte) bool {
		return string(value) == a.Source.URL || string(value) == a.cloneURL()
	}
	entries, err := os.ReadDir(target)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(entries) == 0 {
		if initial {
			return false, nil
		}
		return false, errors.New("member clone is missing; explicit workspace application required")
	}
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(filepath.Join(target, ".git"))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("nonempty workspace is not a managed clone; preserve and inspect it")
	}
	// Reject symlinked Git metadata before running commands that might mutate it.
	err = filepath.WalkDir(filepath.Join(target, ".git"), func(_ string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type()&os.ModeSymlink != 0 {
			return errors.New("unsafe Git metadata")
		}
		return nil
	})
	if err != nil {
		return false, errors.New("unsafe Git metadata; preserve and inspect it")
	}
	top, err := a.git(ctx, target, nil, "rev-parse", "--show-toplevel")
	actual, _ := filepath.EvalSymlinks(target)
	if err != nil || string(top) != actual {
		return false, errors.New("incomplete clone; preserve and inspect it")
	}
	origin, err := a.git(ctx, target, nil, "config", "--local", "--get-all", "remote.origin.url")
	if err != nil || !matchesURL(origin) {
		return false, errors.New("workspace origin mismatch; preserve and inspect it")
	}
	// Also check effective URL rewriting locally; do not contact the remote.
	origin, err = a.git(ctx, target, nil, "remote", "get-url", "--all", "origin")
	if err != nil || !matchesURL(origin) {
		return false, errors.New("workspace effective origin mismatch; preserve and inspect it")
	}
	push, err := a.git(ctx, target, nil, "remote", "get-url", "--push", "--all", "origin")
	if err != nil || !matchesURL(push) {
		return false, errors.New("workspace push origin mismatch; preserve and inspect it")
	}
	return true, nil
}

func missingGitConfig(err error) bool {
	var exit interface{ ExitCode() int }
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

// NetworkCommand classifies only the preparation commands owned by this
// adapter. The credential bridge uses it to avoid remote refresh for local work.
func NetworkCommand(executable string, args []string) bool {
	if executable != "git" {
		return false
	}
	for len(args) >= 2 && args[0] == "-c" {
		args = args[2:]
	}
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "clone", "fetch", "push", "ls-remote":
		return true
	}
	return false
}
