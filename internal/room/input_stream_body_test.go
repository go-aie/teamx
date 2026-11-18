package room

import (
	"bytes"
	"encoding/json"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"testing"
	"time"
)

func TestInputBusinessProjectionConservesEvidence(t *testing.T) {
	v := inputPageContent{remaining: 3, notifications: []inputStreamNotificationEntry{{Item: notification.View{Content: notification.Content{Text: "Do not comment; request cancelled; existing answer ANSWER_SAVED", Structured: true, Sender: &notification.Participant{Name: "saved author", ID: platform.ExternalID{Namespace: "native:private", Value: "author-1"}}, Deleted: true, Truncated: true, MetadataTruncated: true, ContextOnly: true, Action: "revoked", Attachments: []notification.Attachment{{Type: "image", Reference: "attachment.png", Status: "missing"}}}, Provider: "platform", Channel: "todos", Instance: "https://private.example", CreatedAt: time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), ReplyTo: platform.ExternalID{Namespace: "private:coordinate", Value: "parent-1"}}, Source: inputStreamSource{Ref: "src_stable", Version: 7, Status: "mismatch", Reason: "Saved source evidence differs; inspect source_ref", EvidenceSource: "private-internal"}}}, followups: []agentFollowupSummary{{Ref: "unit_stable", Status: "waiting", Summary: "already answered", Questions: []followupQuestionSummary{{Ref: "question", Text: "saved question"}}, Actions: []referenceActionSummary{{ID: "action_stable", Status: "unknown"}}}}, images: []streamImageCoverage{{Source: "src_stable", Status: "omitted", Reason: "image budget"}}}
	b, err := json.Marshal(projectInputBody(v))
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range []string{"ANSWER_SAVED", "request cancelled", "saved author", "author-1", "2026-11-18", "src_stable", "parent-1", "revoked", "unknown", "action_stable", "image budget", "truncated", "context_only", "todo", "evidence_note"} {
		if !bytes.Contains(b, []byte(fact)) {
			t.Fatal("lost business fact", fact, string(b))
		}
	}
	for _, internal := range []string{"private:coordinate", "native:private", "private.example", "private-internal", "source_version", "structured", "namespace", `"stream"`, `"item"`, `"source"`} {
		if bytes.Contains(b, []byte(internal)) {
			t.Fatal("internal leaked", internal, string(b))
		}
	}
	room := projectInputBody(inputPageContent{room: true, roomEntries: []agentRoomMessage{{Seq: 12, Author: Author{Kind: "human", ID: "user"}, Text: "result", Event: &agentRoomEvent{Kind: "user_selection", Source: 4, Result: "operation/result"}, Source: &agentRoomSource{Provider: "lark", Label: "external conversation"}}}, images: []streamImageCoverage{{Source: "Room #12", SharedSource: "Room #9", Status: "supplied"}}})
	b, _ = json.Marshal(room)
	for _, fact := range []string{"user_selection", "operation/result", "external conversation", "room_seq", "shared_room_seq"} {
		if !bytes.Contains(b, []byte(fact)) {
			t.Fatal(fact, string(b))
		}
	}
	if bytes.Contains(b, []byte("Room #")) {
		t.Fatal("duplicate image coordinate", string(b))
	}
}

func TestInputReceiptCanonicalDigestMatchesRuntime(t *testing.T) {
	data := []byte("{\"remaining_entries\":0,\"entries\":[{\"text\":\"<tag>&     中文\",\"seq\":7}]}")
	digest, err := inputBodyDigest(data)
	if err != nil || digest != "945f363f724118ed6539f230e703d4fb58575c164d461ed400df14ad6380e62b" {
		t.Fatal(digest, err)
	}
}
