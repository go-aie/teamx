package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// A script is a finite, comma-separated list of actions, one per turn.
// Exhaustion means silence. There is no implicit loop or reply budget to renew.
func scriptActions(script string) ([]string, error) {
	actions := strings.Split(script, ",")
	if len(actions) > 64 {
		return nil, ErrRequest
	}
	for _, action := range actions {
		switch action {
		case "read", "reply", "ignore", "fail":
		default:
			return nil, fmt.Errorf("%w: script actions are read, reply, ignore or fail", ErrRequest)
		}
	}
	return actions, nil
}

type virtualBackend struct {
	store  *Store
	script string
}

func (b virtualBackend) OpenSession(ctx context.Context, opts SessionOptions) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	actions, err := scriptActions(b.script)
	if err != nil {
		return nil, err
	}
	if opts.StatePath != filepath.Join(b.store.dir, "agents", opts.MembershipID, "session.json") {
		return nil, ErrIdentity
	}
	state, err := b.store.loadVirtualSession(opts.MemberIdentity)
	if errors.Is(err, os.ErrNotExist) {
		id, idErr := newOpaqueID()
		if idErr != nil {
			return nil, idErr
		}
		state = virtualSnapshot{Version: formatVersion, MemberIdentity: opts.MemberIdentity, SessionID: id}
	} else if err != nil {
		return nil, err
	}
	s := &virtualSession{store: b.store, opts: opts, actions: actions, state: state}
	s.event("session opened")
	if err := b.store.saveVirtualSession(s.state); err != nil {
		return nil, err
	}
	return s, nil
}

// Run and Close are serialized by the member. A task retains ownership until
// completion is adopted; discard closes it after Run has returned.
type virtualSession struct {
	store   *Store
	opts    SessionOptions
	actions []string
	state   virtualSnapshot
	closed  bool
}

func (s *virtualSession) event(event string) {
	s.state.Events = append(s.state.Events, event)
	if len(s.state.Events) > 64 {
		s.state.Events = append([]string(nil), s.state.Events[len(s.state.Events)-64:]...)
	}
}

func (s *virtualSession) Run(ctx context.Context, input UnreadChanged) (runErr error) {
	if s.closed || input.MemberIdentity != s.opts.MemberIdentity {
		return ErrIdentity
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	action := "ignore"
	if s.state.ScriptPosition < uint64(len(s.actions)) {
		action = s.actions[s.state.ScriptPosition]
	}
	s.state.Turns++
	s.state.ScriptPosition++
	s.state.LastInput = &input
	s.state.LastRead = nil
	s.state.PrivateOutput = fmt.Sprintf("%s private turn %d: %s", s.opts.AgentID, s.state.Turns, action)
	s.event("turn started")
	// Advance the script before side effects. A failed/canceled turn is never
	// automatically replayed, even if a tool succeeded before losing its reply.
	if err := s.store.saveVirtualSession(s.state); err != nil {
		return err
	}
	defer func() {
		s.event("turn ended")
		s.state.LastError = ""
		if runErr != nil {
			s.state.LastError = runErr.Error()
		}
		runErr = errors.Join(runErr, s.store.saveVirtualSession(s.state))
	}()
	if action == "fail" {
		return fmt.Errorf("script requested failure")
	}
	if action == "ignore" {
		return nil
	}
	// Body access is only through a real CLI subprocess. Five messages keep
	// both the tool output and the persisted private snapshot bounded.
	output, err := s.cli(ctx, "read", "--unread", "--limit", "5", "--json")
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(output))
	for {
		var page ReadPage
		if err := dec.Decode(&page); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		s.state.LastRead = append(s.state.LastRead, page.Messages...)
		s.state.ProcessedThrough = page.ScannedThrough
	}
	if action == "reply" && len(s.state.LastRead) != 0 {
		last := s.state.LastRead[len(s.state.LastRead)-1]
		text := []rune(last.Text)
		if len(text) > 200 {
			text = text[:200]
		}
		_, err = s.cli(ctx, "send", "--text", fmt.Sprintf("%s read %d message(s); last: %s", s.opts.AgentID, len(s.state.LastRead), string(text)),
			"--id", fmt.Sprintf("%s-turn-%d", s.state.SessionID, s.state.Turns), "--reply-to", fmt.Sprint(last.Seq), "--json")
	}
	return err
}

func (s *virtualSession) cli(ctx context.Context, args ...string) ([]byte, error) {
	toolCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(toolCtx, s.opts.CLIPath, args...)
	cmd.Env = s.opts.CLIEnv
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &boundedWriter{w: &stdout, remaining: 4 << 20}
	cmd.Stderr = &boundedWriter{w: &stderr, remaining: 32 << 10}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("CLI %s: %w: %s", args[0], err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (s *virtualSession) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.closed = true
	s.event("session closed")
	err := s.store.saveVirtualSession(s.state)
	// Removal invalidates private writes. Closing has no process left to join
	// after Run, and must not recreate files for a deleted membership.
	if errors.Is(err, ErrIdentity) {
		return nil
	}
	return err
}

// virtualSnapshot is owned by the virtual backend, separately from the Agent
// scheduler. A running Go call stack is never represented as resumable work.
type virtualSnapshot struct {
	Version int `json:"version"`
	MemberIdentity
	SessionID        string         `json:"session_id"`
	Turns            uint64         `json:"turns"`
	ScriptPosition   uint64         `json:"script_position"`
	ProcessedThrough uint64         `json:"processed_through"`
	LastInput        *UnreadChanged `json:"last_input,omitempty"`
	LastRead         []RoomMessage  `json:"last_read,omitempty"`
	PrivateOutput    string         `json:"private_output,omitempty"`
	LastError        string         `json:"last_error,omitempty"`
	Events           []string       `json:"events,omitempty"`
}

func (s virtualSnapshot) validate() error {
	if err := validateMemberHeader(s.Version, s.MemberIdentity); err != nil {
		return err
	}
	if !validText(s.SessionID) {
		return fmt.Errorf("missing session ID")
	}
	return nil
}

func (s *Store) loadVirtualSession(id MemberIdentity) (virtualSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v virtualSnapshot
	path, latest, err := s.memberPath(id, "session.json", false)
	if err != nil {
		return v, err
	}
	if err := readSnapshot(path, &v, maxPrivateFileBytes); err != nil {
		return virtualSnapshot{}, err
	}
	if err := v.validate(); err != nil {
		return virtualSnapshot{}, fmt.Errorf("%w: session.json: %w", ErrInvalid, err)
	}
	if v.MemberIdentity != id {
		return virtualSnapshot{}, ErrIdentity
	}
	if v.ProcessedThrough > latest {
		return virtualSnapshot{}, fmt.Errorf("%w: session progress exceeds room history", ErrInvalid)
	}
	return v, nil
}

func (s *Store) saveVirtualSession(v virtualSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := v.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	path, latest, err := s.memberPath(v.MemberIdentity, "session.json", true)
	if err != nil {
		return err
	}
	if v.ProcessedThrough > latest {
		return fmt.Errorf("%w: session progress exceeds room history", ErrInvalid)
	}
	return s.save(path, v, maxPrivateFileBytes)
}
