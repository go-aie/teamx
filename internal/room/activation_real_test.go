//go:build darwin || linux

package room

import (
	"bytes"
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

func catalogService(list roomsResponse, id RoomIdentity) *ServiceInfo {
	for _, room := range list.Rooms {
		if room.RoomIdentity == id {
			return room.Service
		}
	}
	return nil
}

func (d *realRoomDriver) selectManaged(epoch string, room roomSummary, id string) error {
	d.phase("open-" + id)
	// Startup recovery reserves each Room before asynchronously inspecting it.
	// Query until that owner is available, then dispatch exactly one open.
	if err := d.until(func() (bool, error) {
		list, err := d.managedCatalog()
		if err != nil {
			return false, err
		}
		for _, summary := range list.Rooms {
			if summary.RoomIdentity == room.RoomIdentity {
				return !summary.Busy, nil
			}
		}
		return false, nil
	}); err != nil {
		return err
	}
	var op roomOpen
	if err := d.managementRequest("/v1/rooms/open", roomOpenRequest{ServiceEpoch: epoch, Caller: humanCaller, RoomIdentity: room.RoomIdentity, OpenID: id}, &op); err != nil {
		return err
	}
	return d.until(func() (bool, error) {
		list, err := d.managedCatalog()
		if err != nil {
			return false, err
		}
		if info := catalogService(list, room.RoomIdentity); info != nil {
			d.info = *info
			return true, nil
		}
		for _, summary := range list.Rooms {
			if summary.RoomIdentity == room.RoomIdentity && summary.RuntimeStatus == "failed" {
				return false, errors.New(summary.RuntimeError)
			}
		}
		return false, nil
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
	// A is paused and settled: it stays lazy until explicitly opened.
	if err := d.selectManaged(epoch, a, "paused-a-after-restart"); err != nil {
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
	d.phase("parallel-tools-and-background-public-send")
	workdir := func(room roomSummary) (string, error) {
		var config managedRoomConfig
		if err := jsonFile(filepath.Join(d.dir, "rooms", room.RoomID, "room-config.json"), &config); err != nil {
			return "", err
		}
		return filepath.Join(d.dir, "rooms", room.RoomID, config.Members[0].Workspace), nil
	}
	aWork, err := workdir(a)
	if err != nil {
		return err
	}
	bWork, err := workdir(b)
	if err != nil {
		return err
	}
	marker := func(path string) error {
		return d.until(func() (bool, error) {
			_, err := os.Stat(path)
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return err == nil, err
		})
	}
	// A's longer tool guarantees overlap even if B takes several model requests.
	if err := d.selectManaged(epoch, a, "parallel-a"); err != nil {
		return err
	}
	if err := d.command(nil, "member", "run", "--agent", "reviewer"); err != nil {
		return err
	}
	if err := d.command(nil, "send", "--id", "parallel-a", "--text", "Read this NEW message with the configured TeamX CLI. Use bash with run_in_background=false and timeoutMs=120000 to run exactly: printf ready > parallel-ready; sleep 60; printf done > parallel-done. When it finishes, explicitly send the public message runtime-A-done using the configured TeamX CLI. Reply privately PARALLEL-A-DONE. Do not launch background processes."); err != nil {
		return err
	}
	if err := marker(filepath.Join(aWork, "parallel-ready")); err != nil {
		return err
	}
	aInfo := d.info
	if err := d.selectManaged(epoch, b, "parallel-b"); err != nil {
		return err
	}
	if err := d.command(nil, "send", "--id", "parallel-b", "--text", "Read this NEW message with the configured TeamX CLI. Use bash with run_in_background=false and timeoutMs=90000 to run exactly: printf ready > parallel-ready; sleep 20; printf done > parallel-done. When it finishes, explicitly send the public message runtime-B-done using the configured TeamX CLI. Reply privately PARALLEL-B-DONE. Do not launch background processes."); err != nil {
		return err
	}
	if err := marker(filepath.Join(bWork, "parallel-ready")); err != nil {
		return err
	}
	bInfo := d.info
	bList, err := d.members()
	if err != nil {
		return err
	}
	bm, err := findMember(bList, "reviewer")
	if err != nil {
		return err
	}
	if err := d.selectManaged(epoch, a, "parallel-return-a"); err != nil {
		return err
	}
	aList, err := d.members()
	if err != nil {
		return err
	}
	am, err := findMember(aList, "reviewer")
	if err != nil {
		return err
	}
	if !am.Execution.Active || !bm.Execution.Active || d.info != aInfo {
		return errors.New("rooms did not overlap or return visit changed epoch")
	}
	d.report["overlap"] = map[string]any{"recordedAt": time.Now().UTC(), "a": am, "b": bm, "aService": aInfo, "bService": bInfo}
	if _, err := d.waitAgent("reviewer", 3, "completed"); err != nil {
		return err
	}
	d.memberDir, d.sessionID = aDir, original
	if err := d.privateResult("aParallelResult", "end_turn", "PARALLEL-A-DONE"); err != nil {
		return err
	}
	if err := d.selectManaged(epoch, b, "completed-b"); err != nil {
		return err
	}
	if _, err := d.waitAgent("reviewer", 2, "completed"); err != nil {
		return err
	}
	d.memberDir, d.sessionID = bDir, ""
	if err := d.privateResult("bParallelResult", "end_turn", "PARALLEL-B-DONE"); err != nil {
		return err
	}
	for _, room := range []roomSummary{a, b} {
		if err := d.selectManaged(epoch, room, "followup-"+room.CreateID); err != nil {
			return err
		}
		var page ReadPage
		if err := d.command(&page, "read", "--all"); err != nil {
			return err
		}
		found := false
		expected := "runtime-A-done"
		turn := uint64(4)
		if room.RoomIdentity == b.RoomIdentity {
			expected = "runtime-B-done"
			turn = 3
		}
		for _, msg := range page.Messages {
			found = found || msg.Author.Kind == "agent" && msg.Text == expected
		}
		if !found {
			return errors.New("background public send missing from owning room")
		}
		if err := d.command(nil, "send", "--id", "followup-"+room.CreateID, "--text", "Read only the NEW unread messages with the configured CLI, remain silent publicly, and reply privately FOLLOWUP-DONE."); err != nil {
			return err
		}
		if _, err := d.waitAgent("reviewer", turn, "completed"); err != nil {
			return err
		}
	}

	// A normal restart restores both enabled rooms without opening or viewing them.
	if err := d.stopReal(); err != nil {
		return err
	}
	epoch, err = d.startManaged(3, filepath.Join(d.output, "removed-teams"))
	if err != nil {
		return err
	}
	if err := d.until(func() (bool, error) {
		list, err := d.managedCatalog()
		return catalogService(list, a.RoomIdentity) != nil && catalogService(list, b.RoomIdentity) != nil, err
	}); err != nil {
		return err
	}
	d.report["unviewedDualRecovery"] = true
	// Confirm both long tools are actually executing before shutting the service down.
	for _, room := range []roomSummary{a, b} {
		if err := d.selectManaged(epoch, room, "shutdown-"+room.CreateID); err != nil {
			return err
		}
		if err := d.command(nil, "send", "--id", "shutdown-"+room.CreateID, "--text", "Read this NEW message with the configured TeamX CLI. Use bash with run_in_background=false and timeoutMs=180000 to run exactly: printf ready > shutdown-ready; sleep 120; printf late > shutdown-late. Wait for it to finish and remain silent publicly. Do not launch background processes."); err != nil {
			return err
		}
		work := aWork
		if room.RoomIdentity == b.RoomIdentity {
			work = bWork
		}
		if err := marker(filepath.Join(work, "shutdown-ready")); err != nil {
			return err
		}
	}
	d.phase("all-room-running-tool-shutdown")
	if err := d.stopReal(); err != nil {
		return err
	}
	for _, dir := range []string{aDir, bDir} {
		if err := dsh.CheckResume(filepath.Join(dir, "session.json.dsh")); err != nil {
			return err
		}
		if err := dsh.CheckReset(filepath.Join(dir, "session.json.dsh")); err != nil {
			return err
		}
	}
	for _, work := range []string{aWork, bWork} {
		if _, err := os.Stat(filepath.Join(work, "shutdown-late")); !errors.Is(err, os.ErrNotExist) {
			return errors.New("shutdown tool produced a late effect")
		}
	}

	var index roomsIndex
	if err := jsonFile(filepath.Join(d.dir, "rooms.json"), &index); err != nil {
		return err
	}
	if len(index.OwnedRooms) != 2 || len(index.Shutdowns) != 2 {
		return errors.New("all-room shutdown receipts missing")
	}
	for _, receipt := range index.Shutdowns {
		if receipt.Error != "" {
			return errors.New(receipt.Error)
		}
	}
	// Restart preserves interrupted turns and budgets; it must not replay either tool.
	epoch, err = d.startManaged(4, filepath.Join(d.output, "removed-teams"))
	if err != nil {
		return err
	}
	for _, room := range []roomSummary{a, b} {
		if err := d.selectManaged(epoch, room, "interrupted-"+room.CreateID); err != nil {
			return err
		}
		list, err := d.members()
		if err != nil {
			return err
		}
		member, err := findMember(list, "reviewer")
		if err != nil {
			return err
		}
		used := uint64(5)
		if room.RoomIdentity == b.RoomIdentity {
			used = 4
		}
		e := member.Execution
		if !e.Paused || e.Active || e.Agent.Turn == nil || e.Agent.Turn.Status != "interrupted" || e.TurnBudget.Used != used {
			return errors.New("shutdown interrupted turn replayed or budget changed")
		}
	}
	if err := d.stopReal(); err != nil {
		return err
	}
	d.report["runningToolsSettled"], d.report["interruptedTurnsNotReplayed"] = true, true

	d.report["shutdowns"] = index.Shutdowns
	d.report["sameOriginalSession"], d.report["independentSessions"], d.report["pauseAndBudgetSurviveRestart"] = true, true, true
	d.report["parallelTurns"], d.report["backgroundPublicMessages"], d.report["followupScheduling"], d.report["allRoomsSettled"] = true, true, true, true
	return nil
}
