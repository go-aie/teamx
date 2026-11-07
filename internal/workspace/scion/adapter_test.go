package scion

import (
	"context"
	"github.com/go-aie/teamx/internal/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScionLocalIdentityAndTargetProtection(t *testing.T) {
	const origin = "https://scion.test"
	a := Adapter{Source: workspace.Source{URL: origin + "/Org/repo", Branch: "main"}}
	for _, mode := range []string{"plain", "git", "symlink", "wrong-endpoint", "wrong-repo", "format", "empty-base", "missing-dirstate", "valid"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if ok, err := a.Check(context.Background(), dir, true); err != nil || ok {
				t.Fatal(ok, err)
			}
			if _, err := a.Check(context.Background(), dir, false); err == nil {
				t.Fatal("revisit accepted missing clone")
			}
			os.Mkdir(filepath.Join(dir, ".scion"), 0700)
			state := `{"format":1,"endpoint":"https://scion.test","repo":"Org/repo","ref":"task/dirty","detached":true,"base":"` + "c1" + strings.Repeat("a", 64) + `"}`
			switch mode {
			case "plain":
				os.Remove(filepath.Join(dir, ".scion"))
				os.WriteFile(filepath.Join(dir, "keep"), []byte("keep"), 0600)
			case "git":
				os.Mkdir(filepath.Join(dir, ".git"), 0700)
			case "wrong-endpoint":
				state = strings.ReplaceAll(state, origin, "https://other.test")
			case "wrong-repo":
				state = strings.ReplaceAll(state, "Org/repo", "Org/other")
			case "format":
				state = strings.ReplaceAll(state, `"format":1`, `"format":2`)
			case "empty-base":
				state = strings.ReplaceAll(state, `"`+"c1"+strings.Repeat("a", 64)+`"`, "null")
			}
			os.WriteFile(filepath.Join(dir, ".scion/state.json"), []byte(state), 0600)
			if mode != "missing-dirstate" {
				os.WriteFile(filepath.Join(dir, ".scion/dirstate.json"), []byte(`{"format":1,"files":{}}`), 0600)
			}
			if mode == "symlink" {
				os.Symlink(t.TempDir(), filepath.Join(dir, ".scion/external"))
			}
			ok, err := a.Check(context.Background(), dir, false)
			if mode == "valid" {
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
			} else if err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
}

func TestScionCloneRequiresVerifiedInitialRef(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-ref", "detached", "sparse"} {
		t.Run(mode, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "member")
			a := Adapter{Source: workspace.Source{URL: "https://scion.test/Org/repo", Branch: "main"}}
			a.Run = func(_ context.Context, _ string, _ map[string]string, exe string, args ...string) ([]byte, error) {
				if exe != "scion" || len(args) != 5 || args[0] != "clone" || args[2] != target || args[3] != "--ref" || args[4] != "main" {
					t.Fatal(exe, args)
				}
				os.MkdirAll(filepath.Join(target, ".scion"), 0700)
				extra := ``
				ref := "main"
				switch mode {
				case "wrong-ref":
					ref = "task/other"
				case "detached":
					extra = `,"detached":true`
				case "sparse":
					extra = `,"sparse":["src"]`
				}
				os.WriteFile(filepath.Join(target, ".scion/state.json"), []byte(`{"format":1,"endpoint":"https://scion.test","repo":"Org/repo","ref":"`+ref+`","base":"c1`+strings.Repeat("a", 64)+`"`+extra+`}`), 0600)
				os.WriteFile(filepath.Join(target, ".scion/dirstate.json"), []byte(`{"format":1,"files":{}}`), 0600)
				return nil, nil
			}
			err := a.Clone(context.Background(), filepath.Dir(target), target, nil)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("accepted wrong initial clone", mode)
			}
		})
	}
}
