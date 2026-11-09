//go:build darwin || linux

package room

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func (d *realRoomDriver) rotationAcceptance() error {
	if err := d.setup(); err != nil {
		return err
	}
	if err := d.startReal(1); err != nil {
		return err
	}
	if err := d.command(nil, "member", "add", "--agent", "reviewer", "--runner", "dsh", "--profile", "reviewer"); err != nil {
		return err
	}
	// This shared token can be recovered only through history after rotation.
	token, err := newOpaqueID()
	if err != nil {
		return err
	}
	if err := d.command(nil, "send", "--text", "Read this message and finish privately. Shared history token: "+token+". Do not send to the Room.", "--id", "history"); err != nil {
		return err
	}
	if err := d.command(nil, "member", "run", "--agent", "reviewer"); err != nil {
		return err
	}
	if _, err := d.waitAgent("reviewer", 1, "completed"); err != nil {
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
	if m.ReadThrough != 1 || m.UnreadCount != 0 {
		return errors.New("first turn did not consume input")
	}
	d.memberDir = filepath.Join(d.dir, "agents", m.MembershipID)
	// Seed one real local business record through the operator API. Rotation
	// must preserve it byte-for-byte, independently of the model's private state.
	policyReq := apiRequest{ServiceInfo: d.info, Caller: humanCaller, AgentID: m.AgentID, MembershipID: m.MembershipID, Policy: &PolicyChange{ID: "s1-retained-rule", Status: "active", Text: "Keep acceptance replies concise.", SourceText: "Keep acceptance replies concise."}}
	client := cliClient{endpoint: d.endpoint, http: &http.Client{Timeout: 10 * time.Second}}
	var policyView PolicyView
	if err := client.request(d.ctx, "/v1/policy/update", &policyReq, &policyView); err != nil {
		return err
	}
	businessPath := filepath.Join(d.memberDir, "policy.json")
	businessBefore, err := os.ReadFile(businessPath)
	if err != nil {
		return err
	}

	var old struct {
		SessionID string `json:"sessionId"`
	}
	if err := jsonFile(filepath.Join(d.memberDir, "session.json.dsh", "backend.json"), &old); err != nil {
		return err
	}
	root, err := d.newRoot(realProcessIdentity{})
	if err != nil {
		return err
	}
	d.report["originalSession"], d.report["originalRoot"] = old.SessionID, root
	var before RoomSnapshot
	if err := jsonFile(filepath.Join(d.dir, "room.json"), &before); err != nil {
		return err
	}
	var rotated MemberStatus
	if rotated, err = d.rotateReal(false); err != nil {
		return err
	}
	if rotated.Rotation == nil || rotated.Rotation.Status != "completed" || !rotated.Paused || rotated.Active || rotated.SessionTurns.Total != 1 {
		return errors.New("invalid settled rotation")
	}
	var after RoomSnapshot
	if err := jsonFile(filepath.Join(d.dir, "room.json"), &after); err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return errors.New("rotation changed history or cursor")
	}
	if err := d.stoppedRoots(root); err != nil {
		return err
	}
	var ref dshReference
	if err := jsonFile(filepath.Join(d.memberDir, "session.json"), &ref); err != nil {
		return err
	}
	if !ref.Pending {
		return errors.New("fresh session was not lazy")
	}
	nextDir := filepath.Join(d.memberDir, ref.Directory)
	if _, err := os.Stat(nextDir); !errors.Is(err, os.ErrNotExist) {
		return errors.New("new backend launched during lazy rotation")
	}

	if rotated.SessionGeneration != ref.Directory || rotated.SessionStartUsed != 1 || rotated.Rotation.Phase != "completed" {
		return errors.New("rotation baseline not committed")
	}
	businessAfter, err := os.ReadFile(businessPath)
	if err != nil || !bytes.Equal(businessBefore, businessAfter) {
		return errors.New("rotation changed business record")
	}
	var repeated MemberStatus
	if err := d.command(&repeated, "member", "session", "rotate", "--agent", "reviewer", "--operation-id", rotated.Rotation.OperationID, "--from-generation", rotated.Rotation.FromGeneration); err != nil {
		return err
	}
	if repeated.Rotation.ToGeneration != ref.Directory || repeated.SessionTurns.Total != 1 {
		return errors.New("duplicate operation rotated twice")
	}
	d.report["businessRecordPreserved"], d.report["duplicateOperationIdempotent"] = true, true
	d.report["rotation"], d.report["preservedRoom"], d.report["newDirectory"] = rotated, after, ref.Directory
	d.phase("history-context-after-rotation")
	text := "Read this new message, then use the TeamX CLI read --all --json to recover the shared history token from the first Room message. Include that token in your ordinary private answer. Do not send to the Room."
	if err := d.command(nil, "send", "--text", text, "--id", "recover-history"); err != nil {
		return err
	}
	if err := d.command(nil, "member", "run", "--agent", "reviewer"); err != nil {
		return err
	}
	if _, err := d.waitAgent("reviewer", 2, "completed"); err != nil {
		return err
	}
	var fresh struct {
		SessionID string `json:"sessionId"`
	}
	if err := jsonFile(filepath.Join(nextDir, "backend.json"), &fresh); err != nil {
		return err
	}
	if fresh.SessionID == "" || fresh.SessionID == old.SessionID {
		return errors.New("rotation did not create distinct SessionID")
	}
	var private struct {
		Result dsh.TurnResult `json:"result"`
		Error  string         `json:"error"`
	}
	if err := jsonFile(filepath.Join(d.memberDir, "dsh-turn.json"), &private); err != nil {
		return err
	}
	if private.Error != "" || private.Result.StopReason != "end_turn" || !strings.Contains(private.Result.Text, token) || len(private.Result.Tools) < 2 {
		return errors.New("new session lacks CLI history/context evidence")
	}
	newRoot, err := d.newRoot(realProcessIdentity{})
	if err != nil {
		return err
	}
	d.report["newSession"], d.report["newRoot"], d.report["contextRecovery"] = fresh.SessionID, newRoot, private
	// A real running shell must settle before cancel-and-rotate can complete.
	d.phase("cancel-running-tool-and-rotate")
	text = "Read this message, then run exactly this shell command and wait for it to finish: printf ready > rotation-busy; sleep 120. Do not send to the Room."
	if err := d.command(nil, "send", "--text", text, "--id", "cancel"); err != nil {
		return err
	}
	if err := d.until(func() (bool, error) {
		_, err := os.Stat(filepath.Join(d.workspace, "rotation-busy"))
		if errors.Is(err, os.ErrNotExist) {
			list, e := d.members()
			if e != nil {
				return false, e
			}
			m, e := findMember(list, "reviewer")
			if e != nil {
				return false, e
			}
			if !m.Execution.Active && m.Execution.Agent.TurnNumber >= 3 {
				return false, fmt.Errorf("tool did not start: %s", m.Execution.Error)
			}
			return false, nil
		}
		return err == nil, err
	}); err != nil {
		return err
	}
	// Snapshot descendants while the tool is still alive, for exit evidence.
	rows, err := realProcesses(d.ctx)
	if err != nil {
		return err
	}
	owned := []realProcessIdentity{newRoot}
	for changed := true; changed; {
		changed = false
		for _, r := range rows {
			seen, parent := false, false
			for _, o := range owned {
				seen = seen || o.PID == r.PID
				parent = parent || o.PID == r.Parent
			}
			if !seen && parent {
				owned = append(owned, r)
				changed = true
			}
		}
	}
	if len(owned) < 2 {
		return errors.New("no owned running tool observed")
	}
	if rotated, err = d.rotateReal(true); err != nil {
		return err
	}
	if rotated.Active || !rotated.Paused || rotated.Agent.TurnNumber != 3 || rotated.Agent.Turn.Status != "interrupted" || rotated.SessionTurns.Total != 3 {
		return errors.New("cancel rotation lost turn settlement")
	}
	if err := d.stoppedRoots(owned...); err != nil {
		return err
	}
	d.report["cancelRotation"], d.report["settledProcesses"] = rotated, owned
	if err := d.command(nil, "send", "--text", "Read new unread messages through the configured CLI and finish privately. Do not send.", "--id", "after-cancel"); err != nil {
		return err
	}
	if err := d.command(nil, "member", "run", "--agent", "reviewer"); err != nil {
		return err
	}
	final, err := d.waitAgent("reviewer", 4, "completed")
	if err != nil {
		return err
	}
	if final.ReadThrough != 4 || final.UnreadCount != 0 {
		return errors.New("new input failed after cancel rotation")
	}
	if err := jsonFile(filepath.Join(d.memberDir, "session.json"), &ref); err != nil {
		return err
	}
	lastDir := filepath.Join(d.memberDir, ref.Directory)
	var last struct {
		SessionID string `json:"sessionId"`
	}
	if err := jsonFile(filepath.Join(lastDir, "backend.json"), &last); err != nil {
		return err
	}
	if last.SessionID == fresh.SessionID || last.SessionID == old.SessionID || last.SessionID == "" {
		return errors.New("cancel rotation reused retired session")
	}
	lastRoot, err := d.newRoot(realProcessIdentity{})
	if err != nil {
		return err
	}
	var page ReadPage
	if err := d.command(&page, "read", "--all"); err != nil {
		return err
	}
	if page.HasMore || len(page.Messages) != 4 {
		return errors.New("incomplete final history")
	}
	for i, msg := range page.Messages {
		if msg.Seq != uint64(i+1) || msg.Author.Kind != "human" {
			return errors.New("ordinary output leaked into Room")
		}
	}
	d.report["finalMembers"], d.report["finalHistory"], d.report["finalSession"] = final, page, last.SessionID
	if err := d.stopReal(); err != nil {
		return err
	}
	if err := d.stoppedRoots(lastRoot); err != nil {
		return err
	}
	if err := dsh.CheckResume(lastDir); err != nil {
		return err
	}
	store, err := OpenStore(d.dir, "dsh-acceptance")
	if err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}

	// A normal restart preserves both cumulative admissions and the new baseline.
	if err := d.startReal(2); err != nil {
		return err
	}
	restored, err := d.members()
	if err != nil {
		return err
	}
	restoredMember, err := findMember(restored, "reviewer")
	if err != nil {
		return err
	}
	if restoredMember.Execution.SessionTurns.Total != 4 || restoredMember.Execution.SessionStartUsed != 3 || restoredMember.Execution.SessionGeneration != ref.Directory {
		return errors.New("normal restart lost cumulative count or baseline")
	}
	if err := d.command(nil, "send", "--text", "Read this new message through the configured CLI and finish privately. Do not send.", "--id", "after-restart"); err != nil {
		return err
	}
	continued, err := d.waitAgent("reviewer", 5, "completed")
	if err != nil {
		return err
	}
	if continued.Execution.SessionTurns.Total != 5 || continued.Execution.SessionStartUsed != 3 {
		return errors.New("normal restart reset counters")
	}
	var resumedBackend struct {
		SessionID string `json:"sessionId"`
	}
	if err := jsonFile(filepath.Join(lastDir, "backend.json"), &resumedBackend); err != nil {
		return err
	}
	if resumedBackend.SessionID != last.SessionID {
		return errors.New("normal restart replaced original Session")
	}
	d.report["normalRestartPreservesCounts"], d.report["normalRestartSessionID"] = true, resumedBackend.SessionID
	if err := d.stopReal(); err != nil {
		return err
	}
	d.report["normalResumeEligible"] = true
	d.phase("complete")
	return nil
}

// A command deadline bounds its wait, not the retained rotation task. Observe
// the original operation after incomplete/unknown; never submit a second one.
func (d *realRoomDriver) rotateReal(cancelTurn bool) (MemberStatus, error) {
	list, err := d.members()
	if err != nil {
		return MemberStatus{}, err
	}
	member, err := findMember(list, "reviewer")
	if err != nil {
		return MemberStatus{}, err
	}
	operationID, err := newOpaqueID()
	if err != nil {
		return MemberStatus{}, err
	}
	args := []string{"member", "session", "rotate", "--agent", "reviewer", "--operation-id", operationID, "--from-generation", member.Execution.SessionGeneration}
	if cancelTurn {
		args = append(args, "--cancel")
	}

	var status MemberStatus
	err = d.command(&status, args...)
	if err == nil {
		return status, nil
	}
	// Public diagnostics intentionally hide internal errors. Even a generic
	// command failure can be a completed or still-owned operation: query its
	// exact ID before interpreting the result, without submitting another rotate.
	commandErr := err
	d.report["rotationCommandOutcomeReconciled"] = true
	deadline := time.Now().Add(30 * time.Second)
	err = d.until(func() (bool, error) {
		list, err := d.members()
		if err != nil {
			return false, err
		}
		member, err := findMember(list, "reviewer")
		if err != nil {
			return false, err
		}
		status = *member.Execution
		if status.Rotation == nil || status.Rotation.OperationID != operationID {
			return false, commandErr
		}
		switch status.Rotation.Status {
		case "completed":
			return true, nil
		case "failed":
			return false, fmt.Errorf("rotation failed: %s", status.Rotation.Error)
		}
		if time.Now().After(deadline) {
			return false, errors.New("rotation remained incomplete after observation budget")
		}
		return false, nil
	})
	return status, err
}
