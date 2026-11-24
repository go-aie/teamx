package room

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/mark3labs/mcp-go/mcp"
)

func roomAttachmentReaderFixture(t *testing.T) (*roomHarness, *inputSession) {
	t.Helper()
	h, s := unifiedReaderFixture(t, 0)
	s.images = true
	s.service.materialAttachments = s.service.roomMaterialAttachments(nil)
	return h, s
}

func postAttachmentMessage(t *testing.T, h *roomHarness, text string, metas ...roomAttachmentMeta) PostReceipt {
	t.Helper()
	inputs := []RoomAttachmentInput{}
	for _, m := range metas {
		inputs = append(inputs, RoomAttachmentInput{ID: m.Attachment.ID})
	}
	return roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: fmt.Sprintf("image-%d", h.list(t).LatestSeq+1), Text: text, Attachments: inputs})
}

func materialPixels(t *testing.T, result *mcp.CallToolResult) [][]byte {
	t.Helper()
	var pixels [][]byte
	for _, content := range result.Content {
		if img, ok := content.(mcp.ImageContent); ok {
			if img.MIMEType != "image/png" {
				t.Fatal(img.MIMEType)
			}
			b, err := base64.StdEncoding.DecodeString(img.Data)
			if err != nil {
				t.Fatal(err)
			}
			pixels = append(pixels, b)
		}
	}
	return pixels
}

func TestRoomAttachmentMaterialsMCPReadHistoryAndReplay(t *testing.T) {
	parallelFixtureTest(t)
	h, s := roomAttachmentReaderFixture(t)
	one, two := uploadTestAttachment(t, h.store, 1), uploadTestAttachment(t, h.store, 2)
	postAttachmentMessage(t, h, "", one, two)
	postAttachmentMessage(t, h, "same image, another message", one)
	a, token, _ := historyAccess(t, s)
	args := map[string]any{"stream_ref": roomStreamRef(s.storage.id)}
	w := httptest.NewRecorder()
	first := simpleMCPRequest(t, a, token, "formal", "inbox_read", args, w)
	if first == nil || first.IsError {
		t.Fatal(w.Body.String())
	}
	pixels := materialPixels(t, first)
	if len(pixels) != 2 || !bytes.Equal(pixels[0], attachmentTestPNG(t, false)) || !bytes.Equal(pixels[1], pixels[0]) {
		t.Fatal("different IDs with identical bytes must remain distinct images", len(pixels))
	}
	var body inputBody
	if err := json.Unmarshal([]byte(first.Content[0].(mcp.TextContent).Text), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 2 || len(body.Images) != 3 || body.Images[0].RoomSeq != 1 || !reflect.DeepEqual(body.Images[0].Positions, []int{1}) || !reflect.DeepEqual(body.Images[1].Positions, []int{2}) || body.Images[2].RoomSeq != 2 || body.Images[2].SharedRoom != 1 {
		t.Fatal(body)
	}
	if body.Entries[0].(map[string]any)["text"] != "" || len(body.Entries[0].(map[string]any)["attachments"].([]any)) != 2 {
		t.Fatal("pure-image message projection lost", body.Entries)
	}
	encoded := w.Body.String()
	for _, secret := range []string{h.store.dir, "service_epoch", "SHA256", "Third-party material", "attachment_id=", "meta.json"} {
		if strings.Contains(encoded, secret) {
			t.Fatal("wrong source label or internal evidence exposed", secret)
		}
	}
	if !strings.Contains(encoded, "Room user attachment") || h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal("wrong provenance or premature native confirmation")
	}
	// Reopen the durable result repository before receipt confirmation. Bytes
	// already saved in the formal page survive loss of the original image file.
	codecs, err := inputStorageCodecs(storageAdapters(), h.store.notificationCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	repo, err := inputstream.OpenRepository(h.store.dir, s.storage.repositoryOwner(), codecs, inputPersistence{h.store})
	if err == nil {
		s.storage.repository = repo
	}
	h.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.store.attachmentPath(one.Attachment.ID), "data")); err != nil {
		t.Fatal(err)
	}
	s, err = newInputSession(h.ctx, s.service, s.storage, s.execution.Generation, s.execution.Turn, true, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	a, token, _ = historyAccess(t, s)
	w = httptest.NewRecorder()
	replay := simpleMCPRequest(t, a, token, "formal", "inbox_read", args, w)
	if !reflect.DeepEqual(first, replay) {
		t.Fatal("reopened formal page changed", w.Body.String())
	}
	if err := s.receiveCall(h.ctx, "formal"); err != nil || h.list(t).Members[0].ReadThrough != 2 {
		t.Fatal("native confirmation failed", err)
	}
	// Restore bytes solely to prove a fresh History read supplies the same real
	// objects independently of the now-exhausted formal image allowance.
	if err := os.WriteFile(filepath.Join(h.store.attachmentPath(one.Attachment.ID), "data"), pixels[0], 0600); err != nil {
		t.Fatal(err)
	}
	before := historyTree(t, h.store.dir)
	past, history := historyMCPCall(t, a, token, "past", "history_read", args)
	if len(materialPixels(t, past)) != 2 || len(history.Entries) != 2 {
		t.Fatal(history)
	}
	for i, entry := range history.Entries {
		e := entry.(map[string]any)
		refs := e["images"].([]any)
		if i == 0 && len(refs) != 2 || i == 1 && len(refs) != 1 {
			t.Fatal(refs)
		}
		for j, ref := range refs {
			image := ref.(map[string]any)
			index := int(image["content_index"].(float64))
			if image["position"] != float64(j+1) || index < 1 || index >= len(past.Content) || !bytes.Equal(materialPixels(t, &mcp.CallToolResult{Content: []mcp.Content{past.Content[index]}})[0], pixels[0]) {
				t.Fatal(image)
			}
		}
	}
	again, _ := historyMCPCall(t, a, token, "past", "history_read", args)
	if !reflect.DeepEqual(past, again) || !reflect.DeepEqual(before, historyTree(t, h.store.dir)) || h.list(t).Members[0].ReadThrough != 2 {
		t.Fatal("history changed cached bytes, storage or consumption")
	}
	h.store.mu.Lock()
	pins := h.store.attachmentStorage().pins
	for id, count := range pins {
		if count != 0 {
			t.Error("leaked object pin", id, count)
		}
	}
	h.store.mu.Unlock()
}

func TestRoomAttachmentMaterialsMissingAndBudgets(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"missing", "corrupt", "pixels"} {
		t.Run(mode, func(t *testing.T) {
			h, s := roomAttachmentReaderFixture(t)
			one, two := uploadTestAttachment(t, h.store, 1), uploadTestAttachment(t, h.store, 2)
			postAttachmentMessage(t, h, "retained message", one, two)
			reason, expectedImages := "", 0
			switch mode {
			case "missing":
				err := os.Remove(filepath.Join(h.store.attachmentPath(one.Attachment.ID), "data"))
				if err != nil {
					t.Fatal(err)
				}
				reason, expectedImages = "unavailable", 1
			case "corrupt":
				err := os.WriteFile(filepath.Join(h.store.attachmentPath(one.Attachment.ID), "data"), []byte("broken"), 0600)
				if err != nil {
					t.Fatal(err)
				}
				reason, expectedImages = "unavailable", 1
			case "pixels":
				s.service.imageMaterials.MaxPixels = 1
				reason = "image byte/pixel"
			}
			a, token, _ := historyAccess(t, s)
			args := map[string]any{"stream_ref": roomStreamRef(s.storage.id)}
			w := httptest.NewRecorder()
			formal := simpleMCPRequest(t, a, token, "formal", "inbox_read", args, w)
			if formal == nil || formal.IsError || !strings.Contains(w.Body.String(), reason) || !strings.Contains(w.Body.String(), "retained message") || len(materialPixels(t, formal)) != expectedImages {
				t.Fatal(w.Body.String())
			}
			if err := s.receiveCall(h.ctx, "formal"); err != nil {
				t.Fatal(err)
			}
			history, page := historyMCPCall(t, a, token, "history", "history_read", args)
			if len(materialPixels(t, history)) != expectedImages || !strings.Contains(string(mustJSON(t, page)), reason) || h.list(t).Members[0].ReadThrough != 1 {
				t.Fatal(page)
			}
		})
	}
}

func TestRoomAttachmentMaterialsSelectedRangeAndLifecycle(t *testing.T) {
	parallelFixtureTest(t)
	h, s := roomAttachmentReaderFixture(t)
	one, orphan := uploadTestAttachment(t, h.store, 1), uploadTestAttachment(t, h.store, 2)
	postAttachmentMessage(t, h, "selected", one)
	postAttachmentMessage(t, h, "outside selected page", one)
	q := inputstream.ReadRequest{Turn: 1, Stream: roomStreamRef(s.storage.id), ID: "selected", Limit: 1}
	page, err := s.reserveRoom(h.ctx, q, nil)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := s.roomEntries(page)
	if err != nil {
		t.Fatal(err)
	}
	notice := s.roomPageNotices(messages, page)[0]
	req := notice.requestWithPath(one.Attachment.ID)
	reader := attachmentReader{s.storage.id, 1}
	limits := attachment.ImageLimits{MaxBytes: 2 << 20, MaxPixels: 16 << 20}
	data, _, access, err := s.service.materialAttachments.download(h.ctx, req, &reader, limits)
	if err != nil || !bytes.Equal(data, attachmentTestPNG(t, false)) {
		t.Fatal(err)
	}
	for _, kind := range []string{"orphan", "outside-page", "no-selection", "wrong-room", "wrong-member", "stale-epoch", "wrong-generation", "no-reader"} {
		t.Run(kind, func(t *testing.T) {
			bad, r := req, reader
			ptr := &r
			switch kind {
			case "orphan":
				bad.Path = orphan.Attachment.ID
			case "outside-page":
				bad.Seq = 2
			case "no-selection":
				bad.Page = nil
			case "wrong-room":
				bad.RoomInstance = "other-room"
			case "wrong-member":
				r.MembershipID = "generation-B"
			case "stale-epoch":
				bad.ServiceEpoch = "old"
			case "wrong-generation":
				copySession := inputSession{service: s.service, storage: s.storage, execution: s.execution}
				copySession.execution.Generation = "old"
				copyPage := *req.Page
				copyPage.session = &copySession
				bad.Page = &copyPage
			case "no-reader":
				ptr = nil
			}
			if _, _, _, err := s.service.materialAttachments.download(h.ctx, bad, ptr, limits); !errors.Is(err, ErrIdentity) {
				t.Fatal("unselected/unauthorized image admitted", err)
			}
		})
	}
	// A successful read cannot authorize later submission after turn cancellation.
	cancelled, cancel := context.WithCancel(h.ctx)
	s.executionContext = cancelled
	cancel()
	agent, err := h.store.LoadAgent(s.storage.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.service.materialAttachments.check(h.ctx, s.service.info, reader, *agent.Turn, []materialNotice{notice}, []*attachmentAccess{access}); err == nil {
		t.Fatal("late canceled bytes admitted")
	}
	s.executionContext = nil
	// History must carry exact retained attachment descriptors as well as seq.
	bound := page.Snapshot
	bound.Through = 2
	historyReq := req
	historyReq.Page = nil
	historyReq.Range = &attachmentRange{session: s, stream: historyStream{provider: "room", bound: bound, locations: []inputstream.Location{{Partition: bound.Partition, Seq: 1}}}, location: inputstream.Location{Partition: bound.Partition, Seq: 1}, attachments: []RoomAttachment{one.Attachment}}
	if _, _, _, err := s.service.materialAttachments.download(h.ctx, historyReq, &reader, limits); err != nil {
		t.Fatal(err)
	}
	historyReq.Range.attachments[0].ID = orphan.Attachment.ID
	if _, _, _, err := s.service.materialAttachments.download(h.ctx, historyReq, &reader, limits); err == nil {
		t.Fatal("forged history association admitted")
	}
	// Service shutdown and actual native membership removal also invalidate
	// previously downloaded bytes at the result-check boundary.
	s.service.closing.Store(true)
	if err := s.service.materialAttachments.check(h.ctx, s.service.info, reader, *agent.Turn, []materialNotice{notice}, []*attachmentAccess{access}); err == nil {
		t.Fatal("closing service admitted late image")
	}
	s.service.closing.Store(false)
	roomCall[MemberInfo](t, h, RemoveMember{MemberIdentity: s.storage.id})
	if err := s.service.materialAttachments.check(h.ctx, s.service.info, reader, *agent.Turn, []materialNotice{notice}, []*attachmentAccess{access}); err == nil {
		t.Fatal("removed member admitted late image")
	}

}

func TestRoomAttachmentMaterialsShortOutputKeepsImagesAndNativeCursor(t *testing.T) {
	parallelFixtureTest(t)
	h, s := roomAttachmentReaderFixture(t)
	postAttachmentMessage(t, h, "", uploadTestAttachment(t, h.store, 1))
	a, token, _ := historyAccess(t, s)
	args := map[string]any{"stream_ref": roomStreamRef(s.storage.id)}
	short := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), write: func([]byte) (int, error) { return 0, io.ErrShortWrite }}
	simpleMCPRequest(t, a, token, "short", "inbox_read", args, short)
	if h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal("short output consumed")
	}
	w := httptest.NewRecorder()
	replay := simpleMCPRequest(t, a, token, "short", "inbox_read", args, w)
	if replay == nil || replay.IsError || len(materialPixels(t, replay)) != 1 {
		t.Fatal(w.Body.String())
	}
	if err := s.receiveCall(h.ctx, "short"); err != nil || h.list(t).Members[0].ReadThrough != 1 {
		t.Fatal(err)
	}
}

func TestRoomAttachmentMaterialsScopeAndPlatformDispatch(t *testing.T) {
	parallelFixtureTest(t)
	h, s := roomAttachmentReaderFixture(t)
	one := uploadTestAttachment(t, h.store, 1)
	postAttachmentMessage(t, h, "local", one)
	page, err := s.reserveRoom(h.ctx, inputstream.ReadRequest{Turn: 1, Stream: roomStreamRef(s.storage.id), ID: "local"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.roomEntries(page)
	if err != nil {
		t.Fatal(err)
	}
	local := s.roomPageNotices(entries, page)[0]
	// Same identity in different Room IDs/instances and in a platform source
	// must never reuse another scope's pixels or execution memo.
	notices := []materialNotice{local, local, local, local}
	notices[1].request.RoomID = "other-room"
	notices[2].request.RoomInstance = "other-instance"
	notices[3].request.Local = false
	keys := map[string]bool{}
	for i := range notices {
		notices[i].request.Seq = uint64(i + 1)
		key := inputImageKey(notices[i], notices[i].images.Images[0])
		if keys[key] {
			t.Fatal("execution dedup scope collision")
		}
		keys[key] = true
	}
	limits, _ := (imageMaterialConfig{}).limits()
	calls := 0
	budget := newNotificationImageBudget(limits)
	groups, _ := budget.prepare(h.ctx, &materialAttachments{download: func(context.Context, attachmentRequest, *attachmentReader, attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		calls++
		return []byte{byte(calls)}, "image/png", nil, nil
	}}, attachmentReader{s.storage.id, 1}, notices, true)
	if calls != 4 || len(groups) != 4 {
		t.Fatal("page dedup scope collision", calls)
	}
	for i, g := range groups {
		if len(g.data) != 1 || g.data[0] != byte(i+1) || g.shared != "" {
			t.Fatal(g)
		}
	}
	// The composed production boundary leaves the platform request, account
	// evidence and validation callbacks intact while filtering local evidence.
	remoteNotice := notices[3]
	remoteNotice.request.Local = false
	remoteAccess := &attachmentAccess{request: remoteNotice.request}
	downloads, checks, inboxChecks := 0, 0, 0
	combined := s.service.roomMaterialAttachments(&materialAttachments{
		download: func(_ context.Context, req attachmentRequest, _ *attachmentReader, _ attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
			downloads++
			if req != remoteNotice.requestWithPath(one.Attachment.ID) {
				t.Fatal(req)
			}
			return []byte("platform pixels"), "image/png", remoteAccess, nil
		},
		check: func(_ context.Context, _ ServiceInfo, _ attachmentReader, _ TurnRecord, n []materialNotice, a []*attachmentAccess) error {
			checks++
			if len(n) != 1 || n[0].request.Local || len(a) != 1 || a[0] != remoteAccess {
				t.Fatal(n, a)
			}
			return nil
		},
		checkInbox: func(_ context.Context, _ ServiceInfo, _ attachmentReader, n []materialNotice) error {
			inboxChecks++
			if len(n) != 1 || n[0].request.Local {
				t.Fatal(n)
			}
			return nil
		},
	})
	reader := attachmentReader{s.storage.id, 1}
	imageLimits := attachment.ImageLimits{MaxBytes: 2 << 20, MaxPixels: 16 << 20}
	_, _, localAccess, err := combined.download(h.ctx, local.requestWithPath(one.Attachment.ID), &reader, imageLimits)
	if err != nil || downloads != 0 {
		t.Fatal(err, downloads)
	}
	if _, _, _, err := combined.download(h.ctx, remoteNotice.requestWithPath(one.Attachment.ID), &reader, imageLimits); err != nil {
		t.Fatal(err)
	}
	mixed := []materialNotice{local, remoteNotice}
	if err := combined.check(h.ctx, s.service.info, reader, TurnRecord{}, mixed, []*attachmentAccess{localAccess, remoteAccess}); err != nil {
		t.Fatal(err)
	}
	if err := combined.checkInbox(h.ctx, s.service.info, reader, mixed); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 || checks != 1 || inboxChecks != 1 {
		t.Fatal(downloads, checks, inboxChecks)
	}
}

func TestRoomAttachmentMaterialsReadProtectionAndRetention(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"formal", "history"} {
		t.Run(mode, func(t *testing.T) {
			h, s := roomAttachmentReaderFixture(t)
			one, orphan := uploadTestAttachment(t, h.store, 1), uploadTestAttachment(t, h.store, 2)
			postAttachmentMessage(t, h, "protected", one)
			h.store.mu.Lock()
			h.store.attachmentStorage().now = func() time.Time { return one.CreatedAt.Add(2 * roomAttachmentTTL) }
			h.store.mu.Unlock()
			// Pause after real local bytes are verified, before preparation commits
			// its result. The ordinary formal/History protection is still live.
			entered, resume := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-resume:
				default:
					close(resume)
				}
			}()
			original := s.service.materialAttachments.download
			s.service.materialAttachments.download = func(ctx context.Context, req attachmentRequest, reader *attachmentReader, limits attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
				b, mime, access, err := original(ctx, req, reader, limits)
				close(entered)
				select {
				case <-resume:
				case <-ctx.Done():
					return nil, "", nil, ctx.Err()
				}
				return b, mime, access, err
			}
			args, _ := json.Marshal(map[string]any{"stream_ref": roomStreamRef(s.storage.id)})
			done := make(chan error, 1)
			go func() {
				if mode == "history" {
					_, err := s.historyCall(h.ctx, "protected", "history_read", args)
					done <- err
					return
				}
				_, flush, release, err := s.readStream(h.ctx, "protected", args)
				if err == nil {
					err = flush(h.ctx)
				}
				if release != nil {
					release()
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			roomError[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 1}, ErrBusy)
			h.store.mu.Lock()
			err := h.store.collectAttachments()
			h.store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(h.store.attachmentPath(one.Attachment.ID)); err != nil {
				t.Fatal("referenced image reclaimed", err)
			}
			if _, err := os.Stat(h.store.attachmentPath(orphan.Attachment.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("expired orphan retained", err)
			}
			// Appends remain available while image preparation is in progress.
			roomCall[PostReceipt](t, h, h.post(humanCaller, "concurrent", "later message"))
			close(resume)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if mode == "formal" {
				if err := s.receiveCall(h.ctx, "protected"); err != nil {
					t.Fatal(err)
				}
				roomError[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 1}, ErrBusy)
				if err := s.coordinate(h.ctx, func(r inputstream.ExecutionStorage) error { return r.Seal(s.execution) }); err != nil {
					t.Fatal(err)
				}
			}
			roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 1})
			if _, err := os.Stat(h.store.attachmentPath(one.Attachment.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("cleared expired image retained", err)
			}
			if mode == "history" {
				if _, err := s.historyCall(h.ctx, "protected", "history_read", args); !errors.Is(err, errHistorySnapshot) {
					t.Fatal("expired cached range replayed", err)
				}
			}
		})
	}
}

func TestRoomAttachmentMaterialsExecutionBudgetAndReuse(t *testing.T) {
	parallelFixtureTest(t)
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprint(reuse), func(t *testing.T) {
			h, s := roomAttachmentReaderFixture(t)
			one := uploadTestAttachment(t, h.store, 1)
			two := one
			if !reuse {
				two = uploadTestAttachment(t, h.store, 2)
			}
			postAttachmentMessage(t, h, "first", one)
			postAttachmentMessage(t, h, "second", two)
			s.service.imageMaterials.MaxImages = 1
			a, token, _ := historyAccess(t, s)
			args := map[string]any{"stream_ref": roomStreamRef(s.storage.id), "limit": 1}
			for _, call := range []string{"first", "second"} {
				w := httptest.NewRecorder()
				out := simpleMCPRequest(t, a, token, call, "inbox_read", args, w)
				if out == nil || out.IsError {
					t.Fatal(w.Body.String())
				}
				if call == "first" && len(materialPixels(t, out)) != 1 {
					t.Fatal("initial image missing")
				}
				if call == "second" {
					var body inputBody
					if err := json.Unmarshal([]byte(out.Content[0].(mcp.TextContent).Text), &body); err != nil {
						t.Fatal(err)
					}
					if len(materialPixels(t, out)) != 0 || len(body.Images) != 1 || body.Images[0].RoomSeq != 2 {
						t.Fatal(body)
					}
					if reuse && (body.Images[0].SharedRoom != 1 || body.Images[0].Status != "supplied") {
						t.Fatal("same Room object was not reused", body.Images)
					}
					if !reuse && (body.Images[0].Status != "omitted" || !strings.Contains(body.Images[0].Reason, "image count budget")) {
						t.Fatal("new ID bypassed execution budget", body.Images)
					}
				}
				if err := s.receiveCall(h.ctx, call); err != nil {
					t.Fatal(err)
				}
			}
			state, err := s.inputState(h.ctx)
			if err != nil || state.MaterialImages != 1 || state.MaterialBytes != one.Attachment.Size {
				t.Fatal(state, err)
			}
			past, _ := historyMCPCall(t, a, token, "history", "history_read", args)
			if len(materialPixels(t, past)) != 1 || h.list(t).Members[0].ReadThrough != 2 {
				t.Fatal("History did not have its independent allowance")
			}
			entries, err := s.roomEntries(state.Attempts[0])
			if err != nil {
				t.Fatal(err)
			}
			req := s.roomPageNotices(entries, state.Attempts[0])[0].requestWithPath(one.Attachment.ID)
			if err := s.coordinate(h.ctx, func(r inputstream.ExecutionStorage) error { return r.Seal(s.execution) }); err != nil {
				t.Fatal(err)
			}
			reader := attachmentReader{s.storage.id, s.execution.Turn}
			if _, _, _, err := s.service.materialAttachments.download(h.ctx, req, &reader, attachment.ImageLimits{MaxBytes: 2 << 20, MaxPixels: 16 << 20}); !errors.Is(err, ErrIdentity) {
				t.Fatal("sealed execution admitted an image", err)
			}
		})
	}
}
