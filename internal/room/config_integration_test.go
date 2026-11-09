//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeConfigCLIOverridesAndSecretIsolation(t *testing.T) {
	parallelFixtureTest(t)
	binary := productionFixtureCLI(t)
	dir := t.TempDir()
	team := testTeamDefinition(t)
	team.Runtime = nil
	writeTestTeam(t, filepath.Join(dir, "teams"), team)
	const secret = "private-startup-secret-not-in-room-files"
	config := "[server]\nlisten = 'invalid'\ndata_dir = './data'\nteams_dir = './teams'\nsession_max_turns = 0\n[env]\nTEAMX_TEST_CONFIG_SECRET = '" + secret + "'\n"
	runtime := testTeamDefinition(t).Runtime
	config += "[runtime.dsh]\nbinary_path = '" + runtime.DSH.BinaryPath + "'\n[runtime.model]\nprotocol = '" + runtime.Model.Protocol + "'\nbase_url = '" + runtime.Model.BaseURL + "'\nmodel_id = '" + runtime.Model.ModelID + "'\napi_key_env = '" + runtime.Model.APIKeyEnv + "'\n"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, mode := range []string{"absolute", "project-relative"} {
		args := []string{"serve", "--listen", "127.0.0.1:0", "--session-max-turns", "7"}
		cwd := t.TempDir()
		configDir := dir
		configText := config
		if mode == "project-relative" {
			cwd = dir
			configDir = filepath.Join(dir, ".teamx")
			if err := os.Mkdir(configDir, 0700); err != nil {
				t.Fatal(err)
			}
			writeTestTeam(t, filepath.Join(configDir, "teams"), team)
		}
		path := writeStartupConfig(t, configDir, configText)
		if mode == "project-relative" {
			path = filepath.Join(".teamx", "config.toml")
		}
		args = append(args, "--config", path)
		p := &recoveryCLI{cmd: exec.CommandContext(ctx, binary, args...)}
		p.cmd.Dir, p.cmd.Env, p.cmd.Stderr = cwd, cleanRoomEnv(), &p.logs
		stdout, err := p.cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := p.cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if !p.waited {
				_ = p.cmd.Process.Kill()
				_ = p.cmd.Wait()
			}
		})
		if err := json.NewDecoder(stdout).Decode(p); err != nil {
			_ = p.cmd.Wait()
			p.waited = true
			t.Fatal("readiness", err, strings.ReplaceAll(p.logs.String(), secret, "[REDACTED]"))
		}
		client := cliClient{endpoint: p.Endpoint, http: &http.Client{Timeout: 5 * time.Second}}
		var room roomSummary
		req := roomCreateRequest{ServiceEpoch: p.ServiceEpoch, Caller: humanCaller, roomSelection: roomSelection{TeamID: "sample", CreateID: mode}}
		if err := client.request(ctx, "/v1/rooms/create", req, &room); err != nil {
			t.Fatal(err)
		}
		p.stop(t)
		var saved managedRoomConfig
		file := filepath.Join(configDir, "data/rooms", room.RoomID, "room-config.json")
		if err := readSnapshot(file, &saved, maxRoomsFileBytes); err != nil || saved.SessionMaxTurns != 7 || saved.Runtime == nil {
			t.Fatal("config paths or explicit CLI precedence lost", err)
		}
		if strings.Contains(p.logs.String(), secret) {
			t.Fatal("credential in service diagnostics")
		}
	}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if filepath.Base(path) == "config.toml" {
			return nil
		}
		b, err := os.ReadFile(path)
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("startup environment leaked into Room persistence")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
