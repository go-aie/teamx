//go:build integration || e2e

package room

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestManagedTeamSchedulingSurvivesWorkspaceAndDiscussionRestart(t *testing.T) {
	m := activationTestManager(t)
	team := m.teams["takeout-dinner"]
	team.Scheduling = schedulingParallel
	m.teams[team.ID] = team
	m.factory = controlledActivationFactory(nil, nil)
	room := managerCreate(t, m, "parallel")
	s := openRoomOK(t, m, room, "initial")
	// Changing the source while applying a new workspace cannot change the mode.
	team.Scheduling = schedulingSerial
	m.teams[team.ID] = team
	applyWorkspace(t, m, workspaceRequest(t, m, room, workspaceConfig{Root: "outputs"}))
	op := awaitWorkspace(t, m)
	if op.Status != "completed" || op.PreviousConfig == nil || op.PreparedConfig == nil || op.PreviousConfig.Scheduling != schedulingParallel || op.PreparedConfig.Scheduling != schedulingParallel {
		t.Fatal(op)
	}
	saved, _ := readManagedRoom(t, m, room)
	if saved.Scheduling != schedulingParallel || saved.Workspace.Root != "outputs" {
		t.Fatal(saved)
	}
	m.mu.Lock()
	s = m.runtimes[room.RoomIdentity].service
	m.mu.Unlock()
	req := apiRequest{ServiceInfo: s.info, Caller: humanCaller, RestartID: strings.Repeat("c", 32)}
	if code := discussionHTTP(t, s, "/v1/discussion/restart", req, nil); code != 200 {
		t.Fatal(code)
	}
	if result := awaitDiscussion(t, s); result.Status != "completed" {
		t.Fatal(result)
	}
	saved, _ = readManagedRoom(t, m, room)
	if saved.Scheduling != schedulingParallel {
		t.Fatal("discussion restart changed scheduling")
	}
	m.teams = nil
	config, err := m.activationConfig(room.RoomIdentity)
	if err != nil || config.scheduling != schedulingParallel {
		t.Fatal(config.scheduling, err)
	}
	// Close and reassemble the service exclusively from the persisted config.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.close(ctx); err != nil {
		t.Fatal(err)
	}
	restored, err := m.factory(m.roomDir(room.RoomID), room.RoomID, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, restored)
	if restored.info.RoomIdentity != room.RoomIdentity {
		t.Fatal("restored wrong Room")
	}
}

func TestManagedTeamParallelWorkspaceJoinsAllSessions(t *testing.T) {
	testWorkspaceApplyJoinsRunningSessions(t, schedulingParallel)
}

func TestManagedTeamParallelDiscussionRestartJoinsAllSessions(t *testing.T) {
	m := activationTestManager(t)
	team := m.teams["takeout-dinner"]
	team.Scheduling = schedulingParallel
	m.teams[team.ID] = team
	entered := make(chan MemberIdentity, len(team.Members))
	cleaning := make(chan MemberIdentity, len(team.Members))
	settled := make(map[string]chan struct{})
	room := managerCreate(t, m, "parallel-restart")
	saved, _ := readManagedRoom(t, m, room)
	for _, member := range saved.Members {
		settled[member.MembershipID] = make(chan struct{})
	}
	defer func() {
		for _, gate := range settled {
			onceRelease(gate)()
		}
	}()
	m.factory = controlledActivationFactory(func(id MemberIdentity, ctx context.Context) error {
		entered <- id
		<-ctx.Done()
		return ctx.Err()
	}, func(id MemberIdentity) { cleaning <- id; <-settled[id.MembershipID] })
	s := openRoomOK(t, m, room, "initial")
	managedPost(t, m, s, "all members start")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range saved.Members {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("members did not overlap", ctx.Err())
		}
	}
	req := apiRequest{ServiceInfo: s.info, Caller: humanCaller, RestartID: strings.Repeat("d", 32)}
	if code := discussionHTTP(t, s, "/v1/discussion/restart", req, nil); code != 200 {
		t.Fatal(code)
	}
	var last MemberIdentity
	for i := range saved.Members {
		select {
		case last = <-cleaning:
		case <-ctx.Done():
			t.Fatal("not all sessions were canceled", ctx.Err())
		}
		if i < len(saved.Members)-1 {
			close(settled[last.MembershipID])
		}
	}
	state, err := s.store.LoadRoom()
	if err != nil || state.LatestSeq != 1 || state.ClearedThrough != 0 {
		t.Fatal("history cleared before final Session settled", state, err)
	}
	close(settled[last.MembershipID])
	if result := awaitDiscussion(t, s); result.Status != "completed" || result.ClearedThrough != 1 {
		t.Fatal(result)
	}
}
