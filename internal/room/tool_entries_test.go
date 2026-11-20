package room

import (
	"errors"
	"testing"

	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// A failed registry read must affect only lookups that previously needed it.
// The adapter must also preserve which error wins when a match, ambiguity and
// a later platform binding coexist in binding order.
func TestToolEntryAdapterLazyConnectionsAndErrorOrder(t *testing.T) {
	local := tool.Binding{AgentID: "agent", MembershipID: "membership", Tool: "local"}
	remote := local
	remote.Tool, remote.PlatformID, remote.PlatformGeneration = "gitlab", "a", "generation-a"
	unknown := remote
	unknown.Tool = "retired"
	other := remote
	other.MembershipID = "old"
	loadFailure := errors.New("registry unavailable")
	for _, tc := range []struct {
		name      string
		bindings  []tool.Binding
		tool, pin string
		want      error
		loads     int
		selected  string
	}{
		{name: "private lookup", bindings: []tool.Binding{remote}, tool: "gitlab", selected: "gitlab"},
		{name: "accountless lookup", bindings: []tool.Binding{local}, tool: "local", selected: "local"},
		{name: "missing binding", tool: "entry-a", want: ErrRequest},
		{name: "retired definition", bindings: []tool.Binding{unknown}, tool: "entry-a", want: ErrRequest},
		{name: "old membership", bindings: []tool.Binding{other}, tool: "entry-a", want: ErrRequest},
		{name: "different pinned account", bindings: []tool.Binding{remote}, tool: "entry-a", pin: "b", want: ErrRequest},
		{name: "public entry requires read", bindings: []tool.Binding{remote}, tool: "entry-a", want: loadFailure, loads: 1},
		{name: "later read wins over selected local", bindings: []tool.Binding{local, remote}, tool: "local", want: loadFailure, loads: 1, selected: "local"},
		{name: "earlier ambiguity wins over read", bindings: []tool.Binding{local, local, remote}, tool: "local", want: ErrConflict},
		{name: "private ambiguity requires no read", bindings: []tool.Binding{remote, remote}, tool: "gitlab", want: ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loads := 0
			m := &roomManager{toolCatalog: []tool.Definition{{Name: "gitlab", Provider: "gitlab"}, {Name: "local"}}}
			m.platforms = registry.New(registry.Store{Check: func(writing bool) error {
				loads++
				if writing {
					t.Fatal("entry lookup attempted a write")
				}
				return loadFailure
			}}, nil, defaultPlatformCatalog().Types())
			m.mu.Lock()
			b, d, err := m.resolveToolBindingLocked(toolBindingsFile{Bindings: tc.bindings}, toolsRequest{Caller: agentCaller(local.AgentID, local.MembershipID), Tool: tc.tool, platformID: tc.pin})
			m.mu.Unlock()
			if !errors.Is(err, tc.want) || loads != tc.loads || b.Tool != tc.selected || d.Name != tc.selected {
				t.Fatalf("binding=%q definition=%q error=%v loads=%d; want selected=%q error=%v loads=%d", b.Tool, d.Name, err, loads, tc.selected, tc.want, tc.loads)
			}
			if errors.Is(err, ErrConflict) {
				var diagnostic *requestDiagnostic
				if !errors.As(err, &diagnostic) || diagnostic.Field != "tool" || diagnostic.Message != "platform is ambiguous" || diagnostic.Hint != "Use the exact entry from teamx tools list." {
					t.Fatalf("ambiguity mapping changed: %v", err)
				}
			}
		})
	}
}

func TestToolListAdapterSkipsConnectionsWithoutKnownPlatformBindings(t *testing.T) {
	local := tool.Binding{AgentID: "agent", MembershipID: "membership", Tool: "local"}
	retired := local
	retired.Tool, retired.PlatformID, retired.PlatformGeneration = "retired", "a", "generation-a"
	m := &roomManager{toolCatalog: []tool.Definition{{Name: "local", Executable: "missing-fixture-executable"}}}
	m.platforms = registry.New(registry.Store{Check: func(bool) error {
		t.Fatal("accountless list loaded registry")
		return nil
	}}, nil, defaultPlatformCatalog().Types())
	m.mu.Lock()
	defer m.mu.Unlock()
	got, err := m.selectionsLocked(toolBindingsFile{Bindings: []tool.Binding{retired, local}}, agentCaller(local.AgentID, local.MembershipID))
	if err != nil || len(got) != 1 || got[0].Name != "local" || got[0].UnavailableReason != "executable_unavailable" {
		t.Fatalf("list = %v, error = %v", got, err)
	}
}
