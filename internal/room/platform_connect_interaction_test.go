package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func interactionRequest(svc *service, op platformConnectOperation) platformConnectInteractionRequest {
	return platformConnectInteractionRequest{ServiceInfo: svc.info, Caller: humanCaller, OperationID: op.ID, Version: op.Version}
}
func interactionList(t *testing.T, m *roomManager, req platformConnectInteractionRequest) []platformConnectInteractionView {
	t.Helper()
	req.OperationID = ""
	req.Version = 0
	w := managerHTTP(m, "/v1/platforms/interactions/list", req)
	var result struct {
		Interactions []platformConnectInteractionView `json:"interactions"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return result.Interactions
}
func TestPlatformConnectInteractionProjectionAndBoundaries(t *testing.T) {
	m, svc, _, req := connectFixture(t)
	op, s := connectPending(t, m, svc, req)
	m.mu.Lock()
	s.Status = "waiting"
	s.URL = "https://open.feishu.cn/page/cli?secret=temporary"
	s.QRCode = "data:image/png;base64,iVBORw0KGgo="
	m.mu.Unlock()
	human := interactionRequest(svc, op)
	before, _ := os.ReadFile(m.connectPath(op.ID))
	views := interactionList(t, m, human)
	if len(views) != 1 || views[0].SourceSeq != op.Source.Seq || views[0].MemberIdentity != op.MemberIdentity || views[0].Interaction.URL != s.URL || views[0].Interaction.QRCodeURL == "" {
		t.Fatal(views)
	}
	after, _ := os.ReadFile(m.connectPath(op.ID))
	if !bytes.Equal(before, after) || bytes.Contains(after, []byte("temporary")) {
		t.Fatal("projection persisted live material")
	}
	// A browser may observe its own operation without weakening the Agent API.
	humanList := human
	humanList.OperationID = ""
	humanList.Version = 0
	data, _ := json.Marshal(humanList)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://example.com/v1/platforms/interactions/list", bytes.NewReader(data))
	r.Header.Set("Origin", "http://example.com")
	m.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("same-origin browser rejected", w.Code, w.Body.String())
	}
	for _, change := range []func(*platformConnectInteractionRequest){
		func(r *platformConnectInteractionRequest) { r.Caller = req.Caller },
		func(r *platformConnectInteractionRequest) { r.Caller.ID = "other-human" },
		func(r *platformConnectInteractionRequest) { r.RoomInstance = "wrong" },
		func(r *platformConnectInteractionRequest) { r.ServiceEpoch = "old" },
	} {
		changed := human
		change(&changed)
		if w := managerHTTP(m, "/v1/platforms/interactions/cancel", changed); w.Code != 403 {
			t.Fatal("foreign cancellation admitted", w.Code, w.Body.String())
		}
	}
	other := human
	other.Caller.ID = "other-human"
	if got := interactionList(t, m, other); len(got) != 0 {
		t.Fatal("another operator saw authorization", got)
	}
	// Native session cancellation must not bypass the operation's durable fence.
	if w := managerHTTP(m, "/v1/platforms/lark/cancel", larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, SessionID: s.SessionID}); w.Code != 409 {
		t.Fatal("raw session bypass", w.Code)
	}
	// Wrong-origin requests are still blocked by the manager boundary.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/v1/platforms/interactions/list", bytes.NewReader(data))
	r.Header.Set("Origin", "https://foreign.invalid")
	m.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin admitted", w.Code)
	}
}
func TestPlatformConnectInteractionVersionedCancellationAndDuplicateReceipt(t *testing.T) {
	m, svc, _, req := connectFixture(t)
	op, s := connectPending(t, m, svc, req)
	human := interactionRequest(svc, op)
	stale := human
	stale.Version++
	if w := managerHTTP(m, "/v1/platforms/interactions/cancel", stale); w.Code != 409 {
		t.Fatal("stale form admitted", w.Code)
	}
	original := s.cancel
	s.cancel = func() { original(); go connectSucceed(t, m, s) }
	w := managerHTTP(m, "/v1/platforms/interactions/cancel", human)
	var view platformConnectInteractionView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Status != "cancelled" || view.Connection.Status == "connected" {
		t.Fatal(w.Code, w.Body.String())
	}
	replay := managerHTTP(m, "/v1/platforms/interactions/cancel", human)
	if replay.Code != 200 || replay.Body.String() != w.Body.String() {
		t.Fatal("duplicate click changed receipt", replay.Code, replay.Body.String())
	}
	views := interactionList(t, m, human)
	if len(views) != 1 || views[0].Status != "cancelled" || views[0].Interaction.URL != "" {
		t.Fatal(views)
	}
}
func TestPlatformConnectInteractionReplacementCannotInheritLiveAuthorization(t *testing.T) {
	m, svc, member, req := connectFixture(t)
	op, s := connectPending(t, m, svc, req)
	human := interactionRequest(svc, op)
	m.mu.Lock()
	s.Status = "waiting"
	s.URL = "https://open.feishu.cn/page/cli?temporary"
	s.QRCode = "data:image/png;base64,iVBORw0KGgo="
	m.mu.Unlock()
	qrURL := interactionList(t, m, human)[0].Interaction.QRCodeURL
	if _, err := svc.room.Call[MemberInfo](context.Background(), RemoveMember{op.MemberIdentity}); err != nil {
		t.Fatal(err)
	}
	target, err := svc.rt.Spawn(notificationSink{})
	if err != nil {
		t.Fatal(err)
	}
	replacement := op.MemberIdentity
	replacement.MembershipID = "replacement"
	if _, err = svc.room.Call[MemberInfo](context.Background(), AddMember{MemberIdentity: replacement, Runner: member.Runner, Target: target}); err != nil {
		t.Fatal(err)
	}
	if w := managerHTTP(m, qrURL, nil); w.Code != 403 {
		t.Fatal("stale QR authorization survived replacement", w.Code)
	}
	views := interactionList(t, m, human)
	if len(views) != 1 || views[0].TargetAvailable || views[0].Interaction.URL != "" {
		t.Fatal(views)
	}
	originalCancel := s.cancel
	s.cancel = func() { originalCancel(); go connectSucceed(t, m, s) }
	w := managerHTTP(m, "/v1/platforms/interactions/cancel", human)
	var receipt platformConnectInteractionView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Status != "cancelled" || receipt.TargetAvailable || receipt.MembershipID == replacement.MembershipID || receipt.Connection.Status == "connected" {
		t.Fatal("original human could not settle the removed member's operation", w.Code, w.Body.String())
	}
}
