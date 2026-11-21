package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/go-aie/teamx/agent"
)

// Hide all concrete backend types. Exercise partial acquisition and proof
// failures through exactly the public host and factory operations.
type factory struct {
	agent.Factory
	component agent.Component
	err       error
}

func (f factory) Open(context.Context, agent.Launch, bool) (agent.Component, error) {
	return f.component, f.err
}

type component struct{ closes int }

func (*component) RunPrompt(context.Context, []agent.ContentBlock) (agent.TurnResult, error) {
	return agent.TurnResult{StopReason: "end_turn"}, nil
}
func (*component) SupportsImages() bool          { return false }
func (c *component) Close(context.Context) error { c.closes++; return nil }

type host struct {
	releases, finishes, turnCloses int
	proof                          error
}

func (*host) PrepareOpen(context.Context) (agent.Launch, error) { return agent.Launch{}, nil }
func (*host) Opened(*agent.Artifact) error                      { return nil }
func (h *host) Release()                                        { h.releases++ }
func (h *host) PrepareTurn(context.Context, agent.Input, agent.TurnCapabilities) (*agent.PreparedTurn, error) {
	return &agent.PreparedTurn{Prompt: []agent.ContentBlock{{Type: "text", Text: "input"}},
		Finish: func(agent.TurnResult, error) error { h.finishes++; return h.proof },
		Close:  func() { h.turnCloses++ }}, nil
}

func TestSessionRetainsPartialOwnershipAndHostProof(t *testing.T) {
	failure := errors.New("partial open")
	for _, acquired := range []bool{false, true} {
		h := &host{}
		f := factory{err: failure}
		c := &component{}
		if acquired {
			f.component = c
		}
		session, err := agent.OpenSession(context.Background(), f, h, false)
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
		if !acquired {
			if session != nil || h.releases != 1 {
				t.Fatal("failed acquisition leaked host binding")
			}
			continue
		}
		if session == nil || h.releases != 0 || c.closes != 0 {
			t.Fatal("partial ownership discarded")
		}
		if err := session.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if c.closes != 1 || h.releases != 1 {
			t.Fatal("partial close bypassed host/component")
		}
	}
	proof := errors.New("input proof failed")
	h := &host{proof: proof}
	session, err := agent.OpenSession(context.Background(), factory{component: &component{}}, h, false)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	if err := session.Run(context.Background(), agent.Input{}); !errors.Is(err, proof) {
		t.Fatal("ACP success replaced host proof", err)
	}
	if h.finishes != 1 || h.turnCloses != 1 {
		t.Fatal("proof or turn release bypassed")
	}
}
