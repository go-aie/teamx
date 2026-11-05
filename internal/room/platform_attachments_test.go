package room

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
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

func TestPlatformAttachmentRoomAndInboxUseSavedSource(t *testing.T) {
	h, _, id, query, data := attachmentFixture(t)
	var calls atomic.Int32
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.String() != "https://gitlab.invalid/api/v4/projects/7/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png" || r.Header.Get("PRIVATE-TOKEN") != "test-platform-secret" {
			t.Error("wrong attachment API request")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/octet-stream"}}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	for _, inbox := range []bool{false, true} {
		q := maps.Clone(query)
		if inbox {
			q.Set("agent_id", id.AgentID)
			q.Set("membership_id", id.MembershipID)
		}
		w := attachmentHTTP(h.m, q)
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Length") != strconv.Itoa(len(data)) {
			t.Fatal(w.Code, w.Header(), w.Body.Len())
		}
	}
	if calls.Load() != 2 {
		t.Fatal("did not fetch on demand", calls.Load())
	}
	for _, change := range []struct{ key, value string }{
		{"path", "/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/other.png"},
		{"path", "https://evil.invalid/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png"},
		{"path", "/-/project/8/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png"},
		{"room_instance", "stale-instance"}, {"service_epoch", "stale-epoch"}, {"seq", "2"},
		{"membership_id", strings.Repeat("f", 32)},
	} {
		q := maps.Clone(query)
		q.Set(change.key, change.value)
		w := attachmentHTTP(h.m, q)
		if w.Code == 200 || calls.Load() != 2 {
			t.Fatal("invalid request reached GitLab", change, w.Code)
		}
	}
	q := maps.Clone(query)
	q.Set("agent_id", id.AgentID)
	q.Set("membership_id", strings.Repeat("f", 32))
	if w := attachmentHTTP(h.m, q); w.Code == 200 || calls.Load() != 2 {
		t.Fatal("cross-membership attachment accepted")
	}
}

type canceledAttachmentBody struct{ ctx context.Context }

func (b canceledAttachmentBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b canceledAttachmentBody) Close() error             { return nil }

func TestPlatformAttachmentLifecycleCancellationDiscardsBody(t *testing.T) {
	h, _, _, query, _ := attachmentFixture(t)
	started := make(chan struct{})
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: canceledAttachmentBody{r.Context()}}, nil
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- attachmentHTTP(h.m, query) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	h.m.mu.Lock()
	waits := h.m.cancelPlatformCallsLocked(func(call *platformCall) bool { return call.owner == h.room.RoomIdentity })
	h.m.mu.Unlock()
	if len(waits) != 1 {
		t.Fatal("attachment missing from lifecycle registry", len(waits))
	}
	select {
	case w := <-done:
		if w.Code == 200 || strings.Contains(w.Body.String(), "test-platform-secret") {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled download did not settle")
	}
	select {
	case <-waits[0]:
	default:
		t.Fatal("download lifecycle not released")
	}
}

func TestPlatformAttachmentUpstreamFailureIsBoundedAndGeneric(t *testing.T) {
	h, _, _, query, _ := attachmentFixture(t)
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("test-platform-secret remote-error"))}, nil
	})
	w := attachmentHTTP(h.m, query)
	if w.Code != 502 || strings.Contains(w.Body.String(), "test-platform-secret") || strings.Contains(w.Body.String(), "remote-error") || !strings.Contains(w.Body.String(), "attachment_unavailable") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestPlatformAttachmentTodosAndRetiredSubscription(t *testing.T) {
	h, svc, id, query, data := attachmentFixture(t)
	events := h.state()
	state, err := h.m.configureTodosSubscription(context.Background(), notification.Key{Owner: h.room.RoomIdentity, Source: gitlab.TodosSourceID}, 0, gitlab.TodosConfig{Enabled: true, ConnectionID: events.Binding.ConnectionID, ProjectScope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	todo := gitlab.Todo{ID: 99, ProjectID: 7, Project: "remote/project", TargetType: "Issue", TargetIID: 2, Reason: "mentioned", Body: "![image](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png)", CreatedAt: events.Baseline}
	item := gitlab.TodosMessage(state, gitlab.TodosInboxEntry{DeliveryID: "todo-upload", Todo: todo})
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), item); err != nil {
		t.Fatal(err)
	}
	query.Set("seq", "2")
	query.Set("agent_id", id.AgentID)
	query.Set("membership_id", id.MembershipID)
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	if w := attachmentHTTP(h.m, query); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Losing the admitted subscription while the body is being read revokes it.
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		h.m.mu.Lock()
		current, _ := h.m.todosSubscriptionLocked(state.Metadata().Key)
		current.Retired = true
		current.Config.Enabled = false
		current.Status = "disabled"
		err := h.m.notifications.SaveLocked(current)
		h.m.mu.Unlock()
		if err != nil {
			t.Error(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	if w := attachmentHTTP(h.m, query); w.Code == 200 || bytes.Contains(w.Body.Bytes(), data) {
		t.Fatal("retired subscription returned image", w.Code)
	}
}
