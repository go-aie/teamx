package room

import (
	"reflect"
	"time"
)

// The same evidence value is available to an immediate action without creating
// an instruction. Only work that must survive a turn needs a persisted entry.
type followupReplyBasis struct {
	QuestionRef string         `json:"question_ref"`
	TargetRef   string         `json:"target_ref"`
	MessageRefs []string       `json:"message_refs,omitempty"`
	Intent      followupIntent `json:"intent"`
	Source      RoomMessage    `json:"source"`
	Basis       string         `json:"basis"`
}

type followupInstruction struct {
	Ref     string `json:"instruction_ref"`
	Version uint64 `json:"version"`
	followupReplyBasis
	Status     string       `json:"status"` // active, fulfilled, revoked
	ExpiresAt  *time.Time   `json:"expires_at,omitempty"`
	Evidence   string       `json:"evidence,omitempty"`
	Revocation *RoomMessage `json:"revocation,omitempty"`
	UpdatedAt  time.Time    `json:"updated_at"`
}

type followupInstructionChange struct {
	followupCommand
	QuestionRef string         `json:"question_ref"`
	MessageRefs []string       `json:"message_refs,omitempty"`
	SourceSeq   uint64         `json:"source_seq"`
	Intent      followupIntent `json:"intent"`
	Basis       string         `json:"basis"`
	ExpiresAt   *time.Time     `json:"expires_at,omitempty"`
}

func instructionReference(u followupUnit, b followupReplyBasis) string {
	return referenceDigest("instruction_", u.Ref, b.QuestionRef, b.Source.Seq, b.Intent.Operation)
}

func activeFollowupInstruction(i followupInstruction, now time.Time) bool {
	return i.Status == "active" && (i.ExpiresAt == nil || i.ExpiresAt.After(now))
}

func replyBasis(u followupUnit, ref string, seq uint64, intent followupIntent, basis string) (followupReplyBasis, error) {
	if !handlingText(basis) || !validFollowupIntent(intent, u.TargetRef != u.ObjectRef) {
		return followupReplyBasis{}, ErrRequest
	}
	for _, q := range u.Questions {
		if q.Ref != ref {
			continue
		}
		// A different operation, exact body or constraints cannot silently replace
		// what the user was asked. Clarification is a new question, not wider scope.
		if q.Proposed == nil || *q.Proposed != intent {
			return followupReplyBasis{}, ErrConflict
		}
		for _, a := range q.Answers {
			if a.Message.Seq == seq {
				return followupReplyBasis{q.Ref, q.TargetRef, q.MessageRefs, intent, a.Message, basis}, nil
			}
		}
	}
	return followupReplyBasis{}, ErrIdentity
}

func (s *Store) validateFollowupInstructionsLocked(id MemberIdentity, u followupUnit) error {
	seen := map[string]bool{}
	for _, i := range u.Instructions {
		if seen[i.Ref] || i.Version == 0 || i.Ref != instructionReference(u, i.followupReplyBasis) || i.UpdatedAt.IsZero() ||
			(i.Status != "active" && i.Status != "fulfilled" && i.Status != "revoked") || len(i.Evidence) > 4096 ||
			(i.Status != "active" && !handlingText(i.Evidence)) || (i.ExpiresAt != nil && i.ExpiresAt.IsZero()) {
			return ErrInvalid
		}
		if i.QuestionRef != "" {
			expected, err := replyBasis(u, i.QuestionRef, i.Source.Seq, i.Intent, i.Basis)
			if err != nil || !reflect.DeepEqual(expected, i.followupReplyBasis) {
				return ErrInvalid
			}
		} else if !validHumanEvidence(i.Source) || i.Source.ReplyTo != 0 || i.TargetRef != u.TargetRef || !validFollowupIntent(i.Intent, u.TargetRef != u.ObjectRef) || !handlingText(i.Basis) {
			return ErrInvalid
		}
		if err := s.validateQuestionTargetLocked(id, u, i.MessageRefs); err != nil {
			return err
		}
		if i.Status == "revoked" {
			if i.Revocation == nil || !validHumanEvidence(*i.Revocation) || i.Revocation.Author.ID != i.Source.Author.ID || i.Revocation.Seq <= i.Source.Seq {
				return ErrInvalid
			}
		} else if i.Revocation != nil {
			return ErrInvalid
		}
		seen[i.Ref] = true
	}
	return nil
}

func (s *Store) followupAnswerBasis(id MemberIdentity, c followupInstructionChange) (followupReplyBasis, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.conversationUnitLocked(id, c.followupCommand)
	if err != nil {
		return followupReplyBasis{}, err
	}
	if u.Revision != c.Revision {
		return followupReplyBasis{}, versionConflict("unit_version", c.Revision, u.Revision, "Read matters read and reassess the exact human answer.")
	}
	b, err := s.instructionBasisLocked(id, u, c)
	if err != nil {
		return b, err
	}
	for _, i := range u.Instructions {
		if i.Ref == instructionReference(u, b) && (!activeFollowupInstruction(i, time.Now()) || !reflect.DeepEqual(i.followupReplyBasis, b)) {
			return followupReplyBasis{}, ErrConflict
		}
	}
	return b, nil
}

func (s *Store) instructionBasisLocked(id MemberIdentity, u followupUnit, c followupInstructionChange) (followupReplyBasis, error) {
	c.MessageRefs = canonicalMessageRefs(c.MessageRefs)
	if c.QuestionRef != "" {
		if len(c.MessageRefs) != 0 {
			return followupReplyBasis{}, ErrRequest
		} // inherited from the frozen question
		return replyBasis(u, c.QuestionRef, c.SourceSeq, c.Intent, c.Basis)
	}
	if !validFollowupIntent(c.Intent, u.TargetRef != u.ObjectRef) || !handlingText(c.Basis) {
		return followupReplyBasis{}, ErrRequest
	}
	if err := s.validateQuestionTargetLocked(id, u, c.MessageRefs); err != nil {
		return followupReplyBasis{}, err
	}
	// A retry after history clear can use its already copied original evidence.
	for _, i := range u.Instructions {
		if i.QuestionRef == "" && i.Source.Seq == c.SourceSeq && i.Intent == c.Intent && reflect.DeepEqual(i.MessageRefs, c.MessageRefs) {
			b := i.followupReplyBasis
			b.Basis = c.Basis
			return b, nil
		}
	}
	room, err := s.loadRoom()
	if err != nil {
		return followupReplyBasis{}, err
	}
	m, err := roomHumanMessage(room, c.SourceSeq)
	if err != nil {
		return followupReplyBasis{}, err
	}
	if m.ReplyTo != 0 {
		return followupReplyBasis{}, ErrIdentity
	} // question replies must keep the question's scope
	return followupReplyBasis{TargetRef: u.TargetRef, MessageRefs: c.MessageRefs, Intent: c.Intent, Source: m, Basis: c.Basis}, nil
}

func (s *Store) saveFollowupInstruction(id MemberIdentity, c followupInstructionChange) (followupUnit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.conversationUnitLocked(id, c.followupCommand)
	if err != nil {
		return u, err
	}
	b, err := s.instructionBasisLocked(id, u, c)
	if err != nil {
		return u, err
	}
	ref := instructionReference(u, b)
	for _, old := range u.Instructions {
		if old.Ref == ref {
			if !reflect.DeepEqual(old.followupReplyBasis, b) || !reflect.DeepEqual(old.ExpiresAt, c.ExpiresAt) {
				return u, ErrConflict
			}
			return u, nil // never reactivate a fulfilled/revoked instruction on retry
		}
	}
	if c.Revision != u.Revision {
		return u, versionConflict("unit_version", c.Revision, u.Revision, "Read matters read and reassess before submitting a change.")
	}
	if c.ExpiresAt != nil && !c.ExpiresAt.After(time.Now()) {
		return u, ErrRequest
	}
	if len(u.Instructions) >= 32 {
		return u, ErrCapacity
	}
	u.Instructions = append(u.Instructions, followupInstruction{Ref: ref, Version: 1, followupReplyBasis: b, Status: "active", ExpiresAt: c.ExpiresAt, UpdatedAt: time.Now().UTC()})
	if u.Status != "waiting_user" {
		u.Status, u.Next = "pending", "落实已保存的准确单次指令"
	}
	touchFollowup(&u, c.TurnNumber)
	return u, s.saveFollowupUnitLocked(id, u)
}

// R4 owns action reconciliation. This status change records a verified business
// result or a human revocation, and is not itself an execution receipt.
func (s *Store) finishFollowupInstruction(id MemberIdentity, c followupCommand, ref string, version uint64, status, evidence string, sourceSeq uint64) (followupUnit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.conversationUnitLocked(id, c)
	if err != nil {
		return u, err
	}
	if !handlingText(evidence) {
		return u, invalidField("evidence", "required nonempty text, at most 4096 bytes")
	}
	if status != "fulfilled" && status != "revoked" {
		return u, invalidField("status", "must be fulfilled or revoked")
	}
	if u.Revision != c.Revision {
		return u, versionConflict("unit_version", c.Revision, u.Revision, "Read matters read and reassess before submitting a change.")
	}
	for j := range u.Instructions {
		i := &u.Instructions[j]
		if i.Ref != ref {
			continue
		}
		if i.Version != version {
			return u, versionConflict("version", version, i.Version, "Read matters read to restore the instruction before changing its status.")
		}
		if i.Status != "active" {
			return u, conflictReason("status", "instruction is no longer active", "Read matters read; do not reactivate or replay it.")
		}
		if status == "revoked" {
			// Retain the real human correction alongside the original instruction.
			room, err := s.loadRoom()
			if err != nil {
				return u, err
			}
			m, err := roomHumanMessage(room, sourceSeq)
			if err != nil {
				return u, err
			}
			if m.Author.ID != i.Source.Author.ID || m.Seq <= i.Source.Seq {
				return u, ErrIdentity
			}
			i.Revocation = &m
		} else if sourceSeq != 0 {
			return u, ErrRequest
		}
		i.Version++
		i.Status = status
		i.Evidence = evidence
		i.UpdatedAt = time.Now().UTC()
		touchFollowup(&u, c.TurnNumber)
		return u, s.saveFollowupUnitLocked(id, u)
	}
	return u, ErrRequest
}
