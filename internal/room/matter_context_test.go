package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func matterContextHarness(t *testing.T) (*inboxHarness, MatterContextChange) {
	t.Helper()
	h := newInboxHarness(t, InboxLimits{})
	for i, key := range []string{"event-a", "todo-b", "separate-request"} {
		r := h.receive(key)
		r.Item.Source.Reference.RecipientID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "13"}
		r.Item.Source.Reference.Scope = "project"
		r.Item.Source.Reference.NoteID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "101"}
		if i == 0 {
			r.Item.Source.Reference.Object.ID = platform.ExternalID{}
		}
		if i == 1 {
			r.Item.Source.Channel = "todos"
			r.Item.Source.Reference.Scope = "account"
		}
		inboxCall[InboxReceipt](t, h, r)
	}
	inboxRunning(t, h, 1, 3)
	page := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	linkInboxPage(t, h, page)
	change := MatterContextChange{ID: "batch-1", TurnNumber: 1}
	for _, e := range page.Entries {
		obj := e.Item.Source.Reference.Object
		obj.ID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9001"}
		report := gitlab.DiscussionResolution{Query: gitlab.DiscussionQuery{Source: e.Item.Source}, CheckedAt: time.Now().UTC(), Status: "resolved", Object: obj, AccountID: e.Item.Source.Reference.RecipientID, Username: "bot", ObjectURL: "https://gitlab.example.com/g/p/-/merge_requests/42", ObjectVerified: true, Complete: true, NoteID: e.Item.Source.Reference.NoteID, DiscussionID: platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: "thread"}}
		report.Mappings = []gitlab.NoteDiscussion{{NoteID: report.NoteID, DiscussionID: report.DiscussionID}}
		change.Resolutions = append(change.Resolutions, MatterResolution{Seq: e.Seq, Report: report})
	}
	return h, change
}
func contextMerge() MatterRelationChange {
	return MatterRelationChange{ID: "merge-1", Revision: 1, TurnNumber: 1, Kind: "merge", From: MatterRef{2, "main"}, FromVersion: 1, To: MatterRef{1, "main"}, ToVersion: 1, Basis: "same verified discussion and request; independent Todo receipt retained", Evidence: "GET object/notes and original input show one question"}
}
func TestMatterContextAliasesMergeSplitAndRecovery(t *testing.T) {
	h, change := matterContextHarness(t)
	c, err := h.store.enrichMatterContext(h.id, change)
	if err != nil || c.Revision != 1 {
		t.Fatal(c, err)
	}
	duplicate, err := h.store.enrichMatterContext(h.id, change)
	if err != nil || !reflect.DeepEqual(c, duplicate) {
		t.Fatal("non-idempotent enrich", err)
	}
	page, err := h.store.listMatters(h.id, "", 0, 25)
	if err != nil {
		t.Fatal(err)
	}
	canonical := page.Records[0].Context.CanonicalObjectKey
	grouped, err := h.store.listMatters(h.id, canonical, 0, 25)
	if err != nil || len(grouped.Records) != 3 || grouped.Records[0].ObjectKey == canonical {
		t.Fatal("alias did not group IID-only source", grouped, err)
	}
	before := page.Records
	op := contextMerge()
	c, err = h.store.relateMatters(h.id, op)
	if err != nil || c.primary(op.From) != op.To {
		t.Fatal(c, err)
	}
	if _, err = h.store.relateMatters(h.id, op); err != nil {
		t.Fatal("exact retry failed", err)
	}
	op.Basis = "changed"
	if _, err = h.store.relateMatters(h.id, op); !errors.Is(err, ErrConflict) {
		t.Fatal("ID reuse", err)
	}
	// Merged placeholders cannot execute or advance independently; a third same-object request remains separate.
	if _, err = h.store.updateMatter(h.id, MatterUpdate{Seq: 2, ID: "main", Version: 1, TurnNumber: 1, Status: "completed", Summary: "guessed", Evidence: "alias"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = h.store.updateMatter(h.id, MatterUpdate{Seq: 3, ID: "main", Version: 1, TurnNumber: 1, Status: "pending", Summary: "independent request"}); err != nil {
		t.Fatal(err)
	}
	h.reload(t, true)
	c, err = h.store.readMatterContext(h.id)
	if err != nil || c.Revision != 2 || c.primary(MatterRef{2, "main"}) != (MatterRef{1, "main"}) {
		t.Fatal(c, err)
	}
	split := contextMerge()
	split.ID = "correct-1"
	split.Kind = "split"
	split.Revision = 2
	split.RelationID = "merge-1"
	split.Basis = "different completion condition after reviewing full discussion"
	c, err = h.store.relateMatters(h.id, split)
	if err != nil || c.primary(split.From) != split.From {
		t.Fatal(c, err)
	}
	page, err = h.store.listMatters(h.id, "", 0, 25)
	if err != nil || len(page.Records[0].Context.Relations) != 0 {
		t.Fatal(page, err)
	}
	for i := range 2 {
		if page.Records[i].DeliveryID != before[i].DeliveryID || !reflect.DeepEqual(page.Records[i].Matters, before[i].Matters) {
			t.Fatal("association changed authoritative original progress")
		}
	}
	if _, err = h.store.updateMatter(h.id, MatterUpdate{Seq: 2, ID: "new-concern", TurnNumber: 1, Status: "pending", Summary: "separate concern from same input"}); err != nil {
		t.Fatal(err)
	}
	// Source reception is unchanged; identical inputs still have three records, not three new matters.
	p := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	linkInboxPage(t, h, p)
	page, err = h.store.listMatters(h.id, "", 0, 25)
	if err != nil || len(page.Records) != 3 || len(page.Records[1].Matters) != 2 {
		t.Fatal(page, err)
	}
}
func TestMatterContextValidationVersionsIsolationAndDegradation(t *testing.T) {
	for _, mode := range []string{"source", "namespace", "alias", "stale-turn", "revision", "recipient", "future", "incomplete", "duplicate-seq"} {
		t.Run(mode, func(t *testing.T) {
			h, c := matterContextHarness(t)
			switch mode {
			case "source":
				c.Resolutions[0].Report.Query.Source.ItemKey = "forged"
			case "namespace":
				c.Resolutions[0].Report.Object.ID.Namespace = "gitlab:graphql:gid"
			case "alias":
				c.Resolutions[1].Report.Object.ID.Value = "42"
			case "stale-turn":
				c.TurnNumber = 2
			case "revision":
				c.Revision = 1
			case "recipient":
				c.Resolutions[0].Report.AccountID.Value = "99"
			case "future":
				c.Resolutions[0].Report.CheckedAt = time.Now().Add(time.Hour)
			case "incomplete":
				c.Resolutions[0].Report.Complete = false
			case "duplicate-seq":
				c.Resolutions = append(c.Resolutions, c.Resolutions[0])
			}
			if _, err := h.store.enrichMatterContext(h.id, c); err == nil {
				t.Fatal("accepted invalid context", mode)
			}
			current, err := h.store.readMatterContext(h.id)
			if err != nil || current.Revision != 0 {
				t.Fatal("partial batch committed", current, err)
			}
		})
	}
	h, c := matterContextHarness(t)
	if _, err := h.store.enrichMatterContext(h.id, c); err != nil {
		t.Fatal(err)
	}
	other := h.id
	other.MembershipID = "generation-B"
	other.AgentID = "B"
	view, err := h.store.readMatterContext(other)
	if err != nil || view.Revision != 0 {
		t.Fatal("cross-member context", view, err)
	}
	// Failed/partial rechecks retain reason and older verified identity, even after reopen.
	c.ID = "recheck"
	c.Revision = 1
	c.Resolutions = c.Resolutions[:1]
	r := &c.Resolutions[0].Report
	r.Status = "unavailable"
	r.Reason = "HTTP 403"
	r.CheckedAt = time.Now().UTC()
	r.ObjectVerified = false
	r.Complete = false
	r.Mappings = nil
	r.NoteID = platform.ExternalID{}
	r.DiscussionID = platform.ExternalID{}
	// One minute minimum is enforced independently of turn/session.
	if _, err := h.store.enrichMatterContext(h.id, c); !errors.Is(err, ErrConflict) {
		t.Fatal("rapid recheck", err)
	}
}
func TestMatterContextRelationsRejectConflictsAndUncertainCommit(t *testing.T) {
	h, c := matterContextHarness(t)
	if _, err := h.store.enrichMatterContext(h.id, c); err != nil {
		t.Fatal(err)
	}
	op := contextMerge()
	op.FromVersion = 2
	if _, err := h.store.relateMatters(h.id, op); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	op.FromVersion = 1
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			u := op
			if n == 1 {
				u.ID = "merge-race"
			}
			_, e := h.store.relateMatters(h.id, u)
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for err := range errs {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	h2, c2 := matterContextHarness(t)
	h2.store.ops.syncDir = func(string) error { return errors.New("injected fsync") }
	if _, err := h2.store.enrichMatterContext(h2.id, c2); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if _, err := h2.store.enrichMatterContext(h2.id, c2); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("uncertain store was not frozen", err)
	}
	h2.reload(t, true)
	saved, err := h2.store.readMatterContext(h2.id)
	if err != nil || saved.Revision != 1 {
		t.Fatal(saved, err)
	}
}
func TestMatterContextHTTPCLIIdentityAndStrictJSON(t *testing.T) {
	h, c := matterContextHarness(t)
	svc := &service{info: ServiceInfo{RoomIdentity: h.id.RoomIdentity, ServiceEpoch: "test-service"}, store: h.store, room: h.roomHarness.addr, requests: make(chan struct{}, 32)}
	server := httptest.NewServer(http.HandlerFunc(svc.ServeHTTP))
	defer server.Close()
	env := map[string]string{"TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": h.id.RoomID, "TEAMX_ROOM_INSTANCE": h.id.RoomInstance, "TEAMX_SERVICE_EPOCH": svc.info.ServiceEpoch, "TEAMX_AGENT_ID": h.id.AgentID, "TEAMX_MEMBERSHIP_ID": h.id.MembershipID}
	var out, stderr bytes.Buffer
	b, _ := json.Marshal(c)
	if err := runCLI(context.Background(), []string{"matters", "enrich", "--change", string(b)}, &out, &stderr, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err, stderr.String())
	}
	var got MatterContext
	if json.Unmarshal(out.Bytes(), &got) != nil || got.Revision != 1 {
		t.Fatal(out.String())
	}
	for _, caller := range []Caller{humanCaller, agentCaller("B", "generation-B")} {
		req := apiRequest{ServiceInfo: svc.info, Caller: caller, AgentID: h.id.AgentID, MembershipID: h.id.MembershipID, MatterContext: &c}
		body, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		svc.ServeHTTP(w, httptest.NewRequest("POST", "/v1/matters/enrich", bytes.NewReader(body)))
		if w.Code != 403 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body)
		}
	}
	if err := runCLI(context.Background(), []string{"matters", "relate", "--change", `{"id":"a","unexpected":true}`}, &out, &stderr, func(k string) string { return env[k] }); err == nil {
		t.Fatal("accepted unknown JSON field")
	}
	if !strings.Contains(dshNotification(UnreadChanged{}), "matters context") {
		t.Fatal("Room turns missing enrichment protocol")
	}
}

func TestMatterContextFailedRecheckKeepsIdentityAndHistory(t *testing.T) {
	h, c := matterContextHarness(t)
	for i := range c.Resolutions {
		c.Resolutions[i].Report.CheckedAt = time.Now().Add(-2 * time.Minute).UTC()
	}
	if _, err := h.store.enrichMatterContext(h.id, c); err != nil {
		t.Fatal(err)
	}
	c.ID, c.Revision = "failed-check", 1
	c.Resolutions = c.Resolutions[:1]
	r := &c.Resolutions[0].Report
	r.CheckedAt = time.Now().UTC()
	r.Status = "unavailable"
	r.Reason = "HTTP 403: current access denied"
	r.ObjectVerified, r.Complete = false, false
	r.Mappings = nil
	r.NoteID, r.DiscussionID = platform.ExternalID{}, platform.ExternalID{}
	if _, err := h.store.enrichMatterContext(h.id, c); err != nil {
		t.Fatal(err)
	}
	h.reload(t, true)
	page, err := h.store.listMatters(h.id, "", 0, 25)
	if err != nil {
		t.Fatal(err)
	}
	v := page.Records[0].Context
	if v.Resolution.Status != "unavailable" || v.CachedIdentity == nil || v.CanonicalObjectKey == "" || v.CachedIdentity.DiscussionID.Value != "thread" {
		t.Fatal("failed query erased identity", v)
	}
	if _, err := h.store.relateMatters(h.id, MatterRelationChange{ID: "keep-alias", Revision: 2, TurnNumber: 1, Kind: "merge", From: MatterRef{2, "main"}, FromVersion: 1, To: MatterRef{1, "main"}, ToVersion: 1, Basis: "same original request, historical identity only", Evidence: "old mapping; current access remains denied; no remote write"}); err != nil {
		t.Fatal(err)
	}
}

func TestMatterContextCorruptOwnerAndCapacityAreRejected(t *testing.T) {
	h, c := matterContextHarness(t)
	path := filepath.Join(h.store.dir, "agents", h.id.MembershipID, "matter-context.json")
	if err := os.WriteFile(path, []byte(`{"revision":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.readMatterContext(h.id); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing ownership accepted", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// Size overflow does not install a partial batch or advance the revision.
	c.Resolutions = append(c.Resolutions[:0], c.Resolutions[0])
	c.Resolutions[0].Report.Reason = strings.Repeat("x", 4097)
	if _, err := h.store.enrichMatterContext(h.id, c); !errors.Is(err, ErrRequest) {
		t.Fatal(err)
	}
	c.Resolutions[0].Report.Reason = ""
	for n := range 499 {
		c.Resolutions[0].Report.Mappings = append(c.Resolutions[0].Report.Mappings, gitlab.NoteDiscussion{NoteID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: strconv.Itoa(1000 + n)}, DiscussionID: platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: strings.Repeat("x", 3000)}})
	}
	if _, err := h.store.enrichMatterContext(h.id, c); err == nil {
		t.Fatal("over-limit context snapshot committed")
	}
	if saved, err := h.store.readMatterContext(h.id); err != nil || saved.Revision != 0 {
		t.Fatal("oversized commit installed partial state", saved.Revision, err)
	}
}

func TestMatterContextMergeCannotTransferDecisionsOrUnknownActions(t *testing.T) {
	for _, mode := range []string{"unknown", "decision", "waiting", "scope", "object"} {
		t.Run(mode, func(t *testing.T) {
			h, c := matterContextHarness(t)
			if mode == "scope" {
				c.Resolutions[1].Report.Query.Source.Reference.Scope = "another-authorization-scope"
				if _, err := h.store.enrichMatterContext(h.id, c); err == nil {
					t.Fatal("rewritten source scope accepted")
				}
				return
			}
			if _, err := h.store.enrichMatterContext(h.id, c); err != nil {
				t.Fatal(err)
			}
			h.store.mu.Lock()
			e, err := h.store.inboxEntryLocked(h.id, 2)
			if err != nil {
				h.store.mu.Unlock()
				t.Fatal(err)
			}
			r, err := h.store.loadMatterLocked(e)
			if err != nil {
				h.store.mu.Unlock()
				t.Fatal(err)
			}
			switch mode {
			case "unknown":
				r.Matters[0].Status = "unknown"
				r.Matters[0].Next = "query current outcome"
			case "waiting":
				r.Matters[0].Status = "waiting_followup"
				r.Matters[0].Next = "await independent confirmation"
			case "object":
				h.store.mu.Unlock()
				op := contextMerge()
				op.To = MatterRef{2, "main"}
				if _, err = h.store.relateMatters(h.id, op); err == nil {
					t.Fatal("self relation accepted")
				}
				return
			case "decision":
				h.store.mu.Unlock()
				change := policyRule()
				change.Kind = "decision"
				change.Seq = 2
				change.MatterID = "main"
				change.MatterVersion = 1
				if _, err := h.store.changeDecision(h.id, humanCaller, change); err != nil {
					t.Fatal(err)
				}
				op := contextMerge()
				op.FromVersion = 2
				if _, err := h.store.relateMatters(h.id, op); !errors.Is(err, ErrConflict) {
					t.Fatal("decision transferred", err)
				}
				return
			}
			err = h.store.save(h.store.matterPath(e), r, matterRecordBytes)
			h.store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.relateMatters(h.id, contextMerge()); !errors.Is(err, ErrConflict) {
				t.Fatal("unsafe merge accepted", err)
			}
		})
	}
}

func TestMatterContextNewSourceURLKeepsOldDeliveryReceipts(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	old := h.receive("before-url")
	first := inboxCall[InboxReceipt](t, h, old)
	h.reload(t, true)
	current := old
	current.Item.Source.URL = "https://gitlab.example.com/g/p/-/merge_requests/42#note_101"
	if got := inboxCall[InboxReceipt](t, h, current); got != first {
		t.Fatal("metadata upgrade changed receipt")
	}
	p := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	if p.Entries[0].Item.Source.URL != "" {
		t.Fatal("old original evidence rewritten")
	}
	current.Item.Source.Reference.Object.ID.Value = "changed-object"
	inboxError[InboxReceipt](t, h, current, ErrConflict)
	fresh := h.receive("after-url")
	fresh.Item.Source.URL = "https://gitlab.example.com/g/p/-/merge_requests/42#note_101"
	inboxCall[InboxReceipt](t, h, fresh)
	fresh.Item.Source.URL += "-changed"
	inboxError[InboxReceipt](t, h, fresh, ErrConflict)
	room := newRoomHarness(t, emptyRoom(), nil)
	request := postIntegrationMessage{RoomIdentity: room.id, DeliveryID: "before-url", Source: old.Item.Source, Text: "original"}
	receipt := roomCall[PostReceipt](t, room, request)
	request.Source.URL = "https://gitlab.example.com/g/p/-/merge_requests/42"
	if got := roomCall[PostReceipt](t, room, request); got != receipt {
		t.Fatal("Room receipt changed")
	}
	request.Source.ItemKey = "changed"
	roomError[PostReceipt](t, room, request, ErrConflict)
}
