//go:build integration || e2e

package room

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestGitlabProgramPairSurvivesInstallationSwitch(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, connection, members := cliToolFixture(t, "exit 99")
	bindCLITool(t, m, room.RoomIdentity, members[0], connection, "gitlab", 0)
	before, err := os.ReadFile(m.toolBindingsPath(room.RoomIdentity))
	if err != nil {
		t.Fatal(err)
	}
	writeRelease := func(label string) string {
		t.Helper()
		dir := t.TempDir()
		files := map[string]string{
			"teamx-gitlab": "#!/bin/sh\nprintf wrapper-" + label + "'|'\nexec \"$TEAMX_GLAB_BIN\" \"$@\"\n",
			"glab":         "#!/bin/sh\ntest -z \"${PRIVATE_SECRET-}\" || exit 91\nprintf native-" + label + "\n",
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	a, b, fallback := writeRelease("A"), writeRelease("B"), writeRelease("PATH")
	alias := filepath.Join(t.TempDir(), "current")
	if err := os.Symlink(a, alias); err != nil {
		t.Fatal(err)
	}
	m.toolCatalog = nil // Exercise the production Catalog and service composition.
	m.config.platformBinDir, err = filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatal(err)
	}
	m.config.environment = []string{"PATH=" + fallback, "PRIVATE_SECRET=never-delegate"}
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: connection.ToolEntryID, Args: []string{"--help"}}
	admit := func() (tool.Process, *platformCall) {
		t.Helper()
		m.mu.Lock()
		p, call, _, err := m.prepareToolLocked(t.Context(), req)
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		return p, call
	}
	pa, ca := admit()
	defer m.finishPlatformCall(ca)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, alias); err != nil {
		t.Fatal(err)
	}
	pb, cb := admit()
	defer m.finishPlatformCall(cb)
	// The running service keeps its physical release even for new calls. Only
	// a newly assembled service selects B; there is no in-process hot update.
	next := &roomManager{platformCatalog: m.platformCatalog, config: m.config}
	next.config.platformBinDir, err = filepath.EvalSymlinks(alias)
	if err != nil {
		t.Fatal(err)
	}
	pc := pb
	pc.Definition, err = next.toolDefinition("gitlab")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		process tool.Process
		want    string
	}{{pa, "wrapper-A|native-A"}, {pb, "wrapper-A|native-A"}, {pc, "wrapper-B|native-B"}} {
		var out, stderr bytes.Buffer
		tc.process.Output, tc.process.Error = &out, &stderr
		if code := tc.process.Run(t.Context(), req.Args); code != 0 || out.String() != tc.want {
			t.Fatal("admitted program pair changed", code, out.String(), stderr.String())
		}
	}
	// Removing an installed native program must not use the working PATH copy.
	if err := os.Remove(filepath.Join(b, "glab")); err != nil {
		t.Fatal(err)
	}
	definition, err := next.toolDefinition("gitlab")
	if err != nil || tool.DependenciesAvailable(definition) {
		t.Fatal("broken installation appeared available", err)
	}
	assertAvailability := func(want bool) {
		t.Helper()
		m.mu.Lock()
		file, loadErr := m.platforms.Load()
		selection := toolSelection{}
		if loadErr == nil && len(file.Platforms) == 1 {
			m.toolAvailability(file, file.Platforms[0], tool.Binding{}, definition, &selection)
		}
		m.mu.Unlock()
		if loadErr != nil || slices.Contains(selection.Capabilities, "run") != want || !slices.Contains(selection.Capabilities, "read") {
			t.Fatal("tool availability disagreed with native dependency or disabled HTTP", loadErr, selection)
		}
		if want && selection.UnavailableReason != "" || !want && selection.UnavailableReason != "executable_unavailable" {
			t.Fatal("wrong dependency diagnosis", selection.UnavailableReason)
		}
	}
	assertAvailability(false)
	var out, stderr bytes.Buffer
	pc.Output, pc.Error = &out, &stderr
	if code := pc.Run(t.Context(), req.Args); code != 127 || out.Len() != 0 || !strings.Contains(stderr.String(), "dependency unavailable") {
		t.Fatal("missing native reached wrapper or PATH", code, out.String(), stderr.String())
	}
	// Restoring the selected file repairs availability without rebinding members.
	if err := os.WriteFile(filepath.Join(b, "glab"), []byte("#!/bin/sh\nprintf repaired"), 0700); err != nil {
		t.Fatal(err)
	}
	if !tool.DependenciesAvailable(definition) {
		t.Fatal("dependency repair required a new binding")
	}
	assertAvailability(true)
	after, err := os.ReadFile(m.toolBindingsPath(room.RoomIdentity))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("program switch rewrote member choices", err)
	}
}
