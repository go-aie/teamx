package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

func selectionFixture(t *testing.T) (*roomManager, *service, platformSelectionRequest) {
	t.Helper()
	m, svc, _, connect := connectFixture(t)
	id, _ := newOpaqueID()
	req := platformSelectionRequest{ServiceInfo: svc.info, Caller: connect.Caller, OperationID: id, SourceSeq: connect.SourceSeq, Input: &platformSelectionInput{Kind: "account"}, Draft: &platformSelectionDraft{Name: "工作飞书"}}
	return m, svc, req
}
func selectionCall(t *testing.T, m *roomManager, action string, req platformSelectionRequest, human bool) platformSelectionView {
	t.Helper()
	prefix := "/v1/platforms/selection/"
	if human {
		prefix = "/v1/platforms/selection-interactions/"
	}
	w := managerHTTP(m, prefix+action, req)
	var v platformSelectionView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(action, w.Code, w.Body.String())
	}
	return v
}
func selectionQuery(req platformSelectionRequest, human bool) platformSelectionRequest {
	q := platformSelectionRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, OperationID: req.OperationID}
	if human {
		q.Caller = humanCaller
	}
	return q
}
func TestPlatformSelectionDraftConflictAndImmutableReceipt(t *testing.T) {
	m, _, req := selectionFixture(t)
	first := selectionCall(t, m, "start", req, false)
	replay := selectionCall(t, m, "start", req, false)
	if !reflect.DeepEqual(replay, first) {
		t.Fatal("start changed receipt")
	}
	human := selectionQuery(req, true)
	human.Version = first.Version
	human.Draft = &platformSelectionDraft{Name: "手动名称"}
	manual := selectionCall(t, m, "update", human, true)
	natural := selectionQuery(req, false)
	natural.Version = first.Version
	natural.Draft = &platformSelectionDraft{Name: "自然语言名称"}
	if w := managerHTTP(m, "/v1/platforms/selection/update", natural); w.Code != 409 {
		t.Fatal("stale natural language edit", w.Code)
	}
	natural.Version = manual.Version
	updated := selectionCall(t, m, "update", natural, false)
	stale := selectionQuery(req, true)
	stale.Version = manual.Version
	if w := managerHTTP(m, "/v1/platforms/selection-interactions/submit", stale); w.Code != 409 {
		t.Fatal("stale tab submitted", w.Code)
	}
	human = selectionQuery(req, true)
	human.Version = updated.Version
	receipt := selectionCall(t, m, "submit", human, true)
	duplicate := selectionCall(t, m, "submit", human, true)
	if receipt.Status != "completed" || receipt.Draft.Name != "自然语言名称" || receipt.ResultVersion != 1 || !reflect.DeepEqual(receipt, duplicate) {
		t.Fatal(receipt, duplicate)
	}
	human.Draft = &platformSelectionDraft{Name: "更换目标"}
	human.Version = receipt.Version
	if w := managerHTTP(m, "/v1/platforms/selection-interactions/update", human); w.Code != 409 {
		t.Fatal("changed submitted target", w.Code)
	}
	// Pure status preserves the result and creates no connection or member config.
	before, _ := os.ReadFile(m.selectionPath(req.OperationID))
	selectionCall(t, m, "status", selectionQuery(req, false), false)
	after, _ := os.ReadFile(m.selectionPath(req.OperationID))
	if string(before) != string(after) {
		t.Fatal("status wrote")
	}
	file, err := m.platforms.Load()
	if err != nil || len(file.Platforms) != 0 {
		t.Fatal("selection connected account", err)
	}
	req.Draft.Name = "changed input"
	if w := managerHTTP(m, "/v1/platforms/selection/start", req); w.Code != 409 {
		t.Fatal("changed start input", w.Code)
	}
}
func TestPlatformSelectionIdentityExpiryCancellationAndRecovery(t *testing.T) {
	m, svc, req := selectionFixture(t)
	v := selectionCall(t, m, "start", req, false)
	for _, mutate := range []func(*platformSelectionRequest){func(r *platformSelectionRequest) { r.Caller.ID = "other" }, func(r *platformSelectionRequest) { r.ServiceEpoch = "old" }, func(r *platformSelectionRequest) { r.RoomInstance = "wrong" }, func(r *platformSelectionRequest) { r.Caller = req.Caller }} {
		q := selectionQuery(req, true)
		q.Version = v.Version
		mutate(&q)
		if w := managerHTTP(m, "/v1/platforms/selection-interactions/submit", q); w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := managerHTTP(m, "/v1/platforms/selection/submit", selectionQuery(req, false)); w.Code != 400 {
		t.Fatal("agent submitted human choice", w.Code)
	}
	q := selectionQuery(req, true)
	q.Version = v.Version
	cancelled := selectionCall(t, m, "cancel", q, true)
	if cancelled.Status != "cancelled" {
		t.Fatal(cancelled)
	}
	if duplicate := selectionCall(t, m, "cancel", q, true); !reflect.DeepEqual(cancelled, duplicate) {
		t.Fatal("cancel changed receipt")
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, m.store.dir, nil)
	live := eventLiveService(t, &eventHarness{m: reopened, room: roomSummary{RoomIdentity: svc.info.RoomIdentity}})
	q = selectionQuery(req, false)
	q.ServiceInfo = live.info
	got := selectionCall(t, reopened, "status", q, false)
	if got.Status != "cancelled" || got.Version != cancelled.Version {
		t.Fatal(got)
	}
}
func TestPlatformSelectionExpirationAndNotificationDeduplication(t *testing.T) {
	m, svc, req := selectionFixture(t)
	mode := "inbox"
	req.Notify = &mode
	selectionCall(t, m, "start", req, false)
	m.mu.Lock()
	s, err := m.readPlatformSelectionLocked(req.OperationID)
	if err == nil {
		s.CreatedAt = time.Now().Add(-48 * time.Hour)
		s.ExpiresAt = time.Now().Add(-time.Hour)
		err = m.savePlatformSelectionLocked(s)
	}
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	q := selectionQuery(req, true)
	q.Version = 1
	if w := managerHTTP(m, "/v1/platforms/selection-interactions/submit", q); w.Code != 409 {
		t.Fatal("expired form submitted", w.Code)
	}
	m.deliverConnectNotifications(t.Context())
	v := selectionCall(t, m, "status", selectionQuery(req, false), false)
	if v.Status != "expired" || v.Continuation.InboxSeq == 0 {
		t.Fatal(v)
	}
	m.deliverConnectNotifications(t.Context())
	again := selectionCall(t, m, "status", selectionQuery(req, false), false)
	if again.Continuation.InboxSeq != v.Continuation.InboxSeq {
		t.Fatal("redelivered")
	}
	s = again.platformSelection
	n, err := m.readSelectionNotificationLocked(s)
	if err != nil || n.Event == nil || n.Event.EventType != "operation_failed" {
		t.Fatal(n, err)
	}
	// Result acknowledgment and consumption stay independent.
	q = selectionQuery(req, false)
	q.ResultVersion = 1
	handled := selectionCall(t, m, "acknowledge", q, false)
	if handled.Continuation.State != "handled" {
		t.Fatal(handled)
	}
	_ = svc
}
func TestPlatformSelectionSourceLookupAndMalformedRequests(t *testing.T) {
	m, _, req := selectionFixture(t)
	selectionCall(t, m, "start", req, false)
	q := selectionQuery(req, false)
	q.OperationID = ""
	q.SourceSeq = req.SourceSeq
	w := managerHTTP(m, "/v1/platforms/selection/status", q)
	var got struct {
		Interactions []platformSelectionView `json:"interactions"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Interactions) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	q.SourceSeq++
	if w := managerHTTP(m, "/v1/platforms/selection/status", q); w.Code != 404 {
		t.Fatal(w.Code)
	}
	for _, body := range []any{map[string]any{"unknown": true}, platformSelectionRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, OperationID: req.OperationID, Notify: new("inbox")}} {
		w := managerHTTP(m, "/v1/platforms/selection/status", body)
		if w.Code == 200 {
			t.Fatal("ignored invalid fields")
		}
	}
	bad := req
	bad.OperationID, _ = newOpaqueID()
	bad.SourceSeq = 999
	if w := managerHTTP(m, "/v1/platforms/selection/start", bad); w.Code != 403 {
		t.Fatal("invented human source", w.Code)
	}
}

func TestPlatformSelectionVerifiedAccountVersionAndMemberReplacement(t *testing.T) {
	m, svc, member, configured := memberToolFixture(t)
	id, _ := newOpaqueID()
	req := platformSelectionRequest{ServiceInfo: configured.ServiceInfo, Caller: configured.Caller, OperationID: id, SourceSeq: configured.SourceSeq, Input: &platformSelectionInput{Kind: "account"}}
	first := selectionCall(t, m, "start", req, false)
	q := selectionQuery(req, true)
	q.Version = first.Version
	q.Draft = &platformSelectionDraft{PlatformID: configured.Input.PlatformID, PlatformVersion: configured.Input.PlatformVersion, Name: "untrusted label"}
	edited := selectionCall(t, m, "update", q, true)
	q.Draft = nil
	q.Version = edited.Version
	// An account selected at an older verification version cannot be submitted.
	m.mu.Lock()
	ticket, err := m.platforms.BeginVerification(configured.Input.PlatformID, configured.Input.PlatformVersion)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	_, err = m.platforms.CompleteVerification(ticket, connectValidation())
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if w := managerHTTP(m, "/v1/platforms/selection-interactions/submit", q); w.Code != 409 {
		t.Fatal("stale account admitted", w.Code, w.Body.String())
	}
	if _, err = svc.remove(apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}); err != nil {
		t.Fatal(err)
	}
	inboxRoutingMember(t, svc, member.AgentID, "selection-replacement")
	if w := managerHTTP(m, "/v1/platforms/selection-interactions/submit", q); w.Code != 403 {
		t.Fatal("replacement submitted old form", w.Code, w.Body.String())
	}
}

func TestPlatformSelectionAutomaticallyContinuesThroughInbox(t *testing.T) {
	m, svc, req := selectionFixture(t)
	mode := "inbox"
	req.Notify = &mode
	v := selectionCall(t, m, "start", req, false)
	id := v.MemberIdentity
	b := newControlledBackend()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	member, err := spawnScheduledMember(svc.rt, &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Backend: b, Store: svc.store, Room: svc.room, Inbox: func(ctx context.Context) (InboxStatus, error) { return svc.memberInboxStatus(ctx, id) }}, svc.scheduler)
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(id.MembershipID, member)
	if _, err = member.Call[MemberStatus](ctx, ControlMember{MemberIdentity: id, Action: "run", State: UnreadChanged{MemberIdentity: id}}); err != nil {
		t.Fatal(err)
	}
	q := selectionQuery(req, true)
	q.Version = v.Version
	selectionCall(t, m, "submit", q, true)
	select {
	case input := <-b.entered:
		if input.Inbox == nil || input.Inbox.SnapshotSeq != 1 || input.LatestSeq != req.SourceSeq {
			t.Fatal(input)
		}
	case <-ctx.Done():
		t.Fatal("no selection continuation", ctx.Err())
	}
	if err := svc.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	page, err := svc.readReferenceInbox(ctx, id, 1, 25, nil)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].TaskEvent.EventType != "user_selection" || page.Entries[0].TaskEvent.ResultRef != "platforms/selection/"+req.OperationID {
		t.Fatal(page, err)
	}
	for {
		_, err := svc.inboxes[id.MembershipID].Call[InboxStatus](ctx, ConfirmInboxBatch{MemberIdentity: id, TurnNumber: 1, Receipt: page.Batch.Receipt})
		if err == nil {
			break
		}
		if !errors.Is(err, ErrBusy) || ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	b.finish <- nil
	waitMember(t, ctx, member, id, func(s MemberStatus) bool { return !s.Active && s.Inbox != nil && s.Inbox.ConsumedThrough == 1 })
	q = selectionQuery(req, false)
	q.ResultVersion = 1
	selectionCall(t, m, "acknowledge", q, false)
	m.deliverConnectNotifications(ctx)
	state, err := svc.store.LoadRoom()
	if err != nil || state.LatestSeq != req.SourceSeq {
		t.Fatal("selection created chat message", err)
	}
}

func TestPlatformSelectionResultDeliverySurvivesUnknownHandoff(t *testing.T) {
	for _, afterReplace := range []bool{false, true} {
		t.Run(fmt.Sprint(afterReplace), func(t *testing.T) {
			m, _, req := selectionFixture(t)
			v := selectionCall(t, m, "start", req, false)
			human := selectionQuery(req, true)
			human.Version = v.Version
			v = selectionCall(t, m, "submit", human, true)
			m.mu.Lock()
			n, err := m.readSelectionNotificationLocked(v.platformSelection)
			n.Mode = "inbox"
			if err == nil {
				err = m.saveSelectionNotificationLocked(n)
			}
			original := m.store.ops.rename
			m.store.ops.rename = func(from, to string) error {
				if to == m.selectionNotificationPath(req.OperationID) {
					raw, _ := os.ReadFile(from)
					var n platformOperationNotification
					_ = json.Unmarshal(raw, &n)
					if n.Receipt != nil {
						if afterReplace {
							if err := original(from, to); err != nil {
								return err
							}
						}
						return errors.New("injected uncertain selection handoff")
					}
				}
				return original(from, to)
			}
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			m.deliverConnectNotifications(t.Context())
			m.mu.Lock()
			m.store.ops.rename = original
			frozen := m.store.check(true)
			m.mu.Unlock()
			if !errors.Is(frozen, ErrCommitUnknown) {
				t.Fatal("handoff boundary not reached", frozen)
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			reopened := testManager(t, m.store.dir, nil)
			svc := eventLiveService(t, &eventHarness{m: reopened, room: roomSummary{RoomIdentity: req.RoomIdentity}})
			reopened.deliverConnectNotifications(t.Context())
			reopened.deliverConnectNotifications(t.Context())
			q := selectionQuery(req, false)
			q.ServiceInfo = svc.info
			got := selectionCall(t, reopened, "status", q, false)
			if got.Status != "completed" || got.Draft.Name != v.Draft.Name || got.Continuation.InboxSeq != 1 {
				t.Fatal(got)
			}
			inbox, err := svc.memberInboxStatus(t.Context(), got.MemberIdentity)
			if err != nil || inbox.LatestSeq != 1 {
				t.Fatal("duplicate event after reopen", inbox, err)
			}
		})
	}
}

func TestPlatformSelectionMultipleVerifiedAccountsKeepExactTarget(t *testing.T) {
	m, _, _, configured := memberToolFixture(t)
	file, err := m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	original := file.Platforms[0]
	profile := *original.CLIProfile
	profile.Profile = "other-account"
	otherID, _ := newOpaqueID()
	other := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: otherID, Name: "另一个账号", Provider: "lark", URL: original.URL, CLIProfile: &profile})
	validation := original.Validation
	validation.Account.ID.Value = "ou_other_account"
	m.mu.Lock()
	ticket, err := m.platforms.BeginVerification(other.ID, other.Version)
	if err == nil {
		_, err = m.platforms.CompleteVerification(ticket, validation)
	}
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := newOpaqueID()
	req := platformSelectionRequest{ServiceInfo: configured.ServiceInfo, Caller: configured.Caller, OperationID: id, SourceSeq: configured.SourceSeq, Input: &platformSelectionInput{Kind: "account"}}
	v := selectionCall(t, m, "start", req, false)
	q := selectionQuery(req, true)
	w := managerHTTP(m, "/v1/platforms/selection-interactions/candidates", q)
	var candidates []platformSelectionAccount
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &candidates) != nil || len(candidates) != 2 || candidates[0].Account == candidates[1].Account {
		t.Fatal(w.Code, w.Body.String())
	}
	var selected platformSelectionAccount
	for _, c := range candidates {
		if c.ID == otherID {
			selected = c
		}
	}
	q.Version = v.Version
	q.Draft = &platformSelectionDraft{PlatformID: selected.ID, PlatformVersion: selected.Version, Name: "forged display name"}
	v = selectionCall(t, m, "update", q, true)
	q.Version = v.Version
	q.Draft = nil
	receipt := selectionCall(t, m, "submit", q, true)
	if receipt.Account.ID.Value != "ou_other_account" || receipt.Draft.PlatformID != selected.ID || receipt.Draft.Name != "另一个账号" || receipt.Draft.PlatformVersion != selected.Version {
		t.Fatal(receipt)
	}
}
