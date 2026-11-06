package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeIdentityWithoutConfiguredArtifact(t *testing.T) {
	opts := fixtureOptions(t)
	opts.Artifact = Artifact{}
	if err := CheckConfig(opts); err != nil {
		t.Fatal(err)
	}
	s := openFixture(t, opts)
	if s.opts.Limits.TurnTimeout != 15*time.Minute {
		t.Fatal("unexpected default turn timeout", s.opts.Limits.TurnTimeout)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	ref, err := recoveryReference(opts.StateDir)
	if err != nil || ref.Artifact != (Artifact{Source: "fixture", Version: "test"}) {
		t.Fatal("runtime identity was not recorded", ref, err)
	}
	opts.Limits.TurnTimeout = 30 * time.Minute
	if err := CheckResumeConfig(opts); err != nil {
		t.Fatal(err)
	}
	resumed, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(context.Background())
	if resumed.id != s.id {
		t.Fatal("replaced original session")
	}
	if resumed.opts.Limits.TurnTimeout != 30*time.Minute {
		t.Fatal("resumed Session did not use current timeout")
	}
}

func TestChangedRuntimeRejectedBeforeSessionResume(t *testing.T) {
	for _, change := range []string{"version", "name"} {
		t.Run(change, func(t *testing.T) {
			opts := fixtureOptions(t)
			opts.Artifact = Artifact{}
			s := openFixture(t, opts)
			if err := s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(opts.Workspace, "session-new.called")); err != nil {
				t.Fatal(err)
			}
			if change == "version" {
				opts.ToolEnv["TEAMX_DSH_TEST_VERSION"] = "next"
			} else {
				opts.ToolEnv["TEAMX_DSH_MODE"] = "changed-agent-name"
			}
			if session, err := Resume(context.Background(), opts); session != nil || !errors.Is(err, ErrRecoveryUnsupported) {
				t.Fatal("accepted changed runtime", session, err)
			}
			for _, marker := range []string{"session-new.called", "session-resume.called"} {
				if _, err := os.Stat(filepath.Join(opts.Workspace, marker)); !os.IsNotExist(err) {
					t.Fatal("sent a session request to changed runtime", marker, err)
				}
			}
			if err := CheckResume(opts.StateDir); err == nil {
				t.Fatal("failed resume retained reusable shutdown evidence")
			}
		})
	}
}

func TestArtifactMetadataMustBeCompleteWhenProvided(t *testing.T) {
	for _, artifact := range []Artifact{{Source: "build"}, {Version: "1"}} {
		opts := fixtureOptions(t)
		opts.Artifact = artifact
		if err := CheckConfig(opts); err == nil {
			t.Fatal("accepted incomplete artifact", artifact)
		}
		if session, err := Open(context.Background(), opts); err == nil || session != nil {
			t.Fatal("opened with incomplete artifact", session, err)
		}
	}
}

func TestInvalidInstalledVersionDoesNotOpenSession(t *testing.T) {
	for _, mode := range []string{"bad-cli-version", "oversized-cli-version", "cli-version-stall"} {
		t.Run(mode, func(t *testing.T) {
			opts := fixtureOptions(t)
			opts.Artifact = Artifact{}
			opts.ToolEnv["TEAMX_DSH_MODE"] = mode
			opts.Limits.HandshakeTimeout = 100 * time.Millisecond
			if session, err := Open(context.Background(), opts); err == nil || session != nil {
				t.Fatal("accepted an invalid installed version", session, err)
			}
			if _, err := os.Stat(filepath.Join(opts.Workspace, "session-new.called")); !os.IsNotExist(err) {
				t.Fatal("created session without a usable version", err)
			}
			lock, err := lockDirectory(opts.StateDir)
			if err != nil {
				t.Fatal("failed version query retained state lock", err)
			}
			_ = lock.Close()
		})
	}
}

func TestNormalResumeKeepsMemoryAndRefreshesEnvironment(t *testing.T) {
	opts := fixtureOptions(t)
	opts.ToolEnv["TEAMX_SERVICE_EPOCH"] = "old"
	s := openFixture(t, opts)
	if _, err := s.Run(context.Background(), "remember"); err != nil {
		t.Fatal(err)
	}
	if err := CheckResume(opts.StateDir); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := CheckResume(opts.StateDir); err != nil {
		t.Fatal(err)
	}
	opts.ToolEnv["TEAMX_SERVICE_EPOCH"] = "current"
	resumed, err := Resume(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(context.Background())
	if resumed.id != s.id {
		t.Fatal("replaced session")
	}
	result, err := resumed.Run(context.Background(), "recall")
	if err != nil || result.Text != "private-memory|current|"+s.id {
		t.Fatal(result, err)
	}
	data, err := os.ReadFile(filepath.Join(opts.StateDir, "backend.json"))
	var ref reference
	if err != nil || json.Unmarshal(data, &ref) != nil || ref.Stopped {
		t.Fatal("resume did not consume shutdown evidence", string(data), err)
	}
	if _, err := Resume(context.Background(), opts); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
}

func TestConfiguredTokenLimitsMaterializedAndRecoveryBound(t *testing.T) {
	opts := fixtureOptions(t)
	opts.Model.ContextWindow, opts.Model.MaxTokens = 1048576, 32768
	if err := CheckConfig(opts); err != nil {
		t.Fatal(err)
	}
	s := openFixture(t, opts)
	data, err := os.ReadFile(filepath.Join(opts.StateDir, "home", "cordis.patch.yml"))
	var patches []struct {
		ID     string `json:"id"`
		Config struct {
			Providers map[string]struct {
				Models []struct {
					ContextWindow int `json:"contextWindow"`
					MaxTokens     int `json:"maxTokens"`
				} `json:"models"`
			} `json:"providers"`
		} `json:"config"`
	}
	if err != nil || json.Unmarshal(data, &patches) != nil {
		t.Fatal(err, string(data))
	}
	found := false
	for _, patch := range patches {
		if patch.ID != "llm-pi-ai" {
			continue
		}
		models := patch.Config.Providers["teamx"].Models
		if len(models) != 1 || models[0].ContextWindow != 1048576 || models[0].MaxTokens != 32768 {
			t.Fatal(models)
		}
		found = true
	}
	if !found {
		t.Fatal("missing model catalog")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := CheckResumeConfig(opts); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"context_window", "max_tokens"} {
		t.Run(field, func(t *testing.T) {
			changed := opts
			if field == "context_window" {
				changed.Model.ContextWindow *= 2
			} else {
				changed.Model.MaxTokens *= 2
			}
			if err := CheckResumeConfig(changed); !errors.Is(err, ErrRecoveryUnsupported) {
				t.Fatalf("changed %s accepted: %v", field, err)
			}
		})
	}
}

func TestResumeFailsClosedWithoutReplacement(t *testing.T) {
	for _, mode := range []string{"missing", "unsettled", "legacy", "corrupt", "role", "model", "artifact", "workspace", "history", "role-file", "profile-file", "no-resume", "resume-error"} {
		t.Run(mode, func(t *testing.T) {
			opts := fixtureOptions(t)
			s := openFixture(t, opts)
			if err := s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(opts.StateDir, "backend.json")
			data, _ := os.ReadFile(path)
			var ref reference
			_ = json.Unmarshal(data, &ref)
			switch mode {
			case "missing":
				_ = os.Remove(path)
			case "unsettled":
				ref.Stopped = false
				_ = atomicJSON(opts.StateDir, "backend.json", ref)
			case "legacy":
				ref.ConfigSHA256 = ""
				_ = atomicJSON(opts.StateDir, "backend.json", ref)
			case "corrupt":
				_ = os.WriteFile(path, []byte("bad"), 0600)
			case "role":
				opts.Instructions += "changed"
			case "model":
				opts.Model.ModelID = "changed"
			case "artifact":
				opts.Artifact.Version = "changed"
			case "workspace":
				opts.Workspace = t.TempDir()
			case "history":
				_ = os.RemoveAll(filepath.Join(opts.StateDir, "home"))
			case "role-file":
				_ = os.WriteFile(filepath.Join(opts.StateDir, "home", "AGENTS.md"), []byte("other role"), 0600)
			case "profile-file":
				_ = os.WriteFile(filepath.Join(opts.StateDir, "home", "cordis.patch.yml"), []byte("{}"), 0600)
			default:
				opts.ToolEnv["TEAMX_DSH_MODE"] = mode
			}
			if session, err := Resume(context.Background(), opts); err == nil || session != nil {
				t.Fatal("unsafe resume succeeded", err)
			}
			if mode == "no-resume" || mode == "resume-error" {
				if err := CheckResume(opts.StateDir); err == nil {
					t.Fatal("failed activation could be retried")
				}
			}
		})
	}
}

func TestFailedSessionCannotAuthorizeNormalResume(t *testing.T) {
	opts := fixtureOptions(t)
	s := openFixture(t, opts)
	if _, err := s.Run(context.Background(), "bad"); err == nil {
		t.Fatal("expected protocol failure")
	}
	_ = s.Close(context.Background())
	if err := CheckResume(opts.StateDir); err == nil || !strings.Contains(err.Error(), "shutdown") {
		t.Fatal(err)
	}
}

func TestCloseDuringUncertainTurnDoesNotAuthorizeResume(t *testing.T) {
	opts := fixtureOptions(t)
	opts.ToolEnv["TEAMX_DSH_MODE"] = "bad-cancel"
	s := openFixture(t, opts)
	done := make(chan error, 1)
	go func() { _, err := s.Run(context.Background(), "hold"); done <- err }()
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.active != nil && s.active.result.Text == "started"
	})
	if err := s.Close(context.Background()); !errors.Is(err, ErrRecoveryUnsupported) || errors.Is(err, ErrCleanupPending) {
		t.Fatal("uncertain turn failure disappeared during cleanup", err)
	}
	if err := <-done; !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if err := CheckResume(opts.StateDir); err == nil {
		t.Fatal("uncertain turn during normal process close authorized recovery")
	}
}

func TestProviderFailureAllowsExplicitResetAfterSettlement(t *testing.T) {
	for _, tc := range []struct {
		prompt string
		want   error
		hint   string
		status int
	}{
		{"rate-limited", ErrRateLimited, "HTTP 429", 0},
		{"authentication-failed", nil, "HTTP 401", 401},
		{"bad-gateway", nil, "HTTP 502", 502},
		{"model-not-found", ErrModelNotFound, "HTTP 404", 0},
		{"model-unavailable", ErrModelUnavailable, "HTTP 503", 0},
	} {
		t.Run(tc.prompt, func(t *testing.T) {
			opts := fixtureOptions(t)
			s := openFixture(t, opts)
			if err := CheckReset(opts.StateDir); !errors.Is(err, ErrLocked) {
				t.Fatal("live execution allowed reset", err)
			}
			_, err := s.Run(context.Background(), tc.prompt)
			if !errors.Is(err, ErrUncertain) || (tc.want != nil && !errors.Is(err, tc.want)) ||
				!strings.Contains(err.Error(), tc.hint) || strings.Contains(err.Error(), "secret-fixture-key") {
				t.Fatal("missing safe provider diagnostic", err)
			}
			if tc.status != 0 {
				var status *HTTPStatusError
				if !errors.As(err, &status) || status.StatusCode != tc.status {
					t.Fatal("turn lost HTTP status", err)
				}
			}
			err = s.Close(context.Background())
			if !errors.Is(err, ErrRecoveryUnsupported) || !CleanupSettled(err) {
				t.Fatal("failed prompt lost its completed cleanup evidence", err)
			}
			if (tc.want != nil && !errors.Is(err, tc.want)) || !strings.Contains(err.Error(), tc.hint) || strings.Contains(err.Error(), "secret-fixture-key") {
				t.Fatal("cleanup lost safe provider diagnostic", err)
			}
			if tc.status != 0 {
				var status *HTTPStatusError
				if !errors.As(err, &status) || status.StatusCode != tc.status {
					t.Fatal("cleanup lost HTTP status", err)
				}
			}
			if err := CheckResume(opts.StateDir); err == nil {
				t.Fatal("failed original session became resumable")
			}
			if err := CheckReset(opts.StateDir); err != nil {
				t.Fatal("failed but stopped execution cannot be explicitly reset", err)
			}
			if CleanupSettled(errors.Join(ErrCleanupPending, &ClosedError{Err: ErrRecoveryUnsupported})) {
				t.Fatal("unfinished cleanup was accepted as settled")
			}
		})
	}
}

func TestResumeConsumesResetEvidenceBeforeLaunch(t *testing.T) {
	opts := fixtureOptions(t)
	s := openFixture(t, opts)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := CheckReset(opts.StateDir); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after preparing a resume but before starting its child.
	opts = s.opts
	lock, _, err := prepare(&opts, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := CheckReset(opts.StateDir); err == nil {
		t.Fatal("free lock reused stale reset evidence after an unproven restart")
	}
}

func TestResumeConfigPreflightDoesNotConsumeEvidenceOrRequireSecrets(t *testing.T) {
	opts := fixtureOptions(t)
	s := openFixture(t, opts)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.StateDir, "backend.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(opts.Model.APIKeyEnv, "")
	if err := CheckResumeConfig(opts); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("preflight consumed normal-close evidence")
	}
	for _, kind := range []string{"model", "instructions", "artifact", "binary", "workspace"} {
		t.Run(kind, func(t *testing.T) {
			changed := opts
			switch kind {
			case "model":
				changed.Model.ModelID = "other"
			case "instructions":
				changed.Instructions += "other"
			case "artifact":
				changed.Artifact.Version = "other"
			case "binary":
				changed.BinaryPath = "/bin/sh"
			case "workspace":
				changed.Workspace = t.TempDir()
			}
			if err := CheckResumeConfig(changed); err == nil {
				t.Fatal("accepted changed", kind)
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("failed preflight consumed evidence")
			}
		})
	}
}
