//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/app/platformtools"
	"github.com/go-aie/teamx/internal/platform"
)

type observedToolRuntime struct {
	platformtools.Runtime
	calls *atomic.Int32
}

func (r observedToolRuntime) Run(ctx context.Context, entry string, args []string) ([]byte, error) {
	r.calls.Add(1)
	return r.Runtime.Run(ctx, entry, args)
}

func TestPlatformEntryUsesSelectedRuntimeAndHelpPreservesLazyAccess(t *testing.T) {
	h := newLarkHarness(t)
	bindCLITool(t, h.m, h.room.RoomIdentity, h.members[0], h.c, "lark", 0)
	var calls atomic.Int32
	var factories []platforms.Factory
	for _, definition := range h.m.platformCatalog.Definitions() {
		if definition.Tool != nil && definition.Type.Provider == "lark" {
			bind := definition.Tool.Bind
			definition.Tool.Bind = func(host platformtools.Host) platformtools.Runtime { return observedToolRuntime{bind(host), &calls} }
		}
		factories = append(factories, func() platforms.Definition { return definition })
	}
	catalog, err := platforms.NewCatalog(factories...)
	if err != nil || calls.Load() != 0 {
		t.Fatal(err, calls.Load())
	}
	h.m.platformCatalog = catalog
	path := filepath.Join(h.m.store.dir, "platform-connections.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Public exact-entry help needs the connection identity, but must not
	// resolve execution access, inspect the executable or load image permissions.
	file, err := h.m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	for i := range file.Platforms {
		if file.Platforms[i].ID == h.c.ID {
			file.Platforms[i].IdentityBinding = nil
			file.Platforms[i].Validation = platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: "credential_unavailable"}
			file.Platforms[i].CLIProfile.Executable = "/missing/lark-cli"
		}
	}
	if err := h.m.store.save(path, file, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.platforms.Load(); err != nil {
		t.Fatal("invalid fixture snapshot", err)
	}
	defer func() {
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Error(err)
		}
	}()
	client, endpoint := toolPipeServer(t, h.m)
	env := cliToolEnv(endpoint, h.svc, h.members[0])
	for _, args := range [][]string{{"--help"}, {"read", "--help"}, {"actions", "resolve", "--help"}, {"actions", "execute", "--help"}} {
		var stdout, stderr bytes.Buffer
		if err := runToolsWithClient(t.Context(), append([]string{"run", h.c.ToolEntryID}, args...), bytes.NewReader(nil), &stdout, &stderr, env, client); err != nil || !strings.Contains(stdout.String(), h.c.ToolEntryID) {
			t.Fatal(args, err, stdout.String(), stderr.String())
		}
	}
	if calls.Load() != 4 || h.m.platformCalls.Len() != 0 {
		t.Fatal("selected runtime or call settlement bypassed", calls.Load(), h.m.platformCalls.Len())
	}
	var stdout, stderr bytes.Buffer
	if err := runToolsWithClient(t.Context(), []string{"run", h.c.ToolEntryID, "read", "--object", "ref_invalid", "--target", "ref_invalid"}, bytes.NewReader(nil), &stdout, &stderr, env, client); err == nil {
		t.Fatal("execution skipped account admission")
	}
	if calls.Load() != 4 || h.m.platformCalls.Len() != 0 {
		t.Fatal("failed admission reached runtime or leaked call")
	}
	// Preserve the other side of the lazy lookup contract: public exact entries
	// require a valid connection snapshot; private type help uses only bindings.
	if err := os.WriteFile(path, []byte("unreadable account snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := runToolsWithClient(t.Context(), []string{"run", h.c.ToolEntryID, "--help"}, bytes.NewReader(nil), &stdout, &stderr, env, client); err == nil {
		t.Fatal("public help bypassed connection identity")
	}
	if calls.Load() != 4 {
		t.Fatal("failed public lookup entered runtime")
	}
	request := toolsRequest{ServiceInfo: h.svc.info, Caller: agentCaller(h.members[0].AgentID, h.members[0].MembershipID), Tool: "lark", platformID: h.c.ID}
	prepared, err := platformtools.Prepare(t.Context(), request.Tool, []string{"--help"}, catalog.ToolRuntime("lark"), builtinToolHost{h.m, request})
	if err != nil {
		t.Fatal(err)
	}
	data, err := prepared.Run()
	prepared.Close()
	if err != nil || !strings.Contains(string(data), "tools run lark read") || calls.Load() != 5 || h.m.platformCalls.Len() != 0 {
		t.Fatal("private help read connection/account state", err, calls.Load())
	}
}
