// Package platforms contains platform application logic, static assembly and
// in-flight call tracking. Entry resolution uses caller-supplied snapshots;
// it performs no I/O and grants no execution access.
package platforms

import (
	"iter"

	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

type EntryErrorCode string

const (
	EntryNotFound            EntryErrorCode = "entry_not_found"
	EntryAmbiguous           EntryErrorCode = "entry_ambiguous"
	EntryConnectionsRequired EntryErrorCode = "entry_connections_required"
	EntryProviderAlias       EntryErrorCode = "entry_provider_alias"
)

// EntryError is a resolution result. Transport and admission adapters retain
// their own error mapping, including the distinction between public and private calls.
type EntryError struct {
	Code EntryErrorCode
	Tool string
}

func (e *EntryError) Error() string { return string(e.Code) + ": " + e.Tool }

// ToolQuery describes a lookup within already verified member bindings.
// PlatformID is optional routing evidence supplied by a private adapter.
type ToolQuery struct {
	AgentID, MembershipID string
	Tool, PlatformID      string
}

type ToolEntry struct {
	Binding    tool.Binding
	Definition tool.Definition
}

// BoundTools preserves binding order and ignores unknown definitions.
// Only active bindings belong here; disabled historical choices are not grants.
func BoundTools(bindings []tool.Binding, definitions []tool.Definition, agentID, membershipID string) iter.Seq[ToolEntry] {
	return func(yield func(ToolEntry) bool) {
		for _, b := range bindings {
			if b.AgentID != agentID || b.MembershipID != membershipID {
				continue
			}
			for _, d := range definitions {
				if d.Name == b.Tool {
					if !yield(ToolEntry{Binding: b, Definition: d}) {
						return
					}
					break
				}
			}
		}
	}
}

// MatchesConnection checks saved identity, not credentials or runtime capability.
func (e ToolEntry) MatchesConnection(p registry.Record) bool {
	return p.ID == e.Binding.PlatformID && p.Provider == e.Definition.Provider && p.Generation == e.Binding.PlatformGeneration
}

// CheckPublicToolEntry rejects provider aliases even for a single account.
// Private adapters may still resolve an implementation type with routing evidence.
func CheckPublicToolEntry(definitions []tool.Definition, name string) error {
	for _, d := range definitions {
		if d.Provider != "" && name == d.Name {
			return &EntryError{Code: EntryProviderAlias, Tool: name}
		}
	}
	return nil
}

// ResolveToolEntry selects one binding, preserving the original lookup order.
// A nil connections snapshot asks the caller to load it only if matching reaches
// a platform binding whose implementation type differs from the requested tool.
// Retry with that snapshot under the same admission gate. This preserves read and
// error order even for unrelated bindings. A non-nil empty snapshot means loaded.
//
// A private type lookup deliberately needs no connection snapshot. Connection
// generation and account access must still be checked by execution admission.
func ResolveToolEntry(bindings []tool.Binding, definitions []tool.Definition, q ToolQuery, connections *registry.Snapshot) (ToolEntry, error) {
	var selected ToolEntry
	found := false
	for entry := range BoundTools(bindings, definitions, q.AgentID, q.MembershipID) {
		b := entry.Binding
		if q.PlatformID != "" && b.PlatformID != q.PlatformID {
			continue
		}
		matches := q.Tool == b.Tool
		if !matches && b.PlatformID != "" {
			if connections == nil {
				return selected, &EntryError{Code: EntryConnectionsRequired, Tool: q.Tool}
			}
			for _, p := range connections.Platforms {
				if entry.MatchesConnection(p) && p.ToolEntryID == q.Tool {
					matches = true
				}
			}
		}
		if !matches {
			continue
		}
		if found {
			return ToolEntry{}, &EntryError{Code: EntryAmbiguous, Tool: q.Tool}
		}
		selected, found = entry, true
	}
	if !found {
		return selected, &EntryError{Code: EntryNotFound, Tool: q.Tool}
	}
	return selected, nil
}
