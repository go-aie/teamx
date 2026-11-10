package gitlab

import (
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

func TestAdapterFrozenResourcesAndBindingChecks(t *testing.T) {
	tools := filepath.Join(t.TempDir(), "tools")
	binding := Binding{Version: 1, RoomID: "room", RoomInstance: "generation", URL: "https://gitlab.invalid", Project: "group/project", PlatformID: strings.Repeat("a", 32), PlatformGeneration: strings.Repeat("b", 32)}
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
	// Revisit never rewrites or queries; only current resources are accepted.
	prepared, err = Prepare(context.Background(), tools, binding, false, write, run)
	if err != nil || !strings.Contains(prepared.Instructions, "tools run gitlab --help") || calls != 1 {
		t.Fatal(prepared, err, calls)
	}
	current, err := os.ReadFile(filepath.Join(tools, "USAGE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "USAGE.md"), []byte("outdated resource"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), tools, binding, false, write, run); err == nil {
		t.Fatal("accepted outdated resource")
	}
	if err := os.WriteFile(filepath.Join(tools, "USAGE.md"), current, 0600); err != nil {
		t.Fatal(err)
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
			binding := Binding{Version: 1, URL: "https://gitlab.invalid", Project: "group/project", PlatformID: strings.Repeat("a", 32), PlatformGeneration: strings.Repeat("b", 32)}
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
