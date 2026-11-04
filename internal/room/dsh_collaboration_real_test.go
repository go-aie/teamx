//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Two production DSH processes use real CLI tools. Explicitly enabled because
// this spends model budget; ordinary tests never read credentials or use models.
func TestRealDSHCollaboration(t *testing.T) {
	if os.Getenv("TEAMX_DSH_COLLAB_REAL") != "1" {
		t.Skip("set TEAMX_DSH_COLLAB_REAL=1 for P2-03 real acceptance")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("TEAMX_DSH_OUTPUT must be a new absolute directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(output, "room")}, output: output,
		report: map[string]any{"scenario": "P2-03 / DA2-01", "status": "failed", "recordedAt": time.Now().UTC().Format(time.RFC3339), "platform": runtime.GOOS + "/" + runtime.GOARCH, "recovery": "normal original-session resume; no crash takeover, replacement or rotation"}}
	defer func() {
		if err := d.stopReal(); err != nil {
			d.report["status"], d.report["cleanupError"] = "failed", err.Error()
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
		t.Logf("P2-03 report: %s", filepath.Join(output, "report.json"))
	}()
	if err := d.collaborate(); err != nil {
		d.report["error"] = err.Error()
		t.Fatal(err)
	}
	d.report["status"] = "passed"
}

func (d *realRoomDriver) waitAgent(agent string, turn uint64, status string) (serviceMember, error) {
	var member serviceMember
	err := d.until(func() (bool, error) {
		list, err := d.members()
		if err != nil {
			return false, err
		}
		if list.WriteError != "" {
			return false, errors.New(list.WriteError)
		}
		member, err = findMember(list, agent)
		if err != nil {
			return false, err
		}
		e := member.Execution
		if e.Active || e.Agent.TurnNumber < turn {
			return false, nil
		}
		if e.Agent.TurnNumber != turn || e.Agent.Turn == nil || e.Agent.Turn.Status != status || (status == "completed" && e.Error != "") {
			return false, fmt.Errorf("%s unexpected settled execution: %+v", agent, e)
		}
		return true, nil
	})
	return member, err
}

func (d *realRoomDriver) privateFor(agent, name, previous, memory, marker, foreign string) (string, error) {
	list, err := d.members()
	if err != nil {
		return "", err
	}
	m, err := findMember(list, agent)
	if err != nil {
		return "", err
	}
	d.memberDir = filepath.Join(d.dir, "agents", m.MembershipID)
	d.sessionID = previous
	if err := d.privateResult(name, "end_turn", memory, marker); err != nil {
		return "", err
	}
	var output struct {
		Result dsh.TurnResult `json:"result"`
	}
	if err := jsonFile(filepath.Join(d.memberDir, "dsh-turn.json"), &output); err != nil {
		return "", err
	}
	if strings.Contains(output.Result.Text, foreign) {
		return "", errors.New("private roles leaked between sessions")
	}
	return d.sessionID, nil
}

func (d *realRoomDriver) newRoot(exclude realProcessIdentity) (realProcessIdentity, error) {
	rows, err := realProcesses(d.ctx)
	if err != nil {
		return realProcessIdentity{}, err
	}
	var found []realProcessIdentity
	for _, row := range rows {
		// Owned DSH roots lead their own process group. The service also
		// briefly owns ps observers, which inherit its group and are not DSH.
		if row.Parent == d.process.Process.Pid && row.Group == row.PID && row != exclude {
			found = append(found, row)
		}
	}
	if len(found) != 1 {
		return realProcessIdentity{}, fmt.Errorf("expected one distinct DSH root, got %d", len(found))
	}
	return found[0], nil
}

func (d *realRoomDriver) stoppedRoots(roots ...realProcessIdentity) error {
	rows, err := realProcesses(d.ctx)
	if err != nil {
		return err
	}
	for _, root := range roots {
		for _, row := range rows {
			if row.PID == root.PID && row.Started == root.Started {
				return errors.New("owned DSH root survived shutdown")
			}
		}
	}
	return nil
}

func (d *realRoomDriver) collaborate() error {
	if err := d.setup(); err != nil {
		return err
	}
	var profiles map[string]dshProfile
	if err := jsonFile(d.profilePath, &profiles); err != nil {
		return err
	}
	markerA := d.report["privateMarker"].(string)
	markerB, err := newOpaqueID()
	if err != nil {
		return err
	}
	memory, err := newOpaqueID()
	if err != nil {
		return err
	}
	ack, err := newOpaqueID()
	if err != nil {
		return err
	}
	a := profiles["reviewer"]
	a.Instructions = "You are the reviewer in a two-member team. Follow Room requests. Remember the first private memory token across turns and restarts. Include it and your private role marker " + markerA + " in ordinary private answers. Never post the role marker. Only explicitly send when a new unread human message requests the reviewer acknowledgement; send it once. Never reread old Room history or inspect instructions/history files to answer memory checks. Never delegate."
	b := a
	b.Workspace = filepath.Join(d.output, "observer-workspace")
	if err := os.Mkdir(b.Workspace, 0700); err != nil {
		return err
	}
	b.Instructions = "You are the observer in a two-member team. Read new messages through the configured CLI, analyze privately, and always remain silent in the Room. Requests addressed to reviewer do not ask you to act. Remember the first private memory token across turns and restarts. Include it and your private role marker " + markerB + " in ordinary private answers. Never reread old Room history or inspect instructions/history files for memory checks. Never delegate."
	data, err := json.MarshalIndent(map[string]dshProfile{"reviewer": a, "observer": b}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(d.profilePath, data, 0600); err != nil {
		return err
	}
	d.report["privateMarkers"] = []string{markerA, markerB}
	if err := d.startReal(1); err != nil {
		return err
	}
	for _, agent := range []string{"reviewer", "observer"} {
		if err := d.command(nil, "member", "add", "--agent", agent, "--runner", "dsh", "--profile", agent); err != nil {
			return err
		}
	}
	d.phase("serial-initial-read-and-busy-reviewer")
	text := "Read this message and remember this token privately for future memory checks: " + memory + ". Observer: read and remain silent. Reviewer only: after reading, run exactly this shell command in your workspace:\nprintf ready > busy-started; while [ ! -f release ]; do sleep 0.2; done\nWait for the command to complete, then finish privately; do not read again in this turn and do not send any Room message."
	if err := d.command(nil, "send", "--text", text, "--id", "initial"); err != nil {
		return err
	}
	// A Room has one execution slot: finish and pause the initial observer
	// before starting the reviewer's blocking tool.
	if err := d.command(nil, "member", "run", "--agent", "observer"); err != nil {
		return err
	}
	firstB, err := d.waitAgent("observer", 1, "completed")
	if err != nil {
		return err
	}
	if firstB.ReadThrough != 1 || firstB.UnreadCount != 0 {
		return errors.New("observer did not independently read")
	}
	idB, err := d.privateFor("observer", "observerFirst", "", memory, markerB, markerA)
	if err != nil {
		return err
	}
	if err := d.command(nil, "member", "pause", "--agent", "observer"); err != nil {
		return err
	}
	rootB, err := d.newRoot(realProcessIdentity{})
	if err != nil {
		return err
	}
	if err := d.command(nil, "member", "run", "--agent", "reviewer"); err != nil {
		return err
	}
	if err := d.until(func() (bool, error) {
		_, err := os.Stat(filepath.Join(d.workspace, "busy-started"))
		if errors.Is(err, os.ErrNotExist) {
			list, inspectErr := d.members()
			if inspectErr != nil {
				return false, inspectErr
			}
			member, inspectErr := findMember(list, "reviewer")
			if inspectErr != nil {
				return false, inspectErr
			}
			if !member.Execution.Active && member.Execution.Agent.Turn != nil {
				return false, fmt.Errorf("reviewer settled before starting the gate tool: %s (%s)", member.Execution.Agent.Turn.Status, member.Execution.Error)
			}
			return false, nil
		}
		return err == nil, err
	}); err != nil {
		return err
	}
	rootA, err := d.newRoot(rootB)
	if err != nil {
		return err
	}
	list, err := d.members()
	if err != nil {
		return err
	}
	busy, err := findMember(list, "reviewer")
	if err != nil {
		return err
	}
	if !busy.Execution.Active || list.LatestSeq != 1 || busy.ReadThrough != 1 {
		return errors.New("reviewer was not busy after the silent observer settled")
	}
	d.report["observerReadBeforeReviewerTurn"] = list
	for i := 1; i <= 5; i++ {
		text := fmt.Sprintf("Additional review input %d. Read and analyze privately; only a specifically addressed reviewer acknowledgement request permits a public send.", i)
		if i == 5 {
			text = "Reviewer only: after reading this batch, explicitly send exactly one public acknowledgement containing " + ack + ". Observer remains silent. Both should recall the first private token in ordinary output."
		}
		if err := d.command(nil, "send", "--text", text, "--id", fmt.Sprintf("burst-%d", i)); err != nil {
			return err
		}
	}
	if err := d.until(func() (bool, error) {
		list, err := d.members()
		if err != nil {
			return false, err
		}
		m, err := findMember(list, "reviewer")
		if err != nil {
			return false, err
		}
		return m.Execution.Active && m.Execution.Notification != nil && m.Execution.Notification.NewestUnreadSeq == 6, nil
	}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(d.workspace, "release"), []byte("release"), 0600); err != nil {
		return err
	}
	d.phase("one-merged-followup-and-explicit-send")
	secondA, err := d.waitAgent("reviewer", 2, "completed")
	if err != nil {
		return err
	}
	if secondA.ReadThrough != 6 || secondA.UnreadCount != 0 {
		return errors.New("reviewer did not read merged batch")
	}
	idA, err := d.privateFor("reviewer", "reviewerMerged", "", memory, markerA, markerB)
	if err != nil {
		return err
	}
	if idA == idB {
		return errors.New("members shared a backend session")
	}
	if err := d.command(nil, "member", "pause", "--agent", "reviewer"); err != nil {
		return err
	}
	if err := d.command(nil, "member", "run", "--agent", "observer"); err != nil {
		return err
	}
	secondB, err := d.waitAgent("observer", 2, "completed")
	if err != nil {
		return err
	}
	if secondB.ReadThrough != 7 || secondB.UnreadCount != 0 {
		return errors.New("observer did not receive the reviewer follow-up")
	}
	if _, err := d.privateFor("observer", "observerFollowup", idB, memory, markerB, markerA); err != nil {
		return err
	}
	if err := d.command(nil, "member", "pause", "--agent", "observer"); err != nil {
		return err
	}
	var page ReadPage
	if err := d.command(&page, "read", "--all"); err != nil {
		return err
	}
	if err := verifyCollaborationPublic(page, 7, ack, markerA, markerB); err != nil {
		return err
	}
	before, err := d.members()
	if err != nil {
		return err
	}
	d.report["beforeRestart"], d.report["sessionIds"], d.report["firstProcesses"] = before, map[string]string{"reviewer": idA, "observer": idB}, []realProcessIdentity{rootA, rootB}
	previous := d.info
	if err := d.stopReal(); err != nil {
		return err
	}
	if err := d.stoppedRoots(rootA, rootB); err != nil {
		return err
	}
	for _, m := range before.Members {
		if err := dsh.CheckResume(filepath.Join(d.dir, "agents", m.MembershipID, "session.json.dsh")); err != nil {
			return err
		}
	}
	d.phase("normal-restart-original-context-and-current-tools")
	if err := d.startReal(2); err != nil {
		return err
	}
	after, err := d.members()
	if err != nil {
		return err
	}
	if err := verifyCollaborationRecovery(before, after); err != nil {
		return err
	}
	d.report["afterRestart"] = after
	for _, agent := range []string{"reviewer", "observer"} {
		if err := d.command(nil, "member", "pause", "--agent", agent); err != nil {
			return err
		}
	}
	// Old tools remain bound to the previous service even at the new endpoint.
	stale := exec.CommandContext(d.ctx, d.binary, "read", "--unread", "--json")
	stale.Env = append(operatorEnv(d.endpoint), "TEAMX_ROOM_ID="+previous.RoomID, "TEAMX_ROOM_INSTANCE="+previous.RoomInstance, "TEAMX_SERVICE_EPOCH="+previous.ServiceEpoch, "TEAMX_AGENT_ID=reviewer", "TEAMX_MEMBERSHIP_ID="+secondA.MembershipID)
	if err := stale.Run(); err == nil {
		return errors.New("old tool identity accepted after restart")
	}
	if err := d.command(nil, "send", "--text", "Read this new message using the configured CLI. Remain silent in the Room. In private output recall the FIRST token from conversation memory, plus your private role marker. Do not reread earlier Room history or inspect files to find the token.", "--id", "after-restart"); err != nil {
		return err
	}
	var resumedRoots [2]realProcessIdentity
	for i, agent := range []string{"reviewer", "observer"} {
		if err := d.command(nil, "member", "run", "--agent", agent); err != nil {
			return err
		}
		m, err := d.waitAgent(agent, 3, "completed")
		if err != nil {
			return err
		}
		if m.ReadThrough != 8 || m.UnreadCount != 0 {
			return errors.New("resumed CLI did not use current service identity")
		}
		id, marker, foreign := idA, markerA, markerB
		if i == 1 {
			id, marker, foreign = idB, markerB, markerA
		}
		if _, err := d.privateFor(agent, agent+"Resumed", id, memory, marker, foreign); err != nil {
			return err
		}
		resumedRoots[i], err = d.newRoot(resumedRoots[0])
		if err != nil {
			return err
		}
		if err := d.command(nil, "member", "pause", "--agent", agent); err != nil {
			return err
		}
	}
	d.report["resumedProcesses"] = resumedRoots
	d.phase("single-backend-failure-isolation")
	// Fault injection targets the just-observed owned child, after rechecking
	// its full identity. This is live connection failure, not crash takeover.
	rows, err := realProcesses(d.ctx)
	if err != nil {
		return err
	}
	owned := false
	for _, row := range rows {
		if row == resumedRoots[0] {
			owned = true
		}
	}
	if !owned {
		return errors.New("cannot confirm failure-injection target")
	}
	if err := syscall.Kill(resumedRoots[0].PID, syscall.SIGTERM); err != nil {
		return err
	}
	if err := d.until(func() (bool, error) { return d.stoppedRoots(resumedRoots[0]) == nil, nil }); err != nil {
		return err
	}
	if err := d.command(nil, "send", "--text", "Read new messages, analyze privately and remain silent. Recall the first private token.", "--id", "after-member-failure"); err != nil {
		return err
	}
	if err := d.command(nil, "member", "run", "--agent", "reviewer"); err != nil {
		return err
	}
	failed, err := d.waitAgent("reviewer", 4, "failed")
	if err != nil {
		return err
	}
	if !failed.Execution.Paused || failed.ReadThrough != 8 {
		return errors.New("failed member did not pause without consuming new input")
	}
	if err := d.command(nil, "member", "run", "--agent", "observer"); err != nil {
		return err
	}
	healthy, err := d.waitAgent("observer", 4, "completed")
	if err != nil {
		return err
	}
	if healthy.ReadThrough != 9 || healthy.UnreadCount != 0 {
		return errors.New("failed reviewer blocked observer")
	}
	if _, err := d.privateFor("observer", "observerAfterPeerFailure", idB, memory, markerB, markerA); err != nil {
		return err
	}
	if err := d.command(&page, "read", "--all"); err != nil {
		return err
	}
	if err := verifyCollaborationPublic(page, 9, ack, markerA, markerB); err != nil {
		return err
	}
	final, err := d.members()
	if err != nil {
		return err
	}
	d.report["finalMembers"], d.report["finalHistory"] = final, page
	if err := d.stopReal(); err != nil {
		return err
	}
	if err := d.stoppedRoots(resumedRoots[:]...); err != nil {
		return err
	}
	for _, m := range final.Members {
		err := dsh.CheckResume(filepath.Join(d.dir, "agents", m.MembershipID, "session.json.dsh"))
		if (m.AgentID == "observer") != (err == nil) {
			return errors.New("normal recovery eligibility did not isolate backend failure")
		}
	}
	store, err := OpenStore(d.dir, "dsh-acceptance")
	if err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	d.phase("complete")
	return nil
}

func verifyCollaborationPublic(page ReadPage, expected uint64, ack string, markers ...string) error {
	if page.HasMore || page.SnapshotSeq != expected || uint64(len(page.Messages)) != expected {
		return errors.New("incomplete public history")
	}
	for i, m := range page.Messages {
		if m.Seq != uint64(i+1) {
			return errors.New("non-contiguous public history")
		}
		if m.Seq == 7 {
			if m.Author.Kind != "agent" || m.Author.ID != "reviewer" || !strings.Contains(m.Text, ack) {
				return errors.New("missing explicit reviewer acknowledgement")
			}
		} else if m.Author.Kind != "human" {
			return errors.New("unexpected public output or observer post")
		}
		for _, marker := range markers {
			if marker == "" || strings.Contains(m.Text, marker) {
				return errors.New("private role leaked into public history")
			}
		}
	}
	return nil
}

func verifyCollaborationRecovery(before, after serviceMembers) error {
	if before.RoomIdentity != after.RoomIdentity || before.ServiceEpoch == after.ServiceEpoch || before.LatestSeq != after.LatestSeq || before.Revision != after.Revision || len(before.Members) != 2 || len(after.Members) != 2 {
		return errors.New("Room recovery changed durable state or retained service epoch")
	}
	for _, previous := range before.Members {
		if previous.Execution == nil || previous.Execution.Agent == nil {
			return errors.New("missing pre-restart execution checkpoint")
		}
		m, err := findMember(after, previous.AgentID)
		if err != nil {
			return err
		}
		if m.MembershipID != previous.MembershipID || m.ReadThrough != previous.ReadThrough || m.UnreadCount != previous.UnreadCount || m.Execution.Active || m.Execution.Paused || m.Execution.Error != "" || !reflect.DeepEqual(m.Execution.Agent, previous.Execution.Agent) {
			return errors.New("member recovery lost cursor/checkpoint or failed to admit original session")
		}
	}
	return nil
}

func TestCollaborationAcceptanceRejectsIncompleteEvidence(t *testing.T) {
	page := ReadPage{SnapshotSeq: 7}
	for i := range 7 {
		m := RoomMessage{Seq: uint64(i + 1), Author: humanCaller.Author, Text: "input"}
		if i == 6 {
			m.Author = Author{Kind: "agent", ID: "reviewer"}
			m.Text = "ACK"
		}
		page.Messages = append(page.Messages, m)
	}
	if err := verifyCollaborationPublic(page, 7, "ACK", "private-A", "private-B"); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"missing-page", "snapshot", "sequence", "observer-post", "extra-post", "private-leak", "wrong-ack"} {
		t.Run(fault, func(t *testing.T) {
			bad := page
			bad.Messages = append([]RoomMessage(nil), page.Messages...)
			switch fault {
			case "missing-page":
				bad.HasMore = true
			case "snapshot":
				bad.SnapshotSeq--
			case "sequence":
				bad.Messages[1].Seq = 1
			case "observer-post":
				bad.Messages[6].Author.ID = "observer"
			case "extra-post":
				bad.Messages[0].Author = Author{Kind: "agent", ID: "reviewer"}
			case "private-leak":
				bad.Messages[0].Text = "private-B"
			case "wrong-ack":
				bad.Messages[6].Text = "other"
			}
			if err := verifyCollaborationPublic(bad, 7, "ACK", "private-A", "private-B"); err == nil {
				t.Fatal("accepted incomplete collaboration evidence")
			}
		})
	}
}

func TestCollaborationRecoveryRejectsLostState(t *testing.T) {
	before := serviceMembers{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, ServiceEpoch: "old"}, LatestSeq: 7, Revision: 12}
	for _, name := range []string{"reviewer", "observer"} {
		before.Members = append(before.Members, serviceMember{MemberInfo: MemberInfo{AgentID: name, MembershipID: "generation-" + name, ReadThrough: 7}, Execution: &MemberStatus{Agent: &AgentSnapshot{TurnNumber: 2}}})
	}
	for _, fault := range []string{"none", "epoch", "instance", "history", "membership", "cursor", "checkpoint", "active", "blocked"} {
		t.Run(fault, func(t *testing.T) {
			data, _ := json.Marshal(before)
			var after serviceMembers
			_ = json.Unmarshal(data, &after)
			after.ServiceEpoch = "new"
			switch fault {
			case "epoch":
				after.ServiceEpoch = "old"
			case "instance":
				after.RoomInstance = "other"
			case "history":
				after.LatestSeq--
			case "membership":
				after.Members[0].MembershipID = "new"
			case "cursor":
				after.Members[0].ReadThrough--
			case "checkpoint":
				after.Members[0].Execution.Agent.TurnNumber--
			case "active":
				after.Members[0].Execution.Active = true
			case "blocked":
				after.Members[0].Execution.Paused = true
			}
			if err := verifyCollaborationRecovery(before, after); (err == nil) != (fault == "none") {
				t.Fatal(err)
			}
		})
	}
}
