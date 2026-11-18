package room

import (
	"errors"
	"os"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
)

// Source location is exposed only to the human preview, never Agent input.
type notificationSourceLocation struct {
	Provider       string `json:"provider"`
	Channel        string `json:"channel"`
	SubscriptionID string `json:"subscription_id"`
	Instance       string `json:"instance"`
}

// Both endpoints share this entry. Preview never resolves or consumes sources.
type inboxEntryView struct {
	Partition          string                      `json:"partition,omitempty"`
	Type               InboxEntryType              `json:"type"`
	TaskEvent          *InboxTaskEvent             `json:"task_event,omitempty"`
	NotificationSource *notificationSourceLocation `json:"notification_source,omitempty"`
	Seq                uint64                      `json:"seq"`
	Item               notification.View           `json:"item,omitzero"`
	ReceivedAt         time.Time                   `json:"received_at"`
	Source             followupSourceSummary       `json:"source,omitzero"`
	Origin             *inboxMessageOrigin         `json:"origin,omitempty"`
	AutomaticAdmission *notification.Admission     `json:"automatic_admission,omitempty"`
}

// Stream reads retain the existing native/business projection and omit the
// physical Inbox position. Formal stream readers share this business DTO.
type inputStreamNotificationEntry struct {
	Type               string                  `json:"type"`
	Item               notification.View       `json:"item"`
	Source             inputStreamSource       `json:"source,omitzero"`
	Origin             *inboxMessageOrigin     `json:"origin,omitempty"`
	AutomaticAdmission *notification.Admission `json:"automatic_admission,omitempty"`
}

func projectStreamNotification(v inboxEntryView) inputStreamNotificationEntry {
	s := v.Source
	return inputStreamNotificationEntry{Type: "notification", Item: v.Item, Source: inputStreamSource{s.SourceRef, s.SourceVersion, s.ObjectRef, s.TargetRef, s.EvidenceSource, string(s.Status), s.ReasonCode, s.Reason}, Origin: v.Origin, AutomaticAdmission: v.AutomaticAdmission}
}

type inputStreamSource struct {
	Ref            string `json:"source_ref"`
	Version        uint64 `json:"source_version,omitempty"`
	ObjectRef      string `json:"object_ref,omitempty"`
	TargetRef      string `json:"target_ref,omitempty"`
	EvidenceSource string `json:"evidence_source,omitempty"`
	Status         string `json:"status"`
	ReasonCode     string `json:"reason_code,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

func projectInboxEntry(e InboxEntry, source followupSourceSummary) inboxEntryView {
	v := inboxEntryView{Type: e.payloadType(), Seq: e.Seq, ReceivedAt: e.ReceivedAt, Partition: inboxEntryPosition(e).Partition}
	switch e.payloadType() {
	case InboxPlatformNotification:
		v.Item, v.Source = projectNotification(e.Item), source
	case InboxTaskEventType:
		v.TaskEvent = e.TaskEvent
	}
	return v
}

func inboxContextOnly(e InboxEntry) bool {
	return projectInboxEntry(e, followupSourceSummary{}).Item.ContextOnly
}

type inboxPreviewPage struct {
	History *inputHistoryNavigation `json:"history,omitempty"`
	MemberIdentity
	ExpiredThrough uint64           `json:"expired_through"`
	SnapshotSeq    uint64           `json:"snapshot_seq"`
	NextAfter      uint64           `json:"next_after"`
	HasMore        bool             `json:"has_more"`
	Entries        []inboxEntryView `json:"entries"`
	Status         InboxStatus      `json:"status"`
}

func (s *Store) projectInboxPreview(page InboxPage, status InboxStatus) (inboxPreviewPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projectInboxPreviewLocked(page, status)
}

func (s *Store) projectInboxPreviewLocked(page InboxPage, status InboxStatus) (inboxPreviewPage, error) {
	v := inboxPreviewPage{MemberIdentity: page.MemberIdentity, ExpiredThrough: page.ExpiredThrough, SnapshotSeq: page.SnapshotSeq,
		NextAfter: page.NextAfter, HasMore: page.HasMore, Entries: []inboxEntryView{}, Status: status}
	v.Status.Batch = nil
	origins, err := s.inboxMessageOriginsLocked(page.Entries)
	if err != nil {
		return v, err
	}
	for _, e := range page.Entries {
		if err := e.validate(page.MemberIdentity); err != nil {
			return v, err
		}
		if e.payloadType() == InboxTaskEventType {
			if err := s.loadTaskEventLocked(e.MemberIdentity, *e.TaskEvent); err != nil {
				return v, err
			}
			v.Entries = append(v.Entries, projectInboxEntry(e, followupSourceSummary{}))
			continue
		}
		source := followupSourceSummary{Seq: e.Seq, SourceRef: inputSourceRef(e), Status: "not_checked"}
		if _, err := os.Stat(s.inputEventPath(e)); err == nil {
			link, err := s.loadFollowupEventLocked(e)
			if err != nil {
				return v, err
			}
			source = sourceSummary(link)
		} else if !errors.Is(err, os.ErrNotExist) {
			return v, err
		}
		entry := projectInboxEntry(e, source)
		original := e.Item.Source
		entry.NotificationSource = &notificationSourceLocation{Provider: original.Provider, Channel: original.Channel, SubscriptionID: original.SubscriptionID, Instance: original.Instance}
		admission, err := s.inboxAdmissionLocked(e)
		if err != nil {
			return v, err
		}
		if !admission.Verified || admission.Decision == notification.AdmissionExcluded {
			entry.AutomaticAdmission = &admission
		}
		if origin, ok := origins[inboxEntryPosition(e)]; ok {
			entry.Origin = &origin
		}
		v.Entries = append(v.Entries, entry)
	}
	return v, nil
}
