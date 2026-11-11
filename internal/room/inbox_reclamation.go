package room

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Expiry and reclamation use the same closed dispatch as reads/confirmation.
// Internal events never inherit platform expiry or depend on long-lived work.
func (s *Store) inboxReclaimEligibleLocked(e InboxEntry, c inboxConsumption, msg MaintainInbox, cutoff time.Time) (bool, error) {
	switch e.payloadType() {
	case InboxPlatformNotification:
		return notification.Expired(e.Item.CreatedAt, cutoff) && !msg.Pending[e.Item.DeliveryID] && (e.Seq <= c.Through || c.skipped(e.Seq)), nil
	case InboxTaskEventType:
		if e.Seq > c.Through || c.skipped(e.Seq) {
			return false, nil
		}
		if err := s.checkTaskEventLinkLocked(e); err != nil {
			return false, err
		}
		var link inboxTaskLink
		if err := readSnapshot(s.taskEventPath(e.MemberIdentity, "links", taskLinkKey(e.Seq)), &link, maxPrivateFileBytes); err != nil {
			return false, err
		}
		return !msg.Now.Before(link.LinkedAt.Add(inboxTaskRetention)), nil
	}
	return false, ErrInvalid
}

func (s *Store) prepareInboxReclaimLocked(e InboxEntry, c inboxConsumption) error {
	if !e.validPayload() {
		return ErrInvalid
	}
	if e.payloadType() == InboxTaskEventType {
		if e.Seq > c.Through || c.skipped(e.Seq) {
			return ErrBusy
		}
		return s.checkTaskEventLinkLocked(e)
	}
	if e.Seq <= c.Through && !c.skipped(e.Seq) {
		if err := s.checkInboxBusinessLinkLocked(e); err != nil {
			return err
		}
	}
	// Context-only platform messages can link existing work without opening it.
	if _, err := os.Stat(s.eventPath(e.MemberIdentity, e.Seq)); err == nil {
		link, err := s.loadFollowupEventLocked(e)
		if err != nil {
			return err
		}
		if inboxContextOnly(e) && !link.ContextOnly {
			link.ContextOnly = true
			return s.save(s.eventPath(e.MemberIdentity, e.Seq), link, followupBytes)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Reclaim serializes with the Inbox writer and previews. Store.mu then excludes
// checkpoint/turn writes and all other readers for the entire commit and sweep.
func (a *AgentInbox) Reclaim(ctx actorx.Context, msg ReclaimInbox, reply actorx.Reply[InboxStatus]) error {
	if msg.MemberIdentity != a.id {
		return ErrIdentity
	}
	if a.writeErr != nil {
		return a.writeErr
	}
	if a.committing || a.previewing {
		return ErrBusy
	}
	if msg.Through > a.latest {
		return ErrRequest
	}
	a.committing, a.previewing = true, true
	_, err := actorx.StartTask(ctx.WithContext(context.WithoutCancel(ctx)), func(ctx context.Context) (int64, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return a.store.reclaimInbox(a, msg.Through)
	}, func(remaining int64, err error) bool {
		a.committing, a.previewing = false, false
		if err == nil {
			a.expiredThrough = max(a.expiredThrough, msg.Through)
			for delivery, index := range a.receipts {
				if index.seq <= a.expiredThrough {
					delete(a.receipts, delivery)
				}
			}
			// Recompute retained bytes under the same bounded entry encoding used
			// for admission. The Store returns this through the committed sweep.
			a.bytes = remaining
		}
		if errors.Is(err, ErrCommitUnknown) {
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

func (s *Store) reclaimInbox(a *AgentInbox, through uint64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, _, err := s.memberPath(a.id, "agent.json", true)
	if err != nil {
		return 0, err
	}
	if a.consumption.Batch != nil && a.consumption.inputThrough(a.expiredThrough) < a.consumption.Batch.SnapshotSeq {
		return 0, ErrBusy // Issued batches must settle before any physical cleanup.
	}
	var agent AgentSnapshot
	err = readSnapshot(path, &agent, maxPrivateFileBytes)
	if err == nil {
		if agent.MemberIdentity != a.id || agent.validate() != nil {
			return 0, ErrInvalid
		}
		if agent.Turn != nil && agent.Turn.Status == "running" {
			return 0, ErrBusy
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	remaining := a.bytes
	for seq := a.expiredThrough + 1; seq <= through; seq++ {
		e, err := s.inboxEntryLocked(a.id, seq)
		if err != nil {
			return 0, err
		}
		if err := s.prepareInboxReclaimLocked(e, a.consumption); err != nil {
			return 0, err
		}
		size, err := inboxEntrySize(e)
		if err != nil {
			return 0, err
		}
		remaining -= size
	}
	if through > a.expiredThrough {
		// Keep a member-level checkpoint even when no input was consumed. After
		// all bodies are removed it also prevents a missing header from looking
		// like an interrupted first initialization and resetting the sequence.
		cursorPath := filepath.Join(a.dir, "consumption.json")
		if _, err := os.Stat(cursorPath); errors.Is(err, os.ErrNotExist) {
			if err := s.save(cursorPath, a.consumption, maxPrivateFileBytes); err != nil {
				return 0, err
			}
		} else if err != nil {
			return 0, err
		}
		header := inboxHeader{Version: 1, MemberIdentity: a.id, Limits: a.limits, LatestSeq: a.latest, ExpiredThrough: through}
		if err := s.save(filepath.Join(a.dir, "inbox.json"), header, maxPrivateFileBytes); err != nil {
			return 0, err // In particular, never delete after an uncertain rename/sync.
		}
	}
	if err := s.removeExpiredInboxFilesLocked(a.dir, max(through, a.expiredThrough)); err != nil {
		// The boundary is committed but the actor's accounting has not advanced.
		// Reopen before any more writes, then resume the idempotent sweep.
		s.uncertain = true
		return 0, fmt.Errorf("%w: reclaim inbox files: %w", ErrCommitUnknown, err)
	}
	return remaining, nil
}

func (s *Store) removeExpiredInboxFilesLocked(dir string, through uint64) error {
	if through == 0 {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		files, err := f.ReadDir(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, file := range files {
			var seq uint64
			name := file.Name()
			if _, e := fmt.Sscanf(name, "%020d.json", &seq); e == nil && seq > 0 && name == inboxEntryName(seq) && seq <= through {
				if err := s.ops.remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	return s.ops.syncDir(dir)
}
