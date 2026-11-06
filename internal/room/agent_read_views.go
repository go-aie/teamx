package room

import (
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// A3 projections are deliberately limited to the storage headers of these two
// reads. In particular, action access evidence, native identities, authors and
// business versions remain intact. Human reads still use the storage types.
type agentFollowupUnit struct {
	Ref          string                `json:"unit_ref"`
	ObjectRef    string                `json:"object_ref"`
	TargetRef    string                `json:"target_ref"`
	Revision     uint64                `json:"revision"`
	Status       string                `json:"status"`
	Summary      string                `json:"summary,omitempty"`
	Next         string                `json:"next,omitempty"`
	Evidence     string                `json:"evidence,omitempty"`
	TurnNumber   uint64                `json:"turn_number"`
	UpdatedAt    time.Time             `json:"updated_at"`
	Questions    []followupQuestion    `json:"questions,omitempty"`
	Instructions []followupInstruction `json:"instructions,omitempty"`
	Actions      []referenceAction     `json:"actions,omitempty"`
}

type agentStoredTarget struct {
	Ref       string                  `json:"ref"`
	ObjectRef string                  `json:"object_ref"`
	Target    platform.TargetIdentity `json:"target"`
}

type followupReadResult struct {
	Unit    followupUnit          `json:"unit"`
	Target  storedTarget          `json:"target"`
	Actions []platform.ActionSpec `json:"registered_actions"`
}

type agentFollowupRead struct {
	MemberIdentity
	Unit    agentFollowupUnit     `json:"unit"`
	Target  agentStoredTarget     `json:"target"`
	Actions []platform.ActionSpec `json:"registered_actions"`
}

func projectAgentFollowupRead(id MemberIdentity, result followupReadResult) (agentFollowupRead, error) {
	u, t := result.Unit, result.Target
	if u.MemberIdentity != id || t.MemberIdentity != id {
		return agentFollowupRead{}, ErrIdentity
	}
	if u.Version != followupSchema || t.Version != followupSchema {
		return agentFollowupRead{}, ErrVersion
	}
	return agentFollowupRead{
		MemberIdentity: id,
		Unit: agentFollowupUnit{u.Ref, u.ObjectRef, u.TargetRef, u.Revision, u.Status,
			u.Summary, u.Next, u.Evidence, u.TurnNumber, u.UpdatedAt, u.Questions, u.Instructions, u.Actions},
		Target: agentStoredTarget{t.Ref, t.ObjectRef, t.Target}, Actions: result.Actions,
	}, nil
}

type agentInboxEntry struct {
	Seq        uint64            `json:"seq"`
	Item       notification.Item `json:"item"`
	ReceivedAt time.Time         `json:"received_at"`
}

// Receipt crosses internal HTTP only. The CLI clears it on a copy for display,
// keeping the original for confirmation and uncertain-consumption diagnostics.
type agentInboxBatch struct {
	ID          string `json:"id"`
	TurnNumber  uint64 `json:"turn_number"`
	Start       uint64 `json:"start"`
	SnapshotSeq uint64 `json:"snapshot_seq"`
	PageThrough uint64 `json:"page_through"`
	Receipt     string `json:"receipt,omitempty"`
}

type agentReferenceInboxPage struct {
	MemberIdentity
	SnapshotSeq uint64                  `json:"snapshot_seq"`
	NextAfter   uint64                  `json:"next_after"`
	HasMore     bool                    `json:"has_more"`
	Entries     []agentInboxEntry       `json:"entries"`
	Batch       agentInboxBatch         `json:"batch"`
	Sources     []followupSourceSummary `json:"sources"`
	Followups   []followupSummary       `json:"followups"`
}

func projectAgentInboxRead(id MemberIdentity, page referenceInboxPage) (agentReferenceInboxPage, error) {
	if page.MemberIdentity != id {
		return agentReferenceInboxPage{}, ErrIdentity
	}
	var entries []agentInboxEntry
	if page.Entries != nil {
		entries = make([]agentInboxEntry, 0, len(page.Entries))
	}
	for _, e := range page.Entries {
		if e.MemberIdentity != id {
			return agentReferenceInboxPage{}, ErrIdentity
		}
		if e.Version != formatVersion {
			return agentReferenceInboxPage{}, ErrVersion
		}
		entries = append(entries, agentInboxEntry{e.Seq, e.Item, e.ReceivedAt})
	}
	b := page.Batch
	return agentReferenceInboxPage{id, page.SnapshotSeq, page.NextAfter, page.HasMore, entries,
		agentInboxBatch{b.ID, b.TurnNumber, b.Start, b.SnapshotSeq, b.PageThrough, b.Receipt}, page.Sources, page.Followups}, nil
}
