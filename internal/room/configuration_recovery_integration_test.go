//go:build integration || e2e

package room

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
)

// One Room combines native Agent CLI, human forms, three durable domains and a
// lost final receipt. Restart must preserve both known effects and the unknown
// operation fence; status/history must not repair or resubmit any configuration.
func TestConfigurationReceiptsAndObservationsSurviveRestart(t *testing.T) {
	h := larkToolConfigurationHarness(t)
	g := configurationGitLab(t, h)
	req := larkMemberToolRequest(t, h, 0)
	id := req.member()
	var toolSaves atomic.Int32
	h.m.mu.Lock()
	rename := h.m.store.ops.rename
	h.m.store.ops.rename = func(from, to string) error {
		if to == h.m.toolBindingsPath(id.RoomIdentity) {
			toolSaves.Add(1)
		}
		if to == h.m.platformMemberPath(req.OperationID) {
			var op platformMemberOperation
			data, _ := os.ReadFile(from)
			if json.Unmarshal(data, &op) == nil && op.Status == "completed" {
				return os.ErrPermission
			}
		}
		return rename(from, to)
	}
	h.m.mu.Unlock()
	server := httptest.NewServer(h.m)
	defer server.Close()
	args := func(platform managedPlatform, operation string) []string {
		return []string{"member", "tool", "--platform-id", platform.ID, "--platform-version", fmt.Sprint(platform.Version), "--operation-id", operation, "--source-seq", fmt.Sprint(req.SourceSeq), "--enabled", "true"}
	}
	gitlabID, _ := newOpaqueID()
	raw, err := toolConfigurationCLI(t, h, server.URL, args(g, gitlabID)...)
	var gitlabOp platformMemberOperation
	if err != nil || json.Unmarshal(raw, &gitlabOp) != nil || gitlabOp.Status != "completed" {
		t.Fatal(err, string(raw))
	}
	notificationReq := req
	notificationReq.OperationID, _ = newOpaqueID()
	notificationReq.Input = &platformMemberInput{PlatformID: h.c.ID, PlatformVersion: h.c.Version, Enabled: boolPointer(false), Groups: &platformMemberGroupPatch{ChatIDs: memberPatchIDs("oc_group_one")}}
	w := managerHTTP(h.m, "/v1/platforms/member/notifications", notificationReq)
	var notificationOp platformMemberOperation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &notificationOp) != nil || notificationOp.Status != "completed" {
		t.Fatal(w.Code, w.Body.String())
	}
	// Human edits and Agent configuration share the same actual member, with
	// independent rule observations from the original empty policy read.
	for _, rule := range []PolicyChange{
		{ID: "style", Status: "active", Text: "简短中文", SourceText: "以后简短中文"},
		{ID: "order", Status: "active", Text: "先给结论", SourceText: "以后先给结论"},
	} {
		w := managerHTTP(h.m, "/v1/policy/update", apiRequest{ServiceInfo: h.svc.info, Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID, Policy: &rule})
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	raw, err = toolConfigurationCLI(t, h, server.URL, args(h.c, req.OperationID)...)
	if err == nil || toolSaves.Load() != 2 {
		t.Fatal("lost final receipt or duplicate tool save", err, string(raw), toolSaves.Load())
	}
	server.Close()
	h.m.mu.Lock()
	h.m.store.ops.rename = rename
	h.m.mu.Unlock()
	bindings := configurationBindings(t, h)
	policy, err := h.svc.store.readPolicy(id)
	if err != nil || bindings.Revision != 2 || len(bindings.Bindings) != 2 || policy.Revision != 2 {
		t.Fatal("combined durable state", bindings, policy, err)
	}
	h.m.mu.Lock()
	notifications, err := h.m.currentLarkForTargetLocked(id.RoomIdentity, h.c.ID, platformMemberTarget(id))
	h.m.mu.Unlock()
	if err != nil || notifications == nil || notifications.SubscriptionVersion != 1 || notifications.Config.Enabled {
		t.Fatal("saved disabled subscription required", notifications, err)
	}
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, h.m.store.dir, nil)
	h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	var recoveryWrites atomic.Int32
	h.m.mu.Lock()
	recoveryRename := h.m.store.ops.rename
	h.m.store.ops.rename = func(from, to string) error {
		recoveryWrites.Add(1)
		return recoveryRename(from, to)
	}
	h.m.mu.Unlock()
	server = httptest.NewServer(h.m)
	defer server.Close()
	for _, original := range []platformMemberOperation{gitlabOp, notificationOp} {
		raw, err := toolConfigurationCLI(t, h, server.URL, "member", "status", "--operation-id", original.ID)
		var status struct {
			Operations []platformMemberOperation `json:"operations"`
		}
		if err != nil || json.Unmarshal(raw, &status) != nil || len(status.Operations) != 1 || !reflect.DeepEqual(status.Operations[0], original) {
			t.Fatal("known history changed on restart", err, string(raw))
		}
	}
	for _, query := range [][]string{{"member", "status", "--operation-id", req.OperationID}, args(h.c, req.OperationID)} {
		raw, err := toolConfigurationCLI(t, h, server.URL, query...)
		if err != nil {
			t.Fatal("unknown history unavailable", err, string(raw))
		}
		if query[1] == "status" {
			var status struct {
				Operations []platformMemberOperation `json:"operations"`
			}
			if json.Unmarshal(raw, &status) != nil || len(status.Operations) != 1 || status.Operations[0].Status != "unknown" {
				t.Fatal(string(raw))
			}
		} else {
			var op platformMemberOperation
			if json.Unmarshal(raw, &op) != nil || op.Status != "unknown" {
				t.Fatal("unknown intent replayed", string(raw))
			}
		}
	}
	after := configurationBindings(t, h)
	afterPolicy, err := h.svc.store.readPolicy(id)
	h.m.mu.Lock()
	afterNotifications, ne := h.m.currentLarkForTargetLocked(id.RoomIdentity, h.c.ID, platformMemberTarget(id))
	h.m.mu.Unlock()
	if err != nil || ne != nil || !reflect.DeepEqual(bindings, after) || !reflect.DeepEqual(policy.PolicySnapshot, afterPolicy.PolicySnapshot) || !reflect.DeepEqual(notifications, afterNotifications) || recoveryWrites.Load() != 0 {
		t.Fatal("recovery changed configuration or issued writes", err, ne, recoveryWrites.Load())
	}
}
