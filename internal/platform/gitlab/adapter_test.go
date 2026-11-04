package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExistingSharedWorkspaceResourcesRemainFrozen(t *testing.T) {
	dir := t.TempDir()
	b := Binding{Version: 1, RoomID: "room", RoomInstance: "instance", ConnectionID: strings.Repeat("a", 32), ConnectionGeneration: strings.Repeat("b", 32), ProjectID: 7, URL: "https://gitlab.invalid", Project: "group/project"}
	write := func(path string, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0600)
	}
	run := func(context.Context, string, url.Values, http.Header, int64) (APIResponse, error) {
		return APIResponse{Data: []byte(`{"id":7,"path_with_namespace":"group/project","http_url_to_repo":"https://gitlab.invalid/group/project.git"}`)}, nil
	}
	if _, err := PrepareGit(context.Background(), dir, b, true, write, run); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile("testdata/frozen-shared-git-usage.md")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "USAGE.md")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	unexpectedWrite := func(string, any) error { t.Fatal("rewrote frozen binding"); return nil }
	unexpectedQuery := func(context.Context, string, url.Values, http.Header, int64) (APIResponse, error) {
		t.Fatal("revisit queried remote")
		return APIResponse{}, nil
	}
	got, err := PrepareGit(context.Background(), dir, b, false, unexpectedWrite, unexpectedQuery)
	if err != nil || got.Instructions != string(old) {
		t.Fatal("known frozen Workspace rejected", err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, old) {
		t.Fatal("frozen file changed", err)
	}
	if err := os.WriteFile(path, append(old, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareGit(context.Background(), dir, b, false, unexpectedWrite, unexpectedQuery); err == nil {
		t.Fatal("accepted unknown modification of historical resource")
	}
}

func TestAdapterFrozenResourcesAndBindingChecks(t *testing.T) {
	tools := filepath.Join(t.TempDir(), "tools")
	binding := Binding{Version: 1, RoomID: "room", RoomInstance: "generation", URL: "https://gitlab.invalid", Project: "group/project", TokenEnv: "ROOM_PAT"}
	calls := 0
	run := func(context.Context, string, url.Values, http.Header, int64) (APIResponse, error) {
		calls++
		return APIResponse{Data: []byte(`{"id":7,"path_with_namespace":"group/project","http_url_to_repo":"https://gitlab.invalid/group/project.git"}`)}, nil
	}
	write := func(path string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0600)
	}
	prepared, err := Prepare(context.Background(), tools, binding, true, write, run)
	if err != nil || calls != 1 || prepared.Env["TEAMX_GITLAB_TOOLS"] != tools || !strings.Contains(prepared.Instructions, "tools run gitlab --help") {
		t.Fatal(prepared, err, calls)
	}
	for _, entry := range []string{"gitlab.py", "todos.py", "__pycache__", "test_gitlab.py", "adapter_test.go"} {
		if _, err := os.Stat(filepath.Join(tools, entry)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("distributed test code")
		}
	}
	// The room owns its frozen instructions; no upgrade writes on return visits.
	os.WriteFile(filepath.Join(tools, "USAGE.md"), []byte("frozen"), 0600)
	prepared, err = Prepare(context.Background(), tools, binding, false, write, run)
	if err != nil || prepared.Instructions != "frozen" || calls != 1 {
		t.Fatal(prepared, err, calls)
	}
	changed := binding
	changed.Project = "other/project"
	if _, err := Prepare(context.Background(), tools, changed, false, write, run); err == nil {
		t.Fatal("accepted different effective binding")
	}
	os.Remove(filepath.Join(tools, "git-askpass.sh"))
	if _, err := Prepare(context.Background(), tools, binding, false, write, run); err == nil {
		t.Fatal("reinstalled missing tool on revisit")
	}
	if calls != 1 {
		t.Fatal("queried remote on revisit")
	}
}

func TestAdapterRejectsUnsafeResourceAndWrongProject(t *testing.T) {
	for _, kind := range []string{"symlink", "empty", "foreign-project", "write-failure"} {
		t.Run(kind, func(t *testing.T) {
			tools := t.TempDir()
			if kind == "symlink" {
				os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(tools, "git-askpass.sh"))
			}
			if kind == "empty" {
				os.WriteFile(filepath.Join(tools, "git-askpass.sh"), nil, 0600)
			}
			binding := Binding{Version: 1, URL: "https://gitlab.invalid", Project: "group/project", TokenEnv: "ROOM_PAT"}
			run := func(context.Context, string, url.Values, http.Header, int64) (APIResponse, error) {
				return APIResponse{Data: []byte(`{"id":8,"path_with_namespace":"other/project","http_url_to_repo":"https://gitlab.invalid/other/project.git"}`)}, nil
			}
			write := func(path string, v any) error {
				if kind == "write-failure" {
					return errors.New("disk failure")
				}
				data, _ := json.Marshal(v)
				return os.WriteFile(path, data, 0600)
			}
			if _, err := Prepare(context.Background(), tools, binding, true, write, run); err == nil {
				t.Fatal("unsafe provider preparation succeeded")
			}
		})
	}
}

func TestFrozenLegacyFilesRemainUnchangedAndUseGoAskpass(t *testing.T) {
	dir := t.TempDir()
	b := eventTestBinding()
	write := func(path string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0600)
	}
	read := func(context.Context, string, url.Values, http.Header, int64) (APIResponse, error) {
		return APIResponse{Data: []byte(`{"id":7,"path_with_namespace":"group/project","http_url_to_repo":"https://gitlab.example.com/group/project.git"}`)}, nil
	}
	if _, err := Prepare(context.Background(), dir, b, true, write, read); err != nil {
		t.Fatal(err)
	}
	// Frozen artifacts are only retained. Even an old helper is overridden by
	// the current Go invocation; it is not interpreted as a query backend.
	old := []byte("#!/bin/sh\nexec python3 old-frozen-script\n")
	os.WriteFile(filepath.Join(dir, "git-askpass.sh"), old, 0700)
	os.WriteFile(filepath.Join(dir, "gitlab.py"), []byte("frozen evidence"), 0600)
	got, err := Prepare(context.Background(), dir, b, false, func(string, any) error { t.Fatal("revisit wrote binding"); return nil }, nil)
	if err != nil || got.Env["GIT_ASKPASS"] == "" || strings.Contains(got.Env["GIT_ASKPASS"], "python") {
		t.Fatal(got, err)
	}
	actual, _ := os.ReadFile(filepath.Join(dir, "git-askpass.sh"))
	if !bytes.Equal(actual, old) {
		t.Fatal("rewrote frozen askpass")
	}
	actual, _ = os.ReadFile(filepath.Join(dir, "gitlab.py"))
	if string(actual) != "frozen evidence" {
		t.Fatal("rewrote frozen Python")
	}
}
