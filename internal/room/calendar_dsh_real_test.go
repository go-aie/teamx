//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/calendar"
)

// Isolated real DSH/model + production Room, scheduler, Calendar CLI and publication.
// Only local conversation writes are allowed; there is no platform dependency.
func TestCalendarRealDSH(t *testing.T)          { testCalendarRealDSH(t, false) }
func TestCalendarRecurringRealDSH(t *testing.T) { testCalendarRealDSH(t, true) }
func testCalendarRealDSH(t *testing.T, recurring bool) {
	flag := "TEAMX_CALENDAR_DSH_REAL"
	if recurring {
		flag = "TEAMX_CALENDAR_RECURRING_DSH_REAL"
	}
	if os.Getenv(flag) != "1" {
		t.Skip("opt-in real model acceptance")
	}
	cfg, entry, credential := inputAcceptanceModel(t)
	output := os.Getenv("TEAMX_CALENDAR_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("fresh absolute output directory required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(output, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(workspace, "teamx")
	if b, err := exec.CommandContext(t.Context(), "go", "build", "-o", cli, "../../cmd/teamx").CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
	due := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute)
	state := historyRoom(1, "请在 "+due.Format("2006-01-02 15:04")+"（UTC）执行一次提醒，内容为 CALENDAR_REAL_DSH_OK，直接回复本私信；现在请创建并返回保存回执。不要访问平台或执行外部动作。")
	if recurring {
		state = historyRoom(1, "请安排一个重复提醒：从现在开始，每天 "+due.Format("15:04")+"（UTC），只在中国大陆法定工作日执行，含调休补班。内容为 CALENDAR_REAL_DSH_OK，直接回复本私信。现在创建任务（重复规则的开始日期交给服务默认），向我解释实际保存的重复规则、固定时区、含补班口径、下次时间和漏跑规则。不要访问平台或执行外部动作。")
	}
	state.IsDirect = true
	state.InputLog = &roomLogRoot{Version: 1}
	dir := filepath.Join(output, "room")
	store, err := OpenStore(dir, state.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	access, _, _ := accessFixture(t)
	access.disabled = true
	bindFixtureAccessSubject(access, humanCaller.ID)
	var svc *service
	var traceMu sync.Mutex
	var calls []calendarAcceptanceCall
	handler := access.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { svc.ServeHTTP(w, r) }))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture only calendar business arguments/results, never headers or tokens.
		var call calendarAcceptanceCall
		if strings.HasPrefix(r.URL.Path, "/v1/calendar/") {
			data, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(data))
			var req apiRequest
			if json.Unmarshal(data, &req) == nil {
				call.Tool = "calendar " + strings.TrimPrefix(r.URL.Path, "/v1/calendar/")
				call.Arguments = req.Calendar
			}
		}
		if call.Tool == "" {
			handler.ServeHTTP(w, r)
			return
		}
		capture := &calendarAcceptanceWriter{ResponseWriter: w}
		handler.ServeHTTP(capture, r)
		call.Response = append(json.RawMessage(nil), capture.body.Bytes()...)
		traceMu.Lock()
		calls = append(calls, call)
		traceMu.Unlock()
	}))
	endpoint := "http://" + server.Listener.Addr().String()
	access.internal = endpoint
	access.public = endpoint
	server.Start()
	defer server.Close()
	profile := dshProfile{BinaryPath: cfg.Runtime.DSH.BinaryPath, BinaryArgs: cfg.Runtime.DSH.BinaryArgs, Artifact: cfg.Runtime.DSH.Artifact, Workspace: workspace, Model: entry.materialize(), Instructions: assistantInstructions}
	svc, err = startService(dir, state.RoomID, serviceConfig{unifiedInputs: true, access: access, endpoint: endpoint, cliPath: cli, maxTurns: 20, dshTurnTimeout: 90 * time.Second, dshProfiles: map[string]dshProfile{"fixture-ignore": profile}}, func(s *service, state RoomSnapshot) error {
		if err := s.loadMembers(state); err != nil {
			return err
		}
		s.memberFactory = func(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) actorx.Actor {
			m := s.newMember(id, runner, restored).(*MemberAgent)
			b := m.Backend.(dshBackend)
			b.open = func(ctx context.Context, opts dsh.Options) (dshComponent, error) {
				opts.Credential = credential
				return dsh.Open(ctx, opts)
			}
			m.Backend = b
			return m
		}
		return s.attachMembers(state)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := svc.close(ctx); err != nil {
			t.Error(err)
		}
	}()
	if _, err := svc.control(t.Context(), apiRequest{AgentID: "A", MembershipID: "generation-A"}, "enable"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Minute)
	var saved calendar.State
	for time.Now().Before(deadline) {
		if h := svc.calendarCurrent(); h != nil {
			saved = h.projection.Load().state.Clone()
			for _, r := range saved.Runs {
				if r.Status == "succeeded" && r.Publication.Status == "published" {
					// Let the Agent finish the turn before checking duplicate reports;
					// stopping the service at publication would hide a later extra send.
					members, err := svc.members(t.Context())
					if err != nil || len(members.Members) != 1 {
						t.Fatal("cannot verify scheduled turn settlement", members, err)
					}
					if members.Members[0].Execution.Active {
						continue
					}
					if members.Members[0].Execution.Error != "" {
						t.Fatal("scheduled turn failed", members.Members[0].Execution.Error)
					}
					if !r.DueAt.Equal(due) || !strings.Contains(r.Report, "CALENDAR_REAL_DSH_OK") || r.Binding.Generation == "" || r.Binding.Turn < 2 || r.Task.Source.MessageSeq != 1 {
						t.Fatal("scheduled time, report, execution or human provenance did not match", r)
					}
					if recurring {
						task := saved.Tasks[r.Task.ID]
						rule := task.Definition.Schedule
						if rule.Frequency != "daily" || rule.DayFilter != "business_days" || rule.WorkCalendar != "CN" || rule.TimeZone != "UTC" || task.State != "active" || !task.NextAt.After(r.DueAt) || r.WorkCalendarVersion == "" {
							t.Fatal("recurring rule/next time/provenance mismatch", task, r)
						}
					}
					room, err := svc.store.LoadRoom()
					if err != nil {
						t.Fatal(err)
					}
					matched := false
					reportCount := 0
					for _, message := range room.Messages {
						if message.Text == r.Report && message.Author.Kind == "agent" {
							reportCount++
						}
						if message.Seq == r.Publication.Seq && message.Text == r.Report && message.Author.Kind == "agent" {
							matched = true
						}
					}
					if recurring {
						explained := false
						for _, message := range room.Messages {
							if message.Author.Kind == "agent" && message.Text != r.Report && strings.Contains(message.Text, "工作日") && strings.Contains(message.Text, "补班") {
								explained = true
							}
						}
						if !explained {
							t.Fatal("Agent did not publicly explain mainland business-day/makeup semantics")
						}
					}
					if !matched || reportCount != 1 {
						t.Fatal("publication receipt has no matching public report")
					}
					traceMu.Lock()
					trace := append([]calendarAcceptanceCall(nil), calls...)
					traceMu.Unlock()
					counts := map[string]int{}
					for _, call := range trace {
						counts[call.Tool]++
						var args map[string]json.RawMessage
						_ = json.Unmarshal(call.Arguments, &args)
						if call.Tool == "calendar update" && args["expected_version"] != nil {
							t.Fatal("real create unnecessarily required expected_version")
						}
					}
					if counts["calendar finish"] == 0 {
						continue
					} // HTTP response may still be returning.
					// A recurring task may read its saved definition/coverage once after
					// creation to explain the official business calendar. This is an optional
					// task read, never a claim preflight or publication polling loop.
					allowedGet := 0
					if recurring {
						allowedGet = 1
					}
					for _, call := range trace {
						if call.Tool == "calendar get" {
							var q calendarQuery
							_ = json.Unmarshal(call.Arguments, &q)
							if !recurring || q.TaskID != r.Task.ID || q.RunID != "" || q.Result || q.History {
								t.Fatal("unexpected real get purpose", call)
							}
						}
					}
					if counts["calendar update"] != 1 || counts["calendar claim"] != 1 || counts["calendar finish"] != 1 || counts["calendar get"] > allowedGet {
						t.Fatal("unexpected calendar normal-path calls", counts)
					}
					evidence := map[string]any{"status": "passed", "contract": "Calendar CLI with on-demand help and compact business views", "model": entry.materialize().ModelID, "room": room, "calendar": saved, "rule_due": due, "calendar_calls": trace, "call_counts": counts, "public_report_count": reportCount, "recurring": recurring, "scope": "real installed DSH/model, isolated Room; no external platform reads or writes"}
					data, _ := json.MarshalIndent(evidence, "", "  ")
					if err := os.WriteFile(filepath.Join(output, "evidence.json"), data, 0600); err != nil {
						t.Fatal(err)
					}
					t.Log("real DSH arranged and published", r.ID, r.Binding, r.Publication.Seq)
					return
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	b, _ := json.MarshalIndent(saved, "", "  ")
	_ = os.WriteFile(filepath.Join(output, "failed-calendar.json"), b, 0600)
	members, _ := svc.members(t.Context())
	t.Fatalf("real scheduled loop incomplete; member status: %+v", members)
}

// This test-only tee captures Calendar HTTP responses without credentials.
type calendarAcceptanceCall struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Response  json.RawMessage `json:"response"`
}
type calendarAcceptanceWriter struct {
	http.ResponseWriter
	body bytes.Buffer
}

func (w *calendarAcceptanceWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	_, _ = w.body.Write(b[:n])
	return n, err
}
func (w *calendarAcceptanceWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
