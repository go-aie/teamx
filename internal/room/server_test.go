package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type serverTestReady struct {
	Endpoint     string `json:"endpoint"`
	Status       string `json:"status"`
	Ready        bool   `json:"ready"`
	ServiceEpoch string `json:"service_epoch"`
	DataDir      string `json:"data_dir"`
}

// Catalog recovery can reject reads and idempotent create/send requests before
// admission. Retry only that explicit response, preserving the original ID.
func serverTestRequest(ctx context.Context, client cliClient, path string, request, response any) error {
	for {
		err := client.request(ctx, path, request, response)
		if err == nil || !strings.HasPrefix(err.Error(), "busy:") {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func startTestServer(t *testing.T, args []string, options serverOptions) (serverTestReady, func()) {
	t.Helper()
	var home, root string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--home" {
			home = args[i+1]
		}
		if args[i] == "--server-root" {
			root = args[i+1]
		}
	}
	if root != "" {
		layout, err := resolveServerLayout(root, home)
		if err != nil {
			t.Fatal(err)
		}
		if err = initializeBundleRuntime(layout.Release, layout.Home, filepath.Join(layout.Root, "current", "bin", "dsh")); err != nil {
			t.Fatal(err)
		}
	}
	if home != "" {
		testAccessConfig(t, home)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	started := make(chan serverTestReady, 1)
	options.ready = func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		var info serverTestReady
		if err := json.Unmarshal(data, &info); err != nil {
			return err
		}
		started <- info
		return nil
	}
	args = append(args, "--listen", "127.0.0.1:0")
	go func() { done <- runServer(ctx, args, io.Discard, io.Discard, options) }()
	stopped := false
	stop := func() {
		t.Helper()
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error("server shutdown", err)
			}
		case <-time.After(35 * time.Second):
			t.Fatal("server did not stop within shutdown budget")
		}
	}
	t.Cleanup(stop)
	select {
	case info := <-started:
		return info, stop
	case err := <-done:
		stopped = true
		cancel()
		t.Fatal("server startup", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not report startup state")
	}
	return serverTestReady{}, stop
}

func TestServerRestrictedSetupOwnsHomeAndData(t *testing.T) {
	home := filepath.Join(t.TempDir(), "私有 home")
	info, stop := startTestServer(t, []string{"--home", home}, serverOptions{create: func(string, string, serviceConfig) (*service, error) {
		t.Error("setup created a business service")
		return nil, errors.New("unexpected service")
	}})
	if info.Status != "setup_required" || info.Ready {
		t.Fatal("missing model reported business ready", info)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	for _, path := range []string{"/healthz", "/", "/v1/rooms", "/v1/platforms", "/v1/settings", "/v1/setup"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req, _ := http.NewRequest(method, info.Endpoint+path, strings.NewReader(`{"token":"synthetic-secret"}`))
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			want := http.StatusUnauthorized
			if method == http.MethodGet && (path == "/healthz" || path == "/") {
				want = http.StatusOK
			}
			if err != nil || response.StatusCode != want || strings.Contains(string(data), home) || strings.Contains(string(data), "synthetic-secret") {
				t.Fatal(method, path, response.StatusCode, string(data), err)
			}
		}
	}
	if err := runServer(context.Background(), []string{"--home", home, "--listen", "127.0.0.1:0"}, io.Discard, io.Discard, serverOptions{}); !errors.Is(err, ErrLocked) {
		t.Fatal("second home owner accepted", err)
	}
	if m, err := newRoomManager(filepath.Join(home, "data"), 7); !errors.Is(err, ErrLocked) {
		if m != nil {
			_ = m.close()
		}
		t.Fatal("setup did not own the data directory", err)
	}
	stop()
	if _, err := os.Stat(filepath.Join(home, "data", "rooms.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("setup initialized business state", err)
	}
	m, err := newRoomManager(filepath.Join(home, "data"), 7)
	if err != nil {
		t.Fatal("shutdown retained data ownership", err)
	}
	_ = m.close()
}

func TestServerStartupFailureReleasesOwnership(t *testing.T) {
	home := t.TempDir()
	testAccessConfig(t, home)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	args := []string{"--home", home, "--listen", listener.Addr().String()}
	err = runServer(context.Background(), args, io.Discard, io.Discard, serverOptions{})
	var op *net.OpError
	if !errors.As(err, &op) || !strings.Contains(err.Error(), listener.Addr().String()) {
		t.Fatal("port conflict omitted address/cause", err)
	}
	failed := errors.New("readiness consumer failed")
	err = runServer(context.Background(), []string{"--home", home, "--listen", "127.0.0.1:0"}, io.Discard, io.Discard, serverOptions{ready: func(any) error { return failed }})
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	_, stop := startTestServer(t, []string{"--home", home}, serverOptions{})
	stop()
}

func TestServerModelRepairRequiresRestartBeforeBusinessStartup(t *testing.T) {
	home := t.TempDir()
	r := testTeamDefinition(t).Runtime
	key := r.Model.APIKeyEnv
	t.Setenv(key, "") // An explicitly empty exported key overrides config env.
	config := map[string]any{"runtime": r, "env": map[string]string{key: "offline-model-key"}}
	data, err := toml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	info, stop := startTestServer(t, []string{"--home", home}, serverOptions{})
	if info.Status != "setup_required" {
		t.Fatal("empty credential opened business service", info)
	}
	stop()
	legacy, err := newRoomManager(filepath.Join(home, "data"), defaultMaxTurns)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(key, "offline-model-key")
	info, stop = startTestServer(t, []string{"--home", home}, serverOptions{create: controlledActivationFactory(nil, nil)})
	if info.Status == "setup_required" || info.ServiceEpoch == "" {
		t.Fatal("repaired model did not start normal service", info)
	}
	stop()
	// Invalid model structure also stays outside the Room manager.
	r.Model.Protocol = "unsupported"
	data, err = toml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(home, "data/rooms.json"))
	if err != nil {
		t.Fatal(err)
	}
	info, stop = startTestServer(t, []string{"--home", home}, serverOptions{})
	if info.Status != "setup_required" {
		t.Fatal("invalid model opened business service", info)
	}
	stop()
	after, err := os.ReadFile(filepath.Join(home, "data/rooms.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("repair state rewrote business data", err)
	}
}

func testServerRelease(t *testing.T, root, id string) string {
	t.Helper()
	release := filepath.Join(root, "releases", id)
	for _, dir := range []string{"bin", "share/teamx/teams"} {
		if err := os.MkdirAll(filepath.Join(release, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := json.Marshal(map[string]any{"schema_version": 1, "version": id, "profile": "standard", "target": runtime.GOOS + "-" + runtime.GOARCH})
	for name, b := range map[string][]byte{"manifest.json": data, "bin/dsh": []byte("#!/bin/sh\nexit 0\n")} {
		if err := os.WriteFile(filepath.Join(release, name), b, 0700); err != nil {
			t.Fatal(err)
		}
	}
	team := testTeamDefinition(t)
	team.Runtime = nil
	writeTestTeam(t, filepath.Join(release, "share/teamx/teams"), team)
	return release
}

func selectTestRelease(t *testing.T, root, release string) {
	t.Helper()
	path := filepath.Join(root, "current")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Symlink(release, path); err != nil {
		t.Fatal(err)
	}
}

func TestServerReleaseSwitchPreservesFrozenRoomsAndRegistration(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, home := filepath.Join(base, "服务 root"), filepath.Join(base, "数据 home")
	first, second := testServerRelease(t, root, "first"), testServerRelease(t, root, "second")
	selectTestRelease(t, root, first)
	layout, err := resolveServerLayout(root, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeBundleRuntime(layout.Release, layout.Home, filepath.Join(root, "current/bin/dsh")); err != nil {
		t.Fatal(err)
	}
	// Keep the managed binary selector while supplying an offline model.
	c, err := readStartupConfig(filepath.Join(home, "config.toml"), true, false)
	if err != nil {
		t.Fatal(err)
	}
	c.Models = nil // This test exercises a pre-catalog deployment.
	c.Runtime.Model = testTeamDefinition(t).Runtime.Model
	c.Env = map[string]string{c.Runtime.Model.APIKeyEnv: "fixture-only-key"}
	data, err := toml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	// Resources are read-only; only server root metadata and home are writable.
	for _, release := range []string{first, second} {
		if err := filepath.WalkDir(release, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0700) })
			return os.Chmod(path, 0500)
		}); err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := newRoomManager(filepath.Join(home, "data"), defaultMaxTurns)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.close(); err != nil {
		t.Fatal(err)
	}
	var previous roomSummary
	var registration []byte
	for i, release := range []string{first, second} {
		selectTestRelease(t, root, release)
		info, stop := startTestServer(t, []string{"--server-root", root, "--home", home}, serverOptions{create: controlledActivationFactory(nil, nil)})
		if info.Status == "setup_required" || info.ServiceEpoch == "" || info.DataDir != filepath.Join(home, "data") {
			t.Fatal(info)
		}
		client := serverTestCLI(t, home, info.Endpoint)
		var created roomSummary
		req := roomCreateRequest{ServiceEpoch: info.ServiceEpoch, Caller: serverTestCaller(t, home), roomSelection: roomSelection{TeamID: "sample", CreateID: filepath.Base(release)}}
		requestCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := serverTestRequest(requestCtx, client, "/v1/rooms/create", req, &created)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if err := runServer(context.Background(), []string{"--server-root", root, "--home", home, "--listen", "127.0.0.1:0"}, io.Discard, io.Discard, serverOptions{}); !errors.Is(err, ErrLocked) {
			t.Fatal("second configured server accepted", err)
		}
		stop()
		var saved managedRoomConfig
		if err := readSnapshot(filepath.Join(home, "data/rooms", created.RoomID, "room-config.json"), &saved, maxRoomsFileBytes); err != nil || saved.Runtime.DSH.BinaryPath != filepath.Join(release, "bin/dsh") {
			t.Fatal("new Room did not freeze selected real release", saved.Runtime, err)
		}
		if i == 1 {
			var old managedRoomConfig
			if err := readSnapshot(filepath.Join(home, "data/rooms", previous.RoomID, "room-config.json"), &old, maxRoomsFileBytes); err != nil || old.Runtime.DSH.BinaryPath != filepath.Join(first, "bin/dsh") {
				t.Fatal("release switch rewrote old Room", old.Runtime, err)
			}
		}
		b, err := os.ReadFile(filepath.Join(root, "installation.json"))
		if err != nil || i == 1 && string(b) != string(registration) {
			t.Fatal("restart replaced installation identity", err)
		}
		previous, registration = created, b
	}
	other, err := resolveServerLayout(root, filepath.Join(base, "other-home"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registerServerInstallation(other); err == nil {
		t.Fatal("installation was rebound to another home")
	}
	// Explicit external runtime paths are never replaced with the package default.
	c.Runtime.DSH.BinaryPath = "/external/custom/dsh"
	if err := pinBundledRuntime(&c, layout); err != nil || c.Runtime.DSH.BinaryPath != "/external/custom/dsh" {
		t.Fatal("external runtime was replaced", err)
	}
}

func TestServerDirectoryDefaultsAndInvalidSelection(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		root, home, err := defaultServerDirectories(goos, "/Users/部署者")
		if err != nil || !filepath.IsAbs(root) || !filepath.IsAbs(home) || pathWithin(root, home) {
			t.Fatal(goos, root, home, err)
		}
	}
	if _, _, err := defaultServerDirectories("windows", "unused"); err == nil {
		t.Fatal("unsupported deployment accepted")
	}
	root := t.TempDir()
	home := filepath.Join(t.TempDir(), "uncreated")
	if _, err := resolveServerLayout(root, home); err == nil {
		t.Fatal("missing release accepted")
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("broken installation created fresh data", err)
	}
	selectTestRelease(t, root, t.TempDir())
	if _, err := resolveServerLayout(root, home); err == nil {
		t.Fatal("release outside installation accepted")
	}
	release := testServerRelease(t, root, "version")
	selectTestRelease(t, root, release)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(release, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveServerLayout(root, filepath.Join(alias, "uncreated-home")); err == nil {
		t.Fatal("symlinked home within release accepted")
	}
	if _, err := os.Stat(filepath.Join(release, "uncreated-home")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid home wrote into release", err)
	}
}

func serverTestCLI(t *testing.T, home, endpoint string) cliClient {
	t.Helper()
	c, err := readStartupConfig(filepath.Join(home, "config.toml"), true, false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := readAdminToken(adminTokenPath(c))
	if err != nil {
		t.Fatal(err)
	}
	return cliClient{endpoint: endpoint, http: &http.Client{Timeout: 5 * time.Second}, token: token}
}
func serverTestCaller(t *testing.T, home string) Caller {
	t.Helper()
	c, err := readStartupConfig(filepath.Join(home, "config.toml"), true, false)
	if err != nil {
		t.Fatal(err)
	}
	return Caller{Author: Author{Kind: "human", ID: c.Access.OperatorID}}
}
