package room

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	gitlabplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/gitlab"
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func helpMemberEnv() map[string]string {
	return map[string]string{"TEAMX_ENDPOINT": "http://127.0.0.1:1", "TEAMX_ROOM_ID": "room", "TEAMX_ROOM_INSTANCE": "instance", "TEAMX_SERVICE_EPOCH": "epoch", "TEAMX_AGENT_ID": "agent", "TEAMX_MEMBERSHIP_ID": "membership"}
}

func TestRootHelpAudienceAndSelection(t *testing.T) {
	empty, err := platforms.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	local, err := platforms.NewCatalogWithTools([]platforms.Tool{{Executable: "/must-not-run", Definition: tool.Definition{Name: "local"}}})
	if err != nil {
		t.Fatal(err)
	}
	gitlab, err := platforms.NewCatalog(gitlabplatform.Definition)
	if err != nil {
		t.Fatal(err)
	}
	for name, selected := range map[string]*platforms.Catalog{"empty": empty, "local": local, "gitlab": gitlab} {
		t.Run(name, func(t *testing.T) {
			for _, member := range []bool{false, true} {
				values := map[string]string{"TEAMX_ENDPOINT": "http://127.0.0.1:1"}
				if member {
					values = helpMemberEnv()
				}
				var out bytes.Buffer
				if err := runCLIWithCatalog(t.Context(), []string{"--help"}, nil, &out, io.Discard, func(k string) string { return values[k] }, selected); err != nil {
					t.Fatal(err)
				}
				for _, maintenance := range []string{"  service ", "  serve ", "  access init", "  member add", "  platforms upgrade", "  extensions check"} {
					if strings.Contains(out.String(), maintenance) == member {
						t.Fatal("wrong help audience", member, maintenance, out.String())
					}
				}
				for _, absent := range []string{"Feishu/GitLab", "lark read", "scion", "askpass"} {
					if strings.Contains(out.String(), absent) {
						t.Fatal("unselected or internal capability advertised", absent, out.String())
					}
				}
				if name == "empty" && !strings.Contains(out.String(), "Selected tool declarations: none") || name == "local" && !strings.Contains(out.String(), "Selected tool declarations: local") || name == "gitlab" && !strings.Contains(out.String(), "Selected platform declarations: gitlab") {
					t.Fatal("selection not reflected", out.String())
				}
			}
		})
	}
	for _, missing := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
		t.Run(missing, func(t *testing.T) {
			values := helpMemberEnv()
			delete(values, missing)
			var out bytes.Buffer
			if err := runCLIWithCatalog(t.Context(), nil, nil, &out, io.Discard, func(k string) string { return values[k] }, empty); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "binding is incomplete") || strings.Contains(out.String(), "extensions check") {
				t.Fatal("partial member binding became management help", out.String())
			}
		})
	}
}

func TestScopedHelpDoesNotAccessServiceOrCredentials(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	empty, err := platforms.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	values := helpMemberEnv()
	values["TEAMX_ENDPOINT"] = server.URL
	values["TEAMX_INPUT_CONTRACT"] = inputstream.Contract
	values["TEAMX_ADMIN_TOKEN_FILE"] = filepath.Join(t.TempDir(), "absent-token")
	values["TEAMX_EXECUTION_TOKEN"] = "invalid-credential-must-not-be-read"
	values["DSH_TEAMX_INPUT_BINDING"] = "invalid-execution-must-not-be-read"
	cases := []struct {
		path, want, absent string
	}{
		{"", "Member business commands", "extensions check"},
		{"inbox", "list", "--peek"},
		{"inbox list", "--cursor", "--stream"},
		{"inbox read", "--stream", "--cursor"},
		{"platforms", "connect", "--todos-enabled"},
		{"platforms connect", "acknowledge", "--todos-enabled"},
		{"platforms connect start", "--source-seq", "--result-version"},
		{"platforms connect authorize", "same account", "--todos-enabled"},
		{"platforms connect status", "--intent authorize", "--todos-enabled"},
		{"platforms connect wait", "--timeout", "--application-id"},
		{"platforms connect cancel", "cannot undo", "--application-id"},
		{"platforms connect notify", "queued event", "--application-id"},
		{"platforms connect acknowledge", "result_version", "--application-id"},
		{"platforms selection", "update", "--chat-id"},
		{"platforms selection start", "--kind", "--result-version"},
		{"platforms selection status", "--source-seq", "--chat-id"},
		{"platforms selection update", "--version", "--result-version"},
		{"platforms selection wait", "--timeout", "--chat-id"},
		{"platforms selection cancel", "--version", "--chat-id"},
		{"platforms selection notify", "--mode", "--chat-id"},
		{"platforms selection acknowledge", "result_version", "--chat-id"},
		{"calendar", "claim", "human_source"},
		{"calendar list", "--state", "--request-file"},
		{"calendar get", "--result", "human_source"},
		{"calendar update", "human_source", "Finish JSON"},
		{"calendar claim", "--run-id", "create:"},
		{"calendar finish", "report_seq", "create:"},
	}
	for _, test := range cases {
		for _, flag := range []string{"--help", "-h"} {
			t.Run(test.path+flag, func(t *testing.T) {
				var out, stderr bytes.Buffer
				args := append(strings.Fields(test.path), flag)
				err := runCLIWithCatalog(t.Context(), args, nil, &out, &stderr, func(k string) string { return values[k] }, empty)
				if err != nil || stderr.Len() != 0 || !strings.Contains(out.String(), test.want) || strings.Contains(out.String(), test.absent) {
					t.Fatal("wrong scoped/offline help", err, stderr.String(), out.String())
				}
			})
		}
	}
	if calls.Load() != 0 {
		t.Fatal("help contacted service", calls.Load())
	}
}

func TestRootHelpDoesNotInferInputTransport(t *testing.T) {
	values := helpMemberEnv()
	values["TEAMX_INPUT_CONTRACT"] = inputstream.Contract
	text := rootHelpText(func(k string) string { return values[k] }, nil)
	for _, want := range []string{"inbox list/read", "MCP Sessions", "Session's transport instructions"} {
		if !strings.Contains(text, want) {
			t.Fatal("input protocol was mistaken for transport", text)
		}
	}
}

func TestInputCLIHelpBeforeExecutionBinding(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"list", "--limit", "1", "--help"}, {"read", "--stream", "returned-ref", "--help"}} {
		var out bytes.Buffer
		if err := runInputHTTPCLI(t.Context(), args, &out, func(k string) string {
			t.Fatalf("help inspected execution environment: %s", k)
			return ""
		}); err != nil || !strings.Contains(out.String(), "Usage: teamx inbox") {
			t.Fatal(args, err, out.String())
		}
	}
}
