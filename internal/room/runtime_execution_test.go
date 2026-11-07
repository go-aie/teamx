package room

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestRuntimeBackgroundNotificationsIgnoreAnotherRoomsOperation(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	seedEventInbox(t, h, 1)
	sa := eventLiveService(t, h)
	b := managerCreate(t, h.m, "other")
	sb := eventLiveService(t, &eventHarness{m: h.m, room: b})
	h.m.mu.Lock()
	rb := h.m.reserveRuntimeLocked(b.RoomIdentity)
	h.m.mu.Unlock()
	s := h.state()
	h.m.mu.Lock()
	scope, err := h.m.admitPlatformNotificationLocked(context.Background(), s)
	h.m.mu.Unlock()
	if err != nil || !scope.Current {
		t.Fatal("background Room lost live polling scope", scope, err)
	}
	if err := h.m.notifications.Deliver(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), s.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	astate, _ := sa.store.LoadRoom()
	bstate, _ := sb.store.LoadRoom()
	if astate.LatestSeq != 1 || bstate.LatestSeq != 0 || len(h.state().Inbox) != 0 {
		t.Fatal("delivery did not follow subscription owner")
	}
	h.m.mu.Lock()
	h.m.finishRuntimeLocked(rb, nil)
	ra := h.m.reserveRuntimeLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	seedEventInbox(t, h, 2)
	s = h.state()
	if err := h.m.notifications.Deliver(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), s.SubscriptionVersion); !errors.Is(err, notification.ErrBusy) {
		t.Fatal("delivery crossed owner's lifecycle operation", err)
	}
	h.m.mu.Lock()
	h.m.finishRuntimeLocked(ra, nil)
	h.m.mu.Unlock()
}

// Production process ownership and Room lifecycle executors; only the tool
// executable and DSH/model component are controlled local fixtures.
func TestRuntimeToolSettlementIsScopedToRoom(t *testing.T) {
	for _, action := range []string{"workspace", "delete", "restart", "shared-rotation"} {
		t.Run(action, func(t *testing.T) {
			m := activationTestManager(t)
			m.factory = controlledActivationFactory(nil, nil)
			a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
			sa, sb := openOK(t, m, a.RoomIdentity, "a"), openOK(t, m, b.RoomIdentity, "b")
			executable := filepath.Join(t.TempDir(), "tool")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf ready\nsleep 30\n"), 0700); err != nil {
				t.Fatal(err)
			}
			name, provider := "local", ""
			var c platformConnection
			if action == "shared-rotation" {
				name, provider = "gitlab", "gitlab"
				c = verifyTodoAccount(t, m, platformTestSave(t, m, platformTestRequest(m)), 101)
			}
			m.toolCatalog = []tool.Definition{{Name: name, Provider: provider, Executable: executable}}
			results := make([]chan int, 2)
			contexts := make([]context.Context, 2)
			for i, info := range []ServiceInfo{sa, sb} {
				m.mu.Lock()
				roster, err := m.toolRosterLocked(context.Background(), info.RoomIdentity)
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				bindCLITool(t, m, info.RoomIdentity, roster[0], c, name, 0)
				m.mu.Lock()
				p, call, ctx, err := m.prepareToolLocked(context.Background(), toolsRequest{ServiceInfo: info, Caller: agentCaller(roster[0].AgentID, roster[0].MembershipID), Tool: name})
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				ready := &toolReadyWriter{started: make(chan struct{})}
				p.Output, p.Error = ready, io.Discard
				results[i], contexts[i] = make(chan int, 1), ctx
				result := results[i]
				go func() { code := p.Run(ctx, nil); m.finishPlatformCall(call); result <- code }()
				select {
				case <-ready.started:
				case <-time.After(3 * time.Second):
					t.Fatal("tool not started")
				}
			}
			openRoomOK(t, m, b, "select-b")
			if contexts[0].Err() != nil {
				t.Fatal("selecting B canceled A tool")
			}
			switch action {
			case "workspace":
				applyWorkspace(t, m, workspaceRequest(t, m, a, workspaceConfig{Root: "changed"}))
				if op := awaitWorkspace(t, m); op.Status != "completed" {
					t.Fatal(op)
				}
			case "delete":
				beginDeletion(t, m, a)
				if remaining := awaitDeletion(t, m, a); remaining != nil {
					t.Fatal(remaining)
				}
			case "restart":
				id, _ := newOpaqueID()
				r := managerHTTP(m, "/v1/discussion/restart", apiRequest{ServiceInfo: sa, Caller: humanCaller, RestartID: id})
				if r.Code != 200 {
					t.Fatal(r.Code, r.Body.String())
				}
				m.mu.Lock()
				done := m.runtimes[a.RoomIdentity].operationDone
				m.mu.Unlock()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("restart did not settle tools")
				}
				m.mu.Lock()
				svc := m.runtimes[a.RoomIdentity].service
				m.mu.Unlock()
				if status := svc.discussionRestartStatus(); status == nil || status.Status != "completed" {
					t.Fatal(status)
				}
			case "shared-rotation":
				req := platformTestRequest(m)
				req.Version = c.Version
				req.ReferencedRooms = []RoomIdentity{a.RoomIdentity, b.RoomIdentity}
				platformTestReauthorize(t, m, req)
			}
			select {
			case code := <-results[0]:
				if code != 130 {
					t.Fatal(code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("A tool not joined")
			}
			if action != "shared-rotation" {
				if contexts[1].Err() != nil {
					t.Fatal("A operation canceled B tool")
				}
				r := managerHTTP(m, "/v1/send", apiRequest{ServiceInfo: sb, Caller: humanCaller, ClientMessageID: "b-survived", Text: "b-survived"})
				if r.Code != 200 {
					t.Fatal("B API stopped", r.Code, r.Body.String())
				}
				m.mu.Lock()
				current := m.runtimes[b.RoomIdentity].service.info
				m.mu.Unlock()
				if current != sb {
					t.Fatal("A operation rebuilt B")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := m.waitPlatformCalls(ctx, &b.RoomIdentity); err != nil {
					t.Fatal(err)
				}
				cancel()
			}
			select {
			case code := <-results[1]:
				if code != 130 {
					t.Fatal(code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("B tool not joined")
			}
			// Reject a late epoch, mixed Room identity and foreign membership on B.
			for _, req := range []toolsRequest{
				{ServiceInfo: ServiceInfo{RoomIdentity: sb.RoomIdentity, ServiceEpoch: sa.ServiceEpoch}, Caller: agentCaller("xiaolin", "stale")},
				{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: sb.RoomID, RoomInstance: sa.RoomInstance}, ServiceEpoch: sb.ServiceEpoch}, Caller: agentCaller("xiaolin", "stale")},
				{ServiceInfo: sb, Caller: agentCaller("xiaolin", "foreign")},
			} {
				m.mu.Lock()
				err := m.checkToolMemberLocked(context.Background(), req)
				m.mu.Unlock()
				if err == nil {
					t.Fatal("foreign tool identity admitted")
				}
			}
		})
	}
}

func TestRuntimeLifecycleOperationsProgressIndependently(t *testing.T) {
	m := activationTestManager(t)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	entered, cleanup, release := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	m.factory = controlledActivationFactory(func(id MemberIdentity, ctx context.Context) error {
		if id.RoomIdentity == a.RoomIdentity {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}, func(id MemberIdentity) {
		if id.RoomIdentity == a.RoomIdentity {
			select {
			case cleanup <- struct{}{}:
			default:
			}
			<-release
		}
	})
	sa := openOK(t, m, a.RoomIdentity, "a")
	openOK(t, m, b.RoomIdentity, "b")
	m.mu.Lock()
	svc := m.runtimes[a.RoomIdentity].service
	m.mu.Unlock()
	managedPost(t, m, svc, "hold-a")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("A did not start")
	}
	beginDeletion(t, m, a)
	select {
	case <-cleanup:
	case <-time.After(5 * time.Second):
		t.Fatal("A did not reach cleanup")
	}
	if r := managerHTTP(m, "/v1/rooms/open", roomOpenRequest{RoomIdentity: a.RoomIdentity, OpenID: "competing", ServiceEpoch: m.epoch, Caller: humanCaller}); r.Code != 409 {
		t.Fatal("open crossed deletion", r.Code)
	}
	req := workspaceRequest(t, m, b, workspaceConfig{Root: "b-new"})
	applyWorkspace(t, m, req)
	if op := awaitWorkspace(t, m); op.Status != "completed" {
		t.Fatal(op)
	}
	beginDeletion(t, m, b)
	if remaining := awaitDeletion(t, m, b); remaining != nil {
		t.Fatal(remaining)
	}
	m.mu.Lock()
	stillOwned := m.runtimes[a.RoomIdentity].busy && m.runtimes[a.RoomIdentity].service.info == sa
	m.mu.Unlock()
	if !stillOwned {
		t.Fatal("B operation released A's cleanup ownership")
	}
	once.Do(func() { close(release) })
	if remaining := awaitDeletion(t, m, a); remaining != nil {
		t.Fatal(remaining)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.runtimes) != 0 || len(m.index.Rooms) != 0 || len(m.index.OwnedRooms) != 0 || len(m.index.WorkspaceOperations) != 0 {
		t.Fatal("concurrent lifecycle left ownership or receipts", m.index)
	}
}

func TestRuntimeConcurrentOpenReceiptsKeepIndependentServices(t *testing.T) {
	m := activationTestManager(t)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	m.factory = func(dir, id string, config serviceConfig) (*service, error) {
		if id == a.RoomID {
			close(entered)
			<-release
		}
		return controlledActivationFactory(nil, nil)(dir, id, config)
	}
	beginManagedOpen(t, m, a, "a")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("A did not enter assembly")
	}
	sb := openRoomOK(t, m, b, "b")
	m.mu.Lock()
	done := m.runtimes[a.RoomIdentity].operationDone
	m.mu.Unlock()
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("A did not finish")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runtimes[b.RoomIdentity].service != sb {
		t.Fatal("late activation overwrote latest selection")
	}
	for _, op := range m.index.Opens {
		if op.Status != "completed" {
			t.Fatal("concurrent activation overwrote another receipt", op)
		}
	}
}
