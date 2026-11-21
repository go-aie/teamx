package boot_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/boot"
	builtinplatforms "github.com/go-aie/teamx/builtin/platforms"
	"github.com/go-aie/teamx/extension"
	"github.com/go-aie/teamx/platform"
)

func TestStaticCheckKeepsEmptySelectionAndStartsNoServices(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("HOME", filepath.Join(root, "must-not-be-created"))
	for _, tools := range [][]platform.Tool{nil, {{Name: "local", Executable: "/nonexistent/tool"}}} {
		var selected *extension.Composition
		var err error
		if tools == nil {
			selected, err = extension.Compose()
		} else {
			selected, err = extension.Compose(extension.Extension{Name: "local", Tools: tools})
		}
		if err != nil {
			t.Fatal(err)
		}
		// A caller changing its declaration after Compose cannot change the catalog.
		if len(tools) > 0 {
			tools[0].Name = "mutated"
		}
		var out, stderr bytes.Buffer
		if err := boot.Run(selected, []string{"extensions", "check"}, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Tools) != len(tools) || len(tools) > 0 && got.Tools[0].Name != "local" {
			t.Fatal(out.String())
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatal("static check created runtime files", entries, err)
		}
	}
	var out bytes.Buffer
	if err := boot.Run(nil, []string{"extensions", "check"}, &out, &out); err == nil {
		t.Fatal("missing composition silently selected defaults")
	}
}

func TestRootHelpUsesSelectedComposition(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("HOME", filepath.Join(root, "must-not-be-created"))
	t.Setenv("TEAMX_ADMIN_TOKEN_FILE", filepath.Join(root, "must-not-be-read"))
	for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID", "TEAMX_INPUT_CONTRACT"} {
		t.Setenv(key, "")
	}
	for _, test := range []struct {
		name       string
		extensions []extension.Extension
		want       string
	}{
		{"empty", nil, "Selected platform declarations: none"},
		{"local", []extension.Extension{{Name: "local", Tools: []platform.Tool{{Name: "local", Executable: "/must-not-run"}}}}, "Selected tool declarations: local"},
		{"standard", builtinplatforms.Extensions(), "Selected platform declarations: gitlab, lark, scion"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, err := extension.Compose(test.extensions...)
			if err != nil {
				t.Fatal(err)
			}
			for _, member := range []bool{false, true} {
				for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
					value := ""
					if member {
						value = "bound"
					}
					t.Setenv(key, value)
				}
				var out, stderr bytes.Buffer
				if err := boot.Run(selected, []string{"--help"}, &out, &stderr); err != nil || stderr.Len() != 0 || !strings.Contains(out.String(), test.want) || strings.Contains(out.String(), "extensions check") == member {
					t.Fatal(err, out.String(), stderr.String())
				}
			}
		})
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("help created runtime files", entries, err)
	}
}
