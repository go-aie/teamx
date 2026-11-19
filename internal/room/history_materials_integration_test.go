//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/go-aie/teamx/internal/platform/notification"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHistoryMaterialsUseExistingDownloaderAndMemberAccount(t *testing.T) {
	h, svc, _, reader, pixels := modelAttachmentFixture(t)
	// This fixture's legacy Inbox is outside the History contract. Start its
	// temporary member partition empty; retain the original native Room message.
	if err := os.RemoveAll(filepath.Join(svc.store.dir, "inboxes", reader.MembershipID)); err != nil {
		t.Fatal(err)
	}
	svc.unifiedInputs = true
	svc.inputAdapters = storageAdapters()
	h.m.bindPlatformReferences(svc)
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: reader.MemberIdentity, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", StartReadThrough: 1, SnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	p, err := svc.inputStorage(reader.MemberIdentity)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newInputSession(context.Background(), svc, p, "isolated-history", 1, true, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	// The Room message precedes the scheduling window and is already consumed.
	room, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	room.Members[0].ReadThrough = room.LatestSeq
	if err := svc.store.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var imageToken atomic.Value
	imageToken.Store("Bearer test-platform-secret")
	imageTransport := workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != "GET" || !strings.Contains(r.URL.Path, "/projects/7/uploads/") || r.Header.Get("Authorization") != imageToken.Load().(string) {
			t.Error("wrong image request")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(pixels))}, nil
	})
	h.m.platformHTTP = imageTransport
	a, token, _ := historyAccess(t, s)
	args := map[string]any{"stream_ref": roomStreamRef(s.storage.id)}
	_, missing := historyMCPCall(t, a, token, "no-binding", "history_read", args)
	if calls.Load() != 0 || !strings.Contains(string(mustJSON(t, missing)), "reason") {
		t.Fatal("borrowed collection account", calls.Load())
	}
	h.m.mu.Lock()
	file, err := h.m.platforms.Load()
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	bindCLITool(t, h.m, room.RoomIdentity, room.Members[0], managedPlatform{Platform: file.Platforms[0].Platform}, "gitlab", 0)
	before := historyTree(t, svc.store.dir)
	result, _ := historyMCPCall(t, a, token, "authorized", "history_read", args)
	if len(result.Content) != 2 || calls.Load() != 1 {
		t.Fatal("existing downloader not used", result, calls.Load())
	}
	if !reflect.DeepEqual(before, historyTree(t, svc.store.dir)) {
		t.Fatal("material read changed authority")
	}
	// A cached result rechecks current member selection before exposing pixels.
	h.m.mu.Lock()
	bindings, err := h.m.readToolBindingsLocked(room.RoomIdentity)
	if err == nil {
		bindings.Bindings = bindings.Bindings[:0]
		err = h.m.store.save(h.m.toolBindingsPath(room.RoomIdentity), bindings, 1<<20)
	}
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	data := mustJSON(t, historyReadRequest{Stream: roomStreamRef(s.storage.id)})
	if _, err := s.historyCall(context.Background(), "authorized", "history_read", data); err == nil {
		t.Fatal("cached pixels escaped binding removal")
	}
	state, err := s.inputState(context.Background())
	if err != nil || len(state.Attempts) != 0 || state.Used != 0 {
		t.Fatal(state, err)
	}
	// Shared retention grants text visibility, but the collector's account must
	// never substitute for a selected member account during image download.
	if _, err := svc.configureSharedMembers(context.Background(), 0, []MemberIdentity{s.storage.id}); err != nil {
		t.Fatal(err)
	}
	item := historyImageItem(s, "shared-native", room.Messages[0].Text)
	item.Source = *room.Messages[0].Source
	item.Key = item.Source.ItemKey
	item.Route.Target = notification.Target{Mode: "shared_inbox", InboxID: sharedProjectInbox}
	if err := svc.receiveInbox(context.Background(), item.Route.Target, item); err != nil {
		t.Fatal(err)
	}
	directory, _ := historyResult[historyDirectory](t, s, "shared-directory", "history_list", map[string]any{})
	sharedRef := ""
	for _, stream := range directory.Streams {
		if stream.Kind == "shared" {
			sharedRef = stream.Ref
		}
	}
	if sharedRef == "" {
		t.Fatal("shared stream missing")
	}
	_, sharedMissing := historyMCPCall(t, a, token, "shared-no-member", "history_read", map[string]any{"stream_ref": sharedRef})
	if calls.Load() != 1 || !strings.Contains(string(mustJSON(t, sharedMissing)), "reason") {
		t.Fatal("shared collector account was borrowed")
	}
	request := platformTestRequest(h.m)
	request.ID = strings.Repeat("b", 32)
	request.Name = "Member image reader"
	created := managerHTTP(h.m, "/v1/platforms/save", request)
	var second managedPlatform
	if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &second) != nil {
		t.Fatal(created.Code, created.Body.String())
	}
	second = authorizeTestPlatform(t, h.m, second, 202, "member-read-secret")
	bindCLITool(t, h.m, room.RoomIdentity, room.Members[0], second, "gitlab", 0)
	imageToken.Store("Bearer member-read-secret")
	sharedResult, _ := historyMCPCall(t, a, token, "shared-own-account", "history_read", map[string]any{"stream_ref": sharedRef})
	if len(sharedResult.Content) != 2 || calls.Load() != 2 {
		t.Fatal("member's selected account not used", sharedResult, calls.Load())
	}

}
