//go:build darwin || linux

package room

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

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
	Diagnostics    string          `json:"diagnostics,omitempty"`
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
	return c.observe(prompt, func() (dsh.TurnResult, error) { return c.dshComponent.Run(ctx, prompt) })
}

func (c *policyRealComponent) RunPrompt(ctx context.Context, blocks []dsh.ContentBlock) (dsh.TurnResult, error) {
	var prompt string
	for _, block := range blocks {
		if block.Type == "text" && strings.HasPrefix(block.Text, "Member policy context (JSON):\n") {
			prompt = block.Text
			break
		}
	}
	return c.observe(prompt, func() (dsh.TurnResult, error) { return c.dshComponent.RunPrompt(ctx, blocks) })
}

func (c *policyRealComponent) observe(prompt string, run func() (dsh.TurnResult, error)) (dsh.TurnResult, error) {
	line, _, _ := strings.Cut(strings.TrimPrefix(prompt, "Member policy context (JSON):\n"), "\n")
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &body); err != nil {
		return dsh.TurnResult{}, err
	}
	turn := policyRealTurn{Policy: json.RawMessage(line), PolicyBytes: len(line), EffectiveBytes: len(body["effective"])}
	result, err := run()
	turn.Result = result
	if err != nil {
		turn.Error = err.Error()
		if diagnostics, ok := c.dshComponent.(interface{ Diagnostics() (string, bool) }); ok {
			turn.Diagnostics, _ = diagnostics.Diagnostics()
		}
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

// RunPrompt is the production path even for a text-only turn. Losing this
// interception would let a real acceptance pass with an empty prompt audit.
func TestPolicyRealAuditObservesPromptBlocks(t *testing.T) {
	audit := &policyRealAudit{Calls: map[string]int{}}
	blocks := []dsh.ContentBlock{{Type: "text", Text: "Member policy context (JSON):\n{\"policy_token\":\"current\",\"effective\":[]}\nTask"}, {Type: "image", MIMEType: "image/png", Data: []byte("fixture")}}
	calls := 0
	expected := dsh.TurnResult{Text: "observed", StopReason: "end_turn"}
	component := &policyRealComponent{dshComponent: &adapterComponent{runPrompt: func(ctx context.Context, got []dsh.ContentBlock) (dsh.TurnResult, error) {
		calls++
		if !reflect.DeepEqual(got, blocks) {
			t.Fatal("observer changed prompt blocks")
		}
		return expected, nil
	}}, audit: audit}
	got, err := component.RunPrompt(context.Background(), blocks)
	if err != nil || calls != 1 || !reflect.DeepEqual(got, expected) || len(audit.Turns) != 1 || audit.Turns[0].EffectiveBytes != 2 || !reflect.DeepEqual(audit.Turns[0].Result, expected) {
		t.Fatal("missing actual prompt/result evidence", got, err)
	}
	if strings.Contains(string(audit.Turns[0].Policy), "fixture") {
		t.Fatal("image bytes leaked to policy audit")
	}
}
