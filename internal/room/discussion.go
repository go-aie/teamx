package room

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// One retained batch per service. Its durable marker prevents a partially
// rotated team from automatically resuming after a service restart. Session
// references and Room history remain independently committed records.
type discussionRestart struct {
	Version int `json:"version"`
	RoomIdentity
	ID             string   `json:"id"`
	Status         string   `json:"status"` // running, completed, failed
	Phase          string   `json:"phase"`
	Error          string   `json:"error,omitempty"`
	ClearedThrough uint64   `json:"cleared_through"`
	PausedMembers  []string `json:"paused_members,omitempty"` // exhausted budgets
}

func (s *service) discussionRestartStatus() *discussionRestart {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.restart == nil {
		return nil
	}
	copy := *s.restart
	copy.PausedMembers = append([]string(nil), copy.PausedMembers...)
	return &copy
}

func (s *service) loadDiscussionRestart() error {
	var record discussionRestart
	err := readSnapshot(filepath.Join(s.store.dir, "discussion-restart.json"), &record, maxPrivateFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load discussion restart: %w", err)
	}
	if record.Version != formatVersion || record.RoomIdentity != s.info.RoomIdentity || !validRestartID(record.ID) ||
		(record.Status != "running" && record.Status != "completed" && record.Status != "failed") {
		return fmt.Errorf("%w: discussion restart record", ErrInvalid)
	}
	if record.Status == "running" {
		record.Status, record.Error = "failed", "service stopped during discussion restart; inspect members and retry; team remains paused"
	}
	s.restart = &record
	return nil
}

func validRestartID(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}

func (s *service) saveDiscussionRestart(record discussionRestart) error {
	s.store.mu.Lock()
	err := s.store.check(true)
	if err == nil {
		err = s.store.save(filepath.Join(s.store.dir, "discussion-restart.json"), record, maxPrivateFileBytes)
	}
	s.store.mu.Unlock()
	if err == nil {
		s.mu.Lock()
		s.restart = &record
		s.mu.Unlock()
	}
	return err
}

func (s *service) startDiscussionRestart(req apiRequest) (*discussionRestart, error) {
	if req.Caller.Kind != "human" {
		return nil, ErrIdentity
	}
	if !validRestartID(req.RestartID) {
		return nil, ErrRequest
	}
	s.changes.Lock()
	defer s.changes.Unlock()
	if s.closing.Load() {
		return nil, ErrClosing
	}
	current := s.discussionRestartStatus()
	previousID := ""
	if current != nil {
		if current.ID == req.RestartID {
			return current, nil // A lost reply must never rotate the team twice.
		}
		previousID = current.ID
	}
	if req.PreviousRestartID != previousID {
		return nil, fmt.Errorf("%w: discussion restart changed; refresh before starting another", ErrConflict)
	}
	if !s.management.TryLock() {
		return nil, ErrBusy
	}
	owned := false
	defer func() {
		if !owned {
			s.management.Unlock()
		}
	}()
	list, err := s.room.Call[MemberList](s.ctx, ListMembers{s.info.RoomIdentity})
	if err != nil {
		return nil, err
	}
	if list.Committing {
		return nil, ErrBusy
	}
	if list.WriteError != "" {
		return nil, fmt.Errorf("room requires recovery before restarting discussion: %s", list.WriteError)
	}
	for _, member := range list.Members {
		if _, ok := s.dshProfiles[member.Runner.Profile]; !ok || s.target(member.MembershipID).IsZero() {
			return nil, fmt.Errorf("%w: member %s is unavailable for session rotation", ErrRequest, member.AgentID)
		}
	}
	record := discussionRestart{Version: formatVersion, RoomIdentity: s.info.RoomIdentity,
		ID: req.RestartID, Status: "running", Phase: "pausing"}
	if err := s.saveDiscussionRestart(record); err != nil {
		return nil, err
	}
	s.restarting.Store(true)
	owned = true
	go s.runDiscussionRestart(record, list.Members, req.Caller)
	return &record, nil
}

func (s *service) runDiscussionRestart(record discussionRestart, members []MemberInfo, caller Caller) {
	defer s.management.Unlock()
	defer s.restarting.Store(false)
	memberRequest := func(member MemberInfo) apiRequest {
		return apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID, Cancel: true}
	}
	pause := func() error {
		var result error
		for _, member := range members {
			_, err := s.control(s.ctx, memberRequest(member), "pause")
			result = errors.Join(result, err)
		}
		return result
	}
	runErr := func() error {
		if err := pause(); err != nil {
			return err
		}
		record.Phase = "rotating"
		if err := s.saveDiscussionRestart(record); err != nil {
			return err
		}
		// Ask every actor to rotate before waiting, so slow cleanup in one
		// member does not prevent cancellation/cleanup of the others.
		for _, member := range members {
			if _, err := s.control(s.ctx, memberRequest(member), "rotate"); err != nil {
				return fmt.Errorf("rotate %s: %w", member.AgentID, err)
			}
		}
		for _, member := range members {
			id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
			for {
				if s.closing.Load() {
					return ErrClosing
				}
				status, err := s.target(member.MembershipID).Call[MemberStatus](s.ctx, InspectMember{id})
				if err != nil {
					return fmt.Errorf("inspect rotation %s: %w", member.AgentID, err)
				}
				if status.Rotation == nil {
					return fmt.Errorf("rotation %s has no status", member.AgentID)
				}
				if status.Rotation.Status == "failed" {
					return fmt.Errorf("rotate %s: %s", member.AgentID, status.Rotation.Error)
				}
				if status.Rotation.Status == "completed" {
					break
				}
				select {
				case <-s.ctx.Done():
					return s.ctx.Err()
				case <-time.After(20 * time.Millisecond):
				}
			}
		}
		record.Phase = "clearing"
		if err := s.saveDiscussionRestart(record); err != nil {
			return err
		}
		// All old turns have joined. Include their final messages, rather than
		// leaving old-context replies in the newly started discussion.
		list, err := s.room.Call[MemberList](s.ctx, ListMembers{s.info.RoomIdentity})
		if err != nil {
			return err
		}
		receipt, err := s.room.Call[ClearReceipt](s.ctx, ClearMessages{RoomIdentity: s.info.RoomIdentity, Caller: caller, ThroughSeq: list.LatestSeq})
		if err != nil {
			return err
		}
		record.ClearedThrough = receipt.ClearedThrough
		record.Phase = "resuming"
		if err := s.saveDiscussionRestart(record); err != nil {
			return err
		}
		for _, member := range members {
			_, err := s.control(s.ctx, memberRequest(member), "run")
			if errors.Is(err, ErrTurnBudget) {
				record.PausedMembers = append(record.PausedMembers, member.AgentID)
			} else if err != nil {
				return fmt.Errorf("resume %s: %w", member.AgentID, err)
			}
		}
		record.Status, record.Phase = "completed", "completed"
		return s.saveDiscussionRestart(record)
	}()
	if runErr != nil {
		// No rollback across independently committed Session references. Keep
		// every member paused and expose the partial result for explicit retry.
		runErr = errors.Join(runErr, pause())
		record.Status, record.Error = "failed", runErr.Error()
		if err := s.saveDiscussionRestart(record); err != nil {
			record.Error = errors.Join(runErr, err).Error()
			s.mu.Lock()
			s.restart = &record
			s.mu.Unlock()
		}
	}
}
