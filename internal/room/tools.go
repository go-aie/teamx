package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/scion"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Bindings require explicit selection; retired read-only or received-todos
// grants never become complete account permissions automatically.
type toolBindingsFile struct {
	LegacyUnknown bool           `json:"legacy_unknown,omitempty"`
	Disabled      []tool.Binding `json:"disabled"` // nil is historical unknown intent; new writes use an array
	RoomIdentity
	Version  int            `json:"version"`
	Revision uint64         `json:"revision"`
	Bindings []tool.Binding `json:"bindings"`
}
type toolSelection struct {
	ActionAuthorization  *tool.ActionAuthorization `json:"action_authorization,omitempty"`
	ActionDisabledReason string                    `json:"action_disabled_reason,omitempty"`
	tool.Definition
	PlatformID        string   `json:"platform_id,omitempty"`
	Instance          string   `json:"instance,omitempty"`
	Username          string   `json:"username,omitempty"`
	Status            string   `json:"status,omitempty"`
	ToolType          string   `json:"tool_type"`
	PlatformName      string   `json:"platform_name,omitempty"`
	Capabilities      []string `json:"capabilities"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
	AgentID           string   `json:"agent_id"`
	MembershipID      string   `json:"membership_id"`
}
type toolsRequest struct {
	ServiceInfo
	Caller     Caller   `json:"caller"`
	Tool       string   `json:"tool,omitempty"`
	Args       []string `json:"args,omitempty"`
	platformID string   // server-owned routing evidence; never accepted over HTTP
}
type toolBindingChange struct {
	ServiceEpoch string `json:"service_epoch"`
	RoomIdentity
	Caller          Caller       `json:"caller"`
	Revision        uint64       `json:"revision"`
	Binding         tool.Binding `json:"binding"`
	Enabled         bool         `json:"enabled"`
	PlatformVersion uint64       `json:"platform_version,omitempty"`
}

func (m *roomManager) toolDefinitions() []tool.Definition {
	if m.toolCatalog != nil {
		return m.toolCatalog
	}
	return tool.Catalog()
}
func (m *roomManager) toolDefinition(name string) (tool.Definition, error) {
	for _, d := range m.toolDefinitions() {
		if d.Name == name {
			return d, nil
		}
	}
	return tool.Definition{}, ErrRequest
}
func (m *roomManager) toolBindingsPath(owner RoomIdentity) string {
	return filepath.Join(m.roomDir(owner.RoomID), "tool-bindings.json")
}
func (m *roomManager) readToolBindingsLocked(owner RoomIdentity) (toolBindingsFile, error) {
	f := toolBindingsFile{RoomIdentity: owner, Version: 1, Bindings: []tool.Binding{}}
	entry, err := m.workspaceRoom(owner)
	if err != nil || entry.Status != "ready" && entry.Status != "deleting" {
		return f, ErrIdentity
	}
	if err = m.store.check(false); err != nil {
		return f, err
	}
	err = readSnapshot(m.toolBindingsPath(owner), &f, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return toolBindingsFile{RoomIdentity: owner, Version: 1, Bindings: []tool.Binding{}}, nil
	}
	if err != nil {
		return f, err
	}
	if f.RoomIdentity != owner || f.Version != 1 || f.Revision == 0 || f.Bindings == nil || len(f.Bindings)+len(f.Disabled) > 3200 {
		return f, ErrInvalid
	}
	seen := map[[4]string]bool{}
	for i, b := range append(slices.Clone(f.Bindings), f.Disabled...) {
		key := [4]string{b.AgentID, b.MembershipID, b.Tool, b.PlatformID}
		if !validText(b.AgentID) || !safeMembershipID(b.MembershipID) || !tool.ValidName(b.Tool) || seen[key] || !b.ActionAuthorization.Valid() ||
			i < len(f.Bindings) && (b.PlatformID == "") != (b.PlatformGeneration == "") || b.PlatformID != "" && (!tool.OpaqueID(b.PlatformID) || b.PlatformGeneration != "" && !tool.OpaqueID(b.PlatformGeneration)) {
			return f, ErrInvalid
		}
		seen[key] = true
	}
	if f.Disabled == nil {
		f.LegacyUnknown = true
	}
	return f, nil
}
func (m *roomManager) checkToolMemberLocked(ctx context.Context, req toolsRequest) error {
	if m.closing {
		return ErrClosing
	}
	svc, rt := m.runtimeServiceLocked(req.RoomIdentity)
	if m.busy || rt != nil && rt.busy && !rt.resuming {
		return ErrBusy
	}
	if svc == nil || svc.closing.Load() || svc.restarting.Load() || req.ServiceInfo != svc.info || req.Caller.Kind != "agent" {
		return ErrIdentity
	}

	id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	if m.platformMemberChanges[id] {
		return ErrBusy
	}
	if target := svc.target(id.MembershipID); !target.IsZero() {
		status, err := target.Call[MemberStatus](ctx, InspectMember{id})
		if err != nil {
			return ErrIdentity
		}
		if status.Rotation != nil && status.Rotation.Status != "completed" {
			return ErrBusy
		}
	}
	if err := m.store.check(true); err != nil {
		return err
	}
	members, err := m.toolRosterLocked(ctx, req.RoomIdentity)
	if err != nil || !memberExists(members, req.Caller.ID, req.Caller.MembershipID) {
		return ErrIdentity
	}
	return nil
}
func (m *roomManager) selectionsLocked(f toolBindingsFile, caller Caller) ([]toolSelection, error) {
	selections := []toolSelection{}
	var platforms *registry.Snapshot
	for _, b := range f.Bindings {
		if b.AgentID != caller.ID || b.MembershipID != caller.MembershipID {
			continue
		}
		d, err := m.toolDefinition(b.Tool)
		if err != nil {
			continue
		}
		s := toolSelection{Definition: d, ToolType: d.Name, PlatformID: b.PlatformID, ActionAuthorization: b.ActionAuthorization, Capabilities: []string{}}
		s.AgentID, s.MembershipID = b.AgentID, b.MembershipID
		// Internal action availability describes registered action adapters,
		// not the native account permissions of independent CLI tools.
		if caps, err := m.referenceCapabilities(d.Provider); err == nil {
			if len(caps.Actions) == 0 {
				s.ActionDisabledReason = "No sending adapter is implemented; newly introduced actions are disabled."
			} else {
				disabled := []string{}
				for _, cap := range caps.Actions {
					if cap.Spec.DisabledReason != "" {
						disabled = append(disabled, cap.Spec.DisabledReason)
					}
				}
				if len(disabled) == len(caps.Actions) {
					slices.Sort(disabled)
					s.ActionDisabledReason = strings.Join(slices.Compact(disabled), "; ")
				}
			}
		}
		if b.PlatformID != "" {
			if platforms == nil {
				file, err := m.platforms.Load()
				if err != nil {
					return nil, err
				}
				platforms = &file
			}
			s.Status = "unavailable"
			for _, c := range platforms.Platforms {
				if c.ID == b.PlatformID && c.Generation == b.PlatformGeneration && c.Provider == d.Provider {
					public := registry.Public(*platforms, c)
					s.Instance, s.Username, s.Status = public.URL, public.Validation.Username, public.Validation.Status
					s.Name, s.PlatformName = c.ToolEntryID, c.Name
					m.toolAvailability(*platforms, c, b, d, &s)
				}
			}
			if s.Name == d.Name {
				s.UnavailableReason = "platform_changed"
			}
		} else if d.Provider == "" {
			if tool.ExecutableAvailable(d.Executable) {
				s.Capabilities = append(s.Capabilities, "run")
			} else {
				s.UnavailableReason = "executable_unavailable"
			}
		}
		selections = append(selections, s)
	}
	return selections, nil
}
func (m *roomManager) changeToolBindingLocked(ctx context.Context, req toolBindingChange) (toolBindingsFile, []<-chan struct{}, error) {
	var empty toolBindingsFile
	if err := m.checkPlatformMemberContextLocked(ctx); err != nil {
		return empty, nil, err
	}
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		return empty, nil, ErrIdentity
	}
	if m.closing || m.roomBusyLocked(req.RoomIdentity) {
		return empty, nil, ErrBusy
	}
	if err := m.store.check(true); err != nil {
		return empty, nil, err
	}
	entry, err := m.workspaceRoom(req.RoomIdentity)
	if err != nil || entry.Status != "ready" {
		return empty, nil, ErrIdentity
	}
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return f, nil, err
	}
	if req.Revision != f.Revision {
		return f, nil, ErrConflict
	}
	b := req.Binding
	if !b.ActionAuthorization.Valid() {
		return f, nil, ErrRequest
	}
	if b.ActionAuthorization != nil {
		d, err := m.toolDefinition(b.Tool)
		if err != nil {
			return f, nil, err
		}
		caps, err := m.referenceCapabilities(d.Provider)
		if err != nil {
			return f, nil, err
		}
		if err := validateBindingEvidence(b, caps, req.Caller.ID); err != nil {
			return f, nil, err
		}
		for _, g := range b.ActionAuthorization.Grants {
			cap, ok := caps.Actions[platform.Operation(g.Operation)]
			if ok && cap.Spec.DisabledReason != "" {
				return f, nil, invalidField("action_authorization", cap.Spec.DisabledReason)
			}
			if !ok || cap.Spec.TargetKind != g.TargetKind || (len(cap.Spec.ObjectTypes) > 0 && !slices.Contains(cap.Spec.ObjectTypes, g.ObjectType)) {
				return f, nil, invalidField("action_authorization", "action adapter is not implemented for this target; sending remains disabled")
			}
		}
		b.ActionAuthorization = &tool.ActionAuthorization{Version: 1, Revision: f.Revision + 1, Grants: append([]tool.ActionGrant(nil), b.ActionAuthorization.Grants...)}
	} else if len(b.CapabilityEvidence) != 0 {
		return f, nil, ErrRequest
	}
	if !validText(b.AgentID) || !safeMembershipID(b.MembershipID) || !tool.ValidName(b.Tool) || b.PlatformGeneration != "" {
		return f, nil, ErrRequest
	}
	if req.Enabled {
		members, err := m.toolRosterLocked(ctx, req.RoomIdentity)
		if err != nil || !memberExists(members, b.AgentID, b.MembershipID) {
			return f, nil, ErrIdentity
		}
		d, err := m.toolDefinition(b.Tool)
		if err != nil {
			return f, nil, err
		}
		if d.Provider != "" {
			file, err := m.platforms.Load()
			if err != nil {
				return f, nil, err
			}
			found := false
			for _, c := range file.Platforms {
				if c.ID == b.PlatformID && c.Provider == d.Provider {
					if c.ToolEntryID == "" {
						return f, nil, invalidField("tool", "platform type has no registered tool entry")
					}
					if c.Version != req.PlatformVersion {
						return f, nil, ErrConflict
					}
					if _, _, err := registry.ResolveAccessAccount(file, c); err != nil {
						return f, nil, ErrConflict
					}
					for _, e := range b.CapabilityEvidence {
						if e.Account != c.Validation.Account || e.CredentialVersion != c.CredentialVersion {
							return f, nil, ErrIdentity
						}
					}
					b.PlatformGeneration = c.Generation
					found = true
				}
			}
			if !found {
				return f, nil, ErrRequest
			}
		} else if b.PlatformID != "" || req.PlatformVersion != 0 {
			return f, nil, ErrRequest
		}
	} else if req.PlatformVersion != 0 || b.PlatformID != "" && !tool.OpaqueID(b.PlatformID) {
		return f, nil, ErrRequest
	}
	// A revocation must identify the exact platform. Never disable a sibling
	// account based on a type name or whichever binding happens to be first.
	if !req.Enabled {
		d, err := m.toolDefinition(b.Tool)
		if err != nil || d.Provider != "" && b.PlatformID == "" || d.Provider == "" && b.PlatformID != "" {
			return f, nil, ErrRequest
		}
	}
	if f.Disabled == nil {
		f.Disabled = []tool.Binding{}
	}
	f.Disabled = slices.DeleteFunc(f.Disabled, func(old tool.Binding) bool {
		return old.AgentID == b.AgentID && old.MembershipID == b.MembershipID && old.Tool == b.Tool && old.PlatformID == b.PlatformID
	})
	if !req.Enabled {
		f.Disabled = append(f.Disabled, b)
	}
	f.Bindings = slices.DeleteFunc(f.Bindings, func(old tool.Binding) bool {
		return old.AgentID == b.AgentID && old.MembershipID == b.MembershipID && old.Tool == b.Tool && old.PlatformID == b.PlatformID
	})
	if req.Enabled {
		f.Bindings = append(f.Bindings, b)
	}
	if len(f.Bindings)+len(f.Disabled) > 3200 || f.Revision == ^uint64(0) {
		return f, nil, ErrCapacity
	}
	f.Revision++
	if err = m.store.save(m.toolBindingsPath(f.RoomIdentity), f, 1<<20); err != nil {
		return f, nil, err
	}
	pending := m.cancelPlatformCallsLocked(func(c *platformCall) bool {
		return c.owner == req.RoomIdentity && c.tool == b.Tool && c.platformID == b.PlatformID && c.agentID == b.AgentID && c.membershipID == b.MembershipID
	})
	return f, pending, nil
}

// Agent tools add process-only preparation after the shared platform admission.
func (m *roomManager) prepareToolLocked(ctx context.Context, req toolsRequest) (tool.Process, *platformCall, context.Context, error) {
	if req.Tool == "workspace-git" {
		return m.prepareWorkspaceGitLocked(ctx, req)
	}
	var p tool.Process
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return p, nil, nil, err
	}
	_, definition, err := m.resolveToolBindingLocked(f, req)
	if err != nil {
		return p, nil, nil, err
	}
	if definition.InternalOnly {
		return p, nil, nil, ErrRequest
	}
	if len(req.Args) > 4096 {
		return p, nil, nil, ErrRequest
	}
	help := tool.Help(req.Args)
	if definition.Provider == "scion" {
		help = scion.Help(req.Args)
	}
	admission, call, callCtx, err := m.preparePlatformCallLocked(ctx, req, help)
	if err != nil {
		return p, nil, nil, err
	}
	p.Definition = admission.definition
	p.Values = map[string]string{}
	if admission.account.PlatformID != "" {
		p.Values = map[string]string{"instance": admission.account.URL, "token": admission.secret.Reveal()}
	}
	prepared := false
	defer func() {
		if !prepared {
			m.finishPlatformCallLocked(call)
		}
	}()

	if definition.Provider == "scion" && help {
		// Help uses only executable metadata, even after logout or a failed check.
		if file, err := m.platforms.Load(); err == nil {
			for _, c := range file.Platforms {
				if c.ID == call.platformID && c.Provider == "scion" && c.CLIProfile != nil && scion.ValidProfile(*c.CLIProfile) {
					p.Definition.Executable = c.CLIProfile.Executable
				}
			}
		}
	}
	if definition.Provider == "scion" && !help {
		if admission.account.CLIProfile == nil || !scion.ValidProfile(*admission.account.CLIProfile) {
			return p, nil, nil, platform.ErrCredential
		}
		if err := scion.ValidateArguments(admission.account.URL, req.Args); err != nil {
			return p, nil, nil, invalidField("args", "Scion destination must match the selected connection; login and CLI configuration are operator actions")
		}
		profile := *admission.account.CLIProfile
		p.Definition.Executable = profile.Executable
		p.Prepare = func(ctx context.Context, private string) (map[string]string, error) {
			return scion.Prepare(ctx, m.store.dir, admission.account.URL, profile, admission.account.Identity, private)
		}
	}
	entry, err := m.workspaceRoom(req.RoomIdentity)
	if err != nil {
		return p, nil, nil, err
	}
	saved, err := m.readWorkspaceRoomConfig(entry)
	if err != nil {
		return p, nil, nil, err
	}
	for _, member := range saved.Members {
		if member.AgentID == req.Caller.ID && member.MembershipID == req.Caller.MembershipID && member.Workspace != "" {
			dir := filepath.Join(m.roomDir(req.RoomID), filepath.FromSlash(member.Workspace))
			if err := checkWorkspaceDirectory(m.roomDir(req.RoomID), member.Workspace); err != nil {
				return p, nil, nil, err
			}
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				p.Directory = dir
			}
		}
	}
	prepared = true
	return p, call, callCtx, nil
}

// Called at every execution guard for adapters requiring an explicit new grant.
// Legacy capabilities retain their existing admission contract.
func (m *roomManager) checkActionAuthorizationLocked(req toolsRequest, access platform.AccessContext, spec platform.ActionSpec, target platform.TargetIdentity) error {
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return err
	}
	for _, b := range f.Bindings {
		if b.AgentID == req.Caller.ID && b.MembershipID == req.Caller.MembershipID && b.PlatformID == access.PlatformID && b.PlatformGeneration == access.Generation && b.ActionAuthorization != nil {
			if access.ActionAuthorizationVersion != b.ActionAuthorization.Revision {
				return ErrConflict
			}
			for _, g := range b.ActionAuthorization.Grants {
				if g.Operation == string(spec.Operation) && g.TargetKind == spec.TargetKind && g.ObjectType == target.Object.Type {
					if spec.BindingEvidence != "" && !currentCapabilityEvidence(b.Evidence(spec.BindingEvidence), spec, access, target) {
						return ErrIdentity
					}
					return nil
				}
			}
		}
	}
	return conflictReason("action_authorization", "member action capability is disabled", "The operator must explicitly enable this action on the selected binding.")
}
