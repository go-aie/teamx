package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/tool"
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
	m.mu.Lock()
	n := len(m.platformCalls)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("platform call leak", n)
	}
}
func TestReferenceActionInternalHTTPWithoutCLI(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		t.Run(map[bool]string{false: "object", true: "discussion"}[discussion], func(t *testing.T) {
			f := managedActionTest(t, discussion)
			if err := os.Remove(f.m.toolCatalog[0].Executable); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEAMX_GITLAB_BIN", filepath.Join(t.TempDir(), "missing"))
			t.Setenv("TEAMX_GLAB_BIN", "/missing/glab")
			t.Setenv("PATH", t.TempDir())
			config := filepath.Join(f.m.roomDir(f.room.RoomID), "room-config.json")
			saved, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, []byte("invalid process-only config"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.WriteFile(config, saved, 0600) })
			server := httptest.NewServer(f.m)
			defer server.Close()
			payload, _ := json.Marshal(agentActionFixture(f.action))
			var output, stderr bytes.Buffer
			err = runCLI(context.Background(), []string{"actions", "execute", "--request", string(payload)}, &output, &stderr, cliToolEnv(server.URL, f.svc, f.member))
			var got referenceActionReceipt
			if err == nil {
				err = json.Unmarshal(output.Bytes(), &got)
			}
			if err != nil || got.Status != "succeeded" || got.Attempts != 1 || got.Result == nil || got.Result.Account.ID.Value != "101" || got.Result.ResultID.Value != "99" {
				t.Fatal(got, err, stderr.String())
			}
			again, err := f.run()
			if err != nil || again.Status != "succeeded" || f.posts.Load() != 1 {
				t.Fatal(again, err, f.posts.Load())
			}
			original := f.action
			f.action.Input.Body = "different"
			if _, err := f.run(); !errors.Is(err, ErrConflict) {
				t.Fatal("content mismatch accepted", err)
			}
			f.action = original
			f.req.ServiceEpoch = "stale"
			if _, err := f.run(); !errors.Is(err, ErrIdentity) {
				t.Fatal("stale service accepted", err)
			}
			f.req.ServiceInfo = f.svc.info
			assertActionCallsSettled(t, f.m)
			if err := os.WriteFile(config, saved, 0600); err != nil {
				t.Fatal(err)
			}
			dir := f.svc.store.dir
			if err := f.m.close(); err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(dir, f.id.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			prior, err := store.existingReferenceAction(f.id, f.action)
			if err != nil || prior == nil || prior.Status != "succeeded" {
				t.Fatal(prior, err)
			}
		})
	}
}
func TestReferenceActionInternalRetriesAndUnknown(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		for _, mode := range []string{"403", "429", "502", "invalid", "missing-thread", "transport"} {
			t.Run(map[bool]string{false: "object", true: "discussion"}[discussion]+"/"+mode, func(t *testing.T) {
				f := managedActionTest(t, discussion)
				base := managedReferenceHTTP(t, f.m, func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "POST" {
						f.posts.Add(1)
						switch mode {
						case "403":
							w.WriteHeader(403)
						case "429":
							w.WriteHeader(429)
						case "502":
							w.WriteHeader(502)
						case "invalid":
							_, _ = io.WriteString(w, `{}`)
						default:
							w.WriteHeader(201)
							_, _ = io.WriteString(w, managedActionNote)
						}
						return
					}
					if mode == "missing-thread" && f.posts.Load() > 0 && strings.HasSuffix(r.URL.Path, "/discussions/thread") {
						_, _ = io.WriteString(w, `{"id":"thread","notes":[]}`)
						return
					}
					f.serve(w, r)
				})
				if mode == "transport" {
					f.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
						if r.Method == "POST" {
							f.posts.Add(1)
							return nil, errors.New("private raw transport message")
						}
						return base.RoundTrip(r)
					})
				}
				want := "unknown"
				attempts := 1
				if mode == "403" || mode == "429" {
					want = "not_applied"
				}
				if !discussion && (mode == "429" || mode == "502" || mode == "transport") {
					attempts = 2
				}
				if !discussion && mode == "missing-thread" {
					want = "succeeded"
				}
				got, err := f.run()
				if err != nil || got.Status != want || got.Attempts != attempts || int(f.posts.Load()) != attempts {
					t.Fatal(got, err, f.posts.Load())
				}
				again, err := f.run()
				if err != nil || again.Status != want || int(f.posts.Load()) != attempts {
					t.Fatal(again, err)
				}
				if want == "unknown" {
					f.action.ID = "other-id"
					f.action.Revision = got.UnitVersion
					if _, err := f.run(); err == nil || int(f.posts.Load()) != attempts {
						t.Fatal("unknown replayed", err, f.posts.Load())
					}
				}
				assertActionCallsSettled(t, f.m)
			})
		}
	}
}
func TestReferenceActionInternalRequiresBinding(t *testing.T) {
	f := managedActionTest(t, false)
	response := managerHTTP(f.m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: f.m.epoch, RoomIdentity: f.id.RoomIdentity, Caller: humanCaller, Revision: 1, Binding: tool.Binding{AgentID: f.id.AgentID, MembershipID: f.id.MembershipID, Tool: "gitlab"}})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if got, err := f.run(); err == nil || f.posts.Load() != 0 {
		t.Fatal(got, err)
	}
	assertActionCallsSettled(t, f.m)
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

func TestReferenceActionInternalRevocationSettles(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		for _, stage := range []string{"before-send", "body", "returned"} {
			for _, change := range []string{"rotate-credential", "reauthorize", "disable", "switch-binding", "remove-member", "session-rotate", "close-room", "delete-room", "shutdown"} {
				t.Run(map[bool]string{false: "object", true: "discussion"}[discussion]+"/"+stage+"/"+change, func(t *testing.T) {
					f := managedActionTest(t, discussion)
					m, svc, id, c := f.m, f.svc, f.id, f.connection
					if change == "session-rotate" {
						target, err := svc.rt.Spawn(&MemberAgent{Options: adapterOptions(svc.store, id), Store: svc.store, Room: svc.room, Backend: dshBackend{store: svc.store}})
						if err != nil {
							t.Fatal(err)
						}
						svc.setTarget(id.MembershipID, target)
						svc.dshProfiles = map[string]dshProfile{f.member.Runner.Profile: {}}
					}
					next := c
					if change == "switch-binding" {
						q := platformTestRequest(m)
						q.ID = strings.Repeat("b", 32)
						q.Name = "Second account"
						next = verifyTodoAccount(t, m, platformTestSave(t, m, q), 202)
					}
					entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
					var once sync.Once
					finish := func() { once.Do(func() { close(release) }) }
					defer finish()
					base := m.platformHTTP
					var users atomic.Int32
					m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
						// Execute re-inspects after the intent is durable. Block its user read,
						// so this stage proves revocation prevents the first business POST.
						if stage == "before-send" && r.URL.Path == "/api/v4/user" && users.Add(1) == 2 {
							close(entered)
							<-r.Context().Done()
							close(canceled)
							<-release
							return nil, r.Context().Err()
						}
						response, err := base.RoundTrip(r)
						if err == nil && r.Method == "POST" {
							response.Body = &actionBlockedBody{ReadCloser: response.Body, ctx: r.Context(), entered: entered, canceled: canceled, release: release, onClose: stage == "returned"}
						}
						return response, err
					})
					type outcome struct {
						receipt referenceActionReceipt
						err     error
					}
					done := make(chan outcome, 1)
					go func() { r, e := f.run(); done <- outcome{r, e} }()
					awaitEventSignal(t, entered, "action did not reach stage")
					changed := make(chan error, 1)
					go func() {
						var err error
						switch change {
						case "rotate-credential":
							q := platformTestRequest(m)
							q.Version = c.Version

							q.ReferencedRooms = []RoomIdentity{id.RoomIdentity}
							_, err = testReauthorizeConnection(m, q)
						case "reauthorize":
							_, err = testReauthorizeConnection(m, platformConnectionRequest{ID: c.ID, Version: c.Version, ReferencedRooms: []RoomIdentity{id.RoomIdentity}})
						case "disable", "switch-binding":
							q := toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: id.RoomIdentity, Caller: humanCaller, Revision: 1, Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "gitlab"}}
							if change == "switch-binding" {
								q.Enabled = true
								q.Binding.ConnectionID = next.ID
								q.ConnectionVersion = next.Version
							}
							r := managerHTTP(m, "/v1/tools/bind", q)
							if r.Code != 200 {
								err = errors.New(r.Body.String())
							}
						case "remove-member":
							_, err = svc.remove(apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID})
						case "session-rotate":
							_, err = svc.rotateSession(context.Background(), apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, Cancel: true})
						case "close-room":
							err = m.shutdownRuntime(context.Background(), id.RoomIdentity, svc, nil)
						case "delete-room":
							m.mu.Lock()
							_, err = m.startDeletion(id.RoomIdentity)
							completed := m.runtimes[id.RoomIdentity].operationDone
							m.mu.Unlock()
							if err == nil {
								select {
								case <-completed:
								case <-time.After(5 * time.Second):
									err = errors.New("deletion did not settle")
								}
							}
						case "shutdown":
							err = m.close()
						}
						changed <- err
					}()
					awaitEventSignal(t, canceled, "revocation did not cancel transport")
					m.mu.Lock()
					pending := len(m.platformCalls)
					m.mu.Unlock()
					if pending == 0 {
						t.Fatal("response released ownership early", pending)
					}
					select {
					case err := <-changed:
						t.Fatal("revocation returned before settlement", err)
					default:
					}
					finish()
					var result outcome
					select {
					case result = <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("action leaked")
					}
					want := "unknown"
					count := 1
					if stage == "before-send" {
						want = "not_applied"
						count = 0
					}
					if stage == "returned" && !discussion {
						want = "succeeded"
					}
					if result.err != nil || result.receipt.Status != want || int(f.posts.Load()) != count || result.receipt.Result == nil || result.receipt.Result.Account.ID.Value != "101" {
						t.Fatal(result, f.posts.Load(), want, count)
					}
					select {
					case err := <-changed:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("revocation leaked")
					}
					assertActionCallsSettled(t, m)
					if change != "delete-room" && change != "shutdown" {
						var record storedAction
						err := readSnapshot(svc.store.actionPath(id, f.action.ID), &record, followupBytes)
						saved := record.Action
						if err != nil || saved.Status != want || saved.Result == nil || saved.Result.Account.ID.Value != "101" {
							t.Fatal("write fact lost", saved, err)
						}
					}
				})
			}
		}
	}
}

func TestReferenceActionInternalResultSaveFailure(t *testing.T) {
	f := managedActionTest(t, false)
	ops := f.svc.store.ops
	base := f.m.platformHTTP
	f.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		response, err := base.RoundTrip(r)
		if r.Method == "POST" && err == nil {
			f.svc.store.mu.Lock()
			f.svc.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("injected result save failure") }
			f.svc.store.mu.Unlock()
		}
		return response, err
	})
	if _, err := f.run(); err == nil || !strings.Contains(err.Error(), "action outcome may exist") {
		t.Fatal("save failure hidden", err)
	}
	assertActionCallsSettled(t, f.m)
	f.svc.store.mu.Lock()
	f.svc.store.ops = ops
	f.svc.store.mu.Unlock()
	if f.posts.Load() != 1 {
		t.Fatal(f.posts.Load())
	}
	dir := f.svc.store.dir
	if err := f.m.close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir, f.id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prior, err := store.existingReferenceAction(f.id, f.action)
	if err != nil || prior == nil || prior.Status != "unknown" || prior.Attempts != 1 {
		t.Fatal(prior, err)
	}
}
