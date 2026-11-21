//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

// The production CLI/action/manager/pinned Lark context chain is retained. Only
// this isolated manager's Inspect is extended to prepare originals and stop;
// neither uploads nor message writes are registered or attempted.
func TestRoomActionImagesManagedActionContextAndCleanup(t *testing.T) {
	h := selfSendHarness(t)
	id, _ := enableSelfSend(t, h, h.members[0], 0)
	server := httptest.NewServer(h.m)
	defer server.Close()
	pixels := attachmentTestPNG(t, true)
	refs := []roomActionImageRef{}
	for i := range 2 {
		imageID := attachmentTestID(i + 1)
		status, body := attachmentUploadHTTP(t, server, h.svc.info, imageID, pixels, false)
		if status != 200 {
			t.Fatal(status, string(body))
		}
		status, body = attachmentJSON(t, server, "/v1/send", apiRequest{ServiceInfo: h.svc.info, Caller: Caller{Author: Author{"human", humanOperator()}}, ClientMessageID: imageID, Attachments: []RoomAttachmentInput{{ID: imageID}}})
		if status != 200 {
			t.Fatal(status, string(body))
		}
		var receipt PostReceipt
		if err := json.Unmarshal(body, &receipt); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, roomActionImageRef{MessageSeq: receipt.Seq, AttachmentID: imageID})
	}
	cli := contractCLI(t, server.URL, h.svc, id, h.c.ToolEntryID)
	q := selfSendRequest(t, cli, "prepare-originals-only")
	stop := errors.New("isolated source preparation complete; no write")
	var captured *roomActionImages
	var prepared, closed bool
	h.m.referenceExecutors["lark"] = func(ctx context.Context, member MemberIdentity, access platform.AccessContext) (referenceActionSession, error) {
		var session referenceActionSession
		var err error
		session, err = h.m.prepareLarkReferenceActions(ctx, member, access, func(reader lark.SendReader, reply lark.ReplyWriter, send lark.SendWriter) platform.ReferenceCapabilities {
			caps := lark.AcceptedActionCapabilities(reader, reply, send)
			cap := caps.Actions[platform.OperationMessageSendSelf]
			original := cap.Inspect
			cap.Inspect = func(ctx context.Context, req platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
				inspection, err := original(ctx, req, a)
				if err != nil {
					return inspection, err
				}
				call, ok := ctx.Value(roomActionContextKey{}).(roomActionContext)
				if !ok || call.ServiceInfo != h.svc.info || call.MemberIdentity != id || call.Turn != 1 || call.ID != q.ID {
					return inspection, ErrIdentity
				}
				captured, err = h.svc.prepareRoomActionImages(ctx, refs, session.Check)
				if err != nil {
					return inspection, err
				}
				prepared = true
				for _, img := range captured.images {
					if !bytes.Equal(img.Data, pixels) {
						return inspection, ErrInvalid
					}
				}
				return inspection, stop
			}
			cap.Execute = func(context.Context, platform.TargetActionRequest, platform.AccessContext) (platform.TargetActionResult, error) {
				t.Error("stage 1 dispatched a write")
				return platform.TargetActionResult{}, ErrInvalid
			}
			caps.Actions[platform.OperationMessageSendSelf] = cap
			return caps
		})
		if err != nil {
			return session, err
		}
		closeSession := session.Close
		session.Close = func() {
			if captured != nil {
				captured.Close()
			}
			closeSession()
			closed = true
		}
		return session, nil
	}
	before := historyTree(t, h.svc.store.dir)
	if _, err := executeSelfSend(cli, q); err == nil || !prepared || !closed {
		t.Fatal("managed preparation/cleanup did not run", prepared, closed, err)
	}
	assertActionImagePinsReleased(t, h.svc.store)
	if !reflect.DeepEqual(before, historyTree(t, h.svc.store.dir)) {
		t.Fatal("preparation changed durable Room/input/action state")
	}
	if _, err := os.Stat(filepath.Join(h.dir, "posts")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected platform write", err)
	}
	// Formal image admission still rejects an unavailable Room source before writes.
	h.m.referenceExecutors["lark"] = h.m.prepareLarkReferenceAction
	var input ActionInput
	if err := json.Unmarshal([]byte(`{"images":[{"message_seq":1,"attachment_id":"img_00000000000000000000000000000001"}]}`), &input); err != nil {
		t.Fatal(err)
	}
	q.Input = input
	if _, err := executeSelfSend(cli, q); err == nil {
		t.Fatal("unavailable Room source admitted by production image capability")
	}
	assertActionImagePinsReleased(t, h.svc.store)
}
