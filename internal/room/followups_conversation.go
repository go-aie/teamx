package room

import (
	"context"
	"reflect"
	"sort"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Conversation records live in the unit snapshot: saving an answer and resuming
// the unit is one atomic write. These are evidence, not inferred permissions.
type followupCommand struct {
	ObjectRef  string `json:"object_ref"`
	UnitRef    string `json:"unit_ref"`
	Revision   uint64 `json:"unit_version"`
	TurnNumber uint64 `json:"turn_number"`
}

type followupIntent struct {
	Operation   platform.Operation `json:"operation"`
	Body        string             `json:"body,omitempty"`
	Constraints string             `json:"constraints,omitempty"`
}

type followupQuestion struct {
	Ref             string           `json:"question_ref"`
	TargetRef       string           `json:"target_ref"`
	MessageRefs     []string         `json:"message_refs,omitempty"`
	Text            string           `json:"text"`
	Proposed        *followupIntent  `json:"proposed,omitempty"`
	ClientMessageID string           `json:"client_message_id"`
	Seq             uint64           `json:"room_seq,omitempty"`
	Answers         []followupAnswer `json:"answers,omitempty"`
}

type followupQuestionChange struct {
	followupCommand
	MessageRefs []string        `json:"message_refs,omitempty"`
	Text        string          `json:"text"`
	Proposed    *followupIntent `json:"proposed,omitempty"`
}

type followupReplyChange struct {
	followupCommand
	QuestionRef string `json:"question_ref,omitempty"`
	SourceSeq   uint64 `json:"source_seq"`
	Basis       string `json:"basis,omitempty"` // Agent's explicit, unambiguous contextual association
}

func followupNoticeID(id MemberIdentity, parts ...any) string {
	// Reuse Room's durable outbox receipt mechanism, including clear/restart.
	return "followup-" + id.MembershipID + "-followup-" + referenceDigest("", parts...)
}

func questionReference(u followupUnit, q followupQuestion) string {
	return referenceDigest("question_", u.Ref, q.TargetRef, q.MessageRefs, q.Text, q.Proposed)
}

func canonicalMessageRefs(refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	result := append([]string(nil), refs...)
	sort.Strings(result)
	return result
}

func validFollowupIntent(i followupIntent, discussion bool) bool {
	operation := platform.OperationObjectComment
	if discussion {
		operation = platform.OperationDiscussionReply
	}
	return i.Operation == operation && (validText(i.Body) || validText(i.Constraints)) && len(i.Body) <= 4096 && len(i.Constraints) <= 4096
}

func (s *Store) conversationUnitLocked(id MemberIdentity, c followupCommand) (followupUnit, error) {
	if err := s.checkFollowupsLocked(id, true); err != nil {
		return followupUnit{}, err
	}
	if c.TurnNumber == 0 {
		return followupUnit{}, invalidField("turn_number", "required positive current_turn")
	}
	if _, err := s.runningTurnLocked(id, c.TurnNumber); err != nil {
		return followupUnit{}, err
	}
	return s.loadUnitLocked(id, c.ObjectRef, c.UnitRef)
}

func (s *Store) validateQuestionTargetLocked(id MemberIdentity, u followupUnit, refs []string) error {
	if len(refs) > 16 {
		return ErrCapacity
	}
	seen := map[string]bool{}
	unit, err := s.loadTargetLocked(id, u.ObjectRef, u.TargetRef)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		t, err := s.loadTargetLocked(id, u.ObjectRef, ref)
		if err != nil {
			return err
		}
		if seen[ref] || !t.Target.Message.Valid() || (unit.Target.Discussion.Valid() && t.Target.Discussion != unit.Target.Discussion) {
			return ErrIdentity
		}
		seen[ref] = true
	}
	return nil
}

func validHumanEvidence(m RoomMessage) bool {
	return m.Seq != 0 && m.Author.Kind == "human" && validText(m.Author.ID) && m.Source == nil && validText(m.Text) && len(m.Text) <= maxTextBytes && !m.CreatedAt.IsZero()
}

func (s *Store) validateFollowupConversationLocked(id MemberIdentity, u followupUnit) error {
	if len(u.Questions) > 16 || len(u.Instructions) > 32 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, q := range u.Questions {
		if q.Ref != questionReference(u, q) || seen[q.Ref] || q.TargetRef != u.TargetRef || !reflect.DeepEqual(q.MessageRefs, canonicalMessageRefs(q.MessageRefs)) || !handlingText(q.Text) ||
			q.ClientMessageID != followupNoticeID(id, u.Ref, q.Ref) || len(q.Answers) > 16 ||
			(q.Proposed != nil && !validFollowupIntent(*q.Proposed, u.TargetRef != u.ObjectRef)) {
			return ErrInvalid
		}
		if err := s.validateQuestionTargetLocked(id, u, q.MessageRefs); err != nil {
			return err
		}
		seen[q.Ref] = true
		answers := map[uint64]bool{}
		for _, a := range q.Answers {
			if q.Seq == 0 || !validHumanEvidence(a.Message) || a.Message.Seq <= q.Seq || (a.Message.ReplyTo != 0 && a.Message.ReplyTo != q.Seq) || !handlingText(a.Basis) || answers[a.Message.Seq] {
				return ErrInvalid
			}
			answers[a.Message.Seq] = true
		}
	}
	return s.validateFollowupInstructionsLocked(id, u)
}

func followupHasPendingConversation(u followupUnit) bool {
	for _, q := range u.Questions {
		if len(q.Answers) == 0 {
			return true
		}
	}
	for _, i := range u.Instructions {
		if activeFollowupInstruction(i, time.Now()) {
			return true
		}
	}
	return false
}

func touchFollowup(u *followupUnit, turn uint64) {
	u.Revision++
	u.TurnNumber, u.UpdatedAt = turn, time.Now().UTC()
}

func (s *Store) prepareFollowupQuestion(id MemberIdentity, c followupQuestionChange) (followupUnit, followupQuestion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.conversationUnitLocked(id, c.followupCommand)
	if err != nil {
		return u, followupQuestion{}, err
	}
	q := followupQuestion{TargetRef: u.TargetRef, MessageRefs: canonicalMessageRefs(c.MessageRefs), Text: c.Text, Proposed: c.Proposed}
	q.Ref = questionReference(u, q)
	q.ClientMessageID = followupNoticeID(id, u.Ref, q.Ref)
	if !handlingText(q.Text) {
		return u, q, invalidField("text", "required nonempty text, at most 4096 bytes")
	}
	if q.Proposed != nil && !validFollowupIntent(*q.Proposed, u.TargetRef != u.ObjectRef) {
		return u, q, invalidField("proposed", "requires the target's operation and body or constraints, each at most 4096 bytes")
	}
	if err := s.validateQuestionTargetLocked(id, u, q.MessageRefs); err != nil {
		return u, q, err
	}
	for _, old := range u.Questions {
		if old.Ref == q.Ref {
			return u, old, nil
		} // retry after any commit gap, including another turn
		// Rephrasing the same still-open question cannot produce repeated public asks.
		if len(old.Answers) == 0 && reflect.DeepEqual(old.MessageRefs, q.MessageRefs) {
			return u, old, conflictReason("text", "an unanswered question already exists for these messages", "Read matters read and reuse the saved question; do not ask it again.")
		}
	}
	if u.Revision != c.Revision {
		return u, q, versionConflict("unit_version", c.Revision, u.Revision, "Read matters read and reassess; reuse the saved question on uncertain sends.")
	}
	if len(u.Questions) >= 16 {
		return u, q, ErrCapacity
	}
	u.Questions = append(u.Questions, q)
	u.Status, u.Next = "waiting_user", q.Text
	touchFollowup(&u, c.TurnNumber)
	err = s.saveFollowupUnitLocked(id, u)
	return u, q, err
}

func (s *Store) finishFollowupQuestion(id MemberIdentity, c followupCommand, ref string) (followupUnit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.conversationUnitLocked(id, c)
	if err != nil {
		return u, err
	}
	room, err := s.loadRoom()
	if err != nil {
		return u, err
	}
	for j := range u.Questions {
		q := &u.Questions[j]
		if q.Ref != ref {
			continue
		}
		receipt, ok := room.FollowupNoticeReceipts[q.ClientMessageID]
		if !ok || receipt.Author != (Author{Kind: "agent", ID: id.AgentID}) || receipt.MembershipID != id.MembershipID || receipt.Digest != noticeDigest(q.Text) {
			return u, ErrIdentity
		}
		if q.Seq == receipt.Seq {
			return u, nil
		}
		if q.Seq != 0 {
			return u, ErrConflict
		}
		q.Seq = receipt.Seq
		touchFollowup(&u, c.TurnNumber)
		return u, s.saveFollowupUnitLocked(id, u)
	}
	return u, ErrRequest
}

func (s *service) askFollowup(ctx context.Context, id MemberIdentity, c followupQuestionChange) (followupUnit, error) {
	u, q, err := s.store.prepareFollowupQuestion(id, c)
	if err != nil || q.Seq != 0 {
		return u, err
	}
	_, err = s.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity,
		Caller: agentCallerIdentity(id), ClientMessageID: q.ClientMessageID, Text: q.Text, FollowupNotice: true})
	if err != nil {
		return u, err
	}
	return s.store.finishFollowupQuestion(id, c.followupCommand, q.Ref)
}

func agentCallerIdentity(id MemberIdentity) Caller {
	return Caller{Author: Author{Kind: "agent", ID: id.AgentID}, MembershipID: id.MembershipID}
}

func roomHumanMessage(room RoomSnapshot, seq uint64) (RoomMessage, error) {
	for _, m := range room.Messages {
		if m.Seq == seq && validHumanEvidence(m) {
			return m, nil
		}
	}
	return RoomMessage{}, ErrIdentity
}

func addFollowupAnswer(u *followupUnit, index int, m RoomMessage, basis string, turn uint64) (bool, error) {
	q := &u.Questions[index]
	for _, a := range q.Answers {
		if a.Message.Seq == m.Seq {
			return false, nil
		}
	}
	if q.Seq == 0 || m.Seq <= q.Seq || (m.ReplyTo != 0 && m.ReplyTo != q.Seq) {
		return false, ErrIdentity
	}
	if len(q.Answers) >= 16 {
		return false, ErrCapacity
	}
	q.Answers = append(q.Answers, followupAnswer{Message: m, Basis: basis})
	u.Status, u.Next = "pending", "核对已关联用户答复的准确范围并继续办理"
	for _, other := range u.Questions {
		if len(other.Answers) == 0 {
			u.Status, u.Next = "waiting_user", other.Text
			break
		}
	}
	touchFollowup(u, turn)
	return true, nil
}

// Explicit reply_to is associated automatically on conversation reads. A plain
// answer requires an Agent's specific contextual association; it never defaults
// to the last question when there are multiple candidates.
func (s *Store) linkFollowupReply(id MemberIdentity, c followupReplyChange) (followupUnit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.conversationUnitLocked(id, c.followupCommand)
	if err != nil {
		return u, err
	}
	for _, q := range u.Questions {
		for _, a := range q.Answers {
			if a.Message.Seq == c.SourceSeq && (c.QuestionRef == "" || c.QuestionRef == q.Ref) {
				return u, nil
			}
		}
	}
	room, err := s.loadRoom()
	if err != nil {
		return u, err
	}
	m, err := roomHumanMessage(room, c.SourceSeq)
	if err != nil {
		return u, err
	}
	index := -1
	for j, q := range u.Questions {
		if (m.ReplyTo != 0 && m.ReplyTo == q.Seq && (c.QuestionRef == "" || c.QuestionRef == q.Ref)) ||
			(m.ReplyTo == 0 && c.QuestionRef == q.Ref && handlingText(c.Basis)) {
			index = j
			break
		}
	}
	if index < 0 {
		return u, conflictReason("question_ref", "answer needs an unambiguous question association", "Use matters reply only with an exact human source and clear basis.")
	}
	if u.Revision != c.Revision {
		return u, versionConflict("unit_version", c.Revision, u.Revision, "Read matters read and reassess before submitting a change.")
	}
	basis := c.Basis
	if m.ReplyTo != 0 {
		basis = "Room 显式回复关联"
	}
	if _, err := addFollowupAnswer(&u, index, m, basis, c.TurnNumber); err != nil {
		return u, err
	}
	return u, s.saveFollowupUnitLocked(id, u)
}

// Synchronize only the requested unit, never inject completed object history.
// Each answer and its unit status commit together before returning.
func (s *Store) syncFollowupReplies(id MemberIdentity, c followupCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.conversationUnitLocked(id, c)
	if err != nil {
		return err
	}
	room, err := s.loadRoom()
	if err != nil {
		return err
	}
	changed := false
	for j, q := range u.Questions {
		if q.Seq == 0 {
			receipt, ok := room.FollowupNoticeReceipts[q.ClientMessageID]
			if !ok {
				continue
			}
			if receipt.Author != (Author{Kind: "agent", ID: id.AgentID}) || receipt.MembershipID != id.MembershipID || receipt.Digest != noticeDigest(q.Text) {
				return ErrIdentity
			}
			q.Seq = receipt.Seq
			u.Questions[j].Seq = receipt.Seq
			touchFollowup(&u, c.TurnNumber)
			changed = true
		}
		for _, m := range room.Messages {
			if !validHumanEvidence(m) || m.ReplyTo != q.Seq {
				continue
			}
			added, err := addFollowupAnswer(&u, j, m, "Room 显式回复关联", c.TurnNumber)
			if err != nil {
				return err
			}
			changed = changed || added
		}
	}
	if changed {
		if err := s.saveFollowupUnitLocked(id, u); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) readFollowupConversation(_ context.Context, id MemberIdentity, c followupCommand) (followupUnit, error) {
	if err := s.store.syncFollowupReplies(id, c); err != nil {
		return followupUnit{}, err
	}
	return s.store.readFollowup(id, c.ObjectRef, c.UnitRef)
}
