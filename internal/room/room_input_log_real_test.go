//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/attachment"
)

// Real installed DSH and model, isolated Room data and fixture image download.
// No provider writes or production Room operations are performed.
func TestRoomTimelineRealDSHImagesAndNativeConfirmation(t *testing.T) {
	if os.Getenv("TEAMX_ROOM_TIMELINE_REAL") != "1" {
		t.Skip("explicit real model acceptance opt-in")
	}
	cfg, entry, credential := inputAcceptanceModel(t)
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "source": "isolated Room with saved image fixture", "transport": "real DSH / HTTP MCP / Session result proof", "model": entry.materialize().ModelID, "external_writes": 0}
	defer imageRealReport(t, output, report)
	state := historyRoom(1, "ROOM_COMMON_ONE")
	imageItem := storedGitLab(MemberIdentity{}, "native-picture", "events", "7", "42", "ROOM_COMMON_IMAGE ![saved picture](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/picture.png)")
	imageItem.Source.ProjectID = 7
	imageItem.Source.URL = "https://gitlab.example.com/group/repo/-/merge_requests/42"
	addHistoricalNotification(&state, imageItem)
	h, s := newUnifiedReaderFixture(t, state, 3*time.Minute)
	reply := h.post(humanCaller, "native-reply", "ROOM_COMMON_REPLY")
	reply.ReplyTo = 1
	roomCall[PostReceipt](t, h, reply)
	appendInputEvent(t, h, "ROOM_COMMON_EVENT", s.storage.id.MembershipID)
	s.images = true
	data, mime := savedInputImage(t, "")
	s.service.materialAttachments = &materialAttachments{download: func(context.Context, attachmentRequest, *attachmentReader, attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		return data, mime, &attachmentAccess{}, nil
	}}
	access, _, _ := accessFixture(t)
	initial, _ := issueInboxCredential(t, access, s.service, s.storage.id, true)
	token, revoke, err := access.issueInputBinding(s)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	server := httptest.NewServer(access.wrap(http.NotFoundHandler()))
	defer server.Close()
	access.mu.Lock()
	access.internal = server.URL
	access.mu.Unlock()
	workspace := filepath.Join(output, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(output, "runtime")
	opts := dsh.Options{BinaryPath: cfg.Runtime.DSH.BinaryPath, BinaryArgs: cfg.Runtime.DSH.BinaryArgs, Artifact: cfg.Runtime.DSH.Artifact, Workspace: workspace, StateDir: stateDir, Instructions: "Read the Room stream formally using inbox_read. Read all four Room entries, including the saved picture, reply and targeted event. Report ROOM_COMMON_ONE, ROOM_COMMON_IMAGE, ROOM_COMMON_REPLY and ROOM_COMMON_EVENT, the reply_to number, and the dominant color actually seen in the picture. No shell, files, web, delegation, platform actions or sending messages. At most 8 tool calls.\n" + unifiedAgentGuide(), Model: entry.materialize(), Credential: credential, InputBridge: true, MCPServers: []dsh.MCPHTTPServer{mcpHTTPConfiguration(server.URL, initial)}, Limits: dsh.Limits{TurnTimeout: 90 * time.Second}}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	session, err := dsh.Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	binding, _ := json.Marshal(map[string]string{"token": token, "endpoint": server.URL})
	bindingPath := filepath.Join(stateDir, "input-execution.json")
	if err := os.WriteFile(bindingPath, binding, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(bindingPath)
	catalog, err := s.catalog(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	directory, _, err := catalog.Directory()
	if err != nil {
		t.Fatal(err)
	}
	s.rememberDirectory(directory)
	directoryJSON, _ := json.Marshal(directory)
	result, err := session.Run(ctx, "Input directory:\n"+string(directoryJSON))
	report["response"] = result.Text
	report["tools"] = result.Tools
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"ROOM_COMMON_ONE", "ROOM_COMMON_IMAGE", "ROOM_COMMON_REPLY", "ROOM_COMMON_EVENT"} {
		if !strings.Contains(result.Text, marker) {
			t.Fatal("missing marker", marker, result.Text)
		}
	}
	if !strings.Contains(strings.ToLower(result.Text), "red") && !strings.Contains(result.Text, "红") {
		t.Fatal("image not understood", result.Text)
	}
	progress, err := s.inputState(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	report["coordinator"] = progress
	count := 0
	for _, a := range progress.Attempts {
		if a.Kind == "room" {
			if a.Phase != "settled" || !a.Received {
				t.Fatal(a)
			}
			count += len(a.Entries)
		}
	}
	native, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 || native.Members[0].ReadThrough != 4 || native.Members[1].ReadThrough != 0 {
		t.Fatal("native consumption", count, native.Members)
	}
	report["native_proof"] = native.ReadProofs[s.storage.id.MembershipID]
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	h.reload(t, true)
	reopened, err := h.store.LoadRoom()
	if err != nil || !reflect.DeepEqual(native.ReadProofs, reopened.ReadProofs) || !reflect.DeepEqual(native.Messages, reopened.Messages) {
		t.Fatal("restart changed native facts", err)
	}
	report["room_identity"] = h.id
	if err := os.CopyFS(filepath.Join(output, "room-data"), os.DirFS(h.store.dir)); err != nil {
		t.Fatal(err)
	}
	report["room_data_dir"] = filepath.Join(output, "room-data")
	report["reopened_same_proof"] = true
	report["status"] = "passed"
}
