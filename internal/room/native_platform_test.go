package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

// Only used while the manager gate is held.
func (h *eventHarness) stateUnlocked() gitlab.EventsState {
	s, _ := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	return s
}

type credentialDriftBody struct {
	io.Reader
	onRead func()
}

func (b *credentialDriftBody) Read(p []byte) (int, error) {
	if b.onRead != nil {
		b.onRead()
		b.onRead = nil
	}
	return b.Reader.Read(p)
}
func (b *credentialDriftBody) Close() error { return nil }

func TestWorkspaceGitCLI(t *testing.T) {
	b := gitlab.Binding{Version: 1, RoomID: "room", RoomInstance: "instance", URL: "https://gitlab.example.com", Project: "group/project", PlatformID: strings.Repeat("a", 32), PlatformGeneration: strings.Repeat("b", 32), ProjectID: 7}
	path := filepath.Join(t.TempDir(), "binding.json")
	data, _ := json.Marshal(b)
	os.WriteFile(path, data, 0600)
	var out, stderr bytes.Buffer
	env := func(k string) string {
		if k == "TEAMX_PLATFORM_TOKEN" {
			return "offline-secret"
		}
		return ""
	}
	err := runCLI(context.Background(), []string{"workspace-git", "askpass", "--binding", path, "Password for 'https://oauth2@gitlab.example.com/group/project.git': "}, &out, &stderr, env)
	if err != nil || out.String() != "offline-secret\n" || stderr.Len() != 0 {
		t.Fatal("Go askpass CLI failed", err)
	}
	out.Reset()
	err = runCLI(context.Background(), []string{"workspace-git", "askpass", "--binding", path, "Password for 'https://evil.invalid/group/project.git': "}, &out, &stderr, env)
	if err == nil || out.Len() != 0 {
		t.Fatal("foreign Git target received credentials")
	}
}

// Optional production binary proof: Git invokes the real Go CLI through the
// GIT_ASKPASS executable path. No listening socket or platform is used.
func TestProductionGitAskpassWithoutPythonOrGlab(t *testing.T) {
	bin := os.Getenv("TEAMX_TEST_CLI_BIN")
	if bin == "" {
		t.Skip("set TEAMX_TEST_CLI_BIN to the built TeamX CLI")
	}
	dir := t.TempDir()
	b := gitlab.Binding{Version: 1, RoomID: "room", RoomInstance: "instance", URL: "https://gitlab.example.com", Project: "group/project", PlatformID: strings.Repeat("a", 32), PlatformGeneration: strings.Repeat("b", 32), ProjectID: 7}
	data, _ := json.Marshal(b)
	path := filepath.Join(dir, "binding.json")
	os.WriteFile(path, data, 0600)
	// A PATH containing Git alone proves the credential helper cannot use Python
	// or glab, including through a frozen script.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	search := filepath.Join(dir, "bin")
	os.Mkdir(search, 0700)
	os.Symlink(gitPath, filepath.Join(search, "git"))

	cmd := exec.Command(gitPath, "-c", "credential.helper=", "-c", "credential.useHttpPath=true", "credential", "fill")
	cmd.Env = []string{"PATH=" + search, "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "TEAMX_PLATFORM_TOKEN=offline-secret", "GIT_ASKPASS=" + bin, "TEAMX_GITLAB_TOOLS=" + dir}
	cmd.Stdin = strings.NewReader("protocol=https\nhost=gitlab.example.com\npath=group/project.git\n\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil || !bytes.Contains(output, []byte("username=oauth2\n")) || !bytes.Contains(output, []byte("password=offline-secret\n")) {
		t.Fatal("production Git/Go askpass failed", err, strings.ReplaceAll(stderr.String(), "offline-secret", "[REDACTED]"))
	}
}
