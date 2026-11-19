package room

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestHistoryRecoveryPreviewAndFormalDelivery(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		t.Run(fmt.Sprint(consumed), func(t *testing.T) {
			h, s := unifiedReaderFixture(t, 25)
			ref := roomStreamRef(s.storage.id)
			if consumed {
				readInputStream(t, s, "before-recovery", ref, 25, true)
			}
			before := historyTree(t, h.store.dir)
			r, err := s.service.prepareDSHRecoveryContext(h.ctx, UnreadChanged{MemberIdentity: s.storage.id, LatestSeq: 25}, 1)
			if err != nil {
				t.Fatal(err)
			}
			blocks, err := r.appendTo(nil)
			if err != nil || r.room == nil || len(r.room.Messages) != 20 || r.room.Messages[0].Seq != 6 || r.room.Messages[19].Seq != 25 || !strings.Contains(blocks[0].Text, `"room_stream_ref":"`+ref+`"`) {
				t.Fatal("consumption-independent bounded preview", r, err)
			}
			// More context is read using the reference from recovery, including
			// consumed records. Neither view changes any authoritative file.
			page, _ := historyRead(t, s, "restored", r.roomStream, 20)
			older, _ := historyNext(t, s, "restored-older", page.Next)
			if len(older.Entries) != 5 || !reflect.DeepEqual(before, historyTree(t, h.store.dir)) {
				t.Fatal("recovery/history wrote consumption or business state")
			}
			if !consumed {
				readInputStream(t, s, "formal-overlap", ref, 25, true)
				settled := historyTree(t, h.store.dir)
				readInputStream(t, s, "formal-overlap", ref, 25, true)
				if !reflect.DeepEqual(settled, historyTree(t, h.store.dir)) || h.list(t).Members[0].ReadThrough != 25 {
					t.Fatal("formal delivery of preview overlap did not settle once")
				}
			}
			catalog, err := s.catalog(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			directory, _, err := catalog.Directory()
			if err != nil || strings.Contains(string(mustJSON(t, directory)), ref) {
				t.Fatal("fully consumed Room still pending", directory, err)
			}
		})
	}
}

func TestHistoryGuidanceAndBackendModes(t *testing.T) {
	for _, mode := range []string{"http", "cli"} {
		t.Run(mode, func(t *testing.T) {
			h, s := unifiedReaderFixture(t, 2)
			s.service.inboxMode = mode
			b := dshBackend{store: h.store, inputService: s.service}
			if mode == "http" {
				b.inboxMCP = s.service
			}
			opts := adapterOptions(h.store, s.storage.id)
			options, err := b.componentOptions(opts, dshReference{})
			if err != nil || b.mcpService() != s.service || len(options.MCPServers) != 1 {
				t.Fatal("missing production History MCP in Inbox mode", err)
			}
			for _, stale := range []string{"read --all", "read --latest", "Room history and business CLI"} {
				if strings.Contains(options.Instructions, stale) {
					t.Fatal("stale history entry in new guide", stale)
				}
			}
			r, err := s.service.prepareDSHRecoveryContext(context.Background(), UnreadChanged{MemberIdentity: s.storage.id, LatestSeq: 2}, 1)
			if err != nil {
				t.Fatal(err)
			}
			blocks, err := r.appendTo([]dsh.ContentBlock{{Type: "text", Text: "policy/directory"}})
			if err != nil || !strings.Contains(blocks[1].Text, "history_read directly") || strings.Contains(blocks[1].Text, "read --latest") {
				t.Fatal("recovery guide does not match enabled tools", err)
			}
			// The JSON remains a complete object, including when whole optional
			// records are omitted under the original prompt budget.
			text := blocks[1].Text
			var value map[string]any
			if json.Unmarshal([]byte(text[strings.Index(text, "{"):]), &value) != nil || value["room_stream_ref"] != roomStreamRef(s.storage.id) {
				t.Fatal("invalid recovery reference")
			}
		})
	}
}

func TestHistoryRecoveryBudgetKeepsReferenceAndMandatoryGuide(t *testing.T) {
	for _, mode := range []string{"http", "cli"} {
		t.Run(mode, func(t *testing.T) {
			r := &dshRecoveryContext{unified: true, inputMode: mode, roomStream: "stream_saved", unknownActions: 2,
				room:    &agentRoomReadPage{Messages: []agentRoomMessage{{Seq: 1, Text: strings.Repeat("<", 40<<10)}}},
				matters: agentFollowupPage{Total: 1, Items: []agentFollowupSummary{{Ref: "omit-whole", Summary: strings.Repeat("<", 40<<10)}}}}
			blocks, err := r.appendTo([]dsh.ContentBlock{{Type: "text", Text: strings.Repeat("x", (1<<20)-5000)}})
			if err != nil {
				t.Fatal(err)
			}
			text := blocks[len(blocks)-1].Text
			if !materialPromptFits(blocks) || len(text) > dshRecoveryBytes || strings.Contains(text, "omit-whole") || !strings.Contains(text, `"room_stream_ref":"stream_saved"`) || !strings.Contains(text, `"room_messages_omitted_for_budget":1`) || !strings.Contains(text, `"saved_unknown_action_count":2`) || !strings.Contains(text, "never repeat an action") {
				t.Fatal("mandatory recovery evidence or whole-record budget was lost")
			}
			if len(r.room.Messages) != 1 || len(r.matters.Items) != 1 {
				t.Fatal("render mutated snapshot")
			}
		})
	}
}
