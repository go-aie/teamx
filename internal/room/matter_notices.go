package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type MatterNotice struct {
	ID              string         `json:"id"`
	Kind            string         `json:"kind"` // question, result, alert
	ProgressVersion uint64         `json:"progress_version"`
	Text            string         `json:"text"`
	ClientMessageID string         `json:"client_message_id"`
	Seq             uint64         `json:"seq,omitempty"` // zero: prepared, no durable Room receipt saved here yet
	TurnNumber      uint64         `json:"turn_number"`
	Answers         []MatterAnswer `json:"answers,omitempty"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type MatterAnswer struct {
	Message RoomMessage `json:"message"`
	Basis   string      `json:"basis"` // explicit correlation, never an inferred standing rule
}

type NoticeChange struct {
	MatterCommand
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type MatterReplyChange struct {
	MatterCommand
	QuestionID string `json:"question_id"`
	SourceSeq  uint64 `json:"source_seq"`
	Basis      string `json:"basis"`
}

// Room receipts persist independently of its visible history. This closes the
// Room commit -> member receipt gap even if chat is cleared before recovery.
type MatterNoticeReceipt struct {
	Seq          uint64 `json:"seq"`
	Author       Author `json:"author"`
	MembershipID string `json:"membership_id"`
	Digest       string `json:"digest"`
}

func noticeDigest(text string) string {
	b := sha256.Sum256([]byte(text))
	return hex.EncodeToString(b[:])
}

func matterNoticeID(id MemberIdentity, seq uint64, matter, notice string) string {
	// Both local IDs can contain hyphens; concatenation would alias a-b/c
	// with a/b-c. Length-prefix before hashing the opaque local pair.
	return fmt.Sprintf("matter-%s-%d-%s", id.MembershipID, seq, noticeDigest(fmt.Sprintf("%d:%s%d:%s", len(matter), matter, len(notice), notice)))
}

func validateNotices(m Matter) error {
	seen := map[string]bool{}
	for _, n := range m.Notices {
		if !safeMembershipID(n.ID) || seen[n.ID] || (n.Kind != "question" && n.Kind != "result" && n.Kind != "alert") ||
			!handlingText(n.Text) || !validText(n.ClientMessageID) || len(n.ClientMessageID) > 512 || n.TurnNumber == 0 || n.UpdatedAt.IsZero() ||
			n.ProgressVersion > m.ProgressVersion || len(n.Answers) > 16 {
			return ErrInvalid
		}
		seen[n.ID] = true
		answers := map[uint64]bool{}
		for _, a := range n.Answers {
			msg := a.Message
			if n.Kind != "question" || n.Seq == 0 || msg.Seq <= n.Seq || msg.Author.Kind != "human" || !validText(msg.Author.ID) ||
				!validText(msg.Text) || len(msg.Text) > maxTextBytes || msg.CreatedAt.IsZero() || !handlingText(a.Basis) || answers[msg.Seq] ||
				(msg.ReplyTo != 0 && msg.ReplyTo != n.Seq) {
				return ErrInvalid
			}
			answers[msg.Seq] = true
		}
	}
	return nil
}

func (s *Store) prepareMatterNotice(id MemberIdentity, c NoticeChange) (MatterRecord, MatterNotice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, r, i, err := s.handlingMatterLocked(id, c.MatterCommand)
	if err != nil {
		return r, MatterNotice{}, err
	}
	if !safeMembershipID(c.ID) || !handlingText(c.Text) || (c.Kind != "question" && c.Kind != "result" && c.Kind != "alert") {
		return r, MatterNotice{}, ErrRequest
	}
	m := &r.Matters[i]
	for _, n := range m.Notices {
		if n.ID == c.ID {
			if n.Kind != c.Kind || n.Text != c.Text {
				return r, n, ErrConflict
			}
			return r, n, nil
		}
		if (c.Kind == "question" && n.Kind == "question" && len(n.Answers) == 0) ||
			(n.Kind == c.Kind && n.ProgressVersion == m.ProgressVersion) {
			return r, n, ErrConflict
		}
	}
	if err := s.checkMatterPrimaryLocked(id, MatterRef{Seq: c.Seq, ID: c.MatterID}); err != nil {
		return r, MatterNotice{}, err
	}
	if len(m.Notices) >= 32 {
		return r, MatterNotice{}, ErrCapacity
	}
	if c.Kind == "question" {
		if !strings.Contains(c.Text, "@"+humanOperator()) {
			return r, MatterNotice{}, ErrRequest
		}
		m.Status, m.Next = "waiting_user", c.Text
	}
	n := MatterNotice{ID: c.ID, Kind: c.Kind, ProgressVersion: m.ProgressVersion, Text: c.Text,
		ClientMessageID: matterNoticeID(id, c.Seq, c.MatterID, c.ID), TurnNumber: c.TurnNumber, UpdatedAt: time.Now().UTC()}
	m.Notices = append(m.Notices, n)
	touchMatter(m, c.TurnNumber, false)
	err = s.save(s.matterPath(e), r, matterRecordBytes)
	return r, n, err
}

func (s *Store) finishMatterNotice(id MemberIdentity, c NoticeChange, n MatterNotice, receipt PostReceipt) (MatterRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Another human decision may change the matter while Room commits. Merge
	// only this frozen receipt; never overwrite the rest of the latest record.
	if _, err := s.runningTurnLocked(id, c.TurnNumber); err != nil {
		return MatterRecord{}, err
	}
	e, err := s.inboxEntryLocked(id, c.Seq)
	if err != nil {
		return MatterRecord{}, err
	}
	r, err := s.loadMatterLocked(e)
	if err != nil {
		return r, err
	}
	if receipt.RoomIdentity != id.RoomIdentity || receipt.Seq == 0 || receipt.ClientMessageID != n.ClientMessageID {
		return r, ErrIdentity
	}
	for i := range r.Matters {
		m := &r.Matters[i]
		if m.ID != c.MatterID {
			continue
		}
		for j := range m.Notices {
			stored := &m.Notices[j]
			if stored.ID != n.ID {
				continue
			}
			if stored.ClientMessageID != n.ClientMessageID || stored.Text != n.Text || (stored.Seq != 0 && stored.Seq != receipt.Seq) {
				return r, ErrConflict
			}
			if stored.Seq == receipt.Seq {
				return r, nil
			}
			stored.Seq, stored.UpdatedAt = receipt.Seq, time.Now().UTC()
			touchMatter(m, c.TurnNumber, false)
			return r, s.save(s.matterPath(e), r, matterRecordBytes)
		}
	}
	return r, ErrConflict
}

func (s *service) notifyMatter(ctx context.Context, id MemberIdentity, c NoticeChange) (MatterRecord, error) {
	r, n, err := s.store.prepareMatterNotice(id, c)
	if err != nil || n.Seq != 0 {
		return r, err
	}
	// No Store/inbox/service mutex is held across actor calls.
	receipt, err := s.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: Caller{Author: Author{Kind: "agent", ID: id.AgentID}, MembershipID: id.MembershipID},
		ClientMessageID: n.ClientMessageID, Text: n.Text, MatterNotice: true})
	if err != nil {
		return r, err
	}
	return s.store.finishMatterNotice(id, c, n, receipt)
}

func (s *Store) linkMatterReply(id MemberIdentity, c MatterReplyChange) (MatterRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, r, i, err := s.handlingMatterLocked(id, c.MatterCommand)
	if err != nil {
		return r, err
	}
	if !safeMembershipID(c.QuestionID) || c.SourceSeq == 0 || !handlingText(c.Basis) {
		return r, ErrRequest
	}
	m := &r.Matters[i]
	for j := range m.Notices {
		n := &m.Notices[j]
		if n.ID != c.QuestionID || n.Kind != "question" || n.Seq == 0 {
			continue
		}
		for _, a := range n.Answers {
			if a.Message.Seq == c.SourceSeq {
				return r, nil
			}
		}
		if len(n.Answers) >= 16 {
			return r, ErrCapacity
		}
		room, err := s.loadRoom()
		if err != nil {
			return r, err
		}
		for _, msg := range room.Messages {
			if msg.Seq != c.SourceSeq {
				continue
			}
			if msg.Author.Kind != "human" || msg.Seq <= n.Seq || (msg.ReplyTo != 0 && msg.ReplyTo != n.Seq) {
				return r, ErrIdentity
			}
			n.Answers = append(n.Answers, MatterAnswer{Message: msg, Basis: c.Basis})
			if m.Status == "waiting_user" {
				m.Status, m.Next = "pending", "核对关联用户回复的准确范围，继续本事项；含糊回复不扩权"
			}
			touchMatter(m, c.TurnNumber, true)
			return r, s.save(s.matterPath(e), r, matterRecordBytes)
		}
		return r, ErrIdentity
	}
	return r, ErrRequest
}
