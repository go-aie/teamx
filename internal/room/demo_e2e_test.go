//go:build e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFixtureDemoAndReport(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "teamx")
	buildFixtureCLI(t, binary)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	dir := t.TempDir()
	cmd := exec.CommandContext(ctx, binary, "demo", "--state-dir", dir, "--json")
	cmd.Env = append(cleanRoomEnv(), "TEAMX_ENDPOINT=http://127.0.0.1:1", "TEAMX_AGENT_ID=stale-agent", "TEAMX_MEMBERSHIP_ID=stale-membership")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("demo: %v: %s", err, stderr.String())
	}
	var result, saved demoReport
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "demo-report.json")
	if err := readSnapshot(path, &saved, maxPrivateFileBytes); err != nil || !reflect.DeepEqual(result, saved) {
		t.Fatal("stdout and saved report differ", err)
	}
	if result.Scenario != "P1-06" || len(result.Checks) != 9 || len(result.Stages) != 6 || len(result.History) != 6 || len(result.Sessions) != 1 {
		t.Fatal(result)
	}
	// A second invocation must not clear or overwrite an existing Room.
	again := exec.CommandContext(ctx, binary, "demo", "--state-dir", dir, "--json")
	again.Env = cleanRoomEnv()
	if output, err := again.CombinedOutput(); err == nil || !strings.Contains(string(output), "new or empty") {
		t.Fatal("demo overwrote an existing directory", string(output), err)
	}
	var after demoReport
	if err := readSnapshot(path, &after, maxPrivateFileBytes); err != nil || !reflect.DeepEqual(saved, after) {
		t.Fatal("rejected rerun changed evidence", err)
	}
	store, err := OpenStore(dir, "design-review")
	if err != nil {
		t.Fatal("demo left a live service or directory lock", err)
	}
	defer store.Close()
	room, err := store.LoadRoom()
	if err != nil || len(room.Members) != 1 || room.Members[0].AgentID != "B" || room.Members[0].ReadThrough != 6 {
		t.Fatal(room, err)
	}
	for _, name := range []string{"serve-1.log", "serve-2.log"} {
		if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Size() == 0 {
			t.Fatal("service logs missing", name, err)
		}
	}
	// Readiness is a barrier proving this service started before cancellation.
	// A canceled driver still owns cleanup and must release the directory.
	cancelDir := t.TempDir()
	driverCtx, cancelDriver := context.WithCancel(ctx)
	defer cancelDriver()
	driver := &cliDriver{ctx: driverCtx, binary: binary, dir: cancelDir}
	if err := driver.start(1); err != nil {
		t.Fatal(err)
	}
	cancelDriver()
	if err := driver.stop(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal("canceled demo cleanup failed", err)
	}
	canceledStore, err := OpenStore(cancelDir, "design-review")
	if err != nil {
		t.Fatal("canceled demo retained a service or directory lock", err)
	}
	defer canceledStore.Close()
}
