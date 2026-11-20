package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/calendar"
)

func calendarFixture(t *testing.T, dir string, backend Backend, settings ...*systemSettings) *service {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "room.json")); errors.Is(err, os.ErrNotExist) {
		store, err := OpenStore(dir, "design-review")
		if err != nil {
			t.Fatal(err)
		}
		state := historyRoom(1, "请安排一次性任务，读取已有资料并回复这里")
		state.IsDirect = true
		state.InputLog = &roomLogRoot{Version: 1}
		if err := store.SaveRoom(state); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	cfg := serviceConfig{unifiedInputs: true, maxTurns: 100}
	if len(settings) > 0 {
		cfg.settings = settings[0]
	}
	svc, err := startService(dir, "design-review", cfg, func(s *service, state RoomSnapshot) error {
		for _, member := range state.Members {
			id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
			var restored *AgentSnapshot
			if saved, err := s.store.LoadAgent(id); err == nil {
				restored = &saved
			}
			m := s.newMember(id, member.Runner, restored).(*MemberAgent)
			m.Backend = backend
			m.SessionMaxTurns = 0
			m.RequestRotation = nil
			m.RequestRecovery = nil
			m.MaxTurns = 100
			addr, err := spawnScheduledMember(s.rt, m, s.scheduler)
			if err != nil {
				return err
			}
			s.setTarget(id.MembershipID, addr)
			if _, err := s.room.Call[MemberInfo](s.ctx, AttachMember{MemberIdentity: id, Target: addr}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.calendarRunning.Store(true)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := svc.close(ctx); err != nil {
			t.Error(err)
		}
	})
	return svc
}
func calendarDefinition(due time.Time) calendar.Definition {
	due = due.UTC()
	return calendar.Definition{Title: "一次性检查", Instruction: "读取已确认的范围并回复本私信", Scope: json.RawMessage(`{"workspace":"verified"}`), Schedule: calendar.Schedule{Frequency: "once", StartDate: due.Format("2006-01-02"), LocalTime: due.Format("15:04"), TimeZone: "UTC", DayFilter: "all_days"}}
}
func calendarCreate(t *testing.T, s *service, due time.Time) calendar.Task {
	t.Helper()
	d := calendarDefinition(due)
	q := calendar.Request{Action: "create", OperationID: "create-once", Definition: &d}
	data, _ := json.Marshal(q)
	v, err := s.calendarCall(t.Context(), "update", data, humanCaller, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return *v.(calendar.Result).Task
}
func calendarRunNow(t *testing.T, s *service, task calendar.Task) *calendar.Run {
	t.Helper()
	h := s.calendarCurrent()
	v, err := h.call(t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) {
		state.Evaluate(task.NextAt.Add(time.Second))
		for _, r := range state.Runs {
			return r, nil
		}
		return nil, ErrRequest
	})
	if err != nil {
		t.Fatal(err)
	}
	return v.(*calendar.Run)
}
func calendarAwait(t *testing.T, s *service, predicate func(calendar.State) bool) calendar.State {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		h := s.calendarCurrent()
		if h != nil {
			p := h.projection.Load()
			if p.err != nil {
				t.Fatal(p.err)
			}
			if predicate(p.state) {
				return p.state.Clone()
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("calendar did not reach expected committed state")
	return calendar.State{}
}
func calendarRunning(t *testing.T, s *service, number uint64) calendar.Binding {
	t.Helper()
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	err := s.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: number, Turn: &TurnRecord{Number: number, Status: "running"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.calendarBinding(id, number)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestCalendarPersistenceDeliveryAndReportReceiptAfterClear(t *testing.T) {
	dir := t.TempDir()
	s := calendarFixture(t, dir, newControlledBackend())
	task := calendarCreate(t, s, time.Now().Add(3*time.Minute))
	r := calendarRunNow(t, s, task)
	calendarAwait(t, s, func(state calendar.State) bool { return state.Runs[r.ID].EventDelivered })
	binding := calendarRunning(t, s, 1)
	h := s.calendarCurrent()
	_, err := h.call(t.Context(), false, func(_ context.Context, state *calendar.State) (any, error) {
		_, err := state.Claim(r.ID, binding, "generation-A", task.NextAt.Add(time.Second))
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Persist finish, then quiesce publication to model a lost Calendar receipt.
	h.retired.Store(true)
	_, err = h.call(t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) {
		return state.Finish(r.ID, binding, "generation-A", "succeeded", "原公开报告 CALENDAR_RECEIPT", nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := h.projection.Load().state.Runs[r.ID]
	receipt, err := s.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: s.info.RoomIdentity, Caller: agentCallerIdentity(MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}), ClientMessageID: pending.Publication.ClientMessageID, Text: pending.Report, DurablePublication: true})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.room.Call[ReadPage](t.Context(), ReadMessages{RoomIdentity: s.info.RoomIdentity, Caller: agentCallerIdentity(MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}), Mode: ReadUnread, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.ReadReceipt != "" {
		if _, err := s.room.Call[ReadConfirmation](t.Context(), ConfirmRead{MemberIdentity: MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}, ReadReceipt: page.ReadReceipt}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.room.Call[ClearReceipt](t.Context(), ClearMessages{RoomIdentity: s.info.RoomIdentity, Caller: humanCaller, ThroughSeq: receipt.Seq}); err != nil {
		t.Fatal(err)
	}
	agent, _ := s.store.LoadAgent(MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"})
	agent.Turn.Status = "completed"
	if err := s.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	// No Calendar confirmation is saved. Closing retains the publishing intent;
	// reopening must recover the original seq from the independent Room receipt.
	if err := s.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened := calendarFixture(t, dir, newControlledBackend())
	h = reopened.calendarCurrent()
	if h == nil {
		t.Fatal("calendar was not recovered")
	}
	state := calendarAwait(t, reopened, func(state calendar.State) bool { return state.Runs[r.ID].Publication.Status == "published" })
	if state.Runs[r.ID].Publication.Seq != receipt.Seq || state.Runs[r.ID].Status != "succeeded" {
		t.Fatal(state.Runs[r.ID])
	}
	saved, _ := reopened.store.LoadRoom()
	if saved.LatestSeq != receipt.Seq || len(saved.Messages) != 0 {
		t.Fatal("report duplicated after clear", saved.LatestSeq, receipt.Seq)
	}
	_, err = reopened.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: reopened.info.RoomIdentity, Caller: agentCallerIdentity(MemberIdentity{RoomIdentity: reopened.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}), ClientMessageID: pending.Publication.ClientMessageID, Text: "changed", DurablePublication: true})
	if !errors.Is(err, ErrConflict) {
		t.Fatal("same ID accepted different body", err)
	}
}
func TestCalendarOperationIdentityAndVersionHTTP(t *testing.T) {
	s := calendarFixture(t, t.TempDir(), newControlledBackend())
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	binding := calendarRunning(t, s, 1)
	q := calendar.Request{Action: "create", OperationID: "agent-create", HumanSource: calendar.Source{MessageSeq: 1}}
	d := calendarDefinition(time.Now().Add(3 * time.Minute))
	q.Definition = &d
	b, _ := json.Marshal(q)
	v, err := s.calendarCall(t.Context(), "update", b, agentCallerIdentity(id), 1, &binding)
	if err != nil {
		t.Fatal(err)
	}
	task := v.(calendarMutationView)
	if _, err := s.room.Call[ClearReceipt](t.Context(), ClearMessages{RoomIdentity: s.info.RoomIdentity, Caller: humanCaller, ThroughSeq: 1}); err != nil {
		t.Fatal(err)
	}
	v, err = s.calendarCall(t.Context(), "update", b, agentCallerIdentity(id), 1, &binding)
	if err != nil || !v.(calendarMutationView).OperationReused {
		t.Fatal("lost create reply did not replay", v, err)
	}
	q.OperationID = "fake-source"
	q.HumanSource.MessageSeq = 999
	b, _ = json.Marshal(q)
	if _, err = s.calendarCall(t.Context(), "update", b, agentCallerIdentity(id), 1, &binding); err == nil {
		t.Fatal("fake human accepted")
	}
	q = calendar.Request{Action: "pause", TaskID: task.TaskID, ExpectedVersion: 99, OperationID: "stale"}
	b, _ = json.Marshal(q)
	body, _ := json.Marshal(apiRequest{ServiceInfo: s.info, Caller: humanCaller, Calendar: b})
	res := httptest.NewRecorder()
	s.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/calendar/update", bytes.NewReader(body)))
	if res.Code != http.StatusConflict {
		t.Fatal(res.Code, res.Body.String())
	}
	bad := binding
	bad.Turn++
	b = json.RawMessage(`{"run_id":"unknown"}`)
	if _, err = s.calendarCall(t.Context(), "claim", b, agentCallerIdentity(id), bad.Turn, &bad); err == nil {
		t.Fatal("old execution accepted")
	}
}
func TestCalendarPauseCancelAndAdditionalReadinessGates(t *testing.T) {
	s := calendarFixture(t, t.TempDir(), newControlledBackend())
	task := calendarCreate(t, s, time.Now().Add(3*time.Minute))
	r := calendarRunNow(t, s, task)
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	if !s.additionalWorkReady(id) {
		t.Fatal("queued task did not publish readiness")
	}
	m := &MemberAgent{Options: SessionOptions{MemberIdentity: id}, AdditionalWorkReady: func() bool { return s.additionalWorkReady(id) }, now: time.Now, turnLimit: 100}
	if !m.ready() {
		t.Fatal("no unread: additional work cannot run")
	}
	m.paused = true
	if m.ready() {
		t.Fatal("calendar bypassed pause")
	}
	m.paused = false
	m.BackendBlocked = "unsettled"
	if m.ready() {
		t.Fatal("calendar bypassed Session barrier")
	}
	q := calendar.Request{Action: "cancel", OperationID: "cancel", TaskID: task.ID, ExpectedVersion: 1}
	b, _ := json.Marshal(q)
	if _, err := s.calendarCall(t.Context(), "update", b, humanCaller, 0, nil); err != nil {
		t.Fatal(err)
	}
	state := s.calendarCurrent().projection.Load().state
	if state.Runs[r.ID].Status != "cancelled" || s.additionalWorkReady(id) {
		t.Fatal("cancelled run eligible")
	}
}
func TestCalendarSingleOwnerAndQuiesceRetainsDirectoryLock(t *testing.T) {
	s := calendarFixture(t, t.TempDir(), newControlledBackend())
	var wg sync.WaitGroup
	handles := make(chan *calendarHandle, 20)
	for range 20 {
		wg.Go(func() {
			h, err := s.calendarOwner(true)
			if err != nil {
				t.Error(err)
			}
			handles <- h
		})
	}
	wg.Wait()
	close(handles)
	var first *calendarHandle
	for h := range handles {
		if first == nil {
			first = h
		}
		if h != first {
			t.Fatal("multiple owners")
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := first.call(context.Background(), false, func(_ context.Context, state *calendar.State) (any, error) {
			close(entered)
			<-release
			return nil, nil
		})
		finished <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.close(ctx); err == nil {
		t.Fatal("shutdown released in-flight writer")
	}
	if _, err := OpenStore(s.store.dir, s.info.RoomID); err == nil {
		t.Fatal("directory lock released before worker")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := s.close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCalendarCancellationOnlyStopsClaimedTurnAndJoinsSettlement(t *testing.T) {
	backend := newControlledBackend()
	backend.settle = make(chan struct{})
	defer func() {
		select {
		case <-backend.settle:
		default:
			close(backend.settle)
		}
	}()
	s := calendarFixture(t, t.TempDir(), backend)
	task := calendarCreate(t, s, time.Now().Add(3*time.Minute))
	run := calendarRunNow(t, s, task)
	if _, err := s.control(t.Context(), apiRequest{AgentID: "A", MembershipID: "generation-A"}, "enable"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("member did not start")
	}
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	agent, err := s.store.LoadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.calendarBinding(id, agent.TurnNumber)
	if err != nil {
		t.Fatal(err)
	}
	bad := binding
	bad.Generation = "old-generation"
	if _, err := s.target(id.MembershipID).Call[MemberStatus](t.Context(), ControlMember{MemberIdentity: id, Action: "cancel_bound", expectedBinding: func() *turnExecution { v := turnExecution(bad); return &v }()}); !errors.Is(err, ErrConflict) {
		t.Fatal("old generation cancellation accepted", err)
	}
	_, err = s.calendarCurrent().call(t.Context(), false, func(_ context.Context, state *calendar.State) (any, error) {
		return state.Claim(run.ID, binding, id.MembershipID, task.NextAt.Add(time.Second))
	})
	if err != nil {
		t.Fatal(err)
	}
	q := calendar.Request{Action: "pause", OperationID: "pause-running", TaskID: task.ID, ExpectedVersion: 1}
	b, _ := json.Marshal(q)
	if _, err = s.calendarCall(t.Context(), "update", b, humanCaller, 0, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("matching turn not cancelled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := s.close(ctx); err == nil {
		t.Fatal("unsettled turn released ownership")
	}
	if _, err := OpenStore(s.store.dir, s.info.RoomID); err == nil {
		t.Fatal("lock released before backend settled")
	}
	close(backend.settle)
	if err := s.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := loadCalendar(s)
	if err != nil {
		t.Fatal(err)
	}
	if state.Runs[run.ID].Status != "cancelled" {
		t.Fatal(state.Runs[run.ID])
	}
}

func TestCalendarCLIJSONUsesCommittedServiceAndRejectsSessionOnlyClaim(t *testing.T) {
	s := calendarFixture(t, t.TempDir(), newControlledBackend())
	server := browserTestServer(t, s)
	env := func(key string) string {
		if key == "TEAMX_ENDPOINT" {
			return server.URL
		}
		if key == "USER" {
			return humanOperator()
		}
		return ""
	}
	d := calendarDefinition(time.Now().Add(10 * time.Minute))
	q := calendar.Request{Action: "create", OperationID: "cli-once", Definition: &d}
	b, _ := json.Marshal(q)
	var output, stderr bytes.Buffer
	if err := runCLIWithInput(t.Context(), []string{"calendar", "update", "--request-file", "-"}, bytes.NewReader(b), &output, &stderr, env); err != nil {
		t.Fatal(err, stderr.String())
	}
	var result calendarMutationView
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.TaskID == "" {
		t.Fatal(output.String(), err)
	}
	output.Reset()
	if err := runCLIWithInput(t.Context(), []string{"calendar", "get", "--task-id", result.TaskID}, nil, &output, &stderr, env); err != nil {
		t.Fatal(err)
	}
	var task calendarTaskView
	if err := json.Unmarshal(output.Bytes(), &task); err != nil || task.TaskID != result.TaskID {
		t.Fatal(output.String(), err)
	}
	req := apiRequest{ServiceInfo: s.info, Caller: agentCallerIdentity(MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}), TurnNumber: 1, Calendar: json.RawMessage(`{"run_id":"run_any"}`)}
	body, _ := json.Marshal(req)
	response := httptest.NewRecorder()
	s.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/calendar/claim", bytes.NewReader(body)))
	if response.Code != http.StatusForbidden {
		t.Fatal("Session-only claim accepted", response.Code, response.Body.String())
	}
}

func TestCalendarSlowCommitDoesNotBlockOtherConversationOrCommittedReads(t *testing.T) {
	shared, err := newCalendarService()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shared.close(ctx); err != nil {
			t.Error(err)
		}
	})
	first := calendarFixture(t, t.TempDir(), newControlledBackend())
	secondDir := t.TempDir()
	store, err := OpenStore(secondDir, "design-review")
	if err != nil {
		t.Fatal(err)
	}
	room := historyRoom(1, "安排一次任务")
	room.IsDirect = true
	room.RoomInstance = "other-instance"
	room.InputLog = &roomLogRoot{Version: 1}
	if err := store.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	second := calendarFixture(t, secondDir, newControlledBackend())
	for _, s := range []*service{first, second} {
		if err := s.calendars.close(t.Context()); err != nil {
			t.Fatal(err)
		}
		s.calendars = shared
		s.ownCalendars = false
	}
	task := calendarCreate(t, first, time.Now().Add(3*time.Minute))
	owner := first.calendarCurrent()
	original := owner.projection.Load().state.Revision
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := owner.call(context.Background(), false, func(_ context.Context, state *calendar.State) (any, error) {
			state.Tasks[task.ID].Definition.Title = "not yet committed"
			close(entered)
			<-release
			return nil, nil
		})
		done <- err
	}()
	<-entered
	observed, err := owner.addr.Call[calendar.State](t.Context(), calendarInspect{})
	if err != nil || observed.Revision != original || observed.Tasks[task.ID].Definition.Title != "一次性检查" {
		t.Fatal("candidate leaked before save", observed, err)
	}
	start := time.Now()
	other := calendarCreate(t, second, time.Now().Add(3*time.Minute))
	if time.Since(start) > time.Second {
		t.Fatal("another calendar waited for slow commit")
	}
	if task.ID == other.ID || owner == second.calendarCurrent() {
		t.Fatal("Room instance identities merged")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCalendarRunsWithNoRoomUnreadWithoutRedeliveringEvent(t *testing.T) {
	backend := newControlledBackend()
	s := calendarFixture(t, t.TempDir(), backend)
	task := calendarCreate(t, s, time.Now().Add(3*time.Minute))
	run := calendarRunNow(t, s, task)
	calendarAwait(t, s, func(state calendar.State) bool { return state.Runs[run.ID].EventDelivered })
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	page, err := s.room.Call[ReadPage](t.Context(), ReadMessages{RoomIdentity: s.info.RoomIdentity, Caller: agentCallerIdentity(id), Mode: ReadUnread, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.room.Call[ReadConfirmation](t.Context(), ConfirmRead{MemberIdentity: id, ReadReceipt: page.ReadReceipt}); err != nil {
		t.Fatal(err)
	}
	// The only due event is already consumed. The actor's durable pending work
	// must request the normal scheduler rather than append another event.
	if _, err := s.control(t.Context(), apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "enable"); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-backend.entered:
		if input.UnreadCount != 0 {
			t.Fatal("test did not exercise zero unread", input)
		}
	case <-time.After(3 * time.Second):
		members, _ := s.members(t.Context())
		t.Fatal("pending calendar did not start normal member turn", s.additionalWorkReady(id), members)
	}
	agent, err := s.store.LoadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := s.calendarBinding(id, agent.TurnNumber)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"run_id": run.ID})
	if _, err := s.calendarCall(t.Context(), "claim", body, agentCallerIdentity(id), bound.Turn, &bound); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(calendarFinish{RunID: run.ID, Outcome: "succeeded", Report: "ZERO_UNREAD_CALENDAR_OK"})
	if _, err := s.calendarCall(t.Context(), "finish", body, agentCallerIdentity(id), bound.Turn, &bound); err != nil {
		t.Fatal(err)
	}
	backend.finish <- nil
	calendarAwait(t, s, func(state calendar.State) bool { return state.Runs[run.ID].Publication.Status == "published" })
	room, err := s.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	for _, message := range room.Messages {
		if message.Event != nil {
			events++
		}
	}
	if events != 1 || room.LatestSeq != 3 {
		t.Fatal("continued work appended another due event", events, room.LatestSeq)
	}
}

func TestCalendarUnknownCommitFreezesWritesButCanCloseAfterJoining(t *testing.T) {
	dir := t.TempDir()
	s := calendarFixture(t, dir, newControlledBackend())
	task := calendarCreate(t, s, time.Now().Add(10*time.Minute))
	h := s.calendarCurrent()
	_, err := h.call(t.Context(), false, func(_ context.Context, state *calendar.State) (any, error) { return nil, ErrCommitUnknown })
	if !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if _, err := h.call(t.Context(), false, func(_ context.Context, state *calendar.State) (any, error) {
		t.Error("frozen calendar admitted a write")
		return nil, nil
	}); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	d := calendarDefinition(task.NextAt)
	original, _ := json.Marshal(calendar.Request{Action: "create", OperationID: "create-once", Definition: &d})
	if _, err := s.calendarCall(t.Context(), "update", original, humanCaller, 0, nil); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("replay bypassed frozen calendar", err)
	}
	if err := s.close(t.Context()); err != nil {
		t.Fatal("idle frozen actor prevented safe shutdown", err)
	}
	reopened := calendarFixture(t, dir, newControlledBackend())
	state := reopened.calendarCurrent().projection.Load().state
	if state.Tasks[task.ID] == nil {
		t.Fatal("original evidence lost on reopen")
	}
	replay, err := reopened.calendarCall(t.Context(), "update", original, humanCaller, 0, nil)
	if err != nil || !replay.(calendar.Result).OperationReused || len(reopened.calendarCurrent().projection.Load().state.Tasks) != 1 {
		t.Fatal("safe reopen did not recover original create", replay, err)
	}

}
