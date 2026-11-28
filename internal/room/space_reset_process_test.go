//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestSpaceResetProcessRestart(t *testing.T) {
	if os.Getenv("TEAMX_SPACE_RESET_PROCESS") != "1" {
		t.Skip("explicit compiled-process reset interruption acceptance")
	}
	home := t.TempDir()
	seed := resetSeedForTest(t)
	binary := productionFixtureCLI(t)
	launcher := testTeamDefinition(t).Runtime.DSH
	raw, err := toml.Marshal(map[string]any{"runtime": map[string]any{"dsh": launcher}, "models": modelCatalog{SchemaVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	mustWriteResetFile(t, filepath.Join(home, "config.toml"), string(raw))
	if err := os.MkdirAll(filepath.Join(home, "teams"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := initializeTeamTemplates(seed, teamDefaultsPath(filepath.Join(home, "config.toml"))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	start := func() (*exec.Cmd, <-chan error, cliClient) {
		cmd := exec.CommandContext(ctx, binary, "serve", "--home", home, "--listen", "127.0.0.1:0")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
		})
		var ready serverTestReady
		if err := json.NewDecoder(stdout).Decode(&ready); err != nil {
			t.Fatal(err)
		}
		return cmd, done, cliClient{endpoint: ready.Endpoint, http: &http.Client{Timeout: 5 * time.Second}, anonymousHuman: true}
	}
	first, firstDone, client := start()
	var directory spaceDirectory
	if err := client.request(ctx, "/v1/spaces", nil, &directory); err != nil {
		t.Fatal(err)
	}
	record := directory.Spaces[0]
	input := resetInputForTest(record)
	marker := filepath.Join(record.Roots.Data, "private-material")
	mustWriteResetFile(t, marker, "secret")
	lock, err := lockDirectory(record.Roots.Teams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if lock != nil {
			_ = unlockDirectory(lock)
		}
	})
	var accepted SpaceRecord
	if err := client.request(ctx, "/v1/spaces/"+record.ID+"/reset", input, &accepted); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := client.request(ctx, "/v1/spaces", nil, &directory); err != nil {
			t.Fatal(err)
		}
		if directory.Spaces[0].Reset.Code == "directory_busy" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reset did not preserve stopping under lock contention")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("deleted before lock/settlement confirmation", err)
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-firstDone
	if err := unlockDirectory(lock); err != nil {
		t.Fatal(err)
	}
	lock = nil
	second, secondDone, restarted := start()
	defer func() {
		_ = second.Process.Signal(os.Interrupt)
		select {
		case err := <-secondDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			_ = second.Process.Kill()
			t.Error("restarted process did not settle")
		}
	}()
	deadline = time.Now().Add(10 * time.Second)
	for {
		if err := restarted.request(ctx, "/v1/spaces", nil, &directory); err != nil {
			t.Fatal(err)
		}
		if directory.Spaces[0].Reset.Phase == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reset did not resume", directory.Spaces[0].Reset)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if directory.Spaces[0].ResetID() != input.RequestID {
		t.Fatal("request ID replaced on process restart")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("old material survived process recovery", err)
	}
	t.Log("compiled process killed during stopping; original request resumed to completed; old data removed; new setup available")
}
