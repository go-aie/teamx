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
	Detail           bool   `json:"detail,omitempty"`
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
	if req.Caller.Kind == "agent" {
		switch path {
		case "/v1/matters/update":
			if req.Progress == nil || req.Followup != nil {
				return nil, invalidField("progress", "use the current matters update contract")
			}
			c := req.Progress
			q = followupRequest{followupCommand: c.followupCommand, Status: c.Status, Summary: c.Summary, Next: c.Next, Evidence: c.Evidence}
		case "/v1/matters/instruction-status":
			if req.InstructionStatus == nil || req.Followup != nil {
				return nil, invalidField("instruction_status", "use instruction_ref/instruction_version")
			}
			c := req.InstructionStatus
			q = followupRequest{followupCommand: c.followupCommand, ID: c.InstructionRef, Version: c.InstructionVersion, Status: c.Status, Evidence: c.Evidence, SourceSeq: c.SourceSeq}
		}
	}
	limit := req.Limit
	if limit == 0 {
		limit = 25
	}
	sourceAfter := uint64(0)
	if req.After != nil {
		sourceAfter = *req.After
	}
	switch path {
	case "/v1/matters/list":
		page, err := s.store.listFollowups(id, followupQuery{ObjectRef: q.ObjectRef, Cursor: q.Cursor, Limit: limit, IncludeCompleted: q.IncludeCompleted})
		if req.Caller.Kind == "agent" {
			return projectFollowupPage(page), err
		}
		return page, err
	case "/v1/matters/sources":
		return s.store.listFollowupSources(id, q.ObjectRef, sourceAfter, limit)
	case "/v1/matters/source":
		event, err := s.store.readFollowupSource(id, q.Seq, q.SourceRef)
		if err != nil {
			return nil, err
		}
		s.store.mu.Lock()
		entry, err := s.store.inboxEntryLocked(id, q.Seq)
		s.store.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if req.Caller.Kind == "agent" {
			return projectSource(event, entry), nil
		}
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
			view, err := projectAgentFollowupRead(id, result)
			if err != nil {
				return nil, err
			}
			view.Details.TurnNumber = q.TurnNumber
			if q.Detail {
				view.Detail = projectFollowupDetails(unit)
				view.Details.Omitted = []string{}
			}
			// Locators expose all source pages; native aliases and object/discussion/
			// message mapping are available through matters source without remote I/O.
			sources, err := s.store.listFollowupSources(id, unit.ObjectRef, 0, 25)
			if err != nil {
				return nil, err
			}
			view.SourcePage = &sources
			s.store.mu.Lock()
			seen := map[string]bool{}
			display := platform.TargetResolution{ObjectVerified: true, Target: &target.Target}
			err = s.store.visitFollowupEventsLocked(id, func(e followupEvent) error {
				if e.ObjectRef != unit.ObjectRef {
					return nil
				}
				r := e.Resolution
				if r.URL != "" && (display.URL == "" || r.CheckedAt.After(display.CheckedAt)) {
					display.URL, display.CheckedAt = r.URL, r.CheckedAt
				}
				for _, a := range r.Aliases {
					a = objectIdentity(a)
					key, _ := a.Key()
					if !seen[key] {
						if len(view.Target.Aliases) >= 32 {
							return ErrCapacity
						}
						seen[key] = true
						view.Target.Aliases = append(view.Target.Aliases, a)
					}
				}
				return nil
			})
			for j, a := range unit.Actions {
				if s.store.executingReferences[a.Request.key(id)] {
					view.Unit.Actions[j].Status = "processing"
				}
			}
			s.store.mu.Unlock()
			if err != nil {
				return nil, err
			}
			display.Aliases = view.Target.Aliases
			view.Target.ObjectLabel, view.Target.ObjectURL = followupObjectDisplay(display)
			return view, nil
		}
		return result, nil
	}
	if req.Caller.Kind != "agent" {
		return nil, ErrIdentity
	}
	switch path {
	case "/v1/matters/resolve":
		if _, err := s.store.readFollowupSource(id, q.Seq, q.SourceRef); err != nil {
			return nil, err
		}
		event, err := s.resolveFollowupEvent(ctx, id, q.Seq, q.TurnNumber, true, s.referenceResolver)
		if err != nil {
			return nil, err
		}
		s.store.mu.Lock()
		entry, err := s.store.inboxEntryLocked(id, q.Seq)
		s.store.mu.Unlock()
		return projectSource(event, entry), err
	case "/v1/matters/ensure":
		unit, reused, err := s.store.ensureDiscussionFollowupResult(id, q.Seq, q.TurnNumber, q.TargetRef)
		if err != nil {
			return nil, err
		}
		// ensure has no observed version. The receipt cannot update an old snapshot.
		return struct {
			UnitRef      string `json:"unit_ref"`
			ObjectRef    string `json:"object_ref"`
			UnitVersion  uint64 `json:"unit_version"`
			Status       string `json:"status"`
			RequiresRead bool   `json:"requires_read"`
			Reused       bool   `json:"reused"`
		}{unit.Ref, unit.ObjectRef, unit.Revision, unit.Status, true, reused}, nil
	}
	c := q.followupCommand
	if req.Question != nil {
		c = req.Question.followupCommand
	}
	if req.FollowupReply != nil {
		c = req.FollowupReply.followupCommand
	}
	if req.Instruction != nil {
		c = req.Instruction.followupCommand
	}
	before, err := s.store.readFollowup(id, c.ObjectRef, c.UnitRef)
	if err != nil {
		return nil, err
	}
	var after followupUnit
	switch path {
	case "/v1/matters/update":
		after, err = s.store.updateFollowup(id, followupUpdate{q.ObjectRef, q.UnitRef, q.Revision, q.TurnNumber, q.Status, q.Summary, q.Next, q.Evidence})
	case "/v1/matters/notify":
		if req.Question == nil {
			return nil, ErrRequest
		}
		return s.notifyAgentFollowup(ctx, id, *req.Question, before)
	case "/v1/matters/reply":
		if req.FollowupReply == nil {
			return nil, ErrRequest
		}
		after, err = s.store.linkFollowupReply(id, *req.FollowupReply)
	case "/v1/matters/instruct":
		if req.Instruction == nil {
			return nil, ErrRequest
		}
		after, err = s.store.saveFollowupInstruction(id, *req.Instruction)
	case "/v1/matters/instruction-status":
		after, err = s.store.finishFollowupInstruction(id, q.followupCommand, q.ID, q.Version, q.Status, q.Evidence, q.SourceSeq)
		err = agentInstructionDiagnostic(err)
	default:
		return nil, ErrRequest
	}
	if err != nil {
		return nil, err
	}
	result := projectFollowupChange(before, after, c.Revision)
	switch path {
	case "/v1/matters/instruct":
		for _, i := range after.Instructions {
			if i.Source.Seq == req.Instruction.SourceSeq && i.QuestionRef == req.Instruction.QuestionRef && i.Intent.Operation == req.Instruction.Intent.Operation {
				result.Instruction = &i
				break
			}
		}
	case "/v1/matters/instruction-status":
		for _, i := range after.Instructions {
			if i.Ref == q.ID {
				result.Instruction = &i
				break
			}
		}
	case "/v1/matters/reply":
		for _, question := range after.Questions {
			for _, answer := range question.Answers {
				if answer.Message.Seq == req.FollowupReply.SourceSeq && (req.FollowupReply.QuestionRef == "" || req.FollowupReply.QuestionRef == question.Ref) {
					result.Question = &question
					break
				}
			}
		}
	}
	return result, nil
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
