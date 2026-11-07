package room

import (
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Explicit Agent business projections. Human reads retain storage contracts.
type agentFollowupUnit struct {
	Ref          string                `json:"unit_ref"`
	ObjectRef    string                `json:"object_ref"`
	TargetRef    string                `json:"target_ref"`
	Revision     uint64                `json:"unit_version"`
	Status       string                `json:"status"`
	Summary      string                `json:"summary,omitempty"`
	Next         string                `json:"next,omitempty"`
	Evidence     string                `json:"evidence,omitempty"`
	TurnNumber   uint64                `json:"turn_number"`
	UpdatedAt    time.Time             `json:"updated_at"`
	Questions    []followupQuestion    `json:"questions,omitempty"`
	Instructions []followupInstruction `json:"instructions,omitempty"`
	Actions      []agentActionRecord   `json:"actions"`
}

type agentStoredTarget struct {
	Ref         string                    `json:"target_ref"`
	ObjectRef   string                    `json:"object_ref"`
	Target      platform.TargetIdentity   `json:"target"`
	Aliases     []platform.TargetIdentity `json:"object_aliases,omitempty"`
	ObjectLabel string                    `json:"object_label,omitempty"`
	ObjectURL   string                    `json:"object_url,omitempty"`
}

type followupReadResult struct {
	Unit    followupUnit          `json:"unit"`
	Target  storedTarget          `json:"target"`
	Actions []platform.ActionSpec `json:"registered_actions"`
}

type agentFollowupRead struct {
	MemberIdentity
	Unit       agentFollowupUnit     `json:"unit"`
	Target     agentStoredTarget     `json:"target"`
	Actions    []platform.ActionSpec `json:"registered_actions"`
	Complete   bool                  `json:"complete"`
	Detail     *agentFollowupDetails `json:"detail,omitempty"`
	Details    agentDetailLocator    `json:"details"`
	SourcePage *followupSourcePage   `json:"source_page,omitempty"`
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
			u.Summary, u.Next, u.Evidence, u.TurnNumber, u.UpdatedAt, u.Questions, u.Instructions, projectActionRecords(u.Actions)},
		Target: agentStoredTarget{Ref: t.Ref, ObjectRef: t.ObjectRef, Target: t.Target}, Actions: result.Actions, Complete: true,
		Details: agentDetailLocator{ObjectRef: u.ObjectRef, UnitRef: u.Ref, TurnNumber: u.TurnNumber, Omitted: []string{"action execution snapshots"}},
	}, nil
}

type agentInboxEntry = inboxEntryView

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
	SnapshotSeq uint64                 `json:"snapshot_seq"`
	NextAfter   uint64                 `json:"next_after"`
	HasMore     bool                   `json:"has_more"`
	Entries     []agentInboxEntry      `json:"entries"`
	Batch       agentInboxBatch        `json:"batch"`
	Followups   []agentFollowupSummary `json:"followups"`
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
		var source *followupSourceSummary
		for j := range page.Sources {
			if page.Sources[j].Seq == e.Seq {
				if source != nil {
					return agentReferenceInboxPage{}, ErrInvalid
				}
				source = &page.Sources[j]
			}
		}
		if source == nil {
			return agentReferenceInboxPage{}, ErrInvalid
		}
		entries = append(entries, projectInboxEntry(e, *source))
	}
	followups := make([]agentFollowupSummary, 0, len(page.Followups))
	for _, v := range page.Followups {
		followups = append(followups, projectFollowupSummary(v))
	}
	b := page.Batch
	return agentReferenceInboxPage{id, page.SnapshotSeq, page.NextAfter, page.HasMore, entries,
		agentInboxBatch{b.ID, b.TurnNumber, b.Start, b.SnapshotSeq, b.PageThrough, b.Receipt}, followups}, nil
}

// Every action remains visible, including completed work and exact body. Only
// repeated admission/inspection/spec/rule snapshots require --detail.
type agentActionRecord struct {
	ID                string                       `json:"id"`
	Authority         agentActionAuthority         `json:"authority"`
	Retry             platform.RetryPolicy         `json:"retry"`
	Basis             *followupReplyBasis          `json:"basis,omitempty"`
	TargetRef         string                       `json:"target_ref"`
	Target            platform.TargetIdentity      `json:"target"`
	Account           platform.AccountIdentity     `json:"account"`
	Operation         platform.Operation           `json:"operation"`
	Input             ActionInput                  `json:"input"`
	Status            string                       `json:"status"`
	Attempts          int                          `json:"attempts"`
	PossibleDuplicate bool                         `json:"possible_duplicate,omitempty"`
	Result            *platform.TargetActionResult `json:"result,omitempty"`
	UpdatedAt         time.Time                    `json:"updated_at"`
}

func projectActionRecords(actions []referenceAction) []agentActionRecord {
	records := make([]agentActionRecord, 0, len(actions))
	for _, a := range actions {
		v := agentActionRecord{ID: a.Request.ID, Authority: projectActionAuthority(a.Request.Authority), Retry: a.Spec.Retry, TargetRef: a.Request.TargetRef, Target: a.Target, Account: a.Access.Account, Operation: a.Request.Operation, Input: a.Request.Input, Status: a.Status, Attempts: a.Attempts, PossibleDuplicate: a.PossibleDuplicate, Result: a.Result, UpdatedAt: a.UpdatedAt}
		if a.Status != "succeeded" {
			v.Basis = a.Basis
		}
		records = append(records, v)
	}
	return records
}

type agentDetailLocator struct {
	ObjectRef  string   `json:"object_ref"`
	UnitRef    string   `json:"unit_ref"`
	TurnNumber uint64   `json:"turn_number"`
	Omitted    []string `json:"omitted"`
}
type agentActionDetail struct {
	ID              string                          `json:"id"`
	Authority       agentActionAuthority            `json:"authority"`
	Inspection      platform.TargetActionInspection `json:"inspection"`
	Spec            platform.ActionSpec             `json:"spec"`
	Rule            *PolicyRevision                 `json:"rule,omitempty"`
	Basis           *followupReplyBasis             `json:"basis,omitempty"`
	ObservedVersion uint64                          `json:"observed_unit_version"`
	TurnNumber      uint64                          `json:"turn_number"`
}
type agentFollowupDetails struct {
	Actions []agentActionDetail `json:"actions"`
}

func projectFollowupDetails(u followupUnit) *agentFollowupDetails {
	v := &agentFollowupDetails{Actions: []agentActionDetail{}}
	for _, a := range u.Actions {
		v.Actions = append(v.Actions, agentActionDetail{a.Request.ID, projectActionAuthority(a.Request.Authority), a.Inspection, a.Spec, a.Rule, a.Basis, a.Request.Revision, a.Request.TurnNumber})
	}
	return v
}

// Receipts describe only committed business changes. A replay against an old
// observed version explicitly requires a read before using the returned version.
type agentFollowupChanged struct {
	Status       *string               `json:"status,omitempty"`
	Summary      *string               `json:"summary,omitempty"`
	Next         *string               `json:"next,omitempty"`
	Evidence     *string               `json:"evidence,omitempty"`
	Questions    []followupQuestion    `json:"questions"`
	Instructions []followupInstruction `json:"instructions"`
	Actions      []agentActionRecord   `json:"actions"`
}
type agentFollowupReceipt struct {
	ObjectRef    string               `json:"object_ref"`
	UnitRef      string               `json:"unit_ref"`
	UnitVersion  uint64               `json:"unit_version"`
	Status       string               `json:"status"`
	UpdatedAt    time.Time            `json:"updated_at"`
	TurnNumber   uint64               `json:"turn_number"`
	Changed      agentFollowupChanged `json:"changed"`
	Reused       bool                 `json:"reused"`
	RequiresRead bool                 `json:"requires_read"`
	Question     *followupQuestion    `json:"question,omitempty"`
	Instruction  *followupInstruction `json:"instruction,omitempty"`
	Publication  string               `json:"publication,omitempty"`
	SendReceipt  *PostReceipt         `json:"send_receipt,omitempty"`
	Failure      string               `json:"failure,omitempty"`
}

func projectFollowupChange(before, after followupUnit, expected uint64) agentFollowupReceipt {
	c := agentFollowupChanged{Questions: []followupQuestion{}, Instructions: []followupInstruction{}, Actions: []agentActionRecord{}}
	if before.Status != after.Status {
		c.Status = &after.Status
	}
	if before.Summary != after.Summary {
		c.Summary = &after.Summary
	}
	if before.Next != after.Next {
		c.Next = &after.Next
	}
	if before.Evidence != after.Evidence {
		c.Evidence = &after.Evidence
	}
	for _, q := range after.Questions {
		found := false
		for _, old := range before.Questions {
			if reflect.DeepEqual(q, old) {
				found = true
				break
			}
		}
		if !found {
			c.Questions = append(c.Questions, q)
		}
	}
	for _, i := range after.Instructions {
		found := false
		for _, old := range before.Instructions {
			if reflect.DeepEqual(i, old) {
				found = true
				break
			}
		}
		if !found {
			c.Instructions = append(c.Instructions, i)
		}
	}
	for _, a := range after.Actions {
		found := false
		for _, old := range before.Actions {
			if reflect.DeepEqual(a, old) {
				found = true
				break
			}
		}
		if !found {
			c.Actions = append(c.Actions, projectActionRecords([]referenceAction{a})[0])
		}
	}
	return agentFollowupReceipt{ObjectRef: after.ObjectRef, UnitRef: after.Ref, UnitVersion: after.Revision, Status: after.Status, UpdatedAt: after.UpdatedAt, TurnNumber: after.TurnNumber, Changed: c, Reused: before.Revision == after.Revision, RequiresRead: before.Revision != expected}
}

// Sources retain all native resolution/mapping data; internal access admission
// snapshots (connection/generation/credential versions) are never Agent data.
type agentSourceView struct {
	SourceRef     string                     `json:"source_ref"`
	SourceVersion uint64                     `json:"source_version"`
	Seq           uint64                     `json:"seq"`
	DeliveryID    string                     `json:"delivery_id"`
	Source        platform.SourceRef         `json:"source"`
	ObjectRef     string                     `json:"object_ref,omitempty"`
	TargetRef     string                     `json:"target_ref,omitempty"`
	Resolution    platform.TargetResolution  `json:"resolution"`
	LastAttempt   *platform.TargetResolution `json:"last_attempt,omitempty"`
	Entry         agentSourceEntry           `json:"entry"`
}
type agentSourceEntry struct {
	Seq        uint64            `json:"seq"`
	Item       notification.View `json:"item"`
	ReceivedAt time.Time         `json:"received_at"`
}

func projectSource(e followupEvent, entry InboxEntry) agentSourceView {
	v := agentSourceView{e.SourceRef, e.Revision, e.Seq, e.DeliveryID, e.Source, e.ObjectRef, e.TargetRef, e.Resolution, nil, agentSourceEntry{entry.Seq, projectInboxEntry(entry, sourceSummary(e)).Item, entry.ReceivedAt}}
	if e.LastAttempt != nil {
		v.LastAttempt = &e.LastAttempt.Resolution
	}
	return v
}

type agentFollowupSummary struct {
	Ref           string                       `json:"unit_ref,omitempty"`
	ObjectLabel   string                       `json:"object_label,omitempty"`
	ObjectURL     string                       `json:"object_url,omitempty"`
	TargetLabel   string                       `json:"target_label,omitempty"`
	Kind          string                       `json:"kind"` // object, discussion, unresolved_source
	ObjectRef     string                       `json:"object_ref,omitempty"`
	TargetRef     string                       `json:"target_ref,omitempty"`
	Revision      uint64                       `json:"unit_version,omitempty"`
	Status        string                       `json:"status"`
	Summary       string                       `json:"summary,omitempty"`
	Next          string                       `json:"next,omitempty"`
	Questions     []followupQuestionSummary    `json:"questions,omitempty"`
	Instructions  []followupInstructionSummary `json:"instructions,omitempty"`
	Actions       []referenceActionSummary     `json:"actions,omitempty"`
	ReasonCode    string                       `json:"reason_code,omitempty"`
	Seq           uint64                       `json:"seq,omitempty"`
	Counts        *followupCounts              `json:"counts,omitempty"`        // derived, never a second authority
	AllCompleted  *bool                        `json:"all_completed,omitempty"` // object aggregation, distinct from its own unit status
	SourceRef     string                       `json:"source_ref,omitempty"`
	SourceVersion uint64                       `json:"source_version,omitempty"`
}

func projectFollowupSummary(v followupSummary) agentFollowupSummary {
	r := agentFollowupSummary{Ref: v.Ref, ObjectLabel: v.ObjectLabel, ObjectURL: v.ObjectURL, TargetLabel: v.TargetLabel, Kind: v.Kind, ObjectRef: v.ObjectRef, TargetRef: v.TargetRef, Revision: v.Revision, Status: v.Status, Summary: v.Summary, Next: v.Next, Questions: v.Questions, Instructions: v.Instructions, Actions: v.Actions, ReasonCode: v.ReasonCode, Seq: v.Seq, Counts: v.Counts, AllCompleted: v.AllCompleted}
	if v.Kind == "unresolved_source" {
		r.SourceRef, r.SourceVersion, r.Ref, r.Revision = v.Ref, v.Revision, "", 0
	}
	return r
}

type agentFollowupPage struct {
	Items    []agentFollowupSummary `json:"items"`
	Total    int                    `json:"total"`
	Next     string                 `json:"next,omitempty"`
	Complete bool                   `json:"complete"`
}

func projectFollowupPage(p followupPage) agentFollowupPage {
	v := agentFollowupPage{Items: []agentFollowupSummary{}, Total: p.Total, Next: p.Next, Complete: p.Complete}
	for _, s := range p.Items {
		v.Items = append(v.Items, projectFollowupSummary(s))
	}
	return v
}

// Room storage revision and confirmation protocol are not decision inputs.
// Keep the fixed snapshot and all coverage/clearing markers and message bodies.
type agentRoomReadPage struct {
	RoomIdentity
	MembershipID   string        `json:"membership_id,omitempty"`
	SnapshotSeq    uint64        `json:"snapshot_seq"`
	ClearedThrough uint64        `json:"cleared_through"`
	Messages       []RoomMessage `json:"messages"`
	FromExclusive  uint64        `json:"from_exclusive"`
	ScannedThrough uint64        `json:"scanned_through"`
	NextAfter      uint64        `json:"next_after"`
	HasMore        bool          `json:"has_more"`
	Remaining      int           `json:"remaining,omitempty"`
	UnreadCount    int           `json:"unread_count"`
}

func projectRoomRead(p ReadPage) agentRoomReadPage {
	return agentRoomReadPage{p.RoomIdentity, p.MembershipID, p.SnapshotSeq, p.ClearedThrough, p.Messages, p.FromExclusive, p.ScannedThrough, p.NextAfter, p.HasMore, p.Remaining, p.UnreadCount}
}
