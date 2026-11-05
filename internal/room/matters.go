package room

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
)

const matterRecordBytes int64 = 128 << 10

// Matters belong to the member, independently of Inbox consumption and Session.
// Each notification starts its own pending matter. Object grouping shares
// context, never a business status. Additional concerns use stable local IDs.
type Matter struct {
	Actions         []MatterAction   `json:"actions,omitempty"`
	Notices         []MatterNotice   `json:"notices,omitempty"`
	ProgressVersion uint64           `json:"progress_version,omitempty"`
	Decisions       *DecisionHistory `json:"decisions,omitempty"`
	ID              string           `json:"id"`
	Version         uint64           `json:"version"`
	Status          string           `json:"status"`
	Summary         string           `json:"summary,omitempty"`
	Next            string           `json:"next,omitempty"`
	Evidence        string           `json:"evidence,omitempty"`
	TurnNumber      uint64           `json:"turn_number"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type MatterRecord struct {
	Version int `json:"version"`
	MemberIdentity
	Context    *MatterContextView `json:"context,omitempty"` // Derived view; never persisted in the source record.
	Seq        uint64             `json:"seq"`
	DeliveryID string             `json:"delivery_id"`
	ObjectKey  string             `json:"object_key"`
	Source     platform.SourceRef `json:"source"`
	Matters    []Matter           `json:"matters"`
}

// Shadow only the decision/history fields at the Agent JSON boundary. Storage
// and human management continue to use the original records without projection.
type agentMatter struct {
	Matter
	Decisions []PolicyRevision `json:"decisions"`
}

type agentMatterRecord struct {
	MatterRecord
	Matters []agentMatter `json:"matters"`
}

func agentMatterRecordView(record MatterRecord, now time.Time) agentMatterRecord {
	view := agentMatterRecord{MatterRecord: record, Matters: make([]agentMatter, 0, len(record.Matters))}
	for _, m := range record.Matters {
		var history []PolicyRevision
		if m.Decisions != nil {
			history = m.Decisions.History
		}
		view.Matters = append(view.Matters, agentMatter{Matter: m, Decisions: effectivePolicy(history, now)})
	}
	return view
}

type agentMatterPage struct {
	MatterPage
	Records []agentMatterRecord `json:"records"`
}

func agentMatterPageView(page MatterPage) agentMatterPage {
	view := agentMatterPage{MatterPage: page, Records: make([]agentMatterRecord, 0, len(page.Records))}
	now := time.Now()
	for _, r := range page.Records {
		view.Records = append(view.Records, agentMatterRecordView(r, now))
	}
	return view
}

type MatterUpdate struct {
	Seq        uint64 `json:"seq"`
	ID         string `json:"id"`
	Version    uint64 `json:"version"` // Zero creates a new concern, not a merge.
	TurnNumber uint64 `json:"turn_number"`
	Status     string `json:"status"`
	Summary    string `json:"summary"`
	Next       string `json:"next"`
	Evidence   string `json:"evidence"`
}

// Missing account/project/object evidence falls back to the individual source.
// ID and IID remain distinct; no aliases, title matching or discussion inference.
func notificationObjectKey(source platform.SourceRef) string {
	r := source.Reference
	_, origin, err := connection.Normalize("notification", source.Instance)
	if err != nil {
		origin = source.Instance
	}
	objectID := r.Object.ID
	if objectID.Value == "" {
		objectID = r.Object.IID
	}
	parts := []string{source.Provider, origin, r.RecipientID.Namespace, r.RecipientID.Value, r.Scope,
		r.ProjectID.Namespace, r.ProjectID.Value, r.Object.Type, objectID.Namespace, objectID.Value}
	if r.RecipientID.Value == "" || r.ProjectID.Value == "" || r.Object.Type == "" || objectID.Value == "" {
		parts = []string{source.Provider, origin, source.Channel, source.SubscriptionID, source.ItemKey}
	}
	// Length-prefix every field so opaque IDs cannot introduce collisions.
	var key strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&key, "%d:%s", len(p), p)
	}
	digest := sha256.Sum256([]byte(key.String()))
	return hex.EncodeToString(digest[:])
}

func (s *Store) inboxEntryLocked(id MemberIdentity, seq uint64) (InboxEntry, error) {
	var e InboxEntry
	if _, _, err := s.memberPath(id, "agent.json", false); err != nil {
		return e, err
	}
	if err := readSnapshot(filepath.Join(s.dir, "inboxes", id.MembershipID, inboxEntryName(seq)), &e, DefaultInboxLimits().EntryBytes); err != nil {
		return e, err
	}
	if err := e.validate(id); err != nil {
		return e, err
	}
	if e.Seq != seq {
		return e, ErrInvalid
	}
	return e, nil
}

func (s *Store) matterPath(e InboxEntry) string {
	return filepath.Join(s.dir, "agents", e.MembershipID, "matters", notificationObjectKey(e.Item.Source), inboxEntryName(e.Seq))
}

func validateMatterRecord(r MatterRecord, e InboxEntry) error {
	if r.Context != nil || r.Version != 1 || r.MemberIdentity != e.MemberIdentity || r.Seq != e.Seq || r.DeliveryID != e.Item.DeliveryID ||
		r.Source != e.Item.Source || r.ObjectKey != notificationObjectKey(e.Item.Source) || len(r.Matters) == 0 || len(r.Matters) > 16 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, m := range r.Matters {
		if !safeMembershipID(m.ID) || m.Version == 0 || !validMatterStatus(m.Status) || m.UpdatedAt.IsZero() || seen[m.ID] {
			return ErrInvalid
		}
		if m.Decisions != nil {
			if len(m.Decisions.History) == 0 || validatePolicyHistory(m.Decisions.History, true) != nil {
				return ErrInvalid
			}
		}
		if err := validateHandling(m); err != nil {
			return err
		}
		for _, a := range m.Actions {
			if !targetMatchesSource(a.Target, r.Source) {
				return ErrInvalid
			}
		}
		for _, n := range m.Notices {
			if !strings.HasPrefix(n.ClientMessageID, fmt.Sprintf("matter-%s-%d-", r.MembershipID, r.Seq)) {
				return ErrInvalid
			}
		}
		seen[m.ID] = true
	}
	return nil
}

func validMatterStatus(status string) bool {
	switch status {
	case "pending", "in_progress", "waiting_user", "waiting_followup", "completed", "archived", "unknown":
		return true
	}
	return false
}

func (s *Store) loadMatterLocked(e InboxEntry) (MatterRecord, error) {
	var record MatterRecord
	if err := readSnapshot(s.matterPath(e), &record, matterRecordBytes); err != nil {
		return record, err
	}
	return record, validateMatterRecord(record, e)
}

// This operation precedes output and acknowledgment. A crash after some records
// commit simply resumes by stable seq/delivery ID. It never overwrites progress.
func (s *Store) associateInboxPage(page InboxPage, turn uint64) ([]MatterRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.runningTurnLocked(page.MemberIdentity, turn); err != nil {
		return nil, err
	}
	records := make([]MatterRecord, 0, len(page.Entries))
	for _, e := range page.Entries {
		record, err := s.loadMatterLocked(e)
		if errors.Is(err, os.ErrNotExist) {
			record = MatterRecord{Version: 1, MemberIdentity: e.MemberIdentity, Seq: e.Seq, DeliveryID: e.Item.DeliveryID,
				ObjectKey: notificationObjectKey(e.Item.Source), Source: e.Item.Source,
				Matters: []Matter{{ID: "main", Version: 1, Status: "pending", TurnNumber: turn, UpdatedAt: time.Now().UTC()}}}
			err = s.save(s.matterPath(e), record, matterRecordBytes)
		}
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) runningTurnLocked(id MemberIdentity, turn uint64) (AgentSnapshot, error) {
	var agent AgentSnapshot
	path, _, err := s.memberPath(id, "agent.json", true)
	if err != nil {
		return agent, err
	}
	if err := readSnapshot(path, &agent, maxPrivateFileBytes); err != nil {
		return agent, err
	}
	if agent.MemberIdentity != id || agent.validate() != nil {
		return agent, ErrInvalid
	}
	if agent.Turn == nil || agent.Turn.Status != "running" || (turn != 0 && agent.TurnNumber != turn) {
		return agent, ErrConflict
	}
	return agent, nil
}

func (s *Store) updateMatter(id MemberIdentity, u MatterUpdate) (MatterRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkMatterPrimaryLocked(id, MatterRef{Seq: u.Seq, ID: u.ID}); err != nil {
		return MatterRecord{}, err
	}
	if u.TurnNumber == 0 || !safeMembershipID(u.ID) || !validMatterStatus(u.Status) || !validText(u.Summary) ||
		len(u.Summary) > 4096 || len(u.Next) > 4096 || len(u.Evidence) > 4096 ||
		((u.Status == "waiting_user" || u.Status == "waiting_followup" || u.Status == "unknown") && !validText(u.Next)) ||
		(u.Status == "completed" && !validText(u.Evidence)) {
		return MatterRecord{}, ErrRequest
	}
	if _, err := s.runningTurnLocked(id, u.TurnNumber); err != nil {
		return MatterRecord{}, err
	}
	e, err := s.inboxEntryLocked(id, u.Seq)
	if err != nil {
		return MatterRecord{}, err
	}
	r, err := s.loadMatterLocked(e)
	if err != nil {
		return r, err
	}
	index := -1
	for i, m := range r.Matters {
		if m.ID == u.ID {
			index = i
			break
		}
	}
	if index < 0 {
		if u.Version != 0 {
			return r, ErrConflict
		}
		if len(r.Matters) >= 16 {
			return r, ErrCapacity
		}
		r.Matters = append(r.Matters, Matter{})
		index = len(r.Matters) - 1
	} else if r.Matters[index].Version != u.Version {
		return r, ErrConflict
	}
	decisions := r.Matters[index].Decisions
	old := r.Matters[index]
	if (u.Status == "completed" || u.Status == "archived") && unresolvedAction(old) {
		return r, ErrConflict
	}
	r.Matters[index] = Matter{Decisions: decisions, Actions: old.Actions, Notices: old.Notices, ProgressVersion: old.ProgressVersion + 1, ID: u.ID, Version: u.Version + 1, Status: u.Status, Summary: u.Summary, Next: u.Next,
		Evidence: u.Evidence, TurnNumber: u.TurnNumber, UpdatedAt: time.Now().UTC()}
	return r, s.save(s.matterPath(e), r, matterRecordBytes)
}

type MatterPage struct {
	Records   []MatterRecord `json:"records"`
	NextAfter uint64         `json:"next_after"`
	HasMore   bool           `json:"has_more"`
}

// The authoritative directory is also the object index. Only bounded record
// bodies are read; no second index can diverge from the notification linkage.
func (s *Store) listMatters(id MemberIdentity, object string, after uint64, limit int) (MatterPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := MatterPage{Records: []MatterRecord{}, NextAfter: after}
	if _, _, err := s.memberPath(id, "agent.json", false); err != nil {
		return page, err
	}
	if limit < 1 || limit > 25 {
		return page, ErrRequest
	}
	context, err := s.loadMatterContextLocked(id)
	if err != nil {
		return page, err
	}
	root := filepath.Join(s.dir, "agents", id.MembershipID, "matters")
	if object != "" {
		if b, err := hex.DecodeString(object); err != nil || len(b) != 32 {
			return page, ErrRequest
		}
		// Verified aliases may live in a different original object directory.
		// Keep the original immutable index and select aliases by bounded context.
	}
	type fileRef struct {
		path string
		seq  uint64
	}
	var files []fileRef
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".snapshot-") {
			return nil
		}
		var seq uint64
		if _, err := fmt.Sscanf(d.Name(), "%020d.json", &seq); err != nil || d.Name() != inboxEntryName(seq) {
			return ErrInvalid
		}
		if seq > after {
			if object != "" && filepath.Base(filepath.Dir(path)) != object {
				report := context.identity(seq)
				if report == nil || !report.ObjectVerified {
					return nil
				}
				source := report.Query.Source
				if resolvedNotificationObjectKey(source, report) != object {
					return nil
				}
			}
			files = append(files, fileRef{path, seq})
		}
		if len(files) > DefaultInboxLimits().Entries {
			return ErrCapacity
		}
		return nil
	})
	if err != nil {
		return page, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].seq < files[j].seq })
	var size int64
	for _, f := range files {
		if len(page.Records) == limit {
			page.HasMore = true
			break
		}
		e, err := s.inboxEntryLocked(id, f.seq)
		if err != nil {
			return page, err
		}
		if f.path != s.matterPath(e) {
			return page, ErrInvalid
		}
		r, err := s.loadMatterLocked(e)
		if err != nil {
			return page, err
		}
		r.Context = context.view(r.Seq, r.Source)
		body, err := json.Marshal(r)
		if err != nil {
			return page, err
		}
		if size+int64(len(body))+4096 > 1<<20 {
			if len(page.Records) == 0 {
				return page, ErrCapacity
			}
			page.HasMore = true
			break
		}
		page.Records = append(page.Records, r)
		page.NextAfter = f.seq
		size += int64(len(body))
	}
	return page, nil
}
