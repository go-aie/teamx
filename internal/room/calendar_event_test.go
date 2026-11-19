package room

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
)

func calendarEventFixture() RoomMessage {
	return RoomMessage{Seq: 2, Type: "event", ClientMessageID: "private-delivery", Author: Author{Kind: "system", ID: "calendar"},
		Text: "定时任务到期：检查资料。保留完整标题。run_id=run_due；请先阅读 teamx calendar --help，核对最新人类意图后用 calendar claim 领取，再办理并以 calendar finish 发布报告。", CreatedAt: time.Date(2026, 11, 19, 8, 53, 0, 0, time.UTC),
		Event: &RoomEvent{ID: "private-event", Kind: "calendar_due", SourceSeq: 1, OperationID: "run_due", ResultVersion: 1,
			ResultRef: "calendar/run/run_due", Targets: []string{"generation-A"}, Actor: Author{Kind: "system", ID: "calendar"}}}
}

func TestCalendarEventAgentReadKeepsActionableIdentityWithoutRepeatedInstructions(t *testing.T) {
	m := calendarEventFixture()
	if calendarDueEventSummary("检查资料。保留完整标题", "run_due") != m.Text {
		t.Fatal("producer changed the body used by existing delivery receipts")
	}
	if guide := unifiedAgentGuide(); !strings.Contains(guide, "calendar_due event supplies run_id directly") || strings.Contains(guide, "calendar/run/") {
		t.Fatal("Agent guide does not match the event contract")
	}
	before := roomEventDigest(m)
	page := ReadPage{Messages: []RoomMessage{m}, SnapshotSeq: 2, ReadReceipt: "private-receipt"}
	for _, asJSON := range []bool{true, false} {
		t.Run(map[bool]string{true: "json", false: "text"}[asJSON], func(t *testing.T) {
			confirms := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/read/confirm" {
					confirms++
					_ = json.NewEncoder(w).Encode(ReadConfirmation{})
					return
				}
				_ = json.NewEncoder(w).Encode(page)
			}))
			defer server.Close()
			var output bytes.Buffer
			if err := readCLI(t.Context(), cliClient{endpoint: server.URL, http: server.Client()}, apiRequest{Caller: agentCaller("A", "generation-A")}, &output, asJSON); err != nil {
				t.Fatal(err)
			}
			text := output.String()
			for _, fact := range []string{"定时任务到期：检查资料。保留完整标题", `"run_id":"run_due"`, `"source_seq":1`, `"event_type":"calendar_due"`} {
				if !strings.Contains(text, fact) {
					t.Fatal("missing execution context", fact, text)
				}
			}
			for _, redundant := range []string{"calendar --help", "calendar claim", "calendar finish", "result_ref", "calendar/run/", "private-delivery", "private-event", "target_memberships", "result_version"} {
				if strings.Contains(text, redundant) {
					t.Fatal("redundant or private event data", redundant, text)
				}
			}
			if confirms != 1 {
				t.Fatal("read confirmation changed", confirms)
			}
		})
	}
	if roomEventDigest(m) != before {
		t.Fatal("projection changed the immutable delivery body")
	}
	other := *m.Event
	other.Kind, other.ResultRef = "user_selection", "platforms/selection/operation"
	if got := projectRoomEvent(&other); got.Result != other.ResultRef || got.RunID != "" || got.Source != 1 {
		t.Fatal("other events lost their result locator", got)
	}
	m.Event = &other
	if projectRoomMessage(m).Text != m.Text {
		t.Fatal("unrelated event text changed")
	}
}

func TestCalendarEventUnifiedReadPreservesSummaryAndRunIdentity(t *testing.T) {
	h, s := unifiedReaderFixture(t, 1)
	m := calendarEventFixture()
	receipt := roomCall[PostReceipt](t, h, AppendRoomEvent{RoomIdentity: h.id, Event: *m.Event, Summary: m.Text, CreatedAt: m.CreatedAt})
	page := deliverRoom(t, s, inputstream.ReadRequest{Turn: 1, Stream: roomStreamRef(s.storage.id), ID: "calendar-event"})
	if len(page.RoomEntries) != 2 || !reflect.DeepEqual(page.RoomEntries[1].Event, projectRoomEvent(m.Event)) || page.RoomEntries[1].Text != "定时任务到期：检查资料。保留完整标题" {
		t.Fatal("unified delivery lost event context", page)
	}
	// Repeating the exact persisted event still recovers the original receipt.
	replayed := roomCall[PostReceipt](t, h, AppendRoomEvent{RoomIdentity: h.id, Event: *m.Event, Summary: m.Text, CreatedAt: m.CreatedAt})
	if receipt != replayed || h.list(t).Members[0].ReadThrough != receipt.Seq {
		t.Fatal("delivery replay or consumption changed", receipt, replayed)
	}
}
