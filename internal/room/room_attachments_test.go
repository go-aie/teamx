package room

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func attachmentTestID(n int) string { return fmt.Sprintf("img_%032x", n) }
func attachmentTestPNG(t *testing.T, noisy bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	var n uint32 = 1
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			n = 1664525*n + 1013904223
			c := color.NRGBA{uint8(n), uint8(n >> 8), uint8(n >> 16), 255}
			if !noisy {
				c = color.NRGBA{255, 0, 0, 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func uploadTestAttachment(t *testing.T, s *Store, n int) roomAttachmentMeta {
	t.Helper()
	m, err := s.uploadAttachment(context.Background(), humanCaller.ID, attachmentTestID(n), "test.png", bytes.NewReader(attachmentTestPNG(t, false)))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func TestRoomAttachmentsUploadValidationAndIdentity(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	saveTestRoom(t, s)
	data := attachmentTestPNG(t, false)
	m, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(1), "../../截图\n.png", bytes.NewReader(data))
	if err != nil || m.Attachment.Name != "截图.png" {
		t.Fatal(m, err)
	}
	again, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(1), "截图.png", bytes.NewReader(data))
	if err != nil || again != m {
		t.Fatal(again, err)
	}
	for _, test := range []struct {
		operator, name string
		data           []byte
	}{{"other", "截图.png", data}, {"user", "other.png", data}, {"user", "截图.png", attachmentTestPNG(t, true)}} {
		_, err := s.uploadAttachment(context.Background(), test.operator, attachmentTestID(1), test.name, bytes.NewReader(test.data))
		if !errors.Is(err, ErrConflict) {
			t.Fatal("overwrote immutable object", err)
		}
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(2), "misnamed.png", &jpg); err != nil {
		t.Fatal("content format must win", err)
	}
	for i, b := range [][]byte{data[:len(data)/2], []byte("GIF89a"), []byte("<svg/>"), bytes.Repeat([]byte{0}, int(roomAttachmentBytes)+1)} {
		if _, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(10+i), "image.png", bytes.NewReader(b)); err == nil {
			t.Fatal("accepted invalid image", i)
		}
	}
	if _, err := s.uploadAttachment(context.Background(), "user", "../../escape", "x", bytes.NewReader(data)); !errors.Is(err, ErrRequest) {
		t.Fatal(err)
	}
	if err := s.check(true); err != nil {
		t.Fatal("ordinary upload error poisoned Room", err)
	}
	s.mu.Lock()
	s.attachmentStorage().limits.MaxPixels = 1
	s.mu.Unlock()
	if _, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(30), "x", bytes.NewReader(data)); err == nil {
		t.Fatal("pixel limit ignored")
	}
}
func TestRoomAttachmentsMessagesRetryAndRecovery(t *testing.T) {
	for _, log := range []bool{false, true} {
		t.Run(fmt.Sprint(log), func(t *testing.T) {
			state := emptyRoom()
			if log {
				state.InputLog = &roomLogRoot{Version: 1, Segments: []roomLogSegment{}}
			}
			h := newRoomHarness(t, state, nil)
			one := uploadTestAttachment(t, h.store, 1)
			two := uploadTestAttachment(t, h.store, 2)
			inputs := []RoomAttachmentInput{{one.Attachment.ID}, {two.Attachment.ID}}
			post := PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "image-only", Attachments: inputs}
			receipt := roomCall[PostReceipt](t, h, post)
			inputs[0].ID = attachmentTestID(99) // request slices do not alias committed state
			post.Attachments = []RoomAttachmentInput{{one.Attachment.ID}, {two.Attachment.ID}}
			if got := roomCall[PostReceipt](t, h, post); got != receipt {
				t.Fatal(got, receipt)
			}
			reverse := post
			reverse.Attachments = []RoomAttachmentInput{{two.Attachment.ID}, {one.Attachment.ID}}
			roomError[PostReceipt](t, h, reverse, ErrConflict)
			duplicate := post
			duplicate.ClientMessageID = "duplicate"
			duplicate.Attachments = []RoomAttachmentInput{{one.Attachment.ID}, {one.Attachment.ID}}
			roomError[PostReceipt](t, h, duplicate, ErrRequest)
			foreign := post
			foreign.ClientMessageID = "foreign"
			foreign.Caller.ID = "other"
			roomError[PostReceipt](t, h, foreign, ErrIdentity)
			loaded, err := h.store.LoadRoom()
			if err != nil || len(loaded.Messages) != 1 || !reflect.DeepEqual(loaded.Messages[0].Attachments, []RoomAttachment{one.Attachment, two.Attachment}) {
				t.Fatal(loaded, err)
			}
			loaded.Messages[0].Attachments[0].Name = "mutated"
			page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadLatest, Latest: 10})
			page.Messages[0].Attachments[0].Name = "mutated"
			loaded, err = h.store.LoadRoom()
			if err != nil || loaded.Messages[0].Attachments[0].Name != "test.png" {
				t.Fatal("shared cached slice", loaded, err)
			}
			h.reload(t, true)
			if got := roomCall[PostReceipt](t, h, post); got != receipt {
				t.Fatal("restart duplicated message")
			}
			if err := os.Remove(filepath.Join(h.store.attachmentPath(one.Attachment.ID), "data")); err != nil {
				t.Fatal(err)
			}
			if got := roomCall[PostReceipt](t, h, post); got != receipt {
				t.Fatal("missing old image broke retry")
			}
			roomCall[PostReceipt](t, h, h.post(humanCaller, "text-after-damage", "still writable"))
		})
	}
}
func TestRoomAttachmentsReferenceRetentionAndPins(t *testing.T) {
	for _, log := range []bool{false, true} {
		t.Run(fmt.Sprint(log), func(t *testing.T) {
			state := emptyRoom()
			if log {
				state.InputLog = &roomLogRoot{Version: 1, Segments: []roomLogSegment{}}
			}
			h := newRoomHarness(t, state, nil)
			one := uploadTestAttachment(t, h.store, 1)
			orphan := uploadTestAttachment(t, h.store, 2)
			roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "image", Attachments: []RoomAttachmentInput{{one.Attachment.ID}}})
			h.store.mu.Lock()
			h.store.attachmentStorage().now = func() time.Time { return one.CreatedAt.Add(25 * time.Hour) }
			h.store.mu.Unlock()
			meta, release, err := h.store.pinAttachment("user", one.Attachment.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 1})
			if _, _, err := h.store.readAttachment(meta); err != nil {
				t.Fatal("active read lost bytes", err)
			}
			if _, err := os.Stat(h.store.attachmentPath(orphan.Attachment.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("orphan retained", err)
			}
			release()
			h.store.mu.Lock()
			_, err = h.store.loadRoom()
			if err == nil {
				err = h.store.collectAttachments()
			}
			h.store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(h.store.attachmentPath(one.Attachment.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("cleared image retained", err)
			}
		})
	}
}
func TestRoomAttachmentsPublicationFaults(t *testing.T) {
	for _, stage := range []string{"object-rename", "object-sync", "message-before", "message-unknown", "clear-before", "clear-unknown"} {
		t.Run(stage, func(t *testing.T) {
			s := openTestStore(t, t.TempDir())
			v := emptyRoom()
			if err := s.SaveRoom(v); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("disk fault")
			rename, syncDir := s.ops.rename, s.ops.syncDir
			if strings.HasPrefix(stage, "object-") {
				s.ops.rename = func(a, b string) error {
					if filepath.Base(b) == attachmentTestID(1) && stage == "object-rename" {
						return fault
					}
					return rename(a, b)
				}
				s.ops.syncDir = func(p string) error {
					if p == filepath.Join(s.attachmentDir(), "objects") && stage == "object-sync" {
						return fault
					}
					return syncDir(p)
				}
				_, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(1), "test.png", bytes.NewReader(attachmentTestPNG(t, false)))
				if err == nil {
					t.Fatal("fault ignored")
				}
				if err := s.check(true); err != nil {
					t.Fatal("upload poisoned Room", err)
				}
				s.ops.rename, s.ops.syncDir = rename, syncDir
				if _, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(1), "test.png", bytes.NewReader(attachmentTestPNG(t, false))); err != nil {
					t.Fatal("same ID recovery", err)
				}
				return
			}
			m := uploadTestAttachment(t, s, 1)
			v.LatestSeq = 1
			v.Revision = 1
			v.Messages = []RoomMessage{{Seq: 1, ClientMessageID: "image", Author: humanCaller.Author, Attachments: []RoomAttachment{m.Attachment}, CreatedAt: time.Now()}}
			if strings.HasPrefix(stage, "clear-") {
				if err := s.SaveRoom(v); err != nil {
					t.Fatal(err)
				}
				v.ClearedThrough = 1
				v.Revision++
				v.Messages = []RoomMessage{}
			}
			// Pre-replace failures are injected before rename is attempted; rename
			// failures themselves intentionally belong to the uncertain boundary.
			create := s.ops.createTemp
			s.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
				if strings.HasSuffix(stage, "before") {
					return nil, fault
				}
				return create(dir, pattern)
			}
			s.ops.syncDir = func(p string) error {
				if p == s.dir && strings.HasSuffix(stage, "unknown") {
					return fault
				}
				return syncDir(p)
			}
			if err := s.SaveRoom(v); err == nil {
				t.Fatal("publication fault ignored")
			}
			s.mu.Lock()
			s.attachmentStorage().now = func() time.Time { return m.CreatedAt.Add(25 * time.Hour) }
			// No reclamation may use an uncommitted candidate or an uncertain root.
			if !strings.HasPrefix(stage, "message-before") {
				_ = s.collectAttachments()
			}
			s.mu.Unlock()
			if _, err := os.Stat(s.attachmentPath(m.Attachment.ID)); err != nil {
				t.Fatal("publication failure lost image", err)
			}
		})
	}
}
func TestRoomAttachmentsRejectSymlinksAndCorruption(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	saveTestRoom(t, s)
	m := uploadTestAttachment(t, s, 1)
	dataPath := filepath.Join(s.attachmentPath(m.Attachment.ID), "data")
	original, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, bytes.Repeat([]byte{'x'}, len(original)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.readAttachment(m); err == nil {
		t.Fatal("digest ignored")
	}
	if err := os.Remove(dataPath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dataPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.readAttachment(m); err == nil {
		t.Fatal("followed symlink")
	}
	bad := sampleRoom()
	bad.LatestSeq++
	bad.Revision++
	bad.Messages = append(bad.Messages, RoomMessage{Seq: 3, ClientMessageID: "bad", Author: humanCaller.Author, Attachments: []RoomAttachment{m.Attachment}, CreatedAt: time.Now()})
	if err := s.SaveRoom(bad); err == nil {
		t.Fatal("published damaged object")
	}
	plain := sampleRoom()
	plain.LatestSeq++
	plain.Revision++
	plain.Messages = append(plain.Messages, RoomMessage{Seq: 3, ClientMessageID: "plain", Author: humanCaller.Author, Text: "safe", CreatedAt: time.Now()})
	if err := s.SaveRoom(plain); err != nil {
		t.Fatal("damaged object blocked unrelated text", err)
	}
}
func TestRoomAttachmentsQuotaReservationsAndClose(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	saveTestRoom(t, s)
	s.mu.Lock()
	s.attachmentStorage().quotaObjects = 1
	s.mu.Unlock()
	entered := make(chan struct{})
	release := make(chan struct{})
	data := attachmentTestPNG(t, false)
	reader := &attachmentBlockingReader{reader: bytes.NewReader(data), entered: entered, release: release}
	uploaded := make(chan error, 1)
	go func() {
		_, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(1), "x.png", reader)
		uploaded <- err
	}()
	<-entered
	if _, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(1), "x.png", bytes.NewReader(data)); !errors.Is(err, ErrBusy) {
		t.Fatal("same ID raced", err)
	}
	if _, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(2), "x.png", bytes.NewReader(data)); !errors.Is(err, ErrCapacity) {
		t.Fatal("reservation ignored", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		t.Fatal("close released live upload", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-uploaded; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(3), "x.png", bytes.NewReader(data)); !errors.Is(err, ErrStoreClosed) {
		t.Fatal("closed store admitted upload", err)
	}
}

type attachmentBlockingReader struct {
	reader  io.Reader
	entered chan struct{}
	release chan struct{}
	started bool
}

func (r *attachmentBlockingReader) Read(b []byte) (int, error) {
	if !r.started {
		r.started = true
		close(r.entered)
		<-r.release
	}
	return r.reader.Read(b)
}

func TestRoomAttachmentsCommitProtectionAcrossExpiry(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	v := emptyRoom()
	if err := s.SaveRoom(v); err != nil {
		t.Fatal(err)
	}
	m := uploadTestAttachment(t, s, 1)
	attachments, release, err := s.prepareRoomAttachments("user", []RoomAttachmentInput{{m.Attachment.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	s.mu.Lock()
	s.attachmentStorage().now = func() time.Time { return m.CreatedAt.Add(25 * time.Hour) }
	err = s.collectAttachments()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	v.Revision = 1
	v.LatestSeq = 1
	v.Messages = []RoomMessage{{Seq: 1, ClientMessageID: "prepared", Author: humanCaller.Author, Attachments: attachments, CreatedAt: time.Now()}}
	if err := s.SaveRoom(v); err != nil {
		t.Fatal("commit lost accepted draft protection", err)
	}
	release()
	s.mu.Lock()
	err = s.collectAttachments()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.readAttachment(m); err != nil {
		t.Fatal("native reference was not retained", err)
	}
	// Storage validation protects old descriptors without revisiting old bytes.
	changed := v
	changed.Messages = cloneRoomLogMessages(v.Messages)
	changed.Messages[0].Attachments[0].Name = "forged.png"
	if err := s.SaveRoom(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("rewrote old descriptor", err)
	}
}
func TestRoomAttachmentsCurrentLimitsAndRebuiltQuota(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	saveTestRoom(t, s)
	one := uploadTestAttachment(t, s, 1)
	two := uploadTestAttachment(t, s, 2)
	s.mu.Lock()
	s.attachmentStorage().limits.MaxImages = 1
	s.mu.Unlock()
	_, release, err := s.prepareRoomAttachments("user", []RoomAttachmentInput{{one.Attachment.ID}, {two.Attachment.ID}})
	if release != nil {
		release()
	}
	if !errors.Is(err, ErrRequest) {
		t.Fatal("count ignored", err)
	}
	s.mu.Lock()
	s.attachmentStorage().limits.MaxImages = 4
	s.attachmentStorage().limits.TotalBytes = one.Attachment.Size + two.Attachment.Size - 1
	s.mu.Unlock()
	_, release, err = s.prepareRoomAttachments("user", []RoomAttachmentInput{{one.Attachment.ID}, {two.Attachment.ID}})
	if release != nil {
		release()
	}
	if !errors.Is(err, ErrRequest) {
		t.Fatal("total bytes ignored", err)
	}
	dir := s.dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, dir)
	s.mu.Lock()
	size, count, err := s.attachmentUsage("")
	s.attachmentStorage().quotaBytes = size + roomAttachmentBytes + 4095
	s.mu.Unlock()
	if err != nil || count != 2 || size <= one.Attachment.Size+two.Attachment.Size {
		t.Fatal("restart capacity accounting", size, count, err)
	}
	if _, err := s.uploadAttachment(context.Background(), "user", attachmentTestID(3), "test.png", bytes.NewReader(attachmentTestPNG(t, false))); !errors.Is(err, ErrCapacity) {
		t.Fatal("restart bypassed byte quota", err)
	}
	// The original ID remains retryable without reserving another stored object,
	// even when the limits for new uploads have since been lowered.
	s.mu.Lock()
	s.attachmentStorage().limits.ImageBytes = 1
	s.mu.Unlock()
	if _, err := s.uploadAttachment(context.Background(), "user", one.Attachment.ID, "test.png", bytes.NewReader(attachmentTestPNG(t, false))); err != nil {
		t.Fatal("full Room broke upload retry", err)
	}
}

func TestRoomAttachmentsRetryReplacesInterruptedReceiverWithinQuota(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	saveTestRoom(t, s)
	if err := s.ensureAttachmentDirs(); err != nil {
		t.Fatal(err)
	}
	id := attachmentTestID(1)
	tmp := filepath.Join(s.attachmentDir(), "tmp", id)
	if err := os.Mkdir(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "data"), []byte("interrupted receive"), 0600); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.attachmentStorage().quotaObjects = 1
	s.mu.Unlock()
	if _, err := s.uploadAttachment(context.Background(), "user", id, "test.png", bytes.NewReader(attachmentTestPNG(t, false))); err != nil {
		t.Fatal("counted same receiver twice", err)
	}
	s.mu.Lock()
	_, count, err := s.attachmentUsage("")
	s.mu.Unlock()
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
