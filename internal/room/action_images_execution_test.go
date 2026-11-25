package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	larkruntime "github.com/go-aie/teamx/internal/app/builtinplatforms/lark"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

// Check the exact durable intent at the upload boundary, without teaching the
// native protocol fixture how to find or interpret the Room ledger.
func assertImageActionAttempt(t *testing.T, store *Store, id MemberIdentity, key string, input ActionInput, digest string) {
	t.Helper()
	var record storedAction
	if err := readSnapshot(store.actionPath(id, key), &record, followupBytes); err != nil {
		t.Fatal(err)
	}
	a := record.Action
	if record.MemberIdentity != id || a.Request.ID != key || a.Request.Input != input || a.Status != "unknown" || a.Attempts != 1 || len(a.Inspection.ContentDigest) != 64 || digest != "" && a.Inspection.ContentDigest != digest {
		t.Fatal("upload lacks exact durable intent/attempt and frozen images", a)
	}
	if err := store.validateStoredAction(id, record, store.actionPath(id, key)); err != nil {
		t.Fatal(err)
	}
}

// Only the Room actor, real source Store and action executor are assembled.
// The provider still inspects originals and verifies the writer's native receipt.
type imageExecutionFixture struct {
	h       *roomHarness
	svc     *service
	id      MemberIdentity
	q       referenceActionRequest
	prepare referenceActionPreparer
	writer  lark.ImageWriter
	writes  atomic.Int32
}

func newImageExecutionFixture(t *testing.T, log bool) *imageExecutionFixture {
	t.Helper()
	h, svc, call, refs := actionImagesFixture(t, log)
	id := call.Value(roomActionContextKey{}).(roomActionContext).MemberIdentity
	if err := h.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	post := roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "image-instruction", Text: "把指定原图和完整说明发到准确测试群"})
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: post.Seq, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: post.Seq, OfferedUnreadSeq: post.Seq}}); err != nil {
		t.Fatal(err)
	}
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: lark.Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant"}}, ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}}
	access := platform.AccessContext{RoomIdentity: h.id, AgentID: id.AgentID, MembershipID: id.MembershipID, Account: account, PlatformID: "connection", Generation: "generation", PlatformVersion: 1, CredentialVersion: 1}
	target := platform.TargetIdentity{IdentityScope: account.IdentityScope, Object: platform.NativeIdentity{Type: "chat", ID: platform.ExternalID{Namespace: "lark:chat_id:cli_fixture", Value: "oc_group"}}}
	ref, err := h.store.saveActionTarget(id, platform.TargetResolution{Status: platform.ResolutionResolved, Target: &target, Account: account, ObjectVerified: true, Complete: true, CheckedAt: time.Now().UTC()}, access)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := h.store.readAgentPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	images := make([]lark.ImageRef, len(refs))
	for i, r := range refs {
		images[i] = lark.ImageRef{MessageSeq: r.MessageSeq, AttachmentID: r.AttachmentID}
	}
	var input ActionInput
	if err := json.Unmarshal(mustJSON(t, lark.ImageSendInput{Body: "完整说明", Images: images}), &input); err != nil {
		t.Fatal(err)
	}
	f := &imageExecutionFixture{h: h, svc: svc, id: id, q: referenceActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: ref, ID: "guarded-originals", PolicyToken: policy.PolicyToken, Authority: referenceActionAuthority{RoomSeq: post.Seq, Reason: "实际 Room 指令"}, Operation: platform.OperationMessageSend, Input: input}}
	reader := &sendContractReader{replyContractReader: replyContractReader{messages: map[string]lark.Message{}}, account: account}
	f.writer = func(ctx context.Context, q platform.TargetActionRequest, m lark.PreparedImageMessage) (string, []string, error) {
		// A controlled writer obeys the same source and last-dispatch callbacks as
		// SendImages. Successful native upload/POST wiring stays in integration.
		f.writes.Add(1)
		keys := []string{"img_first", "img_second"}
		msg := lark.Message{ID: "om_receipt", ChatID: "oc_group", Type: "post", Created: "100000", Sender: lark.Sender{ID: account.ID.Value, IDType: "open_id", Type: "user", TenantKey: account.Tenant.Value}}
		rows := [][]map[string]string{{{"tag": "text", "text": m.Body}}}
		for _, key := range keys {
			rows = append(rows, []map[string]string{{"tag": "img", "image_key": key}})
		}
		msg.Body.Content = string(mustJSON(t, map[string]any{"zh_cn": map[string]any{"title": "", "content": rows}}))
		reader.messages[msg.ID] = msg
		return msg.ID, keys, nil
	}
	f.prepare = func(ctx context.Context, member MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
		var lease *roomActionImages
		source := func(ctx context.Context, input lark.ImageSendInput) (lark.PreparedImageMessage, error) {
			if lease == nil {
				var err error
				lease, err = svc.prepareRoomActionImages(ctx, refs, contextGuard)
				if err != nil {
					return lark.PreparedImageMessage{}, err
				}
			}
			if err := lease.Check(ctx); err != nil {
				return lark.PreparedImageMessage{}, err
			}
			m := lark.PreparedImageMessage{Body: input.Body}
			for _, img := range lease.images {
				m.Images = append(m.Images, lark.SendImage{Data: img.Data, MIME: img.Attachment.MIMEType, SHA256: img.SHA256})
			}
			return m, nil
		}
		writer := func(ctx context.Context, q platform.TargetActionRequest, m lark.PreparedImageMessage) (string, []string, error) {
			// The test writer can pause before rechecking, just as a native upload can.
			return f.writer(context.WithValue(ctx, imageLeaseTestKey{}, lease), q, m)
		}
		caps := lark.WithImageActions(lark.AcceptedActionCapabilities(reader, nil, nil), reader, source, writer, contextGuard)
		return referenceActionSession{Access: access, Capabilities: caps, Check: contextGuard, Close: func() {
			if lease != nil {
				lease.Close()
			}
		}}, nil
	}
	return f
}

type imageLeaseTestKey struct{}

func (f *imageExecutionFixture) run(ctx context.Context) (referenceActionReceipt, error) {
	return f.svc.executeReferenceAction(ctx, f.id, f.q, f.prepare)
}

func TestImageActionExecutionGuards(t *testing.T) {
	for _, mode := range []string{"concurrent", "cancel", "digest", "input", "policy"} {
		t.Run(mode, func(t *testing.T) {
			parallelFixtureTest(t)
			f := newImageExecutionFixture(t, false)
			ready, release := make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithCancel(f.h.ctx)
			defer cancel()
			write := f.writer
			f.writer = func(ctx context.Context, q platform.TargetActionRequest, m lark.PreparedImageMessage) (string, []string, error) {
				close(ready)
				select {
				case <-release:
				case <-ctx.Done():
				}
				lease := ctx.Value(imageLeaseTestKey{}).(*roomActionImages)
				if lease.Check(ctx) != nil {
					return "", nil, lark.ErrSendNotSent
				}
				final := ctx.Value(actionDispatchCheckKey{}).(actionDispatchCheck)
				if final(ctx, q, m.ContentDigest()) != nil {
					return "", nil, lark.ErrSendNotSent
				}
				return write(ctx, q, m)
			}
			type outcome struct {
				r   referenceActionReceipt
				err error
			}
			done := make(chan outcome, 1)
			settled := make(chan struct{})
			go func() { defer close(settled); r, err := f.run(ctx); done <- outcome{r, err} }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-settled:
				case <-time.After(2 * time.Second):
					t.Error("image executor did not settle")
				}
			})
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			assertImageActionAttempt(t, f.h.store, f.id, f.q.ID, f.q.Input, "")
			roomError[ClearReceipt](t, f.h, ClearMessages{RoomIdentity: f.h.id, Caller: humanCaller, ThroughSeq: 2}, ErrBusy)
			roomCall[PostReceipt](t, f.h, PostMessage{RoomIdentity: f.h.id, Caller: humanCaller, ClientMessageID: "during-upload", Text: "继续追加"})
			var record storedAction
			if err := readSnapshot(f.h.store.actionPath(f.id, f.q.ID), &record, followupBytes); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "concurrent":
				r, err := f.run(ctx)
				if err != nil || r.Status != "processing" {
					t.Fatal(r, err)
				}
			case "cancel":
				cancel()
			case "digest", "input":
				if mode == "digest" {
					record.Action.Inspection.ContentDigest = strings.Repeat("f", 64)
				} else {
					var input lark.ImageSendInput
					_ = json.Unmarshal(actionInputJSON(record.Action.Request.Input), &input)
					input.Body = "替换后的说明"
					_ = json.Unmarshal(mustJSON(t, input), &record.Action.Request.Input)
				}
				f.h.store.mu.Lock()
				err := f.h.store.saveActionLocked(f.id, record.Action)
				f.h.store.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "policy":
				_, err := f.h.store.changeAgentPolicy(f.id, agentCallerIdentity(f.id), agentPolicyChange{Type: "save", ID: "new-policy", TurnNumber: 1, SourceSeq: record.Action.Basis.Source.Seq, Basis: "用户更新长期要求", Body: &agentPolicyBody{Text: "回复前先确认处理人"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			select {
			case got := <-done:
				want, writes := "not_applied", int32(0)
				if mode == "concurrent" {
					want, writes = "succeeded", 1
				}
				if got.err != nil || got.r.Status != want || got.r.Attempts != 1 || f.writes.Load() != writes {
					t.Fatal(got, writes, f.writes.Load())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("writer did not settle")
			}
			assertActionImagePinsReleased(t, f.h.store)
			roomCall[ClearReceipt](t, f.h, ClearMessages{RoomIdentity: f.h.id, Caller: humanCaller, ThroughSeq: 2})
		})
	}
}

func TestImageActionLedgerFailureReleasesSource(t *testing.T) {
	for _, log := range []bool{false, true} {
		for _, mode := range []string{"intent", "attempt", "unknown-intent"} {
			t.Run(fmt.Sprintf("log=%v/%s", log, mode), func(t *testing.T) {
				parallelFixtureTest(t)
				f := newImageExecutionFixture(t, log)
				store := f.h.store
				ops := store.ops
				fault := errors.New("injected ledger failure")
				writes := 0
				pinned := false
				store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					if filepath.Base(dir) == "records" {
						writes++
						pinned = len(store.roomHistoryPins) == 1
						if mode == "intent" && writes == 1 || mode == "attempt" && writes == 2 {
							return nil, fault
						}
					}
					return ops.createTemp(dir, pattern)
				}
				if mode == "unknown-intent" {
					store.ops.syncDir = func(path string) error {
						if filepath.Base(path) == "records" {
							return fault
						}
						return ops.syncDir(path)
					}
				}
				result, err := f.run(f.h.ctx)
				if mode == "attempt" {
					if err != nil || result.Status != "not_applied" {
						t.Fatal(result, err)
					}
				} else if !errors.Is(err, fault) {
					t.Fatal("injected ledger failure hidden", result, err)
				}
				if !pinned || writes == 0 || f.writes.Load() != 0 {
					t.Fatal("failure did not stop writer after source pin", pinned, writes, f.writes.Load())
				}
				assertActionImagePinsReleased(t, store)
				store.ops = ops
				if mode == "unknown-intent" {
					if !errors.Is(store.check(true), ErrCommitUnknown) {
						t.Fatal("unknown commit did not freeze Store")
					}
					prior, err := f.run(f.h.ctx)
					if err != nil || prior.Status != "unknown" || prior.Attempts != 0 {
						t.Fatal("unknown intent receipt changed", prior, err)
					}
				}
				if f.writes.Load() != 0 {
					t.Fatal("ledger failure uploaded")
				}
			})
		}
	}
}

func TestBuiltinActionFieldDiagnostics(t *testing.T) {
	// Use the registered runtime and its actual owner decoder; malformed fields
	// fail before a manager, source material or action session is needed.
	runtime := larkruntime.Definition().Tool.Bind(builtinToolHost{})
	for _, tc := range []struct{ op, body, field string }{
		{"resolve", `{"locator":{"candidate":{"target":{"private":"do-not-echo"}}},"turn_number":1}`, "locator.candidate.target"},
		{"execute", `{"unexpected":"do-not-echo"}`, "unexpected"},
	} {
		_, err := runtime.Run(t.Context(), "lark-entry", []string{"actions", tc.op, "--request", tc.body})
		if err == nil || !strings.Contains(err.Error(), tc.field) || strings.Contains(err.Error(), "do-not-echo") {
			t.Fatal(tc.op, err)
		}
	}
}
