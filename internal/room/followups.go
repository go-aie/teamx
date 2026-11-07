package room

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// The current business schema owns all persisted reference followups.
const followupSchema = 2
const followupBytes int64 = 128 << 10
const followupCapacity = 10000

type followupHeader struct {
	Version int `json:"version"`
	MemberIdentity
}

type storedTarget struct {
	followupHeader
	Ref       string                  `json:"ref"`
	ObjectRef string                  `json:"object_ref"`
	Target    platform.TargetIdentity `json:"target"`
}

// The original Inbox entry is the authority for source text and delivery.
// This separate record owns resolution evidence, never a rewritten source.
type followupEvent struct {
	followupHeader
	Seq         uint64                     `json:"seq"`
	DeliveryID  string                     `json:"delivery_id"`
	SourceRef   string                     `json:"source_ref"`
	Source      platform.SourceRef         `json:"source"`
	ObjectRef   string                     `json:"object_ref,omitempty"`
	TargetRef   string                     `json:"target_ref,omitempty"`
	Access      platform.AccessContext     `json:"access"`
	Resolution  platform.TargetResolution  `json:"resolution"`
	LastAttempt *followupResolutionAttempt `json:"last_attempt,omitempty"`
	Revision    uint64                     `json:"revision"`
}

type followupResolutionAttempt struct {
	Resolution platform.TargetResolution `json:"resolution"`
	Access     platform.AccessContext    `json:"access"`
}

type followupUnit struct {
	followupHeader
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

func referenceDigest(prefix string, parts ...any) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return prefix + hex.EncodeToString(h[:])
}

func targetReference(id MemberIdentity, target platform.TargetIdentity) string {
	key, _ := target.Key()
	return referenceDigest("ref_", id, key)
}

func objectIdentity(t platform.TargetIdentity) platform.TargetIdentity {
	t.Discussion, t.Message = platform.ExternalID{}, platform.ExternalID{}
	return t
}

func sameNativeTarget(a, b platform.TargetIdentity) bool {
	x, err := a.Key()
	y, other := b.Key()
	return err == nil && other == nil && x == y
}

func validOpaqueRef(ref, prefix string) bool {
	if !strings.HasPrefix(ref, prefix) {
		return false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(ref, prefix))
	return err == nil && len(b) == sha256.Size && ref == strings.ToLower(ref)
}

func (s *Store) followupRoot(id MemberIdentity) string {
	return filepath.Join(s.dir, "agents", id.MembershipID, "followups")
}

func (s *Store) followupSchemaExistsLocked(id MemberIdentity) (bool, error) {
	if _, _, err := s.memberPath(id, "agent.json", false); err != nil {
		return false, err
	}
	var h followupHeader
	err := readSnapshot(filepath.Join(s.followupRoot(id), "schema.json"), &h, followupBytes)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if h.Version != followupSchema || h.MemberIdentity != id {
		return false, ErrVersion
	}
	return true, nil
}

// Initialize the current schema only on an empty followup directory.
func (s *Store) initReferenceFollowups(id MemberIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.memberPath(id, "agent.json", true); err != nil {
		return err
	}
	enabled, err := s.followupSchemaExistsLocked(id)
	if err != nil || enabled {
		return err
	}
	if err := checkEmptyFollowupRoot(s.followupRoot(id)); err != nil {
		return err
	}
	return s.save(filepath.Join(s.followupRoot(id), "schema.json"), followupHeader{followupSchema, id}, followupBytes)
}

func (s *Store) checkFollowupsLocked(id MemberIdentity, writing bool) error {
	if _, _, err := s.memberPath(id, "agent.json", writing); err != nil {
		return err
	}
	enabled, err := s.followupSchemaExistsLocked(id)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrVersion
	}
	return nil
}

func (s *Store) targetPath(id MemberIdentity, objectRef, ref string) string {
	return filepath.Join(s.followupRoot(id), "objects", objectRef, "targets", ref+".json")
}

func (s *Store) unitPath(id MemberIdentity, objectRef, ref string) string {
	return filepath.Join(s.followupRoot(id), "objects", objectRef, "units", ref+".json")
}

func (s *Store) saveFollowupUnitLocked(id MemberIdentity, u followupUnit) error {
	u.Actions = append([]referenceAction(nil), u.Actions...)
	kept := u.Actions[:0]
	for _, a := range u.Actions {
		if !a.Ledger {
			kept = append(kept, a)
		}
	}
	u.Actions = kept
	b, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if int64(len(b)+1) > followupBytes {
		return ErrCapacity
	}
	return s.save(s.unitPath(id, u.ObjectRef, u.Ref), u, followupBytes)
}

func (s *Store) saveTargetLocked(id MemberIdentity, t platform.TargetIdentity) (storedTarget, error) {
	if !t.Valid() {
		return storedTarget{}, platform.ErrReference
	}
	r := storedTarget{followupHeader{followupSchema, id}, targetReference(id, t), targetReference(id, objectIdentity(t)), t}
	var old storedTarget
	path := s.targetPath(id, r.ObjectRef, r.Ref)
	err := readSnapshot(path, &old, followupBytes)
	if err == nil {
		if old.followupHeader != r.followupHeader || old.Ref != r.Ref || old.ObjectRef != r.ObjectRef || !sameNativeTarget(old.Target, r.Target) {
			return r, ErrInvalid
		}
		return old, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return r, err
	}
	files, err := followupFiles(filepath.Join(s.followupRoot(id), "objects"), "targets")
	if err != nil {
		return r, err
	}
	if len(files) >= 4*followupCapacity {
		return r, ErrCapacity
	}
	return r, s.save(path, r, followupBytes)
}

func (s *Store) loadTargetLocked(id MemberIdentity, objectRef, ref string) (storedTarget, error) {
	var r storedTarget
	if !validOpaqueRef(objectRef, "ref_") || !validOpaqueRef(ref, "ref_") {
		return r, ErrRequest
	}
	if err := readSnapshot(s.targetPath(id, objectRef, ref), &r, followupBytes); err != nil {
		return r, err
	}
	if r.Version != followupSchema || r.MemberIdentity != id || !r.Target.Valid() || r.Ref != ref || r.ObjectRef != objectRef ||
		targetReference(id, r.Target) != ref || targetReference(id, objectIdentity(r.Target)) != objectRef {
		return r, ErrInvalid
	}
	return r, nil
}

func (s *Store) loadUnitLocked(id MemberIdentity, objectRef, ref string) (followupUnit, error) {
	var u followupUnit
	if !validOpaqueRef(objectRef, "ref_") {
		return u, invalidField("object_ref", "required stable ref_ reference")
	}
	if !validOpaqueRef(ref, "unit_") {
		return u, invalidField("unit_ref", "required stable unit_ reference")
	}
	if err := readSnapshot(s.unitPath(id, objectRef, ref), &u, followupBytes); err != nil {
		return u, err
	}
	if u.Version != followupSchema || u.MemberIdentity != id || u.ObjectRef != objectRef || u.Ref != ref ||
		u.Ref != referenceDigest("unit_", u.TargetRef) || u.Revision == 0 || u.TurnNumber == 0 || !validFollowupStatus(u.Status) || u.UpdatedAt.IsZero() {
		return u, ErrInvalid
	}
	t, err := s.loadTargetLocked(id, objectRef, u.TargetRef)
	if err != nil {
		return u, err
	}
	if t.Target.Message.Valid() {
		return u, ErrInvalid // no semantic subtask or per-message followup units
	}
	if err := s.validateFollowupConversationLocked(id, u); err != nil {
		return u, err
	}
	if err := validateReferenceActions(u, t.Target); err != nil {
		return u, err
	}
	if err := s.projectUnitActionsLocked(id, &u); err != nil {
		return u, err
	}
	return u, nil
}

func (s *Store) ensureUnitLocked(id MemberIdentity, target storedTarget, turn uint64) (followupUnit, error) {
	ref := referenceDigest("unit_", target.Ref)
	u, err := s.loadUnitLocked(id, target.ObjectRef, ref)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return u, err
	}
	files, err := followupFiles(filepath.Join(s.followupRoot(id), "objects"), "units")
	if err != nil {
		return u, err
	}
	if len(files) >= followupCapacity {
		return u, ErrCapacity
	}
	u = followupUnit{followupHeader: followupHeader{followupSchema, id}, Ref: ref, ObjectRef: target.ObjectRef, TargetRef: target.Ref,
		Revision: 1, Status: "pending", TurnNumber: turn, UpdatedAt: time.Now().UTC()}
	return u, s.saveFollowupUnitLocked(id, u)
}

func (s *Store) eventPath(id MemberIdentity, seq uint64) string {
	return filepath.Join(s.followupRoot(id), "events", inboxEntryName(seq))
}

func (s *Store) loadFollowupEventLocked(e InboxEntry) (followupEvent, error) {
	var r followupEvent
	if err := readSnapshot(s.eventPath(e.MemberIdentity, e.Seq), &r, followupBytes); err != nil {
		return r, err
	}
	if r.Version != followupSchema || r.MemberIdentity != e.MemberIdentity || r.Seq != e.Seq || r.DeliveryID != e.Item.DeliveryID ||
		r.Source != e.Item.Source || r.SourceRef != referenceDigest("src_", e.MemberIdentity, e.Seq, e.Item.DeliveryID) || r.Revision == 0 {
		return r, ErrInvalid
	}
	if err := validateFollowupResolution(e, r.Resolution, r.Access); err != nil {
		return r, err
	}
	if r.LastAttempt != nil {
		if !r.Resolution.ObjectVerified || validateFollowupResolution(e, r.LastAttempt.Resolution, r.LastAttempt.Access) != nil {
			return r, ErrInvalid
		}
		if attempt := r.LastAttempt.Resolution; attempt.ObjectVerified {
			previous, current := verifiedResolutionTarget(r.Resolution), verifiedResolutionTarget(attempt)
			if attempt.Status != platform.ResolutionPartial || targetReference(e.MemberIdentity, objectIdentity(current)) != r.ObjectRef ||
				(previous.Discussion.Valid() && current.Discussion.Valid() && previous.Discussion != current.Discussion) ||
				(previous.Message.Valid() && current.Message.Valid() && previous.Message != current.Message) {
				return r, ErrInvalid
			}
		}
	}
	if r.Resolution.ObjectVerified {
		t, err := s.loadTargetLocked(e.MemberIdentity, r.ObjectRef, r.TargetRef)
		if err != nil {
			return r, err
		}
		if !sameNativeTarget(t.Target, verifiedResolutionTarget(r.Resolution)) {
			return r, ErrInvalid
		}
		if _, err := s.loadUnitLocked(e.MemberIdentity, r.ObjectRef, referenceDigest("unit_", r.ObjectRef)); err != nil {
			return r, err
		}
	} else if r.ObjectRef != "" || r.TargetRef != "" {
		return r, ErrInvalid
	}
	return r, nil
}

func verifiedResolutionTarget(r platform.TargetResolution) platform.TargetIdentity {
	t := *r.Target
	if !r.DiscussionVerified {
		t.Discussion = platform.ExternalID{}
	}
	if !r.MessageVerified {
		t.Message = platform.ExternalID{}
	}
	return t
}

// Validate common evidence here; each provider additionally verifies native
// fields/aliases against the actual response. No GitLab branching in this path.
func validateFollowupResolution(e InboxEntry, r platform.TargetResolution, a platform.AccessContext) error {
	if r.CheckedAt.IsZero() || r.CheckedAt.After(time.Now().Add(time.Minute)) || len(r.Reason) > 4096 || len(r.ReasonCode) > 128 || len(r.Aliases) > 32 {
		return platform.ErrReference
	}
	if r.EvidenceSource != "" {
		// Captured native identity is checked against the original saved entry,
		// independently of live member credentials. Unknown evidence fails closed.
		return validateNotificationResolution(e, r, a)
	}
	switch r.Status {
	case platform.ResolutionResolved, platform.ResolutionPartial, platform.ResolutionUnavailable, platform.ResolutionUnsupported, platform.ResolutionMismatch:
	default:
		return platform.ErrReference
	}
	if !r.ObjectVerified {
		if r.DiscussionVerified || r.MessageVerified || r.Status == platform.ResolutionResolved {
			return platform.ErrReference
		}
		return nil
	}
	if (r.Status != platform.ResolutionResolved && r.Status != platform.ResolutionPartial) || r.Target == nil || !r.Target.Valid() || !a.Valid() ||
		a.RoomIdentity != e.RoomIdentity || a.AgentID != e.AgentID || a.MembershipID != e.MembershipID || r.Account != a.Account ||
		(r.Target.Provider != a.Account.Provider || r.Target.Instance != a.Account.Instance) || r.Target.Provider != e.Item.Source.Provider || r.Target.Instance != e.Item.Source.Instance || r.Target.Tenant != e.Item.Source.Tenant ||
		(r.DiscussionVerified && !r.Target.Discussion.Valid()) || (r.MessageVerified && !r.Target.Message.Valid()) {
		return platform.ErrReference
	}
	for _, alias := range r.Aliases {
		if !alias.Valid() || alias.IdentityScope != r.Target.IdentityScope || !reflect.DeepEqual(alias.Containers, r.Target.Containers) || alias.Object.Type != r.Target.Object.Type {
			return platform.ErrReference
		}
	}
	return nil
}

// All identity/unit files are deterministic and commit before the event link.
// Repeating after any partial failure repairs linkage without replacing state.
func (s *Store) recordFollowupResolution(id MemberIdentity, seq, turn, expected uint64, r platform.TargetResolution, access platform.AccessContext) (followupEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFollowupsLocked(id, true); err != nil {
		return followupEvent{}, err
	}
	if turn == 0 {
		return followupEvent{}, ErrRequest
	}
	if _, err := s.runningTurnLocked(id, turn); err != nil {
		return followupEvent{}, err
	}
	e, err := s.inboxEntryLocked(id, seq)
	if err != nil {
		return followupEvent{}, err
	}
	if err := validateFollowupResolution(e, r, access); err != nil {
		return followupEvent{}, err
	}
	old, err := s.loadFollowupEventLocked(e)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return old, err
	}
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Stat(s.eventPath(id, seq)); statErr == nil {
			return old, ErrInvalid
		}
	}
	if old.Revision != expected {
		return old, ErrConflict
	}
	if expected == 0 {
		var c inboxConsumption
		if err := readSnapshot(filepath.Join(s.dir, "inboxes", id.MembershipID, "consumption.json"), &c, maxPrivateFileBytes); err != nil {
			return old, err
		}
		if c.MemberIdentity != id || c.Batch == nil || c.Batch.TurnNumber != turn || seq <= c.Batch.Start || seq > c.Batch.PageThrough {
			return old, ErrConflict
		}
	}
	if old.Resolution.ObjectVerified {
		// Failed refreshes cannot erase or rename verified object identity.
		if !r.ObjectVerified {
			old.LastAttempt = &followupResolutionAttempt{Resolution: r, Access: access}
			old.Revision++
			return old, s.save(s.eventPath(id, seq), old, followupBytes)
		}
		if targetReference(id, objectIdentity(*r.Target)) != old.ObjectRef {
			return old, ErrConflict
		}
		previous := verifiedResolutionTarget(old.Resolution)
		current := verifiedResolutionTarget(r)
		if (previous.Discussion.Valid() && current.Discussion.Valid() && previous.Discussion != current.Discussion) || (previous.Message.Valid() && current.Message.Valid() && previous.Message != current.Message) {
			return old, ErrConflict
		}
		weaker := (previous.Discussion.Valid() && !current.Discussion.Valid()) || (previous.Message.Valid() && !current.Message.Valid()) || (old.Resolution.Complete && !r.Complete)
		for _, alias := range old.Resolution.Aliases {
			key, _ := alias.Key()
			found := false
			for _, candidate := range r.Aliases {
				if other, _ := candidate.Key(); key == other {
					found = true
					break
				}
			}
			weaker = weaker || !found
		}
		if weaker {
			if r.Status != platform.ResolutionPartial {
				return old, ErrConflict
			}
			// A partial refresh may still verify the object while losing discussion,
			// message or alias evidence. Keep the historical identity and report this
			// failed attempt separately, just like a wholly unavailable refresh.
			old.LastAttempt = &followupResolutionAttempt{Resolution: r, Access: access}
			old.Revision++
			return old, s.save(s.eventPath(id, seq), old, followupBytes)
		}
	}
	link := followupEvent{followupHeader: followupHeader{followupSchema, id}, Seq: seq, DeliveryID: e.Item.DeliveryID,
		SourceRef: referenceDigest("src_", id, seq, e.Item.DeliveryID), Source: e.Item.Source, Resolution: r, Access: access, Revision: expected + 1}
	if r.ObjectVerified {
		object, err := s.saveTargetLocked(id, objectIdentity(*r.Target))
		if err != nil {
			return link, err
		}
		if _, err := s.ensureUnitLocked(id, object, turn); err != nil {
			return link, err
		}
		target, err := s.saveTargetLocked(id, verifiedResolutionTarget(r))
		if err != nil {
			return link, err
		}
		link.ObjectRef, link.TargetRef = object.Ref, target.Ref
	}
	return link, s.save(s.eventPath(id, seq), link, followupBytes)
}

// Opening a native discussion is explicit and idempotent; discovering twenty
// discussions does not create twenty pending business records.
func (s *Store) ensureDiscussionFollowup(id MemberIdentity, seq, turn uint64, targetRef string) (followupUnit, error) {
	u, _, err := s.ensureDiscussionFollowupResult(id, seq, turn, targetRef)
	return u, err
}

func (s *Store) ensureDiscussionFollowupResult(id MemberIdentity, seq, turn uint64, targetRef string) (followupUnit, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFollowupsLocked(id, true); err != nil {
		return followupUnit{}, false, err
	}
	if turn == 0 {
		return followupUnit{}, false, invalidField("turn_number", "required positive current_turn")
	}
	if _, err := s.runningTurnLocked(id, turn); err != nil {
		return followupUnit{}, false, err
	}
	e, err := s.inboxEntryLocked(id, seq)
	if err != nil {
		return followupUnit{}, false, err
	}
	link, err := s.loadFollowupEventLocked(e)
	if err != nil {
		return followupUnit{}, false, err
	}
	if link.TargetRef != targetRef || !link.Resolution.DiscussionVerified {
		return followupUnit{}, false, ErrConflict
	}
	t := verifiedResolutionTarget(link.Resolution)
	t.Message = platform.ExternalID{}
	target, err := s.saveTargetLocked(id, t)
	if err != nil {
		return followupUnit{}, false, err
	}
	_, err = s.loadUnitLocked(id, target.ObjectRef, referenceDigest("unit_", target.Ref))
	reused := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return followupUnit{}, false, err
	}
	u, err := s.ensureUnitLocked(id, target, turn)
	return u, reused, err
}

type followupUpdate struct {
	ObjectRef, UnitRef              string
	Revision, TurnNumber            uint64
	Status, Summary, Next, Evidence string
}

func (s *Store) updateFollowup(id MemberIdentity, q followupUpdate) (followupUnit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFollowupsLocked(id, true); err != nil {
		return followupUnit{}, err
	}
	if q.TurnNumber == 0 {
		return followupUnit{}, invalidField("turn_number", "required positive current_turn")
	}
	if _, err := s.runningTurnLocked(id, q.TurnNumber); err != nil {
		return followupUnit{}, err
	}
	u, err := s.loadUnitLocked(id, q.ObjectRef, q.UnitRef)
	if err != nil {
		return u, err
	}
	if u.Revision != q.Revision {
		return u, versionConflict("unit_version", q.Revision, u.Revision, "Read matters read and reassess before submitting a change.")
	}
	if !validFollowupStatus(q.Status) {
		return u, invalidField("status", "unsupported followup status; see matters update --help")
	}
	if !handlingText(q.Summary) {
		return u, invalidField("summary", "required nonempty text, at most 4096 bytes")
	}
	if len(q.Next) > 4096 {
		return u, invalidField("next", "must be at most 4096 bytes")
	}
	if len(q.Evidence) > 4096 {
		return u, invalidField("evidence", "must be at most 4096 bytes")
	}
	if (q.Status == "waiting_user" || q.Status == "waiting_followup" || q.Status == "unknown") && !validText(q.Next) {
		return u, invalidField("next", "required for waiting or unknown status")
	}
	if q.Status == "completed" && !validText(q.Evidence) {
		return u, invalidField("evidence", "required for completed status")
	}
	if !activeFollowup(q.Status) && (followupHasPendingConversation(u) || unresolvedReferenceAction(u)) {
		return u, conflictReason("status", "questions, instructions or unknown actions remain unresolved", "Read matters read and resolve pending work before completion/archival.")
	}
	u.Status, u.Summary, u.Next, u.Evidence = q.Status, q.Summary, q.Next, q.Evidence
	u.Revision++
	u.TurnNumber, u.UpdatedAt = q.TurnNumber, time.Now().UTC()
	return u, s.saveFollowupUnitLocked(id, u)
}

// Directory enumeration is bounded too, including completed history. Capacity
// rejects growth explicitly; no record or unresolved item is silently evicted.
func followupFiles(root, category string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".snapshot-") {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != category {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".json") || d.Type()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
		files = append(files, path)
		capacity := followupCapacity
		if category == "targets" {
			capacity *= 4
		}
		if len(files) > capacity {
			return ErrCapacity
		}
		return nil
	})
	return files, err
}
