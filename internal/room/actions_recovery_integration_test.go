//go:build integration || e2e

package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-aie/actorx"
)

type actionLedgerRecoveryMember struct{ status MemberStatus }

func (p actionLedgerRecoveryMember) Bindings() []actorx.Binding {
	return []actorx.Binding{actorx.Handle(func(actorx.Context, InspectMember) (MemberStatus, error) { return p.status, nil })}
}

func TestActionLedgerReadableDuringSessionRecovery(t *testing.T) {
	for _, state := range []string{"waiting", "closing", "failed"} {
		t.Run(state, func(t *testing.T) {
			parallelFixtureTest(t)
			h, id, _, cli, refs := imageSendHarness(t)
			q := imageActionRequest(t, h, id, cli, "self", refs[:2], "完整消息", "inspect-unknown")
			if err := os.WriteFile(filepath.Join(h.dir, "unknown"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if r, err := executeSelfSend(cli, q); err != nil || r.Status != "unknown" {
				t.Fatal(r, err)
			}
			before := historyTree(t, h.svc.store.dir)
			member, err := h.svc.rt.Spawn(actionLedgerRecoveryMember{MemberStatus{Paused: true, Rotation: &SessionRotation{Status: state, Phase: "prepared", Reason: "backend_recovery", FailedStep: "platform_settlement"}}})
			if err != nil {
				t.Fatal(err)
			}
			h.svc.setTarget(id.MembershipID, member)
			t.Cleanup(func() { h.svc.setTarget(id.MembershipID, actorx.Addr{}) })
			req := apiRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id), Followup: &followupRequest{ID: q.ID}}
			read := managerHTTP(h.m, "/v1/actions/read", req)
			var got actionQueryRecord
			if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &got) != nil || got.Status != "unknown" || got.Detail == nil || got.Detail.Attempts != 1 || string(actionInputJSON(got.Detail.Request.Input)) != string(actionInputJSON(q.Input)) {
				t.Fatal("receipt inaccessible during recovery", read.Code, read.Body.String())
			}
			if list := managerHTTP(h.m, "/v1/actions/list", req); list.Code != 200 {
				t.Fatal(list.Code, list.Body.String())
			}
			if _, err := executeSelfSend(cli, q); err == nil {
				t.Fatal("rotation admitted action execution")
			}
			var resolution actionTargetView
			if err := cli([]string{"actions", "resolve", "--request", `{"provider":"lark","locator":{"self":true},"turn_number":1}`}, &resolution); err == nil {
				t.Fatal("rotation admitted target resolution")
			}
			foreign := req
			foreign.Caller.MembershipID = "another-membership"
			if r := managerHTTP(h.m, "/v1/actions/read", foreign); r.Code == 200 {
				t.Fatal("foreign membership read receipt")
			}
			foreign = req
			foreign.RoomInstance = "replaced-room"
			if r := managerHTTP(h.m, "/v1/actions/read", foreign); r.Code == 200 {
				t.Fatal("foreign Room read receipt")
			}
			h.m.mu.Lock()
			if h.m.platformMemberChanges == nil {
				h.m.platformMemberChanges = map[MemberIdentity]bool{}
			}
			h.m.platformMemberChanges[id] = true
			h.m.mu.Unlock()
			if r := managerHTTP(h.m, "/v1/actions/read", req); r.Code == 200 {
				t.Fatal("membership change gate bypassed")
			}
			h.m.mu.Lock()
			delete(h.m.platformMemberChanges, id)
			h.m.mu.Unlock()
			if len(selfSendPosts(t, h.dir)) != 1 {
				t.Fatal("receipt inspection sent again")
			}
			after := historyTree(t, h.svc.store.dir)
			for path, hash := range before {
				if after[path] != hash {
					t.Fatal("receipt inspection changed durable state", path)
				}
			}
		})
	}
}
