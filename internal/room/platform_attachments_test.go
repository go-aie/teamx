package room

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func attachmentFixture(t *testing.T) (*eventHarness, *service, MemberIdentity, url.Values, []byte) {
	t.Helper()
	h, _ := newIndependentEventHarness(t)
	h.prepare()
	svc := eventLiveService(t, h)
	room, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	member := room.Members[0]
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	event := h.event(1, 0)
	event.Kind, event.NoteID, event.NoteableType, event.NoteableIID = "note", 15, "Issue", 2
	event.NoteBody = "**图片说明**\n![image](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png){width=900 height=536}"
	item := gitlab.EventsMessage(h.state(), gitlab.EventsInboxEntry{DeliveryID: "upload-notice", Event: event})
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), item); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.room.Call[PostReceipt](context.Background(), postIntegrationMessage{RoomIdentity: room.RoomIdentity, DeliveryID: item.DeliveryID, Text: item.Text, Source: item.Source}); err != nil {
		t.Fatal(err)
	}
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	q := url.Values{"room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "service_epoch": {svc.info.ServiceEpoch}, "caller_id": {"operator"}, "seq": {"1"}, "path": {"/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png"}}
	return h, svc, id, q, imageBytes.Bytes()
}

func attachmentHTTP(m *roomManager, q url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v1/platform/attachments?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}

type canceledAttachmentBody struct{ ctx context.Context }

func (b canceledAttachmentBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b canceledAttachmentBody) Close() error             { return nil }
