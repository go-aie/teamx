package room

import (
	"context"
	"fmt"
	"strings"
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
