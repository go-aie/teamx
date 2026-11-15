package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
)

func TestNotificationObservationRequiredAtCLIAndHTTPBoundaries(t *testing.T) {
	m, svc, member, req := gitlabMemberFixture(t)
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, member)
	args := []string{"member", "notifications", "--platform-id", req.Input.PlatformID, "--platform-version", strconv.FormatUint(req.Input.PlatformVersion, 10), "--operation-id", req.OperationID, "--source-seq", strconv.FormatUint(req.SourceSeq, 10), "--todos-enabled", "true"}
	var out bytes.Buffer
	if err := runPlatformConnectWithClient(context.Background(), args, &out, &out, env, client); err == nil {
		t.Fatal("omitted CLI observation accepted")
	}
	raw, _ := json.Marshal(req)
	for _, value := range []any{"omit", nil} {
		var wire map[string]any
		_ = json.Unmarshal(raw, &wire)
		input := wire["input"].(map[string]any)
		if value == nil {
			input["notification_version"] = nil
		} else {
			delete(input, "notification_version")
		}
		w := managerHTTP(m, "/v1/platforms/member/notifications", wire)
		var failure apiError
		_ = json.Unmarshal(w.Body.Bytes(), &failure)
		if w.Code != 400 || failure.Field != "input.notification_version" || failure.Outcome != "not_applied" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	// Full human forms also distinguish missing/null from an observed zero.
	for path, field := range map[string]string{"/v1/platform/notifications/configure": "subscription_version", "/v1/platform/gitlab/notifications/configure": "version"} {
		for _, null := range []bool{false, true} {
			body := map[string]any{"service_epoch": m.epoch, "room_id": req.RoomID, "room_instance": req.RoomInstance, "caller": humanCaller}
			if null {
				body[field] = nil
			}
			w := managerHTTP(m, path, body)
			var failure apiError
			_ = json.Unmarshal(w.Body.Bytes(), &failure)
			if w.Code != 400 || failure.Field != field || failure.Outcome != "not_applied" {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
	if _, err := os.Stat(m.platformMemberPath(req.OperationID)); !os.IsNotExist(err) {
		t.Fatal("invalid observation reserved ID", err)
	}
	args = append(args, "--notification-version", "0")
	out.Reset()
	if err := runPlatformConnectWithClient(context.Background(), args, &out, &out, env, client); err != nil {
		t.Fatal("explicit zero rejected", err)
	}
	// Desired config now exists. Stale zero cannot replace it, and querying the
	// original ID still returns its original receipt before any version check.
	op := gitlabMemberApply(t, m, "notifications", req)
	if op.Status != "completed" || op.Result.Version != 1 {
		t.Fatal(op)
	}
	req.OperationID, _ = newOpaqueID()
	source, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller, ClientMessageID: "stale-notification-edit", Text: "修改旧配置"})
	if err != nil {
		t.Fatal(err)
	}
	req.SourceSeq = source.Seq
	w := managerHTTP(m, "/v1/platforms/member/notifications", req)
	var failure apiError
	_ = json.Unmarshal(w.Body.Bytes(), &failure)
	if w.Code != 409 || failure.Field != "notification_version" || failure.ExpectedVersion == nil || *failure.ExpectedVersion != 0 || failure.ActualVersion == nil || *failure.ActualVersion != 1 || failure.Outcome != "not_applied" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestGitLabPartialApplicationRetainsAcceptedVersionAndOriginalReceipt(t *testing.T) {
	m, _, _, req := gitlabMemberFixture(t)
	real := m.store.ops
	m.mu.Lock()
	m.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		if dir == m.roomDir(req.RoomID)+"/notifications" {
			return nil, os.ErrPermission
		}
		return real.createTemp(dir, pattern)
	}
	m.mu.Unlock()
	op := gitlabMemberApply(t, m, "notifications", req)
	m.mu.Lock()
	m.store.ops = real
	m.mu.Unlock()
	// The desired save is proven; collector application remains incomplete under
	// the existing unknown fence. Never claim not_applied or submit it again.
	if op.Status != "unknown" || op.Result.Version != 1 || op.Result.ErrorCode != "source_apply_pending" {
		t.Fatal(op)
	}
	v := gitlabMemberGet(t, m, req)
	if v.NotificationVersion != 1 || !v.GitLab.Pending["todos"] || v.GitLab.Errors["todos"] == "" {
		t.Fatal(v)
	}
	if replay := gitlabMemberApply(t, m, "notifications", req); !reflect.DeepEqual(replay, op) {
		t.Fatal("partial intent replayed", replay)
	}
	after := gitlabMemberGet(t, m, req)
	if after.NotificationVersion != 1 || !after.GitLab.Pending["todos"] {
		t.Fatal("status or replay applied source", after)
	}
	// Existing full-settings "retry apply" uses the saved desired configuration,
	// not a new member intent or a replay of its operation ID.
	body := map[string]any{"service_epoch": m.epoch, "room_id": req.RoomID, "room_instance": req.RoomInstance, "caller": humanCaller, "version": v.NotificationVersion, "config": v.GitLab.Config}
	w := managerHTTP(m, "/v1/platform/gitlab/notifications/configure", body)
	var synced gitlabNotificationView
	err := json.Unmarshal(w.Body.Bytes(), &synced)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err != nil || synced.Version != 1 || synced.Applying || synced.Pending["todos"] || len(synced.Errors) != 0 {
		t.Fatal(synced, err)
	}
	if replay := gitlabMemberApply(t, m, "notifications", req); !reflect.DeepEqual(replay, op) {
		t.Fatal("retry apply rewrote historical receipt")
	}
}
