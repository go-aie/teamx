package room

import "github.com/go-aie/actorx"

// notificationObserver only retains the newest notification. It never runs a Session.
//

type notificationObserver struct {
	Identity MemberIdentity
	latest   *UnreadChanged
}

func (m *notificationObserver) Notify(_ actorx.Context, msg UnreadChanged) error {
	if msg.MemberIdentity != m.Identity {
		return ErrIdentity
	}
	if m.latest == nil || msg.Revision > m.latest.Revision {
		copy := msg
		m.latest = &copy
	}
	return nil
}

func (m *notificationObserver) Inspect(_ actorx.Context, msg InspectMember) (MemberStatus, error) {
	if msg.MemberIdentity != m.Identity {
		return MemberStatus{}, ErrIdentity
	}
	result := MemberStatus{}
	if m.latest != nil {
		copy := *m.latest
		result.Notification = &copy
	}
	return result, nil
}

func (m *notificationObserver) Bindings() []actorx.Binding {
	return []actorx.Binding{actorx.Handle(m.Inspect), actorx.HandleMessage(m.Notify)}
}
