package room

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-aie/teamx/internal/app/platforms"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/computer"
)

type memberComputerInput struct {
	MemberIdentity
	Caller          Caller `json:"caller"`
	ServiceEpoch    string `json:"service_epoch"`
	ExpectedVersion uint64 `json:"expected_version"`
	OperationID     string `json:"operation_id"`
	Enabled         bool   `json:"enabled"`
}
type memberComputerChange struct {
	MemberIdentity
	ID            string `json:"id"`
	Digest        string `json:"digest"`
	Directory     string `json:"directory"`
	BeforeVersion uint64 `json:"before_version"`
	Enabled       bool   `json:"enabled"`
	Rotate        bool   `json:"rotate"`
	Global        bool   `json:"global,omitempty"`
	RotationID    string `json:"rotation_id"`
	Stage         string `json:"stage"`
	Status        string `json:"status"`
	Message       string `json:"message,omitempty"`
}

func (x *computerMembers) operationPath(id string) string {
	return filepath.Join(filepath.Dir(x.path), "member-changes", id+".json")
}
func (x *computerMembers) loadChanges() error {
	dir := filepath.Join(filepath.Dir(x.path), "member-changes")
	if err := makeDurableDir(dir); err != nil {
		return err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(files) > 4096 {
		return ErrCapacity
	}
	x.changes = map[string]memberComputerChange{}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		var op memberComputerChange
		if err := readSnapshot(filepath.Join(dir, file.Name()), &op, maxPrivateFileBytes); err != nil {
			return err
		}
		if !validRestartID(op.ID) || file.Name() != op.ID+".json" || !validRestartID(op.RotationID) || validateMemberHeader(formatVersion, op.MemberIdentity) != nil || !filepath.IsAbs(op.Directory) {
			return ErrInvalid
		}
		x.changes[op.ID] = op
	}
	return nil
}
func (x *computerMembers) saveChange(op memberComputerChange) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	// Retain the pending gate on an uncertain receipt write.
	x.changes[op.ID] = op
	if err := x.connection.writer.Save(x.operationPath(op.ID), op, maxPrivateFileBytes); err != nil {
		op.Status = "waiting"
		x.changes[op.ID] = op
		return err
	}
	return nil
}
func (x *computerMembers) memberPending(id MemberIdentity) bool {
	if x == nil {
		return false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, op := range x.changes {
		if op.MemberIdentity == id && op.Status != "completed" {
			return true
		}
	}
	return false
}
func (x *computerMembers) memberDirectory(id MemberIdentity) string {
	if x.manager != nil {
		return x.manager.roomDir(id.RoomID)
	}
	if x.service != nil && x.service.info.RoomIdentity == id.RoomIdentity {
		return x.service.store.dir
	}
	return ""
}
func (x *computerMembers) runtime(id MemberIdentity) *service {
	if x.manager != nil {
		x.manager.mu.Lock()
		defer x.manager.mu.Unlock()
		svc, _ := x.manager.runtimeServiceLocked(id.RoomIdentity)
		return svc
	}
	return x.service
}
func (x *computerMembers) request(input memberComputerInput, owner string) (memberComputerChange, error) {
	x.connection.changeMu.Lock()
	defer x.connection.changeMu.Unlock()
	if input.Caller.Kind != "human" || input.Caller.ID != owner || owner != x.connection.owner.OwnerID || input.Caller.MembershipID != "" || input.ServiceEpoch != x.connection.service.ServiceEpoch {
		return memberComputerChange{}, ErrIdentity
	}
	if validateMemberHeader(formatVersion, input.MemberIdentity) != nil {
		return memberComputerChange{}, ErrIdentity
	}
	if !validRestartID(input.OperationID) {
		return memberComputerChange{}, ErrRequest
	}
	semantic := input
	semantic.ServiceEpoch = ""
	digest := computer.Hash(semantic)
	if x.manager != nil {
		x.manager.mu.Lock()
		defer x.manager.mu.Unlock()
		found := false
		for _, room := range x.manager.index.Rooms {
			if room.RoomIdentity == input.RoomIdentity && room.Status == "ready" {
				found = true
			}
		}
		if !found {
			return memberComputerChange{}, ErrIdentity
		}
		if x.manager.closing {
			return memberComputerChange{}, ErrClosing
		}
		if x.manager.roomBusyLocked(input.RoomIdentity) || x.pendingWorkspaceLocked(input.RoomIdentity) {
			return memberComputerChange{}, ErrBusy
		}
		if x.manager.config.models != nil && x.manager.config.models.gate(input.MemberIdentity) != "" {
			return memberComputerChange{}, ErrBusy
		}
	}
	x.mu.Lock()
	old, exists := x.changes[input.OperationID]
	x.mu.Unlock()
	if exists {
		if old.Digest != digest {
			return old, ErrConflict
		}
		x.connection.signal()
		return old, nil
	}
	x.mu.Lock()
	capacity := len(x.changes) >= 4096
	x.mu.Unlock()
	if capacity {
		return memberComputerChange{}, ErrCapacity
	}
	if x.memberPending(input.MemberIdentity) {
		return memberComputerChange{}, ErrBusy
	}
	c := x.connection
	c.mu.Lock()
	pending := c.pendingLocked() != nil
	c.mu.Unlock()
	if pending {
		return memberComputerChange{}, ErrBusy
	}
	dir := x.memberDirectory(input.MemberIdentity)
	m, err := x.preference(input.MemberIdentity, dir)
	if err != nil {
		return memberComputerChange{}, err
	}
	if m.ComputerSettingsVersion != input.ExpectedVersion {
		return memberComputerChange{}, ErrConflict
	}
	op := memberComputerChange{MemberIdentity: input.MemberIdentity, ID: input.OperationID, Digest: digest, Directory: dir, BeforeVersion: m.ComputerSettingsVersion, Enabled: input.Enabled, Rotate: c.current.Load().Enabled && m.ComputerEnabled != input.Enabled, RotationID: newComputerID(), Stage: "intent", Status: "applying"}
	if m.ComputerEnabled == input.Enabled {
		op.Stage = "done"
		op.Status = "completed"
	}
	if err = x.saveChange(op); err != nil {
		return op, err
	}
	c.signal()
	return op, nil
}
func (x *computerMembers) savePreference(op memberComputerChange) error {
	if op.Global {
		return nil
	}
	if x.manager != nil {
		x.manager.mu.Lock()
		defer x.manager.mu.Unlock()
		if x.manager.roomBusyLocked(op.RoomIdentity) {
			return ErrBusy
		}
	}
	path := filepath.Join(op.Directory, "room-config.json")
	var saved managedRoomConfig
	if err := readSnapshot(path, &saved, maxPrivateFileBytes); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return x.saveMemberPreference(op)
		}
		return err
	}
	if saved.RoomIdentity != op.RoomIdentity {
		return ErrIdentity
	}
	for i, m := range saved.Members {
		if m.AgentID == op.AgentID && m.MembershipID == op.MembershipID {
			if m.ComputerSettingsVersion == op.BeforeVersion+1 && m.ComputerEnabled == op.Enabled {
				return nil
			}
			if m.ComputerSettingsVersion != op.BeforeVersion {
				return ErrConflict
			}
			saved.Members[i].ComputerEnabled = op.Enabled
			saved.Members[i].ComputerSettingsVersion++
			return x.connection.writer.Save(path, saved, maxPrivateFileBytes)
		}
	}
	return x.saveMemberPreference(op)
}
func (x *computerMembers) advance() {
	// Recovery is per member; failed providers leave only those members fenced.
	x.recover(x.connection.ctx)
	x.connection.mu.Lock()
	applying := x.connection.pendingLocked() != nil
	x.connection.mu.Unlock()
	if applying {
		return
	}
	ctx, cancel := context.WithTimeout(x.connection.ctx, 8*time.Second)
	defer cancel()
	x.mu.Lock()
	var ops []memberComputerChange
	for _, op := range x.changes {
		if !op.Global && op.Status != "completed" {
			ops = append(ops, op)
		}
	}
	x.mu.Unlock()
	for _, op := range ops {
		err := x.applyChange(ctx, &op)
		if err != nil {
			op.Status = "waiting"
			op.Message = "应用待处理：请检查电脑连接后重试原操作。"
			if computer.Is(err, computer.Capacity) {
				op.Message = "运行屏幕已达上限，可提高全局容量或关闭其他成员的电脑。"
			}
		} else {
			op.Message = ""
		}
		_ = x.saveChange(op)
		if op.Status == "completed" {
			x.notify(op.MemberIdentity)
		} else if err == nil {
			x.connection.signal()
		}
	}
}
func (x *computerMembers) applyChange(ctx context.Context, op *memberComputerChange) error {
	current, err := x.currentMember(op.MemberIdentity, op.Directory)
	if err != nil {
		return err
	}
	if !current {
		if err := x.retire(ctx, op.MemberIdentity); err != nil {
			return err
		}
		op.Stage = "done"
		op.Status = "completed"
		return nil
	}
	switch op.Stage {
	case "intent":
		if err := x.savePreference(*op); err != nil {
			return err
		}
		op.Stage = "settle"
	case "settle":
		stop := !op.Enabled || !x.connection.current.Load().Enabled
		if stop {
			if err := x.closeAdmission(ctx, op.MemberIdentity, computerConfig{}, 0, op.BeforeVersion+1, false); err != nil {
				return err
			}
		}
		if err := x.settleScope(ctx, op.MemberIdentity, computer.Scope{}, stop); err != nil {
			return err
		}
		if op.Enabled && x.connection.current.Load().Enabled {
			if _, err := x.screen(ctx, op.MemberIdentity, filepath.Join(op.Directory, "agents", op.MembershipID)); err != nil {
				return err
			}
		}
		op.Stage = "rotate"
	case "rotate":
		if !op.Rotate {
			op.Stage = "done"
			op.Status = "completed"
			return nil
		}
		done, err := x.rotate(ctx, *op)
		if err != nil {
			return err
		}
		if !done {
			return nil
		}
		op.Stage = "done"
		op.Status = "completed"
	case "done":
		op.Status = "completed"
	default:
		return ErrInvalid
	}
	return nil
}
func (x *computerMembers) notify(id MemberIdentity) {
	if svc := x.runtime(id); svc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = svc.target(id.MembershipID).Call[MemberStatus](ctx, ControlMember{MemberIdentity: id, capabilitiesChanged: true})
		svc.invalidate("members", id)
	}
	if x.manager != nil {
		x.manager.updates.mark(uiUpdate{Topic: "settings"})
	}
}
func (x *computerMembers) rotate(ctx context.Context, op memberComputerChange) (bool, error) {
	current, err := x.currentMember(op.MemberIdentity, op.Directory)
	if err != nil {
		return false, err
	}
	if !current {
		return true, x.retire(ctx, op.MemberIdentity)
	}
	svc := x.runtime(op.MemberIdentity)
	if svc != nil {
		opts := SessionOptions{MemberIdentity: op.MemberIdentity, StatePath: filepath.Join(op.Directory, "agents", op.MembershipID, "session.json")}
		ref, exists, err := readSessionReference(opts.StatePath, op.MemberIdentity)
		if err != nil {
			return false, err
		}
		if !exists && !ref.Pending {
			return true, nil
		}
		prior, err := svc.store.loadRotation(op.MemberIdentity)
		if err != nil {
			return false, err
		}
		from := ref.Directory
		continuing := false
		if prior != nil && prior.OperationID == op.RotationID {
			if prior.Phase == "completed" {
				return true, nil
			}
			from = prior.From
			continuing = true
		}
		var release func()
		lease := &rotationLease{gateDone: make(chan struct{}), begin: func(ctx context.Context) error {
			var err error
			if svc.beginPlatformMemberChange != nil {
				release, err = svc.beginPlatformMemberChange(ctx, op.MemberIdentity)
			}
			return err
		}, release: func() {
			if release != nil {
				release()
			}
		}}
		status, err := svc.target(op.MembershipID).Call[MemberStatus](ctx, ControlMember{MemberIdentity: op.MemberIdentity, Action: "rotate", OperationID: op.RotationID, FromGeneration: from, Reason: "computer_change", ContinueRotation: continuing, preserveIntent: true, lease: lease})
		if err != nil {
			lease.close()
			return false, err
		}
		if status.Rotation == nil {
			lease.close()
			return false, ErrConflict
		}
		if status.Rotation.Status == "completed" {
			lease.close()
			return true, nil
		}
		return false, nil
	}
	// The manager lock protects local generation writes only. No provider IO is
	// performed here, and a closed Room is never opened to apply a capability.
	if x.manager != nil {
		x.manager.mu.Lock()
		defer x.manager.mu.Unlock()
		if x.manager.roomBusyLocked(op.RoomIdentity) {
			return false, ErrBusy
		}
	}
	store, err := openStoreWithCatalog(op.Directory, op.RoomID, nil, x.managerCatalog())
	if err != nil {
		return false, err
	}
	defer store.Close()
	opts := SessionOptions{MemberIdentity: op.MemberIdentity, StatePath: filepath.Join(op.Directory, "agents", op.MembershipID, "session.json")}
	ref, exists, err := readSessionReference(opts.StatePath, op.MemberIdentity)
	if err != nil {
		return false, err
	}
	if !exists && !ref.Pending {
		return true, nil
	}
	state, err := store.loadScheduling(op.MemberIdentity)
	if err != nil {
		return false, err
	}
	r, err := store.loadRotation(op.MemberIdentity)
	if err != nil {
		return false, err
	}
	if r == nil || r.OperationID != op.RotationID {
		if r != nil && r.Phase != "completed" {
			return false, ErrBusy
		}
		r = &rotationRecord{Version: rotationVersion, MemberIdentity: op.MemberIdentity, OperationID: op.RotationID, Reason: "computer_change", From: ref.Directory, To: sessionGenerationForOperation(op.RotationID), BaseUsed: state.Used, Phase: "prepared", PreserveIntent: true}
		if err := store.saveRotation(*r); err != nil {
			return false, err
		}
	}
	err = reconcileRotation(store, opts, r, &state)
	return err == nil, err
}
func (x *computerMembers) stopRoom(ctx context.Context, id RoomIdentity) error {
	x.mu.Lock()
	var ids []MemberIdentity
	for member := range x.members {
		if member.RoomIdentity == id {
			ids = append(ids, member)
		}
	}
	for _, op := range x.changes {
		if op.RoomIdentity == id && op.Enabled && !slices.Contains(ids, op.MemberIdentity) {
			ids = append(ids, op.MemberIdentity)
		}
	}
	x.mu.Unlock()
	for _, member := range ids {
		if err := x.retire(ctx, member); err != nil {
			return err
		}
	}
	return nil
}
func (m *roomManager) serveMemberComputer(w http.ResponseWriter, r *http.Request) {
	if m.settings == nil || m.settings.computer == nil || m.settings.computer.members == nil {
		writeAPIError(w, ErrRequest)
		return
	}
	serveMemberComputer(w, r, m.settings.computer.members)
}
func serveMemberComputer(w http.ResponseWriter, r *http.Request, x *computerMembers) {
	if r.Method == http.MethodPost {
		var input memberComputerInput
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			writeAPIError(w, ErrRequest)
			return
		}
		op, err := x.request(input, requestOperator(r))
		if err != nil {
			writeAPIError(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": op.ID, "status": op.Status, "message": op.Message})
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	id := MemberIdentity{RoomIdentity: RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}, AgentID: q.Get("agent_id"), MembershipID: q.Get("membership_id")}
	if validateMemberHeader(formatVersion, id) != nil {
		writeAPIError(w, ErrIdentity)
		return
	}
	member, err := x.preference(id, x.memberDirectory(id))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	view := map[string]any{"computer_enabled": member.ComputerEnabled, "computer_settings_version": member.ComputerSettingsVersion, "global_enabled": x.connection.current.Load().Enabled, "status": "completed"}
	if x.blocked(id) {
		view["status"] = "waiting"
		view["message"] = "电脑执行恢复待处理，请检查电脑连接。"
	}
	x.mu.Lock()
	for _, op := range x.changes {
		if op.MemberIdentity == id && op.Status != "completed" {
			view["operation_id"] = op.ID
			view["status"] = op.Status
			view["message"] = op.Message
		}
	}
	x.mu.Unlock()
	_ = json.NewEncoder(w).Encode(view)
}
func (x *computerMembers) managerCatalog() *platforms.Catalog {
	if x.manager != nil {
		return x.manager.platformCatalog
	}
	return x.service.store.catalog
}

// A global application retains just member identities and existing rotation IDs.
// Session execution/settlement still belongs to the normal rotation record.
func (x *computerMembers) prepareGlobal(ctx context.Context, op *computerChange) error {
	if op.Members == nil {
		// A global disable may supersede an enable waiting for capacity. Keep
		// its preference, pause its executor, and let the global operation own
		// settlement/rotation before that original member operation resumes.
		x.mu.Lock()
		var intents []memberComputerChange
		for _, member := range x.changes {
			if !member.Global && member.Status != "completed" && member.Stage == "intent" {
				intents = append(intents, member)
			}
		}
		x.mu.Unlock()
		for _, member := range intents {
			if err := x.savePreference(member); err != nil {
				return err
			}
		}
		op.Members = []memberComputerChange{}
		if x.manager != nil {
			x.manager.mu.Lock()
			defer x.manager.mu.Unlock()
			for _, room := range x.manager.index.Rooms {
				if room.Status == "deleting" || room.Status == "creating" {
					continue
				}
				dir := x.manager.roomDir(room.RoomID)
				members, err := x.roomPreferences(dir, room.RoomIdentity)
				if err != nil {
					return err
				}
				for _, m := range members {
					if m.ComputerEnabled || x.entry(MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID}) != nil {
						op.Members = append(op.Members, x.globalMember(*op, room.RoomIdentity, m, dir))
					}
				}
			}
		} else if x.service != nil {
			dir := x.service.store.dir
			members, err := x.roomPreferences(dir, x.service.info.RoomIdentity)
			if err != nil {
				return err
			}
			for _, m := range members {
				if m.ComputerEnabled {
					op.Members = append(op.Members, x.globalMember(*op, x.service.info.RoomIdentity, m, dir))
				}
			}
		}
		// Persist affected identities before closing any execution.
		for _, member := range op.Members {
			if err := x.saveChange(member); err != nil {
				return err
			}
		}
		return nil // next connection step commits this list before remote settlement
	}
	for _, member := range op.Members {
		stop := !op.Target.Enabled || op.Before.InstanceID != op.Target.InstanceID || op.Before.Provider != op.Target.Provider
		if stop && op.Before.Enabled {
			if err := x.closeAdmission(ctx, member.MemberIdentity, op.Before, op.Target.Version, member.BeforeVersion, false); err != nil {
				return err
			}
		}
		if err := x.settleScope(ctx, member.MemberIdentity, computer.Scope{}, stop); err != nil {
			return err
		}
	}
	return nil
}
func (x *computerMembers) globalMember(op computerChange, room RoomIdentity, m managedMember, dir string) memberComputerChange {
	id := MemberIdentity{RoomIdentity: room, AgentID: m.AgentID, MembershipID: m.MembershipID}
	key := computer.Hash(struct {
		Operation string
		Member    MemberIdentity
	}{op.ID, id})[:32]
	return memberComputerChange{MemberIdentity: id, ID: key, Digest: op.Digest, Directory: dir, BeforeVersion: m.ComputerSettingsVersion, Enabled: m.ComputerEnabled, Rotate: m.ComputerEnabled && (op.Disruptive || !op.Before.Enabled && op.Target.Enabled), Global: true, RotationID: key, Stage: "rotate", Status: "applying"}
}
func (x *computerMembers) applyGlobal(ctx context.Context, op *computerChange) (bool, error) {
	all := true
	for i := range op.Members {
		member := &op.Members[i]
		x.mu.Lock()
		saved, exists := x.changes[member.ID]
		x.mu.Unlock()
		if exists && saved.Status == "completed" {
			member.Status = "completed"
			continue
		}
		done := !member.Rotate
		var err error
		if member.Rotate {
			done, err = x.rotate(ctx, *member)
		}
		if err != nil {
			return false, err
		}
		if !done {
			all = false
			continue
		}
		member.Status = "completed"
		member.Stage = "done"
		if err := x.saveChange(*member); err != nil {
			return false, err
		}
		x.notify(member.MemberIdentity)
	}
	return all, nil
}

func (x *computerMembers) anyPending() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, op := range x.changes {
		if op.Status != "completed" {
			return true
		}
	}
	return false
}
func (x *computerMembers) pendingWorkspaceLocked(id RoomIdentity) bool {
	if x.manager == nil {
		return false
	}
	for _, op := range x.manager.index.WorkspaceOperations {
		if op.RoomIdentity == id && !op.finished() {
			return true
		}
	}
	return false
}
func (m *roomManager) pendingComputerChangeLocked(id RoomIdentity) bool {
	if m.settings == nil || m.settings.computer == nil || m.settings.computer.members == nil {
		return false
	}
	x := m.settings.computer.members
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, op := range x.changes {
		if op.RoomIdentity == id && op.Enabled {
			return true
		}
	}
	return false
}

func (x *computerMembers) currentMember(id MemberIdentity, dir string) (bool, error) {
	if x.manager != nil {
		x.manager.mu.Lock()
		exists := false
		for _, room := range x.manager.index.Rooms {
			if room.RoomIdentity == id.RoomIdentity {
				exists = true
			}
		}
		x.manager.mu.Unlock()
		if !exists {
			return false, nil
		}
	}
	state, err := readPublishedRoom(dir, id.RoomIdentity)
	if err != nil {
		return false, err
	}
	for _, m := range state.Members {
		if m.AgentID == id.AgentID && m.MembershipID == id.MembershipID {
			return true, nil
		}
	}
	return false, nil
}

func (x *computerMembers) saveMemberPreference(op memberComputerChange) error {
	member, err := x.preference(op.MemberIdentity, op.Directory)
	if err != nil {
		return err
	}
	if member.ComputerSettingsVersion == op.BeforeVersion+1 && member.ComputerEnabled == op.Enabled {
		return nil
	}
	if member.ComputerSettingsVersion != op.BeforeVersion {
		return ErrConflict
	}
	return x.connection.writer.Save(computerPreferencesPath(op.Directory, op.MemberIdentity), computerMemberPreferences{op.MemberIdentity, op.Enabled, op.BeforeVersion + 1}, maxPrivateFileBytes)
}
