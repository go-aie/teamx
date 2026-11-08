package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform/notification"
)

const inboxBatchSize uint64 = 25

type InboxBatch struct {
	ID          string `json:"id"`
	TurnNumber  uint64 `json:"turn_number"`
	Start       uint64 `json:"start"`
	SnapshotSeq uint64 `json:"snapshot_seq"`
	PageThrough uint64 `json:"page_through"`
	Receipt     string `json:"receipt"`
}

type inboxConsumption struct {
	Version int `json:"version"`
	MemberIdentity
	Through uint64      `json:"through"`
	Batch   *InboxBatch `json:"batch,omitempty"`
}

type ReadInboxBatch struct {
	MemberIdentity
	TurnNumber uint64
	Limit      int
}
type ConfirmInboxBatch struct {
	MemberIdentity
	TurnNumber uint64
	Receipt    string
}
type InboxBatchPage struct {
	InboxPage
	Batch InboxBatch `json:"batch"`
}

func (s *Store) loadInboxConsumption(a *AgentInbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := inboxConsumption{Version: 1, MemberIdentity: a.id}
	err := readSnapshot(filepath.Join(a.dir, "consumption.json"), &c, maxPrivateFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		// Followup events can only be linked after a durable batch is issued. A missing
		// cursor alongside business records is corruption, never a fresh inbox.
		for _, name := range []string{filepath.Join("followups", "events")} {
			entries, e := os.ReadDir(filepath.Join(s.dir, "agents", a.id.MembershipID, name))
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
			if len(entries) != 0 {
				return ErrInvalid
			}
		}
		err = nil
	}
	if err != nil {
		return err
	}
	if c.Version != 1 || c.MemberIdentity != a.id || c.Through > a.latest || (c.Through != 0 && c.Batch == nil) {
		return ErrInvalid
	}
	if b := c.Batch; b != nil {
		if !safeMembershipID(b.ID) || !safeMembershipID(b.Receipt) || b.TurnNumber == 0 || b.Start > c.Through ||
			c.Through > b.PageThrough || b.PageThrough > b.SnapshotSeq || b.SnapshotSeq > a.latest || b.Start >= b.SnapshotSeq ||
			b.SnapshotSeq-b.Start > inboxBatchSize {
			return ErrInvalid
		}
	}
	// A consumed notification must still have its authoritative business
	// record. Never recover a cursor that would hide a missing/corrupt followup event.
	for seq := uint64(1); seq <= c.Through; seq++ {
		e, err := s.inboxEntryLocked(a.id, seq)
		if err != nil {
			return err
		}
		if err := s.checkInboxBusinessLinkLocked(e); err != nil {
			return err
		}
	}
	a.consumption = c
	return nil
}

// Owned by Inbox. The member's linkage is checked under the same Store lock
// immediately before consumption advances. Two files need no atomic commit:
// linkage always commits first and is idempotent across an interrupted ack.
func (s *Store) saveInboxConsumption(c inboxConsumption, confirm bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, err := s.runningTurnLocked(c.MemberIdentity, c.Batch.TurnNumber)
	if err != nil {
		return err
	}
	if c.Batch.SnapshotSeq > agent.Turn.InboxSnapshotSeq {
		return ErrConflict
	}
	if confirm {
		for seq := c.Batch.Start + 1; seq <= c.Through; seq++ {
			e, err := s.inboxEntryLocked(c.MemberIdentity, seq)
			if err != nil {
				return err
			}
			admission, err := s.inboxAdmissionLocked(e)
			if err != nil {
				return err
			}
			if !admission.Verified {
				return ErrBusy
			}
			if err := s.checkInboxBusinessLinkLocked(e); err != nil {
				return err
			}
		}
	}
	return s.save(filepath.Join(s.dir, "inboxes", c.MembershipID, "consumption.json"), c, maxPrivateFileBytes)
}

func (s *Store) checkInboxBusinessLinkLocked(e InboxEntry) error {
	admission, err := s.inboxAdmissionLocked(e)
	if err != nil {
		return err
	}
	if admission.Verified && admission.Decision == notification.AdmissionExcluded {
		return nil
	}
	_, err = s.loadFollowupEventLocked(e)
	return err
}

func (a *AgentInbox) ReadBatch(ctx actorx.Context, msg ReadInboxBatch, reply actorx.Reply[InboxBatchPage]) error {
	if msg.MemberIdentity != a.id {
		return ErrIdentity
	}
	if a.writeErr != nil {
		return a.writeErr
	}
	if a.committing || a.previewing {
		return ErrBusy
	}
	if msg.TurnNumber == 0 || msg.Limit < 0 || msg.Limit > int(inboxBatchSize) {
		return ErrRequest
	}
	limit := msg.Limit
	if limit == 0 {
		limit = int(inboxBatchSize)
	}
	limit = min(limit, a.limits.PageSize)
	c := a.consumption
	if c.Batch != nil {
		b := *c.Batch
		c.Batch = &b
	}
	latest := a.latest
	a.committing, a.previewing = true, true
	_, err := actorx.StartTask(ctx.WithContext(context.WithoutCancel(ctx)), func(ctx context.Context) (InboxBatchPage, error) {
		if err := ctx.Err(); err != nil {
			return InboxBatchPage{}, err
		}
		a.store.mu.Lock()
		agent, err := a.store.runningTurnLocked(a.id, msg.TurnNumber)
		a.store.mu.Unlock()
		if err != nil {
			return InboxBatchPage{}, err
		}
		end := min(latest, agent.Turn.InboxSnapshotSeq)
		if end <= c.Through {
			// A Room-only turn or an exhausted fixed snapshot has no Inbox work.
			// Do not issue a receipt or change the durable consumption cursor.
			return InboxBatchPage{InboxPage: InboxPage{MemberIdentity: a.id, SnapshotSeq: end, NextAfter: end, Entries: []InboxEntry{}},
				Batch: InboxBatch{TurnNumber: msg.TurnNumber, Start: end, SnapshotSeq: end, PageThrough: end}}, nil
		}
		if c.Batch == nil || c.Batch.SnapshotSeq <= c.Through {
			id, err := newOpaqueID()
			if err != nil {
				return InboxBatchPage{}, err
			}
			c.Batch = &InboxBatch{ID: id, TurnNumber: msg.TurnNumber, Start: c.Through, SnapshotSeq: min(end, c.Through+inboxBatchSize)}
		} else {
			if c.Batch.SnapshotSeq > end {
				return InboxBatchPage{}, ErrConflict
			}
			c.Batch.TurnNumber = msg.TurnNumber // Explicitly resumed execution, same batch.
		}
		page, err := a.preview(c.Through, c.Batch.SnapshotSeq, limit)
		if err != nil {
			return InboxBatchPage{}, err
		}
		if page.NextAfter <= c.Through {
			return InboxBatchPage{}, ErrInvalid
		}
		// Persist a single outstanding page. Re-reading it preserves its receipt.
		if c.Batch.PageThrough != page.NextAfter || c.Batch.Receipt == "" {
			c.Batch.Receipt, err = newOpaqueID()
			if err != nil {
				return InboxBatchPage{}, err
			}
		}
		c.Batch.PageThrough = page.NextAfter
		if err := a.store.saveInboxConsumption(c, false); err != nil {
			return InboxBatchPage{}, err
		}
		return InboxBatchPage{InboxPage: page, Batch: *c.Batch}, nil
	}, func(page InboxBatchPage, err error) bool {
		a.committing, a.previewing = false, false
		if err == nil {
			a.consumption = c
		}
		if errors.Is(err, ErrCommitUnknown) {
			a.writeErr = err
		}
		reply.Complete(page, err)
		return true
	}, nil)
	if err != nil {
		a.committing, a.previewing = false, false
	}
	return err
}

func (a *AgentInbox) ConfirmBatch(ctx actorx.Context, msg ConfirmInboxBatch, reply actorx.Reply[InboxStatus]) error {
	if msg.MemberIdentity != a.id {
		return ErrIdentity
	}
	if a.writeErr != nil {
		return a.writeErr
	}
	if a.committing {
		return ErrBusy
	}
	c := a.consumption
	if c.Batch == nil || msg.Receipt == "" || c.Batch.Receipt != msg.Receipt || msg.TurnNumber != c.Batch.TurnNumber {
		return ErrConflict
	}
	if c.Through == c.Batch.PageThrough {
		reply.Complete(a.inboxStatus(), nil)
		return nil
	}
	c.Through = c.Batch.PageThrough
	a.committing = true
	_, err := actorx.StartTask(ctx.WithContext(context.WithoutCancel(ctx)), func(ctx context.Context) (struct{}, error) {
		if err := ctx.Err(); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, a.store.saveInboxConsumption(c, true)
	}, func(_ struct{}, err error) bool {
		a.committing = false
		if err == nil {
			a.consumption = c
		}
		if errors.Is(err, ErrCommitUnknown) {
			a.writeErr = err
		}
		reply.Complete(a.inboxStatus(), err)
		return true
	}, nil)
	if err != nil {
		a.committing = false
	}
	return err
}
