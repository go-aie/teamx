package room

import (
	"context"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
)

// User intent belongs to the scheduling owner, not the worker checkpoint. Keep
// the event publication intent here too, so a crash/lost reply can be repaired
// with the same Room event rather than a second notification system.
type userStop struct {
	ExecutionID uint64    `json:"execution_id"`
	Turn        uint64    `json:"turn"`
	Generation  string    `json:"generation,omitempty"`
	Actor       Author    `json:"actor"`
	Name        string    `json:"name,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Published   bool      `json:"published,omitempty"`
}

func (m *MemberAgent) recordUserStop(actor Author, name string) error {
	// Internal actor controls have no authenticated operator. They may cancel
	// execution, but must never manufacture a human instruction in Room history.
	if actor == (Author{}) {
		return nil
	}
	if actor.Kind != "human" || !validText(actor.ID) {
		return ErrIdentity
	}
	if m.userStop != nil && m.userStop.ExecutionID == m.turnsUsed {
		m.work.userStopped.Store(true)
		return nil
	}
	if m.userStop != nil && !m.userStop.Published {
		return ErrBusy
	}
	previous := m.userStop
	m.userStop = &userStop{ExecutionID: m.turnsUsed, Turn: m.state.TurnNumber + 1,
		Generation: m.currentGeneration, Actor: actor, Name: name, CreatedAt: m.now().UTC()}
	m.RecoveryPending, m.recoveryArmed, m.recoveryAttempted = false, false, false
	m.sessionRecovery, m.rotationNext = nil, time.Time{}
	m.PersistentScheduling = true
	if err := m.persistScheduling(m.enabled, m.turnsUsed); err != nil {
		m.userStop = previous
		return err
	}
	m.work.userStopped.Store(true)
	return nil
}

func (m *MemberAgent) publishUserStop(ctx context.Context) error {
	r := m.userStop
	if r == nil || r.Published {
		return nil
	}
	id := m.Options.MemberIdentity
	operation := inputstream.Ref("user_stop_", id, r.ExecutionID, r.Generation)
	summary := "用户要求停止当前执行。"
	if r.Name != "" {
		summary = "用户要求停止「" + r.Name + "」当前执行。"
	}
	_, err := m.Room.Call[PostReceipt](ctx, AppendRoomEvent{RoomIdentity: id.RoomIdentity,
		Event: RoomEvent{ID: operation, Kind: "user_stop", OperationID: operation, ResultVersion: 1,
			ResultRef: "execution/" + operation, Actor: r.Actor},
		Summary: summary, CreatedAt: r.CreatedAt})
	if err != nil {
		return err
	}
	// No targets: visible in Room history, without becoming pending work.
	published := *r
	published.Published = true
	m.userStop = &published
	if err := m.persistScheduling(m.enabled, m.turnsUsed); err != nil {
		m.userStop = r
		return err
	}
	return nil
}
