package room

import (
	"context"
	"fmt"
	"time"

	"github.com/go-aie/actorx"
)

func (r *Room) notification(m MemberState) UnreadChanged {
	count, newest := r.unread(m)
	return UnreadChanged{
		MemberIdentity: MemberIdentity{RoomIdentity: r.state.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID},
		Revision:       r.state.Revision, LatestSeq: r.state.LatestSeq, NewestUnreadSeq: newest, UnreadCount: count,
	}
}

func (r *Room) queueNotifications() {
	for _, m := range r.state.Members {
		if !r.targets[m.MembershipID].IsZero() {
			r.pending[m.MembershipID] = r.notification(m)
		}
	}
}

type notificationDelivery struct {
	target actorx.Addr
	state  UnreadChanged
	err    error
}

// One batch owns at most maxMembers immutable deliveries. Send only admits a
// notification; periodic reconciliation repairs loss after admission.
func (r *Room) dispatchNotifications() {
	if r.notifying || len(r.pending) == 0 || r.now().Before(r.nextNotify) {
		return
	}
	batch := make([]notificationDelivery, 0, len(r.pending))
	for _, m := range r.state.Members {
		if n, ok := r.pending[m.MembershipID]; ok {
			batch = append(batch, notificationDelivery{target: r.targets[m.MembershipID], state: n})
		}
	}
	timeout := r.notificationTimeout
	if timeout == 0 {
		timeout = 100 * time.Millisecond
	}
	r.notifying = true
	_, err := actorx.StartTask(r.owner,
		func(ctx context.Context) ([]notificationDelivery, error) {
			for i := range batch {
				sendCtx, cancel := context.WithTimeout(ctx, timeout)
				batch[i].err = batch[i].target.Send(sendCtx, batch[i].state)
				cancel()
			}
			return batch, nil
		},
		func(done []notificationDelivery, taskErr error) bool {
			r.notifying = false
			failed := taskErr != nil
			for _, d := range done {
				current, ok := r.pending[d.state.MembershipID]
				if !ok || r.targets[d.state.MembershipID] != d.target {
					continue
				}
				if d.err != nil {
					failed = true
					continue
				}
				if taskErr == nil && current.Revision == d.state.Revision {
					delete(r.pending, d.state.MembershipID)
				}
			}
			if failed {
				r.nextNotify = r.now().Add(time.Second)
			}
			r.dispatchNotifications()
			return true
		}, nil)
	if err != nil {
		r.notifying = false
		r.nextNotify = r.now().Add(time.Second)
	}
}

// Maintenance is established before Spawn reports readiness. Losing its task
// reservation later stops the Room explicitly rather than silently stranding
// pending notifications. TeamX service assembly reserves ample capacity.
func (r *Room) startMaintenance() error {
	delay := r.maintenanceInterval
	_, err := actorx.StartTask(r.owner,
		func(ctx context.Context) (struct{}, error) {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return struct{}{}, ctx.Err()
			case <-timer.C:
				return struct{}{}, nil
			}
		},
		func(_ struct{}, err error) bool {
			if err != nil {
				return true
			}
			if err := r.startMaintenance(); err != nil {
				r.owner.Stop(fmt.Errorf("Room maintenance unavailable: %w", err))
				return true
			}
			now := r.now()
			for id, page := range r.pages {
				if id != r.confirming && !now.Before(page.expires) {
					delete(r.pages, id)
				}
			}
			if !now.Before(r.nextSync) {
				r.queueNotifications()
				r.nextSync = now.Add(5 * time.Second)
			}
			r.dispatchNotifications()
			return true
		}, nil)
	return err
}
