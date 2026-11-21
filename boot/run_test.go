package boot_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-aie/teamx/boot"
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
