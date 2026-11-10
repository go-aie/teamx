package room

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestInboxPreviewStatusIdentityAndReadOnly(t *testing.T) {
	svc, err := startService(t.TempDir(), "inbox-status", serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer svc.close(context.Background())
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	post := integrationPost(id.RoomIdentity, 1)
	item := notification.Item{Key: post.Source.ItemKey, DeliveryID: post.DeliveryID, Text: post.Text, Source: post.Source, CreatedAt: time.Now().UTC(), Route: notification.Route{Target: inboxTarget(id), Version: 1}}
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), item); err != nil {
		t.Fatal(err)
	}
	read := func(req apiRequest) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		svc.ServeHTTP(w, httptest.NewRequest("POST", "/v1/member/inbox/status", strings.NewReader(string(body))))
		return w
	}
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID}
	for i := 0; i < 2; i++ {
		w := read(req)
		var status InboxStatus
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &status) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		if status.MemberIdentity != id || status.LatestSeq != 1 || status.RetainedEntries != 1 || status.ConsumedThrough != 0 || status.Batch != nil || status.AutomaticChecked {
			t.Fatal(status)
		}
	}
	// A legal reclaimed prefix changes the retained count without resetting seq.
	svc.inboxMu.Lock()
	addr, err := svc.inboxLocked(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = addr.Call[InboxStatus](context.Background(), ReclaimInbox{id, 1}); err != nil {
		t.Fatal(err)
	}
	svc.inboxMu.Unlock()
	w := read(req)
	var status InboxStatus
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || status.LatestSeq != 1 || status.ExpiredThrough != 1 || status.RetainedEntries != 0 || status.ConsumedThrough != 0 {
		t.Fatal(w.Body.String())
	}
	for _, kind := range []string{"agent", "human"} {
		bad := req
		if kind == "agent" {
			bad.Caller = Caller{Kind: "agent", ID: id.AgentID, MembershipID: id.MembershipID}
		} else {
			bad.MembershipID = strings.Repeat("2", 32)
		}
		if w := read(bad); w.Code == 200 {
			t.Fatal("accepted foreign identity", w.Body.String())
		}
	}
	bad := req
	bad.ServiceEpoch = "stale"
	if w := read(bad); w.Code == 200 {
		t.Fatal("accepted stale epoch")
	}
}
