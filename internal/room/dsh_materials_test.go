package room

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func materialInput(id MemberIdentity, roomEnd, inboxEnd uint64) UnreadChanged {
	input := UnreadChanged{MemberIdentity: id, LatestSeq: roomEnd, NewestUnreadSeq: roomEnd}
	if inboxEnd > 0 {
		input.Inbox = &InboxInput{SnapshotSeq: inboxEnd}
	}
	return input
}
func materialText(blocks []dsh.ContentBlock) string {
	var out strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			out.WriteString(block.Text)
			out.WriteByte('\n')
		}
	}
	return out.String()
}
func materialImageCount(blocks []dsh.ContentBlock) int {
	count := 0
	for _, block := range blocks {
		if block.Type == "image" {
			count++
		}
	}
	return count
}
func saveMaterialTurn(t *testing.T, svc *service, id MemberIdentity, roomEnd, inboxEnd uint64) {
	t.Helper()
	agent := AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: roomEnd, LastOfferedInboxSeq: inboxEnd, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: roomEnd, OfferedUnreadSeq: roomEnd, InboxSnapshotSeq: inboxEnd}}
	if err := svc.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
}
func addMaterialNotice(t *testing.T, h *eventHarness, svc *service, id MemberIdentity, n int, body string, inbox bool) {
	t.Helper()
	event := h.event(int64(n), 0)
	event.Kind, event.NoteBody = "note", body
	item := gitlab.EventsMessage(h.state(), gitlab.EventsInboxEntry{DeliveryID: fmt.Sprintf("material-%d", n), Event: event})
	if inbox {
		if err := svc.receiveInbox(context.Background(), inboxTarget(id), item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.room.Call[PostReceipt](context.Background(), postIntegrationMessage{RoomIdentity: id.RoomIdentity, DeliveryID: item.DeliveryID, Text: item.Text, Source: item.Source}); err != nil {
		t.Fatal(err)
	}
}

func TestDSHMaterialsProductionAdapterFixedRangesAndNoConsumption(t *testing.T) {
	h, svc, req, reader, data := modelAttachmentFixture(t)
	h.m.bindPlatformReferences(svc)
	id := reader.MemberIdentity
	beforeRoom, _ := svc.store.LoadRoom()
	beforeAgent, _ := svc.store.LoadAgent(id)
	beforeInbox, err := svc.memberInboxStatus(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	var received []dsh.ContentBlock
	component := &adapterComponent{images: true, runPrompt: func(_ context.Context, prompt []dsh.ContentBlock) (dsh.TurnResult, error) {
		received = prompt
		return dsh.TurnResult{}, nil
	}}
	backend := svc.newMember(id, RunnerConfig{}, nil).(*MemberAgent).Backend.(dshBackend)
	if backend.prepare == nil || svc.materialAttachments == nil {
		t.Fatal("production preparation not wired")
	}
	session := &dshSession{backend: backend, opts: adapterOptions(svc.store, id), component: component}
	input := materialInput(id, 1, 1)
	for range 2 {
		if err := session.Run(context.Background(), input); err != nil {
			t.Fatal(err)
		}
		text := materialText(received)
		if materialImageCount(received) != 1 || !bytes.Equal(received[4].Data, data) {
			t.Fatal("missing or unordered image blocks", received)
		}
		for _, want := range []string{"Member policy context", "Reference followup", materialWarning, "Room #1", "Inbox #1", "图片说明", "Same source attachment as Room #1", "image positions [1]"} {
			if !strings.Contains(text, want) {
				t.Fatal("missing material", want)
			}
		}
		if !strings.HasPrefix(received[2].Text, "Third-party material: Room #1;") || received[3].Type != "text" || received[4].Type != "image" || !strings.HasPrefix(received[5].Text, "Third-party material: Inbox #1;") {
			t.Fatal("group association lost")
		}
		if strings.Contains(text, "test-platform-secret") || strings.Contains(text, "/v1/platform/attachments") {
			t.Fatal("credential/proxy URL exposed")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("dedup persisted across turns or duplicate downloaded", calls.Load())
	}
	afterRoom, _ := svc.store.LoadRoom()
	afterAgent, _ := svc.store.LoadAgent(id)
	afterInbox, _ := svc.memberInboxStatus(context.Background(), id)
	if !reflect.DeepEqual(beforeRoom, afterRoom) || !reflect.DeepEqual(beforeAgent, afterAgent) || !reflect.DeepEqual(beforeInbox, afterInbox) {
		t.Fatal("preview consumed or changed state")
	}
	if _, err := os.Stat(filepath.Join(svc.store.dir, "agents", id.MembershipID, "followups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview created matters", err)
	}
	// New notifications arriving during download cannot enter this prompt.
	h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
		addMaterialNotice(t, h, svc, id, 2, "later-material ![new]("+req.Path+")", true)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	if err := session.Run(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if text := materialText(received); strings.Contains(text, "later-material") || strings.Contains(text, "Room #2") || strings.Contains(text, "Inbox #2") {
		t.Fatal("snapshot expanded")
	}
}

func TestDSHMaterialsScanImageBudgetsAndPartialFailures(t *testing.T) {
	h, svc, req, reader, data := modelAttachmentFixture(t)
	h.m.bindPlatformReferences(svc)
	id := reader.MemberIdentity
	body := "正文已截断，内容可能不完整\n![same](" + req.Path + ") ![same again](" + req.Path + ") ![bad](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/bad.png) ![other](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/other.png) ![external](https://example.invalid/x.png)"
	addMaterialNotice(t, h, svc, id, 2, body, true)
	saveMaterialTurn(t, svc, id, 2, 2)
	svc.imageMaterials = imageMaterialConfig{MaxImages: 1, ScanEntries: 3}
	var paths []string
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	materials, err := svc.prepareDSHMaterials(context.Background(), materialInput(id, 2, 2), 1, true)
	if err != nil {
		t.Fatal(err)
	}
	blocks := materials.blocks("base")
	text := materialText(blocks)
	for _, want := range []string{"Room #2 image positions [1 2]", "count budget exceeded", "unsupported or incomplete", "正文已截断，内容可能不完整", "Inbox #2..#2 not scanned", "1 entries remain"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing budget evidence", want, text)
		}
	}
	if len(paths) != 1 || materialImageCount(blocks) != 1 {
		t.Fatal("image budget exceeded", paths)
	}
	// Failed first image does not prevent an independent second one.
	svc.imageMaterials = imageMaterialConfig{ScanEntries: 2}
	paths = nil
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		code := 200
		payload := data
		if strings.HasSuffix(r.URL.Path, "image.png") {
			code = 404
		}
		if strings.HasSuffix(r.URL.Path, "bad.png") {
			payload = []byte("invalid raster")
		}
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(payload))}, nil
	})
	materials, err = svc.prepareDSHMaterials(context.Background(), materialInput(id, 2, 2), 1, true)
	if err != nil {
		t.Fatal(err)
	}
	text = materialText(materials.blocks("base"))
	if len(paths) != 3 || materialImageCount(materials.blocks("base")) != 1 || !strings.Contains(text, "attachment unavailable") || !strings.Contains(text, "invalid image format") {
		t.Fatal("partial failures lost", paths, text)
	}
}

func TestDSHMaterialsTextOnlyAndBytePixelBudgets(t *testing.T) {
	for _, mode := range []string{"text", "single", "total", "pixels"} {
		t.Run(mode, func(t *testing.T) {
			h, svc, req, reader, data := modelAttachmentFixture(t)
			h.m.bindPlatformReferences(svc)
			addMaterialNotice(t, h, svc, reader.MemberIdentity, 2, "![two](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/two.png)", false)
			saveMaterialTurn(t, svc, reader.MemberIdentity, 2, 1)
			switch mode {
			case "single":
				svc.imageMaterials.ImageBytes = int64(len(data) - 1)
			case "total":
				svc.imageMaterials.TotalBytes = int64(len(data))
			case "pixels":
				svc.imageMaterials.MaxPixels = 5
			}
			var calls int
			h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
			})
			materials, err := svc.prepareDSHMaterials(context.Background(), materialInput(reader.MemberIdentity, 2, 1), 1, mode != "text")
			if err != nil {
				t.Fatal(err)
			}
			blocks := materials.blocks("base")
			text := materialText(blocks)
			if mode == "text" {
				if calls != 0 || materialImageCount(blocks) != 0 || !strings.Contains(text, "current session is text-only") {
					t.Fatal("text model fetched", text)
				}
			} else if mode == "total" {
				if calls != 1 || materialImageCount(blocks) != 1 || !strings.Contains(text, "turn image byte budget exceeded") {
					t.Fatal("total budget failed", text)
				}
			} else if materialImageCount(blocks) != 0 || !strings.Contains(text, "byte/pixel") {
				t.Fatal("single image limit failed", req.Path, text)
			}
		})
	}
}

func TestDSHMaterialsTimeoutDegradesButCancellationAndInvalidTurnAbort(t *testing.T) {
	h, svc, _, reader, _ := modelAttachmentFixture(t)
	h.m.bindPlatformReferences(svc)
	input := materialInput(reader.MemberIdentity, 1, 1)
	svc.imageMaterials.Timeout = "10ms"
	var calls atomic.Int32
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	materials, err := svc.prepareDSHMaterials(context.Background(), input, 1, true)
	if err != nil {
		t.Fatal("preparation timeout aborted usable text", err)
	}
	text := materialText(materials.blocks("base"))
	if materialImageCount(materials.blocks("base")) != 0 || (!strings.Contains(text, "timed out") && !strings.Contains(text, "time budget exceeded")) {
		t.Fatal("timeout omitted missing-material status", text)
	}
	beforeInvalid := calls.Load()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.prepareDSHMaterials(canceled, input, 1, true); err == nil {
		t.Fatal("canceled turn retained prompt")
	}
	for _, mutate := range []func(*UnreadChanged){
		func(i *UnreadChanged) { i.LatestSeq++ }, func(i *UnreadChanged) { i.Inbox = nil }, func(i *UnreadChanged) { i.MembershipID = strings.Repeat("f", 32) }, func(i *UnreadChanged) { i.Inbox = &InboxInput{Through: 1, SnapshotSeq: 2} },
	} {
		i := input
		mutate(&i)
		if _, err := svc.prepareDSHMaterials(context.Background(), i, 1, true); err == nil {
			t.Fatal("invalid range accepted")
		}
	}
	if calls.Load() != beforeInvalid {
		t.Fatal("invalid scope reached network", calls.Load())
	}
	if _, err := svc.prepareDSHMaterials(context.Background(), input, 2, true); err == nil {
		t.Fatal("wrong turn accepted")
	}
}

func TestDSHMaterialsFinalCheckRejectsRevocationBeforePrompt(t *testing.T) {
	for _, mode := range []string{"turn", "member", "credential", "epoch"} {
		t.Run(mode, func(t *testing.T) {
			h, svc, _, reader, data := modelAttachmentFixture(t)
			h.m.bindPlatformReferences(svc)
			h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
			})
			materials, err := svc.prepareDSHMaterials(context.Background(), materialInput(reader.MemberIdentity, 1, 1), 1, true)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "turn":
				agent, _ := svc.store.LoadAgent(reader.MemberIdentity)
				agent.Turn.Status = "completed"
				if err := svc.store.SaveAgent(agent); err != nil {
					t.Fatal(err)
				}
			case "member":
				finish, err := h.m.beginPlatformMemberChange(context.Background(), svc, reader.MemberIdentity)
				if err != nil {
					t.Fatal(err)
				}
				defer finish()
			case "credential":
				connection := platformTestList(t, h.m)[0]
				update := platformTestRequest(h.m)
				update.Version, update.PAT, update.ReferencedRooms = connection.Version, "replacement-test-token", []RoomIdentity{reader.RoomIdentity}
				if _, err := h.m.updatePlatformConnection(update, false); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				old := svc.info
				svc.info.ServiceEpoch = "replacement-epoch"
				defer func() { svc.info = old }()
			}
			if err := materials.check(context.Background()); err == nil {
				t.Fatal("revoked prepared image accepted")
			}
			var submitted bool
			session := &dshSession{backend: dshBackend{store: svc.store, prepare: func(context.Context, UnreadChanged, uint64, bool) (*dshMaterials, error) { return materials, nil }}, opts: adapterOptions(svc.store, reader.MemberIdentity), component: &adapterComponent{images: true, runPrompt: func(context.Context, []dsh.ContentBlock) (dsh.TurnResult, error) {
				submitted = true
				return dsh.TurnResult{}, nil
			}}}
			if err := session.Run(context.Background(), materialInput(reader.MemberIdentity, 1, 1)); err == nil || submitted {
				t.Fatal("invalid prepared material submitted", err)
			}
		})
	}
}

func TestDSHMaterialsPromptBudgetsCountEscapingAndBase64(t *testing.T) {
	group := materialGroup{notice: materialNotice{request: attachmentRequest{Seq: 1}, text: strings.Repeat("<", 700<<10), images: notificationImageReferences{References: 2}}, images: []materialImage{
		{positions: []int{1}, status: "read: image block supplied below", block: &dsh.ContentBlock{Type: "image", MIMEType: "image/png", Data: make([]byte, 2<<20)}},
		{positions: []int{2}, status: "read: image block supplied below", block: &dsh.ContentBlock{Type: "image", MIMEType: "image/png", Data: make([]byte, 2<<20)}},
	}}
	materials := &dshMaterials{groups: []materialGroup{group}}
	blocks := materials.blocks("base")
	if !materialPromptFits(blocks) || materialImageCount(blocks) >= 2 || !strings.Contains(materialText(blocks), "prompt frame budget exceeded") {
		t.Fatal("escaped text/base64 did not trim images")
	}
	materials.groups = append(materials.groups, group)
	blocks = materials.blocks("base")
	if !materialPromptFits(blocks) || !strings.Contains(materialText(blocks), "saved text and all 2 image references not supplied") {
		t.Fatal("oversized text not explicitly omitted")
	}
	if materials.groups[0].images[1].block == nil {
		t.Fatal("render modified preparation evidence")
	}
}

func TestDSHMaterialsConfigBounds(t *testing.T) {
	config, err := loadStartupConfig(writeStartupConfig(t, t.TempDir(), "[server.image_materials]\nscan_entries=2\nmax_images=1\nimage_bytes=1024\ntotal_bytes=2048\nmax_pixels=64\ntimeout='5s'\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	limits, err := config.Server.ImageMaterials.limits()
	if err != nil || limits.scan != 2 || limits.images != 1 || limits.timeout != 5*time.Second || limits.imageBytes != 1024 || limits.totalBytes != 2048 || limits.pixels != 64 {
		t.Fatal("config lost", limits, err)
	}
	for _, invalid := range []string{"scan_entries=101", "max_images=-1", "image_bytes=4194305", "total_bytes=4194305", "max_pixels=-1", "timeout='0s'", "timeout='3m'"} {
		if _, err := loadStartupConfig(writeStartupConfig(t, t.TempDir(), "[server.image_materials]\n"+invalid+"\n"), true); err == nil {
			t.Fatal("invalid service budget accepted", invalid)
		}
	}
}

func TestDSHMaterialsPreviewFailureDoesNotReplaceFrozenRange(t *testing.T) {
	h, svc, _, reader, data := modelAttachmentFixture(t)
	h.m.bindPlatformReferences(svc)
	path := filepath.Join(svc.store.dir, "inboxes", reader.MembershipID, inboxEntryName(1))
	moved := path + ".hold"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(moved, path)
	h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	materials, err := svc.prepareDSHMaterials(context.Background(), materialInput(reader.MemberIdentity, 1, 1), 1, true)
	if err != nil {
		t.Fatal(err)
	}
	text := materialText(materials.blocks("base"))
	if !strings.Contains(text, "Inbox #1..#1 not scanned: preview unavailable") || strings.Contains(text, "Third-party material: Inbox #1") || materialImageCount(materials.blocks("base")) != 1 {
		t.Fatal("preview failure substituted content", text)
	}
}

func TestDSHMaterialsInboxPaginationAndIndependentSequenceOrder(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{Entries: 100, Bytes: 1 << 20, EntryBytes: 128 << 10, PageSize: 1, PageBytes: 128 << 10})
	source := referenceTestSource("first")
	source.ProjectID, source.Project = 123, "org/repo"
	for n := 1; n <= 3; n++ {
		msg := h.receive(fmt.Sprint(n))
		msg.Item.Source = source
		msg.Item.Source.ItemKey = msg.Item.Key
		msg.Item.Text = fmt.Sprintf("entry-%d ![x](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png)", n)
		inboxCall[InboxReceipt](t, h, msg)
	}
	svc := referenceTestService(h)
	saveMaterialTurn(t, svc, h.id, 0, 3)
	agent, _ := h.store.LoadAgent(h.id)
	agent.Turn.InboxStart = 1
	agent.LastOfferedInboxThrough = 1
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	input := materialInput(h.id, 0, 3)
	input.Inbox.Through = 1
	materials, err := svc.prepareDSHMaterials(context.Background(), input, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	text := materialText(materials.blocks("base"))
	first, second := strings.Index(text, "Third-party material: Inbox #2"), strings.Index(text, "Third-party material: Inbox #3")
	if first < 0 || second <= first || strings.Contains(text, "entry-1") || strings.Contains(text, "Third-party material: Room") {
		t.Fatal("fixed Inbox pagination lost", text)
	}
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.ConsumedThrough != 0 || status.Batch != nil {
		t.Fatal("preview created consumption batch", status)
	}
}

func TestDSHMaterialsCanceledDownloadNeverSubmitsPrompt(t *testing.T) {
	h, svc, _, reader, _ := modelAttachmentFixture(t)
	h.m.bindPlatformReferences(svc)
	started := make(chan struct{})
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	var submitted atomic.Bool
	backend := svc.newMember(reader.MemberIdentity, RunnerConfig{}, nil).(*MemberAgent).Backend.(dshBackend)
	session := &dshSession{backend: backend, opts: adapterOptions(svc.store, reader.MemberIdentity), component: &adapterComponent{images: true, runPrompt: func(context.Context, []dsh.ContentBlock) (dsh.TurnResult, error) {
		submitted.Store(true)
		return dsh.TurnResult{}, nil
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- session.Run(ctx, materialInput(reader.MemberIdentity, 1, 1)) }()
	awaitEventSignal(t, started, "material download not started")
	cancel()
	select {
	case err := <-finished:
		if err == nil || submitted.Load() {
			t.Fatal("canceled preparation submitted prompt", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("material download did not settle")
	}
	h.m.mu.Lock()
	pending := len(h.m.platformCalls)
	h.m.mu.Unlock()
	if pending != 0 {
		t.Fatal("canceled material call still registered")
	}
}
