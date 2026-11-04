package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

func discussionService(t *testing.T, dir string, open func(context.Context, dsh.Options) (dshComponent, error)) *service {
	t.Helper()
	config := recoveryConfig()
	t.Setenv("TEAMX_DISCUSSION_TEST_KEY", "offline-test-only")
	config.dshProfiles = map[string]dshProfile{"reviewer": {Model: dsh.ModelConfig{APIKeyEnv: "TEAMX_DISCUSSION_TEST_KEY"}}}
	svc, err := startService(dir, "discussion", config, func(s *service, state RoomSnapshot) error {
		if err := s.loadMembers(state); err != nil {
			return err
		}
		s.memberFactory = func(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) actorx.Actor {
			member := s.newMember(id, runner, restored).(*MemberAgent)
			backend := member.Backend.(dshBackend)
			backend.open = open
			member.Backend = backend
			return member
		}
		return s.attachMembers(state)
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func discussionHTTP(t *testing.T, svc *service, path string, req apiRequest, out any) int {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	svc.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data)))
	if out != nil && json.Unmarshal(w.Body.Bytes(), out) != nil {
		t.Fatalf("%s: %s", path, w.Body.String())
	}
	return w.Code
}

func awaitDiscussion(t *testing.T, svc *service) *discussionRestart {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if status := svc.discussionRestartStatus(); status != nil && status.Status != "running" && !svc.restarting.Load() {
			return status
		}
		select {
		case <-deadline:
			t.Fatal("discussion restart did not finish", svc.discussionRestartStatus())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestDiscussionRestartSettlesWholeTeamBeforeClearing(t *testing.T) {
	var opens atomic.Int32
	entered, cancelled, release := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
	var svc *service
	svc = discussionService(t, t.TempDir(), func(_ context.Context, opts dsh.Options) (dshComponent, error) {
		generation := opens.Add(1)
		if generation > 1 && !strings.Contains(opts.Instructions, "private context has been reset") {
			t.Error("fresh session did not receive reset instructions")
		}
		return &adapterComponent{run: func(ctx context.Context, _ string) (dsh.TurnResult, error) {
			if generation == 1 {
				entered <- struct{}{}
				<-ctx.Done()
				cancelled <- struct{}{}
				<-release
				// A final already-owned effect must be cleared too. Public HTTP
				// sends are blocked while this batch owns the admission boundary.
				_, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: svc.info.RoomIdentity,
					Caller: humanCaller, ClientMessageID: "late-old-effect", Text: "old-context reply"})
				return dsh.TurnResult{StopReason: "cancelled"}, errors.Join(ctx.Err(), err)
			}
			return dsh.TurnResult{StopReason: "end_turn"}, nil
		}}, nil
	})
	defer closeService(t, svc)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var members []MemberInfo
	for _, name := range []string{"A", "B"} {
		m, err := svc.add(apiRequest{AgentID: name, Runner: RunnerConfig{Kind: "dsh", Profile: "reviewer"}})
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, m)
	}
	post := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, ClientMessageID: "old", Text: "old discussion"}
	if code := discussionHTTP(t, svc, "/v1/send", post, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	a := apiRequest{AgentID: "A", MembershipID: members[0].MembershipID}
	if _, err := svc.control(context.Background(), a, "run"); err != nil {
		t.Fatal(err)
	}
	<-entered
	oldRef, err := os.ReadFile(filepath.Join(svc.store.dir, "agents", a.MembershipID, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, RestartID: strings.Repeat("a", 32)}
	// A vanished HTTP caller does not cancel ownership of the admitted batch.
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	data, _ := json.Marshal(req)
	response := httptest.NewRecorder()
	svc.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/discussion/restart", bytes.NewReader(data)).WithContext(requestCtx))
	cancelRequest()
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	<-cancelled
	for _, path := range []string{"/v1/send", "/v1/messages/clear", "/v1/member/run", "/v1/member/add", "/v1/member/session/rotate", "/v1/read/confirm"} {
		if code := discussionHTTP(t, svc, path, post, nil); code != http.StatusConflict {
			t.Fatal("mutation admitted while resetting", path, code)
		}
	}
	var list serviceMembers
	if code := discussionHTTP(t, svc, "/v1/member/list", req, &list); code != http.StatusOK || list.Restart == nil || list.Restart.Status != "running" {
		t.Fatal("status unavailable during restart", code, list)
	}
	before, _ := svc.store.LoadRoom()
	if len(before.Messages) != 1 || before.ClearedThrough != 0 {
		t.Fatal("cleared history before old turn settled", before)
	}
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", req, nil); code != http.StatusOK {
		t.Fatal("duplicate start not idempotent", code)
	}
	close(release)
	result := awaitDiscussion(t, svc)
	if result.Status != "completed" || result.ClearedThrough != 2 || opens.Load() != 1 {
		t.Fatal(result, opens.Load())
	}
	saved, err := svc.store.LoadRoom()
	if err != nil || len(saved.Messages) != 0 || saved.ClearedThrough != 2 || saved.LatestSeq != 2 || len(saved.Members) != 2 {
		t.Fatal(saved, err)
	}
	refs := make([][]byte, 0, 2)
	for i, m := range members {
		if saved.Members[i].MembershipID != m.MembershipID || saved.Members[i].ReadThrough != 2 {
			t.Fatal("membership or cursor changed unexpectedly", saved.Members)
		}
		id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID}
		status := inspectMember(t, context.Background(), svc.target(m.MembershipID), id)
		if status.Paused || status.Active || status.Status != "idle" || status.TurnBudget.Used != uint64(1-i) {
			t.Fatal("team not resumed or budget renewed", status)
		}
		data, err := os.ReadFile(filepath.Join(svc.store.dir, "agents", m.MembershipID, "session.json"))
		var ref dshReference
		if err != nil || json.Unmarshal(data, &ref) != nil || !ref.Pending {
			t.Fatal("new session was not lazy", string(data), err)
		}
		refs = append(refs, data)
	}
	if bytes.Equal(refs[0], oldRef) {
		t.Fatal("old session reference retained")
	}
	// Retrying even after completion must preserve the already chosen sessions.
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", req, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	current, _ := os.ReadFile(filepath.Join(svc.store.dir, "agents", a.MembershipID, "session.json"))
	if !bytes.Equal(refs[0], current) {
		t.Fatal("completed restart replay rotated again")
	}
	post.ClientMessageID, post.Text = "fresh", "new discussion"
	if code := discussionHTTP(t, svc, "/v1/send", post, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	for _, m := range members {
		id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID}
		waitMember(t, context.Background(), svc.target(m.MembershipID), id, func(s MemberStatus) bool {
			return !s.Active && s.Agent.LastOfferedUnreadSeq == 3
		})
	}
	if opens.Load() != 3 {
		t.Fatal("fresh input did not open fresh sessions", opens.Load())
	}
}

func TestDiscussionRestartFailureKeepsHistoryAndTeamPaused(t *testing.T) {
	svc := discussionService(t, t.TempDir(), func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
			return dsh.TurnResult{StopReason: "end_turn"}, nil
		},
			close: func(context.Context) error { return errors.New("tool cleanup unconfirmed") }}, nil
	})
	// This test deliberately leaves a failed Session cleanup in the actor exit.
	defer func() { _ = svc.close(context.Background()) }()
	for _, name := range []string{"A", "B"} {
		if _, err := svc.add(apiRequest{AgentID: name, Runner: RunnerConfig{Kind: "dsh", Profile: "reviewer"}}); err != nil {
			t.Fatal(err)
		}
	}
	post := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, ClientMessageID: "old", Text: "retain history on failure"}
	discussionHTTP(t, svc, "/v1/send", post, nil)
	list, _ := svc.members(context.Background())
	a := list.Members[0]
	svc.control(context.Background(), apiRequest{AgentID: a.AgentID, MembershipID: a.MembershipID}, "run")
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: a.AgentID, MembershipID: a.MembershipID}
	waitMember(t, context.Background(), svc.target(a.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, RestartID: strings.Repeat("b", 32)}
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", req, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	result := awaitDiscussion(t, svc)
	if result.Status != "failed" || !strings.Contains(result.Error, "tool cleanup unconfirmed") {
		t.Fatal(result)
	}
	saved, err := svc.store.LoadRoom()
	if err != nil || saved.ClearedThrough != 0 || len(saved.Messages) != 1 {
		t.Fatal("failed rotation deleted history", saved, err)
	}
	list, _ = svc.members(context.Background())
	for _, member := range list.Members {
		if !member.Execution.Paused {
			t.Fatal("failed restart enabled member", member)
		}
	}
}

func TestDiscussionRestartRecoversSettledRateLimitFailures(t *testing.T) {
	var opens atomic.Int32
	svc := discussionService(t, t.TempDir(), func(context.Context, dsh.Options) (dshComponent, error) {
		failed := opens.Add(1) <= 2
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
			if failed {
				return dsh.TurnResult{}, errors.Join(dsh.ErrUncertain, dsh.ErrRateLimited)
			}
			return dsh.TurnResult{StopReason: "end_turn"}, nil
		}, close: func(context.Context) error {
			if failed {
				return &dsh.ClosedError{Err: dsh.ErrRecoveryUnsupported}
			}
			return nil
		}}, nil
	})
	defer closeService(t, svc)
	var members []MemberInfo
	for _, name := range []string{"A", "B"} {
		member, err := svc.add(apiRequest{AgentID: name, Runner: RunnerConfig{Kind: "dsh", Profile: "reviewer"}})
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, member)
	}
	post := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, ClientMessageID: "old", Text: "request before rate limit"}
	if code := discussionHTTP(t, svc, "/v1/send", post, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	for _, member := range members {
		if _, err := svc.control(svc.ctx, apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}, "run"); err != nil {
			t.Fatal(err)
		}
		id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		waitMember(t, svc.ctx, svc.target(member.MembershipID), id, func(s MemberStatus) bool {
			return !s.Active && s.Agent.TurnNumber == 1 && s.Status == "backend_blocked"
		})
	}
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, RestartID: strings.Repeat("d", 32)}
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", req, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	if result := awaitDiscussion(t, svc); result.Status != "completed" || result.ClearedThrough != 1 {
		t.Fatal("settled failures left the discussion stuck", result)
	}
	post.ClientMessageID, post.Text = "fresh", "new request after reset"
	if code := discussionHTTP(t, svc, "/v1/send", post, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	for _, member := range members {
		id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		status := waitMember(t, svc.ctx, svc.target(member.MembershipID), id, func(s MemberStatus) bool {
			return !s.Active && s.Agent.TurnNumber == 2
		})
		if status.Agent.Turn.Status != "completed" {
			t.Fatal("fresh session failed to run after explicit reset", status)
		}
	}
	if opens.Load() != 4 {
		t.Fatal("reset reused the failed originals", opens.Load())
	}
}

func TestDiscussionRestartAdmissionAndStaleRetries(t *testing.T) {
	svc := discussionService(t, t.TempDir(), nil)
	defer closeService(t, svc)
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, RestartID: strings.Repeat("c", 32)}
	bad := req
	bad.RestartID = ""
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", bad, nil); code != http.StatusBadRequest {
		t.Fatal(code)
	}
	bad = req
	bad.ServiceEpoch = "old"
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", bad, nil); code != http.StatusForbidden {
		t.Fatal(code)
	}
	if _, err := svc.startDiscussionRestart(apiRequest{Caller: agentCaller("A", "generation-A"), RestartID: req.RestartID}); !errors.Is(err, ErrIdentity) {
		t.Fatal("agent could restart discussion", err)
	}
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", req, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	awaitDiscussion(t, svc)
	next := req
	next.PreviousRestartID, next.RestartID = req.RestartID, strings.Repeat("d", 32)
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", next, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	awaitDiscussion(t, svc)
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", req, nil); code != http.StatusConflict {
		t.Fatal("stale retry started another restart", code)
	}
}

func TestUnfinishedDiscussionRestartPausesTeamAfterServiceRestart(t *testing.T) {
	dir := t.TempDir()
	svc := discussionService(t, dir, nil)
	m, err := svc.add(apiRequest{AgentID: "A", Runner: RunnerConfig{Kind: "dsh", Profile: "reviewer"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID}
	if _, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: svc.info.RoomIdentity, Caller: humanCaller,
		ClientMessageID: "old", Text: "old input"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: 1,
		Turn: &TurnRecord{Number: 1, Status: "completed", SnapshotSeq: 1, OfferedUnreadSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	// Simulate a batch interrupted after only part of its independent writes.
	record := discussionRestart{Version: formatVersion, RoomIdentity: svc.info.RoomIdentity, ID: strings.Repeat("e", 32), Status: "running", Phase: "rotating"}
	if err := svc.saveDiscussionRestart(record); err != nil {
		t.Fatal(err)
	}
	closeService(t, svc)
	restored := discussionService(t, dir, nil)
	defer closeService(t, restored)
	if result := restored.discussionRestartStatus(); result == nil || result.Status != "failed" {
		t.Fatal(result)
	}
	// Force an otherwise eligible automatic resume to exercise the batch guard.
	restored.recovered[0].resume = true
	if err := restored.resumeMembers(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := inspectMember(t, context.Background(), restored.target(m.MembershipID), id)
	if !status.Paused || status.Active {
		t.Fatal("partially reset team resumed after restart", status)
	}
}

func TestDiscussionRestartClearFailureCanBeRetried(t *testing.T) {
	svc := discussionService(t, t.TempDir(), nil)
	defer closeService(t, svc)
	m, err := svc.add(apiRequest{AgentID: "A", Runner: RunnerConfig{Kind: "dsh", Profile: "reviewer"}})
	if err != nil {
		t.Fatal(err)
	}
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, ClientMessageID: "old", Text: "retain on disk failure", RestartID: strings.Repeat("f", 32)}
	if code := discussionHTTP(t, svc, "/v1/send", req, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	svc.store.mu.Lock()
	createTemp := svc.store.ops.createTemp
	svc.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		file, err := createTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		return discussionClearFailureFile{file}, nil
	}
	svc.store.mu.Unlock()
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", req, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	failed := awaitDiscussion(t, svc)
	if failed.Status != "failed" || failed.Phase != "clearing" || !strings.Contains(failed.Error, "clear disk failure") {
		t.Fatal(failed)
	}
	saved, _ := svc.store.LoadRoom()
	if len(saved.Messages) != 1 {
		t.Fatal("clear failure lost public history", saved)
	}
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID}
	if status := inspectMember(t, context.Background(), svc.target(m.MembershipID), id); !status.Paused {
		t.Fatal("clear failure resumed member", status)
	}
	svc.store.mu.Lock()
	svc.store.ops.createTemp = createTemp
	svc.store.mu.Unlock()
	req.PreviousRestartID, req.RestartID = req.RestartID, strings.Repeat("0", 32)
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", req, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	if result := awaitDiscussion(t, svc); result.Status != "completed" {
		t.Fatal(result)
	}
}

type discussionClearFailureFile struct{ snapshotFile }

func (f discussionClearFailureFile) Write(data []byte) (int, error) {
	// Fail the Room payload before replacement, leaving the restart journal
	// and independently committed Session references writable for recovery.
	if bytes.Contains(data, []byte(`"messages":`)) {
		return 0, errors.New("clear disk failure")
	}
	return f.snapshotFile.Write(data)
}

func TestDiscussionRestartDoesNotRenewExhaustedBudget(t *testing.T) {
	svc := discussionService(t, t.TempDir(), func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
			return dsh.TurnResult{StopReason: "end_turn"}, nil
		}}, nil
	})
	defer closeService(t, svc)
	svc.maxTurns = 1
	m, err := svc.add(apiRequest{AgentID: "A", Runner: RunnerConfig{Kind: "dsh", Profile: "reviewer"}})
	if err != nil {
		t.Fatal(err)
	}
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, ClientMessageID: "old", Text: "old input", RestartID: strings.Repeat("1", 32)}
	if code := discussionHTTP(t, svc, "/v1/send", req, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	if _, err := svc.control(context.Background(), apiRequest{AgentID: m.AgentID, MembershipID: m.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitMember(t, ctx, svc.target(m.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	if code := discussionHTTP(t, svc, "/v1/discussion/restart", req, nil); code != http.StatusOK {
		t.Fatal(code)
	}
	result := awaitDiscussion(t, svc)
	if result.Status != "completed" || len(result.PausedMembers) != 1 || result.PausedMembers[0] != "A" {
		t.Fatal(result)
	}
	status := inspectMember(t, ctx, svc.target(m.MembershipID), id)
	if !status.Paused || status.Status != "budget_exhausted" || status.TurnBudget.Used != 1 || status.Agent.TurnNumber != 1 {
		t.Fatal("restart renewed budget or turn count", status)
	}
}
