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
	NotificationSource *notificationSourceLocation `json:"notification_source,omitempty"`
	Seq                uint64                      `json:"seq"`
	Item               notification.View           `json:"item"`
	ReceivedAt         time.Time                   `json:"received_at"`
	Source             followupSourceSummary       `json:"source"`
	Origin             *inboxMessageOrigin         `json:"origin,omitempty"`
	AutomaticAdmission *notification.Admission     `json:"automatic_admission,omitempty"`
}

func projectInboxEntry(e InboxEntry, source followupSourceSummary) inboxEntryView {
	return inboxEntryView{Seq: e.Seq, Item: projectNotification(e.Item), ReceivedAt: e.ReceivedAt, Source: source}
}

func inboxContextOnly(e InboxEntry) bool {
	return projectInboxEntry(e, followupSourceSummary{}).Item.ContextOnly
}

type inboxPreviewPage struct {
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
	v := inboxPreviewPage{MemberIdentity: page.MemberIdentity, ExpiredThrough: page.ExpiredThrough, SnapshotSeq: page.SnapshotSeq,
		NextAfter: page.NextAfter, HasMore: page.HasMore, Entries: []inboxEntryView{}, Status: status}
	v.Status.Batch = nil
	origins, err := s.inboxMessageOriginsLocked(page.Entries)
	if err != nil {
		return v, err
	}
	for _, e := range page.Entries {
		source := followupSourceSummary{Seq: e.Seq, SourceRef: referenceDigest("src_", e.MemberIdentity, e.Seq, e.Item.DeliveryID), Status: "not_checked"}
		if _, err := os.Stat(s.eventPath(e.MemberIdentity, e.Seq)); err == nil {
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
		if origin, ok := origins[e.Seq]; ok {
			entry.Origin = &origin
		}
		v.Entries = append(v.Entries, entry)
	}
	return v, nil
}
