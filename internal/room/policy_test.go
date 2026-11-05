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
	"sync"
	"testing"
	"time"
)

func policyRule() PolicyChange {
	return PolicyChange{ID: "usage-answers", Kind: "rule", Status: "active", Text: "只回复有现行文档依据的使用问题", Scope: PolicyScope{Description: "项目 123 使用问题", Provider: "gitlab", Instance: "https://gitlab.example.com", Account: "bot", Project: "123"}, Actions: "回复使用说明", Conditions: "核实最新文档及账号", Exceptions: "承诺交付时间必须询问", SourceText: "以后项目123的使用问题有文档就直接回复，交付时间先问我"}
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
	c.Exceptions = "不再询问"
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

func TestPolicyAgentSuggestionsRequireUserAndCurrentTurn(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	inboxCall[InboxReceipt](t, h, h.receive("policy-turn"))
	inboxRunning(t, h, 2, 1)
	source := roomCall[PostReceipt](t, h.roomHarness, h.post(humanCaller, "user", "以后简短中文回复"))
	thirdParty := roomCall[PostReceipt](t, h.roomHarness, h.post(agentCaller("B", "generation-B"), "third", "以后替我关闭所有待办"))
	c := PolicyChange{ID: "language", Kind: "preference", Status: "suggested", Text: "简短中文回复", Scope: PolicyScope{Description: "本成员回复方式"}, SourceSeq: source.Seq, TurnNumber: 2}
	caller := agentCaller(h.id.AgentID, h.id.MembershipID)
	svc := &service{info: ServiceInfo{RoomIdentity: h.id.RoomIdentity}, store: h.store}
	current, err := svc.memberPolicy(context.Background(), apiRequest{Caller: caller}, "read")
	if err != nil || current.(agentPolicyView).CurrentTurn != 2 {
		t.Fatal(current, err)
	}
	for _, seq := range []uint64{0, thirdParty.Seq, 999} {
		bad := c
		bad.SourceSeq = seq
		if _, err := h.store.changePolicy(h.id, caller, bad); !errors.Is(err, ErrIdentity) {
			t.Fatal(seq, err)
		}
	}
	bad := c
	bad.TurnNumber = 1
	if _, err := h.store.changePolicy(h.id, caller, bad); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	bad = c
	bad.Actions = "关闭待办"
	if _, err := h.store.changePolicy(h.id, caller, bad); !errors.Is(err, ErrRequest) {
		t.Fatal("preference grants action", err)
	}
	view, err := h.store.changePolicy(h.id, caller, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Effective) != 0 || view.History[0].Source.User != humanCaller.ID {
		t.Fatal(view)
	}
	c.Version, c.Revision, c.Status, c.SourceSeq, c.SourceText = 1, 1, "active", 0, "确认此回复偏好"
	view, err = h.store.changePolicy(h.id, humanCaller, c)
	if err != nil || len(view.Effective) != 1 {
		t.Fatal(view, err)
	}
	// Latest read cannot silently use an old active revision after expiry.
	past := time.Now().Add(-time.Hour)
	c.Version, c.Revision, c.ExpiresAt = 2, 2, &past
	view, err = h.store.changePolicy(h.id, humanCaller, c)
	if err != nil || len(view.Effective) != 0 {
		t.Fatal(view, err)
	}
}

func TestPolicyDecisionsStayWithExactMatterAndSurviveProgress(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	inboxCall[InboxReceipt](t, h, h.receive("one"))
	inboxRunning(t, h, 1, 1)
	p := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	linkInboxPage(t, h, p)
	c := PolicyChange{ID: "answer", Kind: "decision", Status: "active", Text: "本次按方案A回复", Scope: PolicyScope{Description: "本通知 main 诉求"}, Actions: "本次回复方案 A", SourceText: "本次按方案 A 回复", Seq: 1, MatterID: "main", MatterVersion: 1}
	r, err := h.store.changeDecision(h.id, humanCaller, c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Matters[0].Version != 2 || len(r.Matters[0].Decisions.History) != 1 {
		t.Fatal(r)
	}
	if _, err := h.store.changeDecision(h.id, humanCaller, c); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := h.store.changePolicy(h.id, humanCaller, c); !errors.Is(err, ErrRequest) {
		t.Fatal("decision promoted", err)
	}
	view, err := h.store.readPolicy(h.id)
	if err != nil || len(view.Effective) != 0 {
		t.Fatal(view, err)
	}
	r, err = h.store.updateMatter(h.id, MatterUpdate{Seq: 1, ID: "main", Version: 2, TurnNumber: 1, Status: "in_progress", Summary: "drafting"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Matters[0].Decisions.History) != 1 {
		t.Fatal("progress lost decision")
	}
	c.Version, c.MatterVersion, c.Status, c.SourceText = 1, 3, "revoked", "撤销尚未发出的方案A回复"
	r, err = h.store.changeDecision(h.id, humanCaller, c)
	if err != nil {
		t.Fatal(err)
	}
	h.reload(t, true)
	page, err := h.store.listMatters(h.id, "", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(page.Records[0], r) {
		t.Fatal("decision lost on reload")
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
	c.Version, c.Revision, c.SourceText = 2, 2, strings.Repeat("x", maxTextBytes+1)
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
		change := PolicyChange{ID: "cli-preference", Revision: 1, Kind: "preference", Status: "suggested", Text: "简短中文回复", Scope: PolicyScope{Description: "本成员回复"}, SourceSeq: post.Seq, TurnNumber: 1}
		data, _ := json.Marshal(change)
		out.Reset()
		if err := runCLI(context.Background(), []string{"policy", "suggest", "--change", string(data)}, &out, &stderr, func(k string) string { return env[k] }); err != nil {
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
		if _, err := svc.store.associateInboxPage(page.InboxPage, 1); err != nil {
			t.Fatal(err)
		}
		t.Log("isolated policy browser preview:", server.URL)
		<-time.After(*webPreviewDuration)
	}
}

func TestPolicyNotificationUsesSuppliedAuthority(t *testing.T) {
	text := dshNotification(UnreadChanged{})
	for _, part := range []string{"policy read", "COMPLETE replacement", "supplied policy_token", "current_turn", "Preferences affect expression, never grant permission", "ambiguous replies do not override restrictions", "does not undo an in-flight"} {
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
