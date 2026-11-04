package room

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-aie/actorx"
	"github.com/pelletier/go-toml/v2"
)

// Deterministic profiles exist only in the test binary. The production parser,
// HTTP API and persisted member format still see ordinary DSH profile names.
var fixtureScripts = []string{"ignore", "read", "reply", "reply,read", "fail,ignore", "read,reply,ignore", "reply,read,ignore", "ignore,read", "read,read,read,read"}

func newFixtureService(dir, roomID string, config serviceConfig) (*service, error) {
	if config.endpoint == "" {
		config.endpoint = "http://127.0.0.1:1"
	}
	if config.cliPath == "" {
		config.cliPath = os.Args[0]
	}
	profiles := make(map[string]dshProfile, len(fixtureScripts))
	for _, script := range fixtureScripts {
		profiles["fixture-"+script] = dshProfile{}
	}
	config.dshProfiles = profiles
	config.uiProfiles = profileNames(profiles)
	if cli := os.Getenv("TEAMX_FIXTURE_CLI"); cli != "" {
		config.cliPath = cli
	}
	return startService(dir, roomID, config, func(s *service, state RoomSnapshot) error {
		s.memberFactory = func(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) actorx.Actor {
			return &MemberAgent{Options: SessionOptions{MemberIdentity: id, StatePath: filepath.Join(s.store.dir, "agents", id.MembershipID, "session.json"), CLIPath: s.cliPath, CLIEnv: s.toolEnv(id)}, Backend: virtualBackend{store: s.store, script: strings.TrimPrefix(runner.Profile, "fixture-")}, Store: s.store, Room: s.room, Restored: restored, MaxTurns: s.maxTurns, MergeWindow: s.mergeWindow}
		}
		if err := s.loadFixtureMembers(state); err != nil {
			return err
		}
		return s.attachMembers(state)
	})
}

var fixtureBuild struct {
	sync.Once
	path string
	err  error
}

// Compile once per suite. The launcher dispatches through a guarded test helper,
// so no simulator, demo or testing switch is compiled into cmd/teamx.
func buildFixtureCLI(t *testing.T, path string) {
	t.Helper()
	fixtureBuild.Do(func() {
		dir, err := os.MkdirTemp("", "teamx-fixture-binary-*")
		if err != nil {
			fixtureBuild.err = err
			return
		}
		fixtureBuild.path = filepath.Join(dir, "room.test")
		output, err := exec.Command("go", "test", "-c", "-o", fixtureBuild.path, ".").CombinedOutput()
		if err != nil {
			fixtureBuild.err = fmt.Errorf("build test harness: %w: %s", err, output)
		}
	})
	if fixtureBuild.err != nil {
		t.Fatal(fixtureBuild.err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\nTEAMX_ROOM_FIXTURE=1 TEAMX_FIXTURE_CLI=" + quote(path) + " exec " + quote(fixtureBuild.path) + " -test.run='^TestFixtureProcess$' -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestFixtureProcess(t *testing.T) {
	if os.Getenv("TEAMX_ROOM_FIXTURE") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	var err error
	switch {
	case len(args) > 0 && args[0] == "serve":
		err = serveWithFactory(args[1:], os.Stdout, os.Stderr, newFixtureService)
	case len(args) > 0 && args[0] == "demo":
		err = demo(args[1:], os.Stdout, os.Stderr)
	default:
		err = runCLI(context.Background(), args, os.Stdout, os.Stderr, os.Getenv)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestMain(m *testing.M) {
	code := m.Run()
	if fixtureBuild.path != "" {
		_ = os.RemoveAll(filepath.Dir(fixtureBuild.path))
	}
	os.Exit(code)
}

// Real and fixture CLI drivers keep operator configuration outside their test scope.
func isolatedStartupConfig(dir string, server map[string]string) (string, error) {
	path := filepath.Join(dir, "startup.config.toml")
	if server == nil {
		server = map[string]string{}
	}
	data, err := toml.Marshal(map[string]any{"server": server})
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0600)
}
