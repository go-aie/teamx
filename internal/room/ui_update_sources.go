package room

import (
	"reflect"
	"time"
)

// Adapters only mark bounded in-memory keys. They must never call back into a
// producer: Store, actors, model and platform owners may still hold their gate.
func (s *service) invalidate(topic string, id MemberIdentity) {
	if topic == "models" {
		s.updates.mark(uiUpdate{Topic: "models"})
		return
	}
	u := roomUIUpdate(topic, s.info)
	u.AgentID, u.MembershipID = id.AgentID, id.MembershipID
	s.updates.mark(u)
}

// Manager callers hold m.mu. Resolving an already registered runtime is an
// in-memory lookup; notifications never open closed Rooms.
func (m *roomManager) invalidateRoomLocked(topic string, id MemberIdentity) {
	if svc, _ := m.runtimeServiceLocked(id.RoomIdentity); svc != nil {
		svc.invalidate(topic, id)
	}
}

func (m *roomManager) authorizationChanged(owner string) {
	m.updates.mark(uiUpdate{Topic: "authorization", Operator: owner})
}

// Compare the public status only on the actor's serialized state loop. This
// includes ephemeral waiting/cancel/recovery/rotation states that do not commit
// Room revisions. No output snapshot, I/O, or work is done without an observer.
func (m *MemberAgent) observeStatus() {
	if m.Observe == nil {
		return
	}
	if m.ObserveEnabled != nil && !m.ObserveEnabled() {
		m.observedStatus = nil
		return
	}
	current := m.status()
	if current.Inbox != nil {
		current.Inbox.Committing, current.Inbox.Previewing = false, false
	}
	if m.observedStatus == nil || !reflect.DeepEqual(*m.observedStatus, current) {
		m.observedStatus = &current
		m.Observe()
	}
}

func (s *Store) changed(topic string, id MemberIdentity) {
	if s.observe != nil {
		s.observe(topic, id)
	}
}

func (a *AgentInbox) observeInstalled() {
	if a.store.observe == nil {
		return
	}
	current := a.inboxStatus()
	current.Committing, current.Previewing = false, false
	if a.observedInbox == nil || !reflect.DeepEqual(*a.observedInbox, current) {
		a.observedInbox = &current
		a.store.changed("inbox", a.id)
		a.store.changed("members", a.id)
	}
}

func (s *Store) policyRead(p PolicyView) {
	if s.observeExpiry == nil {
		return
	}
	var next time.Time
	for _, r := range p.Effective {
		if r.ExpiresAt != nil && (next.IsZero() || r.ExpiresAt.Before(next)) {
			next = *r.ExpiresAt
		}
	}
	s.observeExpiry("policy", p.MemberIdentity, next)
}
