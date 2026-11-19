package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/calendar"
)

func calendarAgentFixture(t *testing.T) (*service, *inputSession, func(string) string, string) {
	t.Helper()
	svc := calendarFixture(t, t.TempDir(), newControlledBackend())
	id := MemberIdentity{svc.info.RoomIdentity, "A", "generation-A"}
	b := calendarRunning(t, svc, 1)
	p, err := svc.store.openPlatformInputStorage(id, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	input, err := newInputSession(t.Context(), svc, p, b.Generation, 1, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	server := browserTestServer(t, svc)
	token, revoke, err := svc.access.issueInputBinding(input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revoke)
	execution, revokeExecution, err := svc.access.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: agentCallerIdentity(id)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revokeExecution)
	hidden, _ := json.Marshal(map[string]string{"token": token})
	values := map[string]string{"TEAMX_EXECUTION_TOKEN": execution, "TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": id.RoomID, "TEAMX_ROOM_INSTANCE": id.RoomInstance, "TEAMX_SERVICE_EPOCH": svc.info.ServiceEpoch, "TEAMX_AGENT_ID": id.AgentID, "TEAMX_MEMBERSHIP_ID": id.MembershipID, "DSH_TEAMX_INPUT_BINDING": string(hidden)}
	return svc, input, func(k string) string { return values[k] }, token
}
func calendarAgentCLICall(t *testing.T, env func(string) string, op string, q any) (map[string]any, error) {
	t.Helper()
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = runCLIWithInput(t.Context(), []string{"calendar", op, "--turn", "1", "--request-file", "-"}, bytes.NewReader(data), &out, io.Discard, env)
	if err != nil {
		return nil, err
	}
	assertCalendarBusinessOnly(t, out.String())
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(out.String(), err)
	}
	return v, nil
}
func calendarAgentCLI(t *testing.T, env func(string) string, op string, q any) map[string]any {
	t.Helper()
	v, err := calendarAgentCLICall(t, env, op, q)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func assertCalendarBusinessOnly(t *testing.T, data string) {
	t.Helper()
	for _, field := range []string{"generation", "turn", "binding", "membership_id", "agent_id", "attempt", "definition_version", "supplies", "event_delivered", "continue_count", "publication", "client_message_id", "phase", "creation_source", "authorization", "deadline"} {
		if strings.Contains(data, `"`+field+`":`) {
			t.Fatal("internal field in Agent output", field, data)
		}
	}
}
func TestCalendarAgentCLICreateClaimFinishAndRecovery(t *testing.T) {
	s, _, env, _ := calendarAgentFixture(t)
	due := time.Now().In(s.settings.snapshot().location).Add(15 * time.Minute).Truncate(time.Minute)
	// No pre-read, expected_version or explicit product defaults.
	q := map[string]any{"action": "create", "operation_id": "compact-create", "human_source": map[string]any{"message_seq": 1}, "definition": map[string]any{"title": "提醒", "instruction": "CALENDAR_CONTRACT_REPORT", "scope": map[string]string{"workspace": "verified"}, "schedule": map[string]string{"frequency": "once", "start_date": due.Format("2006-01-02"), "local_time": due.Format("15:04")}}}
	created := calendarAgentCLI(t, env, "update", q)
	taskID := created["task_id"].(string)
	if created["version"] != float64(1) || created["changed"] != nil || created["operation_reused"] != nil || !strings.Contains(created["rule_summary"].(string), s.settings.snapshot().TimeZone) || !strings.Contains(created["rule_summary"].(string), "latest_within_one_hour") {
		t.Fatal(created)
	}
	task := s.calendarCurrent().projection.Load().state.Tasks[taskID]
	original := task.Definition
	// Simulate a lost reply. Change the system zone before replay; defaults must
	// remain those committed with the original operation, without another get.
	oldSettings := s.settings.committed.Load()
	changedSettings := *oldSettings
	changedSettings.TimeZone = "America/New_York"
	changedSettings.location, _ = time.LoadLocation(changedSettings.TimeZone)
	s.settings.committed.Store(&changedSettings)
	replay := calendarAgentCLI(t, env, "update", q)
	s.settings.committed.Store(oldSettings)
	if replay["task_id"] != taskID || replay["operation_reused"] != true || replay["rule_summary"] != created["rule_summary"] || len(s.calendarCurrent().projection.Load().state.Tasks) != 1 {
		t.Fatal(replay)
	}
	taskCopy := *task
	run := calendarRunNow(t, s, taskCopy)
	claimed := calendarAgentCLI(t, env, "claim", map[string]any{"run_id": run.ID})
	if claimed["instruction"] != original.Instruction || claimed["scope"].(map[string]any)["workspace"] != "verified" || claimed["time_zone"] != original.Schedule.TimeZone || claimed["version"] != nil {
		t.Fatal(claimed)
	}
	// The initial finish response is formed from its committed publication intent.
	finish := map[string]any{"run_id": run.ID, "outcome": "succeeded", "report": "CALENDAR_CONTRACT_REPORT"}
	accepted := calendarAgentCLI(t, env, "finish", finish)
	if accepted["status"] != "publishing" || accepted["report_seq"] != nil || len(accepted) != 2 {
		t.Fatal(accepted)
	}
	calendarAgentCLI(t, env, "finish", finish)
	details := calendarAgentCLI(t, env, "get", map[string]any{"run_id": run.ID})
	definition := details["definition"].(map[string]any)
	if details["time_zone"] != nil || definition["schedule"].(map[string]any)["time_zone"] != original.Schedule.TimeZone {
		t.Fatal("run view lost or repeated the frozen time zone", details)
	}
	if (details["status"] != "publishing" && details["status"] != "succeeded") || details["report"] != nil {
		t.Fatal(details)
	}
	report := calendarAgentCLI(t, env, "get", map[string]any{"run_id": run.ID, "result": true})
	if report["report"] != "CALENDAR_CONTRACT_REPORT" {
		t.Fatal(report)
	}
	finish["report"] = "different"
	if _, err := calendarAgentCLICall(t, env, "finish", finish); err == nil || !strings.Contains(err.Error(), "operation_conflict") {
		t.Fatal("conflicting report accepted", err)
	}
	_, err := s.calendarCurrent().call(t.Context(), true, func(context.Context, *calendar.State) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	saved := calendarAwait(t, s, func(state calendar.State) bool { return state.Runs[run.ID].Publication.Status == "published" })
	// Read the published result through the dedicated CLI flags.
	var out bytes.Buffer
	if err := runCalendarCLI(t.Context(), []string{"get", "--run-id", run.ID, "--result"}, nil, &out, io.Discard, env); err != nil {
		t.Fatal(err)
	}
	assertCalendarBusinessOnly(t, out.String())
	var cli calendarRunView
	if json.Unmarshal(out.Bytes(), &cli) != nil || cli.Status != "succeeded" || cli.ReportSeq != saved.Runs[run.ID].Publication.Seq || cli.Report != "CALENDAR_CONTRACT_REPORT" {
		t.Fatal(out.String())
	}
	finish["report"] = "CALENDAR_CONTRACT_REPORT"
	published := calendarAgentCLI(t, env, "finish", finish)
	if published["status"] != "succeeded" || published["report_seq"] == nil || len(published) != 3 {
		t.Fatal(published)
	}
	room, err := s.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range room.Messages {
		if m.Text == "CALENDAR_CONTRACT_REPORT" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("duplicate public report", count)
	}
}

func TestCalendarAgentListsTasksAndBoundsHistory(t *testing.T) {
	state := calendar.New("dm", "instance")
	now := time.Now().UTC()
	for i := range 63 {
		id := fmt.Sprintf("task_%03d", i)
		d := calendarDefinition(now.AddDate(0, 0, 20+i))
		kind := []string{"active", "paused", "needs_info"}[i%3]
		state.Tasks[id] = &calendar.Task{ID: id, Version: 1, Definition: d, State: kind, CreatedAt: now, NextAt: now.AddDate(0, 0, 20+i)}
	}
	list := func(q calendarQuery) map[string]any {
		t.Helper()
		v, err := calendarAgentRead(state, "list", q)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		assertCalendarBusinessOnly(t, string(b))
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return out
	}
	first := list(calendarQuery{})
	tasks := first["tasks"].([]any)
	if len(tasks) != 50 || tasks[0].(map[string]any)["definition"] != nil || tasks[0].(map[string]any)["task_id"] != "task_000" {
		t.Fatal(first)
	}
	cursor := first["next_cursor"].(string)
	second := list(calendarQuery{Cursor: cursor})
	if len(second["tasks"].([]any)) != 13 {
		t.Fatal(second)
	}
	if _, err := calendarAgentRead(state, "list", calendarQuery{Cursor: cursor, State: "active"}); err == nil {
		t.Fatal("cursor accepted altered filters")
	}
	state.Revision++
	if _, err := calendarAgentRead(state, "list", calendarQuery{Cursor: cursor}); err == nil {
		t.Fatal("cursor accepted changed snapshot")
	}
	from, _ := calendar.Once(state.Tasks["task_000"].Definition.Schedule)
	ranged := list(calendarQuery{From: from, To: from.Add(time.Minute)})
	if len(ranged["tasks"].([]any)) != 1 {
		t.Fatal(ranged)
	}
	state.Tasks["task_001"].State = "completed"
	if len(list(calendarQuery{State: "completed"})["tasks"].([]any)) != 1 {
		t.Fatal("explicit completed filter")
	}
	for i := range 63 {
		id := fmt.Sprintf("run_%03d", i)
		state.Runs[id] = &calendar.Run{ID: id, Version: 2, Task: *state.Tasks["task_000"], DueAt: now.Add(-time.Duration(i) * time.Hour), Status: "succeeded", Report: "private-unless-requested", Publication: calendar.Publication{Status: "published", Seq: 123}}
	}
	v, err := calendarAgentRead(state, "get", calendarQuery{TaskID: "task_000"})
	if err != nil {
		t.Fatal(err)
	}
	plain := v.(calendarTaskView)
	if plain.History != nil || plain.Definition == nil || plain.Title != "" || plain.RuleSummary != "" {
		t.Fatal(plain)
	}
	v, err = calendarAgentRead(state, "get", calendarQuery{TaskID: "task_000", History: true})
	if err != nil {
		t.Fatal(err)
	}
	history := v.(calendarTaskView)
	if len(history.History) != 50 || history.NextCursor == "" || history.History[0].Report != "" {
		t.Fatal(history)
	}
	v, err = calendarAgentRead(state, "get", calendarQuery{TaskID: "task_000", History: true, Cursor: history.NextCursor})
	if err != nil || len(v.(calendarTaskView).History) != 13 {
		t.Fatal(v, err)
	}
	if _, err = calendarAgentRead(state, "get", calendarQuery{TaskID: "task_000", Cursor: history.NextCursor}); err == nil {
		t.Fatal("history cursor without history")
	}
}

func TestCalendarAgentCLIMutationsAndVersionConflict(t *testing.T) {
	s, _, env, _ := calendarAgentFixture(t)
	due := time.Now().UTC().Add(25 * time.Minute).Truncate(time.Minute)
	q := map[string]any{"action": "create", "operation_id": "cli-create", "human_source": map[string]any{"message_seq": 1}, "definition": calendarDefinition(due)}
	call := func(q any) (map[string]any, error) {
		b, _ := json.Marshal(q)
		var out bytes.Buffer
		err := runCalendarCLI(t.Context(), []string{"update", "--turn", "1", "--request-file", "-"}, bytes.NewReader(b), &out, io.Discard, env)
		assertCalendarBusinessOnly(t, out.String())
		var v map[string]any
		_ = json.Unmarshal(out.Bytes(), &v)
		return v, err
	}
	created, err := call(q)
	if err != nil {
		t.Fatal(err)
	}
	id := created["task_id"].(string)
	edit := map[string]any{"action": "edit", "operation_id": "edit-title", "human_source": map[string]any{"message_seq": 1}, "task_id": id, "expected_version": 1, "changes": map[string]any{"title": "新标题", "scope": nil}}
	changed, err := call(edit)
	if err != nil || changed["version"] != float64(2) {
		t.Fatal(changed, err)
	}
	task := s.calendarCurrent().projection.Load().state.Tasks[id]
	if task.Definition.Title != "新标题" || len(task.Definition.Scope) != 0 || task.Definition.Instruction == "" || !task.NextAt.Equal(due) {
		t.Fatal(task)
	}
	edit["operation_id"] = "stale-edit"
	_, err = call(edit)
	if err == nil || !strings.Contains(err.Error(), id) || !strings.Contains(err.Error(), "actual_version=2") {
		t.Fatal("lost conflict target/version", err)
	}
	edit["expected_version"] = 2
	delete(edit, "expected_version")
	if _, err = call(edit); err == nil {
		t.Fatal("missing existing version accepted")
	}
	// Direct claim is revoked after cancellation, including same execution replay.
	run := calendarRunNow(t, s, *task)
	calendarAgentCLI(t, env, "claim", map[string]any{"run_id": run.ID})
	// Cancel the claimed snapshot; an old repeated claim must lose eligibility.
	_, err = s.calendarCurrent().call(t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) {
		return state.Update(calendar.Request{Action: "cancel", OperationID: "cancel", TaskID: id, ExpectedVersion: 2}, calendar.Source{}, "A", "generation-A", "UTC", time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := calendarAgentCLICall(t, env, "claim", map[string]any{"run_id": run.ID}); err == nil || !strings.Contains(err.Error(), "task_inactive") {
		t.Fatal("cancelled claim accepted", err)
	}
}

func TestCalendarAgentEvidenceBounds(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"text":"duplicate report"}`, `{"source_refs":["guessed"]}`, `{"source_refs":[]}`} {
		if validateCalendarEvidence(json.RawMessage(raw)) == nil {
			t.Fatal("accepted evidence", raw)
		}
	}
	ref := "src_" + strings.Repeat("a", 64)
	good, _ := json.Marshal(map[string]any{"source_refs": []string{ref}})
	if err := validateCalendarEvidence(good); err != nil {
		t.Fatal(err)
	}
	refs := []string{}
	for range 17 {
		refs = append(refs, ref)
	}
	big, _ := json.Marshal(map[string]any{"source_refs": refs})
	if validateCalendarEvidence(big) == nil {
		t.Fatal("unbounded evidence")
	}
}

func TestCalendarCLIHelpWithoutConnection(t *testing.T) {
	for _, op := range []string{"", "list", "get", "update", "claim", "finish"} {
		t.Run(op, func(t *testing.T) {
			args := []string{"calendar"}
			if op != "" {
				args = append(args, op)
			}
			args = append(args, "--help")
			var out bytes.Buffer
			if err := runCLIWithInput(t.Context(), args, nil, &out, io.Discard, func(string) string { return "" }); err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"--turn", "--request-file", "human_source", "expected_version", "changes", "business_days", "operation_id", "publishing", "report_seq"} {
				if !strings.Contains(out.String(), required) {
					t.Fatal("missing Calendar usage", required)
				}
			}
		})
	}
}

func TestCalendarAgentCLIRejectsInvalidRequestsAndStaleExecution(t *testing.T) {
	s, _, env, _ := calendarAgentFixture(t)
	d := calendarDefinition(time.Now().Add(20 * time.Minute))
	created := calendarAgentCLI(t, env, "update", map[string]any{"action": "create", "operation_id": "cli-validation", "human_source": map[string]int{"message_seq": 1}, "definition": d})
	taskID := created["task_id"].(string)
	original := s.calendarCurrent().projection.Load().state.Tasks[taskID].Definition
	for _, fields := range []map[string]any{
		{"action": "edit", "changes": map[string]any{"title": "missing version"}},
		{"action": "edit", "expected_version": 1, "definition": d},
		{"action": "pause", "expected_version": 1, "local_date": "2099-12-20"},
		{"action": "skip_once", "expected_version": 1},
		{"action": "pause", "expected_version": 1, "unexpected": true},
	} {
		fields["operation_id"] = "invalid"
		fields["task_id"] = taskID
		fields["human_source"] = map[string]int{"message_seq": 1}
		if _, err := calendarAgentCLICall(t, env, "update", fields); err == nil {
			t.Fatal("invalid CLI update accepted", fields)
		}
	}
	task := s.calendarCurrent().projection.Load().state.Tasks[taskID]
	before, _ := json.Marshal(original)
	after, _ := json.Marshal(task.Definition)
	if task.Version != 1 || task.State != "active" || !bytes.Equal(before, after) {
		t.Fatal("invalid requests changed task", task)
	}
	run := calendarRunNow(t, s, *task)
	for _, tc := range []struct {
		name, turn string
		binding    bool
	}{
		{"missing turn", "0", true},
		{"wrong turn", "2", true},
		{"missing binding", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			boundEnv := func(k string) string {
				if k == "DSH_TEAMX_INPUT_BINDING" && !tc.binding {
					return ""
				}
				return env(k)
			}
			if err := runCLIWithInput(t.Context(), []string{"calendar", "claim", "--turn", tc.turn, "--run-id", run.ID}, nil, io.Discard, io.Discard, boundEnv); err == nil {
				t.Fatal("unbound claim accepted")
			}
		})
	}
	if state := s.calendarCurrent().projection.Load().state; state.Runs[run.ID].Status != "queued" {
		t.Fatal("invalid claim changed run", state.Runs[run.ID])
	}
	id := MemberIdentity{s.info.RoomIdentity, "A", "generation-A"}
	agent, err := s.store.LoadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	agent.Turn.Status = "completed"
	if err := s.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	if _, err := calendarAgentCLICall(t, env, "claim", map[string]any{"run_id": run.ID}); err == nil {
		t.Fatal("terminal execution claimed work")
	}
}

func TestCalendarAgentCLIDirectClaimAndFinish(t *testing.T) {
	s, _, env, _ := calendarAgentFixture(t)
	call := func(op string, q any) map[string]any {
		t.Helper()
		data, _ := json.Marshal(q)
		var out bytes.Buffer
		if err := runCalendarCLI(t.Context(), []string{op, "--turn", "1", "--request-file", "-"}, bytes.NewReader(data), &out, io.Discard, env); err != nil {
			t.Fatal(err)
		}
		assertCalendarBusinessOnly(t, out.String())
		var v map[string]any
		if err := json.Unmarshal(out.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	d := calendarDefinition(time.Now().Add(10 * time.Minute))
	created := call("update", map[string]any{"action": "create", "operation_id": "cli-direct", "human_source": map[string]int{"message_seq": 1}, "definition": d})
	task := s.calendarCurrent().projection.Load().state.Tasks[created["task_id"].(string)]
	run := calendarRunNow(t, s, *task)
	claimed := call("claim", map[string]string{"run_id": run.ID})
	if claimed["instruction"] != d.Instruction || claimed["scope"].(map[string]any)["workspace"] != "verified" {
		t.Fatal(claimed)
	}
	finished := call("finish", map[string]string{"run_id": run.ID, "outcome": "blocked", "report": "Scope unavailable; no business write was attempted."})
	if finished["status"] != "publishing" || len(finished) != 2 {
		t.Fatal(finished)
	}
	calendarAwait(t, s, func(state calendar.State) bool {
		return state.Runs[run.ID].Status == "blocked" && state.Runs[run.ID].Publication.Seq != 0
	})
	receipt := call("get", map[string]string{"run_id": run.ID})
	if receipt["status"] != "blocked" || receipt["report_seq"] == nil || receipt["report"] != nil {
		t.Fatal(receipt)
	}
}
