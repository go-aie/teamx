package room

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

// Exercise the real NewReadAPI HTTP client and body consumption against TLS;
// only the destination is remapped from the fixture's saved connection origin.
func managedReferenceHTTP(t *testing.T, m *roomManager, handler http.HandlerFunc) http.RoundTripper {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	destination, _ := url.Parse(server.URL)
	transport := workspaceHTTPFunc(func(req *http.Request) (*http.Response, error) {
		copy := req.Clone(req.Context())
		target := *req.URL
		target.Scheme, target.Host = destination.Scheme, destination.Host
		copy.URL = &target
		return server.Client().Transport.RoundTrip(copy)
	})
	m.platformHTTP = transport
	return transport
}
func managedReferencePayload(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v4/user":
		_, _ = io.WriteString(w, `{"id":101,"username":"user101"}`)
	case "/api/v4/projects/123/merge_requests/42":
		_, _ = io.WriteString(w, `{"id":9001,"iid":42,"project_id":123,"web_url":"https://gitlab.invalid/g/p/-/merge_requests/42"}`)
	case "/api/v4/projects/123/merge_requests/42/discussions":
		_, _ = io.WriteString(w, `[{"id":"thread","individual_note":false,"notes":[{"id":101}]}]`)
	default:
		w.WriteHeader(404)
	}
}
func managedReferenceInput(t *testing.T, m *roomManager, room roomSummary, svc *service, member MemberState, native ...bool) (MemberIdentity, apiRequest, platform.SourceRef) {
	t.Helper()
	m.bindPlatformReferences(svc)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	if err := svc.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	source, _, _ := gitlabReferenceFixture()
	source.Instance = "https://gitlab.invalid"
	source.SubscriptionID = strings.Repeat("a", 32)
	source.ItemKey = "internal-reference"
	source.URL = "https://gitlab.invalid/g/p/-/merge_requests/42#note_101"
	source.Reference.RecipientID.Value = "101"
	if len(native) == 0 || !native[0] {
		source.Reference.Object.ID = platform.ExternalID{}
	}
	h := &inboxHarness{id: id}
	q := h.receive("internal-reference")
	q.Item.Source = source
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), q.Item); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	return id, apiRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), TurnNumber: 1, Limit: 25}, source
}

type referenceBlockedBody struct {
	io.ReadCloser
	ctx                        context.Context
	entered, canceled, release chan struct{}
	once                       sync.Once
}

func (b *referenceBlockedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.ctx.Done(); close(b.canceled); <-b.release })
	return b.ReadCloser.Read(p)
}
