// Package room implements TeamX Room coordination, persistence and the local CLI.
package room

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform"
)

const (
	formatVersion   = 1
	maxMembers      = 32
	maxMessages     = 10_000
	maxTextBytes    = 16 << 10
	maxHistoryBytes = 64 << 20
)

// RoomIdentity survives a restart. ServiceEpoch deliberately does not belong
// to any snapshot: every service start creates a new value.
type RoomIdentity = platform.RoomIdentity

type MemberIdentity struct {
	RoomIdentity
	AgentID      string `json:"agent_id"`
	MembershipID string `json:"membership_id"`
}

// ToolIdentity is the JSON boundary for member tool requests. Actor addresses,
// tasks and contexts stay in process and never enter this or a snapshot.
type ToolIdentity struct {
	MemberIdentity
	ServiceEpoch string `json:"service_epoch"`
}

func newOpaqueID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

type Author struct {
	Kind string `json:"kind"` // human, agent or integration
	ID   string `json:"id"`
}

type RoomMessage struct {
	Seq             uint64             `json:"seq"`
	ClientMessageID string             `json:"client_message_id"`
	Author          Author             `json:"author"`
	Text            string             `json:"text"`
	Source          *IntegrationSource `json:"source,omitempty"`
	ReplyTo         uint64             `json:"reply_to,omitempty"`
	CreatedAt       time.Time          `json:"created_at"`
}

// RunnerConfig selects the service-side profile for the supported DSH backend.
type RunnerConfig struct {
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
}

func (r RunnerConfig) valid() bool { return r.Kind == "dsh" && validText(r.Profile) }

// Decode only the current runner configuration.
func (r *RunnerConfig) UnmarshalJSON(data []byte) error {
	type config RunnerConfig
	var next config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return err
	}
	*r = RunnerConfig(next)
	return nil
}

type MemberState struct {
	AgentID      string       `json:"agent_id"`
	MembershipID string       `json:"membership_id"`
	ReadThrough  uint64       `json:"read_through"`
	Runner       RunnerConfig `json:"runner"`
}

type RoomSnapshot struct {
	Version int `json:"version"`
	RoomIdentity
	IsDirect               bool                             `json:"is_direct"`
	LatestSeq              uint64                           `json:"latest_seq"`
	ClearedThrough         uint64                           `json:"cleared_through,omitempty"`
	Revision               uint64                           `json:"revision"`
	Messages               []RoomMessage                    `json:"messages"`
	Members                []MemberState                    `json:"members"`
	IntegrationReceipts    map[string]IntegrationReceipt    `json:"integration_receipts,omitempty"`
	FollowupNoticeReceipts map[string]followupNoticeReceipt `json:"followup_notice_receipts,omitempty"`
}

// Ordinary messages store their deduplication key (Author, ClientMessageID).
// Integration receipts remain authoritative after their chat bodies are cleared.
func (s RoomSnapshot) validate() error {
	if err := validateHeader(s.Version, s.RoomIdentity); err != nil {
		return err
	}
	if len(s.Messages) > maxMessages || len(s.Members) > maxMembers {
		return fmt.Errorf("room capacity exceeded")
	}
	if s.Messages == nil || s.Members == nil {
		return fmt.Errorf("missing messages or members array")
	}
	if s.IsDirect && len(s.Members) != 1 {
		return fmt.Errorf("direct conversation requires exactly one agent")
	}
	if s.ClearedThrough > s.LatestSeq || s.LatestSeq-s.ClearedThrough != uint64(len(s.Messages)) || s.Revision < s.LatestSeq {
		return fmt.Errorf("inconsistent sequence or revision")
	}
	type messageKey struct {
		Author Author
		ID     string
	}
	keys := make(map[messageKey]bool, len(s.Messages))
	bytes := 0
	for i, m := range s.Messages {
		if m.Seq != s.ClearedThrough+uint64(i+1) || m.ReplyTo >= m.Seq || m.CreatedAt.IsZero() {
			return fmt.Errorf("message %d: invalid sequence, reference or timestamp", i+1)
		}
		if (m.Author.Kind != "human" && m.Author.Kind != "agent" && m.Author.Kind != "integration") || !validText(m.Author.ID) || !validText(m.ClientMessageID) {
			return fmt.Errorf("message %d: invalid author or client ID", i+1)
		}
		if m.Author.Kind == "integration" {
			receipt, ok := s.IntegrationReceipts[m.ClientMessageID]
			if m.Source == nil || m.Author.ID != m.Source.Provider || !validIntegrationSource(*m.Source) || !ok || receipt.Seq != m.Seq || receipt.Source != *m.Source {
				return fmt.Errorf("invalid integration message")
			}
		} else if m.Source != nil {
			return fmt.Errorf("unexpected integration source")
		}
		if !validText(m.Text) || len(m.Text) > maxTextBytes {
			return fmt.Errorf("message %d: invalid text", i+1)
		}
		key := messageKey{m.Author, m.ClientMessageID}
		if keys[key] {
			return fmt.Errorf("message %d: duplicate client ID for author", i+1)
		}
		keys[key] = true
		bytes += len(m.Text)
	}
	if bytes > maxHistoryBytes {
		return fmt.Errorf("history byte capacity exceeded")
	}
	if len(s.IntegrationReceipts) > maxIntegrationReceipts {
		return ErrCapacity
	}
	if len(s.FollowupNoticeReceipts) > maxIntegrationReceipts {
		return ErrCapacity
	}
	for key, receipt := range s.FollowupNoticeReceipts {
		digest, err := hex.DecodeString(receipt.Digest)
		if !validText(key) || receipt.Seq == 0 || receipt.Seq > s.LatestSeq || receipt.Author.Kind != "agent" ||
			!validText(receipt.Author.ID) || !safeMembershipID(receipt.MembershipID) || !strings.HasPrefix(key, "followup-"+receipt.MembershipID+"-") || err != nil || len(digest) != 32 {
			return fmt.Errorf("invalid followup notice receipt")
		}
		if receipt.Seq > s.ClearedThrough {
			m := s.Messages[receipt.Seq-s.ClearedThrough-1]
			if m.ClientMessageID != key || m.Author != receipt.Author || m.ReplyTo != 0 || noticeDigest(m.Text) != receipt.Digest {
				return fmt.Errorf("followup notice receipt differs from message")
			}
		}
	}
	for id, receipt := range s.IntegrationReceipts {
		if !validText(id) || receipt.Seq == 0 || receipt.Seq > s.LatestSeq || !validIntegrationSource(receipt.Source) {
			return fmt.Errorf("invalid integration receipt")
		}
	}
	agents, memberships := map[string]bool{}, map[string]bool{}
	for _, m := range s.Members {
		if !validText(m.AgentID) || !safeMembershipID(m.MembershipID) || m.ReadThrough < s.ClearedThrough || m.ReadThrough > s.LatestSeq {
			return fmt.Errorf("invalid member identity or read cursor")
		}
		if agents[m.AgentID] || memberships[m.MembershipID] {
			return fmt.Errorf("duplicate agent or membership")
		}
		if !m.Runner.valid() {
			return fmt.Errorf("unsupported runner %q or missing DSH profile", m.Runner.Kind)
		}
		agents[m.AgentID], memberships[m.MembershipID] = true, true
	}
	return nil
}

type TurnRecord struct {
	InboxStart       uint64 `json:"inbox_start,omitempty"`
	InboxSnapshotSeq uint64 `json:"inbox_snapshot_seq,omitempty"`
	Number           uint64 `json:"number"`
	Status           string `json:"status"` // running, completed, failed, interrupted
	StartReadThrough uint64 `json:"start_read_through"`
	SnapshotSeq      uint64 `json:"snapshot_seq"`
	OfferedUnreadSeq uint64 `json:"offered_unread_seq"`
	Error            string `json:"error,omitempty"`
}

type AgentSnapshot struct {
	Version int `json:"version"`
	MemberIdentity
	LastOfferedInboxSeq     uint64      `json:"last_offered_inbox_seq,omitempty"`
	LastOfferedInboxThrough uint64      `json:"last_offered_inbox_through,omitempty"`
	LastOfferedUnreadSeq    uint64      `json:"last_offered_unread_seq"`
	TurnNumber              uint64      `json:"turn_number"`
	Turn                    *TurnRecord `json:"turn,omitempty"`
}

func (s AgentSnapshot) validate() error {
	if err := validateMemberHeader(s.Version, s.MemberIdentity); err != nil {
		return err
	}
	if s.Turn == nil {
		if s.TurnNumber != 0 || s.LastOfferedUnreadSeq != 0 || s.LastOfferedInboxSeq != 0 || s.LastOfferedInboxThrough != 0 {
			return fmt.Errorf("missing turn record")
		}
		return nil
	}
	t := s.Turn
	if t.Number == 0 || t.Number != s.TurnNumber || t.StartReadThrough > t.SnapshotSeq ||
		(t.OfferedUnreadSeq == 0 && t.InboxSnapshotSeq == 0) || t.OfferedUnreadSeq > t.SnapshotSeq || t.OfferedUnreadSeq != s.LastOfferedUnreadSeq ||
		t.InboxStart > t.InboxSnapshotSeq || (t.InboxSnapshotSeq != 0 && (t.InboxStart == t.InboxSnapshotSeq || t.InboxSnapshotSeq-t.InboxStart > inboxBatchSize || t.InboxSnapshotSeq != s.LastOfferedInboxSeq || t.InboxStart != s.LastOfferedInboxThrough)) ||
		s.LastOfferedInboxThrough > s.LastOfferedInboxSeq || (s.LastOfferedInboxSeq != 0 && s.LastOfferedInboxThrough == s.LastOfferedInboxSeq) {
		return fmt.Errorf("inconsistent turn boundaries")
	}
	switch t.Status {
	case "running", "completed", "failed", "interrupted":
		return nil
	default:
		return fmt.Errorf("invalid turn status %q", t.Status)
	}
}

func validateHeader(version int, id RoomIdentity) error {
	if version != formatVersion {
		return fmt.Errorf("%w: got %d, support %d", ErrVersion, version, formatVersion)
	}
	if !validText(id.RoomID) || !validText(id.RoomInstance) {
		return fmt.Errorf("invalid room identity")
	}
	return nil
}

func validateMemberHeader(version int, id MemberIdentity) error {
	if err := validateHeader(version, id.RoomIdentity); err != nil {
		return err
	}
	if !validText(id.AgentID) || !safeMembershipID(id.MembershipID) {
		return fmt.Errorf("invalid member identity")
	}
	return nil
}

func validText(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.ValidString(s)
}

// Membership IDs become directory names; logical agent and room names do not.
func safeMembershipID(s string) bool {
	if len(s) == 0 || len(s) > 128 || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
