package room

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestInputReceiptReopensExactImagePageBeforeConsumption(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	item := storedGitLab(id, "image", "events", "7", "42", "saved evidence ![diagram](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/one.png)")
	item.Source.ProjectID = 7
	item.Source.URL = "https://gitlab.example.com/group/repo/-/merge_requests/42"
	receivePlatform(t, p, item)
	downloads := 0
	svc.imageMaterials = imageMaterialConfig{MaxImages: 1}
	svc.materialAttachments = &materialAttachments{download: func(context.Context, attachmentRequest, *attachmentReader, attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		downloads++
		return []byte("saved-image-fixture"), "image/png", &attachmentAccess{}, nil
	}}
	s := startPlatformReader(t, h, p, svc, 1)
	s.images = true
	ref := streamFor(t, s, "saved evidence")
	args, _ := json.Marshal(map[string]any{"stream_ref": ref})
	first, flush, release, err := s.readStream(h.ctx, "first-call", args)
	if err != nil {
		t.Fatal(err)
	}
	if err = flush(h.ctx); err != nil {
		t.Fatal(err)
	}
	release()
	if downloads != 1 {
		t.Fatal("image was not supplied", first.Content, downloads)
	}
	images := 0
	for _, block := range first.Content {
		if _, ok := block.(mcp.ImageContent); ok {
			images++
		}
	}
	if images != 1 {
		t.Fatal("missing native image block")
	}
	if p.repository.Barrier(s.execution) == nil {
		t.Fatal("output alone acknowledged the page")
	}
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
	s.images = true
	replayed, _, release, err := s.readStream(h.ctx, "framework-restarted", args)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !reflect.DeepEqual(first, replayed) || downloads != 1 {
		t.Fatal("reopen changed the supplied page or downloaded again")
	}
	body := replayed.Content[0].(mcp.TextContent).Text
	digest, err := inputBodyDigest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.receiveGroupBodies(h.ctx, "framework-restarted", []string{digest}); err != nil {
		t.Fatal(err)
	}
	state, _ := p.repository.State(s.execution)
	if state.Used != 1 || state.MaterialImages != 1 || p.repository.Barrier(s.execution) != nil {
		t.Fatal("receipt did not settle exactly one page", state)
	}
}

func TestInputConcurrentCallAndOutputProofDoNotCreateOrphanBinding(t *testing.T) {
	h, s := unifiedReaderFixture(t, 2)
	ref := roomStreamRef(s.storage.id)
	args, _ := json.Marshal(map[string]any{"stream_ref": ref, "limit": 1})
	body, _, release, err := s.readStream(h.ctx, "command/one", args)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, _, rejectedRelease, err := s.readStream(h.ctx, "command/concurrent", args)
	if rejectedRelease != nil {
		rejectedRelease()
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent read did not fail at lease", err)
	}
	release()
	state, err := s.storage.repository.State(s.execution)
	if err != nil || len(state.Calls) != 1 {
		t.Fatal(state.Calls, err)
	}

	digest, err := inputBodyDigest([]byte(body.Content[0].(mcp.TextContent).Text))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.receiveGroupBodies(h.ctx, "command", []string{digest}); err != nil {
		t.Fatal(err)
	}
	state, err = s.storage.repository.State(s.execution)
	if err != nil || len(state.Attempts) != 1 || !state.Attempts[0].Received || h.list(t).Members[0].ReadThrough != 1 {
		t.Fatal(state.Attempts, err)
	}
}
