package room

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

const followupPageBytes = 128 << 10

type followupCounts struct {
	Units           int `json:"units"`
	Active          int `json:"active"`
	WaitingUser     int `json:"waiting_user"`
	WaitingFollowup int `json:"waiting_followup"`
	Unknown         int `json:"unknown"`
}

type followupSummary struct {
	Ref          string                       `json:"ref"`
	ObjectLabel  string                       `json:"object_label,omitempty"`
	ObjectURL    string                       `json:"object_url,omitempty"`
	TargetLabel  string                       `json:"target_label,omitempty"`
	Kind         string                       `json:"kind"` // object, discussion, unresolved_source
	ObjectRef    string                       `json:"object_ref,omitempty"`
	TargetRef    string                       `json:"target_ref,omitempty"`
	Revision     uint64                       `json:"revision"`
	Status       string                       `json:"status"`
	Summary      string                       `json:"summary,omitempty"`
	Next         string                       `json:"next,omitempty"`
	Questions    []followupQuestionSummary    `json:"questions,omitempty"`
	Instructions []followupInstructionSummary `json:"instructions,omitempty"`
	Actions      []referenceActionSummary     `json:"actions,omitempty"`
	ReasonCode   string                       `json:"reason_code,omitempty"`
	Seq          uint64                       `json:"seq,omitempty"`
	Counts       *followupCounts              `json:"counts,omitempty"`        // derived, never a second authority
	AllCompleted *bool                        `json:"all_completed,omitempty"` // object aggregation, distinct from its own unit status
}

type referenceActionSummary struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	Attempts          int    `json:"attempts"`
	PossibleDuplicate bool   `json:"possible_duplicate,omitempty"`
}

type followupQuestionSummary struct {
	Ref  string `json:"question_ref"`
	Text string `json:"text"`
	Seq  uint64 `json:"room_seq,omitempty"`
}

type followupInstructionSummary struct {
	Ref         string `json:"instruction_ref"`
	Version     uint64 `json:"version"`
	QuestionRef string `json:"question_ref"`
}

type followupQuery struct {
	ObjectRef        string
	Cursor           string
	Limit            int
	IncludeCompleted bool
	preferredObjects map[string]bool // Internal first-page recovery ordering only.
}

type followupPage struct {
	Items    []followupSummary `json:"items"`
	Total    int               `json:"total"`
	Next     string            `json:"next,omitempty"`
	Complete bool              `json:"complete"`
}

func activeFollowup(status string) bool { return status != "completed" && status != "archived" }

func unitSummary(u followupUnit) followupSummary {
	kind := "discussion"
	if u.TargetRef == u.ObjectRef {
		kind = "object"
	}
	v := followupSummary{Ref: u.Ref, Kind: kind, ObjectRef: u.ObjectRef, TargetRef: u.TargetRef, Revision: u.Revision,
		Status: u.Status, Summary: u.Summary, Next: u.Next}
	for _, q := range u.Questions {
		if len(q.Answers) == 0 {
			v.Questions = append(v.Questions, followupQuestionSummary{q.Ref, q.Text, q.Seq})
		}
	}
	for _, i := range u.Instructions {
		if activeFollowupInstruction(i, time.Now()) {
			v.Instructions = append(v.Instructions, followupInstructionSummary{i.Ref, i.Version, i.QuestionRef})
		}
	}
	for _, a := range u.Actions {
		if a.Status == "unknown" {
			v.Actions = append(v.Actions, referenceActionSummary{a.Request.ID, a.Status, a.Attempts, a.PossibleDuplicate})
		}
	}
	return v
}

func (s *Store) followupUnitsLocked(id MemberIdentity, object string) ([]followupUnit, error) {
	root := filepath.Join(s.followupRoot(id), "objects")
	if object != "" {
		if !validOpaqueRef(object, "ref_") {
			return nil, ErrRequest
		}
		root = filepath.Join(root, object)
	}
	files, err := followupFiles(root, "units")
	if err != nil {
		return nil, err
	}
	units := make([]followupUnit, 0, len(files))
	for _, path := range files {
		obj := filepath.Base(filepath.Dir(filepath.Dir(path)))
		ref := strings.TrimSuffix(filepath.Base(path), ".json")
		u, err := s.loadUnitLocked(id, obj, ref)
		if err != nil {
			return nil, err
		}
		units = append(units, u)
	}
	return units, nil
}

func (s *Store) visitFollowupEventsLocked(id MemberIdentity, visit func(followupEvent) error) error {
	files, err := followupFiles(filepath.Join(s.followupRoot(id), "events"), "events")
	if err != nil {
		return err
	}
	for _, path := range files {
		var seq uint64
		name := filepath.Base(path)
		if _, err := fmt.Sscanf(name, "%020d.json", &seq); err != nil || seq == 0 || name != inboxEntryName(seq) {
			return ErrInvalid
		}
		e, err := s.inboxEntryLocked(id, seq)
		if err != nil {
			return err
		}
		link, err := s.loadFollowupEventLocked(e)
		if err != nil {
			return err
		}
		if err := visit(link); err != nil {
			return err
		}
	}
	return nil
}

// A continuation is query- and member-scoped. It is a locator, never permission.
func followupCursorKey(id MemberIdentity, q followupQuery) string {
	return referenceDigest("", id, q.ObjectRef, q.IncludeCompleted)
}

func (s *Store) listFollowups(id MemberIdentity, q followupQuery) (followupPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listFollowupsLocked(id, q)
}

func (s *Store) listFollowupsLocked(id MemberIdentity, q followupQuery) (followupPage, error) {
	page := followupPage{Items: []followupSummary{}}
	if err := s.checkFollowupsLocked(id, false); err != nil {
		return page, err
	}
	if q.Limit < 1 || q.Limit > 25 || len(q.Cursor) > 512 {
		return page, ErrRequest
	}
	if len(q.preferredObjects) != 0 && q.Cursor != "" {
		return page, ErrRequest // Recovery ordering is only a first-page preview.
	}
	after := ""
	key := followupCursorKey(id, q)
	if q.Cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil {
			return page, ErrRequest
		}
		parts := strings.Split(string(b), ":")
		if len(parts) != 2 || parts[0] != key || (!validOpaqueRef(parts[1], "unit_") && !validOpaqueRef(parts[1], "src_")) {
			return page, ErrRequest
		}
		after = parts[1]
	}
	units, err := s.followupUnitsLocked(id, q.ObjectRef)
	if err != nil {
		return page, err
	}
	counts := map[string]followupCounts{}
	for _, u := range units {
		c := counts[u.ObjectRef]
		c.Units++
		if activeFollowup(u.Status) {
			c.Active++
		}
		if u.Status == "waiting_user" {
			c.WaitingUser++
		}
		if u.Status == "waiting_followup" {
			c.WaitingFollowup++
		}
		if u.Status == "unknown" || unresolvedReferenceAction(u) {
			c.Unknown++
		}
		counts[u.ObjectRef] = c
	}
	// Reuse durable resolution evidence only. Source URLs are unverified input;
	// no URL parsing, provider-specific ID conversion or remote lookup belongs here.
	var items []followupSummary
	objects := map[string]platform.TargetResolution{}
	if err := s.visitFollowupEventsLocked(id, func(e followupEvent) error {
		if e.ObjectRef == "" {
			if q.ObjectRef == "" {
				items = append(items, followupSummary{Ref: e.SourceRef, Kind: "unresolved_source", Revision: e.Revision,
					Seq: e.Seq, Status: string(e.Resolution.Status), Next: e.Resolution.Reason, ReasonCode: e.Resolution.ReasonCode})
			}
			return nil
		}
		if q.ObjectRef != "" && e.ObjectRef != q.ObjectRef {
			return nil
		}
		r := e.Resolution // a failed LastAttempt cannot erase verified evidence
		old, ok := objects[e.ObjectRef]
		if !ok {
			objects[e.ObjectRef] = r
		} else {
			old.Aliases = append(old.Aliases, r.Aliases...)
			if r.URL != "" && (old.URL == "" || r.CheckedAt.After(old.CheckedAt)) {
				old.URL, old.CheckedAt = r.URL, r.CheckedAt
			}
			objects[e.ObjectRef] = old
		}
		return nil
	}); err != nil {
		return page, err
	}
	objectLabels, objectURLs := map[string]string{}, map[string]string{}
	for ref, resolution := range objects {
		objectLabels[ref], objectURLs[ref] = followupObjectDisplay(resolution)
	}
	for _, u := range units {
		c := counts[u.ObjectRef]
		if !q.IncludeCompleted && !activeFollowup(u.Status) && !(u.TargetRef == u.ObjectRef && c.Active > 0) {
			continue
		}
		v := unitSummary(u)
		target, err := s.loadTargetLocked(id, u.ObjectRef, u.TargetRef)
		if err != nil {
			return page, err
		}
		v.ObjectLabel, v.ObjectURL = objectLabels[u.ObjectRef], objectURLs[u.ObjectRef]
		v.TargetLabel = target.Target.Object.Type + " " + target.Target.Object.ID.Value
		if target.Target.Discussion.Valid() {
			v.TargetLabel += " / " + target.Target.Discussion.Value
		}

		if v.Kind == "object" {
			v.Counts = &c
			complete := c.Active == 0
			v.AllCompleted = &complete
		}
		items = append(items, v)
	}
	sort.Slice(items, func(i, j int) bool {
		if q.preferredObjects[items[i].ObjectRef] != q.preferredObjects[items[j].ObjectRef] {
			return q.preferredObjects[items[i].ObjectRef]
		}
		return items[i].Ref < items[j].Ref
	})
	page.Total = len(items)
	size := 0
	for _, item := range items {
		if item.Ref <= after {
			continue
		}
		b, _ := json.Marshal(item)
		if len(page.Items) == q.Limit || size+len(b)+2048 > followupPageBytes {
			if len(page.Items) == 0 {
				return page, ErrCapacity
			}
			page.Next = base64.RawURLEncoding.EncodeToString([]byte(key + ":" + page.Items[len(page.Items)-1].Ref))
			return page, nil
		}
		page.Items = append(page.Items, item)
		size += len(b)
	}
	page.Complete = true
	return page, nil
}

// Detail is one unit, not the complete object/event history. Sources have their
// own bounded page; original content can be read using its immutable Inbox seq.
func (s *Store) readFollowup(id MemberIdentity, objectRef, unitRef string) (followupUnit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFollowupsLocked(id, false); err != nil {
		return followupUnit{}, err
	}
	return s.loadUnitLocked(id, objectRef, unitRef)
}

type followupSourceSummary struct {
	SourceRef      string                    `json:"source_ref"`
	SourceVersion  uint64                    `json:"source_version,omitempty"`
	Seq            uint64                    `json:"seq"`
	DeliveryID     string                    `json:"-"`
	ObjectRef      string                    `json:"object_ref,omitempty"`
	TargetRef      string                    `json:"target_ref,omitempty"`
	EvidenceSource string                    `json:"evidence_source,omitempty"`
	Status         platform.ResolutionStatus `json:"status"`
	ReasonCode     string                    `json:"reason_code,omitempty"`
	Reason         string                    `json:"reason,omitempty"`
}

func sourceSummary(e followupEvent) followupSourceSummary {
	r := e.Resolution
	if e.LastAttempt != nil {
		r = e.LastAttempt.Resolution
	}
	return followupSourceSummary{SourceRef: e.SourceRef, SourceVersion: e.Revision, Seq: e.Seq, DeliveryID: e.DeliveryID, ObjectRef: e.ObjectRef, TargetRef: e.TargetRef, EvidenceSource: r.EvidenceSource, Status: r.Status, ReasonCode: r.ReasonCode, Reason: r.Reason}
}

type followupSourcePage struct {
	Items     []followupSourceSummary `json:"items"`
	Total     int                     `json:"total"`
	NextAfter uint64                  `json:"next_after"`
	Complete  bool                    `json:"complete"`
}

func (s *Store) listFollowupSources(id MemberIdentity, objectRef string, after uint64, limit int) (followupSourcePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := followupSourcePage{Items: []followupSourceSummary{}, NextAfter: after}
	if err := s.checkFollowupsLocked(id, false); err != nil {
		return page, err
	}
	if limit < 1 || limit > 25 || (objectRef != "" && !validOpaqueRef(objectRef, "ref_")) {
		return page, ErrRequest
	}
	size := 0
	more := false
	err := s.visitFollowupEventsLocked(id, func(e followupEvent) error {
		if objectRef != "" && e.ObjectRef != objectRef {
			return nil
		}
		page.Total++
		if e.Seq <= after || more {
			return nil
		}
		v := sourceSummary(e)
		b, _ := json.Marshal(v)
		if len(page.Items) == limit || size+len(b)+2048 > followupPageBytes {
			more = true
			return nil
		}
		page.Items = append(page.Items, v)
		page.NextAfter = e.Seq
		size += len(b)
		return nil
	})
	if err != nil {
		return page, err
	}
	page.Complete = !more
	return page, nil
}

func (s *Store) readFollowupSource(id MemberIdentity, seq uint64, sourceRef string) (followupEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFollowupsLocked(id, false); err != nil {
		return followupEvent{}, err
	}
	e, err := s.inboxEntryLocked(id, seq)
	if err != nil {
		return followupEvent{}, err
	}
	link, err := s.loadFollowupEventLocked(e)
	if err == nil && link.SourceRef != sourceRef {
		return followupEvent{}, ErrIdentity
	}
	return link, err
}

// Labels name verified alternate native IDs with their namespaces. In particular,
// a canonical REST ID is never presented as an IID. The URL denotes the object,
// even when the summary is a discussion or message followup.
func followupObjectDisplay(r platform.TargetResolution) (string, string) {
	if !r.ObjectVerified || r.Target == nil {
		return "", ""
	}
	labels := map[string]bool{}
	for _, alias := range r.Aliases {
		if alias.Object.ID != r.Target.Object.ID {
			labels[alias.Object.Type+" "+alias.Object.ID.Namespace+":"+alias.Object.ID.Value] = true
		}
	}
	values := make([]string, 0, len(labels))
	for label := range labels {
		values = append(values, label)
	}
	sort.Strings(values)
	return strings.Join(values, "; "), r.URL
}
