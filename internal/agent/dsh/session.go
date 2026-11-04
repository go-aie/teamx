package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"
	"unicode/utf8"
)

type turn struct {
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	permissions sync.WaitGroup
	settling    bool
	result      TurnResult // guarded by Session.mu
	eventSeq    uint64
}

type Session struct {
	opts          Options
	lock          *os.File
	process       *process
	conn          *connection
	id            string
	supportsClose bool
	mu            sync.Mutex
	active        *turn
	closing       bool
	disabled      error
	cleanupOnce   sync.Once
	cleanupDone   chan struct{}
	openDone      chan struct{}
	cleanupErr    error
	secret        string
	reverseSlots  chan struct{}
	reverseIDs    map[string]bool
	workers       sync.WaitGroup
	reference     reference
	opened        bool
}

// Open creates a fresh session; it never overwrites existing state.
func Open(ctx context.Context, opts Options) (*Session, error) {
	return open(ctx, opts, false)
}

// Resume restores the original session only after a verified normal Close.
// A failed resume never falls back to session/new or replays a prompt.
func Resume(ctx context.Context, opts Options) (*Session, error) {
	return open(ctx, opts, true)
}

func open(ctx context.Context, opts Options, resume bool) (_ *Session, openErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts, err := validate(opts)
	if err != nil {
		return nil, err
	}
	if opts.inspect == nil {
		opts.inspect = processTable
	}
	// Do not start execution if this host cannot observe process ownership.
	if _, err := opts.inspect(); err != nil {
		return nil, err
	}
	lock, ref, err := prepare(&opts, resume)
	if err != nil {
		return nil, err
	}
	s := &Session{opts: opts, lock: lock, cleanupDone: make(chan struct{}), openDone: make(chan struct{}), secret: os.Getenv(opts.Model.APIKeyEnv), reverseSlots: make(chan struct{}, 4), reverseIDs: map[string]bool{}}
	defer func() {
		if openErr != nil {
			s.startCleanup()
			cleanupCtx, cancel := context.WithTimeout(context.Background(), opts.Limits.CleanupTimeout)
			defer cancel()
			if err := s.awaitCleanup(cleanupCtx); err != nil {
				openErr = errors.Join(openErr, err)
			}
		}
	}()
	defer close(s.openDone)
	hash, err := executableDigest(opts.BinaryPath)
	if err != nil {
		return nil, err
	}
	var version string
	if opts.Artifact == (Artifact{}) {
		version, err = installedVersion(ctx, opts)
		if err != nil {
			return nil, err
		}
		if resume && ref.Artifact.Version != version {
			return nil, fmt.Errorf("%w: installed DSH version changed", ErrRecoveryUnsupported)
		}
	}
	s.process, err = startProcess(opts, s.disable)
	if err != nil {
		return nil, err
	}
	s.conn = newConnection(s.process.in, s.process.out, opts.Limits)
	s.conn.onNotification, s.conn.onRequest, s.conn.onFailure = s.notification, s.request, s.disable
	go s.conn.read()
	handshakeCtx, cancel := context.WithTimeout(ctx, opts.Limits.HandshakeTimeout)
	defer cancel()
	var init struct {
		ProtocolVersion   int               `json:"protocolVersion"`
		AuthMethods       []json.RawMessage `json:"authMethods"`
		AgentInfo         json.RawMessage   `json:"agentInfo"`
		AgentCapabilities struct {
			SessionCapabilities map[string]json.RawMessage `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	if err := s.conn.call(handshakeCtx, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "teamx", "version": "da1-02"}}, &init); err != nil {
		return nil, err
	}
	if init.ProtocolVersion != 1 || len(init.AuthMethods) != 0 {
		return nil, errors.New("unsupported ACP version or authentication requirement")
	}
	identity, err := runtimeArtifact(init.AgentInfo)
	if err != nil {
		return nil, err
	}
	if resume {
		previous, err := runtimeArtifact(ref.AgentInfo)
		if err != nil || previous != identity {
			return nil, fmt.Errorf("%w: DSH runtime name or version changed", ErrRecoveryUnsupported)
		}
	}
	s.mu.Lock()
	_, s.supportsClose = init.AgentCapabilities.SessionCapabilities["close"]
	s.mu.Unlock()
	var created struct {
		SessionID     string         `json:"sessionId"`
		ConfigOptions []configOption `json:"configOptions"`
	}
	method := "session/new"
	params := map[string]any{"cwd": opts.Workspace, "mcpServers": []any{}}
	if resume {
		if _, ok := init.AgentCapabilities.SessionCapabilities["resume"]; !ok {
			return nil, ErrRecoveryUnsupported
		}
		method, params["sessionId"] = "session/resume", ref.SessionID
		// ResumeSessionResponse has configOptions, but no sessionId.
		created.SessionID = ref.SessionID
	}
	if err := s.conn.call(handshakeCtx, method, params, &created); err != nil {
		return nil, err
	}
	if resume && created.SessionID != ref.SessionID {
		return nil, fmt.Errorf("%w: resumed session ID changed", ErrProtocol)
	}
	if created.SessionID == "" || len(created.SessionID) > 1024 {
		return nil, fmt.Errorf("%w: invalid session ID", ErrProtocol)
	}
	s.mu.Lock()
	s.id = created.SessionID
	s.mu.Unlock()
	value, err := modelValue(created.ConfigOptions, opts.Model)
	if err != nil {
		return nil, err
	}
	var selected struct {
		ConfigOptions []configOption `json:"configOptions"`
	}
	if err := s.conn.call(handshakeCtx, "session/set_config_option", map[string]any{"sessionId": s.id, "configId": "model", "value": value}, &selected); err != nil {
		return nil, err
	}
	confirmed := false
	for _, option := range selected.ConfigOptions {
		if option.ID == "model" && option.CurrentValue == value {
			confirmed = true
		}
	}
	if !confirmed {
		return nil, errors.New("DSH did not confirm configured model")
	}
	artifact := opts.Artifact
	if artifact == (Artifact{}) {
		artifact = identity
		artifact.Version = version
	}
	ref = reference{Version: 1, Artifact: artifact, BinaryPath: opts.BinaryPath, BinaryArgs: opts.BinaryArgs, BinarySHA256: hash, AgentInfo: init.AgentInfo, Workspace: opts.Workspace, SessionID: s.id, ConfigSHA256: configDigest(opts)}
	if err := atomicJSON(opts.StateDir, "backend.json", ref); err != nil {
		return nil, err
	}
	s.mu.Lock()
	disabled := s.disabled
	s.mu.Unlock()
	if disabled != nil {
		return nil, disabled
	}
	s.reference, s.opened = ref, true
	return s, nil
}

// Run rejects overlap instead of queueing. A cancelled context does not release
// the execution slot: the original prompt must settle or cleanup must start.
func (s *Session) Run(ctx context.Context, text string) (TurnResult, error) {
	if err := ctx.Err(); err != nil {
		return TurnResult{}, err
	}
	if len(text) > s.opts.Limits.InputBytes || !utf8.ValidString(text) {
		return TurnResult{}, errors.New("invalid or oversized DSH input")
	}
	s.mu.Lock()
	if s.closing || s.disabled != nil {
		s.mu.Unlock()
		return TurnResult{}, ErrUnavailable
	}
	if s.active != nil {
		s.mu.Unlock()
		return TurnResult{}, ErrBusy
	}
	turnCtx, cancel := context.WithTimeout(ctx, s.opts.Limits.TurnTimeout)
	t := &turn{ctx: turnCtx, cancel: cancel, done: make(chan struct{}), result: TurnResult{Events: []ActivityEvent{}}}
	s.active = t
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.active = nil
		close(t.done)
		s.mu.Unlock()
	}()
	pending, err := s.conn.begin(turnCtx, "session/prompt", map[string]any{"sessionId": s.id, "prompt": []map[string]string{{"type": "text", "text": text}}})
	if err != nil {
		if s.conn.health() != nil {
			return s.failTurn(t, err)
		}
		return TurnResult{}, err // no Write started, hence no dispatched prompt
	}
	var reply response
	var cancellation error
	var control context.Context
	select {
	case reply = <-pending:
	case <-turnCtx.Done():
		cancellation = turnCtx.Err()
		var stop context.CancelFunc
		control, stop = context.WithTimeout(context.Background(), s.opts.Limits.CancelTimeout)
		defer stop()
		// Anchor running descendants before cancellation can reap an
		// intermediary and reparent a detached tool between watcher ticks.
		if _, err := s.process.snapshot(); err != nil {
			return s.failTurn(t, errors.Join(cancellation, err))
		}
		if err := s.conn.notify(control, "session/cancel", map[string]string{"sessionId": s.id}); err != nil {
			return s.failTurn(t, errors.Join(cancellation, err))
		}
		select {
		case reply = <-pending:
		case <-control.Done():
			return s.failTurn(t, errors.Join(cancellation, errors.New("ACP cancellation did not settle")))
		}
	}
	if reply.err != nil {
		return s.failTurn(t, errors.Join(cancellation, reply.err))
	}
	var terminal struct {
		StopReason string `json:"stopReason"`
	}
	if json.Unmarshal(reply.result, &terminal) != nil {
		return s.failTurn(t, ErrProtocol)
	}
	switch terminal.StopReason {
	case "end_turn", "cancelled", "max_tokens", "max_turn_requests", "refusal":
	default:
		return s.failTurn(t, fmt.Errorf("%w: unknown prompt stop reason", ErrProtocol))
	}
	s.mu.Lock()
	t.settling = true
	t.result.StopReason = terminal.StopReason
	s.mu.Unlock()
	cancel() // finish/cancel pending permission responses before releasing the slot
	drained := make(chan struct{})
	go func() { t.permissions.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(s.opts.Limits.CancelTimeout):
		return s.failTurn(t, errors.New("permission handler did not settle"))
	}
	if terminal.StopReason == "cancelled" || cancellation != nil {
		if control == nil {
			var stop context.CancelFunc
			control, stop = context.WithTimeout(context.Background(), s.opts.Limits.CancelTimeout)
			defer stop()
		}
		if err := s.process.waitToolsStopped(control); err != nil {
			return s.failTurn(t, errors.Join(cancellation, err))
		}
	}
	if err := s.conn.health(); err != nil {
		return s.failTurn(t, err)
	}
	result := s.turnResult(t)
	switch terminal.StopReason {
	case "end_turn":
		return result, cancellation
	case "cancelled":
		return result, errors.Join(cancellation, ErrCancelled)
	default:
		return result, errors.Join(cancellation, &StopError{Reason: terminal.StopReason})
	}
}

func (s *Session) turnResult(t *turn) TurnResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotResult(t)
}

// Activity returns an immutable snapshot of the admitted turn, including while
// cancellation is settling. It never reads history or drives execution.
func (s *Session) Activity() (TurnResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return TurnResult{}, false
	}
	return s.snapshotResult(s.active), true
}

// Caller holds mu. Snapshots also mask substantial trailing key fragments,
// since a later chunk may complete that credential.
func (s *Session) snapshotResult(t *turn) TurnResult {
	result := t.result
	result.Text = ""
	appendDiagnostic(&result.Text, s.redactDiagnostic(t.result.Text, result.TextTruncated), s.opts.Limits.TextBytes, &result.TextTruncated)
	result.Thought = ""
	appendDiagnostic(&result.Thought, s.redactDiagnostic(t.result.Thought, result.ThoughtTruncated), s.opts.Limits.TextBytes, &result.ThoughtTruncated)
	result.Tools = slices.Clone(result.Tools)
	for i := range result.Tools {
		result.Tools[i].ID = s.redact(result.Tools[i].ID)
		result.Tools[i].Status = s.redact(result.Tools[i].Status)
		result.Tools[i].Title = s.redactDiagnostic(result.Tools[i].Title, result.Tools[i].DetailsTruncated)
		result.Tools[i].Input = s.redactDiagnostic(result.Tools[i].Input, result.Tools[i].DetailsTruncated)
		result.Tools[i].Output = s.redactDiagnostic(result.Tools[i].Output, result.Tools[i].DetailsTruncated)
	}
	boundTools(&result)
	result.Events = slices.Clone(result.Events)
	for i := range result.Events {
		event := &result.Events[i]
		event.Text = ""
		appendDiagnostic(&event.Text, s.redactDiagnostic(t.result.Events[i].Text, event.TextTruncated), min(s.opts.Limits.TextBytes, activityTextBytes), &event.TextTruncated)
		if event.Tool != nil {
			tool := *event.Tool
			tool.ID = s.redact(tool.ID)
			tool.Status = s.redact(tool.Status)
			tool.Title = s.redactDiagnostic(tool.Title, tool.DetailsTruncated)
			tool.Input = s.redactDiagnostic(tool.Input, tool.DetailsTruncated)
			tool.Output = s.redactDiagnostic(tool.Output, tool.DetailsTruncated)
			event.Tool = &tool
		}
	}
	boundActivity(&result)
	boundActivitySnapshot(&result)
	return result
}

func (s *Session) failTurn(t *turn, cause error) (TurnResult, error) {
	t.cancel()
	// Run can discover uncertainty while Close is already waiting. Unlike
	// the expected transport EOF during shutdown, this must forbid recovery.
	s.mu.Lock()
	if s.disabled == nil {
		s.disabled = cause
	}
	s.mu.Unlock()
	s.startCleanup()
	return s.turnResult(t), errors.Join(ErrUncertain, cause)
}

func (s *Session) disable(cause error) {
	s.mu.Lock()
	if !s.closing && s.disabled == nil {
		s.disabled = cause
	}
	s.mu.Unlock()
	s.startCleanup()
}

func (s *Session) startCleanup() {
	s.cleanupOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		active := s.active
		if active != nil {
			active.cancel()
		}
		s.mu.Unlock()
		go func() {
			<-s.openDone
			if active != nil {
				<-active.done
			}
			if s.conn != nil {
				s.mu.Lock()
				supportsClose, id := s.supportsClose, s.id
				s.mu.Unlock()
				if s.conn.health() == nil && supportsClose && id != "" {
					ctx, cancel := context.WithTimeout(context.Background(), s.opts.Limits.CancelTimeout)
					var result map[string]any
					s.cleanupErr = s.conn.call(ctx, "session/close", map[string]string{"sessionId": id}, &result)
					cancel()
				}
				// A normal close may dispose stdout while the close response is
				// still in flight. Transport shutdown is expected from here on.
				s.conn.fail(ErrUnavailable)
				<-s.conn.readDone
			}
			if s.process != nil {
				s.process.stop(s.opts.Limits.CancelTimeout)
			}
			s.workers.Wait()
			// Only an opened, healthy session with successful protocol close
			// and natural process exit can authorize a later normal resume.
			s.mu.Lock()
			opened, disabled := s.opened, s.disabled
			s.mu.Unlock()
			if opened && disabled != nil {
				s.cleanupErr = errors.Join(s.cleanupErr, fmt.Errorf("%w: %w", ErrRecoveryUnsupported, disabled))
			}
			healthy := opened && s.cleanupErr == nil
			if healthy && s.process != nil {
				s.process.mu.Lock()
				forced, state := s.process.forced, s.process.cmd.ProcessState
				s.process.mu.Unlock()
				switch {
				case forced:
					s.cleanupErr = fmt.Errorf("%w: process required forced cleanup", ErrRecoveryUnsupported)
				case state == nil:
					s.cleanupErr = fmt.Errorf("%w: process exit status unavailable", ErrRecoveryUnsupported)
				case !state.Success():
					s.cleanupErr = fmt.Errorf("%w: process %s", ErrRecoveryUnsupported, state)
				default:
					ref := s.reference
					ref.Stopped = true
					s.cleanupErr = atomicJSON(s.opts.StateDir, "backend.json", ref)
				}
			}
			// Physical settlement is sufficient for an explicit reset, even
			// when the old Session cannot be resumed after a failed prompt.
			settledErr := atomicJSON(s.opts.StateDir, "settled.json", struct {
				Version int `json:"version"`
			}{1})
			lockErr := s.lock.Close()
			s.cleanupErr = errors.Join(s.cleanupErr, settledErr, lockErr)
			if s.cleanupErr != nil && settledErr == nil && lockErr == nil {
				s.cleanupErr = &ClosedError{Err: s.cleanupErr}
			}
			close(s.cleanupDone)
		}()
	})
}

// Close is idempotent. Its context bounds only this caller's wait; cleanup
// continues and a later Close may wait again. The directory stays locked until
// process, protocol reader and permission workers have all stopped.
// A disabled session, forced cleanup, or abnormal exit reports unavailable
// recovery instead of silently losing resume eligibility after cleanup.
func (s *Session) Close(ctx context.Context) error {
	s.startCleanup()
	bounded, cancel := context.WithTimeout(ctx, s.opts.Limits.CleanupTimeout)
	defer cancel()
	return s.awaitCleanup(bounded)
}

func (s *Session) awaitCleanup(ctx context.Context) error {
	select {
	case <-s.cleanupDone:
		return s.cleanupErr
	default:
	}
	select {
	case <-s.cleanupDone:
		return s.cleanupErr
	case <-ctx.Done():
		return errors.Join(ErrCleanupPending, ctx.Err())
	}
}
