package room

import (
	"context"
	"runtime"
	"slices"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Binding storage keeps the implementation type and platform identity. The
// stable public entry is owned by the platform registry, never by its name.
func (m *roomManager) resolveToolBindingLocked(f toolBindingsFile, req toolsRequest) (tool.Binding, tool.Definition, error) {
	var selected tool.Binding
	var definition tool.Definition
	var file registry.Snapshot
	loaded := false
	found := false
	for _, b := range f.Bindings {
		if b.AgentID != req.Caller.ID || b.MembershipID != req.Caller.MembershipID || req.platformID != "" && b.PlatformID != req.platformID {
			continue
		}
		d, err := m.toolDefinition(b.Tool)
		if err != nil {
			continue
		}
		matches := req.Tool == b.Tool // private adapter calls may supply type + evidence
		if !matches && b.PlatformID != "" {
			if !loaded {
				file, err = m.platforms.Load()
				if err != nil {
					return selected, definition, err
				}
				loaded = true
			}
			for _, p := range file.Platforms {
				if p.ID == b.PlatformID && p.Provider == d.Provider && p.Generation == b.PlatformGeneration && p.ToolEntryID == req.Tool {
					matches = true
				}
			}
		}
		if !matches {
			continue
		}
		if found {
			return tool.Binding{}, tool.Definition{}, conflictReason("tool", "platform is ambiguous", "Use the exact entry from teamx tools list.")
		}
		selected, definition, found = b, d, true
	}
	if !found {
		return selected, definition, ErrRequest
	}
	return selected, definition, nil
}

// Public run/skill never accepts a provider alias, even for one platform.
func (m *roomManager) checkToolEntryLocked(req toolsRequest) error {
	for _, d := range m.toolDefinitions() {
		if d.Provider != "" && req.Tool == d.Name {
			return invalidField("tool", "use an exact entry from teamx tools list")
		}
	}
	return nil
}

func (m *roomManager) toolAvailability(file registry.Snapshot, p registry.Record, b tool.Binding, d tool.Definition, s *toolSelection) {
	defer func() { slices.Sort(s.Capabilities) }()
	account, _, err := registry.ResolveAccessAccount(file, p)
	if err != nil {
		s.Status = "unavailable"
		s.UnavailableReason = "account_unavailable"
		return
	}
	executable := d.Executable
	if (d.Provider == "lark" || d.Provider == "scion") && account.CLIProfile != nil {
		executable = account.CLIProfile.Executable
	}
	available := tool.ExecutableAvailable(executable) && tool.DependenciesAvailable(d)
	if !d.InternalOnly && available {
		s.Capabilities = append(s.Capabilities, "run")
	}
	if !available {
		s.UnavailableReason = "executable_unavailable"
	}
	// GitLab's HTTP adapters do not depend on its external CLI.
	if d.Provider == "gitlab" || d.Provider == "lark" && available {
		s.Capabilities = append(s.Capabilities, "read")
		if d.Provider == "lark" && runtime.GOOS != "darwin" {
			s.ActionDisabledReason = "Account-pinned actions are unavailable on this server."
			return
		}
		if caps, err := m.referenceCapabilities(d.Provider); err == nil {
			for _, cap := range caps.Actions {
				if cap.Spec.DisabledReason != "" {
					continue
				}
				if cap.Spec.RequiresAuthorization {
					granted := false
					if b.ActionAuthorization != nil {
						for _, g := range b.ActionAuthorization.Grants {
							if g.Operation == string(cap.Spec.Operation) {
								granted = true
							}
						}
					}
					if cap.Spec.BindingEvidence != "" {
						e := b.Evidence(cap.Spec.BindingEvidence)
						if e == nil || !e.Current(cap.Spec.BindingEvidence, platform.AccessContext{Account: account.Identity, CredentialVersion: account.CredentialVersion}, e.Target) {
							granted = false
						}
					}
					if !granted {
						continue
					}
				}
				s.Capabilities = append(s.Capabilities, string(cap.Spec.Operation))
			}
		}
	}
}

func entrySkill(d tool.Definition, entry string) ([]byte, error) {
	data, err := tool.ReadSkill(d)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "tools run "+d.Name, "tools run "+entry)
	text = strings.ReplaceAll(text, "tools skill "+d.Name, "tools skill "+entry)
	return []byte(text), nil
}

// An explicit entry constrains all nested built-in operations, including calls
// that restore a saved reference. A mismatch is rejected before admission.
type toolEntryContextKey struct{}
type toolEntryContext struct{ entry, platformID, provider string }

func (m *roomManager) withToolEntry(ctx context.Context, req toolsRequest) (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkToolMemberLocked(ctx, req); err != nil {
		return ctx, err
	}
	// Workspace Git retains its independent repository admission contract.
	if req.Tool == "workspace-git" {
		return ctx, nil
	}
	if err := m.checkToolEntryLocked(req); err != nil {
		return ctx, err
	}
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return ctx, err
	}
	b, d, err := m.resolveToolBindingLocked(f, req)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, toolEntryContextKey{}, toolEntryContext{req.Tool, b.PlatformID, d.Provider}), nil
}

func constrainToolRequest(ctx context.Context, req toolsRequest) (toolsRequest, error) {
	if entry, ok := ctx.Value(toolEntryContextKey{}).(toolEntryContext); ok {
		if req.platformID != "" && req.platformID != entry.platformID || req.Tool != entry.provider && req.Tool != entry.entry {
			return req, ErrIdentity
		}
		req.Tool, req.platformID = entry.entry, entry.platformID
	}
	return req, nil
}

// Source envelopes retain their subscription identity. Resolve that exact
// saved association before consulting any member binding, including retired
// sources; never guess an account from the native object or provider alone.
func (m *roomManager) sourcePlatformLocked(owner RoomIdentity, source platform.SourceRef) (string, error) {
	for _, state := range m.notifications.StatesLocked(owner) {
		meta := state.Metadata()
		if meta.Key.SubscriptionID == source.SubscriptionID && meta.Key.Source.Provider == source.Provider && meta.Key.Source.Channel == source.Channel {
			return meta.PlatformID, nil
		}
	}
	// Imported historical envelopes may outlive the saved source. Their exact
	// native recipient and instance can resolve a unique account, but a missing
	// recipient or duplicate platform is ambiguous and must never pick a default.
	if !source.Reference.RecipientID.Valid() {
		return "", ErrIdentity
	}
	file, err := m.platforms.Load()
	if err != nil {
		return "", err
	}
	id := ""
	for _, p := range file.Platforms {
		a, _, err := registry.ResolveAccessAccount(file, p)
		if err != nil || a.Identity.Provider != source.Provider || a.Identity.Instance != source.Instance || a.Identity.ID != source.Reference.RecipientID || source.Reference.AccountTenant.Valid() && a.Identity.Tenant != source.Reference.AccountTenant {
			continue
		}
		if id != "" {
			return "", ErrIdentity
		}
		id = p.ID
	}
	if id == "" {
		return "", ErrIdentity
	}
	return id, nil
}
