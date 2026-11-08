package room

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func policyRule() PolicyChange {
	return PolicyChange{ID: "usage-answers", Status: "active", Text: "有现行文档依据的使用问题，核实最新文档和账号后回复使用说明；承诺交付时间必须询问", SourceText: "以后项目123的使用问题有文档就直接回复，交付时间先问我"}
}

func TestEffectivePolicyUsesLatestRevisionOfEachRule(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Second)
	for _, tc := range []struct {
		name    string
		status  string
		expires *time.Time
		active  bool
	}{
		{name: "amended", status: "active", active: true},
		{name: "future expiry", status: "active", expires: &future, active: true},
		{name: "suggested replacement", status: "suggested"},
		{name: "revoked", status: "revoked"},
		{name: "expired", status: "active", expires: &past},
		{name: "expires now", status: "active", expires: &now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := PolicyRevision{ID: "owner", Version: 1, Status: "active", Text: "负责人甲"}
			original := PolicyRevision{ID: "style", Version: 1, Status: "active", Text: "简短"}
			latest := PolicyRevision{ID: "style", Version: 2, Status: tc.status, Text: "先给结论", ExpiresAt: tc.expires}
			history := []PolicyRevision{original, owner, latest}
			current := policyCurrent(history)
			if !reflect.DeepEqual(current, []PolicyRevision{latest, owner}) {
				t.Fatal("current view lost an independent rule or returned an older version", current)
			}
			want := []PolicyRevision{owner}
			if tc.active {
				want = []PolicyRevision{latest, owner}
			}
			if got := effectivePolicy(history, now); !reflect.DeepEqual(got, want) {
				t.Fatal("effective view revived an older version or dropped another rule", got)
			}
		})
	}
	if got := effectivePolicy(nil, now); got == nil || len(got) != 0 {
		t.Fatal("empty effective policy must encode as []", got)
	}
}

func TestPolicyNaturalBodyLifecycle(t *testing.T) {
	h, id, _ := policyTestService(t)
	empty, err := h.store.readPolicy(id)
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
		saved, err := h.store.changePolicy(id, humanCaller, c)
		if err != nil || saved.Version != 1 || saved.Revision != version {
			t.Fatal(saved, err)
		}
		assertNaturalPolicyJSON(t, saved.History[len(saved.History)-1])
		h.reload(t, true)
		restored, err := h.store.readPolicy(id)
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
		h, id, _ := policyTestService(t)
		c := policyRule()
		c.Text = text
		if _, err := h.store.changePolicy(id, humanCaller, c); !errors.Is(err, ErrRequest) {
			t.Fatal(err)
		}
	}
}

func TestPolicyProvenanceRestrictionsAndClear(t *testing.T) {
	h, id, _ := policyTestService(t)
	posted := roomCall[PostReceipt](t, h, h.post(humanCaller, "policy-user", "以后回复简短中文，交付时间先问我"))
	c := policyRule()
	c.SourceSeq = posted.Seq
	c.SourceText = ""
	view, err := h.store.changePolicy(id, humanCaller, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Effective) != 1 || view.Current[0].Source.Text != "以后回复简短中文，交付时间先问我" {
		t.Fatal(view)
	}
	agent := agentCaller(id.AgentID, id.MembershipID)
	// Neither a real source nor a vague new reply lets a model replace restrictions.
	roomCall[PostReceipt](t, h, h.post(humanCaller, "vague", "好"))
	c.Version, c.Revision, c.SourceSeq, c.TurnNumber = 1, 1, posted.Seq+1, 1
	c.Text = "不再询问"
	c.Status = "active"
	if _, err := h.store.changePolicy(id, agent, c); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	c.Status = "suggested"
	if _, err := h.store.changePolicy(id, agent, c); !errors.Is(err, ErrIdentity) {
		t.Fatal("active rule can be deactivated by model", err)
	}
	// Revocation is immediately visible without replacing the Session.
	c = policyRule()
	c.Version, c.Revision, c.Status, c.SourceText = 1, 1, "revoked", "撤回此规则，以后先问我"
	view, err = h.store.changePolicy(id, humanCaller, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Effective) != 0 || len(view.History) != 2 {
		t.Fatal(view)
	}
	roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: posted.Seq + 1})
	h.reload(t, true)
	got, err := h.store.readPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, view) {
		t.Fatal("policy/evidence changed after clear and reload")
	}
	// Ownership remains attached to the user who supplied the instruction.
	c.Version, c.Revision = 2, 2
	if _, err := h.store.changePolicy(id, Caller{Author: Author{Kind: "human", ID: "other"}}, c); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
}

func TestPolicyConcurrentVersionsCapacityAndUncertainCommit(t *testing.T) {
	h, id, _ := policyTestService(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.store.changePolicy(id, humanCaller, policyRule())
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
	if _, err := h.store.changePolicy(id, humanCaller, c); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if _, err := h.store.changePolicy(id, humanCaller, c); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	h.store.ops.syncDir = original
	h.reload(t, true)
	view, err := h.store.readPolicy(id)
	if err != nil || view.Revision != 2 {
		t.Fatal(view, err)
	}
	// Bounded evidence is rejected rather than evicting prior revisions.
	c.Version, c.Revision, c.SourceText = 2, 2, strings.Repeat("x", maxPolicyTextBytes+1)
	if _, err := h.store.changePolicy(id, humanCaller, c); !errors.Is(err, ErrRequest) {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	saved, path, err := h.store.loadPolicyLocked(id, true)
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
	if _, err := h.store.changePolicy(id, humanCaller, c); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
}

func TestPolicyMembershipIsolationAndInvalidStorage(t *testing.T) {
	h, id, _ := policyTestService(t)
	if _, err := h.store.changePolicy(id, humanCaller, policyRule()); err != nil {
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
	path := filepath.Join(h.store.dir, "agents", id.MembershipID, "policy.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"revision":0,"history":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.readPolicy(id); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad data reset", err)
	}
}

func TestPolicyEffectiveTokenIdentityContentAndExpiry(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	p := PolicySnapshot{Version: 1, Revision: 1,
		MemberIdentity: MemberIdentity{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, AgentID: "assistant", MembershipID: "member"},
		History: []PolicyRevision{{ID: "style", Version: 1, Status: "active", Text: "简短中文", ExpiresAt: &expires,
			Source: PolicySource{User: "user", Text: "以后简短中文"}, ChangedBy: humanCaller.Author, UpdatedAt: now}}}
	first := agentPolicy(p, 1, now)
	next := agentPolicy(p, 2, now.Add(time.Minute))
	if first.PolicyToken == "" || first.PolicyToken != next.PolicyToken || next.CurrentTurn != 2 || !reflect.DeepEqual(first.Effective, p.History) {
		t.Fatal("unstable or incomplete effective view", first, next)
	}
	// A new suggestion changes storage metadata only, without invalidating body.
	suggested := p.History[0]
	suggested.ID, suggested.Status, suggested.Text = "other", "suggested", "not-effective"
	p.History = append(p.History, suggested)
	p.Revision++
	if got := agentPolicy(p, 3, now); got.PolicyToken != first.PolicyToken || got.Revision != 2 {
		t.Fatal("suggestion changed effective token", got)
	}
	for _, mutate := range []func(*PolicyRevision){
		func(r *PolicyRevision) { r.Text = "new body" },
		func(r *PolicyRevision) { r.Source.Text = "new source" },
		func(r *PolicyRevision) { r.Version++ },
		func(r *PolicyRevision) { r.Status = "revoked" },
	} {
		changed := p
		changed.History = append([]PolicyRevision(nil), p.History...)
		mutate(&changed.History[0])
		if agentPolicy(changed, 1, now).PolicyToken == first.PolicyToken {
			t.Fatal("effective change did not invalidate token")
		}
	}
	for _, mutate := range []func(*MemberIdentity){
		func(id *MemberIdentity) { id.RoomID += "other" },
		func(id *MemberIdentity) { id.RoomInstance += "other" },
		func(id *MemberIdentity) { id.AgentID += "other" },
		func(id *MemberIdentity) { id.MembershipID += "other" },
	} {
		changed := p
		mutate(&changed.MemberIdentity)
		if agentPolicy(changed, 1, now).PolicyToken == first.PolicyToken {
			t.Fatal("token crossed identity scope")
		}
	}
	expired := agentPolicy(p, 4, expires)
	if len(expired.Effective) != 0 || expired.PolicyToken == first.PolicyToken || expired.Revision != p.Revision {
		t.Fatal("natural expiry failed to change token without a write", expired)
	}
}
