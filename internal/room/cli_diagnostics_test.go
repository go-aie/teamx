package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAgentCommandHelpIsScopedAndOffline(t *testing.T) {
	for key := range referenceCommandHelps {
		parts := strings.Split(key, "/")
		for _, flag := range []string{"--help", "-h"} {
			t.Run(key+flag, func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				err := runCLI(context.Background(), append(parts, flag), &stdout, &stderr, func(string) string { return "" })
				// Help must not require a binding or connect to a service.
				if err != nil {
					t.Fatal(err)
				}
				for _, section := range []string{"Usage: teamx " + strings.Join(parts, " "), "Parameters:", "Returns:", "Example:", "Constraints:"} {
					if !strings.Contains(stdout.String(), section) {
						t.Fatal("missing", section, stdout.String())
					}
				}
				if stderr.Len() != 0 || strings.Contains(stdout.String(), "schema 2, JSON output") {
					t.Fatal(stdout.String(), stderr.String())
				}
				if key != "actions/execute" && strings.Contains(stdout.String(), "Ordinary comments allow at most two") {
					t.Fatal("unrelated action help", stdout.String())
				}
			})
		}
	}
	for _, args := range [][]string{{"--help"}, {"matters", "--help"}, {"policy", "--help"}, {"actions", "--help"}, {"inbox", "--help"}, {"read", "--help"}, {"send", "--help"}} {
		var stdout bytes.Buffer
		err := runCLI(context.Background(), args, &stdout, io.Discard, func(string) string { return "" })
		// read/send retain their pre-existing flag.ErrHelp exit contract.
		wantHelpError := args[0] == "read" || args[0] == "send"
		if (err != nil && !(wantHelpError && errors.Is(err, flag.ErrHelp))) || !strings.Contains(stdout.String(), "Usage:") {
			t.Fatal(args, err, stdout.String())
		}
	}
	for key, snippets := range map[string][]string{
		"matters/read":    {"reply_to", "revision", "unit_version", "registered_actions", "new-ID"},
		"actions/execute": {"ONE", "answer's message.seq", "never fall back", "at most two", "discussion replies one", "Never retry unknown", "policy_changed"},
		"inbox/read":      {"Full page output", "Confirmation failure", "business completion"},
		"policy/update":   {"inferred_habit saves suggested only", "One-off consent", "source_text", "Omission is not a wildcard"},
	} {
		parts := strings.Split(key, "/")
		help := referenceHelpText(parts[0], parts[1])
		for _, snippet := range snippets {
			if !strings.Contains(help, snippet) {
				t.Fatal(key, "lost constraint", snippet)
			}
		}
	}
}

func TestAgentJSONDiagnosticsDoNotSendMalformedRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	for _, tc := range []struct{ command, op, flag, payload, field, message string }{
		{"actions", "execute", "--request", `{"input":{"body":42}}`, "input.body", "expected string"},
		{"actions", "execute", "--request", `{"authority":{"typo":"private-test-secret"}}`, "authority.typo", "unknown JSON field"},
		{"policy", "update", "--change", `{"change":{"scope":{"account_id":{"typo":"private-test-secret"}}}}`, "change.scope.account_id.typo", "unknown JSON field"},
		{"matters", "update", "--change", `{"unit_version":"private-test-secret"}`, "unit_version", "expected uint64"},
		{"matters", "notify", "--change", `{"proposed":{"body":42}}`, "proposed.body", "expected string"},
		{"matters", "instruct", "--change", `{"intent":{"typo":true}}`, "intent.typo", "unknown JSON field"},
		{"matters", "reply", "--change", `{"source_seq":`, "", "incomplete JSON"},
		{"matters", "update", "--change", `{"status":!}`, "", "invalid character"},
		{"policy", "update", "--change", `{} {}`, "", "exactly one JSON"},
		{"matters", "instruction-status", "--change", ``, "", "missing JSON"},
		{"matters", "instruct", "--change", `{"expires_at":"private-test-secret"}`, "expires_at", "RFC3339"},
		{"policy", "update", "--change", `{"change":{"expires_at":"private-test-secret"}}`, "change.expires_at", "RFC3339"},
	} {
		t.Run(tc.command+"/"+tc.op+"/"+tc.message, func(t *testing.T) {
			var stdout bytes.Buffer
			err := runCLI(context.Background(), []string{tc.command, tc.op, tc.flag, tc.payload}, &stdout, io.Discard, func(k string) string {
				if k == "TEAMX_ENDPOINT" {
					return server.URL
				}
				return ""
			})
			if !errors.Is(err, ErrRequest) || !strings.Contains(err.Error(), tc.message) || !strings.Contains(err.Error(), tc.field) || strings.Contains(err.Error(), "private-test-secret") || stdout.Len() != 0 {
				t.Fatal(err, stdout.String())
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("malformed request reached HTTP", calls.Load())
	}
}

func diagnosticHTTP(t *testing.T, svc *service, req apiRequest, path string) (int, apiError) {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	svc.ServeHTTP(w, httptest.NewRequest("POST", path, bytes.NewReader(data)))
	var failure apiError
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
		t.Fatal(err, w.Body)
	}
	return w.Code, failure
}

func TestAgentVersionDiagnosticsRespectMemberGateAndKeepState(t *testing.T) {
	h, svc, units, _ := conversationHarness(t, 1)
	svc.requests = make(chan struct{}, 32)
	u := units[0]
	q := followupRequest{followupCommand: conversationCommand(u, 1), Status: "in_progress", Summary: "test"}
	q.Revision = u.Revision + 1
	req := apiRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(h.id), Followup: &q}
	status, failure := diagnosticHTTP(t, svc, req, "/v1/matters/update")
	if status != 409 || failure.Code != "conflict" || failure.Field != "unit_version" || failure.ExpectedVersion == nil || *failure.ExpectedVersion != q.Revision || failure.ActualVersion == nil || *failure.ActualVersion != u.Revision || failure.Hint == "" {
		t.Fatal(status, failure)
	}
	cli := entryCLI(t, svc)
	if err := cli(entryChange(t, "matters", "update", q), nil); err == nil || !strings.Contains(err.Error(), "unit_version") || !strings.Contains(err.Error(), "expected=") {
		t.Fatal(err)
	}
	req.ServiceInfo = svc.info
	q.Revision, q.TurnNumber = u.Revision, 2
	status, failure = diagnosticHTTP(t, svc, req, "/v1/matters/update")
	if status != 409 || failure.Field != "turn_number" || failure.ActualVersion == nil || *failure.ActualVersion != 1 {
		t.Fatal(status, failure)
	}
	for _, mutate := range []func(*apiRequest){
		func(r *apiRequest) { r.Caller.MembershipID = "other-membership" },
		func(r *apiRequest) { r.RoomID = "other-room" },
		func(r *apiRequest) { r.ServiceEpoch = "old-service" },
		func(r *apiRequest) { r.AgentID = "other-agent" },
	} {
		bad := req
		mutate(&bad)
		status, failure := diagnosticHTTP(t, svc, bad, "/v1/matters/update")
		if status != 403 || failure.Code != "identity" || failure.ExpectedVersion != nil || failure.ActualVersion != nil {
			t.Fatal("leaked member version", status, failure)
		}
	}
	got, err := h.store.readFollowup(h.id, u.ObjectRef, u.Ref)
	if err != nil || !reflect.DeepEqual(got, u) {
		t.Fatal("rejected changes mutated unit", err, got)
	}
}

func TestAgentPolicyAndActionDiagnosticRecovery(t *testing.T) {
	h, svc, _, _ := conversationHarness(t, 1)
	source := postConversationReply(t, h, "diagnostic-rule", "以后按规则办理", 0)
	q := conversationRule(source.Seq)
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q); err != nil {
		t.Fatal(err)
	}
	// A true replay must reconcile before version checks.
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q); err != nil {
		t.Fatal("replay rejected", err)
	}
	q.Change.Text += " new change"
	_, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q)
	assertVersionDiagnostic(t, err, "change.revision", 0, 1)
	q.Change.Revision = 1
	_, err = svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q)
	assertVersionDiagnostic(t, err, "change.version", 0, 1)

	f := referenceExecuteTest(t, "object")
	f.q.Input.Body = ""
	if _, err := f.run(); !errors.Is(err, ErrRequest) || !strings.Contains(err.Error(), "input.body") || f.calls.Load() != 0 {
		t.Fatal(err)
	}
	f.q.Input.Body = "本次按方案 A 答复"
	f.q.Revision++
	_, err = f.run()
	assertVersionDiagnostic(t, err, "unit_version", f.q.Revision, f.unit(t).Revision)
	f.q.Revision--
	f.q.Authority.RuleID, f.q.Authority.RuleVersion = "rule", 1
	if _, err := f.run(); !errors.Is(err, ErrRequest) || !strings.Contains(err.Error(), "authority") || f.calls.Load() != 0 {
		t.Fatal(err)
	}
	f.q.Authority.RuleID, f.q.Authority.RuleVersion = "", 0
	first, err := f.run()
	if err != nil || first.Status != "succeeded" {
		t.Fatal(first, err)
	}
	f.q.TurnNumber, f.q.Revision = 99, 99
	if again, err := f.run(); err != nil || !reflect.DeepEqual(again, first) || f.calls.Load() != 1 {
		t.Fatal("same ID replay changed", again, err)
	}
	f.q.Input.Body = "different body"
	if _, err := f.run(); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "different business content") || f.calls.Load() != 1 {
		t.Fatal(err)
	}
}

func assertVersionDiagnostic(t *testing.T, err error, field string, expected, actual uint64) {
	t.Helper()
	var d *requestDiagnostic
	if !errors.Is(err, ErrConflict) || !errors.As(err, &d) || d.Field != field || d.ExpectedVersion == nil || *d.ExpectedVersion != expected || d.ActualVersion == nil || *d.ActualVersion != actual {
		t.Fatalf("wrong version diagnostic: %v", err)
	}
}

func TestAPIDiagnosticsDoNotEchoPrivateErrors(t *testing.T) {
	for _, err := range []error{fmt.Errorf("remote private-test-secret"), fmt.Errorf("%w: private-test-secret", ErrIdentity), fmt.Errorf("%w: private-test-secret", ErrConflict)} {
		w := httptest.NewRecorder()
		writeAPIError(w, err)
		if strings.Contains(w.Body.String(), "private-test-secret") {
			t.Fatal(w.Body)
		}
		var failure apiError
		if json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Message == "" || failure.Error == "" || failure.Code == "" {
			t.Fatal(w.Body)
		}
	}
}

func TestAgentHelpDoesNotTightenIgnoredFlagParsing(t *testing.T) {
	var calls atomic.Int32
	var got apiRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/matters/list" || json.NewDecoder(r.Body).Decode(&got) != nil {
			t.Error("unexpected request", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	env := map[string]string{"TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": "room", "TEAMX_ROOM_INSTANCE": "instance", "TEAMX_SERVICE_EPOCH": "epoch", "TEAMX_AGENT_ID": "agent", "TEAMX_MEMBERSHIP_ID": "membership"}
	err := runCLI(context.Background(), []string{"matters", "list", "--target", "ignored", "--version", "7", "--peek"}, io.Discard, io.Discard, func(k string) string { return env[k] })
	if err != nil || calls.Load() != 1 || got.Followup == nil || got.Followup.TargetRef != "ignored" || got.Followup.Version != 7 {
		t.Fatal("previously accepted flags changed", got, err)
	}
}

func TestAgentHTTPJSONDiagnosticsAndLegacyError(t *testing.T) {
	h, svc, _, _ := conversationHarness(t, 1)
	svc.requests = make(chan struct{}, 32)
	w := httptest.NewRecorder()
	svc.ServeHTTP(w, httptest.NewRequest("POST", "/v1/matters/update", strings.NewReader(`{"followup":{"unit_version":"private-test-secret"}}`)))
	var failure apiError
	if json.Unmarshal(w.Body.Bytes(), &failure) != nil || w.Code != 400 || failure.Code != "request" || failure.Field != "followup.unit_version" || strings.Contains(w.Body.String(), "private-test-secret") {
		t.Fatal(w.Code, w.Body)
	}
	// The same bounded decoder is used by the managed action endpoint.
	w = httptest.NewRecorder()
	if decodeToolRequest(w, httptest.NewRequest("POST", "/v1/actions/execute", strings.NewReader(`{"action_request":{"input":{"body":false}}}`)), new(apiRequest)) {
		t.Fatal("bad action JSON accepted")
	}
	if json.Unmarshal(w.Body.Bytes(), &failure) != nil || w.Code != 400 || failure.Field != "action_request.input.body" {
		t.Fatal(w.Code, w.Body)
	}
	if got := (apiError{Code: "conflict", Error: "legacy detail"}).cliError().Error(); got != "conflict: legacy detail" {
		t.Fatal(got)
	}
	// A valid bound request still produces the unprojected existing result.
	cli := entryCLI(t, svc)
	var result agentPolicyView
	if err := cli([]string{"policy", "read"}, &result); err != nil || result.MemberIdentity != h.id {
		t.Fatal(result, err)
	}
}
