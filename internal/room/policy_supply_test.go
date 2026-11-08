package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func suppliedPolicy(t *testing.T, text string) map[string]json.RawMessage {
	t.Helper()
	const prefix = "Member policy context (JSON):\n"
	if !strings.HasPrefix(text, prefix) {
		t.Fatal("missing policy context")
	}
	line, _, _ := strings.Cut(strings.TrimPrefix(text, prefix), "\n")
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"history", "current"} {
		if _, ok := got[key]; ok {
			t.Fatalf("Agent received management field %s", key)
		}
	}
	return got
}

func TestPolicySessionSupplyFirstUnchangedChangesAndRebuild(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	inboxCall[InboxReceipt](t, h, h.receive("one"))
	inboxRunning(t, h, 1, 1)
	var prompts []string
	fail := false
	b := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{run: func(_ context.Context, prompt string) (dsh.TurnResult, error) {
			prompts = append(prompts, prompt)
			if fail {
				return dsh.TurnResult{}, errors.New("rejected prompt")
			}
			return dsh.TurnResult{}, nil
		}}, nil
	}}
	s, err := b.OpenSession(h.ctx, adapterOptions(h.store, h.id))
	if err != nil {
		t.Fatal(err)
	}
	run := func(inbox bool) map[string]json.RawMessage {
		t.Helper()
		input := UnreadChanged{MemberIdentity: h.id}
		if inbox {
			input.Inbox = &InboxInput{SnapshotSeq: 1}
		}
		if err := s.Run(h.ctx, input); err != nil {
			t.Fatal(err)
		}
		return suppliedPolicy(t, prompts[len(prompts)-1])
	}
	first := run(false)
	if string(first["effective"]) != "[]" || string(first["current_turn"]) != "1" {
		t.Fatal("new Session must explicitly supply empty policy", first)
	}
	c := policyRule()
	if _, err := h.store.changePolicy(h.id, humanCaller, c); err != nil {
		t.Fatal(err)
	}
	inboxRunning(t, h, 2, 1)
	full := run(true)
	var records []PolicyRevision
	if err := json.Unmarshal(full["effective"], &records); err != nil || len(records) != 1 || records[0].Text != c.Text || records[0].Source.Text != c.SourceText {
		t.Fatal("effective body was incomplete", records, err)
	}
	inboxRunning(t, h, 3, 1)
	metadata := run(false)
	if _, ok := metadata["effective"]; ok || string(metadata["current_turn"]) != "3" || string(metadata["policy_token"]) != string(full["policy_token"]) {
		t.Fatal("unchanged continuous Session repeated body or stale turn", metadata)
	}
	fullBytes, _ := json.Marshal(full)
	metadataBytes, _ := json.Marshal(metadata)
	t.Logf("policy supply: complete context %d bytes; unchanged context %d bytes; runtime requires no policy CLI query", len(fullBytes), len(metadataBytes))
	suggestion := PolicyChange{ID: "style", Revision: 1, Status: "suggested", Text: "short", SourceText: "suggest short replies"}
	if _, err := h.store.changePolicy(h.id, humanCaller, suggestion); err != nil {
		t.Fatal(err)
	}
	metadata = run(true)
	if _, ok := metadata["effective"]; ok || string(metadata["revision"]) != "2" || string(metadata["policy_token"]) != string(full["policy_token"]) {
		t.Fatal("suggestion caused body redelivery", metadata)
	}
	c.Version, c.Revision, c.Text = 1, 2, "changed restriction"
	if _, err := h.store.changePolicy(h.id, humanCaller, c); err != nil {
		t.Fatal(err)
	}
	changed := run(false)
	if !bytes.Contains(changed["effective"], []byte(c.Text)) || bytes.Equal(changed["policy_token"], full["policy_token"]) {
		t.Fatal("changed effective policy was not replaced", changed)
	}
	c.Version, c.Revision, c.Status = 2, 3, "revoked"
	if _, err := h.store.changePolicy(h.id, humanCaller, c); err != nil {
		t.Fatal(err)
	}
	if revoked := run(true); string(revoked["effective"]) != "[]" {
		t.Fatal("revocation did not replace with an explicit empty set", revoked)
	}
	// Error is not delivery evidence, even if the policy token was unchanged.
	fail = true
	if err := s.Run(h.ctx, UnreadChanged{MemberIdentity: h.id}); err == nil {
		t.Fatal("expected prompt failure")
	}
	fail = false
	if got := run(false); string(got["effective"]) != "[]" {
		t.Fatal("failed prompt did not invalidate supply marker", got)
	}
	if err := s.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	// Recovery opens a new adapter instance even for the same backend Session.
	b.recover = true
	s, err = b.OpenSession(h.ctx, adapterOptions(h.store, h.id))
	if err != nil {
		t.Fatal(err)
	}
	if got := run(false); string(got["effective"]) != "[]" {
		t.Fatal("recovered Session inherited an in-memory marker", got)
	}
	if err := s.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	// A member's explicit rotation also starts with a complete policy body.
	rotated, err := b.RotateSession(adapterOptions(h.store, h.id), true)
	if err != nil {
		t.Fatal(err)
	}
	b = rotated.(dshBackend)
	s, err = b.OpenSession(h.ctx, adapterOptions(h.store, h.id))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(h.ctx)
	if got := run(false); string(got["effective"]) != "[]" {
		t.Fatal("rotated Session inherited an in-memory marker", got)
	}
	// Invalid authoritative storage must stop the turn before invoking a model.
	count := len(prompts)
	path := filepath.Join(h.store.dir, "agents", h.id.MembershipID, "policy.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(h.ctx, UnreadChanged{MemberIdentity: h.id}); !errors.Is(err, ErrInvalid) || len(prompts) != count {
		t.Fatal("invalid policy reached model", err)
	}
}

func TestPolicySessionNaturalExpiryRefreshesWithoutRevisionChange(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	c := policyRule()
	expires := time.Now().Add(time.Second)
	c.ExpiresAt = &expires
	if _, err := h.store.changePolicy(h.id, humanCaller, c); err != nil {
		t.Fatal(err)
	}
	var prompts []string
	s := &dshSession{backend: dshBackend{store: h.store}, opts: adapterOptions(h.store, h.id),
		component: &adapterComponent{run: func(_ context.Context, prompt string) (dsh.TurnResult, error) {
			prompts = append(prompts, prompt)
			return dsh.TurnResult{}, nil
		}}}
	input := UnreadChanged{MemberIdentity: h.id}
	if err := s.Run(h.ctx, input); err != nil {
		t.Fatal(err)
	}
	first := suppliedPolicy(t, prompts[0])
	if string(first["effective"]) == "[]" {
		t.Fatal("rule expired before initial supply")
	}
	time.Sleep(time.Until(expires))
	if err := s.Run(h.ctx, input); err != nil {
		t.Fatal(err)
	}
	next := suppliedPolicy(t, prompts[1])
	if string(next["effective"]) != "[]" || !bytes.Equal(first["revision"], next["revision"]) || bytes.Equal(first["policy_token"], next["policy_token"]) {
		t.Fatal("expiry did not refresh body with unchanged revision", next)
	}
}

func TestPolicyAgentRecoveryReadAndManagementHistory(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	c := policyRule()
	if _, err := h.store.changePolicy(h.id, humanCaller, c); err != nil {
		t.Fatal(err)
	}
	c.Version, c.Revision, c.Text = 1, 1, strings.Repeat("完整限制", 300)
	if _, err := h.store.changePolicy(h.id, humanCaller, c); err != nil {
		t.Fatal(err)
	}
	svc := &service{info: ServiceInfo{RoomIdentity: h.id.RoomIdentity, ServiceEpoch: "test-service"}, store: h.store,
		room: h.roomHarness.addr, requests: make(chan struct{}, 32)}
	server := httptest.NewServer(svc)
	defer server.Close()
	env := map[string]string{"TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": h.id.RoomID, "TEAMX_ROOM_INSTANCE": h.id.RoomInstance, "TEAMX_SERVICE_EPOCH": svc.info.ServiceEpoch, "TEAMX_AGENT_ID": h.id.AgentID, "TEAMX_MEMBERSHIP_ID": h.id.MembershipID}
	var out, stderr bytes.Buffer
	if err := runCLI(h.ctx, []string{"policy", "read"}, &out, &stderr, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err, stderr.String())
	}
	var got agentPolicyView
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Effective) != 1 || got.Effective[0].Text != c.Text || got.PolicyToken == "" {
		t.Fatal("recovery view truncated or invalid", out.String(), err)
	}
	for _, key := range []string{`"history"`, `"current"`} {
		if bytes.Contains(out.Bytes(), []byte(key)) {
			t.Fatal("Agent read leaked management history", key)
		}
	}
	view, err := svc.memberPolicy(h.ctx, apiRequest{Caller: humanCaller, AgentID: h.id.AgentID, MembershipID: h.id.MembershipID}, "read")
	if err != nil || len(view.(PolicyView).History) != 2 {
		t.Fatal("management history lost", view, err)
	}
	if err := runCLI(h.ctx, []string{"policy", "decisions"}, &out, &stderr, func(k string) string { return env[k] }); !errors.Is(err, ErrRequest) {
		t.Fatal("retired Agent command accepted", err)
	}
	if _, err := svc.memberPolicy(h.ctx, apiRequest{Caller: agentCaller(h.id.AgentID, h.id.MembershipID)}, "decisions/read"); !errors.Is(err, ErrRequest) {
		t.Fatal("Agent can still query management decisions", err)
	}
}

func TestPolicyNotificationUsesSuppliedAuthority(t *testing.T) {
	text := dshNotification(UnreadChanged{})
	for _, part := range []string{"policy read", "completely replaces", "policy_token", "current_turn", "Reply style never authorizes external writes", "body is {text,expires_at?}", "ALL current restrictions", "does not verify natural-language applicability", "AgenticHub 产品负责人是 huayi", "hongwei 和 luojun", "saving one starts no listener", "pause only the affected operation", "Latest explicit restrictions/revocations win", "type save", "type confirm"} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %q", part)
		}
	}
	for _, part := range []string{"policy decisions", "Before processing each turn and immediately before any action", "read current policy/decisions", "Rules may replace body.scope", "Preferences need no target scope", "body?,scope_from?"} {
		if strings.Contains(text, part) {
			t.Fatalf("obsolete reading instruction %q", part)
		}
	}
}
