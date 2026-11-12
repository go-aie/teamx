//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestPlatformMemberLarkCLIConfiguresRangesAndConditions(t *testing.T) {
	parallelFixtureTest(t)
	h, req := platformMemberHarness(t)
	bindCLITool(t, h.m, req.RoomIdentity, h.members[1], h.c, "lark", 0)
	bindings, err := os.ReadFile(h.m.toolBindingsPath(req.RoomIdentity))
	if err != nil {
		t.Fatal(err)
	}
	// An inaccessible old target can be removed without calling Chat for it.
	bin := filepath.Join(h.dir, "lark-cli")
	script, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	script = bytes.Replace(script, []byte("path=a[2]\n"), []byte("path=a[2]\nif path.endswith('/oc_inaccessible'):\n raise SystemExit('removed target must not be read')\n"), 1)
	if err := os.WriteFile(bin, script, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := h.m.defaultLarkConfig(h.c.ID)
	cfg.Enabled, cfg.Private.Enabled = true, false
	cfg.Target = platformMemberTarget(req.member())
	cfg.Private.Scope, cfg.Private.ChatIDs = "specified", []string{"oc_private"}
	cfg.Groups = lark.GroupConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_group_one", "oc_inaccessible"}, MessageMode: "mentions", AtMe: true, AtAll: true}
	cfg.IntervalSeconds, cfg.BackfillSeconds = 61, 20
	initial, err := h.m.configureLarkSubscription(context.Background(), notification.Key{Owner: req.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Establish a real reception baseline first, then pause without resetting it.
	cfg.Enabled = false
	initial, err = h.m.configureLarkSubscription(context.Background(), initial.Metadata().Key, initial.SubscriptionVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	version := initial.SubscriptionVersion
	server := httptest.NewServer(h.m)
	defer server.Close()
	binary := productionFixtureCLI(t)
	env := cliToolEnv(server.URL, h.svc, h.members[0])
	apply := func(flags ...string) platformMemberOperation {
		t.Helper()
		source, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller, ClientMessageID: "change-notifications-" + strconv.FormatUint(version, 10), Text: "按明确范围调整我的飞书通知"})
		if err != nil {
			t.Fatal(err)
		}
		id, _ := newOpaqueID()
		args := []string{"platforms", "member", "notifications", "--platform-id", h.c.ID, "--platform-version", strconv.FormatUint(h.c.Version, 10), "--notification-version", strconv.FormatUint(version, 10), "--source-seq", strconv.FormatUint(source.Seq, 10), "--operation-id", id}
		args = append(args, flags...)
		run := func() []byte {
			command := exec.Command(binary, args...)
			command.Env = os.Environ()
			for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
				command.Env = append(command.Env, key+"="+env(key))
			}
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatal(args, err, string(output))
			}
			return output
		}
		output := run()
		var op platformMemberOperation
		if json.Unmarshal(output, &op) != nil || op.Status != "completed" || op.Result.Status != "applied" {
			t.Fatal(string(output))
		}
		if replay := run(); !bytes.Equal(output, replay) {
			t.Fatal("duplicate CLI submission changed its receipt")
		}
		version = op.Result.Version
		h.m.mu.Lock()
		current, readErr := h.m.currentLarkForTargetLocked(req.RoomIdentity, h.c.ID, cfg.Target)
		h.m.mu.Unlock()
		if readErr != nil || current == nil || current.SubscriptionVersion != version || current.Baseline != initial.Baseline || current.Config.BackfillStart != initial.Config.BackfillStart || current.Config.IntervalSeconds != 61 || !reflect.DeepEqual(current.Config, *op.Config) {
			t.Fatal("configuration or original baseline lost", current, op, readErr)
		}
		return op
	}
	op := apply("--remove-chat-id", "oc_inaccessible", "--group-at-me", "true", "--group-at-all", "false")
	if len(op.Chats) != 0 || op.Config.Enabled || !reflect.DeepEqual(op.Config.Groups.ChatIDs, []string{"oc_group_one"}) || op.Config.Groups.AtAll || !reflect.DeepEqual(op.Config.Private, initial.Config.Private) {
		t.Fatal(op)
	}
	op = apply("--group-scope", "all", "--group-enabled", "true", "--private-enabled", "false")
	if !op.Config.Enabled || !op.Config.Groups.Enabled || op.Config.Groups.Scope != "all" || len(op.Chats) != 0 {
		t.Fatal(op)
	}
	op = apply("--group-scope", "specified", "--group-chat-id", "oc_group_two", "--group-chat-id", "oc_group_one", "--group-message-mode", "mentions", "--group-at-me", "true", "--group-at-all", "false", "--private-enabled", "false")
	if len(op.Chats) != 2 || op.Chats[0].ID != "oc_group_two" || !reflect.DeepEqual(op.Config.Groups.ChatIDs, []string{"oc_group_one", "oc_group_two"}) || op.Config.Private.Enabled || op.Config.Groups.AtAll {
		t.Fatal(op)
	}
	op = apply("--private-enabled", "true", "--group-enabled", "false")
	if !op.Config.Private.Enabled || op.Config.Private.Scope != "specified" || !reflect.DeepEqual(op.Config.Private.ChatIDs, []string{"oc_private"}) || op.Config.Groups.Enabled {
		t.Fatal("private enable changed its existing specified range", op)
	}
	op = apply("--private-scope", "all")
	if op.Config.Private.Scope != "all" || !reflect.DeepEqual(op.Config.Private.ChatIDs, []string{"oc_private"}) {
		t.Fatal(op)
	}
	op = apply("--group-chat-id", "", "--private-enabled", "false")
	if op.Config.Enabled || op.Config.Groups.Enabled || op.Config.Private.Enabled || len(op.Config.Groups.ChatIDs) != 0 || op.Input.Groups.ChatIDs == nil {
		t.Fatal("explicit clear or last-source shutdown was lost", op)
	}
	after, err := os.ReadFile(h.m.toolBindingsPath(req.RoomIdentity))
	if err != nil || !bytes.Equal(bindings, after) {
		t.Fatal("notification changes modified tool bindings", err)
	}
	h.m.mu.Lock()
	selections, err := h.m.platformSelectionsLocked()
	h.m.mu.Unlock()
	if err != nil || len(selections) != 0 {
		t.Fatal("direct configuration created a selection draft", selections, err)
	}
}

func TestPlatformMemberLarkReplacementUnknownReceiptSurvivesRestart(t *testing.T) {
	parallelFixtureTest(t)
	h, req := platformMemberHarness(t)
	req.Input.AddChatIDs = nil
	req.Input.Groups = &platformMemberGroupPatch{ChatIDs: memberPatchIDs("oc_group_two"), AtMe: memberPatchBool(true), AtAll: memberPatchBool(false)}
	// Keep reception off so reopening does not need a running provider process.
	req.Input.Enabled = memberPatchBool(false)
	real := h.m.store.ops
	h.m.mu.Lock()
	h.m.store.ops.rename = func(from, to string) error {
		if to == h.m.platformMemberPath(req.OperationID) {
			var op platformMemberOperation
			data, _ := os.ReadFile(from)
			if json.Unmarshal(data, &op) == nil && op.Status == "completed" {
				return os.ErrPermission
			}
		}
		return real.rename(from, to)
	}
	h.m.mu.Unlock()
	if r := managerHTTP(h.m, "/v1/platforms/member/notifications", req); r.Code == 200 {
		t.Fatal("uncertain receipt reported success")
	}
	h.m.mu.Lock()
	h.m.store.ops = real
	h.m.mu.Unlock()
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, h.m.store.dir, nil)
	svc := eventLiveService(t, &eventHarness{m: reopened, room: roomSummary{RoomIdentity: req.RoomIdentity}})
	req.ServiceInfo = svc.info
	r := managerHTTP(reopened, "/v1/platforms/member/notifications", req)
	var op platformMemberOperation
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &op) != nil || op.Status != "unknown" || len(op.Chats) != 1 || op.Chats[0].ID != "oc_group_two" || !reflect.DeepEqual(op.Input, *req.Input) {
		t.Fatal(r.Code, r.Body.String())
	}
	reopened.mu.Lock()
	current, err := reopened.currentLarkForTargetLocked(req.RoomIdentity, req.Input.PlatformID, platformMemberTarget(req.member()))
	reopened.mu.Unlock()
	if err != nil || current == nil || current.SubscriptionVersion != 1 || current.Config.Enabled || !reflect.DeepEqual(current.Config.Groups.ChatIDs, []string{"oc_group_two"}) {
		t.Fatal("unknown replacement was replayed or lost", current, err)
	}
	req.OperationID, _ = newOpaqueID()
	req.Input.NotificationVersion = 1
	if r := managerHTTP(reopened, "/v1/platforms/member/notifications", req); r.Code != 409 || !strings.Contains(r.Body.String(), "source_seq") {
		t.Fatal("unknown submission bypassed under a new ID", r.Code, r.Body.String())
	}
}

func TestPlatformMemberLarkReplacementRejectsPrivateTarget(t *testing.T) {
	parallelFixtureTest(t)
	h, req := platformMemberHarness(t)
	req.Input.AddChatIDs = nil
	req.Input.Groups = &platformMemberGroupPatch{ChatIDs: memberPatchIDs("oc_private"), Enabled: memberPatchBool(true)}
	if r := managerHTTP(h.m, "/v1/platforms/member/notifications", req); r.Code != 400 {
		t.Fatal("group replacement accepted a private chat", r.Code, r.Body.String())
	}
	if _, err := os.Stat(h.m.platformMemberPath(req.OperationID)); !os.IsNotExist(err) {
		t.Fatal("invalid replacement reserved an operation", err)
	}
}
