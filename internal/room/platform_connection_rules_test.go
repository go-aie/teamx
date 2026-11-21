package room

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlatformMemberCurrentRequestFields(t *testing.T) {
	const get = "/v1/platforms/member/get"
	const notifications = "/v1/platforms/member/notifications"
	const tools = "/v1/platforms/member/tool"
	req := platformMemberRequest{ObserveCurrent: true}
	validate := func(path string, input, fields map[string]any) error {
		t.Helper()
		if fields == nil {
			fields = map[string]any{}
		}
		fields["input"] = input
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			t.Fatal(err)
		}
		return validatePlatformMemberRequestFields(path, req, object)
	}
	for _, field := range []string{"platform_version", "notification_version", "tool_revision", "project_id", "groups", "private", "todos_enabled", "actions", "add_chat_ids", "expected_binding_revision", "unknown"} {
		for _, value := range []any{nil, 0, "", false, []any{}, map[string]any{}} {
			for _, path := range []string{notifications, tools} {
				if err := validate(path, map[string]any{field: value}, nil); err == nil {
					t.Fatal("expanded toggle accepted", path, field, value)
				}
			}
		}
	}
	for field, value := range map[string]any{"platform_version": 0, "notification_version": 0, "enabled": false, "groups": nil} {
		if err := validate(get, map[string]any{field: value}, nil); err == nil {
			t.Fatal("expanded current read accepted", field)
		}
	}
	for _, field := range []string{"operation_id", "query", "page_token", "type"} {
		if err := validate(notifications, nil, map[string]any{field: ""}); err == nil {
			t.Fatal("present empty field accepted", field)
		}
	}
	for _, path := range []string{"/v1/platforms/member/status", "/v1/platforms/chats", "/v1/platforms/projects"} {
		if err := validate(path, nil, nil); err == nil {
			t.Fatal("current observation accepted on another endpoint", path)
		}
	}
	for _, tc := range []struct {
		path  string
		input map[string]any
	}{
		{get, map[string]any{"platform_id": strings.Repeat("a", 32)}},
		{notifications, map[string]any{"platform_id": strings.Repeat("a", 32), "enabled": false}},
		{tools, map[string]any{"platform_id": strings.Repeat("a", 32), "enabled": false}},
	} {
		if err := validate(tc.path, tc.input, nil); err != nil {
			t.Fatal(tc.path, err)
		}
	}
}

func TestPlatformMemberSwitchIdentityPreservesNotificationReceipts(t *testing.T) {
	req := platformMemberRequest{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: strings.Repeat("a", 32)}}, Caller: agentCaller("agent", strings.Repeat("b", 32)), SourceSeq: 42, Input: &platformMemberInput{PlatformID: strings.Repeat("c", 32), Enabled: boolPointer(true)}}
	// Captured from the notification-only identity before adding tool switches.
	const legacyID = "bc27894732b022f0b6169710eb0cc9e8"
	if got := platformMemberToggleID(req); got != legacyID {
		t.Fatal("changed persisted notification operation identity", got)
	}
	if platformMemberSwitchID(req, "tool") == legacyID {
		t.Fatal("tool and notification receipts share an identity")
	}
}
