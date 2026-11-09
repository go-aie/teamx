//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeOpenRoutesAndReusesInstances(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	var assemblies atomic.Int32
	m.factory = func(dir, id string, config serviceConfig) (*service, error) {
		assemblies.Add(1)
		return controlledActivationFactory(nil, nil)(dir, id, config)
	}
	a, b, c := managerCreate(t, m, "a"), managerCreate(t, m, "b"), managerCreate(t, m, "c")
	sa := openOK(t, m, a.RoomIdentity, "open-a")
	sb := openOK(t, m, b.RoomIdentity, "open-b")
	if again := openOK(t, m, a.RoomIdentity, "a-again"); again != sa || assemblies.Load() != 2 {
		t.Fatal("open replaced a healthy instance", again, assemblies.Load())
	}
	if op := beginOpen(t, m, a.RoomIdentity, "open-a"); op.Service == nil || *op.Service != sa {
		t.Fatal("receipt changed", op)
	}
	for _, info := range []ServiceInfo{sa, sb} {
		req := apiRequest{ServiceInfo: info, Caller: humanCaller, ClientMessageID: "same-id", Text: info.RoomID}
		if r := managerHTTP(m, "/v1/send", req); r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		r := managerHTTP(m, "/v1/read", apiRequest{ServiceInfo: info, Caller: humanCaller, Mode: ReadAll})
		var page ReadPage
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &page) != nil || len(page.Messages) != 1 || page.Messages[0].Text != info.RoomID {
			t.Fatal("request crossed rooms", r.Code, r.Body.String())
		}
		q := url.Values{"room_id": {info.RoomID}, "room_instance": {info.RoomInstance}, "service_epoch": {info.ServiceEpoch}, "caller_id": {humanCaller.ID}}
		r = managerHTTP(m, "/v1/info?"+q.Encode(), nil)
		var got ServiceInfo
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &got) != nil || got != info {
			t.Fatal("GET selected the wrong instance", r.Code, r.Body.String())
		}
		m.mu.Lock()
		svc := m.runtimes[info.RoomIdentity].service
		m.mu.Unlock()
		members, err := svc.members(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, member := range members.Members {
			if member.Execution == nil || member.Execution.SessionTurns.Total > 1 {
				t.Fatal("open reset execution accounting", member)
			}
		}
	}
	for _, bad := range []ServiceInfo{
		{RoomIdentity: RoomIdentity{RoomID: sa.RoomID, RoomInstance: sb.RoomInstance}, ServiceEpoch: sa.ServiceEpoch},
		{RoomIdentity: sa.RoomIdentity, ServiceEpoch: sb.ServiceEpoch},
		{RoomIdentity: c.RoomIdentity, ServiceEpoch: sa.ServiceEpoch},
	} {
		if r := managerHTTP(m, "/v1/send", apiRequest{ServiceInfo: bad, Caller: humanCaller, Text: "wrong", ClientMessageID: "wrong"}); r.Code != 403 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if r := managerHTTP(m, "/v1/info", nil); r.Code != 403 {
		t.Fatal("identity-free GET picked an instance", r.Code)
	}
	for _, caller := range []Caller{{Author: Author{Kind: "human", ID: ""}}, {Author: Author{Kind: "agent", ID: "xiaolin"}, MembershipID: "old"}} {
		if r := managerHTTP(m, "/v1/read", apiRequest{ServiceInfo: sa, Caller: caller}); r.Code != 403 {
			t.Fatal("invalid caller accepted", r.Code, r.Body.String())
		}
	}
	if assemblies.Load() != 2 {
		t.Fatal("ordinary request implicitly started a Room")
	}
	var catalog roomsResponse
	r := managerHTTP(m, "/v1/rooms", nil)
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &catalog) != nil || catalog.Rooms[0].Service == nil || *catalog.Rooms[0].Service != sa || catalog.Rooms[1].Service == nil || *catalog.Rooms[1].Service != sb || catalog.Rooms[2].RuntimeStatus != "closed" {
		t.Fatal(r.Code, r.Body.String())
	}

}

func TestRuntimeOpenReservesOnlyItsRoomAndDeduplicates(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var assemblies atomic.Int32
	m.factory = func(dir, id string, config serviceConfig) (*service, error) {
		assemblies.Add(1)
		if id == a.RoomID {
			close(entered)
			<-release
		}
		return newService(dir, id, config)
	}
	beginOpen(t, m, a.RoomIdentity, "open-a")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("assembly not reached")
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := managerHTTP(m, "/v1/rooms/open", roomOpenRequest{RoomIdentity: a.RoomIdentity, OpenID: "open-a", ServiceEpoch: m.epoch, Caller: humanCaller})
			if r.Code != 200 {
				t.Error(r.Code, r.Body.String())
			}
		}()
	}
	wg.Wait()
	if assemblies.Load() != 1 {
		t.Fatal("duplicate service assembly")
	}
	if r := managerHTTP(m, "/v1/rooms/delete", struct {
		RoomIdentity
		ServiceEpoch string `json:"service_epoch"`
		Caller       Caller `json:"caller"`
	}{a.RoomIdentity, m.epoch, humanCaller}); r.Code != 409 {
		t.Fatal("deletion crossed the opening slot", r.Code, r.Body.String())
	}
	if r := managerHTTP(m, "/v1/rooms/open", roomOpenRequest{RoomIdentity: a.RoomIdentity, OpenID: "competing-a", ServiceEpoch: m.epoch, Caller: humanCaller}); r.Code != 409 {
		t.Fatal("same-room slot not reserved", r.Code)
	}
	if r := managerHTTP(m, "/v1/rooms/open", roomOpenRequest{RoomIdentity: b.RoomIdentity, OpenID: "open-a", ServiceEpoch: m.epoch, Caller: humanCaller}); r.Code != 409 {
		t.Fatal("receipt reused for another Room", r.Code)
	}
	// A blocked factory does not hold the catalog lock or B's operation slot.
	openOK(t, m, b.RoomIdentity, "open-b")
	q := url.Values{"service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}, "open_id": {"open-a"}}
	r := managerHTTP(m, "/v1/rooms/open?"+q.Encode(), nil)
	var op roomOpen
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &op) != nil || op.Status != "running" {
		t.Fatal(r.Code, r.Body.String())
	}
	m.mu.Lock()
	owners := append([]RoomIdentity{}, m.index.OwnedRooms...)
	m.mu.Unlock()
	if len(owners) != 2 {
		t.Fatal("assembly did not durably reserve both owners", owners)
	}
	unblock()
	if op := awaitOpen(t, m, a.RoomIdentity, "open-a"); op.Status != "completed" {
		t.Fatal("A completion overwrote another Room's receipt", op)
	}
}

func TestRuntimeOpenFailureIsLocalAndRequiresNewReceipt(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	var fail atomic.Bool
	fail.Store(true)
	m.factory = func(dir, id string, config serviceConfig) (*service, error) {
		if id == a.RoomID && fail.Load() {
			return nil, errors.New("injected assembly failure")
		}
		return newService(dir, id, config)
	}
	beginOpen(t, m, a.RoomIdentity, "bad-a")
	if op := awaitOpen(t, m, a.RoomIdentity, "bad-a"); op.Status != "failed" {
		t.Fatal(op)
	}
	sb := openOK(t, m, b.RoomIdentity, "open-b")
	fail.Store(false)
	if op := beginOpen(t, m, a.RoomIdentity, "bad-a"); op.Status != "failed" {
		t.Fatal("replayed failed open", op)
	}
	openOK(t, m, a.RoomIdentity, "retry-a")
	if again := openOK(t, m, b.RoomIdentity, "b-again"); again != sb {
		t.Fatal("A failure changed B's epoch")
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	// Basic resource ownership must close every opened Store even before the
	// multi-room recovery/freeze/settlement work in step 3.
	for _, room := range []roomSummary{a, b} {
		store, err := OpenStore(m.roomDir(room.RoomID), room.RoomID)
		if err != nil {
			t.Fatal("shutdown leaked a Room lock", err)
		}
		store.Close()
	}
}

func TestRuntimeCLIResolvesOnlyExplicitOpenRoom(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	a, b, c := managerCreate(t, m, "a"), managerCreate(t, m, "b"), managerCreate(t, m, "c")
	sa := openOK(t, m, a.RoomIdentity, "open-a")
	sb := openOK(t, m, b.RoomIdentity, "open-b")
	server := httptest.NewServer(m)
	defer server.Close()
	client := cliClient{endpoint: server.URL, http: server.Client()}
	env := func(key string) string {
		if key == "USER" {
			return "operator"
		}
		return ""
	}
	for _, info := range []ServiceInfo{sa, sb} {
		req, err := client.identity(context.Background(), info.RoomID, env)
		if err != nil || req.ServiceInfo != info {
			t.Fatal(req, err)
		}
	}
	for _, id := range []string{"", c.RoomID, "missing"} {
		if _, err := client.identity(context.Background(), id, env); err == nil {
			t.Fatal("CLI implicitly selected or opened a Room", id)
		}
	}
}

func TestRuntimeOpenUnknownReservationNeverStartsAssembly(t *testing.T) {
	parallelFixtureTest(t)
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-replace", true: "after-replace"}[after], func(t *testing.T) {
			m := activationTestManager(t)
			a := managerCreate(t, m, "a")
			var assemblies atomic.Int32
			m.factory = func(dir, id string, config serviceConfig) (*service, error) {
				assemblies.Add(1)
				return newService(dir, id, config)
			}
			rename := m.store.ops.rename
			m.store.ops.rename = func(from, to string) error {
				if after {
					if err := rename(from, to); err != nil {
						return err
					}
				}
				return errors.New("open reservation result unknown")
			}
			r := managerHTTP(m, "/v1/rooms/open", roomOpenRequest{RoomIdentity: a.RoomIdentity, OpenID: "uncertain", ServiceEpoch: m.epoch, Caller: humanCaller})
			if r.Code != 500 || assemblies.Load() != 0 || len(m.runtimes) != 0 {
				t.Fatal("started assembly without durable ownership", r.Code, r.Body.String())
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored := testManager(t, m.store.dir, managerTestProfiles(t))
			restored.config = m.config
			if after {
				op := beginOpen(t, restored, a.RoomIdentity, "uncertain")
				if op.Status != "failed" || len(restored.index.OwnedRooms) != 1 || len(restored.runtimes) != 0 {
					t.Fatal("replayed unknown open or lost ownership", op)
				}
			} else {
				openOK(t, restored, a.RoomIdentity, "uncertain")
			}
		})
	}
}

func TestRuntimeShutdownRetainsOwnershipOfOpeningWorker(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	a := managerCreate(t, m, "a")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	m.factory = func(dir, id string, config serviceConfig) (*service, error) {
		close(entered)
		<-release
		return newService(dir, id, config)
	}
	beginOpen(t, m, a.RoomIdentity, "opening")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("assembly not reached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown abandoned an opening worker", err)
	}
	if other, err := newRoomManager(m.store.dir, 7); err == nil {
		other.close()
		t.Fatal("released root ownership before worker settled")
	}
	unblock()
	if op := awaitOpen(t, m, a.RoomIdentity, "opening"); op.Status != "failed" {
		t.Fatal("published an instance after shutdown", op)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCatalogHasNoGlobalSelectionAndScopesBusyAndNames(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	sa, sb := openOK(t, m, a.RoomIdentity, "a"), openOK(t, m, b.RoomIdentity, "b")
	m.mu.Lock()
	rt := m.reserveRuntimeLocked(a.RoomIdentity)
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.finishRuntimeLocked(rt, nil); m.mu.Unlock() }()
	r := managerHTTP(m, "/v1/rooms", nil)
	var catalog roomsResponse
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &catalog) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	if catalog.Busy {
		t.Fatal("one Room's operation blocked the catalog")
	}
	for _, room := range catalog.Rooms {
		want := sa
		if room.RoomIdentity == b.RoomIdentity {
			want = sb
		}
		if room.Service == nil || *room.Service != want || room.Busy != (room.RoomIdentity == a.RoomIdentity) || len(room.Members) != len(m.teams["takeout-dinner"].Members) || room.Members[0].Name == "" {
			t.Fatal(room)
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(r.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"active", "active_room_id", "default_room_id", "activation", "members"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("legacy global %s exposed", key)
		}
	}
	if r := managerHTTP(m, "/v1/rooms/activate", map[string]any{"service_epoch": m.epoch, "caller": humanCaller, "room_id": b.RoomID, "room_instance": b.RoomInstance, "activate_id": "retired"}); r.Code != 404 {
		t.Fatal("retired endpoint is callable", r.Code, r.Body.String())
	}
}
