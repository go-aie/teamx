package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPolicyManagementHTTP(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	m := activationTestManager(t)
	m.teams = map[string]teamDefinition{"sample": testTeamDefinition(t)}
	m.factory = controlledActivationFactory(nil, nil)
	server := httptest.NewUnstartedServer(m)
	m.config.endpoint = "http://" + server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "web_policy_http.mjs")
	cmd.Env = append(os.Environ(), "TEAMX_WEB_ENDPOINT="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("policy management HTTP: %v\n%s", err, output)
	}
}

func policyRule() PolicyChange {
	return PolicyChange{ID: "usage-answers", Status: "active", Text: "有现行文档依据的使用问题，核实最新文档和账号后回复使用说明；承诺交付时间必须询问", SourceText: "以后项目123的使用问题有文档就直接回复，交付时间先问我"}
}

func TestPolicyNaturalBodyLifecycle(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	empty, err := h.store.readPolicy(h.id)
	if err != nil || empty.Version != 1 || len(empty.Effective) != 0 {
		t.Fatal(empty, err)
	}
	c := policyRule()
	for version := uint64(1); version <= 3; version++ {
		c.Version, c.Revision = version-1, version-1
		if version == 2 {
			c.Text = "核实现行安装文档；交付时间仍先问我"
		}
		if version == 3 {
			c.Status = "revoked"
		}
		saved, err := h.store.changePolicy(h.id, humanCaller, c)
		if err != nil || saved.Version != 1 || saved.Revision != version {
			t.Fatal(saved, err)
		}
		assertNaturalPolicyJSON(t, saved.History[len(saved.History)-1])
		h.reload(t, true)
		restored, err := h.store.readPolicy(h.id)
		if err != nil || !reflect.DeepEqual(saved, restored) {
			t.Fatal(restored, err)
		}
		if (version == 3) != (len(restored.Effective) == 0) {
			t.Fatal(restored)
		}
	}
}

func assertNaturalPolicyJSON(t *testing.T, record any) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"kind", "scope", "scope_from", "actions", "conditions", "exceptions"} {
		if _, exists := fields[field]; exists {
			t.Fatalf("removed field %s in %s", field, data)
		}
	}
}

func TestPolicyBodyValidation(t *testing.T) {
	for _, text := range []string{"", " ", strings.Repeat("x", maxPolicyTextBytes+1)} {
		h := newInboxHarness(t, InboxLimits{})
		c := policyRule()
		c.Text = text
		if _, err := h.store.changePolicy(h.id, humanCaller, c); !errors.Is(err, ErrRequest) {
			t.Fatal(err)
		}
	}
}

func TestPolicyProvenanceRestrictionsAndClear(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	inboxCall[InboxReceipt](t, h, h.receive("policy-turn"))
	inboxRunning(t, h, 1, 1)
	posted := roomCall[PostReceipt](t, h.roomHarness, h.post(humanCaller, "policy-user", "以后回复简短中文，交付时间先问我"))
	c := policyRule()
	c.SourceSeq = posted.Seq
	c.SourceText = ""
	view, err := h.store.changePolicy(h.id, humanCaller, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Effective) != 1 || view.Current[0].Source.Text != "以后回复简短中文，交付时间先问我" {
		t.Fatal(view)
	}
	agent := agentCaller(h.id.AgentID, h.id.MembershipID)
	// Neither a real source nor a vague new reply lets a model replace restrictions.
	roomCall[PostReceipt](t, h.roomHarness, h.post(humanCaller, "vague", "好"))
	c.Version, c.Revision, c.SourceSeq, c.TurnNumber = 1, 1, posted.Seq+1, 1
	c.Text = "不再询问"
	c.Status = "active"
	if _, err := h.store.changePolicy(h.id, agent, c); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	c.Status = "suggested"
	if _, err := h.store.changePolicy(h.id, agent, c); !errors.Is(err, ErrIdentity) {
		t.Fatal("active rule can be deactivated by model", err)
	}
	// Revocation is immediately visible without replacing the Session.
	c = policyRule()
	c.Version, c.Revision, c.Status, c.SourceText = 1, 1, "revoked", "撤回此规则，以后先问我"
	view, err = h.store.changePolicy(h.id, humanCaller, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Effective) != 0 || len(view.History) != 2 {
		t.Fatal(view)
	}
	roomCall[ClearReceipt](t, h.roomHarness, ClearMessages{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: posted.Seq + 1})
	h.reload(t, true)
	got, err := h.store.readPolicy(h.id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, view) {
		t.Fatal("policy/evidence changed after clear and reload")
	}
	// Ownership remains attached to the user who supplied the instruction.
	c.Version, c.Revision = 2, 2
	if _, err := h.store.changePolicy(h.id, Caller{Author: Author{Kind: "human", ID: "other"}}, c); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
}

func TestPolicyConcurrentVersionsCapacityAndUncertainCommit(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.store.changePolicy(h.id, humanCaller, policyRule())
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
	c := policyRule()
	c.Version, c.Revision = 1, 1
	original := h.store.ops.syncDir
	h.store.ops.syncDir = func(string) error { return errors.New("fsync failed") }
	if _, err := h.store.changePolicy(h.id, humanCaller, c); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if _, err := h.store.changePolicy(h.id, humanCaller, c); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	h.store.ops.syncDir = original
	h.reload(t, true)
	view, err := h.store.readPolicy(h.id)
	if err != nil || view.Revision != 2 {
		t.Fatal(view, err)
	}
	// Bounded evidence is rejected rather than evicting prior revisions.
	c.Version, c.Revision, c.SourceText = 2, 2, strings.Repeat("x", maxPolicyTextBytes+1)
	if _, err := h.store.changePolicy(h.id, humanCaller, c); !errors.Is(err, ErrRequest) {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	saved, path, err := h.store.loadPolicyLocked(h.id, true)
	if err != nil {
		h.store.mu.Unlock()
		t.Fatal(err)
	}
	for len(saved.History) < maxPolicyRevisions {
		r := saved.History[len(saved.History)-1]
		r.Version++
		saved.History = append(saved.History, r)
		saved.Revision++
	}
	err = h.store.save(path, saved, maxPrivateFileBytes)
	h.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c = policyRule()
	c.Revision = saved.Revision
	c.Version = saved.History[len(saved.History)-1].Version
	if _, err := h.store.changePolicy(h.id, humanCaller, c); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
}

func TestPolicyMembershipIsolationAndInvalidStorage(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	if _, err := h.store.changePolicy(h.id, humanCaller, policyRule()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []MemberIdentity{h.memberID("B", "generation-B"), h.memberID("A", "new-generation")} {
		view, err := h.store.readPolicy(id)
		if id.AgentID == "B" {
			if err != nil || len(view.History) != 0 {
				t.Fatal(view, err)
			}
		} else if !errors.Is(err, ErrIdentity) {
			t.Fatal(err)
		}
	}
	path := filepath.Join(h.store.dir, "agents", h.id.MembershipID, "policy.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"revision":0,"history":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.readPolicy(h.id); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad data reset", err)
	}
}

func TestPolicyHTTPCLIAndBrowserPreview(t *testing.T) {
	svc, err := startService(t.TempDir(), "policy-http", serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := svc.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	server := httptest.NewServer(svc)
	defer server.Close()
	call := func(path string, req apiRequest) (*httptest.ResponseRecorder, []byte) {
		t.Helper()
		data, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		svc.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data)))
		return w, w.Body.Bytes()
	}
	previewCaller := humanCaller
	if *webPreviewDuration > 0 {
		previewCaller.ID = humanOperator()
	}
	req := apiRequest{ServiceInfo: svc.info, Caller: previewCaller, AgentID: id.AgentID, MembershipID: id.MembershipID, Policy: new(PolicyChange)}
	*req.Policy = policyRule()
	w, _ := call("/v1/policy/update", req)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body)
	}
	req.Caller = agentCaller(id.AgentID, id.MembershipID)
	w, _ = call("/v1/policy/read", req)
	if w.Code != 403 {
		t.Fatal("agent selected membership", w.Code)
	}
	req.AgentID, req.MembershipID = "", ""
	w, _ = call("/v1/policy/read", req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	env := map[string]string{"TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": id.RoomID, "TEAMX_ROOM_INSTANCE": id.RoomInstance, "TEAMX_SERVICE_EPOCH": svc.info.ServiceEpoch, "TEAMX_AGENT_ID": id.AgentID, "TEAMX_MEMBERSHIP_ID": id.MembershipID}
	var out, stderr bytes.Buffer
	if err := runCLI(context.Background(), []string{"policy", "read"}, &out, &stderr, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "usage-answers") {
		t.Fatal(out.String())
	}
	if *webPreviewDuration == 0 {
		post, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "policy-cli-source", Text: "以后用简短中文回复"})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: post.Seq, Turn: &TurnRecord{Number: 1, Status: "running", OfferedUnreadSeq: post.Seq, SnapshotSeq: post.Seq}}); err != nil {
			t.Fatal(err)
		}
		change := PolicyChange{ID: "cli-preference", Revision: 1, Status: "suggested", Text: "简短中文回复", SourceSeq: post.Seq, TurnNumber: 1}
		data, _ := json.Marshal(agentPolicyFixture(conversationPolicyChange{Change: change, Mode: "inferred_habit", Basis: "用户本次表达仅支持提出长期建议"}))
		out.Reset()
		if err := runCLI(context.Background(), []string{"policy", "update", "--change", string(data)}, &out, &stderr, func(k string) string { return env[k] }); err != nil {
			t.Fatal(err)
		}
		view, err := svc.store.readPolicy(id)
		if err != nil || view.Revision != 2 || len(view.Effective) != 1 || view.Current[1].Status != "suggested" {
			t.Fatal(view, err)
		}
	}
	if *webPreviewDuration > 0 {
		svc.inboxMu.Lock()
		addr, err := svc.inboxLocked(context.Background(), id)
		svc.inboxMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		item := (&inboxHarness{id: id}).receive("browser-policy").Item
		if _, err := addr.Call[InboxReceipt](context.Background(), ReceiveInbox{MemberIdentity: id, Item: item}); err != nil {
			t.Fatal(err)
		}
		if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); err != nil {
			t.Fatal(err)
		}
		page, err := addr.Call[InboxBatchPage](context.Background(), ReadInboxBatch{MemberIdentity: id, TurnNumber: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.store.initReferenceFollowups(id); err != nil {
			t.Fatal(err)
		}
		for _, entry := range page.Entries {
			if _, err := svc.resolveFollowupEvent(context.Background(), id, entry.Seq, 1, false, nil); err != nil {
				t.Fatal(err)
			}
		}

		t.Log("isolated policy browser preview:", server.URL)
		<-time.After(*webPreviewDuration)
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
