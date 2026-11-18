package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	frozen "github.com/go-aie/teamx/internal/inputstreamtest/frozen"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func platformReaderFixture(t *testing.T) (*roomHarness, *platformInputStorage, *service, MemberIdentity) {
	t.Helper()
	h := newRoomHarness(t, historyRoom(1, "只读检查，不要评论或合并"), nil)
	id := h.memberID("A", "generation-A")
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 1, OfferedUnreadSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	p, err := h.store.openPlatformInputStorage(id, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	svc := &service{store: h.store, info: ServiceInfo{RoomIdentity: h.id, ServiceEpoch: "fixture"}}
	return h, p, svc, id
}
func startPlatformReader(t *testing.T, h *roomHarness, p *platformInputStorage, svc *service, turn uint64) *inputSession {
	t.Helper()
	s, err := newInputSession(h.ctx, svc, p, "fixture-session", turn, true, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func receivePlatform(t *testing.T, p *platformInputStorage, item notification.Item) {
	t.Helper()
	if _, err := p.Receive(item, time.Now()); err != nil {
		t.Fatal(err)
	}
}
func streamFor(t *testing.T, s *inputSession, text string) string {
	t.Helper()
	c, err := s.legacyCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.List(frozen.Query{Text: text, State: "all"})
	if err != nil || len(list.Streams) != 1 {
		t.Fatal(list, err)
	}
	return list.Streams[0].Ref
}
func platformArgs(turn uint64, stream, id string) []byte {
	b, _ := json.Marshal(inputstream.ReadRequest{Turn: turn, Stream: stream, ID: id})
	return b
}
func deliverPlatform(t *testing.T, s *inputSession, stream, id string) platformInputView {
	t.Helper()
	var out bytes.Buffer
	if err := s.writeCLI(context.Background(), platformArgs(s.execution.Turn, stream, id), &out, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var v platformInputView
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestPlatformReadSharedTransportExactSourcesAndRecovery(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	a1 := storedGitLab(id, "a1", "events", "7", "42", "A first")
	a1.Source.Reference.Object.ID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "142"}
	b := storedGitLab(id, "b1", "todos", "7", "56", "B pending")
	a3 := a1
	a3.Key = "a3"
	a3.DeliveryID = "a3"
	a3.Source.ItemKey = "a3"
	a3.Text = "A last"
	receivePlatform(t, p, a1)
	receivePlatform(t, p, b)
	receivePlatform(t, p, a3)
	l := storedLark(id, "c1", "oc_chat", "产品群", "omt_thread", "C message")
	l.Content.Conversation.Type = "group"
	receivePlatform(t, p, l)
	var resolves atomic.Int32
	svc.referenceResolver = func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		resolves.Add(1)
		return referenceTestResolver(ctx, id, source)
	}
	s := startPlatformReader(t, h, p, svc, 1)
	a := streamFor(t, s, "A first")
	c := streamFor(t, s, "C message")
	// Discovery and preview never create business links.
	cat, _ := s.legacyCatalog(h.ctx)
	if _, err := cat.Preview(a, 1); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(filepath.Join(h.store.followupRoot(id), "input-sources"))
	if len(files) != 0 {
		t.Fatal(files)
	}
	v := deliverPlatform(t, s, a, "A")
	if len(v.Entries) != 2 || v.Remaining != 23 || len(v.Followups) != 1 {
		t.Fatalf("%+v", v)
	}
	first := v.Entries[0].Source.Ref
	if first == v.Entries[1].Source.Ref {
		t.Fatal("source collision")
	}
	v2 := deliverPlatform(t, s, a, "A")
	if len(v2.Entries) != 2 || resolves.Load() != 2 {
		t.Fatal(v2, resolves.Load())
	}
	// MCP uses the SAME coordinator and source association service as CLI.
	mcpHTTP := newMCPHTTP()
	mcpHTTP.addLegacyPlatformInputTools(s)
	request := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"inbox_read","arguments":{"turn_number":1,"stream_ref":%q,"read_id":"C"}}}`, c)
	req := httptest.NewRequest("POST", "http://example/mcp", strings.NewReader(request))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	out := httptest.NewRecorder()
	mcpHTTP.ServeHTTP(out, req)
	if out.Code != 200 || strings.Contains(out.Body.String(), `"error"`) {
		t.Fatal(out.Code, out.Body.String())
	}
	state, err := p.repository.State(s.execution)
	if err != nil || state.Used != 3 || p.repository.Barrier(s.execution) != nil {
		t.Fatal(state, err, out.Body.String())
	}
	// Cross-provider Seq=1 remains distinct and source_ref alone finds its body.
	var sourceC string
	for _, attempt := range state.Attempts {
		if attempt.Stream == c {
			entries, err := s.entries(attempt)
			if err != nil {
				t.Fatal(err)
			}
			sourceC = inputSourceRef(entries[0])
		}
	}
	if first == sourceC {
		t.Fatal("cross-provider source collision")
	}
	link, entry, err := h.store.readFollowupSourceContent(id, 0, first)
	if err != nil || entry == nil || entry.Item.Text != "A first" || link.ObjectRef == "" {
		t.Fatal(link, entry, err)
	}
	_, centry, err := h.store.readFollowupSourceContent(id, 0, sourceC)
	if err != nil || centry == nil || centry.Item.Text != "C message" {
		t.Fatal(centry, err)
	}
	// Body projections do not leak physical positions or private envelope fields.
	encoded, _ := json.Marshal(v)
	for _, field := range []string{`"partition"`, `"seq"`, `"delivery_id"`, `"payload"`, `"receiver"`, `"project_id"`} {
		if bytes.Contains(encoded, []byte(field)) {
			t.Fatal(field, string(encoded))
		}
	}
	pending, err := p.repository.PendingPage(s.execution)
	if err != nil || pending == nil || pending.Stream != c || !pending.Confirmed {
		t.Fatal(pending, err)
	}
	catalog, _ := s.legacyCatalog(h.ctx)
	list, _ := catalog.List(frozen.Query{})
	if len(list.Streams) != 1 {
		t.Fatal(list)
	}
}

type shortInputWriter struct{}

func (shortInputWriter) Write(b []byte) (int, error) { return len(b) / 2, nil }
func TestPlatformReadOutputFailureBusyTerminalAndCrossEntry(t *testing.T) {
	for _, failure := range []string{"short", "flush", "http-short", "http-flush"} {
		t.Run(failure, func(t *testing.T) {
			h, p, svc, id := platformReaderFixture(t)
			receivePlatform(t, p, storedGitLab(id, "a", "events", "7", "42", "unique-a"))
			receivePlatform(t, p, storedGitLab(id, "b", "events", "7", "56", "unique-b"))
			s := startPlatformReader(t, h, p, svc, 1)
			a := streamFor(t, s, "unique-a")
			b := streamFor(t, s, "unique-b")
			if strings.HasPrefix(failure, "http") {
				http := newMCPHTTP()
				http.addLegacyPlatformInputTools(s)
				data := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"inbox_read","arguments":{"turn_number":1,"stream_ref":%q,"read_id":"a"}}}`, a)
				req := httptest.NewRequest("POST", "/mcp", strings.NewReader(data))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
				w := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder()}
				if failure == "http-short" {
					w.write = func(data []byte) (int, error) { return len(data) / 2, nil }
				} else {
					w.flush = func() error { return io.ErrClosedPipe }
				}
				http.ServeHTTP(w, req)
			} else {
				var w io.Writer = &bytes.Buffer{}
				flush := func() error { return nil }
				if failure == "short" {
					w = shortInputWriter{}
				} else {
					flush = func() error { return io.ErrClosedPipe }
				}
				if err := s.writeCLI(h.ctx, platformArgs(1, a, "a"), w, flush); err == nil {
					t.Fatal("accepted failed output")
				}
			}
			if err := p.repository.Barrier(s.execution); !errors.Is(err, inputstream.ErrBusy) {
				t.Fatal(err)
			}
			_, _, release, err := s.read(h.ctx, platformArgs(1, b, "b"))
			if release != nil {
				release()
			}
			if !errors.Is(err, inputstream.ErrBusy) {
				t.Fatal(err)
			}
			agent, err := h.store.LoadAgent(id)
			if err != nil {
				t.Fatal(err)
			}
			agent.Turn.Status = "completed"
			if err := h.store.SaveAgent(agent); !errors.Is(err, inputstream.ErrBusy) {
				t.Fatal(err)
			}
			state, _ := p.repository.State(s.execution)
			if state.Used != 1 {
				t.Fatal(state.Used)
			}
			if err := s.mutate(func(r *inputstream.Repository) error { return r.Abort(s.execution, state.Attempts[0].ID, "too late") }); !errors.Is(err, inputstream.ErrBusy) {
				t.Fatal(err)
			}
			v := deliverPlatform(t, s, a, "a")
			if len(v.Entries) != 1 || v.Remaining != 24 {
				t.Fatal(v)
			}
		})
	}
}
func TestPlatformReadAbortPartialAssociationAndRevocation(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	receivePlatform(t, p, storedGitLab(id, "a1", "events", "7", "42", "A first"))
	receivePlatform(t, p, storedGitLab(id, "a2", "events", "7", "42", "A second"))
	receivePlatform(t, p, storedGitLab(id, "b", "events", "7", "56", "unique-b"))
	s := startPlatformReader(t, h, p, svc, 1)
	var calls int
	svc.referenceResolver = func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		calls++
		return platform.TargetResolution{}, platform.AccessContext{}, nil
	}
	// The local source gate fails after one association, before result/handoff.
	svc.referenceCommit = nil
	original := s.validate
	var checks atomic.Int32
	s.validate = func(ctx context.Context) error {
		if checks.Add(1) == 4 {
			return &inputSourceUnavailable{"source unavailable"}
		}
		return original(ctx)
	}
	a := streamFor(t, s, "A first")
	b := streamFor(t, s, "unique-b")
	checks.Store(0)
	// Explicit fault seam is the source/material gate; does not fake consumption.
	svc.materialAttachments = &materialAttachments{checkInbox: func(context.Context, ServiceInfo, attachmentReader, []materialNotice) error {
		return &inputSourceUnavailable{"source unavailable"}
	}}
	_, _, release, err := s.read(h.ctx, platformArgs(1, a, "a"))
	if release != nil {
		release()
	}
	if err == nil {
		t.Fatal("expected local failure")
	}
	state, _ := p.repository.State(s.execution)
	if state.Used != 0 || state.Attempts[0].Phase != "failed_before_output" || len(state.Attempts[0].Entries) != 2 {
		t.Fatal(state)
	}
	files, _ := os.ReadDir(filepath.Join(h.store.followupRoot(id), "input-sources"))
	if len(files) != 2 {
		t.Fatal(files)
	}
	svc.materialAttachments = nil
	s.validate = original
	deliverPlatform(t, s, b, "b")
	_, _, release, err = s.read(h.ctx, platformArgs(1, a, "a"))
	if release != nil {
		release()
	}
	if !errors.Is(err, inputstream.ErrBlocked) {
		t.Fatal(err)
	}
	before := calls
	deliverPlatform(t, s, a, "recovered")
	if calls != before {
		t.Fatal("associations resolved twice", before, calls)
	}
	s.validate = func(context.Context) error { return ErrIdentity }
	_, _, release, err = s.read(h.ctx, platformArgs(1, a, "recovered"))
	if release != nil {
		release()
	}
	if !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
}

func TestPlatformReadSourceReleaseAndRoomOnlyRecovery(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	a1 := storedGitLab(id, "a1", "events", "7", "42", "alpha-first")
	a1.Source.Reference.Object.ID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "142"}
	a3 := a1
	a3.Key = "a3"
	a3.DeliveryID = "a3"
	a3.Source.ItemKey = "a3"
	a3.Text = "alpha-last"
	receivePlatform(t, p, a1)
	receivePlatform(t, p, storedGitLab(id, "b", "events", "7", "56", "beta-pending"))
	receivePlatform(t, p, a3)
	svc.referenceResolver = referenceTestResolver
	s := startPlatformReader(t, h, p, svc, 1)
	a := streamFor(t, s, "alpha-first")
	read := deliverPlatform(t, s, a, "a")
	source := read.Entries[0].Source.Ref
	agent, err := h.store.LoadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	agent.Turn.Status = "completed"
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	inventory := inputSourceInventory{Revision: "source-state-1", Pending: map[notification.SourceID]map[string]bool{{Provider: "gitlab", Channel: "events"}: {"a1": true}}}
	plan, err := p.maintain("gitlab", inventory, time.Now())
	if err != nil || plan.Usage.Records != 0 {
		t.Fatal(plan, err)
	}
	delete(inventory.Pending[notification.SourceID{Provider: "gitlab", Channel: "events"}], "a1")
	inventory.Revision = "source-state-2"
	plan, err = p.maintain("gitlab", inventory, time.Now())
	if err != nil || plan.Through != 1 || plan.Usage.Records != 1 {
		t.Fatal(plan, err)
	}
	link, entry, err := h.store.readFollowupSourceContent(id, 0, source)
	if err != nil || entry != nil || link.ObjectRef == "" {
		t.Fatal(link, entry, err)
	}
	// A new Room-only turn neither consumes the remaining B input nor erases
	// the last actual platform page. Its body may already be legally reclaimed.
	agent.TurnNumber = 2
	agent.Turn = &TurnRecord{Number: 2, Status: "running", SnapshotSeq: 1, OfferedUnreadSeq: 1}
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	s2 := startPlatformReader(t, h, p, svc, 2)
	page, err := p.repository.PendingPage(s2.execution)
	if err != nil || page == nil || page.Stream != a || len(page.Entries) != 2 {
		t.Fatal(page, err)
	}
	facts, err := h.store.recoveryBusinessContext(id, *agent.Turn)
	if err != nil || len(facts.matters.Items) != 1 || facts.matters.Items[0].ObjectRef != link.ObjectRef {
		t.Fatal(facts, err)
	}
	status, err := s2.readiness(h.ctx)
	if err != nil || status.Pending != 1 || status.Readable != 1 {
		t.Fatal(status, err)
	}
	sources, err := h.store.listInputSources(id, link.ObjectRef, "", 1)
	if err != nil || len(sources.Items) != 1 || sources.Next == "" {
		t.Fatal(sources, err)
	}
	next, err := h.store.listInputSources(id, link.ObjectRef, sources.Next, 1)
	if err != nil || len(next.Items) != 1 || next.Items[0].Ref == sources.Items[0].Ref {
		t.Fatal(next, err)
	}
}
func TestPlatformReadNewFormatCLIAndPlatformOnlyTurn(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	receivePlatform(t, p, storedGitLab(id, "a", "events", "7", "42", "unique-command"))
	s := startPlatformReader(t, h, p, svc, 1)
	stream := streamFor(t, s, "unique-command")
	var out bytes.Buffer
	if err := s.legacyRunCLI(h.ctx, []string{"inbox", "read", "--turn", "1", "--stream", stream, "--read-id", "cli", "--json"}, &out, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var view platformInputView
	if err := json.Unmarshal(out.Bytes(), &view); err != nil || len(view.Entries) != 1 || view.Remaining != 24 {
		t.Fatal(out.String(), err)
	}
	agent, err := h.store.LoadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	agent.Turn.Status = "completed"
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	agent.TurnNumber = 2
	agent.LastOfferedUnreadSeq = 0
	agent.Turn = &TurnRecord{Number: 2, Status: "running", StartReadThrough: 1, SnapshotSeq: 1}
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal("platform-only turn rejected", err)
	}
	s2 := startPlatformReader(t, h, p, svc, 2)
	if _, _, release, err := s.read(h.ctx, platformArgs(1, stream, "cli")); err == nil {
		if release != nil {
			release()
		}
		t.Fatal("old turn admitted")
	}
	out.Reset()
	if err := s2.legacyRunCLI(h.ctx, []string{"inbox", "stats", "--turn", "2", "--filter", `{"project_id":"7"}`}, &out, func() error { return nil }); !errors.Is(err, frozen.ErrScopeRequired) {
		t.Fatal(err)
	}
}

func TestPlatformReadImagesSharedBudgetReplayAndReopen(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	base := "/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/"
	a := storedGitLab(id, "a", "events", "7", "42", "image-alpha ![first]("+base+"one.png)")
	b := storedGitLab(id, "b", "events", "7", "56", "image-beta ![repeat]("+base+"one.png) ![second]("+base+"two.png)")
	a.Source.ProjectID = 7
	b.Source.ProjectID = 7
	a.Source.URL = "https://gitlab.example.com/group/repo/-/merge_requests/42"
	b.Source.URL = "https://gitlab.example.com/group/repo/-/merge_requests/56"
	receivePlatform(t, p, a)
	receivePlatform(t, p, b)
	svc.imageMaterials = imageMaterialConfig{MaxImages: 1}
	var downloads atomic.Int32
	svc.materialAttachments = &materialAttachments{download: func(ctx context.Context, req attachmentRequest, reader *attachmentReader, limits attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		if req.SourceRef == "" || reader.MemberIdentity != id || limits.MaxBytes <= 0 {
			t.Fatal(req, reader, limits)
		}
		downloads.Add(1)
		return []byte("fixture-image"), "image/png", &attachmentAccess{}, nil
	}}
	s := startPlatformReader(t, h, p, svc, 1)
	ar := streamFor(t, s, "image-alpha")
	br := streamFor(t, s, "image-beta")
	v := deliverPlatform(t, s, ar, "a")
	if len(v.Images) != 1 || v.Images[0].Status != "supplied" || downloads.Load() != 1 {
		t.Fatal(v.Images, downloads.Load())
	}
	source := v.Entries[0].Source.Ref
	deliverPlatform(t, s, ar, "a")
	if downloads.Load() != 1 {
		t.Fatal("retry downloaded image")
	}
	// Reload from the same original Store, without any process-local page/budget.
	codecs, err := inputStorageCodecs(storageAdapters(), h.store.notificationCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	repo, err := inputstream.OpenRepository(h.store.dir, p.repositoryOwner(), codecs, inputPersistence{h.store})
	if err == nil {
		p.repository = repo
	}
	h.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s = startPlatformReader(t, h, p, svc, 1)
	v = deliverPlatform(t, s, br, "b")
	if downloads.Load() != 1 || len(v.Images) != 2 {
		t.Fatal(v.Images, downloads.Load())
	}
	shared, omitted := false, false
	for _, image := range v.Images {
		shared = shared || image.SharedSource == source
		omitted = omitted || image.Status == "omitted"
	}
	if !shared || !omitted {
		t.Fatal(v.Images)
	}
	state, _ := p.repository.State(s.execution)
	if state.MaterialImages != 1 || state.Used != 2 {
		t.Fatal(state.MaterialImages, state.Used)
	}
}
func (p *platformInputStorage) repositoryOwner() inputstream.Member {
	return inputstream.Member{RoomID: p.id.RoomID, RoomInstance: p.id.RoomInstance, MembershipID: p.id.MembershipID}
}

func TestPlatformReadLocalAdmissionBlocksOnlyItsStream(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	blocked := storedLark(id, "direct", "oc_direct", "待核验私聊", "omt_thread", "unverified-direct")
	receivePlatform(t, p, blocked)
	receivePlatform(t, p, storedGitLab(id, "safe", "events", "7", "42", "safe-source"))
	s := startPlatformReader(t, h, p, svc, 1)
	status, err := s.readiness(h.ctx)
	if err != nil || status.Readable != 1 || status.Blocked != 1 {
		t.Fatal(status, err)
	}
	stream := streamFor(t, s, "safe-source")
	v := deliverPlatform(t, s, stream, "safe")
	if len(v.Entries) != 1 {
		t.Fatal(v)
	}
	state, _ := p.repository.State(s.execution)
	if state.Used != 1 {
		t.Fatal(state.Used)
	}
	blockedStream := streamFor(t, s, "unverified-direct")
	_, _, release, err := s.read(h.ctx, platformArgs(1, blockedStream, "blocked"))
	if release != nil {
		release()
	}
	if err == nil {
		t.Fatal("unverified source consumed")
	}
	state, _ = p.repository.State(s.execution)
	if state.Used != 1 || state.Attempts[1].Phase != "failed_before_output" {
		t.Fatal(state)
	}
}

func TestPlatformReadPartialSourceFailureRepairsOnlyMissingLink(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	for i := 1; i <= 2; i++ {
		item := storedGitLab(id, fmt.Sprint("a", i), "events", "7", "42", fmt.Sprint("partial-alpha-", i))
		item.Source.Reference.Object.ID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "142"}
		receivePlatform(t, p, item)
	}
	receivePlatform(t, p, storedGitLab(id, "b", "events", "7", "56", "partial-beta"))
	svc.referenceResolver = referenceTestResolver
	var calls int
	svc.referenceCommit = func(ctx context.Context, id MemberIdentity, access platform.AccessContext, commit func() (followupEvent, error)) (followupEvent, error) {
		calls++
		if calls == 2 {
			return followupEvent{}, &inputSourceUnavailable{"local source temporarily unavailable"}
		}
		return commit()
	}
	s := startPlatformReader(t, h, p, svc, 1)
	a := streamFor(t, s, "partial-alpha-1")
	b := streamFor(t, s, "partial-beta")
	_, _, release, err := s.read(h.ctx, platformArgs(1, a, "failed"))
	if release != nil {
		release()
	}
	var local *inputSourceUnavailable
	if !errors.As(err, &local) {
		t.Fatal(err)
	}
	state, _ := p.repository.State(s.execution)
	files, _ := os.ReadDir(filepath.Join(h.store.followupRoot(id), "input-sources"))
	if state.Used != 0 || len(files) != 1 {
		t.Fatal(state.Used, len(files))
	}
	deliverPlatform(t, s, b, "b")
	before := calls
	v := deliverPlatform(t, s, a, "repair")
	if len(v.Entries) != 2 || calls != before+1 {
		t.Fatal(v, calls, before)
	}
	files, _ = os.ReadDir(filepath.Join(h.store.followupRoot(id), "input-sources"))
	if len(files) != 3 {
		t.Fatal(files)
	}
	state, _ = p.repository.State(s.execution)
	if state.Used != 3 {
		t.Fatal(state.Used)
	}
}
func TestPlatformReadConcurrentEntriesShareOperationLease(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	receivePlatform(t, p, storedGitLab(id, "a", "events", "7", "42", "concurrent-source"))
	s := startPlatformReader(t, h, p, svc, 1)
	stream := streamFor(t, s, "concurrent-source")
	_, confirm, release, err := s.read(h.ctx, platformArgs(1, stream, "same"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	var out bytes.Buffer
	if err := s.legacyRunCLI(h.ctx, []string{"inbox", "read", "--turn", "1", "--stream", stream, "--read-id", "other"}, &out, func() error { return nil }); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err := confirm(h.ctx); err != nil {
		t.Fatal(err)
	}
	release()
	state, _ := p.repository.State(s.execution)
	if state.Used != 1 || len(state.Attempts) != 1 {
		t.Fatal(state)
	}
}

func TestPlatformReadCannotBypassCoordinatorThroughLegacyEntry(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	receivePlatform(t, p, storedGitLab(id, "a", "events", "7", "42", "single-authority"))
	s := startPlatformReader(t, h, p, svc, 1)
	stream := streamFor(t, s, "single-authority")
	if err := h.store.claimInboxMode(id, 1, "cli"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	_, _, release, err := s.read(h.ctx, platformArgs(1, stream, "one"))
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := h.store.claimInboxMode(id, 1, "mcp"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := p.repository.Barrier(s.execution); !errors.Is(err, inputstream.ErrBusy) {
		t.Fatal(err)
	}
}

type inputReadiness struct{ Pending, Readable, Blocked int }

// Historical reader fixtures inspect admission within their fixed execution.
func (s *inputSession) readiness(ctx context.Context) (inputReadiness, error) {
	var status inputReadiness
	if err := s.check(ctx); err != nil {
		return status, err
	}
	p := s.storage
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	state, err := p.repository.State(s.execution)
	if err != nil {
		return status, err
	}
	locations, err := p.repository.PendingLocations(state.Bounds)
	if err != nil {
		return status, err
	}
	for _, l := range locations {
		entry, err := p.store.businessInputLocked(p.id, 0, &l)
		if err != nil {
			return status, err
		}
		ad, err := p.store.inboxAdmissionLocked(entry)
		if err != nil {
			return status, err
		}
		if ad.Verified && ad.Decision == notification.AdmissionExcluded {
			if err := p.repository.Skip(s.execution, l, "admission_excluded:"+ad.ReasonCode); err != nil {
				return status, p.result(err)
			}
			continue
		}
		status.Pending++
		if ad.Verified {
			status.Readable++
		} else {
			status.Blocked++
		}
	}
	return status, nil
}
