package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-aie/teamx/internal/platform/tool"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type executeFixture struct {
	m   *roomManager
	svc *service
	id  MemberIdentity
	req apiRequest
	q   ActionRequest
	dir string
}

func newExecuteFixture(t *testing.T, results ...string) *executeFixture {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`test "$1" = __action-mr-comment || exit 90
cat > /dev/null
if test "$2" = inspect; then
 printf '%%s' '{"status":"verified","project_id":123,"mr_id":9001,"mr_iid":42,"project":"a/b","url":"https://gitlab.invalid/a/b/-/merge_requests/42","user_id":101,"username":"user101"}'
 exit 0
fi
printf x >> %q/sends
n=$(wc -c < %q/sends | tr -d ' ')
while test -f %q/gate; do sleep 0.02; done
cat %q/result-$n
`, dir, dir, dir, dir)
	m, room, svc, c, members := cliToolFixture(t, script)
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: members[0].AgentID, MembershipID: members[0].MembershipID}
	h := &inboxHarness{id: id}
	msg := h.receive("execute")
	msg.Item.Source.Instance = "https://gitlab.invalid"
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), msg.Item); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	e, err := svc.store.inboxEntryLocked(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.associateInboxPage(InboxPage{MemberIdentity: id, SnapshotSeq: 1, Entries: []InboxEntry{e}}, 1); err != nil {
		t.Fatal(err)
	}
	rule := policyRule()
	rule.Scope.Instance = "https://gitlab.invalid"
	rule.Scope.Account = "user101"
	if _, err := svc.store.changePolicy(id, humanCaller, rule); err != nil {
		t.Fatal(err)
	}
	p, err := svc.store.readAgentPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	q := ActionRequest{MatterCommand: MatterCommand{Seq: 1, MatterID: "main", MatterVersion: 1, TurnNumber: 1}, ID: "reply-1", PolicyToken: p.PolicyToken, Authority: ActionAuthority{RuleID: rule.ID, RuleVersion: 1, Reason: "verified usage answer"}, Verification: "MR and current docs checked", Operation: "gitlab.mr.comment", Target: CommentTarget{"123", "42"}, Input: CommentInput{"exact ordinary reply"}}
	f := &executeFixture{m, svc, id, apiRequest{ServiceInfo: svc.info, Caller: agentCaller(id.AgentID, id.MembershipID)}, q, dir}
	for i, r := range results {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("result-%d", i+1)), []byte(r), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

const executeOK = `{"status":"succeeded","remote_id":"99","mr_id":9001,"user_id":101,"username":"user101","message":"exact comment returned"}`
const executeTransient = `{"status":"unknown","retryable":true,"message":"connection timed out"}`
const executeDenied = `{"status":"not_applied","message":"HTTP 403"}`

func (f *executeFixture) run(ctx context.Context) (ActionReceipt, error) {
	return f.m.executeAction(ctx, f.req, f.q)
}
func (f *executeFixture) sends() int {
	b, _ := os.ReadFile(filepath.Join(f.dir, "sends"))
	return len(b)
}
func (f *executeFixture) matter(t *testing.T) Matter {
	t.Helper()
	p, err := f.svc.store.listMatters(f.id, "", 0, 25)
	if err != nil {
		t.Fatal(err)
	}
	return p.Records[0].Matters[0]
}
func TestActionExecuteOutcomesAndRetryBudget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		results   []string
		status    string
		attempts  int
		duplicate bool
	}{
		{"success", []string{executeOK}, "succeeded", 1, false},
		{"retry-success", []string{executeTransient, executeOK}, "succeeded", 2, true},
		{"permission-denied", []string{executeDenied}, "not_applied", 1, false},
		{"unknown-then-denied", []string{executeTransient, executeDenied}, "unknown", 2, true},
		{"exhausted", []string{executeTransient, executeTransient}, "unknown", 2, true},
		{"known-transient-rejection", []string{`{"status":"not_applied","retryable":true,"message":"HTTP 429"}`, executeOK}, "succeeded", 2, false},
		{"malformed-output", []string{"not-json"}, "unknown", 1, false},
		{"long-message", []string{fmt.Sprintf(`{"status":"unknown","message":%q}`, strings.Repeat("x", 4090))}, "unknown", 1, false},
		{"wrong-result-account", []string{strings.Replace(executeOK, "user101", "somebody-else", 1)}, "unknown", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExecuteFixture(t, tc.results...)
			got, err := f.run(context.Background())
			if err != nil || got.Status != tc.status || got.Attempts != tc.attempts || got.PossibleDuplicate != tc.duplicate || f.sends() != tc.attempts {
				t.Fatal(got, err, f.sends())
			}
			m := f.matter(t)
			a := m.Actions[0]
			if m.Status == "completed" || a.Execution == nil || a.Intent != f.q.Input.Body || a.Status != tc.status || a.Attempts != tc.attempts || a.Authority.PolicyRevision != 1 {
				t.Fatal(m)
			}
			if got.Status == "succeeded" && (got.RemoteID != "99" || !strings.HasSuffix(got.URL, "#note_99")) {
				t.Fatal(got)
			}
			// Same ID bypasses only new-action version/turn/token checks, never identity.
			f.q.TurnNumber = 888
			f.q.MatterVersion = 999
			f.q.PolicyToken = "obsolete"
			repeated, err := f.run(context.Background())
			if err != nil || repeated.Status != got.Status || f.sends() != tc.attempts {
				t.Fatal(repeated, err)
			}
			f.q.Input.Body = "changed content"
			if _, err := f.run(context.Background()); !errors.Is(err, ErrConflict) {
				t.Fatal("changed ID content accepted", err)
			}
		})
	}
}
func TestActionExecuteUnknownAllowsUnrelatedWork(t *testing.T) {
	f := newExecuteFixture(t, executeTransient, executeTransient, executeOK)
	got, err := f.run(context.Background())
	if err != nil || got.Status != "unknown" {
		t.Fatal(got, err)
	}
	f.q.MatterVersion = got.MatterVersion
	f.q.ID = "new-id"
	if _, err := f.run(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatal("uncertain comment recreated", err)
	}
	f.q.Input.Body = "another independent ordinary comment"
	got, err = f.run(context.Background())
	if err != nil || got.Status != "succeeded" || f.sends() != 3 {
		t.Fatal(got, err)
	}
	m := f.matter(t)
	if len(m.Actions) != 2 || m.Actions[0].Status != "unknown" {
		t.Fatal(m)
	}
	if _, err := f.svc.store.updateMatter(f.id, MatterUpdate{Seq: 1, ID: "main", Version: m.Version, TurnNumber: 1, Status: "completed", Summary: "done", Evidence: "guess"}); !errors.Is(err, ErrConflict) {
		t.Fatal("unknown treated as completion", err)
	}
}
func TestActionExecutePolicyChangesAndSuggestionRevision(t *testing.T) {
	t.Run("changed", func(t *testing.T) {
		f := newExecuteFixture(t, executeOK)
		rule := policyRule()
		rule.Version = 1
		rule.Revision = 1
		rule.Status = "revoked"
		if _, err := f.svc.store.changePolicy(f.id, humanCaller, rule); err != nil {
			t.Fatal(err)
		}
		got, err := f.run(context.Background())
		if err != nil || got.Status != "policy_changed" || got.Policy == nil || len(got.Policy.Effective) != 0 || f.sends() != 0 {
			t.Fatal(got, err)
		}
	})
	t.Run("suggestion", func(t *testing.T) {
		f := newExecuteFixture(t, executeOK)
		rule := policyRule()
		rule.ID = "suggestion"
		rule.Status = "suggested"
		rule.Revision = 1
		if _, err := f.svc.store.changePolicy(f.id, humanCaller, rule); err != nil {
			t.Fatal(err)
		}
		got, err := f.run(context.Background())
		if err != nil || got.Status != "succeeded" || f.matter(t).Actions[0].Authority.PolicyRevision != 2 {
			t.Fatal(got, err)
		}
	})
}
func waitActionSend(t *testing.T, f *executeFixture) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.sends() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("adapter did not start")
}
func TestActionExecuteConcurrentIDAndRetryRechecks(t *testing.T) {
	for _, kind := range []string{"duplicate", "revoked-rule", "expired-decision", "binding", "cancel", "turn"} {
		t.Run(kind, func(t *testing.T) {
			f := newExecuteFixture(t, executeTransient, executeOK)
			if kind == "expired-decision" {
				expires := time.Now().Add(2 * time.Second)
				d := PolicyChange{ID: "one-off", Kind: "decision", Status: "active", Text: "one reply", Scope: PolicyScope{Description: "exact matter"}, Actions: "reply", SourceText: "reply once", Seq: 1, MatterID: "main", MatterVersion: 1, ExpiresAt: &expires}
				r, err := f.svc.store.changeDecision(f.id, humanCaller, d)
				if err != nil {
					t.Fatal(err)
				}
				f.q.MatterVersion = r.Matters[0].Version
				f.q.Authority = ActionAuthority{DecisionID: d.ID, DecisionVersion: 1, Reason: "one exact reply"}
			}
			gate := filepath.Join(f.dir, "gate")
			if err := os.WriteFile(gate, nil, 0600); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(gate)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				r   ActionReceipt
				err error
			}
			done := make(chan outcome, 1)
			go func() { r, err := f.run(ctx); done <- outcome{r, err} }()
			waitActionSend(t, f)
			switch kind {
			case "duplicate":
				got, err := f.run(context.Background())
				if err != nil || got.Status != "processing" || got.Attempts != 1 {
					t.Fatal(got, err)
				}
			case "revoked-rule":
				c := policyRule()
				c.Revision = 1
				c.Version = 1
				c.Status = "revoked"
				if _, err := f.svc.store.changePolicy(f.id, humanCaller, c); err != nil {
					t.Fatal(err)
				}
			case "expired-decision":
				time.Sleep(2100 * time.Millisecond)
			case "binding":
				f.m.mu.Lock()
				_, _, err := f.m.changeToolBindingLocked(context.Background(), toolBindingChange{ServiceEpoch: f.m.epoch, RoomIdentity: f.id.RoomIdentity, Caller: humanCaller, Revision: 1, Binding: tool.Binding{AgentID: f.id.AgentID, MembershipID: f.id.MembershipID, Tool: "gitlab"}})
				f.m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			case "turn":
				if err := f.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: f.id, TurnNumber: 2, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 2, Status: "running", InboxSnapshotSeq: 1}}); err != nil {
					t.Fatal(err)
				}
			}
			os.Remove(gate)
			select {
			case got := <-done:
				want := "unknown"
				n := 1
				if kind == "duplicate" {
					want = "succeeded"
					n = 2
				}
				if got.err != nil || got.r.Status != want || got.r.Attempts != n || f.sends() != n || got.r.PossibleDuplicate != (n == 2) {
					t.Fatal(got, f.sends())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("execute did not settle")
			}
		})
	}
}
func TestActionExecuteIdentityTargetAndIntentSaveFences(t *testing.T) {
	for _, kind := range []string{"human", "membership", "epoch", "source-id", "project", "turn", "version", "authority", "save"} {
		t.Run(kind, func(t *testing.T) {
			f := newExecuteFixture(t, executeOK)
			switch kind {
			case "human":
				f.req.Caller = humanCaller
			case "membership":
				f.req.Caller.MembershipID = "old-generation"
			case "epoch":
				f.req.ServiceEpoch = "old-epoch"
			case "source-id":
				f.q.Target.MRIID = "999"
			case "project":
				f.q.Target.ProjectID = "456"
			case "turn":
				f.q.TurnNumber = 2
			case "version":
				f.q.MatterVersion = 2
			case "authority":
				f.q.Authority.RuleVersion = 2
			case "save":
				f.svc.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("cannot save intent") }
			}
			if got, err := f.run(context.Background()); err == nil || f.sends() != 0 {
				t.Fatal(got, err, f.sends())
			}
		})
	}
}
func TestActionExecuteHTTPAndCLI(t *testing.T) {
	f := newExecuteFixture(t, executeOK)
	server := httptest.NewServer(f.m)
	defer server.Close()
	env := cliToolEnv(server.URL, f.svc, MemberState{AgentID: f.id.AgentID, MembershipID: f.id.MembershipID})
	var out, stderr bytes.Buffer
	b, _ := json.Marshal(f.q)
	if err := runCLI(context.Background(), []string{"actions", "execute", "--request", string(b)}, &out, &stderr, env); err != nil {
		t.Fatal(err, stderr.String())
	}
	var receipt ActionReceipt
	if json.Unmarshal(out.Bytes(), &receipt) != nil || receipt.Status != "succeeded" || receipt.Attempts != 1 || strings.Contains(out.String(), "effective") {
		t.Fatal(out.String())
	}
	req := f.req
	req.ActionRequest = &f.q
	b, _ = json.Marshal(req)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/actions/execute", bytes.NewReader(b))
	r.Header.Set("Origin", server.URL)
	f.m.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	for _, op := range []string{"prepare", "record"} {
		if err := runCLI(context.Background(), []string{"actions", op, "--change", "{}"}, &out, &stderr, env); err == nil {
			t.Fatal("old command accepted", op)
		}
	}
}

func TestActionExecuteRecoveryDoesNotResume(t *testing.T) {
	h, c := handlingHarness(t)
	p, err := h.store.readAgentPolicy(h.id)
	if err != nil {
		t.Fatal(err)
	}
	q := ActionRequest{MatterCommand: c.MatterCommand, ID: c.ID, PolicyToken: p.PolicyToken, Authority: c.Authority, Verification: c.Verification, Operation: "gitlab.mr.comment", Target: CommentTarget{"123", "42"}, Input: CommentInput{"exact reply"}}
	c.Target.Operation = q.Operation
	got, started, err := h.store.beginAction(h.id, q, c.Target)
	if err != nil || !started || got.Status != "processing" {
		t.Fatal(got, started, err)
	}
	if _, err := h.store.actionAttempt(h.id, q); err != nil {
		t.Fatal(err)
	}
	h.reload(t, true)
	gotPtr, err := h.store.existingAction(h.id, q)
	if err != nil || gotPtr == nil || gotPtr.Status != "unknown" || gotPtr.Attempts != 1 {
		t.Fatal(gotPtr, err)
	}
	got, started, err = h.store.beginAction(h.id, q, c.Target)
	if err != nil || started || got.Status != "unknown" {
		t.Fatal(got, started, err)
	}
}
func TestActionExecuteResultSaveFailureKeepsUnknown(t *testing.T) {
	f := newExecuteFixture(t, executeOK)
	gate := filepath.Join(f.dir, "gate")
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(gate)
	done := make(chan error, 1)
	go func() { _, err := f.run(context.Background()); done <- err }()
	waitActionSend(t, f)
	// No Store I/O is in flight: the process is waiting to return its first result.
	old := f.svc.store.ops.createTemp
	f.svc.store.mu.Lock()
	f.svc.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("result storage unavailable") }
	f.svc.store.mu.Unlock()
	os.Remove(gate)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "do not resend") {
		t.Fatal(err)
	}
	f.svc.store.mu.Lock()
	f.svc.store.ops.createTemp = old
	f.svc.store.mu.Unlock()
	got, err := f.run(context.Background())
	if err != nil || got.Status != "unknown" || got.Attempts != 1 || f.sends() != 1 {
		t.Fatal(got, err)
	}
}
