//go:build integration && (darwin || linux)

package dsh_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/builtin/agents/dsh"
)

// This author imports public packages only. The child speaks ACP locally and
// never calls a model or platform. Its PID and method log prove process work.
type sessionHost struct {
	launch          agent.Launch
	once            sync.Once
	releases, turns int
	proof           error
}

func (h *sessionHost) PrepareOpen(context.Context) (agent.Launch, error) { return h.launch, nil }
func (h *sessionHost) Opened(artifact *agent.Artifact) error {
	if artifact == nil || artifact.Source != "background-fixture" {
		return errors.New("runtime identity missing")
	}
	return nil
}
func (h *sessionHost) PrepareTurn(_ context.Context, _ agent.Input, c agent.TurnCapabilities) (*agent.PreparedTurn, error) {
	if c.Images {
		return nil, errors.New("fixture unexpectedly supports images")
	}
	return &agent.PreparedTurn{Prompt: []agent.ContentBlock{{Type: "text", Text: "bounded fixture prompt"}},
		Finish: func(result agent.TurnResult, err error) error {
			if err == nil && result.StopReason != "end_turn" {
				return errors.New("prompt did not complete")
			}
			h.turns++
			return errors.Join(err, h.proof)
		}}, nil
}
func (h *sessionHost) Release() { h.once.Do(func() { h.releases++ }) }

func TestFactoryPublicSessionResumeAndProcessSettlement(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required for local ACP fixture")
	}
	node, err = filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("../../../scripts/fixtures/background-acp.mjs")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log, release := filepath.Join(dir, "methods.jsonl"), filepath.Join(dir, "release")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	binary := filepath.Join(dir, "fixture-dsh")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"${1:-}\" = --version ]; then echo 1.0.0; exit 0; fi\nexec "+quote(node)+" "+quote(fixture)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	launch := agent.Launch{BinaryPath: binary, Workspace: dir, StateDir: filepath.Join(dir, "state"),
		Model:      agent.ModelConfig{Protocol: "openai-completions", BaseURL: "http://127.0.0.1:1/v1", ModelID: "background-fixture", APIKeyEnv: "TEAMX_FACTORY_TEST_KEY"},
		Credential: &agent.Credential{Ref: "factory-test", Value: "offline-only"},
		ToolEnv:    map[string]string{"TEAMX_BACKGROUND_LOG": log, "TEAMX_BACKGROUND_RELEASE": release}, TurnTimeout: 10 * time.Second}
	factory := dsh.NewFactory()
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("constructor executed child", err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proof := errors.New("host has no durable input receipt")
	for _, resume := range []bool{false, true} {
		host := &sessionHost{launch: launch}
		session, err := factory.OpenSession(ctx, host, resume)
		if session != nil {
			defer session.Close(context.Background())
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := session.Run(ctx, agent.Input{}); err != nil {
			t.Fatal(err)
		}
		// An ACP success cannot replace the application's independent proof.
		host.proof = proof
		if err := session.Run(ctx, agent.Input{}); !errors.Is(err, proof) {
			t.Fatal("input proof ignored", err)
		}
		if err := session.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if host.releases != 1 || host.turns != 2 {
			t.Fatal("host lifecycle bypassed", host.releases, host.turns)
		}
		if err := factory.CheckResume(launch.StateDir); err != nil {
			t.Fatal(err)
		}
		if err := factory.CheckReset(launch.StateDir); err != nil {
			t.Fatal(err)
		}
		if err := factory.CheckResumeConfig(launch); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]int{}
	pids := map[int]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row struct {
			PID    int
			Method string
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		methods[row.Method]++
		pids[row.PID] = true
	}
	if methods["session/new"] != 1 || methods["session/resume"] != 1 || methods["session/close"] != 2 || methods["prompt_completed"] != 4 || len(pids) != 2 {
		t.Fatal(methods, pids)
	}
	for pid := range pids {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatal("child not independently settled", pid, err)
		}
	}
}
