//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-aie/teamx/internal/inputstream"
)

func TestRoomAttachmentMaterialsServiceAssembly(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed=%v", managed), func(t *testing.T) {
			// Normal service initialization supplies the local resolver. Only the
			// executing member is controlled; no DSH/model or live Room is involved.
			svc, err := newService(t.TempDir(), "attachment-materials", serviceConfig{unifiedInputs: true})
			if err != nil {
				t.Fatal(err)
			}
			defer closeService(t, svc)
			id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: "reader", MembershipID: "reader-generation"}
			target, err := svc.rt.Spawn(notificationSink{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.room.Call[MemberInfo](t.Context(), AddMember{MemberIdentity: id, Runner: RunnerConfig{Kind: "dsh", Profile: "controlled"}, Target: target}); err != nil {
				t.Fatal(err)
			}
			var handler http.Handler = svc
			if managed {
				m := testManager(t, t.TempDir(), nil)
				m.mu.Lock()
				m.runtimes[svc.info.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
				m.bindPlatformReferences(svc)
				m.mu.Unlock()
				handler = m
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			pixels := attachmentTestPNG(t, true)
			imageID := attachmentTestID(1)
			status, body := attachmentUploadHTTP(t, server, svc.info, imageID, pixels, false)
			if status != 200 {
				t.Fatal(status, string(body))
			}
			status, body = attachmentJSON(t, server, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{"human", humanOperator()}}, ClientMessageID: "image-only", Attachments: []RoomAttachmentInput{{ID: imageID}}})
			if status != 200 {
				t.Fatal(status, string(body))
			}
			if err = svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 1}}); err != nil {
				t.Fatal(err)
			}
			p, err := svc.inputStorage(id)
			if err != nil {
				t.Fatal(err)
			}
			s, err := newInputSession(t.Context(), svc, p, "controlled-execution", 1, true, func(ctx context.Context) error { return ctx.Err() })
			if err != nil {
				t.Fatal(err)
			}
			access, token, _ := historyAccess(t, s)
			args := map[string]any{"stream_ref": roomStreamRef(id)}
			w := httptest.NewRecorder()
			formal := simpleMCPRequest(t, access, token, "read", "inbox_read", args, w)
			if formal == nil || formal.IsError || len(materialPixels(t, formal)) != 1 || !bytes.Equal(materialPixels(t, formal)[0], pixels) {
				t.Fatal(w.Body.String())
			}
			if err := s.receiveCall(t.Context(), "read"); err != nil {
				t.Fatal(err)
			}
			agentBefore, err := svc.store.LoadAgent(id)
			if err != nil {
				t.Fatal(err)
			}
			past, _ := historyMCPCall(t, access, token, "history", "history_read", args)
			if len(materialPixels(t, past)) != 1 || !bytes.Equal(materialPixels(t, past)[0], pixels) {
				t.Fatal("history bytes changed")
			}
			agentAfter, err := svc.store.LoadAgent(id)
			if err != nil || string(mustJSON(t, agentBefore)) != string(mustJSON(t, agentAfter)) {
				t.Fatal("history changed execution state", err)
			}
			state, err := svc.store.LoadRoom()
			if err != nil || state.Members[0].ReadThrough != 1 {
				t.Fatal(state, err)
			}
			execution, err := p.repository.State(inputstream.Execution{Generation: "controlled-execution", Turn: 1})
			if err != nil || execution.MaterialImages != 1 || execution.MaterialBytes != int64(len(pixels)) {
				t.Fatal(execution, err)
			}
		})
	}
}
