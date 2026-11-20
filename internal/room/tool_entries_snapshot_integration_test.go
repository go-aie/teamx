//go:build integration || e2e

package room

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestPlatformEntryStaleGenerationKeepsPrivateHelpAndRejectsExecution(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, connection, members := cliToolFixture(t, "exit 0")
	member := members[0]
	bindCLITool(t, m, room.RoomIdentity, member, connection, "gitlab", 0)
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := m.readToolBindingsLocked(room.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	// Model a saved binding whose connection identity has since changed.
	f.Bindings[0].PlatformGeneration = strings.Repeat("f", 32)
	if err := m.store.save(m.toolBindingsPath(room.RoomIdentity), f, 1<<20); err != nil {
		t.Fatal(err)
	}
	caller := agentCaller(member.AgentID, member.MembershipID)
	selections, err := m.selectionsLocked(f, caller)
	if err != nil || len(selections) != 1 || selections[0].UnavailableReason != "platform_changed" || len(selections[0].Capabilities) != 0 {
		t.Fatalf("stale binding list = %v, error = %v", selections, err)
	}
	public := toolsRequest{ServiceInfo: svc.info, Caller: caller, Tool: connection.ToolEntryID}
	if _, _, err := m.resolveToolBindingLocked(f, public); !errors.Is(err, ErrRequest) {
		t.Fatalf("stale public entry = %v", err)
	}
	if err := m.checkToolEntryLocked(toolsRequest{Tool: "gitlab"}); !errors.Is(err, ErrRequest) {
		t.Fatalf("public alias = %v", err)
	}
	private := public
	private.Tool, private.platformID = "gitlab", connection.ID
	if _, call, _, err := m.preparePlatformCallLocked(t.Context(), private, false); !errors.Is(err, ErrConflict) {
		if call != nil {
			m.finishPlatformCallLocked(call)
		}
		t.Fatalf("stale private execution = %v", err)
	}
	// Private help still resolves a binding without resolving its account.
	_, call, _, err := m.preparePlatformCallLocked(t.Context(), private, true)
	if err != nil {
		t.Fatal(err)
	}
	m.finishPlatformCallLocked(call)
	// Removing the active choice cannot resurrect its disabled historical copy.
	f.Disabled, f.Bindings = f.Bindings, []tool.Binding{}
	if _, _, err := m.resolveToolBindingLocked(f, private); !errors.Is(err, ErrRequest) {
		t.Fatalf("disabled binding = %v", err)
	}
}
