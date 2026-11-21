package room

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestMaterialImageBudgetsAndFailureCoverage(t *testing.T) {
	pixels := attachmentTestPNG(t, false)
	notice := materialNotice{request: attachmentRequest{Local: true, Seq: 1}, text: "retained message", images: notificationImageReferences{References: 2, Images: []notificationImage{
		{Path: "one", Identity: "one", Supported: true, Positions: []int{1}},
		{Path: "two", Identity: "two", Supported: true, Positions: []int{2}},
	}}}
	for _, mode := range []string{"text-only", "count", "bytes", "pixels", "unavailable", "format"} {
		t.Run(mode, func(t *testing.T) {
			config := imageMaterialConfig{}
			reason, expectedImages, expectedCalls := "", 0, 2
			switch mode {
			case "text-only":
				reason, expectedCalls = "text-only", 0
			case "count":
				config.MaxImages = 1
				reason, expectedImages, expectedCalls = "image count budget", 1, 1
			case "bytes":
				config.TotalBytes = int64(len(pixels))
				reason, expectedImages, expectedCalls = "image byte budget", 1, 1
			case "pixels":
				config.MaxPixels, reason = 1, "image byte/pixel"
			case "unavailable":
				reason = "attachment unavailable"
			case "format":
				reason = "invalid image format"
			}
			limits, err := config.limits()
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			loader := &materialAttachments{download: func(_ context.Context, _ attachmentRequest, _ *attachmentReader, limits attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
				calls++
				if mode == "unavailable" {
					return nil, "", nil, attachment.ErrUnavailable
				}
				data := pixels
				if mode == "format" {
					data = []byte("broken")
				}
				mime, err := attachment.ValidateImage(data, limits)
				return data, mime, nil, err
			}}
			budget := newNotificationImageBudget(limits)
			groups, _ := budget.prepare(t.Context(), loader, attachmentReader{}, []materialNotice{notice}, mode != "text-only")
			if len(groups) != 2 || calls != expectedCalls {
				t.Fatal(len(groups), calls)
			}
			supplied := 0
			for _, g := range groups {
				if len(g.data) > 0 {
					supplied++
				} else if !strings.Contains(g.reason, reason) {
					t.Fatal(mode, g.reason)
				}
			}
			if supplied != expectedImages || budget.used != int64(expectedImages*len(pixels)) {
				t.Fatal(supplied, budget.used)
			}
		})
	}
}

func TestInboxPayloadValidation(t *testing.T) {
	id := MemberIdentity{RoomIdentity: historyRoom(0, "").RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	for _, mode := range []string{"legacy", "platform", "event", "unknown", "mixed", "missing_event", "platform_with_event", "bad_event_kind", "missing_operation"} {
		t.Run(mode, func(t *testing.T) {
			event := taskEventFixture(mode)
			entry := InboxEntry{Version: 1, MemberIdentity: id, Seq: 1, ReceivedAt: time.Now(), Item: storedGitLab(id, "one", "events", "7", "42", "saved")}
			switch mode {
			case "platform":
				entry.Type = InboxPlatformNotification
			case "event", "bad_event_kind", "missing_operation":
				entry.Type, entry.TaskEvent, entry.Item = InboxTaskEventType, &event, notification.Item{}
				if mode == "bad_event_kind" {
					event.EventType = "unrecognized"
				}
				if mode == "missing_operation" {
					event.OperationID = ""
				}
			case "unknown":
				entry.Type = "future"
			case "mixed":
				entry.Type, entry.TaskEvent = InboxTaskEventType, &event
			case "missing_event":
				entry.Type, entry.Item = InboxTaskEventType, notification.Item{}
			case "platform_with_event":
				entry.TaskEvent = &event
			}
			// Exercise the same decoded envelope loaded from a persisted body.
			data, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			var decoded InboxEntry
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			err = decoded.validate(id)
			valid := mode == "legacy" || mode == "platform" || mode == "event"
			if valid && err != nil || !valid && !errors.Is(err, ErrInvalid) {
				t.Fatal(mode, err)
			}
		})
	}
}

func TestHistoryReadArgumentValidation(t *testing.T) {
	for _, args := range []string{`{}`, `{"stream_ref":""}`, `{"stream_ref":"x","limit":0}`, `{"stream_ref":"x","limit":10001}`, `{"cursor":""}`, `{"cursor":"x","limit":1}`, `{"cursor":"x","stream_ref":"y"}`, `{"stream_ref":"x","query":"q"}`, `{"stream_ref":"x","consume":false}`, `{"stream_ref":null}`, `{"stream_ref":"x","stream_ref":"y"}`} {
		if _, err := decodeHistoryRead([]byte(args)); err == nil {
			t.Fatal("accepted", args)
		}
	}
	for _, args := range []string{`{"stream_ref":"x"}`, `{"stream_ref":"x","limit":10000}`, `{"cursor":"returned"}`} {
		if _, err := decodeHistoryRead([]byte(args)); err != nil {
			t.Fatal(args, err)
		}
	}
}

func TestMCPInboxArgumentValidation(t *testing.T) {
	for _, data := range []string{`{}`, `{"turn_number":0}`, `{"turn_number":1.5}`, `{"turn_number":1,"limit":0}`, `{"turn_number":1,"limit":26}`, `{"turn_number":1,"limit":null}`, `{"turn_number":1,"agent_id":"B"}`} {
		var args mcpInboxArguments
		if err := decodeMCPInboxArguments([]byte(data), &args); !errors.Is(err, ErrRequest) {
			t.Fatal(data, err)
		}
	}
	for _, data := range []string{`{"turn_number":1}`, `{"turn_number":1,"limit":25}`, `{"turn_number":1,"cursor":"returned"}`} {
		var args mcpInboxArguments
		if err := decodeMCPInboxArguments([]byte(data), &args); err != nil || args.Turn != 1 || args.Limit != 25 {
			t.Fatal(data, args, err)
		}
	}
}
