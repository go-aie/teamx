package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type inboxMaintenance struct {
	run func(context.Context, MemberIdentity) error
}

type inboxMaintenanceResult struct {
	c       inboxConsumption
	through uint64
	bytes   int64
}

// MaintainInbox receives the outstanding source inventory under the catalog
// gate. An unconfirmed delivery never becomes a fabricated success.
type MaintainInbox struct {
	MemberIdentity
	Policy  notification.ExpiryPolicy
	Now     time.Time
	Pending map[string]bool
}

func (c inboxConsumption) skipped(seq uint64) bool {
	_, found := slices.BinarySearch(c.Skipped, seq)
	return found
}
func (c inboxConsumption) inputThrough(expired uint64) uint64 {
	through := max(c.Through, expired)
	for _, seq := range c.Skipped {
		if seq == through+1 {
			through = seq
		} else if seq > through+1 {
			break
		}
	}
	return through
}

func (a *AgentInbox) Maintain(ctx actorx.Context, msg MaintainInbox, reply actorx.Reply[InboxStatus]) error {
	if msg.MemberIdentity != a.id {
		return ErrIdentity
	}
	if a.writeErr != nil {
		return a.writeErr
	}
	if a.committing || a.previewing {
		return ErrBusy
	}
	if err := msg.Policy.Validate(); err != nil {
		return ErrRequest
	}
	a.committing, a.previewing = true, true
	_, err := actorx.StartTask(ctx.WithContext(context.WithoutCancel(ctx)), func(ctx context.Context) (inboxMaintenanceResult, error) {
		result := inboxMaintenanceResult{a.consumption, a.expiredThrough, a.bytes}
		c, through, err := a.store.planInboxMaintenance(a, msg)
		if err != nil {
			return result, err
		}
		result.c = c
		if through > a.expiredThrough {
			// The skipped checkpoint is already durable. Reclaim uses it to avoid
			// demanding business linkage for input explicitly excluded by expiry.
			aCopy := *a
			aCopy.consumption = c
			remaining, err := a.store.reclaimInbox(&aCopy, through)
			if errors.Is(err, ErrBusy) {
				return result, nil
			}
			if err != nil {
				return result, err
			}
			result.through, result.bytes = through, remaining
		}
		return result, nil
	}, func(result inboxMaintenanceResult, err error) bool {
		a.committing, a.previewing = false, false
		// A skipped checkpoint can commit even if physical cleanup later fails.
		// Unknown writes always fence the actor until a Store reopen.
		if err == nil {
			a.consumption = result.c
			a.expiredThrough, a.bytes = result.through, result.bytes
			for delivery, index := range a.receipts {
				if index.seq <= a.expiredThrough {
					delete(a.receipts, delivery)
				}
			}
		} else if !errors.Is(err, ErrBusy) {
			a.writeErr = err
		}
		reply.Complete(a.inboxStatus(), err)
		return true
	}, nil)
	if err != nil {
		a.committing, a.previewing = false, false
	}
	return err
}

func (s *Store) planInboxMaintenance(a *AgentInbox, msg MaintainInbox) (inboxConsumption, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := a.consumption
	c.Skipped = slices.DeleteFunc(slices.Clone(c.Skipped), func(seq uint64) bool { return seq <= a.expiredThrough })
	if c.Batch != nil && c.inputThrough(a.expiredThrough) < c.Batch.SnapshotSeq {
		return c, a.expiredThrough, ErrBusy
	}
	path, _, err := s.memberPath(a.id, "agent.json", true)
	if err != nil {
		return c, 0, err
	}
	var agent AgentSnapshot
	err = readSnapshot(path, &agent, maxPrivateFileBytes)
	if err == nil {
		if agent.MemberIdentity != a.id || agent.validate() != nil {
			return c, 0, ErrInvalid
		}
		if agent.Turn != nil && agent.Turn.Status == "running" {
			return c, a.expiredThrough, ErrBusy
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return c, 0, err
	}
	cutoff := msg.Policy.Cutoff(msg.Now)
	// Use timestamps already validated by the immutable writer/loader so an
	// idle poll does not reread up to 10,000 files. Validate each newly expired
	// body again before recording a skip; missing/corrupt retained input fails.
	var added []uint64
	if msg.Policy.SkipExpired {
		for _, index := range a.receipts {
			if index.seq <= c.Through || c.skipped(index.seq) || !notification.Expired(index.createdAt, cutoff) {
				continue
			}
			if _, err := s.inboxEntryLocked(a.id, index.seq); err != nil {
				return c, 0, err
			}
			added = append(added, index.seq)
		}
	}
	c.Skipped = append(c.Skipped, added...)
	slices.Sort(c.Skipped)
	if !slices.Equal(c.Skipped, a.consumption.Skipped) {
		if err := s.save(filepath.Join(a.dir, "consumption.json"), c, maxPrivateFileBytes); err != nil {
			return c, 0, err
		}
	}
	through := a.expiredThrough
	for seq := a.expiredThrough + 1; seq <= min(a.latest, a.expiredThrough+128); seq++ {
		e, err := s.inboxEntryLocked(a.id, seq)
		if err != nil {
			return c, 0, err
		}
		if !notification.Expired(e.Item.CreatedAt, cutoff) || msg.Pending[e.Item.DeliveryID] || (seq > c.Through && !c.skipped(seq)) {
			break
		}
		through = seq
	}
	return c, through, nil
}

func (s *service) maintainInboxLocal(ctx context.Context, id MemberIdentity, pending map[string]bool, now time.Time) error {
	s.inboxMu.Lock()
	defer s.inboxMu.Unlock()
	// Avoid creating an empty Inbox for every member on every poll.
	if s.inboxes[id.MembershipID].IsZero() {
		if _, err := os.Stat(filepath.Join(s.store.dir, "inboxes", id.MembershipID)); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	}
	addr, err := s.inboxLocked(ctx, id)
	if err != nil {
		return err
	}
	_, err = addr.Call[InboxStatus](ctx, MaintainInbox{MemberIdentity: id, Policy: s.inboxExpiry.Resolved(), Now: now, Pending: pending})
	if errors.Is(err, ErrBusy) {
		return nil
	}
	return err
}
func (m *roomManager) maintainNotificationInboxLocked(ctx context.Context, meta notification.Metadata) error {
	if !meta.Target.Inbox() {
		return nil
	}
	svc, _ := m.runtimeServiceLocked(meta.Key.Owner)
	if svc == nil || m.roomBusyLocked(meta.Key.Owner) || svc.closing.Load() || svc.restarting.Load() {
		return notification.ErrBusy
	}
	if !svc.changes.TryRLock() {
		return notification.ErrBusy
	}
	defer svc.changes.RUnlock()
	id := MemberIdentity{RoomIdentity: meta.Key.Owner, AgentID: meta.Target.AgentID, MembershipID: meta.Target.MembershipID}
	return roomNotificationError(svc.maintainInboxLocal(ctx, id, m.notifications.PendingDeliveriesLocked(meta.Key.Owner, meta.Target), m.notifications.Now()))
}
