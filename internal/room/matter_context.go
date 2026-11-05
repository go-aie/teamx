package room

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

const matterContextBytes int64 = 1 << 20

// A stable matter address is independent of object aliases and Session. Records,
// decisions, actions and notification receipts stay at their original address.
type MatterRef struct {
	Seq uint64 `json:"seq"`
	ID  string `json:"id"`
}

type MatterResolution struct {
	Seq    uint64                      `json:"seq"`
	Report gitlab.DiscussionResolution `json:"report"`
}

type MatterRelationChange struct {
	ID          string    `json:"id"` // stable operation ID, including split/correction operations
	Revision    uint64    `json:"revision"`
	TurnNumber  uint64    `json:"turn_number"`
	Kind        string    `json:"kind"` // append/merge: one canonical concern; relate: independent concerns; split: undo an edge
	From        MatterRef `json:"from"`
	FromVersion uint64    `json:"from_version"`
	To          MatterRef `json:"to"`
	ToVersion   uint64    `json:"to_version"`
	RelationID  string    `json:"relation_id,omitempty"` // split references the original edge ID
	Basis       string    `json:"basis"`
	Evidence    string    `json:"evidence"`
}

type MatterContextChange struct {
	ID          string             `json:"id"`
	Revision    uint64             `json:"revision"`
	TurnNumber  uint64             `json:"turn_number"`
	Resolutions []MatterResolution `json:"resolutions"`
}

type MatterContextCommit struct {
	Change MatterContextChange `json:"change"`
	At     time.Time           `json:"at"`
}

type MatterRelationCommit struct {
	Change MatterRelationChange `json:"change"`
	At     time.Time            `json:"at"`
}

type MatterContext struct {
	Version int `json:"version"`
	MemberIdentity
	Revision    uint64                 `json:"revision"`
	Enrichments []MatterContextCommit  `json:"enrichments"`
	Relations   []MatterRelationCommit `json:"relations"`
}

type MatterContextView struct {
	Resolution         *gitlab.DiscussionResolution `json:"resolution,omitempty"`
	CachedIdentity     *gitlab.DiscussionResolution `json:"cached_identity,omitempty"`
	CanonicalObjectKey string                       `json:"canonical_object_key,omitempty"`
	Relations          []MatterRelationChange       `json:"relations,omitempty"`
}

func (c MatterContext) resolution(seq uint64) *gitlab.DiscussionResolution {
	for i := len(c.Enrichments) - 1; i >= 0; i-- {
		for _, r := range c.Enrichments[i].Change.Resolutions {
			if r.Seq == seq {
				report := r.Report
				return &report
			}
		}
	}
	return nil
}

// Failed rechecks remain visible without erasing previously verified identity.
func (c MatterContext) identity(seq uint64) *gitlab.DiscussionResolution {
	for i := len(c.Enrichments) - 1; i >= 0; i-- {
		for _, r := range c.Enrichments[i].Change.Resolutions {
			if r.Seq == seq && r.Report.ObjectVerified {
				report := r.Report
				return &report
			}
		}
	}
	return nil
}

func (c MatterContext) edges() map[string]MatterRelationChange {
	edges := map[string]MatterRelationChange{}
	for _, op := range c.Relations {
		if op.Change.Kind == "split" {
			delete(edges, op.Change.RelationID)
		} else {
			edges[op.Change.ID] = op.Change
		}
	}
	return edges
}

func (c MatterContext) primary(ref MatterRef) MatterRef {
	edges := c.edges()
	for range len(edges) + 1 {
		next := ref
		for _, edge := range edges {
			if edge.Kind != "relate" && edge.From == ref {
				next = edge.To
				break
			}
		}
		if next == ref {
			return ref
		}
		ref = next
	}
	return MatterRef{} // A persisted cycle is corrupt, never an alternate executor.
}

// The built-in Events project scan and Todos account scan describe collection
// scope, not authority. Only after remote verification can these two known
// scopes share an object index. Unknown scopes remain isolated.
func verifiedSourceScope(source platform.SourceRef) string {
	if source.Provider == "gitlab" && ((source.Channel == "events" && source.Reference.Scope == "project") || (source.Channel == "todos" && source.Reference.Scope == "account")) {
		return "gitlab:verified-recipient"
	}
	return source.Reference.Scope
}
func resolvedNotificationObjectKey(source platform.SourceRef, report *gitlab.DiscussionResolution) string {
	source.Reference.Object = report.Object
	source.Reference.Scope = verifiedSourceScope(source)
	return notificationObjectKey(source)
}

func (c MatterContext) view(seq uint64, source platform.SourceRef) *MatterContextView {
	v := &MatterContextView{Resolution: c.resolution(seq)}
	if r := c.identity(seq); r != nil {
		if v.Resolution == nil || !v.Resolution.ObjectVerified {
			v.CachedIdentity = r
		}
		v.CanonicalObjectKey = resolvedNotificationObjectKey(source, r)
	}
	edges := c.edges()
	// Preserve deterministic history order in JSON, rather than map iteration.
	for _, op := range c.Relations {
		if edge, ok := edges[op.Change.ID]; ok && (edge.From.Seq == seq || edge.To.Seq == seq) {
			v.Relations = append(v.Relations, edge)
		}
	}
	if v.Resolution == nil && len(v.Relations) == 0 {
		return nil
	}
	return v
}

func (s *Store) loadMatterContextLocked(id MemberIdentity) (MatterContext, error) {
	c := MatterContext{Version: 1, MemberIdentity: id, Enrichments: []MatterContextCommit{}, Relations: []MatterRelationCommit{}}
	path, _, err := s.memberPath(id, "matter-context.json", false)
	if err != nil {
		return c, err
	}
	var loaded MatterContext
	if err = readSnapshot(path, &loaded, matterContextBytes); errors.Is(err, os.ErrNotExist) {
		return c, nil
	} else if err != nil {
		return c, err
	}
	c = loaded
	if c.Version != 1 || c.MemberIdentity != id || c.Revision != uint64(len(c.Enrichments)+len(c.Relations)) || c.Revision > 256 {
		return c, ErrInvalid
	}
	ids := map[string]bool{}
	revisions := map[uint64]bool{}
	var previousEnrichment uint64
	for i, commit := range c.Enrichments {
		if i > 0 && commit.Change.Revision <= previousEnrichment {
			return c, ErrInvalid
		}
		previousEnrichment = commit.Change.Revision
		op := commit.Change
		if !safeMembershipID(op.ID) || ids[op.ID] || op.Revision >= c.Revision || revisions[op.Revision] || op.TurnNumber == 0 || commit.At.IsZero() || len(op.Resolutions) == 0 || len(op.Resolutions) > 25 {
			return c, ErrInvalid
		}
		ids[op.ID] = true
		revisions[op.Revision] = true
		seen := map[uint64]bool{}
		for _, r := range op.Resolutions {
			e, err := s.inboxEntryLocked(id, r.Seq)
			if err != nil || seen[r.Seq] || validateDiscussionResolution(r.Report, e) != nil {
				return c, ErrInvalid
			}
			seen[r.Seq] = true
		}
	}
	prior := MatterContext{}
	var previousRelation uint64
	for i, commit := range c.Relations {
		if i > 0 && commit.Change.Revision <= previousRelation {
			return c, ErrInvalid
		}
		previousRelation = commit.Change.Revision
		op := commit.Change
		if validateRelation(op) != nil || ids[op.ID] || op.Revision >= c.Revision || revisions[op.Revision] || commit.At.IsZero() {
			return c, ErrInvalid
		}
		ids[op.ID] = true
		revisions[op.Revision] = true
		if op.Kind == "split" {
			edge, ok := prior.edges()[op.RelationID]
			if !ok || edge.From != op.From || edge.To != op.To {
				return c, ErrInvalid
			}
		} else if op.Kind != "relate" {
			if prior.primary(op.From) != op.From || prior.primary(op.To) != op.To {
				return c, ErrInvalid
			}
		}
		for _, ref := range []MatterRef{op.From, op.To} {
			e, err := s.inboxEntryLocked(id, ref.Seq)
			if err != nil {
				return c, err
			}
			r, err := s.loadMatterLocked(e)
			if err != nil {
				return c, err
			}
			found := false
			for _, m := range r.Matters {
				if m.ID == ref.ID {
					found = true
				}
			}
			if !found {
				return c, ErrInvalid
			}
		}
		prior.Relations = append(prior.Relations, commit)
	}
	return c, nil
}

func validateDiscussionResolution(r gitlab.DiscussionResolution, e InboxEntry) error {
	if r.Query.Source != e.Item.Source || r.Query.URL != e.Item.Source.URL || r.CheckedAt.IsZero() || len(r.Reason) > 4096 || len(r.Mappings) > 500 || len(r.ObjectURL) > 4096 {
		return ErrRequest
	}
	switch r.Status {
	case "resolved", "partial", "unavailable", "unsupported", "mismatch":
	default:
		return ErrRequest
	}
	source := e.Item.Source
	if !source.Reference.Valid() || source.Provider != "gitlab" {
		return ErrRequest
	}
	if r.ObjectVerified {
		ref := source.Reference
		positive := func(id platform.ExternalID, namespace string) bool {
			n, e := strconv.ParseInt(id.Value, 10, 64)
			return e == nil && n > 0 && id.Namespace == namespace && strconv.FormatInt(n, 10) == id.Value
		}
		u, e := url.Parse(r.ObjectURL)
		_, origin, oe := connection.Normalize(source.Provider, source.Instance)
		resource := ""
		switch r.Object.Type {
		case "MergeRequest":
			resource = "merge_requests"
		case "Issue":
			resource = "issues"
		}
		if !positive(r.Object.ID, "gitlab:rest:id") || !positive(r.Object.IID, "gitlab:rest:iid") || !positive(ref.ProjectID, "gitlab:rest:id") || !positive(r.AccountID, "gitlab:rest:id") || resource == "" || e != nil || oe != nil || u.User != nil || u.Scheme+"://"+strings.ToLower(u.Host) != origin || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/-/"+resource+"/"+r.Object.IID.Value) {
			return ErrRequest
		}
		if !targetMatchesSource(ActionTarget{Provider: source.Provider, Instance: source.Instance, ProjectID: ref.ProjectID, Object: r.Object}, source) || r.AccountID != ref.RecipientID || r.AccountID.Value == "" || !platform.ValidUsername(r.Username) || r.Object.ID.Value == "" || r.Object.IID.Value == "" {
			return ErrRequest
		}
	}
	if !r.ObjectVerified && (r.Complete || len(r.Mappings) > 0 || r.NoteID.Value != "" || r.DiscussionID.Value != "") {
		return ErrRequest
	}
	seen := map[platform.ExternalID]bool{}
	found := r.NoteID.Value == "" && r.DiscussionID.Value == ""
	for _, m := range r.Mappings {
		if n, e := strconv.ParseInt(m.NoteID.Value, 10, 64); e != nil || n <= 0 || strconv.FormatInt(n, 10) != m.NoteID.Value {
			return ErrRequest
		}
		if m.NoteID.Namespace != "gitlab:rest:id" || m.NoteID.Value == "" || m.DiscussionID.Namespace != "gitlab:rest:discussion" || !handlingText(m.DiscussionID.Value) || seen[m.NoteID] {
			return ErrRequest
		}
		seen[m.NoteID] = true
		if m.NoteID == r.NoteID && m.DiscussionID == r.DiscussionID {
			found = true
		}
	}
	if !found || (r.Status == "resolved" && (!r.ObjectVerified || !r.Complete)) {
		return ErrRequest
	}
	return nil
}

func (s *Store) readMatterContext(id MemberIdentity) (MatterContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadMatterContextLocked(id)
}

func (s *Store) enrichMatterContext(id MemberIdentity, op MatterContextChange) (MatterContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadMatterContextLocked(id)
	if err != nil {
		return c, err
	}
	if _, err = s.runningTurnLocked(id, op.TurnNumber); err != nil || op.TurnNumber == 0 {
		if err == nil {
			err = ErrRequest
		}
		return c, err
	}
	for _, old := range c.Enrichments {
		if old.Change.ID == op.ID {
			if reflect.DeepEqual(old.Change, op) {
				return c, nil
			}
			return c, ErrConflict
		}
	}
	for _, old := range c.Relations {
		if old.Change.ID == op.ID {
			return c, ErrConflict
		}
	}
	if !safeMembershipID(op.ID) || len(op.Resolutions) == 0 || len(op.Resolutions) > 25 {
		return c, ErrRequest
	}
	if op.Revision != c.Revision {
		return c, ErrConflict
	}
	if c.Revision >= 256 {
		return c, ErrCapacity
	}
	seen := map[uint64]bool{}
	for _, r := range op.Resolutions {
		e, err := s.inboxEntryLocked(id, r.Seq)
		if err != nil {
			return c, err
		}
		if _, err = s.loadMatterLocked(e); err != nil {
			return c, err
		}
		if old := c.identity(r.Seq); old != nil && r.Report.ObjectVerified && old.Object != r.Report.Object {
			return c, ErrConflict
		}
		if seen[r.Seq] || validateDiscussionResolution(r.Report, e) != nil || r.Report.CheckedAt.After(time.Now().Add(time.Minute)) || time.Since(r.Report.CheckedAt) > 10*time.Minute {
			return c, ErrRequest
		}
		seen[r.Seq] = true
		// Prevent repeated model turns from turning this on-demand operation into polling.
		if old := c.resolution(r.Seq); old != nil && r.Report.CheckedAt.Sub(old.CheckedAt) < time.Minute {
			return c, ErrConflict
		}
	}
	c.Enrichments = append(c.Enrichments, MatterContextCommit{op, time.Now().UTC()})
	c.Revision++
	path, _, err := s.memberPath(id, "matter-context.json", true)
	if err != nil {
		return c, err
	}
	return c, s.save(path, c, matterContextBytes)
}

func validateRelation(op MatterRelationChange) error {
	if !safeMembershipID(op.ID) || op.TurnNumber == 0 || op.From.Seq == 0 || op.To.Seq == 0 || !safeMembershipID(op.From.ID) || !safeMembershipID(op.To.ID) || op.From == op.To || op.FromVersion == 0 || op.ToVersion == 0 || !handlingText(op.Basis) || !handlingText(op.Evidence) {
		return ErrRequest
	}
	switch op.Kind {
	case "append", "merge", "relate":
		if op.RelationID != "" {
			return ErrRequest
		}
	case "split":
		if !safeMembershipID(op.RelationID) {
			return ErrRequest
		}
	default:
		return ErrRequest
	}
	return nil
}

func (s *Store) relationMatterLocked(id MemberIdentity, ref MatterRef, version, turn uint64) (MatterRecord, Matter, error) {
	_, r, i, err := s.handlingMatterLocked(id, MatterCommand{Seq: ref.Seq, MatterID: ref.ID, MatterVersion: version, TurnNumber: turn})
	if err != nil {
		return r, Matter{}, err
	}
	return r, r.Matters[i], nil
}

func verifiedRelationScope(a, b MatterRecord, ca, cb *gitlab.DiscussionResolution, sameObject bool) bool {
	_, ai, ae := connection.Normalize(a.Source.Provider, a.Source.Instance)
	_, bi, be := connection.Normalize(b.Source.Provider, b.Source.Instance)
	if ca == nil || cb == nil || !ca.ObjectVerified || !cb.ObjectVerified || ae != nil || be != nil || a.Source.Provider != b.Source.Provider || ai != bi || ca.AccountID != cb.AccountID || ca.AccountID.Value == "" || verifiedSourceScope(a.Source) != verifiedSourceScope(b.Source) {
		return false
	}
	return !sameObject || (a.Source.Reference.ProjectID == b.Source.Reference.ProjectID && ca.Object == cb.Object)
}

func (s *Store) relateMatters(id MemberIdentity, op MatterRelationChange) (MatterContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadMatterContextLocked(id)
	if err != nil {
		return c, err
	}
	if validateRelation(op) != nil {
		return c, ErrRequest
	}
	if _, err = s.runningTurnLocked(id, op.TurnNumber); err != nil {
		return c, err
	}
	for _, old := range c.Relations {
		if old.Change.ID == op.ID {
			if reflect.DeepEqual(old.Change, op) {
				return c, nil
			}
			return c, ErrConflict
		}
	}
	for _, old := range c.Enrichments {
		if old.Change.ID == op.ID {
			return c, ErrConflict
		}
	}
	if c.Revision != op.Revision {
		return c, ErrConflict
	}
	if c.Revision >= 256 {
		return c, ErrCapacity
	}
	a, am, err := s.relationMatterLocked(id, op.From, op.FromVersion, op.TurnNumber)
	if err != nil {
		return c, err
	}
	b, bm, err := s.relationMatterLocked(id, op.To, op.ToVersion, op.TurnNumber)
	if err != nil {
		return c, err
	}
	if op.Kind == "split" {
		edge, ok := c.edges()[op.RelationID]
		if !ok || edge.From != op.From || edge.To != op.To {
			return c, ErrConflict
		}
	} else {
		if !verifiedRelationScope(a, b, c.identity(a.Seq), c.identity(b.Seq), op.Kind != "relate") {
			return c, ErrRequest
		}
		for _, edge := range c.edges() {
			if edge.From == op.From && edge.To == op.To || edge.From == op.To && edge.To == op.From {
				return c, ErrConflict
			}
		}
		if op.Kind != "relate" {
			if c.primary(op.From) != op.From || c.primary(op.To) != op.To || unresolvedAction(am) || unresolvedAction(bm) || am.Status == "unknown" || bm.Status == "unknown" || am.Decisions != nil || bm.Decisions != nil {
				return c, ErrConflict
			}
			// Existing questions/completion conditions must be settled independently.
			if hasPendingMatterNotice(am) || hasPendingMatterNotice(bm) {
				return c, ErrConflict
			}
			if am.Status == "waiting_user" || bm.Status == "waiting_user" || am.Status == "waiting_followup" || bm.Status == "waiting_followup" {
				return c, ErrConflict
			}
		}
	}
	c.Relations = append(c.Relations, MatterRelationCommit{op, time.Now().UTC()})
	c.Revision++
	path, _, err := s.memberPath(id, "matter-context.json", true)
	if err != nil {
		return c, err
	}
	return c, s.save(path, c, matterContextBytes)
}

// A merged source cannot acquire new actions/progress. All evidence remains
// readable; a split re-enables it. Relate edges never redirect execution.
func (s *Store) checkMatterPrimaryLocked(id MemberIdentity, ref MatterRef) error {
	c, err := s.loadMatterContextLocked(id)
	if err != nil {
		return err
	}
	if primary := c.primary(ref); primary != ref {
		return fmt.Errorf("%w: matter continues at seq=%d id=%s; inspect context and original evidence", ErrConflict, primary.Seq, primary.ID)
	}
	return nil
}

func hasPendingMatterNotice(m Matter) bool {
	for _, n := range m.Notices {
		if n.Seq == 0 || n.Kind == "question" && len(n.Answers) == 0 {
			return true
		}
	}
	return false
}
