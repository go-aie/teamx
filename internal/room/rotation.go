package room

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

type rotatingBackend interface {
	// ownedSettled means this actor has joined Run and verified that all Session
	// execution stopped. Otherwise durable settlement evidence is mandatory.
	RotateSession(SessionOptions, bool) (Backend, error)
}

func (b dshBackend) RotateSession(opts SessionOptions, ownedSettled bool) (Backend, error) {
	ref, exists, err := b.reference(opts)
	if err != nil {
		return nil, err
	}
	if exists && !ownedSettled {
		dir := ref.Directory
		if dir == "" {
			dir = filepath.Base(dshStateDir(opts.StatePath))
		}
		if err := dsh.CheckReset(filepath.Join(filepath.Dir(opts.StatePath), dir)); err != nil {
			return nil, fmt.Errorf("old execution has no verified settlement: %w", err)
		}
	}
	generation, err := newOpaqueID()
	if err != nil {
		return nil, err
	}
	ref = dshReference{Version: formatVersion, MemberIdentity: opts.MemberIdentity,
		Directory: filepath.Base(dshStateDir(opts.StatePath)) + "-" + generation, Pending: true}
	b.store.mu.Lock()
	path, err := b.store.dshPath(opts.MemberIdentity, "session.json", true)
	if err == nil {
		err = b.store.save(path, ref, maxPrivateFileBytes)
	}
	b.store.mu.Unlock()
	if err != nil {
		return nil, err
	}
	b.recover = false
	return b, nil
}

type memberRotation struct {
	session *ownedSession
}

func (s *service) rotateSession(ctx context.Context, req apiRequest) (MemberStatus, error) {
	status, err := s.control(ctx, req, "rotate")
	if err != nil {
		return status, err
	}
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if status.Rotation != nil {
			switch status.Rotation.Status {
			case "completed":
				return status, nil
			case "failed":
				return status, fmt.Errorf("rotation failed: %s", status.Rotation.Error)
			}
		}
		select {
		case <-wait.Done():
			return status, fmt.Errorf("rotation incomplete; member still owns cleanup; inspect member list before retrying: %w", wait.Err())
		case <-ticker.C:
		}
		status, err = s.target(req.MembershipID).Call[MemberStatus](wait, InspectMember{id})
		if err != nil {
			return status, fmt.Errorf("rotation outcome unknown; inspect member list: %w", err)
		}
	}
}

// Runs only after the previous turn's completion callback has adopted its
// resource. No new turn can start until this task itself has been adopted.
func (m *MemberAgent) rotate() {
	if m.rotation == nil || m.rotation.Status != "waiting" || m.active || m.rotationWork != nil {
		return
	}
	if m.fault {
		m.rotation.Status, m.rotation.Error = "failed", "checkpoint failure requires service recovery"
		return
	}
	backend := m.Backend.(rotatingBackend)
	opts, room := m.Options, m.Room
	work := &memberRotation{session: m.session}
	type result struct {
		backend Backend
		state   UnreadChanged
	}
	_, err := actorx.StartTask(m.owner, func(ctx context.Context) (result, error) {
		if err := work.session.close(); !dsh.CleanupSettled(err) {
			return result{}, fmt.Errorf("old Session cleanup failed; rotation incomplete: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return result{}, err
		}
		next, err := backend.RotateSession(opts, work.session != nil)
		if err != nil {
			return result{}, err
		}
		list, err := room.Call[MemberList](ctx, ListMembers{opts.RoomIdentity})
		if err != nil {
			return result{backend: next}, err
		}
		for _, member := range list.Members {
			if member.AgentID == opts.AgentID && member.MembershipID == opts.MembershipID {
				return result{next, UnreadChanged{MemberIdentity: opts.MemberIdentity, Revision: list.Revision,
					LatestSeq: list.LatestSeq, NewestUnreadSeq: member.NewestUnreadSeq, UnreadCount: member.UnreadCount}}, nil
			}
		}
		return result{backend: next}, ErrIdentity
	}, func(r result, err error) bool {
		m.rotationWork = nil
		m.rotation.Status = "completed"
		m.session = nil
		if r.backend != nil {
			m.Backend = r.backend
		}
		if err != nil {
			m.rotation.Status, m.rotation.Error = "failed", err.Error()
			m.lastError = err.Error()
			m.BackendBlocked = "rotation incomplete; inspect state before retry: " + err.Error()
			// Keep the closed resource as cleanup evidence. Never infer that a
			// failed Close allowed replacement execution.
			m.session = work.session
		} else {
			m.rotation.ContextReset = true
			m.BackendBlocked, m.lastError = "", ""
			m.paused = !m.rotationResume
			if err := m.persistScheduling(!m.paused, m.turnsUsed); err != nil {
				m.rotation.Status, m.rotation.Error = "failed", err.Error()
			}
			_ = m.Notify(m.owner, r.state)
		}
		m.schedule()
		return true
	}, func(result) {
		if err := work.session.close(); err != nil {
			m.owner.Stop(errors.Join(errors.New("rotation discard cleanup failed"), err))
		}
	})
	if err != nil {
		m.lastError = err.Error() // Maintenance retries bounded task admission.
		return
	}
	m.session = nil
	m.rotationWork = work
	m.rotation.Status = "closing"
}
