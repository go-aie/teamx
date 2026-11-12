package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func (c *modelController) apply(op *modelChangeOperation) {
	if !op.Committed {
		c.service.document.mu.Lock()
		var reconcileErr error
		if c.service.document.pending != nil {
			reconcileErr = c.service.document.reconcile()
		}
		c.service.document.mu.Unlock()
		c.manager.mu.Lock()
		if reconcileErr == nil && op.Target != nil {
			if op.Input.Kind == "selection" {
				op.Committed = c.selectionCommitted(op)
			} else {
				op.Committed = reflect.DeepEqual(c.manager.config.models.catalog(), *op.Target)
			}
		}
		c.manager.mu.Unlock()
	}
	c.manager.mu.Lock()
	if !op.Committed {
		op.Status = "failed"
		op.Message = "请重启服务核对配置提交事实后重试。"
		_ = c.persist(op)
		c.manager.mu.Unlock()
		return
	}
	op.Status = "applying"
	_ = c.persist(op)
	c.manager.mu.Unlock()
	var wg sync.WaitGroup
	for i := range op.Members {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := c.applyMember(op, i)
			c.manager.mu.Lock()
			if err != nil {
				op.Members[i].Error = modelChangeError(err)
			} else {
				op.Members[i].Error = ""
				if c.manager.config.models.gate(op.Members[i].MemberIdentity) == op.ID {
					c.manager.config.models.setGate(op.Members[i].MemberIdentity, "")
				}
			}
			_ = c.persist(op)
			c.manager.mu.Unlock()
		}(i)
	}
	wg.Wait()
	c.manager.mu.Lock()
	defer c.manager.mu.Unlock()
	op.Status = "completed"
	op.Message = "模型配置已保存，相关成员已完成应用。"
	for _, member := range op.Members {
		if member.Error != "" {
			op.Status = "failed"
			op.Message = "配置已保存，部分成员尚未完成；可重试同一目标。"
			break
		}
	}
	if op.Status == "completed" && op.Input.Kind == "delete" {
		if err := c.finishDeletionLocked(op); err != nil {
			op.Status, op.Message = "failed", modelChangeError(err)
		}
	}
	_ = c.persist(op)
}
func (c *modelController) applyMember(op *modelChangeOperation, index int) error {
	member := op.Members[index]
	m := c.manager
	completed := c.memberCompleted(member)
	if op.Input.Kind == "delete" && !completed {
		m.mu.Lock()
		exists, err := c.replaceDeletedSelectionLocked(member)
		m.mu.Unlock()
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
	}
	if completed {
		m.mu.Lock()
		svc, _ := m.runtimeServiceLocked(member.RoomIdentity)
		owns := m.config.models.gate(member.MemberIdentity) == op.ID
		m.mu.Unlock()
		if owns && svc != nil {
			gate := ""
			_, err := svc.target(member.MembershipID).Call[MemberStatus](c.ctx, ControlMember{MemberIdentity: member.MemberIdentity, modelGate: &gate})
			return err
		}
		return nil
	}
	if member.Rotate && !completed {
		select {
		case c.runtimeSlots <- struct{}{}:
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
		actual, err := c.inspect(c.ctx, dsh.Options{BinaryPath: member.Target.Profile.BinaryPath, BinaryArgs: member.Target.Profile.BinaryArgs, Artifact: member.Target.Profile.Artifact})
		<-c.runtimeSlots
		if err != nil {
			return err
		}
		expected := member.Target.Runtime
		expected.Agent = dsh.Artifact{}
		if !reflect.DeepEqual(actual, expected) {
			return errModelRuntimeIdentity
		}
	}
	m.mu.Lock()
	refs, err := c.referencesLocked()
	if err != nil {
		m.mu.Unlock()
		return err
	}
	exists := false
	for _, ref := range refs {
		if ref.MemberIdentity == member.MemberIdentity {
			exists = true
			if ref.Selection != member.Selection || ref.Version != member.SelectionVersion {
				m.mu.Unlock()
				return ErrConflict
			}
		}
	}
	if !exists {
		m.mu.Unlock()
		return nil // the exact membership was removed; never adopt its replacement
	}
	if !member.Rotate {
		m.mu.Unlock()
		return nil
	}
	svc, _ := m.runtimeServiceLocked(member.RoomIdentity)
	if svc == nil {
		if m.roomBusyLocked(member.RoomIdentity) {
			m.mu.Unlock()
			return ErrBusy
		}
		// Closed rooms stay closed. Only local settlement and fixed generation files
		// are touched, without starting actors, DSH or an unsolicited model turn.
		err = c.applyClosedMember(member)
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	ctx := c.ctx
	opts := SessionOptions{MemberIdentity: member.MemberIdentity, StatePath: filepath.Join(svc.store.dir, "agents", member.MembershipID, "session.json")}
	prior, err := svc.store.loadRotation(member.MemberIdentity)
	if err != nil {
		return err
	}
	from, err := (dshBackend{store: svc.store}).sessionGeneration(opts)
	if err != nil {
		return err
	}
	continuing := false
	if prior != nil && prior.OperationID == member.RotationID {
		from = prior.From
		continuing = true
		if prior.Phase == "completed" {
			gate := ""
			_, err = svc.target(member.MembershipID).Call[MemberStatus](ctx, ControlMember{MemberIdentity: member.MemberIdentity, modelGate: &gate})
			return err
		}
	}
	var releasePlatform func()
	lease := &rotationLease{gateDone: make(chan struct{}), begin: func(ctx context.Context) error {
		var err error
		if svc.beginPlatformMemberChange != nil {
			releasePlatform, err = svc.beginPlatformMemberChange(ctx, member.MemberIdentity)
		}
		return err
	}, release: func() {
		if releasePlatform != nil {
			releasePlatform()
		}
	}}
	status, err := svc.target(member.MembershipID).Call[MemberStatus](ctx, ControlMember{MemberIdentity: member.MemberIdentity, Action: "rotate", OperationID: member.RotationID, FromGeneration: from, Reason: "model_change", ContinueRotation: continuing, modelTarget: &member.Target, lease: lease})
	if err != nil {
		lease.close()
		return err
	}
	// On idempotent observation the actor already owns another lease.
	if status.Rotation != nil && status.Rotation.Status == "completed" {
		lease.close()
		return nil
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if status.Rotation == nil || status.Rotation.OperationID != member.RotationID {
			return ErrConflict
		}
		if status.Rotation.Status == "failed" {
			return errModelApplication
		}
		if status.Rotation.Status == "completed" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		status, err = svc.target(member.MembershipID).Call[MemberStatus](ctx, InspectMember{member.MemberIdentity})
		if err != nil {
			return err
		}
	}
}
func (c *modelController) applyClosedMember(member modelChangeMember) error {
	store, err := OpenStore(c.manager.roomDir(member.RoomID), member.RoomID)
	if err != nil {
		return err
	}
	defer store.Close()
	room, err := store.LoadRoom()
	if err != nil {
		return err
	}
	if room.RoomIdentity != member.RoomIdentity {
		return ErrIdentity
	}
	scheduling, err := store.loadScheduling(member.MemberIdentity)
	if errors.Is(err, os.ErrNotExist) {
		agent, loadErr := store.LoadAgent(member.MemberIdentity)
		if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
			return loadErr
		}
		if agent.TurnNumber != 0 || agent.Turn != nil {
			return ErrConflict
		}
		generation, generationErr := (dshBackend{store: store}).sessionGeneration(SessionOptions{MemberIdentity: member.MemberIdentity, StatePath: filepath.Join(store.dir, "agents", member.MembershipID, "session.json")})
		if generationErr != nil {
			return generationErr
		}
		scheduling = memberScheduling{Version: schedulingVersion, MemberIdentity: member.MemberIdentity, Enabled: true, SessionGeneration: generation}
		err = store.saveScheduling(scheduling)
	}
	if err != nil {
		return err
	}
	opts := SessionOptions{MemberIdentity: member.MemberIdentity, StatePath: filepath.Join(store.dir, "agents", member.MembershipID, "session.json")}
	rotation, err := store.loadRotation(member.MemberIdentity)
	if err != nil {
		return err
	}
	if rotation != nil && rotation.OperationID == member.RotationID {
		return reconcileRotation(store, opts, rotation, &scheduling)
	}
	if rotation != nil && rotation.Phase != "completed" {
		return ErrBusy
	}
	b := dshBackend{store: store}
	from, err := b.sessionGeneration(opts)
	if err != nil {
		return err
	}
	target := member.Target.clone()
	target.Generation = "session.json.dsh-" + member.RotationID
	rotation = &rotationRecord{Version: rotationVersion, MemberIdentity: member.MemberIdentity, OperationID: member.RotationID, Reason: "model_change", From: from, To: target.Generation, BaseUsed: scheduling.Used, Phase: "prepared", ModelTarget: &target}
	if err := store.saveRotation(*rotation); err != nil {
		return err
	}
	return reconcileRotation(store, opts, rotation, &scheduling)
}
