package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/go-aie/teamx/internal/platform/connection"
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
		if !validText(b.AgentID) || !safeMembershipID(b.MembershipID) || !tool.ValidName(b.Tool) || seen[key] ||
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
		s := toolSelection{Definition: d, ConnectionID: b.ConnectionID}
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
	admission, call, callCtx, err := m.preparePlatformCallLocked(ctx, req, tool.Help(req.Args))
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
