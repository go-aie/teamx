package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
)

func TestInboxPreviewStatusIdentityAndReadOnly(t *testing.T) {
	svc, err := startService(t.TempDir(), "inbox-status", serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer svc.close(context.Background())
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	item := inboxFixtureItem(id, 1)
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), item); err != nil {
		t.Fatal(err)
	}
	read := func(req apiRequest) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		svc.ServeHTTP(w, httptest.NewRequest("POST", "/v1/member/inbox/status", strings.NewReader(string(body))))
		return w
	}
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID}
	for i := 0; i < 2; i++ {
		w := read(req)
		var status InboxStatus
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &status) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		if status.MemberIdentity != id || status.LatestSeq != 1 || status.RetainedEntries != 1 || status.ConsumedThrough != 0 || status.Batch != nil || status.AutomaticChecked {
			t.Fatal(status)
		}
	}
	// A legal reclaimed prefix changes the retained count without resetting seq.
	svc.inboxMu.Lock()
	addr, err := svc.inboxLocked(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = addr.Call[InboxStatus](context.Background(), ReclaimInbox{id, 1}); err != nil {
		t.Fatal(err)
	}
	svc.inboxMu.Unlock()
	w := read(req)
	var status InboxStatus
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || status.LatestSeq != 1 || status.ExpiredThrough != 1 || status.RetainedEntries != 0 || status.ConsumedThrough != 0 {
		t.Fatal(w.Body.String())
	}
	for _, kind := range []string{"agent", "human"} {
		bad := req
		if kind == "agent" {
			bad.Caller = Caller{Kind: "agent", ID: id.AgentID, MembershipID: id.MembershipID}
		} else {
			bad.MembershipID = strings.Repeat("2", 32)
		}
		if w := read(bad); w.Code == 200 {
			t.Fatal("accepted foreign identity", w.Body.String())
		}
	}
	bad := req
	bad.ServiceEpoch = "stale"
	if w := read(bad); w.Code == 200 {
		t.Fatal("accepted stale epoch")
	}
}

func TestUnifiedInboxHistoryReadsRetainedPartitionsWithoutConsuming(t *testing.T) {
	svc, err := startService(t.TempDir(), "inbox-history", serviceConfig{unifiedInputs: true, inputAdapters: storageAdapters()}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer svc.close(context.Background())
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	p, err := svc.inputStorage(id)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 26; i++ {
		item := storedGitLab(id, fmt.Sprintf("item-%d", i), "events", "7", "42", fmt.Sprintf("gitlab evidence %d", i))
		if _, err := p.Receive(item, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	lark := storedLark(id, "one-lark", "oc_chat", "团队群", "omt_thread", "lark evidence")
	if _, err := p.Receive(lark, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	post := func(path string, req apiRequest, dst any, code int) {
		t.Helper()
		body, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		svc.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(string(body))))
		if w.Code != code || (dst != nil && json.Unmarshal(w.Body.Bytes(), dst) != nil) {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID}
	var status InboxStatus
	post("/v1/member/inbox/status", req, &status, 200)
	if status.RetainedEntries != 27 || status.History == nil || status.History.Pending != 27 || status.LatestSeq != 0 {
		t.Fatal(status)
	}
	var first, older, back inboxPreviewPage
	post("/v1/member/inbox", req, &first, 200)
	if len(first.Entries) != 25 || first.Entries[24].Item.Text != lark.Text || first.Entries[24].Partition == "" ||
		first.History == nil || first.History.Older == "" || first.History.Newer != "" || first.History.Latest != status.History.Latest {
		t.Fatal(first.Entries, first.History)
	}
	if err := svc.receiveInbox(svc.ctx, inboxTarget(id), storedGitLab(id, "new", "events", "7", "42", "arrived later")); err != nil {
		t.Fatal(err)
	}
	req.InboxHistoryCursor = first.History.Older
	post("/v1/member/inbox", req, &older, 200)
	if len(older.Entries) != 2 || older.Entries[0].Seq != 1 || older.Entries[0].Partition == first.Entries[24].Partition ||
		older.History.Latest != first.History.Latest || older.Status.History.Latest == older.History.Latest ||
		older.History.Newer == "" || older.History.Older != "" {
		t.Fatal(older.Entries, older.History, older.Status)
	}
	req.InboxHistoryCursor = older.History.Newer
	post("/v1/member/inbox", req, &back, 200)
	if !reflect.DeepEqual(first.Entries, back.Entries) {
		t.Fatal("back navigation changed fixed snapshot")
	}

	// Two providers have an entry at Seq=1. Their projected locations and
	// source references must remain distinct.
	git, larkEntry := older.Entries[0], first.Entries[24]
	if git.Seq != 1 || larkEntry.Seq != 1 || git.Source.SourceRef == larkEntry.Source.SourceRef {
		t.Fatal(git, larkEntry)
	}
	manager := &roomManager{runtimes: map[RoomIdentity]*roomRuntime{id.RoomIdentity: {service: svc}}}
	for _, entry := range []inboxEntryView{git, larkEntry} {
		lookup := attachmentRequest{ServiceInfo: svc.info, AgentID: id.AgentID, MembershipID: id.MembershipID, Partition: entry.Partition, Seq: entry.Seq}
		source, body, err := manager.attachmentSourceLocked(lookup)
		if err != nil || source.Provider != entry.Item.Provider || body != entry.Item.Text {
			t.Fatal(source.Provider, body, err)
		}
		lookup.AgentID = "different-agent"
		if _, _, err := manager.attachmentSourceLocked(lookup); !errors.Is(err, ErrIdentity) {
			t.Fatal("attachment lookup accepted another Agent", err)
		}
	}
	if state, exists, err := p.repository.CurrentState(); err != nil || exists {
		t.Fatal("preview created a formal execution", state, err)
	}
	if _, err := os.Stat(filepath.Join(svc.store.followupRoot(id), "input-sources")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview created a source association", err)
	}
	// Once a prefix has been published as reclaimed, the old cursor must
	// expire. The latest page and status can immediately be reopened.
	p.store.mu.Lock()
	release := inputstream.ReleaseSet{Revision: "fixture-facts", Records: map[uint64]inputstream.ReleaseFacts{}}
	for seq := uint64(1); seq <= 27; seq++ {
		release.Records[seq] = inputstream.ReleaseFacts{Revision: "fixture", SourceConfirmed: true, BusinessReleased: true, Consumed: seq == 1}
	}
	plan, err := p.repository.PrepareReclaim("gitlab", release, now)
	if err == nil {
		_, err = p.repository.CommitReclaim("gitlab", plan, release)
	}
	p.store.mu.Unlock()
	if err != nil || plan.Through != 1 {
		t.Fatal("reclaim setup", plan, err)
	}
	post("/v1/member/inbox", req, nil, 409)
	req.InboxHistoryCursor = ""
	post("/v1/member/inbox/status", req, &status, 200)
	if !status.History.Reclaimed || status.RetainedEntries != 27 {
		t.Fatal(status)
	}
	bad := req
	bad.MembershipID = "different-member"
	post("/v1/member/inbox", bad, nil, 403)
	bad = req
	bad.Caller = agentCallerIdentity(id)
	post("/v1/member/inbox", bad, nil, 403)
}
