package room

import (
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
)

// This is a closed envelope. Empty type is the original platform encoding.
type InboxEntryType string

const (
	InboxPlatformNotification InboxEntryType = "platform_notification"
	InboxTaskEventType        InboxEntryType = "task_event"
)

// InboxTaskEvent contains local references only. It is never a platform source
// or authority to replay an operation. Consumers read the latest result/intent.
type InboxTaskEvent struct {
	EventID       string    `json:"event_id"`
	OperationID   string    `json:"operation_id"`
	ResultVersion uint64    `json:"result_version"`
	SourceSeq     uint64    `json:"source_seq"`
	EventType     string    `json:"event_type"`
	ResultRef     string    `json:"result_ref"`
	CreatedAt     time.Time `json:"created_at"`
}

func (e InboxTaskEvent) valid() bool {
	for _, value := range []string{e.EventID, e.OperationID, e.ResultRef} {
		if strings.ContainsRune(value, 0) {
			return false
		}
	}
	if !handlingText(e.EventID) || len(e.EventID) > 256 || !handlingText(e.OperationID) ||
		!handlingText(e.ResultRef) || e.ResultVersion == 0 || e.SourceSeq == 0 || e.CreatedAt.IsZero() {
		return false
	}
	switch e.EventType {
	case "operation_completed", "operation_failed", "user_selection":
		return true
	}
	return false
}

func (e InboxEntry) payloadType() InboxEntryType {
	if e.Type == "" {
		return InboxPlatformNotification
	}
	return e.Type
}

func (e InboxEntry) validPayload() bool {
	switch e.payloadType() {
	case InboxPlatformNotification:
		return e.TaskEvent == nil && validInboxItem(e.Item)
	case InboxTaskEventType:
		return reflect.ValueOf(e.Item).IsZero() && e.TaskEvent != nil && e.TaskEvent.valid()
	}
	return false
}

type inboxMetadata struct {
	deliveryID string
	createdAt  time.Time
	digest     [32]byte
}

// Call after payload validation. Platform hashes retain their original format.
func (e InboxEntry) metadata() inboxMetadata {
	switch e.payloadType() {
	case InboxPlatformNotification:
		return inboxMetadata{e.Item.DeliveryID, e.Item.CreatedAt, inboxDigest(e.Item)}
	case InboxTaskEventType:
		event := *e.TaskEvent
		event.CreatedAt = event.CreatedAt.UTC()
		b, _ := json.Marshal(event)
		return inboxMetadata{"task_" + referenceDigest("", e.MemberIdentity, event.EventID), event.CreatedAt, sha256.Sum256(b)}
	}
	return inboxMetadata{}
}

func (e InboxEntry) index() inboxIndex {
	m := e.metadata()
	return inboxIndex{seq: e.Seq, digest: m.digest, createdAt: m.createdAt, kind: e.payloadType()}
}

func (s *Store) checkInboxBusinessLinkLocked(e InboxEntry) error {
	if !e.validPayload() {
		return ErrInvalid
	}
	switch e.payloadType() {
	case InboxTaskEventType:
		return s.checkTaskEventLinkLocked(e)
	case InboxPlatformNotification:
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
	return ErrInvalid
}
