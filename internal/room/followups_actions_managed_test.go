package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

const managedActionNote = `{"id":99,"body":"exact reply","system":false,"noteable_id":9001,"noteable_iid":42,"noteable_type":"MergeRequest","author":{"id":101,"username":"user101"}}`

type managedActionFixture struct {
	m          *roomManager
	room       roomSummary
	svc        *service
	connection platformConnection
	member     MemberState
	id         MemberIdentity
	req        apiRequest
	action     referenceActionRequest
	posts      atomic.Int32
}

func (f *managedActionFixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-platform-secret" {
		w.WriteHeader(401)
		return
	}
	if r.Method == "POST" {
		f.posts.Add(1)
		want := "/api/v4/projects/123/merge_requests/42/notes"
		if f.action.Operation == platform.OperationDiscussionReply {
			want = "/api/v4/projects/123/merge_requests/42/discussions/thread/notes"
		}
		if r.URL.Path != want {
			w.WriteHeader(404)
			return
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["body"] != "exact reply" {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, managedActionNote)
		return
	}
	switch r.URL.Path {
	case "/api/v4/projects/123":
		_, _ = io.WriteString(w, `{"id":123,"path_with_namespace":"g/p"}`)
	case "/api/v4/projects/123/merge_requests/42/discussions/thread":
		_, _ = io.WriteString(w, `{"id":"thread","individual_note":false,"notes":[`+managedActionNote+`]}`)
	default:
		managedReferencePayload(w, r)
	}
}
func managedActionTest(t *testing.T, discussion bool) *managedActionFixture {
	t.Helper()
	m, room, svc, c, members := cliToolFixture(t, "exit 99\n")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	id, req, _ := managedReferenceInput(t, m, room, svc, members[0])
	f := &managedActionFixture{m: m, room: room, svc: svc, connection: c, member: members[0], id: id, req: req}
	managedReferenceHTTP(t, m, f.serve)
	page, err := m.readReferenceInbox(context.Background(), req)
	if err != nil || len(page.Followups) != 1 {
		t.Fatal(page, err)
	}
	unitRef := page.Followups[0].Ref
	if !discussion {
		unitRef = referenceDigest("unit_", page.Followups[0].ObjectRef)
	}
	u, err := svc.store.readFollowup(id, page.Followups[0].ObjectRef, unitRef)
	if discussion {
		u, err = svc.store.ensureDiscussionFollowup(id, 1, 1, page.Sources[0].TargetRef)
	}
	if err != nil {
		t.Fatal(err)
	}
	rule := policyRule()
	if _, err := svc.store.changePolicy(id, humanCaller, rule); err != nil {
		t.Fatal(err)
	}
	policy, err := svc.store.readAgentPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	op := platform.OperationObjectComment
	if discussion {
		op = platform.OperationDiscussionReply
	}
	f.action = referenceActionRequest{followupCommand: conversationCommand(u, 1), TargetRef: u.TargetRef, ID: "managed", PolicyToken: policy.PolicyToken, Authority: referenceActionAuthority{RuleID: rule.ID, RuleVersion: 1, Reason: "已核实文档问题"}, Operation: op, Input: CommentInput{Body: "exact reply"}}
	return f
}
func (f *managedActionFixture) run() (referenceActionReceipt, error) {
	return f.m.executeReferenceAction(context.Background(), f.req, f.action)
}
func assertActionCallsSettled(t *testing.T, m *roomManager) {
	t.Helper()
	// A client can read the terminal frame before the HTTP handler runs its
	// deferred lease/call settlement. Wait for that contract, not scheduler luck.
	m.mu.Lock()
	var pending []<-chan struct{}
	for call := range m.platformCalls {
		pending = append(pending, call.done)
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("platform calls did not settle", ctx.Err())
		}
	}
	m.mu.Lock()
	n := len(m.platformCalls)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("platform call leak", n)
	}
}

type actionBlockedBody struct {
	io.ReadCloser
	ctx                        context.Context
	entered, canceled, release chan struct{}
	onClose                    bool
	once                       sync.Once
}

func (b *actionBlockedBody) wait() {
	b.once.Do(func() { close(b.entered); <-b.ctx.Done(); close(b.canceled); <-b.release })
}
func (b *actionBlockedBody) Read(p []byte) (int, error) {
	if !b.onClose {
		b.wait()
		return 0, b.ctx.Err()
	}
	return b.ReadCloser.Read(p)
}
func (b *actionBlockedBody) Close() error {
	if b.onClose {
		b.wait()
	}
	return b.ReadCloser.Close()
}
