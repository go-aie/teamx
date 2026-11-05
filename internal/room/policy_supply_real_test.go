//go:build darwin || linux

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Observe the prompt at the real ACP boundary and CLI requests at HTTP ingress.
// No model, Session, rule supply or command result is substituted.
type policyRealAudit struct {
	mu    sync.Mutex
	Turns []policyRealTurn `json:"turns"`
	Calls map[string]int   `json:"http_calls"`
}

type policyRealTurn struct {
	Policy         json.RawMessage `json:"policy"`
	PolicyBytes    int             `json:"policy_bytes"`
	EffectiveBytes int             `json:"effective_bytes"`
	Result         dsh.TurnResult  `json:"result"`
	Error          string          `json:"error,omitempty"`
}

func (a *policyRealAudit) serve(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.Calls[r.URL.Path]++
		a.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

type policyRealBackend struct {
	dshBackend
	audit *policyRealAudit
}

func (b policyRealBackend) OpenSession(ctx context.Context, opts SessionOptions) (Session, error) {
	s, err := b.dshBackend.OpenSession(ctx, opts)
	if s != nil {
		ds := s.(*dshSession)
		ds.component = &policyRealComponent{ds.component, b.audit}
	}
	return s, err
}

func (b policyRealBackend) RotateSession(opts SessionOptions, settled bool) (Backend, error) {
	next, err := b.dshBackend.RotateSession(opts, settled)
	if err != nil {
		return nil, err
	}
	return policyRealBackend{next.(dshBackend), b.audit}, nil
}

type policyRealComponent struct {
	dshComponent
	audit *policyRealAudit
}

func (c *policyRealComponent) Run(ctx context.Context, prompt string) (dsh.TurnResult, error) {
	line, _, _ := strings.Cut(strings.TrimPrefix(prompt, "Member policy context (JSON):\n"), "\n")
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &body); err != nil {
		return dsh.TurnResult{}, err
	}
	turn := policyRealTurn{Policy: json.RawMessage(line), PolicyBytes: len(line), EffectiveBytes: len(body["effective"])}
	result, err := c.dshComponent.Run(ctx, prompt)
	turn.Result = result
	if err != nil {
		turn.Error = err.Error()
	}
	c.audit.mu.Lock()
	c.audit.Turns = append(c.audit.Turns, turn)
	c.audit.mu.Unlock()
	return result, err
}

func (c *policyRealComponent) Activity() (dsh.TurnResult, bool) {
	return c.dshComponent.(interface{ Activity() (dsh.TurnResult, bool) }).Activity()
}

func observePolicyReal(svc *service, audit *policyRealAudit) {
	svc.memberFactory = func(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) actorx.Actor {
		member := svc.newMember(id, runner, restored).(*MemberAgent)
		member.Backend = policyRealBackend{member.Backend.(dshBackend), audit}
		return member
	}
}

// Installed DSH/model with isolated local Room/Inbox input, no platform tools
// or remote writes. Opt in separately from the approved real-comment test.
func TestPolicySupplyRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_POLICY_SUPPLY_REAL") != "1" {
		t.Skip("set TEAMX_POLICY_SUPPLY_REAL=1 for real DSH policy supply acceptance (no platform writes)")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("new absolute TEAMX_DSH_OUTPUT required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(output, "driver-room")}, output: output,
		report: map[string]any{"scenario": "policy-action-S3-supply", "status": "failed", "remote_writes": 0, "notification_source": "local fixture; real DSH/model"}}
	audit := &policyRealAudit{Calls: map[string]int{}}
	defer func() {
		audit.mu.Lock()
		defer audit.mu.Unlock()
		d.report["audit"] = audit
		data, err := json.MarshalIndent(d.report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(output, "report.json"), append(data, '\n'), 0600)
		}
		if err != nil {
			t.Error(err)
		}
	}()
	if err := d.setup(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(d.report["apiKeyEnv"].(string), d.secret)
	var profiles map[string]dshProfile
	if err := jsonFile(d.profilePath, &profiles); err != nil {
		t.Fatal(err)
	}
	p := profiles["reviewer"]
	p.Model.ContextWindow, p.Model.MaxTokens, p.Model.ReasoningEffort = 1048576, 32768, "low"
	p.Instructions = `You are a bounded policy supply acceptance reader. Follow the current runtime policy protocol. Read unread Room messages, read each offered Inbox batch, and use matters list for current effective decisions. Keep all model output private. In each final private answer report the current policy_token, current_turn, ALL currently effective preference texts and ALL effective matter decision texts. When effective policy is empty include EMPTY_POLICY and omit every old preference text. Use conversation memory for an unchanged token; restore policy only if its body is missing. Never make remote requests, enable tools, inspect files/environment/credentials, edit files, delegate, create rules or decisions, execute actions or send public messages. Only the configured TeamX read, inbox read, matters list and recovery policy read commands are needed.`
	m := testManager(t, filepath.Join(output, "rooms-root"), managerTestProfiles(t))
	room := managerCreate(t, m, "policy-supply-s3")
	server := httptest.NewServer(audit.serve(m))
	defer server.Close()
	svc, err := newService(m.roomDir(room.RoomID), room.RoomID, serviceConfig{endpoint: server.URL, cliPath: d.binary, maxTurns: 5, persistentScheduling: true, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": p}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	observePolicyReal(svc, audit)
	m.mu.Lock()
	m.runtimes[room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	m.mu.Unlock()
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	marker, err := newOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	v1, v2, decisionText := "S3_V1_"+marker, "S3_V2_"+marker, "S3_DECISION_"+marker
	rule := PolicyChange{ID: "supply-preference", Kind: "preference", Status: "active", Text: v1, Scope: PolicyScope{Description: "local acceptance output"}, SourceText: "隔离验收：输出当前有效偏好文本，不授予平台操作权限"}
	if _, err := svc.store.changePolicy(id, humanCaller, rule); err != nil {
		t.Fatal(err)
	}
	post := func(n int) {
		t.Helper()
		_, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: fmt.Sprintf("supply-%d", n), Text: "请报告当前有效偏好、事项决定、policy_token 和 current_turn；只输出私有结果。"})
		if err != nil {
			t.Fatal(err)
		}
	}
	wait := func(n uint64, want, absent string, full bool) policyRealTurn {
		t.Helper()
		s := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber >= n })
		if s.Error != "" || s.Agent.Turn.Status != "completed" || s.Agent.TurnNumber != n {
			t.Fatal("real DSH turn failed", n, s.Error)
		}
		audit.mu.Lock()
		defer audit.mu.Unlock()
		if len(audit.Turns) != int(n) {
			t.Fatal("missing real prompt evidence", n)
		}
		turn := audit.Turns[n-1]
		if (turn.EffectiveBytes > 0) != full || !strings.Contains(turn.Result.Text, want) || (absent != "" && strings.Contains(turn.Result.Text, absent)) {
			t.Fatalf("turn %d supply or model result mismatch; inspect retained report", n)
		}
		var supplied agentPolicyView
		if err := json.Unmarshal(turn.Policy, &supplied); err != nil || supplied.CurrentTurn != n || !strings.Contains(turn.Result.Text, supplied.PolicyToken) {
			t.Fatal("model did not use current metadata", n)
		}
		if audit.Calls["/v1/policy/read"] != 0 || audit.Calls["/v1/decisions/read"] != 0 {
			t.Fatal("normal path made unnecessary policy/decision reads")
		}
		return turn
	}
	post(1)
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	first := wait(1, v1, "", true)
	// A local notification exercises the real Inbox turn without enabling a
	// platform account. Its contents contain none of the random policy markers.
	h := &inboxHarness{id: id}
	if err := svc.receiveInbox(ctx, inboxTarget(id), h.receive("s3-local").Item); err != nil {
		t.Fatal(err)
	}
	second := wait(2, v1, "", false)
	var a, b agentPolicyView
	_ = json.Unmarshal(first.Policy, &a)
	_ = json.Unmarshal(second.Policy, &b)
	if a.PolicyToken != b.PolicyToken || second.PolicyBytes >= first.PolicyBytes {
		t.Fatal("unchanged token/body comparison failed")
	}
	page, err := svc.store.listMatters(id, "", 0, 25)
	if err != nil || len(page.Records) != 1 {
		t.Fatal("Inbox was not read", err)
	}
	decision := PolicyChange{ID: "local-decision", Kind: "decision", Status: "active", Text: decisionText, Scope: PolicyScope{Description: "exact local matter"}, Actions: "报告本地事项验收结果", SourceText: "仅此隔离事项的验收决定", Seq: 1, MatterID: "main", MatterVersion: page.Records[0].Matters[0].Version}
	if _, err := svc.store.changeDecision(id, humanCaller, decision); err != nil {
		t.Fatal(err)
	}
	rule.Revision, rule.Version, rule.Text = 1, 1, v2
	if _, err := svc.store.changePolicy(id, humanCaller, rule); err != nil {
		t.Fatal(err)
	}
	post(3)
	third := wait(3, v2, v1, true)
	if !strings.Contains(third.Result.Text, decisionText) {
		t.Fatal("effective decision not obtained from matters list")
	}
	rule.Revision, rule.Version, rule.Status = 2, 2, "revoked"
	if _, err := svc.store.changePolicy(id, humanCaller, rule); err != nil {
		t.Fatal(err)
	}
	post(4)
	fourth := wait(4, "EMPTY_POLICY", v2, true)
	if !strings.Contains(string(fourth.Policy), `"effective":[]`) {
		t.Fatal("revocation did not supply explicit empty replacement")
	}
	if _, err := svc.rotateSession(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, Resume: true}); err != nil {
		t.Fatal(err)
	}
	post(5)
	fifth := wait(5, "EMPTY_POLICY", v2, true)
	if !strings.Contains(string(fifth.Policy), `"effective":[]`) {
		t.Fatal("rotated Session did not receive empty full policy")
	}
	d.report["status"] = "passed"
	d.report["comparison"] = map[string]any{"task": "report current policy and matter decisions", "first_policy_bytes": first.PolicyBytes, "unchanged_policy_bytes": second.PolicyBytes, "policy_read_calls": 0, "decision_read_calls": 0, "action_calls": 0, "cost_measured": false}
	t.Logf("real DSH: first=%d bytes, unchanged=%d bytes; policy/decisions reads=0; changed/revoked/rotated full replacement and matter decision verified", first.PolicyBytes, second.PolicyBytes)
}
