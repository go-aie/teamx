package room

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/calendar"
)

// CalendarService owns routing and the single process scheduler, never tasks.
// The complete Room identity is reserved before actor creation or disk reads.
type CalendarService struct {
	mu      sync.Mutex
	runtime *actorx.Runtime
	handles map[RoomIdentity]*calendarHandle
	loading map[RoomIdentity]bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func newCalendarService() (*CalendarService, error) {
	if _, err := calendar.LoadWorkCalendars(); err != nil {
		return nil, fmt.Errorf("calendar annual data: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt, err := actorx.NewRuntime(context.Background())
	if err != nil {
		cancel()
		return nil, err
	}
	c := &CalendarService{runtime: rt, handles: map[RoomIdentity]*calendarHandle{}, loading: map[RoomIdentity]bool{}, cancel: cancel, done: make(chan struct{})}
	go c.schedule(ctx)
	return c, nil
}
func (c *CalendarService) attach(s *service) (*calendarHandle, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosing
	}
	if h := c.handles[s.info.RoomIdentity]; h != nil {
		c.mu.Unlock()
		if h.service != s {
			return nil, ErrBusy
		}
		return h, nil
	}
	if c.loading[s.info.RoomIdentity] {
		c.mu.Unlock()
		return nil, ErrBusy
	}
	c.loading[s.info.RoomIdentity] = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.loading, s.info.RoomIdentity); c.mu.Unlock() }()
	// No disk or actor wait holds the routing gate for other conversations.
	state, err := loadCalendar(s)
	if err != nil {
		return nil, fmt.Errorf("calendar: %w", err)
	}
	boot, err := newOpaqueID()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	closing := c.closed
	c.mu.Unlock()
	if closing {
		return nil, ErrClosing
	}
	h := &calendarHandle{service: s, boot: boot}
	a := &CalendarActor{h: h, state: state, inFlight: map[string]bool{}}
	addr, err := c.runtime.Spawn(a)
	if err != nil {
		return nil, err
	}
	h.addr = addr
	a.publish()
	// Re-evaluate with the current offline data before relying on the saved next
	// time. A revised calendar can make an earlier nominal date valid.
	initial := *h.projection.Load()
	initial.next = time.Now().UTC()
	h.projection.Store(&initial)
	c.mu.Lock()
	c.handles[s.info.RoomIdentity] = h
	c.mu.Unlock()
	return h, nil
}
func (s *service) calendarOwner(create bool) (*calendarHandle, error) {
	if !s.isDirect || !s.unifiedInputs {
		return nil, calendar.Fail("not_authorized", "room", "calendar is available in direct conversations with stream inputs")
	}
	if s.closing.Load() || s.restarting.Load() {
		return nil, ErrClosing
	}
	s.calendarMu.Lock()
	defer s.calendarMu.Unlock()
	if s.calendar != nil {
		return s.calendar, nil
	}
	if !create {
		return nil, nil
	}
	h, err := s.calendars.attach(s)
	if err == nil {
		s.calendar = h
	}
	return h, err
}
func (s *service) calendarCurrent() *calendarHandle {
	s.calendarMu.Lock()
	defer s.calendarMu.Unlock()
	return s.calendar
}
func (c *CalendarService) retire(ctx context.Context, h *calendarHandle) error {
	if h == nil {
		return nil
	}
	h.retired.Store(true)
	if _, err := h.addr.Call[struct{}](ctx, calendarQuiesce{}); err != nil {
		return err
	}
	// Terminal records are already persisted by MemberAgent; reconcile again
	// after all workers settled even if a terminal notification was lost.
	_, err := h.call(ctx, true, func(ctx context.Context, state *calendar.State) (any, error) {
		h.service.reconcileCalendar(ctx, state)
		return nil, nil
	})
	if err != nil && !errors.Is(err, ErrCommitUnknown) {
		return err
	}
	// A frozen store cannot save final observations. All workers and members
	// have joined; stop the idle owner and retain disk evidence for safe reopen.
	if _, err := h.addr.Call[struct{}](ctx, calendarQuiesce{}); err != nil {
		return err
	}
	_ = h.addr.Stop(nil)
	exit, err := h.addr.Wait(ctx)
	if err != nil {
		return err
	}
	if exit.Reason != nil {
		return exit.Reason
	}
	c.mu.Lock()
	if c.handles[h.service.info.RoomIdentity] == h {
		delete(c.handles, h.service.info.RoomIdentity)
	}
	c.mu.Unlock()
	return nil
}
func (c *CalendarService) close(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	select {
	case <-c.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	c.mu.Lock()
	remaining := len(c.handles) + len(c.loading)
	c.mu.Unlock()
	if remaining != 0 {
		return errors.New("calendar actors still own room stores")
	}
	return c.runtime.Close(ctx)
}
func (s *service) calendarBinding(id MemberIdentity, turn uint64) (calendar.Binding, error) {
	b, err := s.executionBindingForTurn(id, turn)
	return calendar.Binding(b), err
}
func (s *service) additionalWorkReady(id MemberIdentity) bool {
	h := s.calendarCurrent()
	if h == nil || h.retired.Load() || s.closing.Load() || s.restarting.Load() {
		return false
	}
	p := h.projection.Load()
	if p == nil || p.err != nil {
		return false
	}
	// Unconfirmed input pages and recovery are execution barriers. Source-local
	// permission failures alone need not prohibit an unrelated scheduled task.
	if raw, ok := s.store.inputHandles.Load(id.MembershipID); ok {
		input := raw.(*platformInputStorage)
		v := input.projection.Load()
		if v == nil || v.err != nil || v.status.Recovery != nil || v.status.Blocked != "" && v.status.Blocked != "source_admission_unverified" {
			return false
		}
		for _, attempt := range v.state.Attempts {
			if attempt.Phase != "settled" && attempt.Phase != "failed_before_output" {
				return false
			}
		}
	}
	return len(p.state.Pending(id.MembershipID, time.Now().UTC())) > 0
}
func (s *service) supplyCalendar(ctx context.Context, id MemberIdentity, turn uint64) ([]calendarSummary, error) {
	h := s.calendarCurrent()
	if h == nil {
		return nil, nil
	}
	b, err := s.calendarBinding(id, turn)
	if err != nil {
		return nil, err
	}
	v, err := h.call(ctx, false, func(_ context.Context, state *calendar.State) (any, error) {
		runs := state.Supply(id.MembershipID, b, time.Now().UTC())
		out := []calendarSummary{}
		for _, r := range runs {
			out = append(out, calendarSummary{r.ID, r.Task.Definition.Title, r.DueAt})
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]calendarSummary), nil
}

type calendarSummary struct {
	RunID string    `json:"run_id"`
	Title string    `json:"title"`
	DueAt time.Time `json:"due_at"`
}

func (s *service) calendarTurnFinished(id MemberIdentity, binding turnExecution, record TurnRecord, settled bool) {
	h := s.calendarCurrent()
	if h == nil {
		return
	}
	_, _ = h.call(context.Background(), true, func(_ context.Context, state *calendar.State) (any, error) {
		state.TurnFinished(id.MembershipID, calendar.Binding(binding), record.Status, settled, time.Now().UTC())
		return nil, nil
	})
}
func (s *service) reconcileCalendar(ctx context.Context, state *calendar.State) {
	now := time.Now().UTC()
	for _, r := range state.Runs {
		if r.Phase == "publishing" {
			continue
		}
		if r.Status != "running" && r.Status != "queued" {
			continue
		}
		if r.NextCheckAt.After(now) {
			continue
		}
		bindings := []calendar.Binding{}
		if r.Binding.Turn != 0 {
			bindings = append(bindings, r.Binding)
		}
		for _, v := range r.Supplies {
			if !v.Checked {
				bindings = append(bindings, v.Binding)
			}
		}
		for _, b := range bindings {
			id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: r.Task.AgentID, MembershipID: r.Task.MembershipID}
			agent, err := s.store.LoadAgent(id)
			if err != nil {
				continue
			}
			if agent.Turn != nil && agent.Turn.Number == b.Turn && agent.Turn.Status != "running" {
				state.TurnFinished(id.MembershipID, b, agent.Turn.Status, true, now)
			} else if agent.Turn == nil || agent.Turn.Number > b.Turn || s.target(id.MembershipID).IsZero() {
				state.TurnFinished(id.MembershipID, b, "unverified", false, now)
			}
		}
		if r.Status == "running" && r.Phase != "publishing" && now.Sub(r.StartedAt) >= 15*time.Minute {
			r.CancelRequested = true
			r.NextCheckAt = time.Time{}
			r.Reason = "timeout"
		}
		if r.Status == "running" && !r.CancelRequested {
			r.NextCheckAt = now.Add(5 * time.Second)
		} else if r.Status == "queued" {
			outstanding := false
			for _, supplied := range r.Supplies {
				outstanding = outstanding || !supplied.Checked
			}
			if outstanding {
				r.NextCheckAt = now.Add(5 * time.Second)
			} else if !r.NextCheckAt.After(now) {
				r.NextCheckAt = time.Time{}
			}
		}
	}
}
func (s *service) pauseCalendar(ctx context.Context) error {
	h := s.calendarCurrent()
	if h == nil {
		return nil
	}
	_, err := h.call(ctx, true, func(_ context.Context, state *calendar.State) (any, error) {
		for _, t := range state.Tasks {
			if t.State == "active" {
				t.State = "paused"
				t.Version++
				t.UpdatedAt = time.Now().UTC()
			}
		}
		for _, r := range state.Runs {
			if r.Status == "queued" {
				r.Status = "cancelled"
				r.Settled = true
				r.Version++
			}
			if r.Status == "running" && r.Phase != "publishing" {
				r.CancelRequested = true
				r.Reason = "discussion_restart"
				r.Version++
			}
		}
		return nil, nil
	})
	return err
}
