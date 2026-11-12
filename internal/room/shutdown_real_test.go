//go:build darwin || linux

package room

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func (d *realRoomDriver) shutdownRecovery() error {
	if err := d.setup(); err != nil {
		return err
	}
	if err := d.startReal(1); err != nil {
		return err
	}
	var member MemberInfo
	if err := d.command(&member, "member", "add", "--agent", "reviewer", "--runner", "dsh", "--profile", "reviewer"); err != nil {
		return err
	}
	d.memberDir = filepath.Join(d.dir, "agents", member.MembershipID)
	memory, err := newOpaqueID()
	if err != nil {
		return err
	}
	marker := d.report["privateMarker"].(string)
	d.phase("establish-original-private-memory")
	if err := d.command(nil, "send", "--text", "Read this message and remain silent in the Room. Remember this token privately for later: "+memory+". Give a private answer only.", "--id", "memory"); err != nil {
		return err
	}
	if err := d.command(nil, "member", "run", "--agent", "reviewer"); err != nil {
		return err
	}
	if _, err := d.waitAgent("reviewer", 1, "completed"); err != nil {
		return err
	}
	if err := d.privateResult("beforeShutdown", "end_turn", memory, marker); err != nil {
		return err
	}
	original := d.sessionID
	root, err := d.newRoot(realProcessIdentity{})
	if err != nil {
		return err
	}
	d.phase("wait-for-real-running-tool")
	text := "Read this message, then use the bash tool with run_in_background=false and timeoutMs=180000 to run exactly this shell command and wait for it to finish: printf ready > shutdown-busy; sleep 120; printf late > shutdown-late. Do not send to the Room."
	if err := d.command(nil, "send", "--text", text, "--id", "busy"); err != nil {
		return err
	}
	if err := d.until(func() (bool, error) {
		_, err := os.Stat(filepath.Join(d.workspace, "shutdown-busy"))
		if !errors.Is(err, os.ErrNotExist) {
			return err == nil, err
		}
		list, err := d.members()
		if err != nil {
			return false, err
		}
		m, err := findMember(list, "reviewer")
		if err != nil {
			return false, err
		}
		if !m.Execution.Active && m.Execution.Agent.TurnNumber >= 2 {
			return false, fmt.Errorf("turn ended before the long tool started: %s", m.Execution.Error)
		}
		return false, nil
	}); err != nil {
		return err
	}
	rows, err := realProcesses(d.ctx)
	if err != nil {
		return err
	}
	owned := []realProcessIdentity{root}
	for changed := true; changed; {
		changed = false
		for _, row := range rows {
			seen, parent := false, false
			for _, owner := range owned {
				seen = seen || row.PID == owner.PID
				parent = parent || row.Parent == owner.PID
			}
			if !seen && parent {
				owned = append(owned, row)
				changed = true
			}
		}
	}
	if len(owned) < 2 {
		return errors.New("no running descendant observed before Ctrl-C")
	}
	d.report["ownedBeforeShutdown"] = owned
	previous := d.info
	d.phase("ctrl-c-with-tool-running")
	started := time.Now()
	// Capture identities plus process state/executable names, never command
	// arguments or environments, while the actual interrupt is settling.
	stop := make(chan error, 1)
	go func() { stop <- d.stopReal() }()
	var observations []string
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var stopErr error
observing:
	for {
		select {
		case stopErr = <-stop:
			break observing
		case <-ticker.C:
			for _, owner := range owned {
				cmd := exec.CommandContext(d.ctx, "/bin/ps", "-p", fmt.Sprint(owner.PID), "-o", "pid=,ppid=,pgid=,lstart=,stat=,comm=")
				if data, err := cmd.Output(); err == nil {
					observations = append(observations, string(data))
				}
			}
		}
	}
	d.report["shutdownProcessObservations"] = observations
	if stopErr != nil {
		return stopErr
	}
	d.report["shutdownSeconds"] = time.Since(started).Seconds()
	if err := d.stoppedRoots(owned...); err != nil {
		return err
	}
	d.report["ownedProcessesStopped"] = true
	backendDir := filepath.Join(d.memberDir, "session.json.dsh")
	if err := dsh.CheckResume(backendDir); err != nil {
		return fmt.Errorf("Ctrl-C lost normal shutdown evidence: %w", err)
	}
	d.report["normalShutdownVerified"] = true
	var agent AgentSnapshot
	if err := jsonFile(filepath.Join(d.memberDir, "agent.json"), &agent); err != nil {
		return err
	}
	if agent.Turn == nil || agent.TurnNumber != 2 || agent.Turn.Status != "interrupted" {
		return errors.New("Ctrl-C did not durably settle the running turn as interrupted")
	}
	d.report["interruptedCheckpoint"] = agent
	if err := d.privateResult("cancelledTurn", "cancelled"); err != nil {
		return err
	}
	var before RoomSnapshot
	if err := jsonFile(filepath.Join(d.dir, "room.json"), &before); err != nil {
		return err
	}
	d.phase("same-directory-restart-enabled-and-lazy")
	if err := d.startReal(2); err != nil {
		return err
	}
	var after RoomSnapshot
	if err := jsonFile(filepath.Join(d.dir, "room.json"), &after); err != nil {
		return err
	}
	list, err := d.members()
	if err != nil {
		return err
	}
	m, err := findMember(list, "reviewer")
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) || d.info.RoomIdentity != previous.RoomIdentity || d.info.ServiceEpoch == previous.ServiceEpoch ||
		m.MembershipID != member.MembershipID || m.Execution.Paused || m.Execution.Active || m.Execution.Error != "" || m.Execution.Agent.TurnNumber != 2 {
		return errors.New("restart changed durable state or failed to restore enabled intent without replay")
	}
	rows, err = realProcesses(d.ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Parent == d.process.Process.Pid && row.Group == row.PID && row.Name != "ps" {
			return errors.New("idle recovery eagerly launched a DSH process")
		}
	}
	d.report["restartEnabledAndLazy"], d.report["afterRestart"] = true, list
	d.phase("automatic-resume-private-memory-and-current-cli")
	text = "Read this NEW message through the configured TeamX CLI. Remain silent in the Room. In private output recall the FIRST token from conversation memory, plus your private role marker. Do not reread old Room history or inspect files to find the token. Do not repeat the earlier shell command."
	if err := d.command(nil, "send", "--text", text, "--id", "recall"); err != nil {
		return err
	}
	m, err = d.waitAgent("reviewer", 3, "completed")
	if err != nil {
		return err
	}
	if m.ReadThrough != 3 || m.UnreadCount != 0 {
		return errors.New("resumed CLI did not consume new input using current identity")
	}
	if err := d.privateResult("afterResume", "end_turn", memory, marker); err != nil {
		return err
	}
	if d.sessionID != original {
		return errors.New("original session was replaced")
	}
	d.report["sameSessionId"], d.report["privateMemoryPreserved"], d.report["currentToolIdentityVerified"] = true, true, true
	resumedRoot, err := d.newRoot(realProcessIdentity{})
	if err != nil {
		return err
	}
	d.phase("final-normal-close")
	if err := d.stopReal(); err != nil {
		return err
	}
	if err := d.stoppedRoots(resumedRoot); err != nil {
		return err
	}
	if err := dsh.CheckResume(backendDir); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(d.workspace, "shutdown-late")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("cancelled tool produced its late side effect")
	}
	d.report["noLateToolEffect"], d.report["finalNormalShutdownVerified"] = true, true
	return nil
}
