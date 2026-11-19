package room

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/calendar"
)

func TestCalendarBrowser(t *testing.T) {
	if os.Getenv("TEAMX_CALENDAR_BROWSER") != "1" {
		t.Skip("opt-in Chromium acceptance")
	}
	settingsPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(settingsPath, []byte("[server]\ntime_zone = \"Asia/Shanghai\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := calendarFixture(t, t.TempDir(), newControlledBackend(), settingsFromFile(t, settingsPath, true))
	task := calendarCreate(t, s, time.Now().Add(10*time.Minute))
	// A simple reminder exercises the compact detail and minute position without
	// conflating it with the controlled historical pagination fixture below.
	local := time.Now().In(s.settings.snapshot().location).AddDate(0, 0, 1)
	d := calendar.Definition{Title: "实习生面试提醒", Instruction: "提醒我参加实习生面试，结果回复本私信。", Schedule: calendar.Schedule{Frequency: "once", StartDate: local.Format("2006-01-02"), LocalTime: "14:30", TimeZone: "Asia/Shanghai", DayFilter: "all_days"}}
	data, _ := json.Marshal(calendar.Request{Action: "create", OperationID: "browser-simple-reminder", Definition: &d})
	if _, err := s.calendarCall(t.Context(), "update", data, humanCaller, 0, nil); err != nil {
		t.Fatal(err)
	}
	// Real persisted API definitions make the next full week span multiple pages.
	for i := 0; i < 30; i++ {
		d := calendarDefinition(time.Now().Add(10 * time.Minute))
		d.Title = fmt.Sprintf("日常检查 %02d", i+1)
		d.Schedule.Frequency = "daily"
		raw, _ := json.Marshal(calendar.Request{Action: "create", OperationID: fmt.Sprintf("browser-daily-%d", i), Definition: &d})
		if _, err := s.calendarCall(t.Context(), "update", raw, humanCaller, 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := s.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: s.info.RoomIdentity, Caller: agentCallerIdentity(MemberIdentity{s.info.RoomIdentity, "A", "generation-A"}), ClientMessageID: calendar.ID("calendar-generation-A-", "browser-history-report"), Text: "C3_HISTORY_REPORT：本地受控执行结果", DurablePublication: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: s.info.RoomIdentity, Caller: agentCallerIdentity(MemberIdentity{s.info.RoomIdentity, "A", "generation-A"}), ClientMessageID: "browser-schedule-receipt", Text: fmt.Sprintf("已安排：[查看日历](#calendar/task/%s)", task.ID)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if _, err := s.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: s.info.RoomIdentity, Caller: humanCaller, Text: fmt.Sprintf("保留消息 %02d", i), ClientMessageID: fmt.Sprintf("browser-context-%02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.calendarCurrent().call(t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) {
		state.Tasks[task.ID].CreationSource = calendar.Source{MessageSeq: 1, Operator: humanCaller.ID, Text: "请安排一次性任务"}
		for i := 0; i < 55; i++ {
			due := time.Now().UTC().Add(-time.Duration(i+1) * 24 * time.Hour).Truncate(time.Minute)
			r := &calendar.Run{ID: fmt.Sprintf("browser-history-%02d", i), Version: 1, Task: task, DueAt: due, LocalDate: due.Format("2006-01-02"), Status: "succeeded", Settled: true, Report: "C3_HISTORY_REPORT：本地受控执行结果"}
			if i == 1 {
				r.Report = "完整报告验证：" + strings.Repeat("逐项核对历史任务资料并保存执行结论。", 25)
			}
			if i == 0 {
				r.Publication = calendar.Publication{Status: "published", Seq: receipt.Seq, ClientMessageID: calendar.ID("calendar-generation-A-", "browser-history-report")}
			}
			state.Runs[r.ID] = r
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	emptyDir := t.TempDir()
	emptyService := calendarFixture(t, emptyDir, newControlledBackend())
	emptyServer := browserTestServer(t, emptyService)
	server := browserTestServer(t, s)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/calendar_browser.cjs", server.URL, emptyServer.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Chromium: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(emptyDir, "calendar", "state.json")); !os.IsNotExist(err) {
		t.Fatal("viewing empty calendar created a state file", err)
	}
	t.Log(string(output))
}
