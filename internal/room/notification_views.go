package room

import (
	"errors"
	"os"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Both endpoints share this entry. Preview never resolves or consumes sources.
type inboxEntryView struct {
	Seq        uint64                `json:"seq"`
	Item       notification.View     `json:"item"`
	ReceivedAt time.Time             `json:"received_at"`
	Source     followupSourceSummary `json:"source"`
}

func projectInboxEntry(e InboxEntry, source followupSourceSummary) inboxEntryView {
	item := e.Item
	if item.Content == nil && item.Source.Provider == "lark" {
		item.Content = lark.SavedContent(item)
	}
	return inboxEntryView{e.Seq, notification.Project(item), e.ReceivedAt, source}
}

type inboxPreviewPage struct {
	MemberIdentity
	SnapshotSeq uint64           `json:"snapshot_seq"`
	NextAfter   uint64           `json:"next_after"`
	HasMore     bool             `json:"has_more"`
	Entries     []inboxEntryView `json:"entries"`
	Status      InboxStatus      `json:"status"`
}

func (s *Store) projectInboxPreview(page InboxPage, status InboxStatus) (inboxPreviewPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := inboxPreviewPage{MemberIdentity: page.MemberIdentity, SnapshotSeq: page.SnapshotSeq,
		NextAfter: page.NextAfter, HasMore: page.HasMore, Entries: []inboxEntryView{}, Status: status}
	v.Status.Batch = nil
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
		v.Entries = append(v.Entries, projectInboxEntry(e, source))
	}
	return v, nil
}
