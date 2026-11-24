package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/calendar"
)

// Real manager, runtime lifecycle, Actor commits and Room publication. Only the
// model component and the evaluation clock are controlled. Neither UI selection
// nor a test-only reload of a calendar is used to restore background work.
type calendarManagedFixture struct {
	t       *testing.T
	root    string
	manager *roomManager
	rooms   []roomSummary
	entered chan MemberIdentity
}

func newCalendarManagedFixture(t *testing.T) *calendarManagedFixture {
	t.Helper()
	t.Setenv("USER", humanCaller.ID)
	f := &calendarManagedFixture{t: t, root: t.TempDir(), entered: make(chan MemberIdentity, 8)}
	access, _, _ := accessFixture(t)
	access.disabled = true
	bindFixtureAccessSubject(access, humanCaller.ID)
	f.manager = f.newManager(access)
	ensureAssistant(t, f.manager)
	w := f.request("/v1/rooms/create", roomCreateRequest{ServiceEpoch: f.manager.epoch, Caller: humanCaller, roomSelection: directSelection("calendar-other")})
	var other roomSummary
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &other) != nil || !other.IsDirect || other.AgentRoleID != assistantRoleID || other.TeamID != "" || other.Status != "ready" {
		t.Fatal(w.Code, w.Body.String())
	}
	f.rooms = []roomSummary{assistantSummary(t, f.manager), other}
	for _, room := range f.rooms {
		w := f.request("/v1/rooms/open", roomOpenRequest{ServiceEpoch: f.manager.epoch, Caller: humanCaller, RoomIdentity: room.RoomIdentity, OpenID: "calendar-open-" + room.RoomID})
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		if op := awaitLatestOpen(t, f.manager); op.Status != "completed" {
			t.Fatal(op)
		}
		f.manager.mu.Lock()
		s := f.manager.runtimes[room.RoomIdentity].service
		f.manager.mu.Unlock()
		f.agentControl(s, "pause")
	}
	return f
}

// Keep the calendar lifecycle fixture on the real local access boundary,
// including the configured Space and human subject after manager restart.
func (f *calendarManagedFixture) request(path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	access := f.manager.config.access
	r := httptest.NewRequest(http.MethodPost, access.internal+path, bytes.NewReader(data))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Origin", access.internal)
	w := httptest.NewRecorder()
	access.wrap(f.manager).ServeHTTP(w, r)
	return w
}

func (f *calendarManagedFixture) newManager(access *serverAccess) *roomManager {
	m := directTestManager(f.t, f.root, testTeamDefinition(f.t).Runtime)
	m.config.unifiedInputs = true
	m.config.access = access
	m.factory = controlledActivationFactory(func(id MemberIdentity, ctx context.Context) error {
		f.entered <- id
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	return m
}

func (f *calendarManagedFixture) service(index int) *service {
	f.t.Helper()
	f.manager.mu.Lock()
	defer f.manager.mu.Unlock()
	rt := f.manager.runtimes[f.rooms[index].RoomIdentity]
	if rt == nil || rt.service == nil || rt.status != "ready" {
		f.t.Fatal("background calendar runtime not restored", f.rooms[index], rt)
	}
	return rt.service
}

func (f *calendarManagedFixture) restart() {
	f.t.Helper()
	old := f.manager
	if err := old.close(); err != nil {
		f.t.Fatal(err)
	}
	f.manager = f.newManager(old.config.access)
	f.manager.config.endpoint = old.config.endpoint
	if err := f.manager.restoreRuntimes(); err != nil {
		f.t.Fatal(err)
	}
	awaitRecovery(f.t, f.manager)
	for i := range f.rooms {
		if f.service(i).calendars != f.manager.calendars {
			f.t.Fatal("recovery installed a separate calendar scheduler")
		}
	}
}

func (f *calendarManagedFixture) agentControl(s *service, action string) {
	f.t.Helper()
	members, err := s.members(f.t.Context())
	if err != nil || len(members.Members) != 1 {
		f.t.Fatal(members, err)
	}
	m := members.Members[0]
	if _, err := s.control(f.t.Context(), apiRequest{AgentID: m.AgentID, MembershipID: m.MembershipID}, action); err != nil {
		f.t.Fatal(err)
	}
}

func (f *calendarManagedFixture) update(index int, request calendar.Request) *calendar.Task {
	f.t.Helper()
	raw, _ := json.Marshal(request)
	w := f.request("/v1/calendar/update", apiRequest{ServiceInfo: f.service(index).info, Caller: humanCaller, Calendar: raw})
	var result calendar.Result
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Task == nil {
		f.t.Fatal(w.Code, w.Body.String())
	}
	return result.Task
}

func (f *calendarManagedFixture) create(index int, title string) *calendar.Task {
	f.t.Helper()
	d := calendarDefinition(time.Now().Add(10 * time.Minute))
	d.Title, d.Schedule.Frequency = title, "daily"
	return f.update(index, calendar.Request{Action: "create", OperationID: "same-operation-in-two-conversations", Definition: &d})
}

func (f *calendarManagedFixture) due(index int, task *calendar.Task) *calendar.Run {
	f.t.Helper()
	s := f.service(index)
	_, err := s.calendarCurrent().call(f.t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) {
		state.Evaluate(task.NextAt.Add(time.Second))
		return nil, nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	state := calendarAwait(f.t, s, func(state calendar.State) bool {
		for _, r := range state.Runs {
			if r.Task.ID == task.ID && r.DueAt.Equal(task.NextAt) && r.EventDelivered {
				return true
			}
		}
		return false
	})
	for _, r := range state.Runs {
		if r.Task.ID == task.ID && r.DueAt.Equal(task.NextAt) {
			return r
		}
	}
	f.t.Fatal("candidate missing")
	return nil
}

func (f *calendarManagedFixture) finish(index int, run *calendar.Run) *calendar.Run {
	f.t.Helper()
	s := f.service(index)
	f.agentControl(s, "enable")
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: run.Task.AgentID, MembershipID: run.Task.MembershipID}
	select {
	case actual := <-f.entered:
		if actual != id {
			f.t.Fatal("another conversation started", actual, id)
		}
	case <-time.After(5 * time.Second):
		f.t.Fatal("controlled model turn did not start")
	}
	var binding calendar.Binding
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		agent, err := s.store.LoadAgent(id)
		if err == nil && agent.Turn != nil && agent.Turn.Status == "running" {
			binding, err = s.calendarBinding(id, agent.TurnNumber)
			if err == nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if binding.Turn == 0 {
		f.t.Fatal("actual scheduled member turn did not start")
	}
	// Exercise the same authenticated execution adapter as the Agent tools;
	// the controlled component supplies the report, never private model output.
	claim, _ := json.Marshal(calendarQuery{RunID: run.ID})
	if _, err := s.calendarCall(f.t.Context(), "claim", claim, agentCallerIdentity(id), binding.Turn, &binding); err != nil {
		f.t.Fatal(err)
	}
	finish, _ := json.Marshal(calendarFinish{RunID: run.ID, Outcome: "succeeded", Report: "组合验收报告：" + run.Task.Definition.Title})
	if _, err := s.calendarCall(f.t.Context(), "finish", finish, agentCallerIdentity(id), binding.Turn, &binding); err != nil {
		f.t.Fatal(err)
	}
	state := calendarAwait(f.t, s, func(state calendar.State) bool { return state.Runs[run.ID].Publication.Status == "published" })
	f.agentControl(s, "pause")
	return state.Runs[run.ID]
}
