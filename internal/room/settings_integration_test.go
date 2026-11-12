//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemSettingsServePersistsToStartupPath(t *testing.T) {
	parallelFixtureTest(t)
	binary := productionFixtureCLI(t)
	for _, mode := range []string{"managed-relative", "single-relative", "managed-default", "single-default"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "custom", "config.toml")
			args := []string{"serve", "--listen", "127.0.0.1:0"}
			if strings.HasSuffix(mode, "default") {
				configPath = filepath.Join(root, ".teamx", "config.toml")
			} else {
				if err := os.Mkdir(filepath.Dir(configPath), 0700); err != nil {
					t.Fatal(err)
				}
				writeStartupConfig(t, filepath.Dir(configPath), "# preserve this comment\n[server]\nsession_max_turns = 2 # allowance\n")
				args = append(args, "--config", "custom/config.toml")
			}
			if err := runAccessInit([]string{"--config", configPath}, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			// Access initialization serializes config. Capture preserved-comment input
			// after initialization so the setting save still proves byte preservation.
			if strings.HasSuffix(mode, "relative") {
				data, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				data = append([]byte("# preserve this comment\n"), data...)
				data = []byte(strings.Replace(string(data), "session_max_turns = 2", "session_max_turns = 2 # allowance", 1))
				if err = os.WriteFile(configPath, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(mode, "single") {
				args = append(args, "--room", "settings")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := func() *recoveryCLI {
				p := &recoveryCLI{cmd: exec.CommandContext(ctx, binary, args...)}
				p.cmd.Dir, p.cmd.Stderr = root, &p.logs
				for _, entry := range cleanRoomEnv() {
					if !strings.HasPrefix(entry, "HOME=") {
						p.cmd.Env = append(p.cmd.Env, entry)
					}
				}
				p.cmd.Env = append(p.cmd.Env, "HOME="+root)
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
					t.Fatal("readiness", err, p.logs.String())
				}
				return p
			}
			p := start()
			client := serverTestCLI(t, filepath.Dir(configPath), p.Endpoint)
			var initial struct {
				ServiceInfo
				settingsView
			}
			if err := client.request(ctx, "/v1/settings", nil, &initial); err != nil || initial.TimeZone != defaultTimeZone {
				t.Fatal(initial, err)
			}
			var saved settingsView
			request := settingsRequest{initial.ServiceInfo, serverTestCaller(t, filepath.Dir(configPath)), initial.SettingsVersion, "America/New_York"}
			if err := client.request(ctx, "/v1/settings/save", request, &saved); err != nil || saved != (settingsView{"America/New_York", 2}) {
				t.Fatal(saved, err)
			}
			// A second client sees the live update with the same service epoch.
			other := serverTestCLI(t, filepath.Dir(configPath), p.Endpoint)
			var live struct {
				ServiceInfo
				settingsView
			}
			if err := other.request(ctx, "/v1/ui", nil, &live); err != nil || live.settingsView != saved || live.ServiceEpoch != initial.ServiceEpoch {
				t.Fatal(live, err)
			}
			p.stop(t)
			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal("save did not use startup config path", err)
			}
			if strings.HasSuffix(mode, "relative") && (!strings.Contains(string(data), "# preserve this comment") || !strings.Contains(string(data), "session_max_turns = 2 # allowance")) {
				t.Fatal("other config text changed")
			}
			p = start()
			client.endpoint = p.Endpoint
			var restarted struct {
				ServiceInfo
				settingsView
			}
			if err := client.request(ctx, "/v1/settings", nil, &restarted); err != nil || restarted.TimeZone != saved.TimeZone || restarted.ServiceEpoch == initial.ServiceEpoch {
				t.Fatal("restart lost config or reused epoch", restarted, err)
			}
			p.stop(t)
		})
	}
}
