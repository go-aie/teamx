package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-aie/actorx"
)

// Production supplies the threshold and the service coordinator together.
// Backends lacking rotation retain their cumulative admission limit.
func (m *MemberAgent) automaticRotation() bool {
	_, capable := m.Backend.(rotatingBackend)
	return capable && m.SessionMaxTurns > 0
}
func (m *MemberAgent) needsRotation() bool {
	return m.automaticRotation() && (m.sessionGeneration == "" || m.turnsUsed-m.sessionStartUsed >= m.SessionMaxTurns)
}
func (m *MemberAgent) requestRotation() {
	if m.rotationRequest || m.now().Before(m.rotationNext) {
		return
	}
	if m.RequestRotation == nil {
		m.BackendBlocked = "automatic Session rotation coordinator unavailable"
		m.lastError = m.BackendBlocked
		return
	}
	id, from, request := m.Options.MemberIdentity, m.currentGeneration, m.RequestRotation
	_, err := actorx.StartTask(m.owner, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, request(ctx, id, from)
	}, func(_ struct{}, err error) bool {
		m.rotationRequest = false
		if errors.Is(err, ErrBusy) {
			m.rotationNext = m.now().Add(100 * time.Millisecond)
		}
		if err != nil && !errors.Is(err, ErrBusy) && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrClosing) {
			m.lastError = "automatic rotation admission: " + err.Error()
			m.BackendBlocked = m.lastError
		}
		m.schedule()
		return true
	}, nil)
	if err != nil {
		m.lastError = err.Error()
		return
	}
	m.rotationRequest = true
}

func (s *service) requestMemberRotation(ctx context.Context, id MemberIdentity, from string) error {
	if id.RoomIdentity != s.info.RoomIdentity {
		return ErrIdentity
	}
	inbox, err := s.memberInboxStatus(ctx, id)
	if err != nil {
		return err
	}
	// The actor has already released its execution slot. Service admission is
	// shared with manual rotation, removal, configuration and shutdown.
	_, err = s.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID,
		FromGeneration: from, RotationReason: "session_max_turns", Resume: true, automatic: true, automaticInbox: &inbox}, "rotate")
	return err
}

// Convert each durable snapshot before using it. An interrupted conversion can
// be retried by version, including catalog-embedded creation/workspace receipts.
func (m *roomManager) upgradeSessionThresholds() error {
	if m.index.Version != roomsIndexVersion {
		return ErrVersion
	}
	if m.index.Rooms == nil || len(m.index.Rooms) > maxRooms {
		return ErrInvalid
	}
	next := m.copyIndex()
	changed := false
	upgrade := func(c *managedRoomConfig) error {
		if c.Version != formatVersion {
			return nil
		}
		converted, err := convertManagedRoomConfig(*c)
		if err != nil {
			return err
		}
		*c = converted
		changed = true
		return nil
	}
	for i := range next.Rooms {
		e := &next.Rooms[i]
		if !safeMembershipID(e.RoomID) || !validText(e.RoomInstance) {
			return ErrIdentity
		}
		if e.Initialization != nil {
			init := *e.Initialization
			if err := upgrade(&init.Config); err != nil {
				return err
			}
			e.Initialization = &init
		}
		if e.Status != "ready" && e.Status != "creating" {
			continue
		}
		path := filepath.Join(m.roomDir(e.RoomID), "room-config.json")
		var c managedRoomConfig
		err := readSnapshot(path, &c, maxPrivateFileBytes)
		if err != nil {
			if e.Status == "creating" && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if c.RoomIdentity != e.RoomIdentity {
			return ErrIdentity
		}
		if err := validateInitialization(*e, roomInitialization{Config: c}); err != nil {
			return err
		}
		if c.Version == formatVersion {
			if err := upgrade(&c); err != nil {
				return err
			}
			if err := m.store.save(path, c, maxPrivateFileBytes); err != nil {
				return fmt.Errorf("Session threshold conversion: %w", err)
			}
		}
	}
	for i := range next.WorkspaceOperations {
		op := &next.WorkspaceOperations[i]
		if op.PreviousConfig != nil {
			if err := upgrade(op.PreviousConfig); err != nil {
				return err
			}
		}
		if op.PreparedConfig != nil {
			if err := upgrade(op.PreparedConfig); err != nil {
				return err
			}
		}
	}
	if changed {
		return m.saveIndex(next)
	}
	return nil
}
