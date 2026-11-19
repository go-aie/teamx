package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/calendar"
)

// Keep the actual application open across a manager/runtime reconstruction.
// The test driver controls time and model results, never browser API responses.
func TestCalendarConversationRecoveryBrowser(t *testing.T) {
	if os.Getenv("TEAMX_CALENDAR_BROWSER") != "1" {
		t.Skip("opt-in Chromium combination acceptance")
	}
	f := newCalendarManagedFixture(t)
	tasks := []*calendar.Task{f.create(0, "私信甲重复提醒"), f.create(1, "私信乙重复提醒")}
	var active atomic.Pointer[roomManager]
	active.Store(f.manager)
	type command struct {
		action string
		result chan any
	}
	commands := make(chan command)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__test/calendar/restart" || r.URL.Path == "/__test/calendar/complete-other" {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			c := command{r.URL.Path, make(chan any, 1)}
			select {
			case commands <- c:
			case <-r.Context().Done():
				return
			}
			select {
			case value := <-c.result:
				w.Header().Set("Content-Type", "application/json")
				_ = writeJSON(w, value)
			case <-r.Context().Done():
			}
			return
		}
		active.Load().ServeHTTP(w, r)
	})
	server := browserTestServer(t, f.manager, handler)
	f.manager.config.endpoint = server.URL
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	identities, _ := json.Marshal(f.rooms)
	cmd := exec.CommandContext(ctx, "node", "testdata/calendar_lifecycle_browser.cjs", server.URL, string(identities))
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case c := <-commands:
			switch c.action {
			case "/__test/calendar/restart":
				before := f.manager.epoch
				f.restart()
				active.Store(f.manager)
				c.result <- map[string]any{"previous_epoch": before, "service_epoch": f.manager.epoch}
			case "/__test/calendar/complete-other":
				run := f.finish(1, f.due(1, tasks[1]))
				c.result <- map[string]any{"run_id": run.ID, "report": run.Report, "report_seq": run.Publication.Seq, "binding": run.Binding, "task_id": run.Task.ID}
			}
		case err := <-done:
			if err != nil {
				t.Fatalf("Chromium combination: %v\n%s", err, output.String())
			}
			// Check authoritative files after the browser's second restart/read.
			for i, task := range tasks {
				state, err := loadCalendar(f.service(i))
				if err != nil || len(state.Tasks) != 1 || state.Tasks[task.ID].State != "active" || len(state.Runs) != i {
					t.Fatal("browser/recovered persistence mismatch", err, state)
				}
			}
			select {
			case id := <-f.entered:
				t.Fatal("viewing or reconstruction started another model turn", id)
			default:
			}
			t.Log(output.String())
			return
		}
	}
}
