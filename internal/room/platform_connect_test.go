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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func connectFixture(t *testing.T) (*roomManager, *service, MemberState, platformConnectRequest) {
	t.Helper()
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	r := managerCreate(t, m, "connect")
	_, snap := readManagedRoom(t, m, r)
	svc := eventLiveService(t, &eventHarness{m: m, room: r})
	source, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: r.RoomIdentity, Caller: humanCaller, ClientMessageID: "connect-source", Text: "连接飞书，名称为工作飞书"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := newOpaqueID()
	req := platformConnectRequest{ServiceInfo: svc.info, Caller: agentCaller(snap.Members[0].AgentID, snap.Members[0].MembershipID), OperationID: id, SourceSeq: source.Seq, Input: &platformConnectInput{Provider: "lark", Name: "工作飞书"}}
	return m, svc, snap.Members[0], req
}
func connectHTTP(t *testing.T, m *roomManager, action string, req platformConnectRequest) platformConnectOperation {
	t.Helper()
	w := managerHTTP(m, "/v1/platforms/connect/"+action, req)
	var op platformConnectOperation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &op) != nil {
		t.Fatal(action, w.Code, w.Body.String())
	}
	return op
}
func connectPending(t *testing.T, m *roomManager, svc *service, req platformConnectRequest) (platformConnectOperation, *larkAuthSession) {
	t.Helper()
	snapshot, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	source, err := roomHumanMessage(snapshot, req.SourceSeq)
	if err != nil {
		t.Fatal(err)
	}
	platformID, _ := newOpaqueID()
	op := platformConnectOperation{Schema: 1, ID: req.OperationID, MemberIdentity: MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}, Caller: req.Caller, Source: source, Input: *req.Input, Status: "running", Stage: "application", PlatformID: platformID, Application: onboardingStep{Status: "pending"}, Authorization: onboardingStep{Status: "pending"}, Connection: onboardingStep{Status: "pending"}, Interaction: platformConnectInteraction{ID: req.OperationID, Kind: "lark_authorization"}, CreatedAt: time.Now().UTC()}
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx
	s := &larkAuthSession{larkAuthView: larkAuthView{SessionID: op.ID, Status: "starting", Stage: "application"}, owner: source.Author.ID, setup: true, operationID: op.ID, cancel: cancel, done: make(chan struct{}), expires: time.Now().Add(time.Minute)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = m.savePlatformConnectLocked(&op); err != nil {
		t.Fatal(err)
	}
	if m.larkAuthSessions == nil {
		m.larkAuthSessions = map[string]*larkAuthSession{}
	}
	m.larkAuthSessions[s.SessionID] = s
	t.Cleanup(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		select {
		case <-s.done:
		default:
			close(s.done)
		}
	})
	return op, s
}
func connectValidation() platform.Validation {
	return platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), Username: "Verified user", Account: platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: lark.Origin("feishu"), Tenant: platform.ExternalID{Namespace: "tenant_key", Value: "tenant"}}, ID: platform.ExternalID{Namespace: "open_id", Value: "ou_reader"}}}
}
func connectSucceed(t *testing.T, m *roomManager, s *larkAuthSession) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	s.profile = platform.CLIProfile{Version: 1, Executable: "/synthetic/lark-cli", Profile: "teamx-feishu-fixture", Brand: "feishu", CLIVersion: lark.CLIVersion}
	s.ConfigurationFound = true
	s.Status, s.Stage = "succeeded", "complete"
	s.validation = connectValidation()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	m.finishPlatformConnectLocked(s)
	if s.connectErr != nil {
		t.Fatal(s.connectErr)
	}
}
func TestPlatformConnectSavedReceiptIdempotenceAndReadOnlyStatus(t *testing.T) {
	m, svc, _, req := connectFixture(t)
	_, s := connectPending(t, m, svc, req)
	connectSucceed(t, m, s)
	query := req
	query.Input = nil
	query.SourceSeq = 0
	op := connectHTTP(t, m, "status", query)
	if op.Status != "completed" || op.Connection.Status != "connected" || op.Authorization.Status != "applied" || op.Application.Status != "applied" || op.ResultVersion != 1 {
		t.Fatal(op)
	}
	before, err := os.ReadFile(m.connectPath(op.ID))
	if err != nil {
		t.Fatal(err)
	}
	replay := connectHTTP(t, m, "start", req)
	if !reflect.DeepEqual(op, replay) {
		t.Fatal("start replay changed receipt")
	}
	query.OperationID, query.SourceSeq = "", req.SourceSeq
	if bySource := connectHTTP(t, m, "status", query); !reflect.DeepEqual(bySource, op) {
		t.Fatal("lost-ID lookup changed operation")
	}
	after, _ := os.ReadFile(m.connectPath(op.ID))
	if !bytes.Equal(before, after) {
		t.Fatal("status or replay rewrote operation")
	}
	req.Input = &platformConnectInput{Provider: "lark", Name: "different"}
	if w := managerHTTP(m, "/v1/platforms/connect/start", req); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	req.Input = &replay.Input
	req.OperationID, _ = newOpaqueID()
	if w := managerHTTP(m, "/v1/platforms/connect/start", req); w.Code != 409 {
		t.Fatal("changed-ID replay admitted", w.Code)
	}
	query.Caller.MembershipID = "replacement"
	if w := managerHTTP(m, "/v1/platforms/connect/status", query); w.Code == 200 {
		t.Fatal("replacement inherited operation")
	}
	file, err := m.platforms.Load()
	if err != nil || len(file.Platforms) != 1 {
		t.Fatal("duplicate connection", err)
	}
	m.mu.Lock()
	bindings, err := m.readToolBindingsLocked(op.RoomIdentity)
	m.mu.Unlock()
	if err != nil || len(bindings.Bindings) != 0 {
		t.Fatal("connection enabled tools", err)
	}
	if strings.Contains(string(after), "authorization_url") || strings.Contains(string(after), "session_id") {
		t.Fatal("temporary authorization persisted")
	}
}
func TestPlatformConnectCancelledLateSuccessPreservesPartialEffects(t *testing.T) {
	m, svc, _, req := connectFixture(t)
	_, s := connectPending(t, m, svc, req)
	query := req
	query.Input = nil
	query.SourceSeq = 0
	originalCancel := s.cancel
	s.cancel = func() { originalCancel(); go connectSucceed(t, m, s) }
	late := connectHTTP(t, m, "cancel", query)
	if late.Status != "cancelled" || late.ResultVersion != 1 || late.Application.Status != "applied" || late.Authorization.Status != "applied" || late.Connection.Status == "connected" {
		t.Fatal("late success revived or lost effects", late)
	}
	file, err := m.platforms.Load()
	if err != nil || len(file.Platforms) != 0 {
		t.Fatal("cancelled operation saved connection", err)
	}
	s.cancel = originalCancel
	again := connectHTTP(t, m, "cancel", query)
	if !reflect.DeepEqual(late, again) {
		t.Fatal("duplicate cancellation changed receipt")
	}
}
func TestPlatformConnectExpiryFailureAndUnknownRecovery(t *testing.T) {
	for _, status := range []string{"expired", "failed"} {
		t.Run(status, func(t *testing.T) {
			m, svc, _, req := connectFixture(t)
			_, s := connectPending(t, m, svc, req)
			m.mu.Lock()
			s.Status, s.ErrorCode = status, "timeout"
			close(s.done)
			m.finishPlatformConnectLocked(s)
			m.mu.Unlock()
			req.Input = nil
			req.SourceSeq = 0
			op := connectHTTP(t, m, "status", req)
			if op.Status != status || op.Connection.Status == "connected" {
				t.Fatal(op)
			}
		})
	}
	t.Run("restart", func(t *testing.T) {
		m, svc, _, req := connectFixture(t)
		_, s := connectPending(t, m, svc, req)
		// Simulate loss of the process owner: recovery reads durable results only.
		m.mu.Lock()
		close(s.done)
		delete(m.larkAuthSessions, s.SessionID)
		err := m.recoverPlatformConnectLocked()
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		req.Input = nil
		req.SourceSeq = 0
		op := connectHTTP(t, m, "status", req)
		if op.Status != "unknown" || op.ErrorCode != "service_restarted" {
			t.Fatal(op)
		}
	})
}
func TestPlatformConnectSkillIdentityAndHumanBoundary(t *testing.T) {
	m, svc, member, req := connectFixture(t)
	read := platformConnectRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}
	for _, path := range []string{"/v1/platforms/skill", "/v1/platforms/list"} {
		if w := managerHTTP(m, path, read); w.Code != 200 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	client, endpoint := toolPipeServer(t, m)
	var out bytes.Buffer
	if err := runPlatformConnectWithClient(context.Background(), []string{"skill"}, &out, &out, cliToolEnv(endpoint, svc, member), client); err != nil || !strings.Contains(out.String(), "Connect Feishu") {
		t.Fatal(err, out.String())
	}
	lookup := read
	lookup.SourceSeq = req.SourceSeq
	if w := managerHTTP(m, "/v1/platforms/connect/status", lookup); w.Code != 404 || !strings.Contains(w.Body.String(), "operation_not_found") {
		t.Fatal("missing operation not distinguished", w.Code, w.Body.String())
	}
	posted, postErr := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: req.Caller, ClientMessageID: "agent-is-not-human", Text: "connect"})
	if postErr != nil {
		t.Fatal(postErr)
	}
	agentSource := req
	agentSource.SourceSeq = posted.Seq
	if w := managerHTTP(m, "/v1/platforms/connect/start", agentSource); w.Code != 403 {
		t.Fatal("Agent message accepted as user evidence", w.Code)
	}
	bad := req
	bad.Caller = humanCaller
	if w := managerHTTP(m, "/v1/platforms/connect/start", bad); w.Code == 200 {
		t.Fatal("self-reported human admitted")
	}
	bad = req
	bad.SourceSeq = 9999
	if w := managerHTTP(m, "/v1/platforms/connect/start", bad); w.Code != 403 {
		t.Fatal("invented source admitted", w.Code, w.Body.String())
	}
	for _, path := range []string{"/v1/platforms/lark/setup", "/v1/platforms/onboarding/prepare", "/v1/platforms/save"} {
		if w := managerHTTP(m, path, req); w.Code == 200 {
			t.Fatal("human endpoint loosened", path)
		}
	}
	for _, field := range []string{"executable", "cli_profile", "source_text"} {
		data, _ := json.Marshal(req)
		var wire map[string]any
		_ = json.Unmarshal(data, &wire)
		wire[field] = "untrusted"
		data, _ = json.Marshal(wire)
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/platforms/connect/start", bytes.NewReader(data)))
		if w.Code != 400 {
			t.Fatal("untrusted field accepted", field, w.Code)
		}
	}
}
func TestPlatformConnectWaitTimeoutAndInterruptionDoNotCancel(t *testing.T) {
	m, svc, member, req := connectFixture(t)
	_, s := connectPending(t, m, svc, req)
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, member)
	args := []string{"connect", "wait", "--operation-id", req.OperationID, "--timeout", "20ms"}
	var out bytes.Buffer
	if err := runPlatformConnectWithClient(context.Background(), args, &out, &out, env, client); err != nil || !strings.Contains(out.String(), `"pending":true`) {
		t.Fatal(err, out.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runPlatformConnectWithClient(ctx, args, &out, &out, env, client); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	m.mu.Lock()
	select {
	case <-s.done:
		t.Fatal("observer cancelled service session")
	default:
	}
	m.mu.Unlock()
	req.Input = nil
	req.SourceSeq = 0
	if op := connectHTTP(t, m, "status", req); op.Status != "running" {
		t.Fatal(op)
	}
}
func TestPlatformConnectRestartReconcilesSavedConnectionWithoutWritingAgain(t *testing.T) {
	m, svc, _, req := connectFixture(t)
	_, s := connectPending(t, m, svc, req)
	connectSucceed(t, m, s)
	m.mu.Lock()
	op, err := m.readPlatformConnectLocked(req.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	// Crash after connection commit, before the final operation receipt save.
	op.Status, op.Stage, op.ResultVersion = "running", "connection", 0
	op.Connection = onboardingStep{Status: "pending"}
	if err = m.savePlatformConnectLocked(&op); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(m.store.dir, "platform-connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.recoverPlatformConnectLocked(); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	req.Input = nil
	req.SourceSeq = 0
	restored := connectHTTP(t, m, "status", req)
	after, _ := os.ReadFile(filepath.Join(m.store.dir, "platform-connections.json"))
	if restored.Status != "completed" || !bytes.Equal(before, after) {
		t.Fatal("recovery replayed connection", restored)
	}
	if err = m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, m.store.dir, nil)
	reopened.mu.Lock()
	durable, err := reopened.readPlatformConnectLocked(req.OperationID)
	reopened.mu.Unlock()
	if err != nil || !reflect.DeepEqual(restored, durable) {
		t.Fatal("restart lost receipt", err, durable)
	}
}

func TestPlatformConnectReuseRequiresExactVerifiedVersion(t *testing.T) {
	m, svc, _, req := connectFixture(t)
	_, s := connectPending(t, m, svc, req)
	connectSucceedWithNativeProfile(t, m, s)
	query := req
	query.Input = nil
	query.SourceSeq = 0
	original := connectHTTP(t, m, "status", query)
	source, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller, ClientMessageID: "reuse", Text: "使用已有的工作飞书连接"})
	if err != nil {
		t.Fatal(err)
	}
	req.OperationID, _ = newOpaqueID()
	req.SourceSeq = source.Seq
	req.Input = &platformConnectInput{Provider: "lark", Name: original.Input.Name, PlatformID: original.PlatformID, PlatformVersion: original.Connection.Version + 1}
	if w := managerHTTP(m, "/v1/platforms/connect/start", req); w.Code != 409 {
		t.Fatal("stale account version reused", w.Code)
	}
	req.Input.PlatformVersion = original.Connection.Version
	_ = connectHTTP(t, m, "start", req)
	reused := waitConnectionResult(t, m, req)
	if reused.Status != "completed" || reused.PlatformID != original.PlatformID || reused.Authorization.Status != "reused" {
		t.Fatal(reused)
	}
	file, err := m.platforms.Load()
	if err != nil || len(file.Platforms) != 1 {
		t.Fatal("reuse duplicated connection", err)
	}
}
func TestPlatformConnectSaveUnknownNeverReplaysConnection(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_replace", true: "after_replace"}[afterRename], func(t *testing.T) {
			m, svc, _, req := connectFixture(t)
			_, s := connectPending(t, m, svc, req)
			real := m.store.ops
			m.mu.Lock()
			m.store.ops.rename = func(from, to string) error {
				if to == m.connectPath(req.OperationID) {
					data, err := os.ReadFile(from)
					if err != nil {
						return err
					}
					var op platformConnectOperation
					if json.Unmarshal(data, &op) == nil && op.Status == "completed" {
						if afterRename {
							if err = real.rename(from, to); err != nil {
								return err
							}
						}
						return errors.New("injected uncertain result receipt")
					}
				}
				return real.rename(from, to)
			}
			s.profile = platform.CLIProfile{Version: 1, Executable: "/synthetic/lark-cli", Profile: "teamx-feishu-fixture", Brand: "feishu", CLIVersion: lark.CLIVersion}
			s.ConfigurationFound = true
			s.Status, s.Stage = "succeeded", "complete"
			s.validation = connectValidation()
			close(s.done)
			m.finishPlatformConnectLocked(s)
			if !errors.Is(s.connectErr, ErrCommitUnknown) {
				t.Fatal("uncertain save misreported", s.connectErr)
			}
			before, err := os.ReadFile(filepath.Join(m.store.dir, "platform-connections.json"))
			if err != nil {
				t.Fatal(err)
			}
			m.store.ops = real
			m.mu.Unlock()
			query := req
			query.Input = nil
			query.SourceSeq = 0
			if w := managerHTTP(m, "/v1/platforms/connect/status", query); w.Code == 200 {
				t.Fatal("uncertain save reported as confirmed")
			}
			if err = m.close(); err != nil {
				t.Fatal(err)
			}
			reopened := testManager(t, m.store.dir, nil)
			reopened.mu.Lock()
			op, err := reopened.readPlatformConnectLocked(req.OperationID)
			reopened.mu.Unlock()
			if err != nil || op.Status != "completed" || op.Connection.Status != "connected" {
				t.Fatal("saved connection not reconciled", err, op)
			}
			after, _ := os.ReadFile(filepath.Join(m.store.dir, "platform-connections.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("recovery replayed connection write")
			}
		})
	}
}
func TestPlatformConnectMembershipRemovedBeforeAuthorizationCompletes(t *testing.T) {
	m, svc, _, req := connectFixture(t)
	op, s := connectPending(t, m, svc, req)
	if _, err := svc.room.Call[MemberInfo](context.Background(), RemoveMember{op.MemberIdentity}); err != nil {
		t.Fatal(err)
	}
	connectSucceed(t, m, s)
	m.mu.Lock()
	defer m.mu.Unlock()
	done, err := m.readPlatformConnectLocked(op.ID)
	if err != nil || done.Status != "failed" || done.ErrorCode != "target_changed" {
		t.Fatal(done, err)
	}
	file, err := m.platforms.Load()
	if err != nil || len(file.Platforms) != 0 {
		t.Fatal("old membership saved connection", err)
	}
}
