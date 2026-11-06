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

func TestPolicyAppHTTP(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	m := activationTestManager(t)
	m.teams = map[string]teamDefinition{"sample": testTeamDefinition(t)}
	m.factory = controlledActivationFactory(nil, nil)
	m.platformVerify = func(context.Context, string, workspaceSecret) platformConnectionValidation {
		return platformConnectionValidation{Status: "valid", UserID: 101, Username: "policy-test", CheckedAt: time.Now().UTC()}
	}
	server := httptest.NewUnstartedServer(m)
	m.config.endpoint = "http://" + server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "web_policy_http.mjs")
	cmd.Env = append(os.Environ(), "TEAMX_WEB_ENDPOINT="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("policy app/HTTP: %v\n%s", err, output)
	}
	if *webPreviewDuration > 0 {
		t.Log("isolated policy browser preview:", server.URL)
		<-time.After(*webPreviewDuration)
	}
}

func policyRule() PolicyChange {
	return PolicyChange{ID: "usage-answers", Kind: "rule", Status: "active", Text: "有现行文档依据的使用问题，核实最新文档和账号后回复使用说明；承诺交付时间必须询问", Scope: PolicyScope{Provider: "gitlab", Instance: "https://gitlab.example.com", Account: "bot", Project: "123"}, SourceText: "以后项目123的使用问题有文档就直接回复，交付时间先问我"}
}

func TestPolicyNaturalBodyAndLegacyHistory(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	c := policyRule()
	v, err := h.store.changePolicy(h.id, humanCaller, c)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(v.Current[0])
	for _, field := range []string{`"description"`, `"actions"`, `"conditions"`, `"exceptions"`} {
		if bytes.Contains(encoded, []byte(field)) {
			t.Fatalf("new rule retained split prose %s: %s", field, encoded)
		}
	}
	// Simulate a policy written before simplification. Its evidence stays intact.
	h.store.mu.Lock()
	p, path, err := h.store.loadPolicyLocked(h.id, true)
	if err == nil {
		p.History[0].Text = "回复使用问题"
		p.History[0].Scope.Description = "仅安装咨询"
		p.History[0].Actions = "回复说明"
		p.History[0].Conditions = "核实现行文档"
		p.History[0].Exceptions = "涉及交付时间先问我"
		err = h.store.save(path, p, maxPrivateFileBytes)
	}
	h.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.reload(t, true)
	v, err = h.store.readPolicy(h.id)
	if err != nil || !reflect.DeepEqual(v.History, p.History) {
		t.Fatal("legacy history changed", v, err)
	}
	for _, part := range []string{"回复使用问题", "仅安装咨询", "回复说明", "核实现行文档", "涉及交付时间先问我"} {
		if !strings.Contains(v.Current[0].Text, part) || !strings.Contains(v.Effective[0].Text, part) {
			t.Fatalf("lost legacy restriction %q", part)
		}
	}
	c.Text, c.Scope, c.Version, c.Revision = v.Current[0].Text, v.Current[0].Scope, 1, 1
	c.SourceText = "保留原有范围和限制"
	v, err = h.store.changePolicy(h.id, humanCaller, c)
	if err != nil || !reflect.DeepEqual(v.History[0], p.History[0]) || v.Current[0].Text != c.Text {
		t.Fatal("editing lost legacy evidence or body", v, err)
	}
	encoded, _ = json.Marshal(v.Current[0])
	if bytes.Contains(encoded, []byte(`"exceptions"`)) || bytes.Contains(encoded, []byte(`"description"`)) {
		t.Fatal("new revision kept legacy fields", string(encoded))
	}
	c.Version, c.Revision, c.Status = 2, 2, "revoked"
	if v, err = h.store.changePolicy(h.id, humanCaller, c); err != nil || len(v.Effective) != 0 {
		t.Fatal(v, err)
	}
}

func TestPolicyRequiresOnlyMachineTargetScope(t *testing.T) {
	for _, mode := range []string{"preference", "rule", "description", "missing-account", "missing-project", "wildcard"} {
		t.Run(mode, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			c := policyRule()
			switch mode {
			case "preference":
				c.Kind, c.Text, c.Scope = "preference", "仅日常回复使用简短中文", PolicyScope{}
			case "description":
				c.Scope.Description = "不再接收单独的自然语言范围说明"
			case "missing-account":
				c.Scope.Account = ""
			case "missing-project":
				c.Scope.Project = ""
			case "wildcard":
				c.Scope.Project = "*"
			}
			_, err := h.store.changePolicy(h.id, humanCaller, c)
			if mode == "preference" || mode == "rule" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrRequest) {
				t.Fatal("invalid scope accepted", err)
			}
		})
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
		change := PolicyChange{ID: "cli-preference", Revision: 1, Kind: "preference", Status: "suggested", Text: "简短中文回复", SourceSeq: post.Seq, TurnNumber: 1}
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
	for _, part := range []string{"policy read", "completely replaces", "policy_token", "current_turn", "preferences never authorize writes", "Latest explicit restrictions/revocations win", "type save", "type confirm"} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing %q", part)
		}
	}
	for _, part := range []string{"policy decisions", "Before processing each turn and immediately before any action", "read current policy/decisions"} {
		if strings.Contains(text, part) {
			t.Fatalf("obsolete reading instruction %q", part)
		}
	}
}
