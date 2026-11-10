package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Binding contains only the verified connection and project references.
// Access tokens are supplied by the service for each bounded operation.
type Binding struct {
	PlatformID            string `json:"platform_id,omitempty"`
	PlatformGeneration    string `json:"platform_generation,omitempty"`
	PlatformVersion       uint64 `json:"platform_version,omitempty"`
	CredentialVersion     uint64 `json:"credential_version,omitempty"`
	CredentialFingerprint string `json:"credential_fingerprint,omitempty"`
	ProjectID             int64  `json:"project_id,omitempty"`
	Version               int    `json:"version"`
	RoomID                string `json:"room_id"`
	RoomInstance          string `json:"room_instance"`
	URL                   string `json:"url"`
	Project               string `json:"project"`
}

type Result struct {
	Env          map[string]string
	Instructions string
}

// Prepare installs missing resources only during an explicit application. A
// revisit requires the frozen copy and matching binding, with no API requests.
// The caller owns lifecycle serialization, directory checks and durable writes.
func Prepare(ctx context.Context, tools string, b Binding, initial bool, write func(string, any) error, read ReadAPI) (Result, error) {
	return prepare(ctx, tools, b, initial, write, read, "tools/")
}

func PrepareGit(ctx context.Context, tools string, b Binding, initial bool, write func(string, any) error, read ReadAPI) (Result, error) {
	return prepare(ctx, tools, b, initial, write, read, "tools/git-only/")
}

func prepare(ctx context.Context, tools string, b Binding, initial bool, write func(string, any) error, read ReadAPI, resourceRoot string) (Result, error) {
	if initial {
		if err := os.MkdirAll(tools, 0700); err != nil {
			return Result{}, err
		}
	}
	for _, name := range []string{"git-askpass.sh", "USAGE.md"} {
		target := filepath.Join(tools, name)
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) && initial {
			data, _ := workspaceResource(resourceRoot, name)
			mode := os.FileMode(0600)
			if name == "git-askpass.sh" {
				mode = 0700
			}
			if err := installResource(target, data, mode); err != nil {
				return Result{}, err
			}
		} else if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() == 0 || info.Size() > 1<<20 || name == "git-askpass.sh" && info.Mode()&0100 == 0 {
			return Result{}, errors.New("GitLab resource missing or unsafe; preserve and inspect it")
		}
	}
	// Reuse only the current immutable resources; no old Python or hash aliases.
	for _, name := range []string{"git-askpass.sh", "USAGE.md"} {
		want, err := workspaceResource(resourceRoot, name)
		if err != nil {
			return Result{}, err
		}
		actual, err := os.ReadFile(filepath.Join(tools, name))
		if err != nil || !bytes.Equal(actual, want) {
			return Result{}, errors.New("GitLab resource differs from current installation")
		}
	}

	target := filepath.Join(tools, "binding.json")
	if initial {
		if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
			return Result{}, errors.New("unsafe GitLab binding")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Result{}, err
		}
		if err := write(target, b); err != nil {
			return Result{}, err
		}
	} else {
		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 32<<10 {
			return Result{}, errors.New("GitLab binding missing or unsafe")
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return Result{}, err
		}
		var saved Binding
		if json.Unmarshal(data, &saved) != nil || saved != b {
			return Result{}, errors.New("GitLab binding differs from effective configuration")
		}
	}
	cli, err := os.Executable()
	if err != nil {
		return Result{}, errors.New("workspace CLI unavailable")
	}
	// Git executes GIT_ASKPASS as one program path (not a shell command).
	// TeamX recognizes Git's prompt argv and reads the private binding under
	// TEAMX_GITLAB_TOOLS. The launcher owns credential access.
	env := map[string]string{"TEAMX_GITLAB_TOOLS": tools, "GIT_ASKPASS": cli, "GIT_TERMINAL_PROMPT": "0"}

	if initial {
		if read == nil {
			return Result{}, errors.New("GitLab workspace query unavailable")
		}
		if _, err := readProject(ctx, b, read); err != nil {
			return Result{}, err
		}
	}

	usage, err := os.ReadFile(filepath.Join(tools, "USAGE.md"))
	if err != nil {
		return Result{}, err
	}
	return Result{Env: env, Instructions: string(usage)}, nil
}

func workspaceResource(root, name string) ([]byte, error) {
	if name == "git-askpass.sh" {
		return resources.ReadFile("tools/git-askpass.sh")
	}
	return resources.ReadFile(root + name)
}

// Publish a complete, synced copy without overwriting an existing room resource.
func installResource(target string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(target), ".resource-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(f.Name(), target); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
