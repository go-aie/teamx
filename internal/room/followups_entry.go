package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/go-aie/teamx/internal/platform"
)

// The same envelope serves the Agent workflow and human management reads.
// Native IDs and caller-provided semantic unit IDs are never creation inputs.
type followupRequest struct {
	followupCommand
	TargetRef        string `json:"target_ref,omitempty"`
	SourceRef        string `json:"source_ref,omitempty"`
	Seq              uint64 `json:"seq,omitempty"`
	Cursor           string `json:"cursor,omitempty"`
	IncludeCompleted bool   `json:"include_completed,omitempty"`
	Status           string `json:"status,omitempty"`
	Summary          string `json:"summary,omitempty"`
	Next             string `json:"next,omitempty"`
	Evidence         string `json:"evidence,omitempty"`
	ID               string `json:"id,omitempty"`
	Version          uint64 `json:"version,omitempty"`
	SourceSeq        uint64 `json:"source_seq,omitempty"`
}

// Check before assembling any member, starting a Session or enabling delivery.
// Old data stays in place; no old decision is promoted to object authority.
func (s *Store) checkReferenceRoom(state RoomSnapshot) error {
	dirs, err := os.ReadDir(filepath.Join(s.dir, "agents"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		root := filepath.Join(s.dir, "agents", dir.Name())
		var h followupHeader
		err = readSnapshot(filepath.Join(root, "followups", "schema.json"), &h, followupBytes)
		if errors.Is(err, os.ErrNotExist) {
			if err := checkEmptyFollowupRoot(filepath.Join(root, "followups")); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if h.Version != followupSchema || h.RoomIdentity != state.RoomIdentity || h.MembershipID != dir.Name() {
			return fmt.Errorf("%w: unsupported followup schema", ErrVersion)
		}
	}
	return nil
}

func (s *service) followupMember(req apiRequest) (MemberIdentity, error) {
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
	if req.Caller.Kind == "agent" {
		if req.AgentID != "" || req.MembershipID != "" {
			return id, ErrIdentity
		}
		id.AgentID, id.MembershipID = req.Caller.ID, req.Caller.MembershipID
	} else if req.Caller.Kind != "human" || req.Caller.MembershipID != "" {
		return id, ErrIdentity
	}
	err := s.store.initReferenceFollowups(id)
	if err != nil {
		return id, fmt.Errorf("followup member: %w", err)
	}
	return id, nil
}

func (s *service) memberFollowups(ctx context.Context, req apiRequest, path string) (any, error) {
	id, err := s.followupMember(req)
	if err != nil {
		return nil, err
	}
	q := followupRequest{}
	if req.Followup != nil {
		q = *req.Followup
	}
	limit := req.Limit
	if limit == 0 {
		limit = 25
	}
	after := uint64(0)
	if req.After != nil {
		after = *req.After
	}
	switch path {
	case "/v1/matters/list":
		return s.store.listFollowups(id, followupQuery{ObjectRef: q.ObjectRef, Cursor: q.Cursor, Limit: limit, IncludeCompleted: q.IncludeCompleted})
	case "/v1/matters/sources":
		return s.store.listFollowupSources(id, q.ObjectRef, after, limit)
	case "/v1/matters/source":
		event, err := s.store.readFollowupSource(id, q.Seq, q.SourceRef)
		if err != nil {
			return nil, err
		}
		s.store.mu.Lock()
		entry, err := s.store.inboxEntryLocked(id, q.Seq)
		s.store.mu.Unlock()
		return struct {
			followupEvent
			Entry InboxEntry `json:"entry"`
		}{event, entry}, err
	case "/v1/matters/read":
		var unit followupUnit
		if req.Caller.Kind == "agent" {
			unit, err = s.readFollowupConversation(ctx, id, q.followupCommand)
		} else {
			unit, err = s.store.readFollowup(id, q.ObjectRef, q.UnitRef)
		}
		if err != nil {
			return nil, fmt.Errorf("read conversation: %w", err)
		}
		s.store.mu.Lock()
		target, err := s.store.loadTargetLocked(id, unit.ObjectRef, unit.TargetRef)
		s.store.mu.Unlock()
		if err != nil {
			return nil, err
		}
		actions := []platform.ActionSpec{}
		if s.referenceSpecs != nil {
			for _, spec := range s.referenceSpecs(target.Target.Provider) {
				if len(spec.ObjectTypes) > 0 && !slices.Contains(spec.ObjectTypes, target.Target.Object.Type) {
					continue
				}
				if spec.TargetKind == "object" && unit.TargetRef == unit.ObjectRef || spec.TargetKind == "discussion" && unit.TargetRef != unit.ObjectRef {
					actions = append(actions, spec)
				}
			}
		}
		result := followupReadResult{unit, target, actions}
		if req.Caller.Kind == "agent" {
			return projectAgentFollowupRead(id, result)
		}
		return result, nil
	}
	if req.Caller.Kind != "agent" {
		return nil, ErrIdentity
	}
	switch path {
	case "/v1/matters/update":
		return s.store.updateFollowup(id, followupUpdate{q.ObjectRef, q.UnitRef, q.Revision, q.TurnNumber, q.Status, q.Summary, q.Next, q.Evidence})
	case "/v1/matters/resolve":
		// Source references are checked before any remote work.
		if _, err := s.store.readFollowupSource(id, q.Seq, q.SourceRef); err != nil {
			return nil, err
		}
		return s.resolveFollowupEvent(ctx, id, q.Seq, q.TurnNumber, true, s.referenceResolver)
	case "/v1/matters/ensure":
		return s.store.ensureDiscussionFollowup(id, q.Seq, q.TurnNumber, q.TargetRef)
	case "/v1/matters/notify":
		if req.Question == nil {
			return nil, ErrRequest
		}
		return s.askFollowup(ctx, id, *req.Question)
	case "/v1/matters/reply":
		if req.FollowupReply == nil {
			return nil, ErrRequest
		}
		return s.store.linkFollowupReply(id, *req.FollowupReply)
	case "/v1/matters/instruct":
		if req.Instruction == nil {
			return nil, ErrRequest
		}
		return s.store.saveFollowupInstruction(id, *req.Instruction)
	case "/v1/matters/instruction-status":
		return s.store.finishFollowupInstruction(id, q.followupCommand, q.ID, q.Version, q.Status, q.Evidence, q.SourceSeq)
	}
	return nil, ErrRequest
}

// Keep the service caller gate and managed-runtime busy gate on one contract.
func agentRuntimePath(path string) bool {
	switch path {
	case "/v1/send", "/v1/read", "/v1/read/confirm", "/v1/inbox/read", "/v1/inbox/confirm",
		"/v1/matters/list", "/v1/matters/read", "/v1/matters/update", "/v1/matters/sources", "/v1/matters/source", "/v1/matters/resolve", "/v1/matters/ensure", "/v1/matters/notify", "/v1/matters/reply", "/v1/matters/instruct", "/v1/matters/instruction-status", "/v1/policy/read", "/v1/policy/update", "/v1/policy/publish":
		return true
	}
	return false
}

func checkEmptyFollowupRoot(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: followup records have no schema header; preserve and inspect existing evidence before switching", ErrVersion)
	}
	return nil
}
