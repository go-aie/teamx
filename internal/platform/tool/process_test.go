package tool

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func processScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestProcessEnvironmentStreamsArgsAndExit(t *testing.T) {
	t.Setenv("MODEL_API_KEY", "must-not-inherit")
	t.Setenv("GITLAB_TOKEN", "operator-token")
	t.Setenv("CI_JOB_TOKEN", "operator-job-token")
	t.Setenv("GLAB_CONFIG_DIR", "operator-config")
	bin := processScript(t, `test -z "$MODEL_API_KEY$CI_JOB_TOKEN$GLAB_CONFIG_DIR" || exit 91
 test "$GITLAB_TOKEN" = selected-token || exit 92
 test "$GITLAB_HOST" = https://selected.invalid || exit 93
 test "$HOME" != "`+os.Getenv("HOME")+`" || exit 94
 printf '%s\n' "$@"
 cat
 printf 'error-bytes' >&2
 exit 23
`)
	var out, errOut bytes.Buffer
	p := Process{Definition: Definition{Executable: bin, Environment: map[string]string{"GITLAB_TOKEN": "token", "GITLAB_HOST": "instance"}}, Input: bytes.NewBufferString("input\x00bytes"), Output: &out, Error: &errOut, Values: map[string]string{"token": "selected-token", "instance": "https://selected.invalid"}}
	if code := p.Run(context.Background(), []string{"api", "a b", "--", "$HOME"}); code != 23 {
		t.Fatal(code, errOut.String())
	}
	if out.String() != "api\na b\n--\n$HOME\ninput\x00bytes" || errOut.String() != "error-bytes" {
		t.Fatal(out.String(), errOut.String())
	}
}
func TestProcessCancelAndMissingExecutable(t *testing.T) {
	var out, errOut bytes.Buffer
	p := Process{Definition: Definition{Executable: processScript(t, "sleep 30 &\nwait\n")}, Output: &out, Error: &errOut}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if code := p.Run(ctx, nil); code != 130 {
		t.Fatal(code)
	}
	p.Definition.Executable = filepath.Join(t.TempDir(), "missing")
	errOut.Reset()
	if code := p.Run(context.Background(), nil); code != 127 {
		t.Fatal(code)
	}
	for _, want := range []string{"unavailable on TeamX server", "restart TeamX", "Agent shell PATH/env changes do not affect server tools"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatal(errOut.String())
		}
	}
	if strings.Contains(errOut.String(), p.Definition.Executable) {
		t.Fatal("private executable path leaked")
	}
}
func TestHelpDoesNotInspectArgumentsAfterSeparator(t *testing.T) {
	for _, args := range [][]string{nil, {"help", "issue"}, {"work-items", "close", "--help"}, {"version"}} {
		if !Help(args) {
			t.Fatal(args)
		}
	}
	if Help([]string{"api", "--", "--help"}) {
		t.Fatal("business value classified as help")
	}
	if got := Catalog(); len(got) != 3 || !got[1].InternalOnly || got[1].Executable != "" || got[1].Provider != "lark" || !reflect.DeepEqual(got[0].Environment, map[string]string{"GITLAB_HOST": "instance", "GITLAB_TOKEN": "token"}) {
		t.Fatal(got)
	}
}
