//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

func TestReferenceInternalHTTPWithoutCLIAndConsumedRecovery(t *testing.T) {
	m, room, svc, c, members := cliToolFixture(t, "exit 99\n")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	id, req, source := managedReferenceInput(t, m, room, svc, members[0])
	if err := os.Remove(m.toolCatalog[0].Executable); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEAMX_GITLAB_BIN", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("TEAMX_GLAB_BIN", "/missing/glab")
	t.Setenv("PATH", t.TempDir())
	var forbidden atomic.Bool
	forbidden.Store(true)
	var requests atomic.Int32
	managedReferenceHTTP(t, m, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-platform-secret" {
			t.Error("unexpected method/credential")
		}
		if forbidden.Load() && strings.HasSuffix(r.URL.Path, "/42") {
			w.WriteHeader(403)
			return
		}
		managedReferencePayload(w, r)
	})
	// Internal admission must not read even an unavailable Workspace config.
	config := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
	saved, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("invalid process-only config"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(config, saved, 0600) })
	page, err := m.readReferenceInbox(context.Background(), req)
	if err != nil || len(page.Sources) != 1 || page.Sources[0].ReasonCode != "permission_denied" || len(page.Followups) != 0 || requests.Load() != 2 {
		t.Fatal(page, err, requests.Load())
	}
	link, err := svc.store.readFollowupSource(id, 1, page.Sources[0].SourceRef)
	if err != nil || !strings.Contains(link.Resolution.Reason, "对象读取失败（HTTP 403）") {
		t.Fatal(link, err)
	}
	svc.inboxMu.Lock()
	inbox, err := svc.inboxLocked(context.Background(), id)
	svc.inboxMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.Call[InboxStatus](context.Background(), ConfirmInboxBatch{MemberIdentity: id, TurnNumber: 1, Receipt: page.Batch.Receipt}); err != nil {
		t.Fatal(err)
	}
	consumption := filepath.Join(svc.store.dir, "inboxes", id.MembershipID, "consumption.json")
	before, err := os.ReadFile(consumption)
	if err != nil {
		t.Fatal(err)
	}
	forbidden.Store(false)
	if _, err := svc.resolveFollowupEvent(context.Background(), id, 1, 1, false, svc.referenceResolver); err != nil || requests.Load() != 2 {
		t.Fatal("ordinary reread retried remote", err, requests.Load())
	}
	recovered, err := svc.resolveFollowupEvent(context.Background(), id, 1, 1, true, svc.referenceResolver)
	if err != nil || recovered.ObjectRef == "" || !recovered.Resolution.DiscussionVerified || recovered.Seq != 1 || recovered.SourceRef != link.SourceRef || recovered.DeliveryID != link.DeliveryID || recovered.Source != source || requests.Load() != 5 {
		t.Fatal(recovered, err, requests.Load())
	}
	repeated, err := svc.resolveFollowupEvent(context.Background(), id, 1, 1, true, svc.referenceResolver)
	if err != nil || repeated.ObjectRef != recovered.ObjectRef {
		t.Fatal(repeated, err)
	}
	units, err := svc.store.listFollowups(id, followupQuery{Limit: 25})
	if err != nil || len(units.Items) != 1 {
		t.Fatal(units, err)
	}
	after, err := os.ReadFile(consumption)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("recovery changed Inbox consumption", err)
	}
	forbidden.Store(true)
	failed, err := svc.resolveFollowupEvent(context.Background(), id, 1, 1, true, svc.referenceResolver)
	if err != nil || failed.LastAttempt == nil || !failed.Resolution.ObjectVerified || failed.ObjectRef != recovered.ObjectRef {
		t.Fatal("verified identity lost", failed, err)
	}
	if err := os.WriteFile(config, saved, 0600); err != nil {
		t.Fatal(err)
	}
	dir := svc.store.dir
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened, err := store.readFollowupSource(id, 1, link.SourceRef)
	if err != nil || reopened.LastAttempt == nil || reopened.ObjectRef != recovered.ObjectRef || reopened.Source != source {
		t.Fatal(reopened, err)
	}
}

func TestReferenceInternalRequiresSelectedMemberConnection(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "exit 99\n")
	id, req, _ := managedReferenceInput(t, m, room, svc, members[0])
	requests := 0
	m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) { requests++; return nil, errors.New("must not query") })
	page, err := m.readReferenceInbox(context.Background(), req)
	if err != nil || requests != 0 || len(page.Followups) != 0 || page.Sources[0].ReasonCode != "binding_unavailable" {
		t.Fatal(page, err, requests)
	}
	bindCLITool(t, m, room.RoomIdentity, members[1], c, "gitlab", 0)
	result, _, err := m.resolveGitlabFollowup(context.Background(), id, referenceTestSource("other-member"))
	if err == nil || result.ObjectVerified || requests != 0 {
		t.Fatal("borrowed another member connection", result, err)
	}
	// Merely asking for action descriptions remains credential and I/O free.
	if specs := svc.referenceSpecs("gitlab"); len(specs) != 2 || requests != 0 {
		t.Fatal(specs, requests)
	}
}

func TestReferenceInternalRevocationSettlesHTTPAndRejectsLateResults(t *testing.T) {
	for _, stage := range []string{"before-send", "body", "returned"} {
		for _, action := range []string{"rotate-credential", "reauthorize", "disable", "switch-binding", "remove-member", "session-rotate", "close-room", "delete-room", "shutdown"} {
			t.Run(stage+"/"+action, func(t *testing.T) {
				parallelFixtureTest(t)
				m, room, svc, c, members := cliToolFixture(t, "exit 99\n")
				bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
				id, req, _ := managedReferenceInput(t, m, room, svc, members[0])
				if action == "session-rotate" {
					target, err := svc.rt.Spawn(&MemberAgent{Options: adapterOptions(svc.store, id), Store: svc.store, Room: svc.room, Backend: dshBackend{store: svc.store}})
					if err != nil {
						t.Fatal(err)
					}
					svc.setTarget(id.MembershipID, target)
					svc.dshProfiles = map[string]dshProfile{members[0].Runner.Profile: {}}
				}
				nextConnection := c
				if action == "switch-binding" {
					q := platformTestRequest(m)
					q.ID = strings.Repeat("b", 32)
					q.Name = "Second account"
					nextConnection = verifyTodoAccount(t, m, platformTestSave(t, m, q), 202)
				}
				entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				finish := func() { releaseOnce.Do(func() { close(release) }) }
				defer finish()
				base := managedReferenceHTTP(t, m, managedReferencePayload)
				m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
					if stage == "before-send" && strings.HasSuffix(r.URL.Path, "/user") {
						close(entered)
						<-r.Context().Done()
						close(canceled)
						<-release
					}
					response, err := base.RoundTrip(r)
					if err == nil && stage == "body" && strings.HasSuffix(r.URL.Path, "/discussions") {
						response.Body = &referenceBlockedBody{ReadCloser: response.Body, ctx: r.Context(), entered: entered, canceled: canceled, release: release}
					}
					return response, err
				})
				if stage == "returned" {
					original := m.referenceResolvers["gitlab"]
					m.referenceResolvers["gitlab"] = func(ctx context.Context, id MemberIdentity, s platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
						r, a, e := original(ctx, id, s)
						scope := ctx.Value(referenceResolutionScopeKey{}).(*referenceResolutionScope)
						close(entered)
						<-scope.ctx.Done()
						close(canceled)
						<-release
						return r, a, e
					}
				}
				resolveDone := make(chan error, 1)
				go func() {
					page, err := m.readReferenceInbox(context.Background(), req)
					if len(page.Followups) != 0 {
						err = errors.New("canceled old response created object")
					}
					resolveDone <- err
				}()
				awaitEventSignal(t, entered, "request did not reach selected stage")
				actionDone := make(chan error, 1)
				go func() {
					var err error
					switch action {
					case "rotate-credential":
						q := platformTestRequest(m)
						q.Version = c.Version

						q.ReferencedRooms = []RoomIdentity{room.RoomIdentity}
						_, err = testReauthorizeConnection(m, q)
					case "reauthorize":
						_, err = testReauthorizeConnection(m, platformConnectionRequest{ID: c.ID, Version: c.Version, ReferencedRooms: []RoomIdentity{room.RoomIdentity}})
					case "disable", "switch-binding":
						change := toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Revision: 1, Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "gitlab"}}
						if action == "switch-binding" {
							change.Enabled = true
							change.Binding.ConnectionID = nextConnection.ID
							change.ConnectionVersion = nextConnection.Version
						}
						response := managerHTTP(m, "/v1/tools/bind", change)
						if response.Code != 200 {
							err = errors.New(response.Body.String())
						}
					case "remove-member":
						_, err = svc.remove(apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID})

					case "session-rotate":
						_, err = svc.rotateSession(context.Background(), apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, Cancel: true})
					case "close-room":
						err = m.shutdownRuntime(context.Background(), room.RoomIdentity, svc, nil)
					case "delete-room":
						m.mu.Lock()
						_, err = m.startDeletion(room.RoomIdentity)
						done := m.runtimes[room.RoomIdentity].operationDone
						m.mu.Unlock()
						if err == nil {
							select {
							case <-done:
							case <-time.After(5 * time.Second):
								err = errors.New("deletion did not settle")
							}
						}
					case "shutdown":
						err = m.close()
					}
					actionDone <- err
				}()
				awaitEventSignal(t, canceled, "revocation did not cancel request context")
				m.mu.Lock()
				pending := len(m.platformCalls)
				m.mu.Unlock()
				if pending == 0 {
					t.Fatal("body/result processing released call early", pending)
				}
				select {
				case err := <-actionDone:
					t.Fatal("revocation returned before request settled", err)
				default:
				}
				finish()
				select {
				case err := <-resolveDone:
					if err == nil && stage == "returned" {
						t.Fatal("late successful response accepted")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("resolution leaked")
				}
				select {
				case err := <-actionDone:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("revocation leaked")
				}
				m.mu.Lock()
				pending = len(m.platformCalls)
				m.mu.Unlock()
				if pending != 0 {
					t.Fatal("call leaked", pending)
				}
				if action != "delete-room" && action != "shutdown" {
					entries, err := os.ReadDir(filepath.Join(svc.store.followupRoot(id), "objects"))
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					if len(entries) != 0 {
						t.Fatal("revoked identity persisted")
					}
				}
			})
		}
	}
}

func TestReferenceInternalResolutionScopeCommitGate(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "exit 99\n")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	_, req, _ := managedReferenceInput(t, m, room, svc, members[0])
	managedReferenceHTTP(t, m, managedReferencePayload)
	original := svc.referenceCommit
	svc.referenceCommit = func(ctx context.Context, id MemberIdentity, a platform.AccessContext, commit func() (followupEvent, error)) (followupEvent, error) {
		m.mu.Lock()
		m.cancelPlatformCallsLocked(func(c *platformCall) bool { return c.owner == id.RoomIdentity })
		m.mu.Unlock()
		return original(ctx, id, a, commit)
	}
	if page, err := m.readReferenceInbox(context.Background(), req); err == nil || len(page.Followups) != 0 {
		b, _ := json.Marshal(page)
		t.Fatal("post-resolve cancellation committed", string(b), err)
	}
	m.mu.Lock()
	pending := len(m.platformCalls)
	m.mu.Unlock()
	if pending != 0 {
		t.Fatal("call not settled after commit rejection")
	}
}

func TestReferenceInternalPartialRefreshKeepsHistoricalIdentity(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "exit 99\n")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	id, req, source := managedReferenceInput(t, m, room, svc, members[0], true)
	var failAt atomic.Value
	failAt.Store("")
	managedReferenceHTTP(t, m, func(w http.ResponseWriter, r *http.Request) {
		if suffix := failAt.Load().(string); suffix != "" && strings.HasSuffix(r.URL.Path, suffix) {
			w.WriteHeader(403)
			return
		}
		managedReferencePayload(w, r)
	})
	page, err := m.readReferenceInbox(context.Background(), req)
	if err != nil || len(page.Followups) != 1 {
		t.Fatal(page, err)
	}
	before, err := svc.store.readFollowupSource(id, 1, page.Sources[0].SourceRef)
	if err != nil || !before.Resolution.DiscussionVerified {
		t.Fatal(before, err)
	}
	for _, step := range []string{"/discussions", "/42"} {
		failAt.Store(step)
		refreshed, err := svc.resolveFollowupEvent(context.Background(), id, 1, 1, true, svc.referenceResolver)
		if err != nil || refreshed.LastAttempt == nil || refreshed.LastAttempt.Resolution.Status != platform.ResolutionPartial || refreshed.LastAttempt.Resolution.ReasonCode != "permission_denied" || !refreshed.LastAttempt.Resolution.ObjectVerified || refreshed.LastAttempt.Resolution.DiscussionVerified || !refreshed.Resolution.DiscussionVerified || refreshed.TargetRef != before.TargetRef || refreshed.ObjectRef != before.ObjectRef || refreshed.Source != source {
			t.Fatal(refreshed, err)
		}
	}
	// A conflicting positive mapping is still rejected, even when marked partial.
	link, err := svc.store.readFollowupSource(id, 1, before.SourceRef)
	if err != nil {
		t.Fatal(err)
	}
	conflicting := link.Resolution
	target := *conflicting.Target
	target.Discussion.Value = "different-thread"
	conflicting.Target = &target
	conflicting.Status = platform.ResolutionPartial
	conflicting.Complete = false
	if _, err := svc.store.recordFollowupResolution(id, 1, 1, link.Revision, conflicting, link.Access); !errors.Is(err, ErrConflict) {
		t.Fatal("conflicting verified mapping accepted", err)
	}
	failAt.Store("")
	recovered, err := svc.resolveFollowupEvent(context.Background(), id, 1, 1, true, svc.referenceResolver)
	if err != nil || recovered.LastAttempt != nil || !recovered.Resolution.DiscussionVerified || recovered.TargetRef != before.TargetRef {
		t.Fatal(recovered, err)
	}
	failAt.Store("/discussions")
	if _, err := svc.resolveFollowupEvent(context.Background(), id, 1, 1, true, svc.referenceResolver); err != nil {
		t.Fatal(err)
	}
	dir := svc.store.dir
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened, err := store.readFollowupSource(id, 1, before.SourceRef)
	if err != nil || reopened.LastAttempt == nil || !reopened.LastAttempt.Resolution.ObjectVerified || !reopened.Resolution.DiscussionVerified || reopened.TargetRef != before.TargetRef {
		t.Fatal(reopened, err)
	}
}
