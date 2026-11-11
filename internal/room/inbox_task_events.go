package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// Records, delivery receipts and consumption associations are separate facts.
// These small local references outlive Inbox bodies, including their dedup keys.
type inboxTaskRecord struct {
	Version int `json:"version"`
	MemberIdentity
	Event InboxTaskEvent `json:"event"`
}

type inboxTaskDelivery struct {
	Version int `json:"version"`
	InboxReceipt
	EventID string   `json:"event_id"`
	Digest  [32]byte `json:"digest"`
}

type inboxTaskLink struct {
	Version int `json:"version"`
	MemberIdentity
	Seq      uint64         `json:"seq"`
	Event    InboxTaskEvent `json:"event"`
	LinkedAt time.Time      `json:"linked_at"`
}

const inboxTaskRetention = 7 * 24 * time.Hour

func (s *Store) taskEventPath(id MemberIdentity, group, key string) string {
	return filepath.Join(s.dir, "agents", id.MembershipID, "task-events", group, key+".json")
}

func taskEventKey(eventID string) string { return referenceDigest("", eventID) }
func taskLinkKey(seq uint64) string      { return fmt.Sprintf("%020d", seq) }

func (s *Store) loadTaskEventLocked(id MemberIdentity, event InboxTaskEvent) error {
	var record inboxTaskRecord
	if err := readSnapshot(s.taskEventPath(id, "events", taskEventKey(event.EventID)), &record, maxPrivateFileBytes); err != nil {
		return err
	}
	if record.Version != 1 || record.MemberIdentity != id || !record.Event.valid() || !reflect.DeepEqual(record.Event, event) {
		return ErrInvalid
	}
	return nil
}

// Service-internal producer boundary, used by connection result subscriptions.
// There is no public CLI or HTTP endpoint for publishing arbitrary events.
// Persist the immutable result reference before handing it to the Inbox actor.
func (s *service) receiveTaskEvent(ctx context.Context, id MemberIdentity, event InboxTaskEvent) (InboxReceipt, error) {
	s.inboxMu.Lock()
	defer s.inboxMu.Unlock()
	addr, err := s.inboxLocked(ctx, id)
	if err != nil {
		return InboxReceipt{}, err
	}
	event.CreatedAt = event.CreatedAt.UTC()
	if !event.valid() {
		return InboxReceipt{}, ErrRequest
	}
	s.store.mu.Lock()
	err = s.store.check(true)
	if err == nil {
		_, _, err = s.store.memberPath(id, "agent.json", true)
	}
	if err == nil {
		err = s.store.loadTaskEventLocked(id, event)
		if errors.Is(err, os.ErrNotExist) {
			record := inboxTaskRecord{Version: 1, MemberIdentity: id, Event: event}
			err = s.store.save(s.store.taskEventPath(id, "events", taskEventKey(event.EventID)), record, maxPrivateFileBytes)
		} else if errors.Is(err, ErrInvalid) {
			err = ErrConflict
		}
	}
	s.store.mu.Unlock()
	if err != nil {
		return InboxReceipt{}, err
	}
	return addr.Call[InboxReceipt](ctx, ReceiveInbox{MemberIdentity: id, Type: InboxTaskEventType, TaskEvent: &event})
}

func (s *Store) taskDeliveryLocked(e InboxEntry) (inboxTaskDelivery, error) {
	var d inboxTaskDelivery
	if err := s.loadTaskEventLocked(e.MemberIdentity, *e.TaskEvent); err != nil {
		return d, err
	}
	err := readSnapshot(s.taskEventPath(e.MemberIdentity, "deliveries", taskEventKey(e.TaskEvent.EventID)), &d, maxPrivateFileBytes)
	if err != nil {
		return d, err
	}
	m := e.metadata()
	if d.Version != 1 || d.MemberIdentity != e.MemberIdentity || d.Seq == 0 ||
		d.EventID != e.TaskEvent.EventID || d.DeliveryID != m.deliveryID || d.Digest != m.digest {
		return d, ErrInvalid
	}
	return d, nil
}

// The Inbox body commits first. Recovery repairs an interrupted second write
// using that exact immutable body, then checks the usual sequence checkpoint.
func (s *Store) saveTaskDeliveryLocked(e InboxEntry) error {
	d, err := s.taskDeliveryLocked(e)
	if err == nil {
		if d.Seq != e.Seq {
			return ErrInvalid
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.loadTaskEventLocked(e.MemberIdentity, *e.TaskEvent); err != nil {
		return err
	}
	m := e.metadata()
	d = inboxTaskDelivery{Version: 1, InboxReceipt: InboxReceipt{MemberIdentity: e.MemberIdentity, Seq: e.Seq, DeliveryID: m.deliveryID}, EventID: e.TaskEvent.EventID, Digest: m.digest}
	return s.save(s.taskEventPath(e.MemberIdentity, "deliveries", taskEventKey(e.TaskEvent.EventID)), d, maxPrivateFileBytes)
}

func (s *Store) checkTaskEventLinkLocked(e InboxEntry) error {
	if err := s.loadTaskEventLocked(e.MemberIdentity, *e.TaskEvent); err != nil {
		return err
	}
	var link inboxTaskLink
	err := readSnapshot(s.taskEventPath(e.MemberIdentity, "links", taskLinkKey(e.Seq)), &link, maxPrivateFileBytes)
	if err != nil {
		return err
	}
	if link.Version != 1 || link.MemberIdentity != e.MemberIdentity || link.Seq != e.Seq ||
		link.LinkedAt.IsZero() || !reflect.DeepEqual(link.Event, *e.TaskEvent) {
		return ErrInvalid
	}
	return nil
}

func (s *Store) linkTaskEvent(id MemberIdentity, e InboxEntry, turn uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, err := s.runningTurnLocked(id, turn)
	if err != nil {
		return err
	}
	if e.MemberIdentity != id || e.Seq > agent.Turn.InboxSnapshotSeq || !e.validPayload() || e.payloadType() != InboxTaskEventType {
		return ErrIdentity
	}
	if err := s.loadTaskEventLocked(id, *e.TaskEvent); err != nil {
		return err
	}
	if err := s.checkTaskEventLinkLocked(e); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	link := inboxTaskLink{Version: 1, MemberIdentity: id, Seq: e.Seq, Event: *e.TaskEvent, LinkedAt: time.Now().UTC()}
	return s.save(s.taskEventPath(id, "links", taskLinkKey(e.Seq)), link, maxPrivateFileBytes)
}

// Validate associations independently after body reclamation. A missing cursor,
// event, delivery receipt or association must never silently hide consumed work.
func (s *Store) validateTaskEventsLocked(id MemberIdentity, expired, latest, consumed uint64, receipts map[string]inboxIndex) error {
	for _, group := range []string{"events", "deliveries", "links"} {
		files, err := os.ReadDir(filepath.Join(s.dir, "agents", id.MembershipID, "task-events", group))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, file := range files {
			if strings.HasPrefix(file.Name(), ".snapshot-") {
				continue
			}
			path := filepath.Join(s.dir, "agents", id.MembershipID, "task-events", group, file.Name())
			switch group {
			case "events":
				var r inboxTaskRecord
				if err := readSnapshot(path, &r, maxPrivateFileBytes); err != nil {
					return err
				}
				if r.Version != 1 || r.MemberIdentity != id || !r.Event.valid() || file.Name() != taskEventKey(r.Event.EventID)+".json" {
					return ErrInvalid
				}
			case "deliveries":
				var d inboxTaskDelivery
				if err := readSnapshot(path, &d, maxPrivateFileBytes); err != nil {
					return err
				}
				var r inboxTaskRecord
				if err := readSnapshot(s.taskEventPath(id, "events", taskEventKey(d.EventID)), &r, maxPrivateFileBytes); err != nil {
					return err
				}
				e := InboxEntry{MemberIdentity: id, Seq: d.Seq, Type: InboxTaskEventType, TaskEvent: &r.Event}
				if _, err := s.taskDeliveryLocked(e); err != nil {
					return err
				}
				if file.Name() != taskEventKey(d.EventID)+".json" || d.Seq > latest {
					return ErrInvalid
				}
				receipts[d.DeliveryID] = e.index()
				if d.Seq <= expired {
					if d.Seq > consumed {
						return ErrInvalid
					}
					if err := s.checkTaskEventLinkLocked(e); err != nil {
						return err
					}
				} else {
					body, err := s.inboxEntryLocked(id, d.Seq)
					if err != nil {
						return err
					}
					if body.payloadType() != InboxTaskEventType || body.metadata().digest != e.metadata().digest {
						return ErrInvalid
					}
				}
			case "links":
				var link inboxTaskLink
				if err := readSnapshot(path, &link, maxPrivateFileBytes); err != nil {
					return err
				}
				e := InboxEntry{MemberIdentity: id, Seq: link.Seq, Type: InboxTaskEventType, TaskEvent: &link.Event}
				if file.Name() != taskLinkKey(link.Seq)+".json" || link.Seq > latest {
					return ErrInvalid
				}
				if err := s.checkTaskEventLinkLocked(e); err != nil {
					return err
				}
				d, err := s.taskDeliveryLocked(e)
				if err != nil {
					return err
				}
				if d.Seq != link.Seq {
					return ErrInvalid
				}
			}
		}
	}
	return nil
}
