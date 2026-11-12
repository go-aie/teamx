package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
)

func waitConnectionResult(t *testing.T, m *roomManager, req platformConnectRequest) platformConnectOperation {
	t.Helper()
	return waitConnectionObservation(t, m, req, func(op platformConnectOperation) bool { return connectTerminal(op.Status) })
}
func waitConnectionObservation(t *testing.T, m *roomManager, req platformConnectRequest, accept func(platformConnectOperation) bool) platformConnectOperation {
	t.Helper()
	req.Input, req.SourceSeq, req.Notify = nil, 0, nil
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		op := connectHTTP(t, m, "status", req)
		if accept(op) {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("connection observation timed out")
	return platformConnectOperation{}
}

func larkAuthorizationFixture(t *testing.T) (*roomManager, platformConnectRequest, managedPlatform, string) {
	t.Helper()
	return larkAuthorizationWithFixture(t, connectFixture)
}
func larkAuthorizationWithFixture(t *testing.T, fixture platformConnectionFixture) (*roomManager, platformConnectRequest, managedPlatform, string) {
	t.Helper()
	m, _, _, req := fixture(t)
	p, dir := roomAuthProfile(t)
	c := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "工作飞书", Provider: "lark", URL: "https://open.feishu.cn", CLIProfile: &p})
	c, err := m.verifyPlatform(context.Background(), managedPlatformRequest{ID: c.ID, Version: c.Version})
	if err != nil || c.Validation.Status != "valid" {
		t.Fatal(err, c.Validation)
	}
	req.Input = &platformConnectInput{Intent: "authorize", Provider: "lark", PlatformID: c.ID, PlatformVersion: c.Version}
	return m, req, c, dir
}

func TestLarkAuthorizationKeepsExactSharedConnectionsAndReceipts(t *testing.T) {
	m, req, c, dir := larkAuthorizationFixture(t)
	second := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("e", 32), Name: "共享飞书", Provider: "lark", URL: c.URL, CLIProfile: c.CLIProfile})
	second, err := m.verifyPlatform(context.Background(), managedPlatformRequest{ID: second.ID, Version: second.Version})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := m.platforms.Load()
	mode := "inbox"
	req.Notify = &mode
	started := connectHTTP(t, m, "authorize", req)
	if started.Schema != 2 || started.PlatformID != c.ID || len(started.AuthorizationTargets) != 2 {
		t.Fatal(started)
	}
	waitConnectionObservation(t, m, req, func(op platformConnectOperation) bool { return op.Interaction.URL != "" })
	duplicate := req
	duplicate.OperationID, _ = newOpaqueID()
	if w := managerHTTP(m, "/v1/platforms/connect/authorize", duplicate); w.Code != 409 {
		t.Fatal("duplicate source started OAuth", w.Code)
	}
	lookup := req
	lookup.Input, lookup.Notify, lookup.OperationID = nil, nil, ""
	lookup.Intent, lookup.PlatformID = "authorize", c.ID
	if op := connectHTTP(t, m, "status", lookup); op.ID != req.OperationID {
		t.Fatal("wrong intent lookup", op)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth-release"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	done := waitConnectionResult(t, m, req)
	if done.Status != "completed" || done.Account != c.Validation.Account || done.Permissions == nil {
		t.Fatal(done)
	}
	after, _ := m.platforms.Load()
	if len(after.Platforms) != len(before.Platforms) {
		t.Fatal("authorization created connection")
	}
	for i, prior := range before.Platforms {
		next := after.Platforms[i]
		if next.ID != prior.ID || next.Name != prior.Name || next.ToolEntryID != prior.ToolEntryID || next.Generation != prior.Generation || next.CredentialVersion != prior.CredentialVersion+1 || next.Validation.Account != prior.Validation.Account || next.Validation.Status != "valid" {
			t.Fatal("shared identity changed")
		}
	}
	replayed := connectHTTP(t, m, "authorize", req)
	done.Continuation, replayed.Continuation = nil, nil
	if !reflect.DeepEqual(done, replayed) {
		t.Fatal("replay changed receipt")
	}
	data, err := os.ReadFile(m.connectPath(req.OperationID))
	if err != nil || strings.Contains(string(data), "device_code") || strings.Contains(string(data), "https://accounts.feishu.cn/oauth") {
		t.Fatal("temporary authorization persisted", err)
	}
	var stored platformConnectOperation
	if json.Unmarshal(data, &stored) != nil || !validPlatformConnect(stored) {
		t.Fatal("invalid authorization receipt")
	}
	// A single real source can connect and authorize, but each intent has one
	// stable reservation. Plain source recovery still means connect.
	reuse := req
	reuse.OperationID, _ = newOpaqueID()
	reuse.Notify = nil
	reuse.Input = &platformConnectInput{Provider: "lark", Name: c.Name, PlatformID: c.ID, PlatformVersion: done.Connection.Version}
	connectHTTP(t, m, "start", reuse)
	if result := waitConnectionResult(t, m, reuse); result.Status != "completed" {
		t.Fatal(result)
	}
	lookup.Intent, lookup.PlatformID = "", ""
	if result := connectHTTP(t, m, "status", lookup); result.ID != reuse.OperationID {
		t.Fatal("connect/authorize source collision")
	}
}

func TestLarkAuthorizationCLIUsesOriginalMemberWithoutToolBinding(t *testing.T) {
	m, req, c, dir := larkAuthorizationWithFixture(t, assistantConnectFixture)
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	server := httptest.NewServer(m)
	defer server.Close()
	env := cliToolEnv(server.URL, svc, MemberState{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID})
	var output bytes.Buffer
	args := []string{"connect", "authorize", "--platform-id", c.ID, "--platform-version", "2", "--operation-id", req.OperationID, "--source-seq", "1", "--notify", "inbox"}
	if err := runPlatformConnectWithClient(context.Background(), args, &output, &output, env, server.Client()); err != nil {
		t.Fatal(err, output.String())
	}
	var op platformConnectOperation
	if json.Unmarshal(output.Bytes(), &op) != nil || op.Input.Intent != "authorize" || op.PlatformID != c.ID || op.MemberIdentity.AgentID != req.Caller.ID {
		t.Fatal(output.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "auth-release"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := waitConnectionResult(t, m, req); result.Status != "completed" || result.Account != c.Validation.Account {
		t.Fatal(result)
	}
	m.mu.Lock()
	bindings, err := m.readToolBindingsLocked(req.RoomIdentity)
	m.mu.Unlock()
	if err != nil || len(bindings.Bindings) != 0 {
		t.Fatal("account authorization created member bindings", err)
	}
}

func TestLarkAuthorizationRejectsAccountSwitchAndCancellation(t *testing.T) {
	for _, mode := range []string{"switch", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			m, req, c, dir := larkAuthorizationFixture(t)
			if mode == "switch" {
				data, _ := os.ReadFile(c.CLIProfile.Executable)
				if err := os.WriteFile(c.CLIProfile.Executable, []byte(strings.ReplaceAll(string(data), "ou_reader", "ou_other")), 0700); err != nil {
					t.Fatal(err)
				}
			}
			connectHTTP(t, m, "authorize", req)
			waitConnectionObservation(t, m, req, func(op platformConnectOperation) bool { return op.Interaction.URL != "" })
			if mode == "cancel" {
				cancel := req
				cancel.Input, cancel.SourceSeq = nil, 0
				connectHTTP(t, m, "cancel", cancel)
			} else if err := os.WriteFile(filepath.Join(dir, "auth-release"), []byte("yes"), 0600); err != nil {
				t.Fatal(err)
			}
			done := waitConnectionResult(t, m, req)
			if done.Account != c.Validation.Account || done.Connection.Status == "connected" || done.Status == "completed" {
				t.Fatal("authorization switched original account", done)
			}
			if mode == "cancel" && done.Status != "cancelled" {
				t.Fatal(done)
			}
			file, _ := m.platforms.Load()
			if len(file.Platforms) != 1 || file.Platforms[0].Validation.Status == "valid" {
				t.Fatal("unconfirmed credentials restored")
			}
			if replay := connectHTTP(t, m, "authorize", req); !reflect.DeepEqual(replay, done) {
				t.Fatal("terminal authorization repeated")
			}
		})
	}
}

func TestLarkAuthorizationRejectsRevisionChangeDuringOAuth(t *testing.T) {
	m, req, c, dir := larkAuthorizationFixture(t)
	connectHTTP(t, m, "authorize", req)
	waitConnectionObservation(t, m, req, func(op platformConnectOperation) bool { return op.Interaction.URL != "" })
	m.mu.Lock()
	file, err := m.platforms.Load()
	if err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	ticket, err := m.platforms.BeginVerification(c.ID, file.Platforms[0].Version)
	if err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	newer, err := m.platforms.CompleteVerification(ticket, c.Validation)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth-release"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	done := waitConnectionResult(t, m, req)
	if done.Status != "failed" || done.Connection.Status == "connected" {
		t.Fatal("old OAuth overwrote a newer verification", done)
	}
	after, err := m.platforms.Load()
	if err != nil || after.Platforms[0].Version != newer.Version {
		t.Fatal("stale operation changed connection", err)
	}
}

func TestLarkAuthorizationInterruptedBeforeInvalidationIsNeverInferredComplete(t *testing.T) {
	m, req, c, _ := larkAuthorizationFixture(t)
	file, _ := m.platforms.Load()
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	_, s := connectPending(t, m, svc, platformConnectRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, OperationID: req.OperationID, SourceSeq: req.SourceSeq, Input: &platformConnectInput{Provider: "lark", Name: c.Name}})
	m.mu.Lock()
	op, err := m.readPlatformConnectLocked(req.OperationID)
	if err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	op.Schema, op.Input, op.PlatformID, op.Profile, op.Account = 2, *req.Input, c.ID, c.CLIProfile, c.Validation.Account
	op.Input.Name = c.Name
	r := file.Platforms[0]
	op.AuthorizationImpact = []larkAuthPlatform{{ID: c.ID, Version: c.Version}}
	op.AuthorizationTargets = []larkAuthorizationTarget{{ID: c.ID, Version: c.Version + 1, Generation: r.Generation, CredentialVersion: r.CredentialVersion + 1, Profile: *c.CLIProfile, Account: c.Validation.Account}}
	if err = m.savePlatformConnectLocked(&op); err == nil {
		err = m.recoverPlatformConnectLocked()
	}
	recovered, e := m.readPlatformConnectLocked(op.ID)
	s.cancel()
	m.mu.Unlock()
	if err != nil || e != nil || recovered.Status != "unknown" {
		t.Fatal("old connection masqueraded as OAuth commit", err, e, recovered)
	}
}

func connectSucceedWithNativeProfile(t *testing.T, m *roomManager, s *larkAuthSession) {
	t.Helper()
	p, _ := roomAuthProfile(t)
	client, err := lark.New(p)
	if err != nil {
		t.Fatal(err)
	}
	validation, err := client.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s.profile, s.validation, s.ConfigurationFound = p, validation, true
	s.Status, s.Stage = "succeeded", "complete"
	close(s.done)
	m.finishPlatformConnectLocked(s)
	if s.connectErr != nil {
		t.Fatal(s.connectErr)
	}
}
