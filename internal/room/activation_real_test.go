//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Flags allow repeatable real acceptance without shell environment wrappers.
var managementRealOutput = flag.String("teamx-management-real-output", "", "new absolute directory; enables real DSH managed-room acceptance")
var managementEnvFile = flag.String("teamx-management-env-file", "", "credential env file for real managed-room acceptance")
var managementBaseURL = flag.String("teamx-management-base-url", "", "model base URL for real managed-room acceptance")

// Production binary + production Room → MemberAgent → DSH. The small custom
// example bounds model spend and makes private memory and isolation observable.
func TestRealDSHManagedRooms(t *testing.T) {
	if *managementRealOutput != "" {
		t.Setenv("TEAMX_DSH_MANAGEMENT_REAL", "1")
		t.Setenv("TEAMX_DSH_OUTPUT", *managementRealOutput)
	}
	if *managementEnvFile != "" {
		t.Setenv("TEAMX_DSH_ENV_FILE", *managementEnvFile)
	}
	if *managementBaseURL != "" {
		t.Setenv("MODEL_BASE_URL", *managementBaseURL)
	}
	if os.Getenv("TEAMX_DSH_MANAGEMENT_REAL") != "1" {
		t.Skip("set TEAMX_DSH_MANAGEMENT_REAL=1 for real managed Room acceptance")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("TEAMX_DSH_OUTPUT must be a new absolute evidence directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(output, "rooms-root")}, output: output, report: map[string]any{"scenario": "RM-04", "status": "failed", "recordedAt": time.Now().UTC().Format(time.RFC3339)}}
	defer func() {
		if err := d.stopReal(); err != nil {
			d.report["cleanupError"] = err.Error()
			d.report["status"] = "failed"
			t.Error(err)
		}
		data, err := json.MarshalIndent(d.report, "", "  ")
		if err == nil {
			if d.secret != "" {
				data = bytes.ReplaceAll(data, []byte(d.secret), []byte("[REDACTED]"))
			}
			err = os.WriteFile(filepath.Join(output, "report.json"), append(data, '\n'), 0600)
		}
		if err != nil {
			t.Error(err)
		}
		t.Log("managed rooms report:", filepath.Join(output, "report.json"))
	}()
	if err := d.acceptManagedRooms(t); err != nil {
		d.report["error"] = err.Error()
		t.Fatal(err)
	}
	d.report["status"] = "passed"
}

func (d *realRoomDriver) startManaged(number int, teamsDir string) (string, error) {
	configPath, err := isolatedStartupConfig(d.output, map[string]string{"data_dir": d.dir, "teams_dir": teamsDir})
	if err != nil {
		return "", err
	}
	log, err := os.OpenFile(filepath.Join(d.output, fmt.Sprintf("serve-%d.log", number)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	cmd := exec.Command(d.binary, "serve", "--config", configPath, "--listen", "127.0.0.1:0", "--merge-window", "0s", "--max-turns", "8")
	cmd.Dir = os.TempDir() // Activation must use frozen Room data, independent of cwd.
	cmd.Env = append(operatorEnv(""), d.report["apiKeyEnv"].(string)+"="+d.secret)
	cmd.Stderr = log
	pipe, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		log.Close()
		return "", err
	}
	d.process, d.logFile, d.exited = cmd, log, make(chan error, 1)
	go func() { d.exited <- cmd.Wait() }()
	var ready struct {
		ServiceEpoch string `json:"service_epoch"`
		Endpoint     string `json:"endpoint"`
	}
	result := make(chan error, 1)
	go func() { result <- json.NewDecoder(pipe).Decode(&ready) }()
	select {
	case err = <-result:
	case <-time.After(20 * time.Second):
		pipe.Close()
		<-result
		err = errors.New("managed readiness timed out")
	}
	if err != nil {
		return "", err
	}
	d.endpoint = ready.Endpoint
	if err := d.until(func() (bool, error) {
		catalog, err := d.managedCatalog()
		return catalog.ServiceEpoch != "" && !catalog.Busy, err
	}); err != nil {
		return "", err
	}
	return ready.ServiceEpoch, nil
}

func (d *realRoomDriver) managementRequest(path string, in, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(d.ctx, http.MethodPost, d.endpoint+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		var e apiError
		_ = json.NewDecoder(res.Body).Decode(&e)
		return fmt.Errorf("%s: %s", path, e.Error)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (d *realRoomDriver) managedCatalog() (roomsResponse, error) {
	var catalog roomsResponse
	req, err := http.NewRequestWithContext(d.ctx, http.MethodGet, d.endpoint+"/v1/rooms", nil)
	if err != nil {
		return catalog, err
	}
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return catalog, err
	}
	defer res.Body.Close()
	if res.StatusCode == 409 {
		return catalog, nil
	} // A brief atomic index commit.
	if res.StatusCode != 200 {
		return catalog, fmt.Errorf("catalog: HTTP %d", res.StatusCode)
	}
	err = json.NewDecoder(res.Body).Decode(&catalog)
	return catalog, err
}

func (d *realRoomDriver) selectManaged(epoch string, room roomSummary, id string) error {
	var op roomActivation
	if err := d.managementRequest("/v1/rooms/activate", activationRequest{ServiceEpoch: epoch, Caller: humanCaller, RoomIdentity: room.RoomIdentity, ActivateID: id}, &op); err != nil {
		return err
	}
	return d.until(func() (bool, error) {
		list, err := d.managedCatalog()
		if err != nil {
			return false, err
		}
		if list.Activation == nil || list.Activation.ActivateID != id || list.Busy {
			return false, nil
		}
		if list.Activation.Status != "completed" {
			return false, fmt.Errorf("activation: %s", list.Activation.Error)
		}
		if list.Active == nil || list.Active.RoomIdentity != room.RoomIdentity {
			return false, errors.New("wrong active identity")
		}
		d.info = *list.Active
		return true, nil
	})
}

func (d *realRoomDriver) acceptManagedRooms(t *testing.T) error {
	if err := d.setup(); err != nil {
		return err
	}
	var profiles map[string]dshProfile
	if err := jsonFile(d.profilePath, &profiles); err != nil {
		return err
	}
	d.sessionID = ""

	source := filepath.Join(d.output, "teams")
	teamDir := filepath.Join(source, "memory")
	if err := os.MkdirAll(teamDir, 0700); err != nil {
		return err
	}
	p := profiles["reviewer"]
	runtime := teamRuntime{Model: p.Model}
	runtime.DSH.BinaryPath = p.BinaryPath
	runtime.DSH.BinaryArgs = p.BinaryArgs
	runtime.DSH.Artifact = p.Artifact
	team := teamDefinition{ID: "memory", Name: "Memory", Description: "Bounded recovery acceptance", OpeningMessage: "draft only", Runtime: &runtime,
		Members: []teamMember{{ID: "reviewer", Name: "Reviewer", Instructions: p.Instructions, ToolEnv: p.ToolEnv}}}
	data, _ := json.Marshal(team)
	if err := os.WriteFile(filepath.Join(teamDir, "team.json"), data, 0600); err != nil {
		return err
	}
	epoch, err := d.startManaged(1, source)
	if err != nil {
		return err
	}
	create := func(id string) (roomSummary, error) {
		var room roomSummary
		err := d.managementRequest("/v1/rooms/create", roomCreateRequest{ServiceEpoch: epoch, Caller: humanCaller, roomSelection: roomSelection{CreateID: id, TeamID: "memory"}}, &room)
		return room, err
	}
	a, err := create("a")
	if err != nil {
		return err
	}
	b, err := create("b")
	if err != nil {
		return err
	}
	d.report["roomA"], d.report["roomB"] = a, b
	if err := d.selectManaged(epoch, a, "first-a"); err != nil {
		return err
	}
	memberDir := func(room roomSummary) (string, error) {
		list, err := d.members()
		if err != nil {
			return "", err
		}
		m, err := findMember(list, "reviewer")
		return filepath.Join(d.dir, "rooms", room.RoomID, "agents", m.MembershipID), err
	}
	d.memberDir, err = memberDir(a)
	if err != nil {
		return err
	}
	aDir := d.memberDir
	token, err := newOpaqueID()
	if err != nil {
		return err
	}
	if err := d.command(nil, "send", "--id", "a-memory", "--text", "Read this message with the configured CLI, remain silent in the Room, and remember this token privately: "+token+". Reply privately with it and your private role marker."); err != nil {
		return err
	}
	if _, err := d.waitAgent("reviewer", 1, "completed"); err != nil {
		return err
	}
	if err := d.privateResult("aBeforeSwitch", "end_turn", token); err != nil {
		return err
	}
	original := d.sessionID
	if err := d.selectManaged(epoch, b, "first-b"); err != nil {
		return err
	}
	if err := dsh.CheckResume(filepath.Join(aDir, "session.json.dsh")); err != nil {
		return err
	}
	d.memberDir, err = memberDir(b)
	if err != nil {
		return err
	}
	bDir := d.memberDir
	if bDir == aDir {
		return errors.New("rooms share member state")
	}
	if err := d.command(nil, "send", "--id", "b-memory", "--text", "Read this message with the configured CLI, remain silent publicly, and privately answer with ROOM-B plus your private role marker."); err != nil {
		return err
	}
	if _, err := d.waitAgent("reviewer", 1, "completed"); err != nil {
		return err
	}
	d.sessionID = ""
	if err := d.privateResult("bIndependent", "end_turn", "ROOM-B"); err != nil {
		return err
	}
	if d.sessionID == original {
		return errors.New("rooms share a SessionID")
	}
	if err := d.selectManaged(epoch, a, "return-a"); err != nil {
		return err
	}
	d.memberDir, d.sessionID = aDir, original
	if err := d.command(nil, "send", "--id", "a-recall", "--text", "Read only this NEW unread message with the CLI and remain silent publicly. Recall the FIRST secret token from conversation memory and your private role marker. Do not read old Room history or files to recover it."); err != nil {
		return err
	}
	member, err := d.waitAgent("reviewer", 2, "completed")
	if err != nil {
		return err
	}
	if member.ReadThrough != 2 || member.Execution.TurnBudget.Used != 2 {
		return errors.New("cursor or cumulative budget changed across switch")
	}
	if err := d.privateResult("aAfterSwitch", "end_turn", token); err != nil {
		return err
	}
	if err := d.command(nil, "member", "pause", "--agent", "reviewer"); err != nil {
		return err
	}
	before := d.info
	if err := d.stopReal(); err != nil {
		return err
	}
	// Saved roles survive removal of the catalog on restart.
	epoch, err = d.startManaged(2, filepath.Join(d.output, "removed-teams"))
	if err != nil {
		return err
	}
	if err := d.until(func() (bool, error) {
		list, err := d.managedCatalog()
		if err != nil {
			return false, err
		}
		if list.Busy || list.Active == nil {
			return false, nil
		}
		d.info = *list.Active
		return true, nil
	}); err != nil {
		return err
	}
	list, err := d.members()
	if err != nil {
		return err
	}
	member, err = findMember(list, "reviewer")
	if err != nil {
		return err
	}
	if d.info.RoomIdentity != before.RoomIdentity || d.info.ServiceEpoch == before.ServiceEpoch || !member.Execution.Paused || member.Execution.TurnBudget.Used != 2 {
		return errors.New("restart lost selection, pause intent, budget or new epoch")
	}
	if err := d.selectManaged(epoch, b, "return-b"); err != nil {
		return err
	}
	d.phase("running-tool-switch")
	if err := d.command(nil, "send", "--id", "b-busy", "--text", "Read this message with the configured CLI. Use bash with run_in_background=false and timeoutMs=180000 to run exactly: printf ready > managed-busy; sleep 120; printf late > managed-late. Wait for it to finish. Remain silent in the Room."); err != nil {
		return err
	}
	workspace := filepath.Join(d.dir, "rooms", b.RoomID, "workspaces", filepath.Base(bDir))
	if err := d.until(func() (bool, error) {
		_, err := os.Stat(filepath.Join(workspace, "managed-busy"))
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}); err != nil {
		return err
	}
	if err := d.selectManaged(epoch, a, "cancel-b-enter-a"); err != nil {
		return err
	}
	if err := dsh.CheckResume(filepath.Join(bDir, "session.json.dsh")); err != nil {
		return err
	}
	if err := d.selectManaged(epoch, b, "interrupted-b-paused"); err != nil {
		return err
	}
	list, err = d.members()
	if err != nil {
		return err
	}
	member, err = findMember(list, "reviewer")
	if err != nil {
		return err
	}
	if !member.Execution.Paused || member.Execution.Active || member.Execution.Agent.Turn.Status != "interrupted" || member.Execution.TurnBudget.Used != 2 {
		return errors.New("interrupted turn replayed or budget renewed")
	}
	if _, err := os.Stat(filepath.Join(workspace, "managed-late")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("cancelled tool produced late effect")
	}
	d.report["sameOriginalSession"], d.report["independentSessions"], d.report["pauseAndBudgetSurviveRestart"] = true, true, true
	d.report["runningToolSettled"], d.report["interruptedMemberPaused"], d.report["noLateToolEffect"] = true, true, true
	return nil
}
