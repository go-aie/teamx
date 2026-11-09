//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Runs actual installed DSH/model/CLI in fresh local data only. Browser mode
// retains the live service for UI acceptance and verifies its durable outcome.
func TestAutomaticRotationReal(t *testing.T) {
	if os.Getenv("TEAMX_ROTATION_REAL") != "1" {
		t.Skip("set TEAMX_ROTATION_REAL=1, TEAMX_ROTATION_CONFIG and a fresh TEAMX_DSH_OUTPUT")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "scope": "S3 automatic rotation and normal restart", "external_business_writes": 0}
	defer imageRealReport(t, output, report)
	config, err := loadStartupConfig(os.Getenv("TEAMX_ROTATION_CONFIG"), true)
	if err != nil || config.Runtime == nil {
		t.Fatal("runtime config required", err)
	}
	t.Setenv("TEAMX_DSH_BINARY_PATH", config.Runtime.DSH.BinaryPath)
	args, _ := json.Marshal(config.Runtime.DSH.BinaryArgs)
	if len(config.Runtime.DSH.BinaryArgs) > 0 {
		t.Setenv("TEAMX_DSH_BINARY_ARGS", string(args))
	}
	model := config.Runtime.Model.Clone()
	key := model.APIKeyEnv
	if os.Getenv(key) == "" && config.Env[key] != "" {
		t.Setenv(key, config.Env[key])
	}
	imageRealCredential(t, key)
	t.Setenv("MODEL_BASE_URL", model.BaseURL)
	t.Setenv("MODEL_ID", model.ModelID)
	t.Setenv("TEAMX_DSH_API_KEY_ENV", key)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(output, "room")}, output: output, report: report, modelOverride: &model, sessionMaxTurns: 2}
	defer func() {
		if err := d.stopReal(); err != nil {
			report["status"] = "failed"
			t.Error(err)
		}
	}()
	must := func(err error) {
		t.Helper()
		if err != nil {
			report["error"] = err.Error()
			t.Fatal(err)
		}
	}
	must(d.setup())
	var profiles map[string]dshProfile
	must(jsonFile(d.profilePath, &profiles))
	p := profiles["reviewer"]
	p.Instructions = `You are a bounded TeamX acceptance assistant. On EVERY normal turn first run exactly: "$TEAMX_CLI" read --unread --json. Recovery previews do not consume input. Then return only the requested S3_TURN marker privately. Never send to the Room, delegate, manage members, inspect files or environment, or use network/platform tools.`
	profiles["reviewer"] = p
	encoded, _ := json.Marshal(profiles)
	must(os.WriteFile(d.profilePath, encoded, 0600))
	must(d.startReal(1))
	must(d.command(nil, "member", "add", "--agent", "reviewer", "--runner", "dsh", "--profile", "reviewer"))
	must(d.command(nil, "member", "run", "--agent", "reviewer"))
	generations := map[string]bool{}
	sessionIDs := map[string]bool{}
	var lastGeneration, lastID string
	var statuses []MemberStatus
	check := func(n uint64) MemberStatus {
		t.Helper()
		list, err := d.waitReal(n, "completed")
		must(err)
		member, err := findMember(list, "reviewer")
		must(err)
		s := member.Execution
		if s.Paused || s.SessionTurns == nil || s.SessionTurns.Total != n || s.SessionTurns.Used != (n-1)%2+1 || member.ReadThrough != n || member.UnreadCount != 0 {
			t.Fatalf("turn %d has invalid counters or cursors: %+v", n, s)
		}
		d.memberDir = filepath.Join(d.dir, "agents", member.MembershipID)
		var backend struct {
			SessionID string `json:"sessionId"`
		}
		must(jsonFile(filepath.Join(d.memberDir, s.SessionGeneration, "backend.json"), &backend))
		if backend.SessionID == "" {
			t.Fatal("missing real SessionID")
		}
		lastGeneration, lastID = s.SessionGeneration, backend.SessionID
		generations[lastGeneration], sessionIDs[lastID] = true, true
		statuses = append(statuses, *s)
		return *s
	}
	for n := uint64(1); n <= 5; n++ {
		d.phase(fmt.Sprintf("automatic-turn-%d", n))
		text := fmt.Sprintf("First run exactly \"$TEAMX_CLI\" read --unread --json in bash to consume this Room message. Answer privately with S3_TURN_%d only. Do not send to the Room, use network tools, or do any other work.", n)
		must(d.command(nil, "send", "--text", text, "--id", fmt.Sprint(n)))
		check(n)
		if n == 2 || n == 4 {
			time.Sleep(350 * time.Millisecond)
			list, err := d.members()
			must(err)
			member, err := findMember(list, "reviewer")
			must(err)
			if member.Execution.SessionGeneration != lastGeneration || member.Execution.Active || member.Execution.Agent.TurnNumber != n {
				t.Fatal("idle Session rotated or executed")
			}
		}
	}
	if len(generations) != 3 || len(sessionIDs) != 3 {
		t.Fatal("two real automatic rotations not observed")
	}
	generationBefore, idBefore := lastGeneration, lastID
	must(d.stopReal())
	must(d.startReal(2))
	d.phase("normal-restart-turn-6")
	must(d.command(nil, "send", "--text", "First run exactly \"$TEAMX_CLI\" read --unread --json in bash to consume this Room message. Answer privately S3_TURN_6. Do not send to the Room or use any other tools.", "--id", "6"))
	check(6)
	if lastGeneration != generationBefore || lastID != idBefore {
		t.Fatal("normal restart did not resume original Session")
	}
	report["normal_restart_same_session"], report["two_automatic_rotations"], report["idle_did_not_rotate"] = true, true, true
	if os.Getenv("TEAMX_ROTATION_BROWSER") == "1" {
		ready := map[string]any{"endpoint": d.endpoint, "info": d.info, "turn": 6, "total": 6, "session_turns": 2, "session_max_turns": 2}
		data, _ := json.MarshalIndent(ready, "", "  ")
		must(os.WriteFile(filepath.Join(output, "browser-ready.json"), data, 0600))
		d.phase("browser-ready")
		must(d.until(func() (bool, error) {
			_, err := os.Stat(filepath.Join(output, "browser-done.json"))
			if os.IsNotExist(err) {
				return false, nil
			}
			return err == nil, err
		}))
		// The browser submits turn 7, pauses and submits another message while
		// paused. No enable/reset/continue operation is needed across the threshold.
		list, err := d.members()
		must(err)
		member, err := findMember(list, "reviewer")
		must(err)
		if !member.Execution.Paused || member.Execution.Active || member.Execution.SessionTurns.Total != 7 || member.Execution.SessionTurns.Used != 1 || member.Execution.SessionGeneration == lastGeneration || member.UnreadCount != 1 {
			t.Fatal("browser normal path or pause outcome invalid", member)
		}
		report["browser_outcome"] = member.Execution
	}
	report["statuses"], report["session_ids"], report["generations"] = statuses, sessionIDs, generations
	report["status"], report["phase"] = "passed", "complete"
}
