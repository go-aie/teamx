package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/scion"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Bindings require explicit selection; retired read-only or received-todos
// grants never become complete account permissions automatically.
type toolBindingsFile struct {
	RoomIdentity
	Version  int            `json:"version"`
	Revision uint64         `json:"revision"`
	Bindings []tool.Binding `json:"bindings"`
}
type toolSelection struct {
	ActionAuthorization  *tool.ActionAuthorization `json:"action_authorization,omitempty"`
	ActionDisabledReason string                    `json:"action_disabled_reason,omitempty"`
	tool.Definition
	ConnectionID string `json:"connection_id,omitempty"`
	Instance     string `json:"instance,omitempty"`
	Username     string `json:"username,omitempty"`
	Status       string `json:"status,omitempty"`
}
type toolsRequest struct {
	ServiceInfo
	Caller Caller   `json:"caller"`
	Tool   string   `json:"tool,omitempty"`
	Args   []string `json:"args,omitempty"`
}
type toolBindingChange struct {
	ServiceEpoch string `json:"service_epoch"`
	RoomIdentity
	Caller            Caller       `json:"caller"`
	Revision          uint64       `json:"revision"`
	Binding           tool.Binding `json:"binding"`
	Enabled           bool         `json:"enabled"`
	ConnectionVersion uint64       `json:"connection_version,omitempty"`
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
	if f.RoomIdentity != owner || f.Version != 1 || f.Revision == 0 || f.Bindings == nil || len(f.Bindings) > 3200 {
		return f, ErrInvalid
	}
	seen := map[[3]string]bool{}
	for _, b := range f.Bindings {
		key := [3]string{b.AgentID, b.MembershipID, b.Tool}
		if !validText(b.AgentID) || !safeMembershipID(b.MembershipID) || !tool.ValidName(b.Tool) || seen[key] || !b.ActionAuthorization.Valid() ||
			(b.ConnectionID == "") != (b.ConnectionGeneration == "") || b.ConnectionID != "" && (!tool.OpaqueID(b.ConnectionID) || !tool.OpaqueID(b.ConnectionGeneration)) {
			return f, ErrInvalid
		}
		seen[key] = true
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
	var connections *connection.Snapshot
	for _, b := range f.Bindings {
		if b.AgentID != caller.ID || b.MembershipID != caller.MembershipID {
			continue
		}
		d, err := m.toolDefinition(b.Tool)
		if err != nil {
			continue
		}
		s := toolSelection{Definition: d, ConnectionID: b.ConnectionID, ActionAuthorization: b.ActionAuthorization}
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
		if b.ConnectionID != "" {
			if connections == nil {
				file, err := m.connections.Load()
				if err != nil {
					return nil, err
				}
				connections = &file
			}
			s.Status = "unavailable"
			for _, c := range connections.Connections {
				if c.ID == b.ConnectionID && c.Generation == b.ConnectionGeneration && c.Provider == d.Provider {
					public := connection.Public(*connections, c)
					s.Instance, s.Username, s.Status = public.URL, public.Validation.Username, public.Validation.Status
				}
			}
		}
		selections = append(selections, s)
	}
	return selections, nil
}
func (m *roomManager) changeToolBindingLocked(ctx context.Context, req toolBindingChange) (toolBindingsFile, []<-chan struct{}, error) {
	var empty toolBindingsFile
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
	if !validText(b.AgentID) || !safeMembershipID(b.MembershipID) || !tool.ValidName(b.Tool) || b.ConnectionGeneration != "" {
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
			file, err := m.connections.Load()
			if err != nil {
				return f, nil, err
			}
			found := false
			for _, c := range file.Connections {
				if c.ID == b.ConnectionID && c.Provider == d.Provider {
					if c.Version != req.ConnectionVersion {
						return f, nil, ErrConflict
					}
					if _, _, err := connection.ResolveAccessAccount(file, c); err != nil {
						return f, nil, ErrConflict
					}
					for _, e := range b.CapabilityEvidence {
						if e.Account != c.Validation.Account || e.CredentialVersion != c.CredentialVersion {
							return f, nil, ErrIdentity
						}
					}
					b.ConnectionGeneration = c.Generation
					found = true
				}
			}
			if !found {
				return f, nil, ErrRequest
			}
		} else if b.ConnectionID != "" || req.ConnectionVersion != 0 {
			return f, nil, ErrRequest
		}
	} else if b.ConnectionID != "" || req.ConnectionVersion != 0 {
		return f, nil, ErrRequest
	}
	f.Bindings = slices.DeleteFunc(f.Bindings, func(old tool.Binding) bool {
		return old.AgentID == b.AgentID && old.MembershipID == b.MembershipID && old.Tool == b.Tool
	})
	if req.Enabled {
		f.Bindings = append(f.Bindings, b)
	}
	if len(f.Bindings) > 3200 || f.Revision == ^uint64(0) {
		return f, nil, ErrCapacity
	}
	f.Revision++
	if err = m.store.save(m.toolBindingsPath(f.RoomIdentity), f, 1<<20); err != nil {
		return f, nil, err
	}
	pending := m.cancelPlatformCallsLocked(func(c *platformCall) bool {
		return c.owner == req.RoomIdentity && c.tool == b.Tool && c.agentID == b.AgentID && c.membershipID == b.MembershipID
	})
	return f, pending, nil
}

// Agent tools add process-only preparation after the shared platform admission.
func (m *roomManager) prepareToolLocked(ctx context.Context, req toolsRequest) (tool.Process, *platformCall, context.Context, error) {
	if req.Tool == "workspace-git" {
		return m.prepareWorkspaceGitLocked(ctx, req)
	}
	var p tool.Process
	definition, err := m.toolDefinition(req.Tool)
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
	if admission.account.ConnectionID != "" {
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
		if file, err := m.connections.Load(); err == nil {
			for _, c := range file.Connections {
				if c.ID == call.connectionID && c.Provider == "scion" && c.CLIProfile != nil && scion.ValidProfile(*c.CLIProfile) {
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
		if b.AgentID == req.Caller.ID && b.MembershipID == req.Caller.MembershipID && b.ConnectionID == access.ConnectionID && b.ConnectionGeneration == access.Generation && b.ActionAuthorization != nil {
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
