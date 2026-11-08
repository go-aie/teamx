package room

import (
	"path/filepath"
	"testing"
)

func TestVirtualSessionLoadsPrivateStateWithoutReplayingScript(t *testing.T) {
	h := newRoomHarness(t, historyRoom(8, "body"), nil)
	id := h.memberID("A", "generation-A")
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(h.store.dir, "agents", id.MembershipID, "session.json")}
	backend := virtualBackend{store: h.store, script: "fail,ignore"}
	s, err := backend.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	n := UnreadChanged{MemberIdentity: id, Revision: 8, LatestSeq: 8, NewestUnreadSeq: 8, UnreadCount: 8}
	if err := s.Run(h.ctx, n); err == nil {
		t.Fatal("expected script failure")
	}
	first, err := h.store.loadVirtualSession(id)
	if err != nil || first.ScriptPosition != 1 || first.LastError == "" {
		t.Fatal(first, err)
	}
	if err := s.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := backend.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Run(h.ctx, n); err != nil {
		t.Fatal("replayed failed action", err)
	}
	second, err := h.store.loadVirtualSession(id)
	if err != nil || second.SessionID != first.SessionID || second.Turns != 2 || second.ScriptPosition != 2 || second.LastError != "" {
		t.Fatal(second, err)
	}
	if err := reopened.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
}
