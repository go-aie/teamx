package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestUnifiedRoomFormalImagesShareBudgetAndRecheckCachedAuthority(t *testing.T) {
	state := historyRoom(1, "查看历史消息")
	item := storedGitLab(MemberIdentity{}, "room-image", "events", "7", "42", "保存的图片 ![first](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/one.png)")
	item.Source.ProjectID = 7
	item.Source.URL = "https://gitlab.example.com/group/repo/-/merge_requests/42"
	addHistoricalNotification(&state, item)
	h, s := newUnifiedReaderFixture(t, state, 10*time.Second)
	s.images = true
	s.service.imageMaterials = imageMaterialConfig{MaxImages: 1}
	downloads, checks := 0, 0
	allowed := true
	s.service.materialAttachments = &materialAttachments{download: func(_ context.Context, req attachmentRequest, reader *attachmentReader, _ attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		if req.Seq != 2 || req.SourceRef != "" || reader.MemberIdentity != s.storage.id {
			t.Fatal(req)
		}
		downloads++
		return []byte("fixture-image"), "image/png", &attachmentAccess{}, nil
	}, check: func(_ context.Context, _ ServiceInfo, _ attachmentReader, _ TurnRecord, notices []materialNotice, _ []*attachmentAccess) error {
		checks++
		if len(notices) == 0 {
			t.Fatal("cached Room image authority not rechecked")
		}
		if !allowed {
			return ErrIdentity
		}
		return nil
	}}
	c, err := s.legacyCatalog(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Preview(roomStreamRef(s.storage.id), 3); err != nil {
		t.Fatal(err)
	}
	if downloads != 0 {
		t.Fatal("preview downloaded")
	}
	args := platformArgs(1, roomStreamRef(s.storage.id), "room-images")
	result, confirm, release, err := s.read(h.ctx, args)
	if err != nil {
		if release != nil {
			release()
		}
		t.Fatal(err)
	}
	imageCount := 0
	for _, block := range result.Content {
		if _, ok := block.(mcp.ImageContent); ok {
			imageCount++
		}
	}
	if imageCount != 1 || downloads != 1 {
		t.Fatal(imageCount, downloads)
	}
	allowed = false
	if err := confirm(h.ctx); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	release()
	if h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal("revoked output confirmed")
	}
	allowed = true
	v := deliverRoom(t, s, inputstream.ReadRequest{Turn: 1, Stream: roomStreamRef(s.storage.id), ID: "room-images"})
	if downloads != 1 || checks < 3 || len(v.Images) != 1 || v.Images[0].RoomSeq != 2 || v.Images[0].Source != "" {
		t.Fatal(v.Images, downloads, checks)
	}
	b, _ := json.Marshal(v)
	for _, field := range []string{`"reference"`, `"subscription_id"`, `"recipient_id"`, `"partition"`} {
		if bytes.Contains(b, []byte(field)) {
			t.Fatal("internal source evidence exposed", field)
		}
	}
}
