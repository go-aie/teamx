package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

type adapterComponent struct {
	runPrompt func(context.Context, []dsh.ContentBlock) (dsh.TurnResult, error)
	images    bool
	run       func(context.Context, string) (dsh.TurnResult, error)
	close     func(context.Context) error
	activity  func() (dsh.TurnResult, bool)
}

func (c *adapterComponent) Activity() (dsh.TurnResult, bool) {
	if c.activity == nil {
		return dsh.TurnResult{}, false
	}
	return c.activity()
}

func (c *adapterComponent) Run(ctx context.Context, text string) (dsh.TurnResult, error) {
	return c.run(ctx, text)
}
func (c *adapterComponent) RunPrompt(ctx context.Context, prompt []dsh.ContentBlock) (dsh.TurnResult, error) {
	if c.runPrompt != nil {
		return c.runPrompt(ctx, prompt)
	}
	if len(prompt) != 1 || prompt[0].Type != "text" {
		return dsh.TurnResult{}, errors.New("text-only test adapter")
	}
	return c.Run(ctx, prompt[0].Text)
}
func (c *adapterComponent) SupportsImages() bool { return c.images }
func (c *adapterComponent) Close(ctx context.Context) error {
	if c.close != nil {
		return c.close(ctx)
	}
	return nil
}

func adapterOptions(store *Store, id MemberIdentity) SessionOptions {
	return SessionOptions{MemberIdentity: id,
		StatePath: filepath.Join(store.dir, "agents", id.MembershipID, "session.json"), CLIPath: os.Args[0],
		CLIEnv: []string{"TEAMX_ENDPOINT=http://127.0.0.1:1", "TEAMX_ROOM_ID=" + id.RoomID,
			"TEAMX_ROOM_INSTANCE=" + id.RoomInstance, "TEAMX_SERVICE_EPOCH=current-service",
			"TEAMX_AGENT_ID=" + id.AgentID, "TEAMX_MEMBERSHIP_ID=" + id.MembershipID,
			"DSH_HOME=/inherited/forbidden", "OPENAI_API_KEY=never-copy-into-tool-env"}}
}

func TestServicePassesDSHTurnTimeoutToComponent(t *testing.T) {
	dir, id := recoveryFixture(t)
	config := recoveryConfig()
	config.dshTurnTimeout = 30 * time.Minute
	config.dshProfiles = map[string]dshProfile{"fixture-fail,ignore": {Instructions: "role"}}
	svc, err := newService(dir, id.RoomID, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	agent := svc.newMember(id, RunnerConfig{Kind: "dsh", Profile: "fixture-fail,ignore"}, nil).(*MemberAgent)
	backend := agent.Backend.(dshBackend)
	opts, err := backend.componentOptions(agent.Options, dshReference{})
	if err != nil || opts.Limits.TurnTimeout != 30*time.Minute {
		t.Fatal("service timeout lost before component Open/Resume", opts.Limits.TurnTimeout, err)
	}
}

func TestDSHAdapterIsolationMetadataAndPrivateOutput(t *testing.T) {
	t.Setenv("USER", "room-owner")
	h := newRoomHarness(t, historyRoom(3, "secret message body"), nil)
	a := h.memberID("A", "generation-A")
	b := h.memberID("B", "generation-B")
	roomCall[MemberInfo](t, h, AddMember{MemberIdentity: b, Target: h.target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	var opened []dsh.Options
	for _, id := range []MemberIdentity{a, b} {
		opts := adapterOptions(h.store, id)
		backend := dshBackend{store: h.store, profile: dshProfile{Instructions: "role-" + id.AgentID,
			Workspace: "/workspace/" + id.AgentID, ToolEnv: map[string]string{"ROLE_ENV": id.AgentID}},
			open: func(_ context.Context, options dsh.Options) (dshComponent, error) {
				opened = append(opened, options)
				if strings.Count(options.Instructions, dshAgentGuide) != 1 ||
					!strings.Contains(options.Instructions, `human operator is "room-owner"`) ||
					!strings.Contains(options.Instructions, `mention "@room-owner"`) {
					t.Error("Session must receive one common guide and the operator identity")
				}
				return &adapterComponent{run: func(_ context.Context, text string) (dsh.TurnResult, error) {
					if strings.Contains(text, "secret message body") || !strings.Contains(text, "Room unread=3.") {
						t.Errorf("invalid notification: %s", text)
					}
					if strings.Contains(text, "TeamX agent guide") || strings.Contains(text, "role-"+id.AgentID) || strings.Contains(text, "room-owner") {
						t.Errorf("turn repeated fixed Session instructions: %s", text)
					}
					return dsh.TurnResult{StopReason: "end_turn", Text: "private answer"}, nil
				}}, nil
			}}
		session, err := backend.OpenSession(h.ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		input := UnreadChanged{MemberIdentity: id, Revision: 9, UnreadCount: 3, LatestSeq: 3, NewestUnreadSeq: 3}
		for range 2 {
			if err := session.Run(h.ctx, input); err != nil {
				t.Fatal(err)
			}
		}
		if err := session.Run(h.ctx, UnreadChanged{MemberIdentity: h.memberID("other", "other")}); !errors.Is(err, ErrIdentity) {
			t.Fatal(err)
		}
		if _, err := backend.OpenSession(h.ctx, opts); !errors.Is(err, dsh.ErrRecoveryUnsupported) {
			t.Fatal("replaced existing session", err)
		}
		if err := session.Close(h.ctx); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(opts.StatePath), "dsh-turn.json"))
		if err != nil || !strings.Contains(string(data), "private answer") {
			t.Fatal(string(data), err)
		}
		var diagnostic dshDiagnostic
		if err := json.Unmarshal(data, &diagnostic); err != nil || diagnostic.Result.Events == nil || len(diagnostic.Result.Events) != 0 {
			t.Fatal("empty diagnostics must use an explicit ordered event list", err)
		}
		var ref map[string]any
		if err := readSnapshot(opts.StatePath, &ref, maxPrivateFileBytes); err != nil || ref["directory"] != "session.json.dsh" || ref["sessionId"] != nil {
			t.Fatal(ref, err)
		}
	}
	if len(opened) != 2 || opened[0].StateDir == opened[1].StateDir || opened[0].Workspace == opened[1].Workspace {
		t.Fatal(opened)
	}
	for i, options := range opened {
		id := []MemberIdentity{a, b}[i]
		if !strings.Contains(options.Instructions, "role-"+id.AgentID) || !strings.Contains(options.Instructions, `"$TEAMX_CLI" read --unread`) ||
			!strings.Contains(options.Instructions, `"$TEAMX_CLI" tools list`) || !strings.Contains(options.Instructions, "Do not delegate or manage members") ||
			!strings.Contains(options.Instructions, "Tools run on the TeamX server") || !strings.Contains(options.Instructions, "Do not bypass bindings") ||
			options.ToolEnv["TEAMX_MEMBERSHIP_ID"] != id.MembershipID || options.ToolEnv["TEAMX_SERVICE_EPOCH"] != "current-service" ||
			options.ToolEnv["ROLE_ENV"] != id.AgentID || options.ToolEnv["TEAMX_CLI"] != os.Args[0] ||
			options.ToolEnv["OPENAI_API_KEY"] != "" || options.ToolEnv["DSH_HOME"] != "" || options.Permission != nil {
			t.Fatal(options)
		}
	}
	state, err := h.store.LoadRoom()
	if err != nil || state.LatestSeq != 3 || state.Members[0].ReadThrough != 0 {
		t.Fatal("ordinary output changed Room history or read cursor", state, err)
	}
}

func TestDSHAdapterSchedulingPausesFailureAndReusesSession(t *testing.T) {
	h := newRoomHarness(t, historyRoom(8, "body"), nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	var opens, closes atomic.Int32
	entered, finish := make(chan string, 4), make(chan error, 4)
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		opens.Add(1)
		return &adapterComponent{run: func(ctx context.Context, text string) (dsh.TurnResult, error) {
			entered <- text
			select {
			case err := <-finish:
				return dsh.TurnResult{StopReason: "end_turn", Text: "private"}, err
			case <-ctx.Done():
				return dsh.TurnResult{StopReason: "cancelled"}, ctx.Err()
			}
		}, close: func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Error("cleanup inherited cancelled context")
			}
			closes.Add(1)
			return nil
		}}, nil
	}}
	addr, err := h.spawn(&MemberAgent{Options: opts, Store: h.store, Room: h.addr, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	input := UnreadChanged{MemberIdentity: id, Revision: 8, LatestSeq: 8, NewestUnreadSeq: 8, UnreadCount: 8}
	for turn, failure := range []error{nil, &dsh.StopError{Reason: "max_tokens"}, nil} {
		controlMember(t, h, addr, id, "run", input, turn != 0, false)
		select {
		case <-entered:
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		}
		finish <- failure
		status := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == uint64(turn+1) })
		if failure != nil && (!status.Paused || status.Agent.Turn.Status != "failed" || !strings.Contains(status.Error, "max_tokens")) {
			t.Fatal(status)
		}
	}
	if opens.Load() != 1 {
		t.Fatal("did not reuse backend", opens.Load())
	}
	_ = addr.Stop(nil)
	if _, err := addr.Wait(h.ctx); err != nil || closes.Load() != 1 {
		t.Fatal(err, closes.Load())
	}
}

func TestDSHRestorePausesWithoutReplacingBackend(t *testing.T) {
	for _, mode := range []string{"reference", "directory", "running", "missing-all"} {
		t.Run(mode, func(t *testing.T) {
			dir, id := recoveryFixture(t)
			store := openTestStore(t, dir)
			state, _ := store.LoadRoom()
			state.Members[0].Runner = RunnerConfig{Kind: "dsh", Profile: "reviewer"}
			if err := store.SaveRoom(state); err != nil {
				t.Fatal(err)
			}
			opts := adapterOptions(store, id)
			if mode == "reference" {
				store.mu.Lock()
				err := store.save(opts.StatePath, dshReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh"}, maxPrivateFileBytes)
				store.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "directory" {
				if err := os.MkdirAll(dshStateDir(opts.StatePath), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "running" {
				if err := store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: 3,
					Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 3, OfferedUnreadSeq: 3}}); err != nil {
					t.Fatal(err)
				}
			}
			_ = store.Close()
			svc, err := newService(dir, id.RoomID, recoveryConfig()) // Missing profiles must still allow observation.
			if mode == "directory" {
				if svc != nil {
					closeService(t, svc)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Fatal("accepted an orphan DSH directory", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer closeService(t, svc)
			if err := svc.resumeMembers(context.Background()); err != nil {
				t.Fatal(err)
			}
			status := inspectMember(t, context.Background(), svc.target(id.MembershipID), id)
			if !status.Paused || status.Active || status.Error == "" {
				t.Fatal(status)
			}
			if _, err := svc.control(context.Background(), apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, Retry: true}, "run"); err == nil {
				t.Fatal("allowed replacement or missing profile")
			}
			got, err := svc.store.LoadRoom()
			if err != nil || !reflect.DeepEqual(got, state) {
				t.Fatal("changed Room recovery state", got, err)
			}
			if mode == "running" && status.Agent.Turn.Status != "interrupted" {
				t.Fatal(status)
			}
		})
	}
}

func TestDSHProfileAndRunnerConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	valid := `{"reviewer":{"binary_path":"/absolute/node","binary_args":["/absolute/bin.js"],"artifact":{"source":"fixed","version":"1"},"workspace":"/workspace","instructions":"review","model":{"protocol":"openai-completions","base_url":"https://model.invalid/v1","model_id":"model","api_key_env":"OPENAI_API_KEY"}}}`
	withoutArtifact := strings.Replace(valid, `"artifact":{"source":"fixed","version":"1"},`, "", 1)
	for _, data := range []string{valid, withoutArtifact, strings.Replace(valid, `,"version":"1"`, "", 1), strings.Replace(valid, `"/absolute/node"`, `"node"`, 1), strings.Replace(valid, `"instructions":"review"`, `"instructions":"review","tool_env":{"TEAMX_AGENT_ID":"other"}`, 1), valid + `{}`, `{"x":{"api_key":"secret"}}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := loadDSHProfiles(path)
		if (err == nil) != (data == valid || data == withoutArtifact) {
			t.Fatal(data, err)
		}
	}
	for _, runner := range []RunnerConfig{{Kind: "dsh"}, {Kind: "manual", Profile: "x"}, {Kind: "scripted", Profile: "x"}} {
		if runner.valid() {
			t.Fatal(runner)
		}
	}
	for _, data := range []string{`{"kind":"dsh","profile":"x","script":"read"}`, `{"kind":"dsh","profile":"x","unknown":1}`} {
		var runner RunnerConfig
		if err := json.Unmarshal([]byte(data), &runner); err == nil {
			t.Fatal("unknown runner field accepted", data)
		}
	}
	config := recoveryConfig()
	config.dshProfiles = map[string]dshProfile{"reviewer": {}}
	svc, err := newService(t.TempDir(), "room", config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	for _, kind := range []string{"manual", "scripted", "dsh"} {
		runner := RunnerConfig{Kind: kind, Profile: "reviewer"}
		member, err := svc.add(apiRequest{AgentID: kind, Runner: runner})
		if kind == "dsh" {
			if err != nil || member.Runner != runner {
				t.Fatal(member, err)
			}
		} else if !errors.Is(err, ErrRequest) {
			t.Fatal("legacy runner accepted", kind, err)
		}
	}
	if _, err := svc.add(apiRequest{AgentID: "bad", Runner: RunnerConfig{Kind: "dsh", Profile: "unknown"}}); !errors.Is(err, ErrRequest) {
		t.Fatal(err)
	}
}

func TestDSHOwnedCleanupRetainsMemberUntilConfirmed(t *testing.T) {
	var calls atomic.Int32
	entered, settled := make(chan struct{}), make(chan struct{})
	session := &ownedSession{Session: &dshSession{component: &adapterComponent{close: func(context.Context) error {
		if calls.Add(1) == 1 {
			return dsh.ErrCleanupPending
		}
		close(entered)
		<-settled
		return nil
	}}}}
	done := make(chan error, 1)
	go func() { done <- session.close() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not continue")
	}
	select {
	case err := <-done:
		t.Fatal("cleanup released early", err)
	default:
	}
	close(settled)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := session.close(); err != nil || calls.Load() != 2 {
		t.Fatal(err, calls.Load())
	}
}

func TestDSHReferenceFailurePreventsLaunch(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "body"), nil)
	id := h.memberID("A", "generation-A")
	h.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("disk failed") }
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		t.Error("launched before reference committed")
		return nil, nil
	}}
	if _, err := backend.OpenSession(h.ctx, adapterOptions(h.store, id)); !errors.Is(err, errDSHPaused) {
		t.Fatal(err)
	}
}

func TestDSHUncertainTurnRejectsExplicitContinuation(t *testing.T) {
	h := newRoomHarness(t, historyRoom(8, "body"), nil)
	id := h.memberID("A", "generation-A")
	var calls atomic.Int32
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
			calls.Add(1)
			return dsh.TurnResult{}, dsh.ErrUncertain
		}}, nil
	}}
	addr, err := h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	input := UnreadChanged{MemberIdentity: id, Revision: 8, LatestSeq: 8, NewestUnreadSeq: 8, UnreadCount: 8}
	controlMember(t, h, addr, id, "run", input, false, false)
	status := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	if !status.Paused || !strings.Contains(status.Error, "uncertain") {
		t.Fatal(status)
	}
	_, err = addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "run", Retry: true, State: input})
	if err == nil || calls.Load() != 1 {
		t.Fatal("uncertain session admitted another turn", err, calls.Load())
	}
}

func TestDSHFailedOpenTransfersCleanupOwnership(t *testing.T) {
	h := newRoomHarness(t, historyRoom(8, "body"), nil)
	id := h.memberID("A", "generation-A")
	var closes atomic.Int32
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{close: func(context.Context) error { closes.Add(1); return nil }}, errors.New("open failed after allocation")
	}}
	addr, err := h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	input := UnreadChanged{MemberIdentity: id, Revision: 8, LatestSeq: 8, NewestUnreadSeq: 8, UnreadCount: 8}
	controlMember(t, h, addr, id, "run", input, false, false)
	status := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	if !status.Paused || closes.Load() != 1 {
		t.Fatal(status, closes.Load())
	}
}

func TestDSHOptionalOutputFailureDoesNotChangeTerminalOutcome(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "body"), nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
			return dsh.TurnResult{StopReason: "end_turn", Text: "settled"}, nil
		}}, nil
	}}
	session, err := backend.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(filepath.Dir(opts.StatePath), "dsh-turn.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := session.Run(h.ctx, UnreadChanged{MemberIdentity: id}); err != nil {
		t.Fatal("optional output became a failed turn", err)
	}
	// A failed optional rename must not freeze subsequent Room commits.
	roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "next", Text: "next"})
}

func TestMissingCredentialsDoNotConsumeTurnOrSessionOnRun(t *testing.T) {
	profiles := managerTestProfiles(t)
	config := serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0], dshProfiles: profiles}
	openService := func(dir string) (*service, func()) {
		svc, err := newService(dir, "credentials", config)
		if err != nil {
			t.Fatal(err)
		}
		close := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := svc.close(ctx); err != nil {
				t.Error(err)
			}
		}
		t.Cleanup(close)
		return svc, close
	}
	key := config.dshProfiles["runtime"].Model.APIKeyEnv
	t.Setenv(key, "")
	dir := t.TempDir()
	svc, close := openService(dir)
	member, err := svc.add(apiRequest{AgentID: "existing", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.room.Call[PostReceipt](svc.ctx, PostMessage{RoomIdentity: svc.info.RoomIdentity,
		Caller: humanCaller, ClientMessageID: "pending", Text: "pending request"}); err != nil {
		t.Fatal(err)
	}
	req := apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}
	if _, err := svc.control(svc.ctx, req, "run"); err == nil || !strings.Contains(err.Error(), key) {
		t.Fatal("missing credential was not identified before activation", err)
	}
	resume := req
	resume.Resume = true
	if _, err := svc.control(svc.ctx, resume, "rotate"); err == nil || !strings.Contains(err.Error(), key) {
		t.Fatal("rotation with activation bypassed credential check", err)
	}
	list, err := svc.members(svc.ctx)
	if err != nil || len(list.Members) != 1 {
		t.Fatal(list, err)
	}
	m := list.Members[0]
	if m.Status != "paused" || m.Execution.Rotation != nil || m.Execution.TurnBudget.Used != 0 || m.Execution.Agent.TurnNumber != 0 || m.UnreadCount != 1 {
		t.Fatal("credential rejection consumed work", m)
	}
	for _, name := range []string{"session.json", "session.json.dsh"} {
		if _, err := os.Stat(filepath.Join(dir, "agents", member.MembershipID, name)); !os.IsNotExist(err) {
			t.Fatal("credential rejection published execution state", name, err)
		}
	}
	close()
	t.Setenv(key, "offline-test-only")
	restored, _ := openService(dir)
	list, err = restored.members(restored.ctx)
	if err != nil || list.Members[0].Status != "paused" || list.Members[0].Execution.Agent.TurnNumber != 0 || list.Members[0].UnreadCount != 1 {
		t.Fatal("unstarted member became recovery-blocked", list, err)
	}
}
