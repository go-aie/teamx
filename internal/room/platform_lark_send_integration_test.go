//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestLarkSendStandaloneCommonContract(t *testing.T) {
	parallelFixtureTest(t)
	for _, kind := range []string{"user", "chat"} {
		t.Run(kind, func(t *testing.T) {
			h := newLarkHarness(t)
			id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
			post, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "send-authority", Text: "向指定测试用户或测试群发送：L4-C 精确正文。"})
			if err != nil {
				t.Fatal(err)
			}
			if err = h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: post.Seq, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: post.Seq, OfferedUnreadSeq: post.Seq}}); err != nil {
				t.Fatal(err)
			}
			h.m.mu.Lock()
			a, _, err := h.m.resolvePlatformAccountLocked(id.RoomIdentity, h.c.ID, "lark")
			h.m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			reader := &sendContractReader{replyContractReader: replyContractReader{messages: map[string]lark.Message{}}, account: a.Identity}
			var calls atomic.Int32
			unknown := false
			caps := func() platform.ReferenceCapabilities {
				return lark.ActionCapabilities(reader, nil, func(ctx context.Context, target platform.TargetIdentity, body, key string) (string, error) {
					calls.Add(1)
					if unknown {
						return "", context.DeadlineExceeded
					}
					chat := target.Object.ID.Value
					if target.Object.Type == "user" {
						chat = target.Containers[0].ID.Value
					}
					m := lark.Message{ID: "om_send", ChatID: chat, Type: "text", Created: "101000", Sender: lark.Sender{ID: a.Identity.ID.Value, Type: "user", IDType: "open_id", TenantKey: a.Identity.Tenant.Value}}
					raw, _ := json.Marshal(map[string]string{"text": body})
					m.Body.Content = string(raw)
					reader.messages[m.ID] = m
					return m.ID, nil
				}, lark.ReplyDisabledReason, "")
			}
			h.m.referenceAdapters["lark"] = caps
			h.m.referenceExecutors["lark"] = func(ctx context.Context, member MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
				if member != id || old.Account.Valid() && old.Account != a.Identity {
					return referenceActionSession{}, ErrIdentity
				}
				access := larkReferenceAccess(id, a, platform.SourceRef{})
				return referenceActionSession{Access: access, Capabilities: caps(), Close: func() {}, Check: func(ctx context.Context) error {
					h.m.mu.Lock()
					defer h.m.mu.Unlock()
					return h.m.checkLarkReferenceAccessLocked(ctx, toolsRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id), Tool: "lark"}, access)
				}}, nil
			}
			h.m.actionResolvers["lark"] = func(ctx context.Context, member MemberIdentity, l platform.TargetLocator) (platform.TargetResolution, referenceActionSession, error) {
				session, err := h.m.referenceExecutors["lark"](ctx, member, platform.AccessContext{})
				if err != nil {
					return platform.TargetResolution{}, session, err
				}
				r, err := session.Capabilities.Resolve(ctx, l, session.Access)
				return r, session, err
			}
			bindings := bindCLITool(t, h.m, id.RoomIdentity, h.members[0], h.c, "lark", 0)
			server := httptest.NewServer(h.m)
			defer server.Close()
			cli := contractCLI(t, server.URL, h.svc, id)
			resolve := func(q actionResolveRequest) actionTargetView {
				t.Helper()
				b, _ := json.Marshal(q)
				var v actionTargetView
				if err := cli([]string{"actions", "resolve", "--request", string(b)}, &v); err != nil {
					t.Fatal(err)
				}
				return v
			}
			candidates := resolve(actionResolveRequest{Provider: "lark", TurnNumber: 1, Locator: platform.TargetLocator{Query: &platform.TargetQuery{ObjectType: "user", Text: "同名用户"}}})
			if candidates.TargetRef != "" || len(candidates.Resolution.Candidates) != 2 || len(candidates.Actions) != 0 {
				t.Fatal(candidates)
			}
			target := platform.TargetIdentity{IdentityScope: a.Identity.IdentityScope, Object: platform.NativeIdentity{Type: kind}}
			if kind == "user" {
				target.Object.ID = platform.ExternalID{Namespace: a.Identity.ID.Namespace, Value: "ou_peer"}
			} else {
				target.Object.ID = platform.ExternalID{Namespace: "lark:chat_id:cli_fixture", Value: "oc_group"}
			}
			resolveQ := actionResolveRequest{Provider: "lark", TurnNumber: 1, Locator: platform.TargetLocator{Candidate: &target}}
			v := resolve(resolveQ)
			if v.TargetRef == "" || len(v.Actions) != 1 || v.Actions[0].Executable || !v.Actions[0].DirectRoomInstructionOnly {
				t.Fatal(v)
			}
			policy, err := h.svc.store.readAgentPolicy(id)
			if err != nil {
				t.Fatal(err)
			}
			q := agentActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: v.TargetRef, ID: "send-one", PolicyToken: policy.PolicyToken, Authority: agentActionAuthority{Type: "human_reply", RoomSeq: post.Seq, Reason: "按真人直接指令发送准确正文"}, Operation: platform.OperationMessageSend, Input: ActionInput{Body: "L4-C 精确正文。"}}
			run := func(q agentActionRequest) (referenceActionReceipt, error) {
				b, _ := json.Marshal(q)
				var r referenceActionReceipt
				err := cli([]string{"actions", "execute", "--request", string(b)}, &r)
				return r, err
			}
			if _, err = run(q); err == nil || calls.Load() != 0 {
				t.Fatal("read binding permitted send", err)
			}
			// A user grant does not grant group sends, and vice versa.
			opposite := "chat"
			if kind == "chat" {
				opposite = "user"
			}
			grant := func(objectType string, revision uint64) {
				r := managerHTTP(h.m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: id.RoomIdentity, Caller: humanCaller, Revision: revision, Enabled: true, PlatformVersion: h.c.Version, Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "lark", PlatformID: h.c.ID, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: "message.send", TargetKind: "object", ObjectType: objectType}}}}})
				if r.Code != 200 {
					t.Fatal(r.Code, r.Body.String())
				}
			}
			grant(opposite, bindings.Revision)
			if _, err = run(q); err == nil || calls.Load() != 0 {
				t.Fatal("opposite target grant leaked", err)
			}
			grant(kind, bindings.Revision+1)
			if v = resolve(resolveQ); !v.Actions[0].Executable {
				t.Fatal(v)
			}
			for _, authority := range []agentActionAuthority{{Type: "rule", ID: "rule", Version: 1, Reason: "rule"}, {Type: "instruction", ID: "instruction", Version: 1, Reason: "instruction"}, {Type: "human_reply", RoomSeq: post.Seq, QuestionRef: "question_fake", Reason: "question"}} {
				wrong := q
				wrong.Authority = authority
				if _, err = run(wrong); err == nil || calls.Load() != 0 {
					t.Fatal("non-direct authority sent", authority, err)
				}
			}
			r, err := run(q)
			if err != nil || r.Status != "succeeded" || calls.Load() != 1 {
				t.Fatal(r, err, calls.Load())
			}
			if r, err = run(q); err != nil || r.Status != "succeeded" || calls.Load() != 1 {
				t.Fatal("duplicate", r, err)
			}
			changed := q
			changed.Input.Body = "changed"
			if _, err = run(changed); err == nil {
				t.Fatal("changed content accepted")
			}
			wrong := q
			wrong.ID = "wrong-operation"
			wrong.Operation = platform.OperationObjectComment
			if _, err = run(wrong); err == nil || calls.Load() != 1 {
				t.Fatal("cross-platform operation", err)
			}
			var read actionQueryRecord
			if err = cli([]string{"actions", "read", "--id", q.ID}, &read); err != nil || read.Context != nil || read.Detail == nil || read.Account.Provider != "lark" {
				t.Fatal(read, err)
			}
			h.svc.store.mu.Lock()
			units, e := h.svc.store.followupUnitsLocked(id, "")
			h.svc.store.mu.Unlock()
			if e != nil || len(units) != 0 {
				t.Fatal("send created followup", units, e)
			}
			second, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "second-send", Text: "独立指令：再发送准确测试正文。"})
			if err != nil {
				t.Fatal(err)
			}

			ownerPost, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "account-owner", Text: "已显示的绑定飞书账号就是我本人。"})
			if err != nil {
				t.Fatal(err)
			}
			if err = h.svc.store.Close(); err != nil {
				t.Fatal(err)
			}
			h.svc.store, err = OpenStore(h.svc.store.dir, id.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			if r, err = run(q); err != nil || r.Status != "succeeded" || calls.Load() != 1 {
				t.Fatal("restart replay", r, err)
			}
			// A new real instruction can authorize an independent action. Unknown from
			// that source blocks a replacement ID even after opening the store again.
			q.ID = "send-uncertain"
			q.Authority.RoomSeq = second.Seq
			unknown = true
			r, err = run(q)
			if err != nil || r.Status != "unknown" || calls.Load() != 2 {
				t.Fatal(r, err)
			}
			h.svc.store.Close()
			h.svc.store, err = OpenStore(h.svc.store.dir, id.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			if r, err = run(q); err != nil || r.Status != "unknown" || calls.Load() != 2 {
				t.Fatal(r, err)
			}
			q.ID = "replacement"
			if _, err = run(q); err == nil || calls.Load() != 2 {
				t.Fatal("unknown replayed", err)
			}
			// Ownership evidence requires an actual human message and invalidates on account changes.
			access := larkReferenceAccess(id, a, platform.SourceRef{})
			if ok, err := h.svc.store.actionAccountOwner(id, access, 0, 1); err != nil || ok {
				t.Fatal(ok, err)
			}
			if ok, err := h.svc.store.actionAccountOwner(id, access, ownerPost.Seq, 1); err != nil || !ok {
				t.Fatal(ok, err)
			}

			self := platform.TargetIdentity{IdentityScope: a.Identity.IdentityScope, Object: platform.NativeIdentity{Type: "user", ID: a.Identity.ID}}
			selfView := resolve(actionResolveRequest{Provider: "lark", TurnNumber: 1, Locator: platform.TargetLocator{Candidate: &self}})
			if !selfView.Resolution.AccountOwnerConfirmed || selfView.TargetRef != "" || selfView.Resolution.ReasonCode != "self_send_unavailable" {
				t.Fatal("ownership enabled self-send", selfView)
			}
			if _, err := h.svc.store.actionAccountOwner(id, access, 9999, 1); err == nil {
				t.Fatal("fabricated ownership source")
			}
			h.svc.store.Close()
			h.svc.store, err = OpenStore(h.svc.store.dir, id.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := h.svc.store.actionAccountOwner(id, access, 0, 1); err != nil || !ok {
				t.Fatal("ownership persistence", ok, err)
			}
			// P4 keeps independently confirmed accounts side by side. The former
			// provider-only proof remains usable only for its exact old platform.
			root := h.svc.store.actionRoot(id)
			if err := os.Rename(filepath.Join(root, "account-owner-"+referenceDigest("", access.Account.Provider, access.PlatformID)+".json"), filepath.Join(root, "account-owner-"+referenceDigest("", access.Account.Provider)+".json")); err != nil {
				t.Fatal(err)
			}
			other := access
			other.PlatformID = strings.Repeat("d", 32)
			other.Account.ID.Value = "ou_other"
			if ok, err := h.svc.store.actionAccountOwner(id, other, 0, 1); err != nil || ok {
				t.Fatal("legacy owner proof crossed account", ok, err)
			}
			if ok, err := h.svc.store.actionAccountOwner(id, other, ownerPost.Seq, 1); err != nil || !ok {
				t.Fatal("second owner proof", ok, err)
			}
			if ok, err := h.svc.store.actionAccountOwner(id, access, 0, 1); err != nil || !ok {
				t.Fatal("second account displaced original proof", ok, err)
			}
			access.Account.ID.Value = "ou_switched"
			if ok, err := h.svc.store.actionAccountOwner(id, access, 0, 1); err != nil || ok {
				t.Fatal("ownership survived account switch", ok, err)
			}
		})
	}
}

func TestLarkSendProductionGateAndCatalog(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	caps, err := h.m.referenceCapabilities("lark")
	if err != nil || caps.Actions[platform.OperationMessageSend].Spec.DisabledReason != "" {
		t.Fatal(caps, err)
	}
	for _, kind := range []string{"user", "chat"} {
		r := managerHTTP(h.m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, Enabled: true, PlatformVersion: h.c.Version, Binding: tool.Binding{AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID, Tool: "lark", PlatformID: h.c.ID, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: "message.send", TargetKind: "object", ObjectType: kind}}}}})
		if (kind == "chat") != (r.Code == 200) {
			t.Fatal("only accepted group sends may be granted", kind, r.Code, r.Body.String())
		}
	}
	h.m.mu.Lock()
	catalog := h.m.toolCatalogViewLocked()
	h.m.mu.Unlock()
	for _, d := range catalog {
		if d.Provider == "lark" {
			if len(d.Actions) != 3 {
				t.Fatal(d)
			}
			for _, spec := range d.Actions {
				if spec.Operation == platform.OperationMessageSend && (len(spec.ObjectTypes) != 1 || spec.ObjectTypes[0] != "chat" || !spec.DirectRoomInstructionOnly || spec.DisabledReason != "") {
					t.Fatal(spec)
				}
			}
			return
		}
	}
	t.Fatal("Lark capability settings missing")
}
