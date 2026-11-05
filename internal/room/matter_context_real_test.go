package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Reuses the actual GET-only tool reports as explicitly selected local inputs.
// This validates durable member linkage, not production notification coverage.
func TestMatterContextRealEvidence(t *testing.T) {
	file := os.Getenv("TEAMX_AN_CONTEXT_EVIDENCE")
	if file == "" {
		t.Skip("set TEAMX_AN_CONTEXT_EVIDENCE to a fresh real resolver report and TEAMX_AN_CONTEXT_STORE to a new absolute directory")
	}
	dir := os.Getenv("TEAMX_AN_CONTEXT_STORE")
	if !filepath.IsAbs(dir) {
		t.Fatal("new absolute store required")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Status  string                        `json:"status"`
		Reports []gitlab.DiscussionResolution `json:"reports"`
	}
	if err := jsonFile(file, &report); err != nil || report.Status != "passed" || len(report.Reports) < 2 {
		t.Fatal("successful real tool report required", err)
	}
	store, err := OpenStore(dir, "design-review")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if store != nil {
			_ = store.Close()
		}
	}()
	state := historyRoom(0, "")
	if err = store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rt, err := actorx.NewRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(ctx)
	room, err := NewRoom(store)
	if err != nil {
		t.Fatal(err)
	}
	roomAddr, err := rt.Spawn(room)
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := SpawnAgentInbox(rt, store, id, InboxLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range report.Reports {
		item := notification.Item{Key: r.Query.Source.ItemKey, DeliveryID: r.Query.Source.ItemKey, CreatedAt: r.CheckedAt, Order: r.Query.Source.ItemKey, Source: r.Query.Source, Text: "Existing real comment selected for GET-only identity acceptance; no remote write or live subscription claim"}
		if _, err := inbox.Call[InboxReceipt](ctx, ReceiveInbox{MemberIdentity: id, Item: item}); err != nil {
			t.Fatal(i, err)
		}
	}
	if err = store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: uint64(len(report.Reports)), Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: uint64(len(report.Reports))}}); err != nil {
		t.Fatal(err)
	}
	page, err := inbox.Call[InboxBatchPage](ctx, ReadInboxBatch{MemberIdentity: id, TurnNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.associateInboxPage(page.InboxPage, 1); err != nil {
		t.Fatal(err)
	}
	svc := &service{info: ServiceInfo{RoomIdentity: id.RoomIdentity, ServiceEpoch: "context-real"}, store: store, room: roomAddr, requests: make(chan struct{}, 32)}
	server := httptest.NewServer(http.HandlerFunc(svc.ServeHTTP))
	defer server.Close()
	env := map[string]string{"TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": id.RoomID, "TEAMX_ROOM_INSTANCE": id.RoomInstance, "TEAMX_SERVICE_EPOCH": svc.info.ServiceEpoch, "TEAMX_AGENT_ID": id.AgentID, "TEAMX_MEMBERSHIP_ID": id.MembershipID}
	change := MatterContextChange{ID: "real-batch", TurnNumber: 1}
	for i, r := range report.Reports {
		change.Resolutions = append(change.Resolutions, MatterResolution{Seq: uint64(i + 1), Report: r})
	}
	invoke := func(op string, change any) {
		t.Helper()
		b, _ := json.Marshal(change)
		var stdout, stderr bytes.Buffer
		if err := runCLI(ctx, []string{"matters", op, "--change", string(b)}, &stdout, &stderr, func(k string) string { return env[k] }); err != nil {
			t.Fatal(op, err, stderr.String())
		}
	}
	invoke("enrich", change)
	op := contextMerge()
	invoke("relate", op)
	got, err := store.listMatters(id, "", 0, 25)
	if err != nil || len(got.Records) != len(report.Reports) || got.Records[0].Context.CanonicalObjectKey != got.Records[1].Context.CanonicalObjectKey {
		t.Fatal("real alias grouping failed", err)
	}
	op.ID, op.Kind, op.Revision, op.RelationID = "real-correction", "split", 2, "merge-1"
	invoke("relate", op)
	if _, err = inbox.Call[InboxStatus](ctx, ConfirmInboxBatch{MemberIdentity: id, TurnNumber: 1, Receipt: page.Batch.Receipt}); err != nil {
		t.Fatal(err)
	}
	saved, err := store.readMatterContext(id)
	if err != nil || saved.Revision != 3 {
		t.Fatal(err)
	}
	if err = rt.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	reopened, err := OpenStore(dir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, err := reopened.readMatterContext(id)
	if err != nil || !reflect.DeepEqual(saved, recovered) {
		t.Fatal("real identities/relations lost on reopen", err)
	}
	rs, err := reopened.LoadRoom()
	if err != nil || len(rs.Messages) != 0 {
		t.Fatal("unexpected public message", err)
	}
	b, _ := json.MarshalIndent(map[string]any{"status": "passed", "context": recovered, "source_inputs": "explicit local fixtures backed by actual GET-only tool reports", "public_messages": len(rs.Messages), "remote_writes": 0, "dsh": false}, "", "  ")
	if err = os.WriteFile(filepath.Join(filepath.Dir(file), "store-report.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual tool reports persisted via member HTTP/CLI; alias grouping, merge, correction and Store reopen passed (%d independent receipts)", len(report.Reports))
}
