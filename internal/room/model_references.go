package room

import (
	"context"
	"path/filepath"
	"reflect"
	"time"
)

// manager.mu is retained only during local preparation/publication, never during
// model requests or waiting for an admitted turn. Creation is serialized here.
func (c *modelController) prepareLocked(ctx context.Context, op *modelChangeOperation, before, next modelCatalog) error {
	m := c.manager
	if m.closing {
		return ErrClosing
	}
	members, err := c.referencesLocked()
	if err != nil {
		return err
	}
	op.Before = &before
	op.Target = &next
	op.Members = nil
	for _, ref := range members {
		if op.Input.Kind == "selection" && ref.MemberIdentity != op.Input.MemberIdentity {
			continue
		}
		selection, version := ref.Selection, ref.Version
		deletingReference := op.Input.Kind == "delete" && selection.Mode == "explicit" && selection.EntryID == op.Input.EntryID
		if deletingReference {
			selection.EntryID = op.Input.ReplacementID
			version++
			if version == 0 {
				return ErrCapacity
			}
		}
		if op.Input.Kind == "selection" {
			if version != op.Input.ExpectedSelectionVersion {
				return ErrConflict
			}
			selection = op.Input.Selection
			if selection != ref.Selection {
				version++
			}
			if version == 0 {
				return ErrCapacity
			}
		}
		old, err := before.resolve(ref.Selection)
		if err != nil {
			// Pending deletions own these members; disjoint maintenance may proceed.
			if m.config.models.gate(ref.MemberIdentity) != "" {
				continue
			}
			return err
		}
		entry, err := next.resolve(selection)
		if err != nil {
			return err
		}
		changed := !old.sameExecution(entry)
		repairing := op.Input.Kind == "save" && op.Input.Entry.ID == entry.ID || op.Input.Kind == "default" && op.Input.EntryID == entry.ID || op.Input.Kind == "delete" && op.Input.ReplacementID == entry.ID
		if repairing && !entry.Unverified && !m.config.models.hasValidation(old.ID) {
			path := filepath.Join(m.roomDir(ref.RoomID), "agents", ref.MembershipID, "session.json")
			if generation, _, readErr := readDSHReference(path, ref.MemberIdentity); readErr != nil {
				return readErr
			} else if generation.Directory == "" || generation.Pending {
				changed = true
			}
		}
		if op.Input.Kind != "selection" && !changed && !deletingReference {
			continue
		}
		if gate := m.config.models.gate(ref.MemberIdentity); gate != "" && gate != op.ID {
			return ErrBusy
		}
		if m.roomBusyLocked(ref.RoomIdentity) {
			return ErrBusy
		}
		for _, ws := range m.index.WorkspaceOperations {
			if ws.RoomIdentity == ref.RoomIdentity && !ws.finished() {
				return ErrBusy
			}
		}
		profile, err := c.memberProfileLocked(ref.MemberIdentity)
		if err != nil {
			return err
		}
		// Freeze validation facts here; each installation is checked independently
		// before application, so one unsupported DSH cannot block other members.
		var receipt modelValidation
		receiptErr := readSnapshot(filepath.Join(filepath.Dir(c.service.credentials.dir), "model-validations", entry.executionDigest()+".json"), &receipt, maxPrivateFileBytes)
		receipt.EntryID = entry.ID
		receipt.Revision = entry.Revision
		target, err := newModelSessionSnapshot(next, memberModelReference{ref.MemberIdentity, selection, version}, "session.json.dsh", profile, receipt)
		if (receiptErr != nil || err != nil) && !changed {
			// A binding-only change can keep a migrated, normally restorable
			// execution without fabricating a probe receipt or resetting context.
			path := filepath.Join(m.roomDir(ref.RoomID), "agents", ref.MembershipID, "session.json")
			if generation, exists, readErr := readDSHReference(path, ref.MemberIdentity); readErr == nil && exists {
				if frozen, loadErr := readModelSessionSnapshot(filepath.Join(filepath.Dir(path), generation.Directory+".model.json"), ref.MemberIdentity, generation.Directory); loadErr == nil && frozen.ExecutionDigest == entry.executionDigest() {
					frozen.Selection, frozen.SelectionVersion, frozen.CatalogVersion = selection, version, next.Version
					frozen.EntryID, frozen.EntryRevision = entry.ID, entry.Revision
					target, err = frozen, nil
				}
			}
		}
		if err != nil {
			return err
		}

		rotationID, err := newOpaqueID()
		if err != nil {
			return err
		}
		op.Members = append(op.Members, modelChangeMember{MemberIdentity: ref.MemberIdentity, Before: ref.Selection, Selection: selection, BeforeVersion: ref.Version, SelectionVersion: version, Target: target, Rotate: changed, RotationID: rotationID})
	}
	if op.Input.Kind == "selection" && len(op.Members) != 1 {
		return ErrIdentity
	}
	op.Status = "prepared"
	if err := c.persist(op); err != nil {
		return err
	}
	for _, member := range op.Members {
		if !member.Rotate && op.Input.Kind != "delete" {
			continue
		}
		m.config.models.setGate(member.MemberIdentity, op.ID)
		if svc, _ := m.runtimeServiceLocked(member.RoomIdentity); svc != nil {
			if !svc.management.TryLock() {
				return ErrBusy
			}
			status, inspectErr := svc.target(member.MembershipID).Call[MemberStatus](ctx, InspectMember{member.MemberIdentity})
			if inspectErr != nil || status.Rotation != nil && status.Rotation.Status != "completed" {
				svc.management.Unlock()
				if inspectErr != nil {
					return inspectErr
				}
				return ErrBusy
			}
			gate := op.ID
			_, err := svc.target(member.MembershipID).Call[MemberStatus](ctx, ControlMember{MemberIdentity: member.MemberIdentity, modelGate: &gate})
			svc.management.Unlock()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
func (c *modelController) referencesLocked() ([]memberModelReference, error) {
	var refs []memberModelReference
	for _, room := range c.manager.index.Rooms {
		if room.Status == "deleting" {
			continue
		}
		// A persisted creation reservation must not miss the publication window.
		if room.Status != "ready" {
			return nil, ErrBusy
		}
		var saved managedRoomConfig
		if err := readSnapshot(filepath.Join(c.manager.roomDir(room.RoomID), "room-config.json"), &saved, maxPrivateFileBytes); err != nil {
			return nil, err
		}
		if saved.RoomIdentity != room.RoomIdentity || saved.Runtime == nil || !saved.Runtime.ManagedModels {
			return nil, ErrIdentity
		}
		var state RoomSnapshot
		if err := readSnapshot(filepath.Join(c.manager.roomDir(room.RoomID), "room.json"), &state, maxRoomFileBytes); err != nil {
			return nil, err
		}
		if state.RoomIdentity != room.RoomIdentity {
			return nil, ErrIdentity
		}
		for _, member := range saved.Members {
			current := false
			for _, live := range state.Members {
				if live.AgentID == member.AgentID && live.MembershipID == member.MembershipID {
					current = true
					break
				}
			}
			if !current {
				continue
			}
			if member.ModelSelection == nil {
				return nil, ErrInvalid
			}
			refs = append(refs, memberModelReference{MemberIdentity{room.RoomIdentity, member.AgentID, member.MembershipID}, *member.ModelSelection, member.ModelSelectionVersion})
		}
	}
	return refs, nil
}
func (c *modelController) memberProfileLocked(id MemberIdentity) (dshProfile, error) {
	var saved managedRoomConfig
	if err := readSnapshot(filepath.Join(c.manager.roomDir(id.RoomID), "room-config.json"), &saved, maxPrivateFileBytes); err != nil {
		return dshProfile{}, err
	}
	if saved.RoomIdentity != id.RoomIdentity || saved.Runtime == nil {
		return dshProfile{}, ErrIdentity
	}
	for _, member := range saved.Members {
		if member.AgentID == id.AgentID && member.MembershipID == id.MembershipID {
			p := saved.Runtime.profile()
			p.Instructions = member.Instructions
			p.Workspace = filepath.Join(c.manager.roomDir(id.RoomID), filepath.FromSlash(member.Workspace))
			p.ToolEnv = cloneStringMap(p.ToolEnv)
			if p.ToolEnv == nil {
				p.ToolEnv = map[string]string{}
			}
			for k, v := range member.ToolEnv {
				p.ToolEnv[k] = resolveTeamResource(c.manager.roomDir(id.RoomID), v)
			}
			return p, nil
		}
	}
	return dshProfile{}, ErrIdentity
}
func (c *modelController) changeSelection(ctx context.Context, op *modelChangeOperation) error {
	input := op.Input
	current := c.service.snapshot()
	c.manager.mu.Lock()
	refs, err := c.referencesLocked()
	c.manager.mu.Unlock()
	if err != nil {
		return err
	}
	var ref *memberModelReference
	for i := range refs {
		if refs[i].MemberIdentity == input.MemberIdentity {
			ref = &refs[i]
			break
		}
	}
	if ref == nil {
		return ErrIdentity
	}
	if ref.Version != input.ExpectedSelectionVersion {
		return ErrConflict
	}
	before, err := current.resolve(ref.Selection)
	if err != nil {
		return err
	}
	entry, err := current.resolve(input.Selection)
	if err != nil {
		return err
	}
	if !before.sameExecution(entry) {
		select {
		case c.service.probeSlot <- struct{}{}:
			defer func() { <-c.service.probeSlot }()
		default:
			return ErrBusy
		}
		opts := c.service.launcher
		opts.Model = entry.materialize()
		opts.Credential, err = c.service.credentials.get(entry.CredentialRef)
		if err != nil {
			return err
		}
		receipt, err := c.service.probe(ctx, opts)
		if err != nil {
			return err
		}
		if !receipt.Text || !receipt.ToolRoundTrip || !receipt.Cleanup {
			return ErrInvalid
		}
		if err := saveModelValidation(c.service.credentials.dir, modelValidation{EntryID: entry.ID, Revision: entry.Revision, ExecutionDigest: entry.executionDigest(), CredentialRef: entry.CredentialRef, Result: receipt}); err != nil {
			return err
		}
	}
	c.manager.mu.Lock()
	defer c.manager.mu.Unlock()
	if op.Status == "cancelled" {
		return context.Canceled
	}
	if c.manager.config.models.catalog().Version != input.ExpectedVersion {
		return ErrConflict
	}
	if err := c.prepareLocked(ctx, op, current, current); err != nil {
		return err
	}
	if err := c.saveSelectionLocked(op.Members[0]); err != nil {
		return err
	}
	// A selection may be the first real validation of an imported entry.
	// Publish that receipt for future members without changing the catalog.
	publish, err := c.manager.config.models.publication(current)
	if err != nil {
		return err
	}
	publish()
	op.Committed = true
	op.Status = "applying"
	return c.persist(op)
}
func (c *modelController) saveSelectionLocked(member modelChangeMember) error {
	path := filepath.Join(c.manager.roomDir(member.RoomID), "room-config.json")
	var saved managedRoomConfig
	if err := readSnapshot(path, &saved, maxPrivateFileBytes); err != nil {
		return err
	}
	if saved.RoomIdentity != member.RoomIdentity {
		return ErrIdentity
	}
	for i, m := range saved.Members {
		if m.AgentID == member.AgentID && m.MembershipID == member.MembershipID {
			if m.ModelSelection == nil {
				return ErrInvalid
			}
			if m.ModelSelectionVersion == member.SelectionVersion && *m.ModelSelection == member.Selection {
				return nil
			}
			if m.ModelSelectionVersion != member.BeforeVersion || *m.ModelSelection != member.Before {
				return ErrConflict
			}
			s := member.Selection
			saved.Members[i].ModelSelection = &s
			saved.Members[i].ModelSelectionVersion = member.SelectionVersion
			return c.manager.store.save(path, saved, maxPrivateFileBytes)
		}
	}
	return ErrIdentity
}
func (c *modelController) releaseGatesLocked(op *modelChangeOperation) {
	for _, member := range op.Members {
		if c.manager.config.models.gate(member.MemberIdentity) != op.ID {
			continue
		}
		c.manager.config.models.setGate(member.MemberIdentity, "")
		if svc, _ := c.manager.runtimeServiceLocked(member.RoomIdentity); svc != nil {
			gate := ""
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, _ = svc.target(member.MembershipID).Call[MemberStatus](ctx, ControlMember{MemberIdentity: member.MemberIdentity, modelGate: &gate})
			cancel()
		}
	}
}

// Model selection is mutable; all other frozen identity, workspace and tool
// fields remain subject to the existing full snapshot authorization checks.
func equalWorkspaceRoomConfig(a, b managedRoomConfig) bool {
	if equalMigratedRuntime(a, b) {
		b.Runtime = a.Runtime
		b.ModelMigrationRuntimeDigest = a.ModelMigrationRuntimeDigest
	}
	if equalMigratedRuntime(b, a) {
		a.Runtime = b.Runtime
		a.ModelMigrationRuntimeDigest = b.ModelMigrationRuntimeDigest
	}
	scrub := func(v managedRoomConfig) managedRoomConfig {
		v.Members = append([]managedMember(nil), v.Members...)
		for i := range v.Members {
			v.Members[i].ModelSelection = nil
			v.Members[i].ModelSelectionVersion = 0
		}
		return v
	}
	return reflect.DeepEqual(scrub(a), scrub(b))
}

func (m *roomManager) pendingModelChangeLocked(id RoomIdentity) bool {
	if m.config.models == nil || m.modelControl == nil {
		return false
	}
	for _, op := range m.modelControl.operations {
		for _, member := range op.Members {
			if member.RoomIdentity == id && m.config.models.gate(member.MemberIdentity) != "" {
				return true
			}
		}
	}
	return false
}
