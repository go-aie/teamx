package room

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Seed a persisted legacy message to exercise reading historical attachments.
func addHistoricalNotification(room *RoomSnapshot, item notification.Item) {
	room.LatestSeq++
	room.Revision++
	source := item.Source
	room.Messages = append(room.Messages, RoomMessage{Seq: room.LatestSeq, ClientMessageID: item.DeliveryID,
		Author: Author{Kind: "integration", ID: source.Provider}, Text: item.Text, Source: &source, CreatedAt: time.Now().UTC()})
	if room.IntegrationReceipts == nil {
		room.IntegrationReceipts = make(map[string]IntegrationReceipt)
	}
	room.IntegrationReceipts[item.DeliveryID] = IntegrationReceipt{Seq: room.LatestSeq, Source: source}
}

func attachmentFixture(t *testing.T) (*eventHarness, *service, MemberIdentity, url.Values, []byte) {
	t.Helper()
	h, _ := newIndependentEventHarness(t)
	h.prepare()
	room, err := readPublishedRoom(h.m.roomDir(h.room.RoomID), h.room.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	member := room.Members[0]
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	event := h.event(1, 0)
	event.Kind, event.NoteID, event.NoteableType, event.NoteableIID = "note", 15, "Issue", 2
	event.NoteBody = "**图片说明**\n![image](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png){width=900 height=536}"
	item := gitlab.EventsMessage(h.state(), gitlab.EventsInboxEntry{Route: notification.Route{Target: inboxTarget(id), Version: 1}, DeliveryID: "upload-notice", Event: event})
	addHistoricalNotification(&room, item)
	store, err := OpenStore(h.m.roomDir(h.room.RoomID), h.room.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	saveErr := store.SaveRoom(room)
	if err := errors.Join(saveErr, store.Close()); err != nil {
		t.Fatal(err)
	}
	svc := eventLiveService(t, h)
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), item); err != nil {
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
