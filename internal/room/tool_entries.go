package room

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Snapshot I/O stays inside the existing manager gate. The pure resolver asks
// for connections only when lookup reaches a binding that needs them.
func (m *roomManager) resolveToolBindingLocked(f toolBindingsFile, req toolsRequest) (tool.Binding, tool.Definition, error) {
	query := platforms.ToolQuery{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID, Tool: req.Tool, PlatformID: req.platformID}
	definitions := m.toolDefinitions()
	entry, err := platforms.ResolveToolEntry(f.Bindings, definitions, query, nil)
	var resolution *platforms.EntryError
	if errors.As(err, &resolution) && resolution.Code == platforms.EntryConnectionsRequired {
		file, loadErr := m.platforms.Load()
		if loadErr != nil {
			return entry.Binding, entry.Definition, loadErr
		}
		entry, err = platforms.ResolveToolEntry(f.Bindings, definitions, query, &file)
	}
	return entry.Binding, entry.Definition, toolEntryError(err)
}

// Keep existing request/conflict details at the admission boundary.
func toolEntryError(err error) error {
	var resolution *platforms.EntryError
	if !errors.As(err, &resolution) {
		return err
	}
	switch resolution.Code {
	case platforms.EntryNotFound:
		return ErrRequest
	case platforms.EntryAmbiguous:
		return conflictReason("tool", "platform is ambiguous", "Use the exact entry from teamx tools list.")
	case platforms.EntryProviderAlias:
		return invalidField("tool", "use an exact entry from teamx tools list")
	default:
		return err
	}
}

func (m *roomManager) checkToolEntryLocked(req toolsRequest) error {
	return toolEntryError(platforms.CheckPublicToolEntry(m.toolDefinitions(), req.Tool))
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
	if d.Provider == "scion" && account.CLIProfile != nil {
		execution, _ := m.scionProgram(account.CLIProfile)
		d.Executable = execution.Executable
		d.Dependencies = execution.Dependencies
		d.ProcessEnvironment = execution.ProcessEnvironment
		executable = d.Executable
	}
	if d.Provider == "lark" && account.CLIProfile != nil {
		executable = m.larkClients.Executable(*account.CLIProfile)
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
		if d.Provider == "lark" && !m.larkClients.SupportsPin() {
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
				if d.Provider == "lark" {
					ready := false
					for _, checked := range m.larkPlatformCapabilitiesLocked(p).Capabilities {
						if checked.Name == string(cap.Spec.Operation) && checked.State == "ready" {
							ready = true
						}
					}
					if !ready {
						continue
					}
				}
				s.Capabilities = append(s.Capabilities, string(cap.Spec.Operation))
			}
		}
	}
}

func entrySkill(d tool.Definition, entry string) ([]byte, error) {
	return entrySkillTopic(d, entry, "")
}

func entrySkillTopic(d tool.Definition, entry, topic string) ([]byte, error) {
	data, err := tool.ReadToolSkill(d, topic)
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(data), "tools run "+d.Name, "tools run "+entry)
	text = strings.ReplaceAll(text, "tools skill "+d.Name, "tools skill "+entry)
	text = strings.ReplaceAll(text, "--tool "+d.Name, "--tool "+entry)
	if topic == "managed-send" {
		text = strings.ReplaceAll(text, "ENTRY", entry)
	}
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
		if m.config.hostTools.restricted {
			return ctx, hostToolUnavailable("managed_repository")
		}
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
