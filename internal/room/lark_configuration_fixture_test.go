package room

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

var larkConfigurationBuild struct {
	sync.Once
	dir, path string
	err       error
}

type larkConfigurationFixtureState struct {
	Profile, AppID, Brand, Version, OpenID, Tenant string
	Self                                           bool
	Gate                                           string
	SpawnChild                                     bool
	Fault                                          string
}

func defaultLarkConfigurationState() larkConfigurationFixtureState {
	return larkConfigurationFixtureState{Profile: "fixture", AppID: "cli_fixture", Brand: "feishu", Version: lark.CLIVersion, OpenID: "ou_reader", Tenant: "account_tenant"}
}

func installLarkConfigurationCLI(t *testing.T, dir string) {
	t.Helper()
	larkConfigurationBuild.Do(func() {
		started := time.Now()
		larkConfigurationBuild.dir, larkConfigurationBuild.err = os.MkdirTemp("", "teamx-lark-configuration-*")
		if larkConfigurationBuild.err != nil {
			return
		}
		larkConfigurationBuild.path = filepath.Join(larkConfigurationBuild.dir, "native-cli")
		data, err := exec.CommandContext(t.Context(), "go", "build", "-o", larkConfigurationBuild.path, "./testdata/lark_configuration_cli").CombinedOutput()
		if err != nil {
			larkConfigurationBuild.err = fmt.Errorf("build Lark configuration fixture: %w: %s", err, data)
		}
		t.Logf("lark configuration build: elapsed=%s", time.Since(started))
	})
	if larkConfigurationBuild.err != nil {
		t.Fatal(larkConfigurationBuild.err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	// Private material travels through argv rather than weakening Client.command's
	// environment whitelist. exec preserves the production process-group owner.
	script := "#!/bin/sh\nexec " + quote(larkConfigurationBuild.path) + " " + quote(dir) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	writeJSONFixture(t, filepath.Join(dir, "native-fixture.json"), defaultLarkConfigurationState())
	writeJSONFixture(t, filepath.Join(dir, "application-scopes.json"), lark.DefaultScopes())
	writeJSONFixture(t, filepath.Join(dir, "missing-scopes.json"), []string{})
}

func larkConfigurationHarness(t *testing.T, count int) *larkHarness {
	t.Helper()
	finish := observeLarkConfigurationFixture(t)
	var m *roomManager
	var room roomSummary
	var svc *service
	var members []MemberState
	if count == 0 {
		m, room, svc, _, members = cliToolFixture(t, "exit 0")
	} else {
		m, room, svc, members = toolRoomFixture(t, count)
	}
	dir := t.TempDir()
	installLarkConfigurationCLI(t, dir)
	h := newLarkHarnessForRoomCLI(t, m, room, svc, members, dir)
	finish(h.dir)
	return h
}

// Register before constructing the fixture so the final observation includes
// component shutdown and private directory cleanup. Each fixture owns its log.
func observeLarkConfigurationFixture(t *testing.T) func(string) {
	t.Helper()
	started := time.Now()
	var ready, finished time.Time
	var starts, exits int
	var native time.Duration
	t.Cleanup(func() {
		if ready.IsZero() || finished.IsZero() {
			return
		}
		t.Logf("lark configuration fixture: setup=%s target=%s cleanup=%s cli_starts=%d cli_exits=%d native=%s", ready.Sub(started), finished.Sub(ready), time.Since(finished), starts, exits, native)
	})
	return func(dir string) {
		ready = time.Now()
		t.Cleanup(func() {
			finished = time.Now()
			file, err := os.Open(filepath.Join(dir, "native-calls.ndjson"))
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			decoder := json.NewDecoder(file)
			seen := map[int]int64{}
			for {
				var event struct {
					Event string `json:"event"`
					PID   int    `json:"pid"`
					Time  int64  `json:"time_ns"`
				}
				if err := decoder.Decode(&event); err == io.EOF {
					break
				} else if err != nil {
					t.Error(err)
					return
				}
				if event.Event == "start" {
					starts++
					seen[event.PID] = event.Time
				} else if event.Event == "exit" {
					exits++
					native += time.Duration(event.Time - seen[event.PID])
				}
			}
		})
	}
}

func configurationNativeClient(t *testing.T) (*lark.Client, string) {
	t.Helper()
	finish := observeLarkConfigurationFixture(t)
	dir := t.TempDir()
	installLarkConfigurationCLI(t, dir)
	client, err := lark.NewClientFactory(0, 0, 0).New(platform.CLIProfile{Version: 1, Executable: filepath.Join(dir, "lark-cli"), Profile: "fixture", Brand: "feishu", CLIVersion: lark.CLIVersion})
	if err != nil {
		t.Fatal(err)
	}
	finish(dir)
	return client, dir
}

func TestLarkConfigurationNativeProtocolAndFailures(t *testing.T) {
	parallelFixtureTest(t)
	client, dir := configurationNativeClient(t)
	if v, err := client.Verify(t.Context()); err != nil || v.Account.ID.Value != "ou_reader" {
		t.Fatal(v, err)
	}
	file, err := os.Open(filepath.Join(dir, "native-calls.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var calls [][]string
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		var event struct {
			Event string
			Args  []string
			CWD   string
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if event.Event == "start" {
			if event.CWD != cwd {
				t.Fatal("native command changed cwd", event.CWD, cwd)
			}
			calls = append(calls, event.Args)
		}
	}
	want := [][]string{
		{"--profile", "fixture", "--version"},
		{"--profile", "fixture", "whoami", "--as", "user"},
		{"--profile", "fixture", "api", "GET", "/open-apis/authen/v1/user_info", "--params", "{}", "--as", "user", "--format", "json"},
		{"--profile", "fixture", "whoami", "--as", "user"},
	}
	if !sameJSON(calls, want) {
		t.Fatal("changed native argv", calls)
	}
	for _, tc := range []struct {
		name, code string
		change     func(*larkConfigurationFixtureState)
	}{
		{"version", "cli_version_mismatch", func(s *larkConfigurationFixtureState) { s.Version = "0.0.0" }},
		{"profile", "identity_mismatch", func(s *larkConfigurationFixtureState) { s.Profile = "other" }},
		{"malformed", "invalid_response", func(s *larkConfigurationFixtureState) { s.Fault = "malformed_user_info" }},
		{"exit", "query_failed", func(s *larkConfigurationFixtureState) { s.Fault = "failed_user_info" }},
		{"stdout limit", "output_limit", func(s *larkConfigurationFixtureState) { s.Fault = "stdout_limit" }},
		{"stderr limit", "output_limit", func(s *larkConfigurationFixtureState) { s.Fault = "stderr_limit" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := defaultLarkConfigurationState()
			tc.change(&state)
			writeJSONFixture(t, filepath.Join(dir, "native-fixture.json"), state)
			_, err := client.Verify(t.Context())
			if lark.ErrorCode(err) != tc.code || strings.Contains(fmt.Sprint(err), "private-fixture-error") {
				t.Fatal("wrong native error boundary", err, tc.code)
			}
		})
	}
	writeJSONFixture(t, filepath.Join(dir, "native-fixture.json"), defaultLarkConfigurationState())
	// This family has no native write protocol, including accidental sends.
	command := exec.Command(filepath.Join(dir, "lark-cli"), "--profile", "fixture", "api", "POST", "/open-apis/im/v1/messages", "--params", "{}", "--body", "{}", "--as", "user", "--format", "json")
	if err := command.Run(); err == nil {
		t.Fatal("configuration fixture accepted a native write")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatal("unexpected native write rejection", err)
	}
	if _, err := client.Verify(t.Context()); err != nil {
		t.Fatal("failed read retained account gate", err)
	}
}

func TestLarkConfigurationNativeIdentityChanges(t *testing.T) {
	parallelFixtureTest(t)
	client, dir := configurationNativeClient(t)
	if _, err := client.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Change the native identity after user_info has returned its original value;
	// the final whoami must still run and reject the changed account.
	if err := os.WriteFile(filepath.Join(dir, "next-open-id"), []byte("ou_other"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Verify(t.Context()); lark.ErrorCode(err) != "identity_mismatch" {
		t.Fatal("late native identity change accepted", err)
	}
	if err := os.Remove(filepath.Join(dir, "next-open-id")); err != nil {
		t.Fatal(err)
	}
	state := defaultLarkConfigurationState()
	state.OpenID = "ou_other"
	writeJSONFixture(t, filepath.Join(dir, "native-fixture.json"), state)
	if v, err := client.Verify(t.Context()); err != nil || v.Account.ID.Value != "ou_other" {
		t.Fatal("native identity was cached", v, err)
	}
}

func TestLarkConfigurationNativeCancellation(t *testing.T) {
	parallelFixtureTest(t)
	client, dir := configurationNativeClient(t)
	state := defaultLarkConfigurationState()
	state.Gate, state.SpawnChild = "identity", true
	writeJSONFixture(t, filepath.Join(dir, "native-fixture.json"), state)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.Verify(ctx); done <- err }()
	pids := []string{}
	// On a failed assertion, still terminate only this fixture's observed PIDs.
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = exec.Command("kill", "-KILL", pid).Run()
		}
	})
	for _, name := range []string{"identity-ready", "child-ready"} {
		for {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil {
				pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil || pid <= 1 {
					t.Fatalf("invalid fixture process readiness %s: %q", name, data)
				}
				pids = append(pids, strconv.Itoa(pid))
				break
			}
			select {
			case err := <-done:
				t.Fatal("native process did not block", err)
			case <-ctx.Done():
				t.Fatal("native process did not become ready", ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	for _, pid := range pids {
		if err := exec.Command("ps", "-p", pid).Run(); err != nil {
			t.Fatal("fixture process missing before cancellation", pid, err)
		}
	}
	cancel()
	select {
	case err := <-done:
		if lark.ErrorCode(err) != "cancelled" {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native cancellation did not settle inherited pipes")
	}
	deadline := time.Now().Add(2 * time.Second)
	for _, pid := range pids {
		for exec.Command("ps", "-p", pid).Run() == nil {
			if time.Now().After(deadline) {
				t.Fatal("native descendant survived cancellation", pid)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	pids = nil
	writeJSONFixture(t, filepath.Join(dir, "native-fixture.json"), defaultLarkConfigurationState())
	if _, err := client.Verify(t.Context()); err != nil {
		t.Fatal("cancelled read retained account gate", err)
	}
}
