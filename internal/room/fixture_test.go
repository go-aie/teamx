package room

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// Reuse this suite's binary, preserving its build tags, race instrumentation
// and helper functions. No nested go test or extra compilation is necessary.
func buildFixtureCLI(t *testing.T, path string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	// The race detector reports before exit. Short CLI helpers do not need its
	// default one-second exit sleep; preserve all other inherited race options.
	script := "#!/bin/sh\nGORACE=\"$GORACE atexit_sleep_ms=0\" TEAMX_ROOM_FIXTURE=1 TEAMX_FIXTURE_CLI=" + quote(path) + " exec " + quote(binary) + " -test.run='^TestFixtureProcess$' -- \"$@\"\n"
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
