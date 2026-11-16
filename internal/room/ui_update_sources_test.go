package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func sourceTopics(t *testing.T, sub *uiUpdateSubscription, want ...string) []uiUpdate {
	t.Helper()
	var got []uiUpdate
	missing := make(map[string]bool)
	for _, topic := range want {
		missing[topic] = true
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		updates, resync := sub.take()
		if resync {
			t.Fatal("unexpected source overflow")
		}
		for _, u := range updates {
			got = append(got, u)
			delete(missing, u.Topic)
		}
		if len(missing) == 0 {
			return got
		}
		select {
		case <-sub.wake:
		case <-timer.C:
			t.Fatalf("missing topics %v; received %v", missing, got)
		}
	}
}

func assertSourceQuiet(t *testing.T, sub *uiUpdateSubscription) {
	t.Helper()
	if got, resync := sub.take(); resync || len(got) != 0 {
		t.Fatalf("observation generated updates: %v, resync=%v", got, resync)
	}
}

func observeStoreSources(store *Store, svc *service) {
	store.observe = svc.invalidate
	store.observeExpiry = func(topic string, id MemberIdentity, at time.Time) {
		u := roomUIUpdate(topic, svc.info)
		u.AgentID, u.MembershipID = id.AgentID, id.MembershipID
		svc.updates.at(u, at)
	}
}

func TestUIUpdateSourcesSharedReadingAcrossHTTPClients(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			svc, _, a, server, cookie, _ := updateHTTPService(t, managed)
			sub := updateSub(t, svc.updates, 6, a.operator, svc.info)
			otherOperator := updateSub(t, svc.updates, 7, "other", svc.info)
			_, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: svc.info.RoomIdentity,
				Caller: Caller{Author: Author{Kind: "human", ID: "peer"}}, ClientMessageID: "peer-message", Text: "new message"})
			if err != nil {
				t.Fatal(err)
			}
			updates := sourceTopics(t, sub, "messages", "members", "human_read", "catalog")
			for _, u := range updates {
				if u.Topic == "models" || u.Topic == "workspace" || u.Topic == "platforms" {
					t.Fatal("message invalidated unrelated config", u)
				}
			}
			otherOperator.take()
			seq := uint64(1)
			resp := updateRequest(t, server.URL, "POST", "/v1/human-read/confirm", cookie,
				humanReadRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "human", ID: a.operator}}, SnapshotSeq: &seq, OpenedThrough: &seq})
			var state humanReadState
			if err := json.NewDecoder(resp.Body).Decode(&state); err != nil || resp.StatusCode != 200 || state.OpenedThrough != 1 {
				t.Fatal(state, err, resp.Status)
			}
			resp.Body.Close()
			for _, u := range sourceTopics(t, sub, "human_read", "catalog") {
				if u.Operator != a.operator {
					t.Fatal("missing operator routing", u)
				}
			}
			assertSourceQuiet(t, otherOperator)
			// A second client reads the installed receipt. Ordinary reads and duplicate
			// confirmations must not create a read -> invalidate feedback loop.
			for _, path := range []string{"/v1/human-read/state", "/v1/human-read/confirm"} {
				req := humanReadRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "human", ID: a.operator}}}
				if path == "/v1/human-read/confirm" {
					req.SnapshotSeq, req.OpenedThrough = &seq, &seq
				}
				r := updateRequest(t, server.URL, "POST", path, cookie, req)
				if err := json.NewDecoder(r.Body).Decode(&state); err != nil || state.OpenedThrough != 1 {
					t.Fatal(state, err)
				}
				r.Body.Close()
			}
			assertSourceQuiet(t, sub)
		})
	}
}

func TestUIUpdateSourcesPolicyInstallAgentAndExpiry(t *testing.T) {
	h, id, svc := policyTestService(t)
	svc.updates = newUIUpdateHub()
	observeStoreSources(h.store, svc)
	sub := updateSub(t, svc.updates, 1, humanCaller.ID, svc.info)
	other := id
	other.MembershipID = "other-membership"
	first := policyRule()
	expires := time.Now().Add(250 * time.Millisecond)
	first.ExpiresAt = &expires
	if _, err := h.store.changePolicy(id, humanCaller, first); err != nil {
		t.Fatal(err)
	}
	for _, u := range sourceTopics(t, sub, "policy") {
		if u.AgentID != id.AgentID || u.MembershipID != id.MembershipID {
			t.Fatal(u)
		}
	}
	view, err := h.store.readPolicy(id)
	if err != nil || len(view.Effective) != 1 {
		t.Fatal(view, err)
	}
	assertSourceQuiet(t, sub)
	sourceTopics(t, sub, "policy")
	view, err = h.store.readPolicy(id)
	if err != nil || len(view.Effective) != 0 || view.Revision != 1 {
		t.Fatal("expiry changed persistence or missed projection", view, err)
	}
	// Agent/CLI/MCP uses the conversation save boundary, independently of handlers.
	source := roomCall[PostReceipt](t, h, h.post(humanCaller, "lasting-rule", "记住：以后回复简短中文"))
	_, err = h.store.changeAgentPolicy(id, agentCallerIdentity(id), agentPolicyChange{Type: "save", ID: "style", SourceSeq: source.Seq, TurnNumber: 1,
		Basis: "用户明确长期要求", Body: &agentPolicyBody{Text: "简短中文"}})
	if err != nil {
		t.Fatal(err)
	}
	sourceTopics(t, sub, "policy")
	if _, err := h.store.readPolicy(other); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	assertSourceQuiet(t, sub)
}

func TestUIUpdateSourcesInboxInstallConsumeReclaimAndQuietReads(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(fmt.Sprint(observed), func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			initReferenceTest(t, h)
			svc := &service{info: ServiceInfo{RoomIdentity: h.id.RoomIdentity, ServiceEpoch: "inbox-runtime"}, updates: newUIUpdateHub()}
			sub := updateSub(t, svc.updates, 1, humanCaller.ID, svc.info)
			if observed {
				observeStoreSources(h.store, svc)
			}
			receiveReference(t, h, "one", referenceTestSource("one"))
			check := func() {
				t.Helper()
				if observed {
					for _, u := range sourceTopics(t, sub, "inbox", "members") {
						if u.Topic == "catalog" || u.AgentID != h.id.AgentID || u.MembershipID != h.id.MembershipID {
							t.Fatal(u)
						}
					}
				} else {
					assertSourceQuiet(t, sub)
				}
			}
			check()
			inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
			assertSourceQuiet(t, sub)
			inboxRunning(t, h, 1, 1)
			page := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
			check()
			linkInboxPage(t, h, page)
			if status := confirmInboxPage(t, h, page); status.ConsumedThrough != 1 {
				t.Fatal(status)
			}
			check()
			confirmInboxPage(t, h, page)
			assertSourceQuiet(t, sub)
			settleReclaimTurn(t, h)
			status := inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 1})
			if status.LatestSeq != 1 || status.ConsumedThrough != 1 || status.ExpiredThrough != 1 || status.RetainedEntries != 0 {
				t.Fatal(status)
			}
			check()
			inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 1})
			inboxCall[InboxStatus](t, h, InspectInbox{h.id})
			assertSourceQuiet(t, sub)
		})
	}
}

func TestUIUpdateSourcesInboxUnknownCommitPreservesFailure(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(fmt.Sprint(observed), func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			svc := &service{info: ServiceInfo{RoomIdentity: h.id.RoomIdentity, ServiceEpoch: "inbox-error"}, updates: newUIUpdateHub()}
			sub := updateSub(t, svc.updates, 1, humanCaller.ID, svc.info)
			if observed {
				observeStoreSources(h.store, svc)
			}
			// Entry replacement happened, then directory durability became unknown.
			h.store.ops.syncDir = func(string) error { return errors.New("directory sync unavailable") }
			inboxError[InboxReceipt](t, h, h.receive("uncertain"), ErrCommitUnknown)
			status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
			if status.WriteError == "" || status.LatestSeq != 0 {
				t.Fatal("unknown write was reported as installed", status)
			}
			if observed {
				sourceTopics(t, sub, "inbox", "members")
			} else {
				assertSourceQuiet(t, sub)
			}
		})
	}
}

func TestUIUpdateSourcesMemberTransitionsAndObserverParity(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(fmt.Sprint(observed), func(t *testing.T) {
			b := newControlledBackend()
			hub := newUIUpdateHub()
			var svc *service
			h, addr, id := memberHarness(t, b, nil, func(m *MemberAgent) {
				svc = &service{info: ServiceInfo{RoomIdentity: m.Options.RoomIdentity, ServiceEpoch: "member-runtime"}, updates: hub}
				if observed {
					m.Observe = func() { svc.invalidate("members", m.Options.MemberIdentity) }
				}
			})
			sub := updateSub(t, hub, 1, humanCaller.ID, svc.info)
			check := func() {
				t.Helper()
				if observed {
					for _, u := range sourceTopics(t, sub, "members") {
						if u.Topic == "catalog" {
							t.Fatal("execution state is not a directory field", u)
						}
					}
				} else {
					assertSourceQuiet(t, sub)
				}
			}
			input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
			controlMember(t, h, addr, id, "run", input, false, false)
			awaitTurn(t, h, b)
			if status := inspectMember(t, h.ctx, addr, id); !status.Active {
				t.Fatal(status)
			}
			check()
			controlMember(t, h, addr, id, "pause", input, false, true)
			waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Paused })
			check()
			controlMember(t, h, addr, id, "run", input, true, false)
			awaitTurn(t, h, b)
			check()
			b.finish <- errors.New("controlled failure")
			failed := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Error != "" })
			if failed.Agent.Turn.Status != "failed" {
				t.Fatal(failed)
			}
			check()
			controlMember(t, h, addr, id, "run", input, true, false)
			awaitTurn(t, h, b)
			check()
			b.finish <- nil
			waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 3 })
			check()
			// Existing maintenance and Inspect are silent once status is stable.
			time.Sleep(75 * time.Millisecond)
			inspectMember(t, h.ctx, addr, id)
			assertSourceQuiet(t, sub)
		})
	}
}

func TestUIUpdateSourcesMemberWaiting(t *testing.T) {
	state := historyRoom(1, "body")
	state.Members = append(state.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: state.Members[0].Runner})
	h := newRoomHarness(t, state, nil, actorx.WithTaskLimit(3))
	svc := &service{info: ServiceInfo{RoomIdentity: h.id, ServiceEpoch: "waiting-runtime"}, updates: newUIUpdateHub()}
	sub := updateSub(t, svc.updates, 1, humanCaller.ID, svc.info)
	var targets []actorx.Addr
	var ids []MemberIdentity
	for _, member := range state.Members {
		id := h.memberID(member.AgentID, member.MembershipID)
		backend := newControlledBackend()
		addr, err := h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: h.store, Room: h.addr, Backend: backend,
			Observe: func() { svc.invalidate("members", id) }})
		if err != nil {
			t.Fatal(err)
		}
		targets, ids = append(targets, addr), append(ids, id)
		roomCall[MemberInfo](t, h, AttachMember{MemberIdentity: id, Target: addr})
		runSerialMember(t, h, addr, id)
		if len(targets) == 1 {
			awaitTurn(t, h, backend)
		}
	}
	waitMember(t, h.ctx, targets[1], ids[1], func(s MemberStatus) bool { return s.Status == "waiting" })
	found := false
	for _, u := range sourceTopics(t, sub, "members") {
		if u.AgentID == ids[1].AgentID && u.MembershipID == ids[1].MembershipID {
			found = true
		}
	}
	if !found {
		t.Fatal("waiting identity not notified")
	}
}

func TestUIUpdateSourcesModelProgressAndWorkspaceProgress(t *testing.T) {
	m, control, _ := onlineModelManager(t)
	global := updateSub(t, m.updates, 1, humanOperator(), ServiceInfo{})
	blocked, release := make(chan struct{}), make(chan struct{})
	original := control.service.probe
	control.service.probe = func(ctx context.Context, o dsh.Options) (dsh.ProbeResult, error) {
		close(blocked)
		select {
		case <-release:
			return original(ctx, o)
		case <-ctx.Done():
			return dsh.ProbeResult{}, ctx.Err()
		}
	}
	id, _ := newOpaqueID()
	if _, err := control.submit(modelChangeInput{RequestID: id, Kind: "save", Entry: modelTestEntry("D"), APIKey: "fixture-key", ExpectedVersion: control.service.snapshot().Version}, humanOperator()); err != nil {
		t.Fatal(err)
	}
	<-blocked
	sourceTopics(t, global, "models")
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		running := control.running[id]
		status := control.operations[id].Status
		m.mu.Unlock()
		if !running {
			if status != "completed" {
				t.Fatal(status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("model did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	sourceTopics(t, global, "models")
	// Workspace's durable writer carries all intermediate and failed phases.
	m2, svc, _, _ := connectFixture(t)
	sub := updateSub(t, svc.updates, 1, humanCaller.ID, svc.info)
	room := roomSummary{RoomIdentity: svc.info.RoomIdentity}
	op := testWorkspaceOperation(t, m2, room, workspaceConfig{Root: "next"})
	for _, phase := range []string{"waiting", "preparing", "failed"} {
		if phase == "failed" {
			op.Status, op.ErrorCode = "failed", "preparation_failed"
		} else {
			op.Phase = phase
		}
		if err := m2.saveWorkspaceOperation(op); err != nil {
			t.Fatal(err)
		}
		sourceTopics(t, sub, "workspace")
	}
}

func TestUIUpdateSourcesInteractionAndAuthorizationProgress(t *testing.T) {
	m, svc, _, req := connectFixture(t)
	sub := updateSub(t, svc.updates, 1, humanCaller.ID, svc.info)
	global := updateSub(t, m.updates, 2, humanCaller.ID, ServiceInfo{})
	other := updateSub(t, m.updates, 3, "other", ServiceInfo{})
	op, session := connectPending(t, m, svc, req)
	sourceTopics(t, sub, "interactions")
	for _, qr := range []string{"first-private-code", "replacement-private-code"} {
		m.mu.Lock()
		session.Status, session.URL, session.QRCode = "waiting", "https://example.com/private", qr
		m.checkpointPlatformConnectLocked(session)
		m.mu.Unlock()
		sourceTopics(t, sub, "interactions")
		for _, u := range sourceTopics(t, global, "authorization") {
			if u.Operator != humanCaller.ID {
				t.Fatal(u)
			}
		}
		assertSourceQuiet(t, other)
	}
	connectSucceed(t, m, session)
	sourceTopics(t, sub, "interactions")
	result := interactionList(t, m, interactionRequest(svc, op))
	if len(result) != 1 || result[0].Status != "completed" {
		t.Fatal(result)
	}
	// Status projections do not start a replacement operation.
	before, err := m.readPlatformConnectLocked(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	interactionList(t, m, interactionRequest(svc, op))
	after, err := m.readPlatformConnectLocked(op.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("read changed the operation", err)
	}
}

func TestUIUpdateSourcesSettingsPlatformsBindingsAndNotificationWorker(t *testing.T) {
	m, _, svc, connection, members := cliToolFixture(t, "exit 0")
	global := updateSub(t, m.updates, 1, humanCaller.ID, ServiceInfo{})
	sub := updateSub(t, svc.updates, 2, humanCaller.ID, svc.info)
	bindCLITool(t, m, svc.info.RoomIdentity, members[0], connection, "gitlab", 0)
	for _, u := range sourceTopics(t, sub, "tools") {
		if u.AgentID != members[0].AgentID || u.MembershipID != members[0].MembershipID {
			t.Fatal(u)
		}
	}
	assertSourceQuiet(t, global)
	req := platformTestRequest(m)
	req.ID, req.Version, req.Name = connection.ID, connection.Version, "renamed"
	platformTestSave(t, m, req)
	sourceTopics(t, global, "platforms")
	// A settings installation uses its publication callback, including when
	// the config writer is shared with model management.
	settings := settingsFromFile(t, writeStartupConfig(t, t.TempDir(), "[server]\ntime_zone = 'Asia/Shanghai'\n"), true)
	settings.observe = func() { m.updates.mark(uiUpdate{Topic: "settings"}) }
	if _, err := settings.save("UTC", 1); err != nil {
		t.Fatal(err)
	}
	sourceTopics(t, global, "settings")
	if _, err := settings.save("UTC", 2); err != nil {
		t.Fatal(err)
	}
	assertSourceQuiet(t, global)
	// A source runner's durable checkpoint is independent of HTTP handlers.
	h := newEventHarness(t, gitlab.EventsConfig{})
	live := eventLiveService(t, h)
	updates := updateSub(t, live.updates, 1, humanCaller.ID, live.info)
	h.pages.events = []gitlab.Event{h.event(1, 1)}
	h.step()
	sourceTopics(t, updates, "notifications")
	h.m.mu.Lock()
	state, _ := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	state, err := gitlab.RetireEvents(state)
	if err == nil {
		err = h.m.notifications.SaveLocked(state)
	}
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	sourceTopics(t, updates, "notifications")
}

func TestUIUpdateSourcesAutomaticRecoveryAndRotation(t *testing.T) {
	for _, recovering := range []bool{false, true} {
		t.Run(fmt.Sprint(recovering), func(t *testing.T) {
			h := newRoomHarness(t, historyRoom(0, ""), nil)
			id := h.memberID("A", "generation-A")
			svc := &service{ctx: h.ctx, store: h.store, info: ServiceInfo{RoomIdentity: h.id, ServiceEpoch: "rotation-runtime"}, room: h.addr,
				updates: newUIUpdateHub(), targets: map[string]actorx.Addr{}, dshProfiles: map[string]dshProfile{}}
			list := roomCall[MemberList](t, h, ListMembers{h.id})
			svc.dshProfiles[list.Members[0].Runner.Profile] = dshProfile{}
			observeStoreSources(h.store, svc)
			sub := updateSub(t, svc.updates, 1, humanCaller.ID, svc.info)
			var opens atomic.Int32
			closing, release := make(chan struct{}, 1), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
				n := opens.Add(1)
				return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
					if recovering && n == 1 {
						return dsh.TurnResult{}, dsh.ErrUnavailable
					}
					return dsh.TurnResult{StopReason: "end_turn"}, nil
				}, close: func(context.Context) error {
					if n == 1 && !recovering {
						closing <- struct{}{}
						<-release
					}
					return nil
				}}, nil
			}}
			member := &MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr, Backend: backend,
				SessionMaxTurns: 1, RequestRotation: svc.requestMemberRotation, RequestRecovery: svc.requestMemberRecovery,
				Observe: func() { svc.invalidate("members", id) }}
			addr, err := h.spawn(member)
			if err != nil {
				t.Fatal(err)
			}
			svc.setTarget(id.MembershipID, addr)
			controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
			automaticPost(t, h, addr, id, 1)
			waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
			sourceTopics(t, sub, "members", "activity")
			var status MemberStatus
			if !recovering {
				automaticPost(t, h, addr, id, 2)
				select {
				case <-closing:
				case <-time.After(4 * time.Second):
					t.Fatal("cleanup did not start")
				}
				status = inspectMember(t, h.ctx, addr, id)
				if status.Rotation == nil || status.Status != "rotating" {
					t.Fatal(status)
				}
				sourceTopics(t, sub, "members")
				once.Do(func() { close(release) })
			}
			status = waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
			if status.Rotation.Status != "completed" || status.Agent.Turn.Status != "completed" || opens.Load() != 2 {
				t.Fatal(status)
			}
			sourceTopics(t, sub, "members", "activity")
		})
	}
}
