package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func savedMemberToolFixture(t *testing.T, provider string) (*roomManager, *service, platformMemberRequest) {
	t.Helper()
	var m *roomManager
	var svc *service
	var req platformMemberRequest
	if provider == "lark" {
		m, svc, _, req = memberToolFixture(t)
		req.Input.Actions = []string{"message.reply"}
	} else {
		m, svc, _, req = gitlabMemberFixture(t)
		req.Input.TodosEnabled, req.Input.Enabled = nil, boolPointer(true)
	}
	gitlabMemberApply(t, m, "tool", req)
	return m, svc, nextMemberToolToggle(t, svc, req, true)
}

func nextMemberToolToggle(t *testing.T, svc *service, req platformMemberRequest, enabled bool) platformMemberRequest {
	t.Helper()
	id, _ := newOpaqueID()
	source, err := svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller, ClientMessageID: id, Text: "切换我的工具 " + strconv.FormatBool(enabled)})
	if err != nil {
		t.Fatal(err)
	}
	req.SourceSeq = source.Seq
	req = currentMemberRequest(req)
	req.Input.Enabled = boolPointer(enabled)
	return req
}

func applyCurrentMemberTool(t *testing.T, m *roomManager, req platformMemberRequest) platformMemberOperation {
	t.Helper()
	w := managerHTTP(m, "/v1/platforms/member/tool", currentMemberWire(t, req))
	var op platformMemberOperation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &op) != nil || op.Status != "completed" {
		t.Fatal(w.Code, w.Body.String())
	}
	return op
}

func TestPlatformMemberToolToggleCLIAndSavedGrants(t *testing.T) {
	parallelFixtureTest(t)
	for _, provider := range []string{"lark", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			m, svc, req := savedMemberToolFixture(t, provider)
			client, endpoint := toolPipeServer(t, m)
			transport := client.Transport
			var paths []string
			client.Transport = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "/platforms/member/") {
					paths = append(paths, r.URL.Path)
					body, err := io.ReadAll(r.Body)
					if err != nil {
						return nil, err
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					var wire map[string]any
					if json.Unmarshal(body, &wire) != nil || wire["observe_current"] != true || wire["operation_id"] != nil || len(wire["input"].(map[string]any)) != 2 {
						t.Fatal("short CLI sent observations or extra fields", string(body))
					}
				}
				return transport.RoundTrip(r)
			})
			env := cliToolEnv(endpoint, svc, MemberState{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID})
			for _, enabled := range []bool{true, false, false, true, true} {
				req = nextMemberToolToggle(t, svc, req, enabled)
				var out bytes.Buffer
				paths = nil
				args := []string{"member", "tool", "--platform-id", req.Input.PlatformID, "--source-seq", strconv.FormatUint(req.SourceSeq, 10), "--enabled", strconv.FormatBool(enabled)}
				if err := runPlatformConnectWithClient(t.Context(), args, &out, &out, env, client); err != nil {
					t.Fatal(err, out.String())
				}
				if !reflect.DeepEqual(paths, []string{"/v1/platforms/member/tool"}) {
					t.Fatal("extra business requests", paths)
				}
				var op platformMemberOperation
				if json.Unmarshal(out.Bytes(), &op) != nil || op.Status != "completed" || op.Input.ExpectedBindingRevision == nil {
					t.Fatal(out.String())
				}
				before, _ := os.ReadFile(m.toolBindingsPath(req.RoomIdentity))
				replay := applyCurrentMemberTool(t, m, req)
				if !sameJSON(op, replay) {
					t.Fatal("retry changed receipt")
				}
				inverse := req
				inverse.Input = &platformMemberInput{PlatformID: req.Input.PlatformID, Enabled: boolPointer(!enabled)}
				if w := managerHTTP(m, "/v1/platforms/member/tool", currentMemberWire(t, inverse)); w.Code != 409 {
					t.Fatal("same source changed direction", w.Code, w.Body.String())
				}
				after, _ := os.ReadFile(m.toolBindingsPath(req.RoomIdentity))
				if !bytes.Equal(before, after) {
					t.Fatal("retry changed authoritative settings")
				}
				m.mu.Lock()
				f, err := m.readToolBindingsLocked(req.RoomIdentity)
				states := m.notifications.StatesLocked(req.RoomIdentity)
				m.mu.Unlock()
				bindings := append(f.Bindings, f.Disabled...)
				if err != nil || len(bindings) != 1 || (len(f.Bindings) == 1) != enabled || len(states) != 0 || bindings[0].PlatformGeneration == "" {
					t.Fatal("wrong binding or extra subscription", f, err)
				}
				if provider == "lark" && (bindings[0].ActionAuthorization == nil || len(bindings[0].ActionAuthorization.Grants) != 1 || bindings[0].ActionAuthorization.Grants[0].Operation != "message.reply" || len(op.Authorization.Grants) != 1) {
					t.Fatal("short switch expanded saved grants", f, op)
				}
			}
		})
	}
}

func TestPlatformMemberToolToggleRejectsMissingIntentAndMixedModes(t *testing.T) {
	parallelFixtureTest(t)
	m, _, _, full := memberToolFixture(t)
	req := currentMemberRequest(full)
	if w := managerHTTP(m, "/v1/platforms/member/tool", currentMemberWire(t, req)); w.Code != 400 || !strings.Contains(w.Body.String(), "complete tool workflow") {
		t.Fatal("initial setup accepted short form", w.Code, w.Body.String())
	}
	for _, enabled := range []bool{false, true} {
		req.Input.Enabled = boolPointer(enabled)
		for _, field := range []string{"platform_version", "notification_version", "expected_binding_revision", "actions", "tool_revision", "todos_enabled"} {
			wire := currentMemberWire(t, req)
			wire["input"].(map[string]any)[field] = nil
			if w := managerHTTP(m, "/v1/platforms/member/tool", wire); w.Code != 400 {
				t.Fatal("mixed wire mode accepted", field, w.Code)
			}
		}
	}
	base := []string{"member", "tool", "--platform-id", req.Input.PlatformID, "--source-seq", "1", "--enabled", "true"}
	for _, extra := range [][]string{{"--platform-version", "0"}, {"--operation-id", ""}, {"--binding-revision", "0"}, {"--action", ""}, {"--platform-version", "1", "--action", "message.reply"}} {
		var out bytes.Buffer
		if err := runPlatformMemberCLI(t.Context(), append(append([]string{}, base...), extra...), &out, &out, func(string) string { t.Fatal("invalid CLI reached network"); return "" }, nil); err == nil {
			t.Fatal("mixed CLI mode accepted", extra)
		}
	}
	if _, err := os.Stat(m.platformMemberPath(platformMemberSwitchID(req, "tool"))); !os.IsNotExist(err) {
		t.Fatal("invalid request reserved an operation", err)
	}
}

func TestPlatformMemberToolToggleMissingAuthorizationAndOAuthRepair(t *testing.T) {
	parallelFixtureTest(t)
	for _, missing := range []string{"authorization", "generation", "oauth"} {
		t.Run(missing, func(t *testing.T) {
			h := larkToolConfigurationHarnessWithMembers(t, 1)
			full := larkMemberToolRequest(t, h, 0)
			full.Input.Actions = []string{"message.reply"}
			gitlabMemberApply(t, h.m, "tool", full)
			req := nextMemberToolToggle(t, h.svc, full, true)
			if missing == "oauth" {
				writeJSONFixture(t, filepath.Join(h.dir, "missing-scopes.json"), []string{"im:message:readonly"})
			} else {
				f := configurationBindings(t, h)
				if missing == "authorization" {
					f.Bindings[0].ActionAuthorization = nil
				} else {
					f.Bindings[0].PlatformGeneration = strings.Repeat("f", 32)
				}
				h.m.mu.Lock()
				err := h.m.store.save(h.m.toolBindingsPath(req.RoomIdentity), f, 1<<20)
				h.m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(h.m.toolBindingsPath(req.RoomIdentity))
			w := managerHTTP(h.m, "/v1/platforms/member/tool", currentMemberWire(t, req))
			if missing == "oauth" {
				if w.Code != 200 || !strings.Contains(w.Body.String(), "user_authorization_required") || !strings.Contains(w.Body.String(), `"status":"not_applied"`) {
					t.Fatal(w.Code, w.Body.String())
				}
			} else if w.Code != 409 {
				t.Fatal(w.Code, w.Body.String())
			}
			after, _ := os.ReadFile(h.m.toolBindingsPath(req.RoomIdentity))
			if !bytes.Equal(before, after) {
				t.Fatal("repair path changed binding")
			}
			if _, err := os.Stat(h.m.platformMemberPath(platformMemberSwitchID(req, "tool"))); !os.IsNotExist(err) {
				t.Fatal("repair path reserved operation", err)
			}
		})
	}
}

func TestPlatformMemberToolToggleUnknownSurvivesRestart(t *testing.T) {
	parallelFixtureTest(t)
	m, _, req := savedMemberToolFixture(t, "lark")
	req.Input.Enabled = boolPointer(false)
	real := m.store.ops
	m.mu.Lock()
	m.store.ops.rename = func(from, to string) error {
		if to == m.platformMemberPath(platformMemberSwitchID(req, "tool")) {
			var op platformMemberOperation
			data, _ := os.ReadFile(from)
			if json.Unmarshal(data, &op) == nil && op.Status == "completed" {
				return os.ErrPermission
			}
		}
		return real.rename(from, to)
	}
	m.mu.Unlock()
	if w := managerHTTP(m, "/v1/platforms/member/tool", currentMemberWire(t, req)); w.Code == 200 {
		t.Fatal("uncertain save reported success")
	}
	m.mu.Lock()
	m.store.ops = real
	m.mu.Unlock()
	before, _ := os.ReadFile(m.toolBindingsPath(req.RoomIdentity))
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, m.store.dir, nil)
	svc := eventLiveService(t, &eventHarness{m: reopened, room: roomSummary{RoomIdentity: req.RoomIdentity}})
	req.ServiceInfo = svc.info
	w := managerHTTP(reopened, "/v1/platforms/member/tool", currentMemberWire(t, req))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"unknown"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	query := platformMemberRequest{ServiceInfo: svc.info, Caller: req.Caller, SourceSeq: req.SourceSeq}
	if w := managerHTTP(reopened, "/v1/platforms/member/status", query); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"unknown"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	after, _ := os.ReadFile(reopened.toolBindingsPath(req.RoomIdentity))
	if !bytes.Equal(before, after) {
		t.Fatal("unknown operation replayed after restart")
	}
}

func TestPlatformMemberToolToggleRevocationWaitsForCallSettlement(t *testing.T) {
	parallelFixtureTest(t)
	m, _, req := savedMemberToolFixture(t, "lark")
	req.Input.Enabled = boolPointer(false)
	callCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	call := &platformCall{owner: req.RoomIdentity, platformID: req.Input.PlatformID, agentID: req.Caller.ID, membershipID: req.Caller.MembershipID, tool: "lark"}
	m.mu.Lock()
	call.Call = m.platformCalls.Register(call, cancel)
	m.mu.Unlock()
	defer m.finishPlatformCall(call)
	done := make(chan platformMemberOperation, 1)
	go func() { op, _ := m.configurePlatformMember(t.Context(), "tool", req); done <- op }()
	select {
	case <-callCtx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("revocation did not cancel active call")
	}
	select {
	case <-done:
		t.Fatal("revocation returned before call settled")
	default:
	}
	m.finishPlatformCall(call)
	select {
	case op := <-done:
		if op.Status != "completed" {
			t.Fatal(op)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("revocation did not settle")
	}
}

func TestPlatformMemberToolToggleRechecksConcurrentEdits(t *testing.T) {
	parallelFixtureTest(t)
	for _, mutation := range []string{"same-target", "sibling", "account"} {
		t.Run(mutation, func(t *testing.T) {
			h := larkToolConfigurationHarnessWithMembers(t, 2)
			full := larkMemberToolRequest(t, h, 0)
			full.Input.Actions = []string{"message.send_self"}
			gitlabMemberApply(t, h.m, "tool", full)
			req := nextMemberToolToggle(t, h.svc, full, true)
			called := false
			h.m.bindingEvidencePreparers["lark"] = func(ctx context.Context, _ *toolBindingChange) error {
				called = true
				switch mutation {
				case "same-target":
					h.m.mu.Lock()
					_, _, err := h.m.changeToolBindingLocked(ctx, toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: req.RoomIdentity, Caller: humanCaller, Binding: tool.Binding{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID, PlatformID: req.Input.PlatformID, Tool: "lark"}})
					h.m.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
				case "sibling":
					bindCLITool(t, h.m, req.RoomIdentity, h.members[1], h.c, "lark", 0)
				case "account":
					h.m.mu.Lock()
					ticket, err := h.m.platforms.BeginVerification(h.c.ID, h.c.Version)
					if err == nil {
						_, err = h.m.platforms.CompleteVerification(ticket, h.c.Validation)
					}
					h.m.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
				}
				return errors.New("self proof unavailable")
			}
			op, err := h.m.configurePlatformMember(t.Context(), "tool", req)
			if !called {
				t.Fatal("did not exercise remote preparation")
			}
			if mutation == "sibling" {
				if err != nil || op.Status != "completed" || len(configurationBindings(t, h).Bindings) != 2 || len(op.Authorization.Grants) != 1 {
					t.Fatal("sibling edit blocked or expanded restoration", op, err)
				}
			} else {
				if err == nil {
					t.Fatal("stale preparation overwrote concurrent edit", op)
				}
				if _, err := os.Stat(h.m.platformMemberPath(platformMemberSwitchID(req, "tool"))); !os.IsNotExist(err) {
					t.Fatal("stale preparation reserved operation", err)
				}
			}
		})
	}
}

func TestPlatformMemberToolToggleMemberAccountAndSourceBoundaries(t *testing.T) {
	parallelFixtureTest(t)
	h := larkToolConfigurationHarnessWithMembers(t, 2)
	full := larkMemberToolRequest(t, h, 0)
	full.Input.Actions = []string{"message.reply"}
	gitlabMemberApply(t, h.m, "tool", full)
	req := nextMemberToolToggle(t, h.svc, full, false)
	other := req
	other.Caller = agentCaller(h.members[1].AgentID, h.members[1].MembershipID)
	if w := managerHTTP(h.m, "/v1/platforms/member/tool", currentMemberWire(t, other)); w.Code == 200 {
		t.Fatal("other member inherited saved binding", w.Body.String())
	}
	other = req
	second := platformTestSave(t, h.m, managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "second", Provider: "lark", URL: h.c.URL, CLIProfile: h.c.CLIProfile})
	second, err := h.m.verifyPlatform(t.Context(), managedPlatformRequest{ID: second.ID, Version: second.Version})
	if err != nil || second.Validation.Status != "valid" {
		t.Fatal(err)
	}
	other.Input = &platformMemberInput{PlatformID: second.ID, Enabled: boolPointer(false)}
	if w := managerHTTP(h.m, "/v1/platforms/member/tool", currentMemberWire(t, other)); w.Code == 200 {
		t.Fatal("other account inherited saved binding", w.Body.String())
	}
	agentPost, err := h.svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: req.Caller, ClientMessageID: "agent-toggle", Text: "关闭我的工具"})
	if err != nil {
		t.Fatal(err)
	}
	other = req
	other.SourceSeq = agentPost.Seq
	if w := managerHTTP(h.m, "/v1/platforms/member/tool", currentMemberWire(t, other)); w.Code != 403 {
		t.Fatal("Agent text accepted as human evidence", w.Code, w.Body.String())
	}
	op := applyCurrentMemberTool(t, h.m, req)
	newer := nextMemberToolToggle(t, h.svc, req, true)
	applyCurrentMemberTool(t, h.m, newer)
	if replay := applyCurrentMemberTool(t, h.m, req); !sameJSON(replay, op) {
		t.Fatal("historical receipt changed after later intent")
	}
	// An old, unsubmitted source cannot undo a later intent.
	other = currentMemberRequest(full)
	other.Input.Enabled = boolPointer(false)
	if w := managerHTTP(h.m, "/v1/platforms/member/tool", currentMemberWire(t, other)); w.Code != 409 {
		t.Fatal("older source overwrote later intent", w.Code, w.Body.String())
	}
	if _, err := h.svc.remove(apiRequest{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}); err != nil {
		t.Fatal(err)
	}
	replacement := inboxRoutingMember(t, h.svc, req.Caller.ID, "tool-toggle-replacement")
	req.Caller = agentCaller(replacement.AgentID, replacement.MembershipID)
	query := platformMemberRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, OperationID: op.ID}
	if w := managerHTTP(h.m, "/v1/platforms/member/status", query); w.Code == 200 {
		t.Fatal("replacement membership inherited receipt")
	}
	if w := managerHTTP(h.m, "/v1/platforms/member/tool", currentMemberWire(t, req)); w.Code == 200 {
		t.Fatal("replacement membership inherited binding")
	}
}
