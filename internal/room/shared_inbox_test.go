package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func sharedReaders(t *testing.T) (*roomHarness, *inputSession, *inputSession, *sharedInputStorage) {
	t.Helper()
	return sharedReadersWithin(t, 10*time.Second)
}
func sharedReadersWithin(t *testing.T, timeout time.Duration) (*roomHarness, *inputSession, *inputSession, *sharedInputStorage) {
	t.Helper()
	h, a := unifiedReaderFixtureWithTimeout(t, 0, timeout)
	svc := a.service
	bID := h.memberID("B", "generation-B")
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: bID, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running"}}); err != nil {
		t.Fatal(err)
	}
	bStorage, err := svc.inputStorage(bID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newInputSession(h.ctx, svc, bStorage, "session-B", 1, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.configureSharedMembers(h.ctx, 0, []MemberIdentity{a.storage.id, bID}); err != nil {
		t.Fatal(err)
	}
	return h, a, b, h.store.sharedHandle.Load()
}
func receiveSharedFixture(t *testing.T, s *inputSession, key, iid string) {
	t.Helper()
	item := storedGitLab(s.storage.id, key, "events", "7", iid, "共享项目记录 "+key)
	item.Route.Target = notification.Target{Mode: "shared_inbox", InboxID: sharedProjectInbox}
	if err := s.service.receiveInbox(context.Background(), item.Route.Target, item); err != nil {
		t.Fatal(err)
	}
}
func sharedRef(t *testing.T, s *inputSession) string {
	t.Helper()
	c, err := s.catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.ReadableList(inputstream.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range d.Streams {
		if v.Kind == "shared" {
			return v.Ref
		}
	}
	t.Fatal("missing shared stream", d)
	return ""
}
func TestSharedInboxTwoMembersReadAndIndependentProgress(t *testing.T) {
	h, a, b, p := sharedReaders(t)
	receiveSharedFixture(t, a, "one", "42")
	receiveSharedFixture(t, a, "two", "77")
	receiveSharedFixture(t, a, "three", "42")
	ar, br := sharedRef(t, a), sharedRef(t, b)
	if ar == br {
		t.Fatal("member refs are not bound to Grant")
	}
	result, confirm, err := a.readPage(h.ctx, inputstream.ReadRequest{Turn: 1, Stream: ar, ID: "A-page", Limit: 1}, a.renderInputPage)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("no result")
	}
	v := p.view.Load()
	if len(v.records) != 3 || len(v.root.Pages) != 1 {
		t.Fatal(v)
	}
	if err := confirm(h.ctx); err != nil {
		t.Fatal(err)
	}
	ga, _ := p.view.Load().grant(inputMember(a.storage.id))
	gb, _ := p.view.Load().grant(inputMember(b.storage.id))
	ac, err := inputstream.LoadSharedConsumption(h.store.dir, inputMember(a.storage.id), v.root.Inbox, ga.ID)
	if err != nil {
		t.Fatal(err)
	}
	bc, err := inputstream.LoadSharedConsumption(h.store.dir, inputMember(b.storage.id), v.root.Inbox, gb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ac.Through) != 1 || len(bc.Through) != 0 {
		t.Fatal(ac, bc)
	}
	if len(ac.Proofs) != 1 {
		t.Fatal(ac)
	}
	_, bConfirm, err := b.readPage(h.ctx, inputstream.ReadRequest{Turn: 1, Stream: br, ID: "B-page"}, b.renderInputPage)
	if err != nil {
		t.Fatal(err)
	}
	if err := bConfirm(h.ctx); err != nil {
		t.Fatal(err)
	}
	if usage, _ := p.repository.InspectUsage(); usage.Records != 3 {
		t.Fatal("body duplicated", usage)
	}
	for _, member := range []string{a.storage.id.MembershipID, b.storage.id.MembershipID} {
		if _, err := os.Stat(filepath.Join(h.store.dir, "inboxes", member, "gitlab", "partition.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("copied shared body to member", err)
		}
	}
	preview, err := a.service.sharedInboxView()
	if err != nil || preview.Records != 3 || len(preview.Entries) != 3 {
		t.Fatal(preview, err)
	}
}
func TestSharedInboxHandoffRevocationAndReceiptRecovery(t *testing.T) {
	h, a, b, p := sharedReaders(t)
	receiveSharedFixture(t, a, "one", "42")
	ref := sharedRef(t, a)
	call := "framework-call"
	var request inputstream.ReadRequest
	if err := a.coordinate(h.ctx, func(r inputstream.ExecutionStorage) error {
		var err error
		request, err = r.BindCall(a.execution, call, ref, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.readPage(h.ctx, request, a.renderInputPage); err != nil {
		t.Fatal(err)
	}
	if err := a.service.closeSharedMember(h.ctx, a.storage.id); !errors.Is(err, ErrBusy) {
		t.Fatal("handoff must retain closing Grant", err)
	}
	if _, _, _, err := a.sharedStream(ref); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	if err := a.receiveCall(h.ctx, call); err != nil {
		t.Fatal("same Session proof must settle after revoke", err)
	}
	if err := a.service.closeSharedMember(h.ctx, a.storage.id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.catalog(h.ctx); err != nil {
		t.Fatal(err)
	}
	old := p.view.Load()
	gaID := ""
	for _, g := range old.root.Grants {
		if g.Member == inputMember(a.storage.id) {
			gaID = g.ID
			if g.Status != "closed" {
				t.Fatal(g)
			}
		}
	}
	if _, err := a.service.configureSharedMembers(h.ctx, old.root.ConfigVersion, []MemberIdentity{a.storage.id, b.storage.id}); err != nil {
		t.Fatal(err)
	}
	ga, _ := p.view.Load().grant(inputMember(a.storage.id))
	if ga.ID == gaID {
		t.Fatal("reused Grant")
	}
	if _, _, _, err := a.sharedStream(ref); !errors.Is(err, ErrIdentity) {
		t.Fatal("old Grant stream accepted", err)
	}
	if _, err := a.catalog(h.ctx); err != nil {
		t.Fatal(err)
	}
}
func TestSharedInboxPrivateAndSharedReadBudget(t *testing.T) {
	h, a, _, p := sharedReaders(t)
	for i := range 28 {
		receiveSharedFixture(t, a, fmt.Sprintf("item-%02d", i), "42")
	}
	item := storedGitLab(a.storage.id, "private", "events", "7", "99", "private record")
	privateReceipt, err := a.storage.Receive(item, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := h.store.LoadAgent(a.storage.id)
	if err != nil {
		t.Fatal(err)
	}
	agent.TurnNumber = 2
	agent.Turn.Number = 2
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	a = startPlatformReader(t, h, a.storage, a.service, 2)
	ref := sharedRef(t, a)
	_, confirm, err := a.readPage(h.ctx, inputstream.ReadRequest{Turn: 2, Stream: ref, ID: "shared-first", Limit: 24}, a.renderInputPage)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirm(h.ctx); err != nil {
		t.Fatal(err)
	}
	state, err := a.inputState(h.ctx)
	if err != nil || state.Used != 24 {
		t.Fatal(state, err)
	}
	privateRef, err := a.storage.repository.StreamAt(inputstream.Location{Partition: privateReceipt.Partition, Seq: privateReceipt.Seq})
	if err != nil {
		t.Fatal(err)
	}
	_, confirm, err = a.readPage(h.ctx, inputstream.ReadRequest{Turn: 2, Stream: privateRef, ID: "private-last", Limit: 25}, a.renderInputPage)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirm(h.ctx); err != nil {
		t.Fatal(err)
	}
	state, _ = a.inputState(h.ctx)
	if state.Used != 25 || len(state.Attempts[1].Entries) != 1 {
		t.Fatal(state)
	}
	for _, page := range p.view.Load().root.Pages {
		if page.State != "released" {
			t.Fatal(page)
		}
	}
	encoded, _ := json.Marshal(state)
	if len(encoded) == 0 {
		t.Fatal("missing coordinator")
	}
}

func TestSharedInboxSettledReplayAndMemberPreview(t *testing.T) {
	h, a, b, _ := sharedReaders(t)
	receiveSharedFixture(t, a, "one", "42")
	ref := sharedRef(t, a)
	q := inputstream.ReadRequest{Turn: 1, ID: "same-page", Stream: ref}
	first, confirm, err := a.readPage(h.ctx, q, a.renderInputPage)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirm(h.ctx); err != nil {
		t.Fatal(err)
	}
	replay, confirm, err := a.readPage(h.ctx, q, a.renderInputPage)
	if err != nil {
		t.Fatal("settled replay", err)
	}
	if err := confirm(h.ctx); err != nil {
		t.Fatal(err)
	}
	one, _ := json.Marshal(first)
	two, _ := json.Marshal(replay)
	if string(one) != string(two) {
		t.Fatal("page changed after settlement")
	}
	for _, s := range []*inputSession{a, b} {
		preview, err := s.storage.previewHistory("")
		if err != nil || len(preview.Entries) != 1 || !preview.Entries[0].Shared {
			t.Fatal(preview, err)
		}
		expected := "pending"
		if s == a {
			expected = "confirmed"
		}
		if preview.Entries[0].Consumption != expected {
			t.Fatal(preview)
		}
	}
}

func TestSharedInboxRevokeBetweenBodyAuthorizationAndMemberHandoff(t *testing.T) {
	h, a, _, p := sharedReaders(t)
	receiveSharedFixture(t, a, "one", "42")
	q := inputstream.ReadRequest{Turn: 1, ID: "authorization-crash", Stream: sharedRef(t, a)}
	attempt, err := a.reserveShared(h.ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.preparePlatformPage(h.ctx, attempt, a.renderInputPage); err != nil {
		t.Fatal(err)
	}
	if _, err := sharedOwnerValue(h.ctx, p, func() (struct{}, error) { return struct{}{}, p.repository.AuthorizeSharedHandoff(*attempt.Shared) }); err != nil {
		t.Fatal(err)
	}
	if err := a.service.closeSharedMember(h.ctx, a.storage.id); err != nil {
		t.Fatal("persisted pre-output abort should settle the authorized but unhanded page", err)
	}
	state, err := a.inputState(h.ctx)
	if err != nil || state.Attempts[0].Phase != "failed_before_output" {
		t.Fatal(state, err)
	}
	if err := a.coordinate(h.ctx, func(r inputstream.ExecutionStorage) error { return r.Handoff(a.execution, attempt.ID) }); !errors.Is(err, inputstream.ErrConflict) {
		t.Fatal("late handoff crossed the durable abort", err)
	}
	for _, g := range p.view.Load().root.Grants {
		if g.Member == inputMember(a.storage.id) && g.Status != "closed" {
			t.Fatal(g)
		}
	}
}
