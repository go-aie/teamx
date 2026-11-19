package room

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/go-aie/teamx/internal/calendar"
)

type calendarQuery struct {
	TaskID  string    `json:"task_id,omitempty"`
	RunID   string    `json:"run_id,omitempty"`
	From    time.Time `json:"from,omitzero"`
	To      time.Time `json:"to,omitzero"`
	State   string    `json:"state,omitempty"`
	Cursor  string    `json:"cursor,omitempty"`
	History bool      `json:"history,omitempty"`
	Result  bool      `json:"result,omitempty"`
}
type calendarFinish struct {
	RunID    string          `json:"run_id"`
	Outcome  string          `json:"outcome"`
	Report   string          `json:"report"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
}
type calendarMessageLink struct {
	TaskID     string `json:"task_id"`
	MessageSeq uint64 `json:"message_seq"`
	RunID      string `json:"run_id,omitempty"`
}
type calendarManagementTask struct {
	*calendar.Task
	FirstAt              time.Time               `json:"first_at,omitzero"`
	History              []calendarManagementRun `json:"history"`
	NextCursor           string                  `json:"next_cursor,omitempty"`
	HistoryRetentionDays int                     `json:"history_retention_days"`
}
type calendarManagementRun struct {
	calendarRunView
	TimeZone string `json:"time_zone"`
}
type calendarList struct {
	MessageLinks         []calendarMessageLink   `json:"message_links"`
	TimeZone             string                  `json:"time_zone"`
	Occurrences          []calendarOccurrence    `json:"occurrences"`
	Days                 []calendar.WorkDay      `json:"days,omitempty"`
	WorkCalendars        []calendar.WorkCoverage `json:"work_calendars"`
	CoverageIncomplete   bool                    `json:"coverage_incomplete"`
	Revision             uint64                  `json:"revision"`
	Tasks                []*calendar.Task        `json:"tasks"`
	Runs                 []*calendar.Run         `json:"runs"`
	NextCursor           string                  `json:"next_cursor,omitempty"`
	Complete             bool                    `json:"complete"`
	HistoryRetentionDays int                     `json:"history_retention_days"`
}

func (s *service) calendarCall(ctx context.Context, op string, data json.RawMessage, caller Caller, turn uint64, bound *calendar.Binding) (any, error) {
	return s.calendarCallView(ctx, op, data, caller, turn, bound, caller.Kind == "agent")
}

func (s *service) calendarCallView(ctx context.Context, op string, data json.RawMessage, caller Caller, turn uint64, bound *calendar.Binding, businessView bool) (any, error) {
	s.calendarRunning.Store(true)
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	h, err := s.calendarOwner(op == "update")
	if err != nil {
		return nil, err
	}
	roster, err := s.room.Call[MemberList](ctx, ListMembers{s.info.RoomIdentity})
	if err != nil {
		return nil, err
	}
	if !roster.IsDirect || len(roster.Members) != 1 {
		return nil, ErrIdentity
	}
	executor := roster.Members[0]
	id := MemberIdentity{s.info.RoomIdentity, executor.AgentID, executor.MembershipID}
	if caller.Kind == "agent" && (caller.ID != id.AgentID || caller.MembershipID != id.MembershipID) {
		return nil, ErrIdentity
	}
	if caller.Kind != "agent" && caller.Kind != "human" {
		return nil, ErrIdentity
	}
	if op == "list" || op == "get" {
		var q calendarQuery
		if err := decodeRequestJSON(string(data), &q); err != nil {
			return nil, err
		}
		state := calendar.New(s.info.RoomID, s.info.RoomInstance)
		if h != nil {
			p := h.projection.Load()
			if p.err != nil {
				return nil, p.err
			}
			state = p.state.Clone()
		}
		if businessView || caller.Kind == "agent" {
			return calendarAgentRead(state, op, q)
		}
		if op == "get" {
			if (q.TaskID == "") == (q.RunID == "") {
				return nil, ErrRequest
			}
			if q.TaskID != "" {
				t := state.Tasks[q.TaskID]
				if t == nil {
					return nil, ErrRequest
				}
				if q.History {
					history, err := calendarAgentRead(state, op, q)
					if err != nil {
						return nil, err
					}
					v := history.(calendarTaskView)
					rows := make([]calendarManagementRun, 0, len(v.History))
					for _, run := range v.History {
						rows = append(rows, calendarManagementRun{run, run.Definition.Schedule.TimeZone})
					}
					return calendarManagementTask{t, calendar.FirstScheduledAt(t), rows, v.NextCursor, v.HistoryRetentionDays}, nil
				}
				if q.Cursor != "" || q.Result || !q.From.IsZero() || !q.To.IsZero() || q.State != "" {
					return nil, ErrRequest
				}
				return t, nil
			}
			if q.History || q.Cursor != "" || !q.From.IsZero() || !q.To.IsZero() || q.State != "" {
				return nil, ErrRequest
			}
			r := state.Runs[q.RunID]
			if r == nil {
				return nil, ErrRequest
			}
			return r, nil
		}
		return calendarManagementList(state, q, s.settings.snapshot().TimeZone)
	}
	if h == nil {
		return nil, ErrRequest
	}
	var binding calendar.Binding
	var release func()
	if caller.Kind == "agent" {
		// All Agent mutations are tied to the accepted execution and joined before
		// its terminal checkpoint. A Session credential alone cannot claim work.
		if bound != nil {
			turn = bound.Turn
		}
		release, err = s.store.beginMCPRead(id, turn)
		if err != nil {
			return nil, err
		}
		defer release()
		binding, err = s.calendarBinding(id, turn)
		if err != nil {
			return nil, err
		}
		if bound != nil && binding != *bound {
			return nil, ErrIdentity
		}
	}
	if op == "update" {
		var q calendar.Request
		if err := decodeRequestJSON(string(data), &q); err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(data, &fields)
		if q.Action != "create" && (fields["expected_version"] == nil || string(fields["expected_version"]) == "null" || q.ExpectedVersion == 0) {
			return nil, calendar.Fail("invalid_request", "expected_version", "existing resource changes require its version")
		}
		if q.HumanSource.Text != "" || q.HumanSource.Operator != "" {
			return nil, invalidField("human_source", "only message_seq may be submitted")
		}
		value, err := h.call(ctx, false, func(ctx context.Context, state *calendar.State) (any, error) {
			if v, ok, err := state.Reuse(q); ok {
				return v, err
			}
			source := calendar.Source{}
			if caller.Kind == "agent" {
				saved, err := s.store.LoadRoom()
				if err != nil {
					return nil, err
				}
				for _, m := range saved.Messages {
					if m.Seq == q.HumanSource.MessageSeq && m.Author.Kind == "human" && m.Source == nil && m.Event == nil {
						source = calendar.Source{MessageSeq: m.Seq, Operator: m.Author.ID, Text: m.Text}
						break
					}
				}
				if source.MessageSeq == 0 {
					return nil, calendar.Fail("not_authorized", "human_source.message_seq", "requires a real human instruction in this direct conversation")
				}
			} else {
				if q.HumanSource.MessageSeq != 0 {
					return nil, ErrRequest
				}
				text, _ := json.Marshal(q)
				source = calendar.Source{Operator: caller.ID, Text: string(text)}
			}
			return state.Update(q, source, id.AgentID, id.MembershipID, s.settings.snapshot().TimeZone, time.Now().UTC())
		})
		if err != nil {
			return nil, err
		}
		if businessView || caller.Kind == "agent" {
			return calendarMutationBusinessView(value.(calendar.Result)), nil
		}
		return value, nil
	}
	if caller.Kind != "agent" || binding.Turn == 0 {
		return nil, ErrIdentity
	}
	switch op {
	case "claim":
		var q struct {
			RunID string `json:"run_id"`
		}
		if err := decodeRequestJSON(string(data), &q); err != nil {
			return nil, err
		}
		return h.call(ctx, false, func(_ context.Context, state *calendar.State) (any, error) {
			r, err := state.Claim(q.RunID, binding, id.MembershipID, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			d := r.Task.Definition
			return calendarClaimView{RunID: r.ID, Status: calendarBusinessStatus(r), Title: d.Title, Instruction: d.Instruction, Scope: d.Scope, DueAt: r.DueAt, TimeZone: d.Schedule.TimeZone, Misfire: d.Misfire}, nil
		})
	case "finish":
		var q calendarFinish
		if err := decodeRequestJSON(string(data), &q); err != nil {
			return nil, err
		}
		if err := validateCalendarEvidence(q.Evidence); err != nil {
			return nil, err
		}
		return h.call(ctx, false, func(_ context.Context, state *calendar.State) (any, error) {
			r, err := state.Finish(q.RunID, binding, id.MembershipID, q.Outcome, q.Report, q.Evidence)
			if err != nil {
				return nil, err
			}
			return calendarFinishView{RunID: r.ID, Status: calendarBusinessStatus(r), Reason: r.Reason, ReportSeq: r.Publication.Seq}, nil
		})
	}
	return nil, ErrRequest
}

// CLI carries the same private bridge credential as MCP, separately from its
// explicit turn number and business JSON. Session credentials cannot guess a
// current execution or reopen a canceled turn.
func (s *service) calendarHTTPCall(ctx context.Context, op string, req apiRequest) (any, error) {
	var bound *calendar.Binding
	if req.Caller.Kind == "agent" && op != "list" && op != "get" {
		if s.access == nil || req.CalendarExecution == "" {
			return nil, ErrIdentity
		}
		s.access.mu.Lock()
		input := s.access.inputBindings[sha256.Sum256([]byte(req.CalendarExecution))]
		s.access.mu.Unlock()
		if input == nil || input.service != s || agentCallerIdentity(input.storage.id) != req.Caller || input.execution.Turn != req.TurnNumber {
			return nil, ErrIdentity
		}
		if err := input.check(ctx); err != nil {
			return nil, err
		}
		value := calendar.Binding{Generation: input.execution.Generation, Turn: input.execution.Turn}
		bound = &value
	}
	return s.calendarCallView(ctx, op, req.Calendar, req.Caller, req.TurnNumber, bound, req.CalendarBusinessView)
}
