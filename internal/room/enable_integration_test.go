//go:build integration || e2e

package room

import (
	"context"
	"testing"
	"time"
)

func TestEnableManagedBudgetSurvivesRestart(t *testing.T) {
	parallelFixtureTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m := activationTestManager(t)
	m.maxTurns = 1
	m.factory = controlledActivationFactory(nil, nil)
	room := managerCreate(t, m, "enable-recovery")
	svc := openRoomOK(t, m, room, "first-open")
	list, err := svc.members(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	member := list.Members[0]
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	managedPost(t, m, svc, "first-turn")
	waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return s.Status == "budget_exhausted" })
	// No new input remains after a successful turn; enabling only
	// extends its allowance and must retain its Session and lifetime counter.
	request := apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}
	status, err := svc.control(context.Background(), request, "enable")
	if err != nil {
		t.Fatal(err)
	}
	if status.TurnBudget.Limit != 2 || status.TurnBudget.Used != 1 || status.Agent.TurnNumber != 1 {
		t.Fatal(status)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored, err := newRoomManager(m.store.dir, 99)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	restored.config, restored.factory = m.config, m.factory
	if err := restored.restoreRuntimes(); err != nil {
		t.Fatal(err)
	}
	awaitRecovery(t, restored)
	svc = openRoomOK(t, restored, room, "reopen")
	status = inspectMember(t, context.Background(), svc.target(id.MembershipID), id)
	if status.Paused || status.TurnBudget.Limit != 2 || status.TurnBudget.Used != 1 || status.TurnBudget.Grant != 1 || status.Agent.TurnNumber != 1 {
		t.Fatal("extended budget lost on managed restart", status)
	}
	status, err = svc.control(context.Background(), request, "enable")
	if err != nil {
		t.Fatal(err)
	}
	if status.Paused || status.TurnBudget.Limit != 2 || status.TurnBudget.Used != 1 {
		t.Fatal("normal enable granted extra turns", status)
	}
}
