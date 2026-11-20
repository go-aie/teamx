package platforms_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestToolEntryExactAccountAndBindingIdentity(t *testing.T) {
	definitions := []tool.Definition{{Name: "gitlab", Provider: "gitlab"}, {Name: "local"}}
	a := tool.Binding{AgentID: "agent", MembershipID: "membership", Tool: "gitlab", PlatformID: "a", PlatformGeneration: "generation-a"}
	b := a
	b.PlatformID, b.PlatformGeneration = "b", "generation-b"
	connection := func(b tool.Binding, entry string) registry.Record {
		return registry.Record{Platform: platform.Platform{ID: b.PlatformID, Provider: "gitlab", ToolEntryID: entry}, Generation: b.PlatformGeneration}
	}
	for _, tc := range []struct {
		name   string
		change func(*[]tool.Binding, *registry.Snapshot, *platforms.ToolQuery)
		want   string
		code   platforms.EntryErrorCode
	}{
		{name: "two accounts exact first", want: "a"},
		{name: "two accounts exact second", change: func(_ *[]tool.Binding, _ *registry.Snapshot, q *platforms.ToolQuery) { q.Tool = "entry-b" }, want: "b"},
		{name: "routing evidence agrees", change: func(_ *[]tool.Binding, _ *registry.Snapshot, q *platforms.ToolQuery) { q.PlatformID = "a" }, want: "a"},
		{name: "routing evidence disagrees", change: func(_ *[]tool.Binding, _ *registry.Snapshot, q *platforms.ToolQuery) { q.PlatformID = "b" }, code: platforms.EntryNotFound},
		{name: "generation changed", change: func(_ *[]tool.Binding, s *registry.Snapshot, _ *platforms.ToolQuery) {
			s.Platforms[0].Generation = "next"
		}, code: platforms.EntryNotFound},
		{name: "provider changed", change: func(_ *[]tool.Binding, s *registry.Snapshot, _ *platforms.ToolQuery) {
			s.Platforms[0].Provider = "lark"
		}, code: platforms.EntryNotFound},
		{name: "connection removed", change: func(_ *[]tool.Binding, s *registry.Snapshot, _ *platforms.ToolQuery) { s.Platforms = s.Platforms[1:] }, code: platforms.EntryNotFound},
		{name: "binding disabled or retired", change: func(b *[]tool.Binding, _ *registry.Snapshot, _ *platforms.ToolQuery) { *b = (*b)[1:] }, code: platforms.EntryNotFound},
		{name: "old membership", change: func(_ *[]tool.Binding, _ *registry.Snapshot, q *platforms.ToolQuery) { q.MembershipID = "rejoined" }, code: platforms.EntryNotFound},
		{name: "another agent", change: func(_ *[]tool.Binding, _ *registry.Snapshot, q *platforms.ToolQuery) { q.AgentID = "another" }, code: platforms.EntryNotFound},
		{name: "definition removed", change: func(b *[]tool.Binding, _ *registry.Snapshot, _ *platforms.ToolQuery) { (*b)[0].Tool = "retired" }, code: platforms.EntryNotFound},
		{name: "unknown entry", change: func(_ *[]tool.Binding, _ *registry.Snapshot, q *platforms.ToolQuery) { q.Tool = "unknown" }, code: platforms.EntryNotFound},
		{name: "ambiguous private type", change: func(_ *[]tool.Binding, _ *registry.Snapshot, q *platforms.ToolQuery) { q.Tool = "gitlab" }, code: platforms.EntryAmbiguous},
		{name: "private type pinned account", change: func(_ *[]tool.Binding, _ *registry.Snapshot, q *platforms.ToolQuery) {
			q.Tool, q.PlatformID = "gitlab", "b"
		}, want: "b"},
		{name: "private type defers generation admission", change: func(_ *[]tool.Binding, s *registry.Snapshot, q *platforms.ToolQuery) {
			q.Tool, q.PlatformID = "gitlab", "a"
			s.Platforms[0].Generation = "next"
		}, want: "a"},
		{name: "ambiguous entry", change: func(b *[]tool.Binding, _ *registry.Snapshot, _ *platforms.ToolQuery) { *b = append(*b, (*b)[0]) }, code: platforms.EntryAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindings := []tool.Binding{a, b}
			snapshot := registry.Snapshot{Platforms: []registry.Record{connection(a, "entry-a"), connection(b, "entry-b")}}
			q := platforms.ToolQuery{AgentID: a.AgentID, MembershipID: a.MembershipID, Tool: "entry-a"}
			if tc.change != nil {
				tc.change(&bindings, &snapshot, &q)
			}
			entry, err := platforms.ResolveToolEntry(bindings, definitions, q, &snapshot)
			if tc.code != "" {
				var resolution *platforms.EntryError
				if !errors.As(err, &resolution) || resolution.Code != tc.code || resolution.Tool != q.Tool {
					t.Fatalf("resolution error = %v; want %s", err, tc.code)
				}
			} else if err != nil || entry.Binding.PlatformID != tc.want || entry.Definition.Name != "gitlab" {
				t.Fatalf("selected platform = %q, definition = %q, error = %v", entry.Binding.PlatformID, entry.Definition.Name, err)
			}
		})
	}
}

func TestToolEntryPublicAliasAndAccountlessTool(t *testing.T) {
	definitions := []tool.Definition{{Name: "gitlab", Provider: "gitlab"}, {Name: "local"}}
	for _, name := range []string{"entry-a", "local", "unknown"} {
		if err := platforms.CheckPublicToolEntry(definitions, name); err != nil {
			t.Fatal(err)
		}
	}
	var resolution *platforms.EntryError
	if err := platforms.CheckPublicToolEntry(definitions, "gitlab"); !errors.As(err, &resolution) || resolution.Code != platforms.EntryProviderAlias {
		t.Fatalf("public alias error = %v", err)
	}
	b := tool.Binding{AgentID: "agent", MembershipID: "membership", Tool: "local"}
	entry, err := platforms.ResolveToolEntry([]tool.Binding{b}, definitions, platforms.ToolQuery{AgentID: b.AgentID, MembershipID: b.MembershipID, Tool: "local"}, nil)
	if err != nil || entry.Definition.Name != "local" || entry.Binding.PlatformID != "" {
		t.Fatalf("accountless resolution = %v, %v", entry, err)
	}
}

func TestToolEntryConnectionReadOrder(t *testing.T) {
	definitions := []tool.Definition{{Name: "gitlab", Provider: "gitlab"}, {Name: "local"}}
	local := tool.Binding{AgentID: "agent", MembershipID: "membership", Tool: "local"}
	remote := local
	remote.Tool, remote.PlatformID, remote.PlatformGeneration = "gitlab", "a", "generation-a"
	for _, tc := range []struct {
		name     string
		bindings []tool.Binding
		query    string
		code     platforms.EntryErrorCode
	}{
		{"no binding", nil, "entry-a", platforms.EntryNotFound},
		{"private type", []tool.Binding{remote}, "gitlab", ""},
		{"public entry", []tool.Binding{remote}, "entry-a", platforms.EntryConnectionsRequired},
		{"unrelated platform still needs original read", []tool.Binding{local, remote}, "local", platforms.EntryConnectionsRequired},
		{"ambiguity precedes later read", []tool.Binding{local, local, remote}, "local", platforms.EntryAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := platforms.ToolQuery{AgentID: local.AgentID, MembershipID: local.MembershipID, Tool: tc.query}
			_, err := platforms.ResolveToolEntry(tc.bindings, definitions, q, nil)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var resolution *platforms.EntryError
			if !errors.As(err, &resolution) || resolution.Code != tc.code {
				t.Fatalf("error = %v; want %s", err, tc.code)
			}
		})
	}
	q := platforms.ToolQuery{AgentID: remote.AgentID, MembershipID: remote.MembershipID, Tool: "entry-a"}
	_, err := platforms.ResolveToolEntry([]tool.Binding{remote}, definitions, q, &registry.Snapshot{})
	var resolution *platforms.EntryError
	if !errors.As(err, &resolution) || resolution.Code != platforms.EntryNotFound {
		t.Fatalf("loaded empty snapshot = %v", err)
	}
}

func TestBoundToolsPreservesMemberOrderAndConnectionIdentity(t *testing.T) {
	a := tool.Binding{AgentID: "agent", MembershipID: "membership", Tool: "gitlab", PlatformID: "a", PlatformGeneration: "generation-a"}
	b := a
	b.PlatformID = "b"
	other := a
	other.MembershipID = "old"
	retired := a
	retired.Tool = "retired"
	entries := slices.Collect(platforms.BoundTools([]tool.Binding{other, b, retired, a}, []tool.Definition{{Name: "gitlab", Provider: "gitlab"}}, a.AgentID, a.MembershipID))
	if len(entries) != 2 || entries[0].Binding.PlatformID != "b" || entries[1].Binding.PlatformID != "a" {
		t.Fatal("member bindings lost order or included retired definitions")
	}
	p := registry.Record{Platform: platform.Platform{ID: "a", Provider: "gitlab"}, Generation: "generation-a"}
	if entries[0].MatchesConnection(p) || !entries[1].MatchesConnection(p) {
		t.Fatal("entry matched another connection")
	}
	p.Generation = "next"
	if entries[1].MatchesConnection(p) {
		t.Fatal("entry matched a replacement generation")
	}
}
