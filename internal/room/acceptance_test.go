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

	"github.com/go-aie/actorx"
)

func TestMemberAgentBudgetPausesWithoutLosingPendingInput(t *testing.T) {
	for _, cancelTurn := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancellation"}[cancelTurn], func(t *testing.T) {
			b := newControlledBackend()
			h, addr, id := memberHarness(t, b, nil, func(m *MemberAgent) { m.MaxTurns = 1 })
			input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
			controlMember(t, h, addr, id, "run", input, false, false)
			awaitTurn(t, h, b)
			appendMemberInput(t, h, 2, 8)
			input.Revision, input.LatestSeq, input.NewestUnreadSeq, input.UnreadCount = 8, 8, 8, 8
			if err := addr.Send(h.ctx, input); err != nil {
				t.Fatal(err)
			}
			if cancelTurn {
				controlMember(t, h, addr, id, "pause", input, false, true)
			} else {
				b.finish <- errors.New("failure consumes one attempt")
			}
			settled := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active })
			if settled.Status != "budget_exhausted" || !settled.Paused || settled.TurnBudget.Limit != 1 || settled.TurnBudget.Used != 1 ||
				settled.Notification.NewestUnreadSeq != 8 || settled.Agent.LastOfferedUnreadSeq != 1 {
				t.Fatal(settled)
			}
			for _, retry := range []bool{false, true} {
				if _, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "run", State: input, Retry: retry}); !errors.Is(err, ErrTurnBudget) {
					t.Fatal("run renewed exhausted budget", err)
				}
			}
			if err := addr.Send(h.ctx, input); err != nil {
				t.Fatal(err)
			}
			if status := inspectMember(t, h.ctx, addr, id); status.Active || status.Agent.TurnNumber != 1 || b.opens.Load() != 1 {
				t.Fatal(status, b.opens.Load())
			}
		})
	}
}

func TestMemberAgentFailedTaskAdmissionDoesNotConsumeBudget(t *testing.T) {
	h := newRoomHarness(t, historyRoom(8, "body"), nil, actorx.WithTaskLimit(1))
	id := h.memberID("A", "generation-A")
	b := newControlledBackend()
	addr, err := h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: h.store, Room: h.addr, Backend: b, MaxTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	input := UnreadChanged{MemberIdentity: id, Revision: 8, LatestSeq: 8, NewestUnreadSeq: 8, UnreadCount: 8}
	controlMember(t, h, addr, id, "run", input, false, false)
	status := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Error != "" })
	// The member's maintenance task occupies its sole worker slot.
	if status.Active || status.TurnBudget.Used != 0 || status.Error == "" || b.opens.Load() != 0 {
		t.Fatal(status, b.opens.Load())
	}
}

func TestRoomHistoryByteCapacityPreservesReadableHistory(t *testing.T) {
	state := historyRoom(maxHistoryBytes/maxTextBytes, strings.Repeat("x", maxTextBytes))
	r := &Room{state: state}
	post := PostMessage{RoomIdentity: state.RoomIdentity, Caller: humanCaller, ClientMessageID: "exceeds-by-one", Text: "x"}
	if err := r.Post(nil, post, actorx.Reply[PostReceipt]{}); !errors.Is(err, ErrCapacity) || r.committing {
		t.Fatal("byte capacity did not reject before committing", err)
	}
	page, err := r.Read(nil, ReadMessages{RoomIdentity: state.RoomIdentity, Caller: humanCaller, Mode: ReadAll})
	if err != nil || page.SnapshotSeq != state.LatestSeq || len(page.Messages) != maxPageBytes/maxTextBytes || !page.HasMore ||
		!reflect.DeepEqual(r.state, state) {
		t.Fatal("capacity rejection changed history or preview", page, err)
	}
}

func TestServeRejectsUnboundedTurnBudget(t *testing.T) {
	for _, limit := range []string{"0", "-1"} {
		var stdout, stderr bytes.Buffer
		if err := serve([]string{"--config", writeStartupConfig(t, t.TempDir(), ""), "--max-turns", limit}, &stdout, &stderr); err == nil || stdout.Len() != 0 {
			t.Fatal("invalid turn limit accepted", limit, err)
		}
	}
}

func TestRecoveryRejectsLegacyRunnersWithoutChangingData(t *testing.T) {
	for _, kind := range []string{"manual", "scripted"} {
		t.Run(kind, func(t *testing.T) {
			dir, id := recoveryFixture(t)
			path := filepath.Join(dir, "room.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data, []byte(`"kind":"dsh"`), []byte(`"kind":"`+kind+`"`), 1)
			if kind == "scripted" {
				data = bytes.Replace(data, []byte(`"profile":"fixture-fail,ignore"`), []byte(`"script":"fail,ignore"`), 1)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if svc, err := newService(dir, id.RoomID, recoveryConfig()); err == nil {
				closeService(t, svc)
				t.Fatal("legacy runner restored")
			} else if !strings.Contains(err.Error(), "unsupported runner") {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("legacy data changed", err)
			}
		})
	}
}

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
