package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/mark3labs/mcp-go/mcp"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func actionImagesFixture(t *testing.T, log bool) (*roomHarness, *service, context.Context, []roomActionImageRef) {
	t.Helper()
	state := historyRoom(0, "")
	if log {
		state.InputLog = &roomLogRoot{Version: 1}
	}
	h := newRoomHarness(t, state, nil)
	one, two := uploadTestAttachment(t, h.store, 1), uploadTestAttachment(t, h.store, 2)
	postAttachmentMessage(t, h, "first", one)
	postAttachmentMessage(t, h, "second", two)
	id := h.memberID("A", "generation-A")
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 2}}); err != nil {
		t.Fatal(err)
	}
	svc := &service{store: h.store, info: ServiceInfo{RoomIdentity: h.id, ServiceEpoch: "action-source"}, room: h.addr}
	ctx := context.WithValue(h.ctx, roomActionContextKey{}, roomActionContext{ServiceInfo: svc.info, MemberIdentity: id, Turn: 1, ID: "send-originals"})
	return h, svc, ctx, []roomActionImageRef{{MessageSeq: 2, AttachmentID: two.Attachment.ID}, {MessageSeq: 1, AttachmentID: one.Attachment.ID}}
}
func assertActionImagePinsReleased(t *testing.T, s *Store) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.roomHistoryPins) != 0 {
		t.Fatal("native retention pin leaked", s.roomHistoryPins)
	}
	for id, n := range s.attachmentStorage().pins {
		if n != 0 {
			t.Fatal("object pin leaked", id, n)
		}
	}
}
func contextGuard(ctx context.Context) error { return ctx.Err() }

func TestRoomActionImagesOriginalsAndRetention(t *testing.T) {
	for _, log := range []bool{false, true} {
		t.Run(fmt.Sprint(log), func(t *testing.T) {
			h, svc, ctx, refs := actionImagesFixture(t, log)
			before := historyTree(t, h.store.dir)
			lease, err := svc.prepareRoomActionImages(ctx, refs, contextGuard)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			if len(lease.images) != 2 || lease.images[0].Ref != refs[0] || lease.images[1].Ref != refs[1] {
				t.Fatal("lost selection/order")
			}
			for _, img := range lease.images {
				digest := sha256.Sum256(img.Data)
				if !bytes.Equal(img.Data, attachmentTestPNG(t, false)) || img.Attachment.MIMEType != "image/png" || img.SHA256 != hex.EncodeToString(digest[:]) {
					t.Fatal("original bytes changed")
				}
			}
			if !reflect.DeepEqual(before, historyTree(t, h.store.dir)) {
				t.Fatal("source preparation wrote read state or other files")
			}
			h.store.mu.Lock()
			if len(h.store.roomHistoryPins) != 1 || h.store.roomHistoryPins[0] != 1 {
				t.Fatal("expected one publication pin")
			}
			h.store.mu.Unlock()
			roomError[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 2}, ErrBusy)
			// Appends and an independent reader complete while the lease remains live.
			done := make(chan error, 1)
			go func() {
				_, e := h.addr.Call[PostReceipt](h.ctx, h.post(humanCaller, "during-source-lease", "new message"))
				if e == nil {
					_, e = h.addr.Call[ReadPage](h.ctx, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadLatest, Latest: 10})
				}
				done <- e
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("source lease blocks appends/reads")
			}
			orphan := uploadTestAttachment(t, h.store, 3)
			h.store.attachmentLife.Lock()
			h.store.mu.Lock()
			h.store.attachmentStorage().now = func() time.Time { return orphan.CreatedAt.Add(25 * time.Hour) }
			err = h.store.collectAttachments()
			h.store.mu.Unlock()
			h.store.attachmentLife.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(h.store.attachmentPath(orphan.Attachment.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unselected expired object retained", err)
			}
			if err := lease.Check(ctx); err != nil {
				t.Fatal(err)
			}
			lease.Close()
			lease.Close()
			assertActionImagePinsReleased(t, h.store)
			if err := lease.Check(ctx); err == nil {
				t.Fatal("closed lease still valid")
			}
			roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 2})
			if _, err := svc.prepareRoomActionImages(ctx, refs, contextGuard); err == nil {
				t.Fatal("cleared source still accepted")
			}
			assertActionImagePinsReleased(t, h.store)
		})
	}
}

func TestRoomActionImagesRejectInvalidSourcesAndRelease(t *testing.T) {
	for _, log := range []bool{false, true} {
		for _, mode := range []string{"empty", "too_many", "duplicate", "wrong_seq", "zero_seq", "invalid_id", "draft", "missing", "corrupt", "owner", "descriptor", "cross_room", "active", "unconfirmed", "recovery", "cancelled", "no_context", "wrong_service", "wrong_member", "failed_guard"} {
			t.Run(fmt.Sprintf("log=%v/%s", log, mode), func(t *testing.T) {
				h, svc, ctx, refs := actionImagesFixture(t, log)
				guard := contextGuard
				// Break the second selected object after the first can be pinned/read.
				id := refs[1].AttachmentID
				switch mode {
				case "empty":
					refs = nil
				case "too_many":
					refs = append(refs, refs[0], refs[1], refs[0])
				case "duplicate":
					refs[1].AttachmentID = refs[0].AttachmentID
				case "wrong_seq":
					refs[1].MessageSeq = 99
				case "zero_seq":
					refs[1].MessageSeq = 0
				case "invalid_id":
					refs[1].AttachmentID = "../../data"
				case "draft":
					m := uploadTestAttachment(t, h.store, 3)
					refs[1].AttachmentID = m.Attachment.ID
				case "missing":
					if err := os.Remove(filepath.Join(h.store.attachmentPath(id), "data")); err != nil {
						t.Fatal(err)
					}
				case "corrupt":
					if err := os.WriteFile(filepath.Join(h.store.attachmentPath(id), "data"), []byte("broken"), 0600); err != nil {
						t.Fatal(err)
					}
				case "owner", "descriptor", "cross_room":
					m, err := h.store.attachmentMeta(id)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "owner" {
						m.Operator = "other-human"
					}
					if mode == "descriptor" {
						m.Attachment.Width++
					}
					if mode == "cross_room" {
						m.RoomInstance = "other-room"
					}
					if err := os.WriteFile(filepath.Join(h.store.attachmentPath(id), "meta.json"), mustJSON(t, m), 0600); err != nil {
						t.Fatal(err)
					}
				case "active":
					h.store.attachmentStorage().active[id] = true
				case "unconfirmed":
					h.store.attachmentStorage().unconfirmed[id] = true
				case "recovery":
					h.store.attachmentStorage().recoveryUncertain = true
					delete(h.store.attachmentStorage().confirmed, id)
				case "cancelled":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				case "no_context":
					ctx = h.ctx
				case "wrong_service", "wrong_member":
					call := ctx.Value(roomActionContextKey{}).(roomActionContext)
					if mode == "wrong_service" {
						call.ServiceEpoch = "old-service"
					} else {
						call.AgentID = "other-agent"
					}
					ctx = context.WithValue(ctx, roomActionContextKey{}, call)
				case "failed_guard":
					guard = func(context.Context) error { return ErrIdentity }
				}
				if lease, err := svc.prepareRoomActionImageLease(ctx, refs, guard); err == nil {
					lease.Close()
					t.Fatal("accepted invalid source")
				} else if lease != nil {
					t.Fatal("failed preparation returned an image lease")
				}
				assertActionImagePinsReleased(t, h.store)
			})
		}
	}
}

func TestRoomActionImagesCheckExecutionWithoutRoomScan(t *testing.T) {
	for _, mode := range []string{"room_unreadable", "turn_changed", "turn_completed", "invalid_agent", "agent_symlink", "session_generation", "pending_session", "session_symlink", "cancelled", "binding_revoked", "closing", "restarting"} {
		t.Run(mode, func(t *testing.T) {
			h, svc, ctx, refs := actionImagesFixture(t, true)
			revoked := false
			lease, err := svc.prepareRoomActionImages(ctx, refs, func(ctx context.Context) error {
				if revoked {
					return ErrIdentity
				}
				return ctx.Err()
			})
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			call := ctx.Value(roomActionContextKey{}).(roomActionContext)
			dir := filepath.Join(h.store.dir, "agents", call.MembershipID)
			switch mode {
			case "room_unreadable":
				if err := os.WriteFile(filepath.Join(h.store.dir, "room.json"), []byte("not JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			case "turn_changed", "turn_completed", "invalid_agent":
				a, err := h.store.LoadAgent(call.MemberIdentity)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "turn_changed" {
					a.TurnNumber = 2
					a.Turn.Number = 2
				}
				if mode == "turn_completed" {
					a.Turn.Status = "completed"
				}
				if mode == "invalid_agent" {
					a.Turn.SnapshotSeq = 0
					a.Turn.StartReadThrough = 1
				}
				if err := os.WriteFile(filepath.Join(dir, "agent.json"), mustJSON(t, a), 0600); err != nil {
					t.Fatal(err)
				}
			case "agent_symlink":
				data, err := os.ReadFile(filepath.Join(dir, "agent.json"))
				if err != nil {
					t.Fatal(err)
				}
				other := filepath.Join(t.TempDir(), "agent.json")
				if err := os.WriteFile(other, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(dir, "agent.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, filepath.Join(dir, "agent.json")); err != nil {
					t.Fatal(err)
				}
			case "session_generation", "pending_session", "session_symlink":
				ref := sessionReference{Version: formatVersion, MemberIdentity: call.MemberIdentity, Directory: filepath.Base(dshStateDir(filepath.Join(dir, "session.json")))}
				if mode == "pending_session" {
					ref.Directory += "-" + strings.Repeat("a", 32)
					ref.Pending = true
				}
				target := filepath.Join(dir, "session.json")
				if mode == "session_symlink" {
					target = filepath.Join(t.TempDir(), "session.json")
				}
				if err := os.WriteFile(target, mustJSON(t, ref), 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "session_symlink" {
					if err := os.Symlink(target, filepath.Join(dir, "session.json")); err != nil {
						t.Fatal(err)
					}
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "binding_revoked":
				revoked = true
			case "closing":
				svc.closing.Store(true)
			case "restarting":
				svc.restarting.Store(true)
			}
			err = lease.Check(ctx)
			if mode == "room_unreadable" {
				if err != nil {
					t.Fatal("Check scanned Room history", err)
				}
			} else if err == nil {
				t.Fatal("stale lease accepted")
			}
			lease.Close()
			assertActionImagePinsReleased(t, h.store)
		})
	}
}

func TestRoomActionImagesValidateActualFormatDimensionsAndTotal(t *testing.T) {
	for _, mode := range []string{"mime", "dimensions", "pixels", "format", "total"} {
		t.Run(mode, func(t *testing.T) {
			h, svc, ctx, _ := actionImagesFixture(t, false)
			data := attachmentTestPNG(t, false)
			if mode == "pixels" {
				var b bytes.Buffer
				if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 4097, 4097))); err != nil {
					t.Fatal(err)
				}
				data = b.Bytes()
			}
			if mode == "total" {
				img := image.NewNRGBA(image.Rect(0, 0, 640, 640))
				var n uint32 = 1
				for i := range img.Pix {
					n = 1664525*n + 1013904223
					img.Pix[i] = uint8(n >> 16)
				}
				var b bytes.Buffer
				if err := png.Encode(&b, img); err != nil {
					t.Fatal(err)
				}
				data = b.Bytes()
				if int64(len(data))*3 <= roomAttachmentTotalBytes || int64(len(data)) > roomAttachmentBytes {
					t.Fatal("bad total limit fixture", len(data))
				}
			}
			var refs []roomActionImageRef
			count := 1
			if mode == "total" {
				count = 3
			}
			for i := range count {
				m := uploadTestAttachment(t, h.store, 10+i)
				if mode == "mime" {
					m.Attachment.MIMEType = "image/jpeg"
				}
				if mode == "dimensions" {
					m.Attachment.Width++
				}
				if mode == "format" {
					data = []byte("not an image")
				}
				digest := sha256.Sum256(data)
				m.SHA256 = hex.EncodeToString(digest[:])
				m.Attachment.Size = int64(len(data))
				if err := os.WriteFile(filepath.Join(h.store.attachmentPath(m.Attachment.ID), "data"), data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(h.store.attachmentPath(m.Attachment.ID), "meta.json"), mustJSON(t, m), 0600); err != nil {
					t.Fatal(err)
				}
				post := postAttachmentMessage(t, h, "saved reference with consistent metadata", m)
				refs = append(refs, roomActionImageRef{MessageSeq: post.Seq, AttachmentID: m.Attachment.ID})
			}
			lease, err := svc.prepareRoomActionImages(ctx, refs, contextGuard)
			if err == nil {
				lease.Close()
				t.Fatal("invalid actual image accepted")
			}
			if (mode == "pixels" || mode == "total") && !errors.Is(err, attachment.ErrLimit) {
				t.Fatal("expected image limit error", err)
			}
			assertActionImagePinsReleased(t, h.store)
		})
	}
}

func TestRoomActionImagesReadContractsAndIndependentBudget(t *testing.T) {
	for _, transport := range []string{"http", "cli"} {
		for _, images := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/images=%v", transport, images), func(t *testing.T) {
				h, s := roomAttachmentReaderFixture(t)
				one, two := uploadTestAttachment(t, h.store, 1), uploadTestAttachment(t, h.store, 2)
				postAttachmentMessage(t, h, "two originals", one, two)
				s.images = images
				s.service.imageMaterials.MaxImages = 1 // The second image exceeds the model allowance.
				a, token, _ := historyAccess(t, s)
				args := map[string]any{"stream_ref": roomStreamRef(s.storage.id)}
				var formal []byte
				var confirm func() error
				if transport == "http" {
					w := httptest.NewRecorder()
					result := simpleMCPRequest(t, a, token, "formal", "inbox_read", args, w)
					if result == nil || result.IsError {
						t.Fatal(w.Body.String())
					}
					formal = []byte(result.Content[0].(mcp.TextContent).Text)
					confirm = func() error { return s.receiveCall(h.ctx, "formal") }
				} else {
					svc := s.service
					svc.inboxMode, svc.access = "cli", a
					execution, revokeExecution, err := a.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: agentCallerIdentity(s.storage.id)})
					if err != nil {
						t.Fatal(err)
					}
					defer revokeExecution()
					server := httptest.NewServer(a.wrap(http.HandlerFunc(svc.serveInputCLI)))
					defer server.Close()
					a.mu.Lock()
					a.internal = server.URL
					a.mu.Unlock()
					hidden, _ := json.Marshal(map[string]string{"token": token, "call": "source-read-cli"})
					values := map[string]string{"TEAMX_EXECUTION_TOKEN": execution, "TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": h.id.RoomID, "TEAMX_ROOM_INSTANCE": h.id.RoomInstance, "TEAMX_SERVICE_EPOCH": svc.info.ServiceEpoch, "TEAMX_AGENT_ID": s.storage.id.AgentID, "TEAMX_MEMBERSHIP_ID": s.storage.id.MembershipID, "TEAMX_INPUT_CONTRACT": inputstream.Contract, "DSH_TEAMX_INPUT_BINDING": string(hidden)}
					var out bytes.Buffer
					if err := runCLIWithInput(h.ctx, []string{"inbox", "read", "--stream", roomStreamRef(s.storage.id)}, strings.NewReader(""), &out, io.Discard, func(k string) string { return values[k] }); err != nil {
						t.Fatal(err)
					}
					formal = out.Bytes()
					// Framework receipts cover the business body; CLI image blocks
					// are transported separately in content.
					var receiptBody map[string]json.RawMessage
					if err := json.Unmarshal(formal, &receiptBody); err != nil {
						t.Fatal(err)
					}
					delete(receiptBody, "content")
					receiptJSON, err := json.Marshal(receiptBody)
					if err != nil {
						t.Fatal(err)
					}
					digest, err := inputBodyDigest(receiptJSON)
					if err != nil {
						t.Fatal(err)
					}
					confirm = func() error { return s.receiveGroupBodies(h.ctx, "source-read-cli", []string{digest}) }
				}
				var body struct {
					Entries []struct {
						Seq         uint64           `json:"seq"`
						Attachments []RoomAttachment `json:"attachments"`
					} `json:"entries"`
				}
				if err := json.Unmarshal(formal, &body); err != nil || len(body.Entries) != 1 || len(body.Entries[0].Attachments) != 2 {
					t.Fatal(string(formal), err)
				}
				refs := []roomActionImageRef{}
				for _, desc := range body.Entries[0].Attachments {
					refs = append(refs, roomActionImageRef{MessageSeq: body.Entries[0].Seq, AttachmentID: desc.ID})
				}
				if err := confirm(); err != nil {
					t.Fatal(err)
				}
				if h.list(t).Members[0].ReadThrough != 1 {
					t.Fatal("formal read did not consume")
				}
				history, _ := historyMCPCall(t, a, token, "retained", "history_read", args)
				var past struct {
					Entries []struct {
						Seq         uint64           `json:"seq"`
						Attachments []RoomAttachment `json:"attachments"`
					} `json:"entries"`
				}
				if err := json.Unmarshal([]byte(history.Content[0].(mcp.TextContent).Text), &past); err != nil || !reflect.DeepEqual(past.Entries, body.Entries) {
					t.Fatal("history lost native refs", err)
				}
				before := historyTree(t, h.store.dir)
				historyBefore := *s.storage.history
				ctx := context.WithValue(h.ctx, roomActionContextKey{}, roomActionContext{ServiceInfo: s.service.info, MemberIdentity: s.storage.id, Turn: 1, ID: "selected-retained-originals"})
				lease, err := s.service.prepareRoomActionImages(ctx, refs, contextGuard)
				if err != nil {
					t.Fatal("read state/model budget blocked original bytes", err)
				}
				for _, img := range lease.images {
					if !bytes.Equal(img.Data, attachmentTestPNG(t, false)) {
						t.Fatal("original bytes lost")
					}
				}
				lease.Close()
				historyAfter := *s.storage.history
				historyBefore.now, historyAfter.now = nil, nil
				if !reflect.DeepEqual(before, historyTree(t, h.store.dir)) || !reflect.DeepEqual(historyBefore, historyAfter) {
					t.Fatal("action source changed read state, budget or History query")
				}
				// A stored model page remains replayable after object loss; the action
				// cannot use that page as an archive of original source bytes.
				if err := os.Remove(filepath.Join(h.store.attachmentPath(one.Attachment.ID), "data")); err != nil {
					t.Fatal(err)
				}
				replay, _ := historyMCPCall(t, a, token, "retained", "history_read", args)
				if !reflect.DeepEqual(replay, history) {
					t.Fatal("cached History replay changed")
				}
				if lease, err := s.service.prepareRoomActionImages(ctx, refs, contextGuard); err == nil {
					lease.Close()
					t.Fatal("page cache substituted for missing originals")
				}
				assertActionImagePinsReleased(t, h.store)
			})
		}
	}
}

func TestRoomActionImagesCapturesPersistedSessionGeneration(t *testing.T) {
	h, svc, ctx, refs := actionImagesFixture(t, true)
	call := ctx.Value(roomActionContextKey{}).(roomActionContext)
	path := filepath.Join(h.store.dir, "agents", call.MembershipID, "session.json")
	ref := sessionReference{Version: formatVersion, MemberIdentity: call.MemberIdentity, Directory: filepath.Base(dshStateDir(path)) + "-" + strings.Repeat("a", 32)}
	if err := os.WriteFile(path, mustJSON(t, ref), 0600); err != nil {
		t.Fatal(err)
	}
	lease, err := svc.prepareRoomActionImages(ctx, refs, contextGuard)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if lease.execution != (turnExecution{Generation: ref.Directory, Turn: 1}) {
		t.Fatal("lost actual execution generation", lease.execution)
	}
	if err := lease.Check(ctx); err != nil {
		t.Fatal(err)
	}
	ref.Directory = filepath.Base(dshStateDir(path)) + "-" + strings.Repeat("b", 32)
	if err := os.WriteFile(path, mustJSON(t, ref), 0600); err != nil {
		t.Fatal(err)
	}
	if err := lease.Check(ctx); err == nil {
		t.Fatal("session rotation did not invalidate source lease")
	}
	lease.Close()
	assertActionImagePinsReleased(t, h.store)
}
