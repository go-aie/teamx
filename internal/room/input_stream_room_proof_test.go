package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRoomConfirmationReopensSamePageProof(t *testing.T) {
	state := historyRoom(4, "read-only human restriction")
	state.Members = append(state.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: state.Members[0].Runner})
	h := newRoomHarness(t, state, nil)
	a := agentCaller("A", "generation-A")
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread, Limit: 2})
	// Only the isolated fixture's delivery page is separate; it stores no Room
	// cursor. The actual coordinator/turn journal is deliberately deferred.
	pagePath := filepath.Join(h.store.dir, "fixture-pending-page.json")
	pageBytes, _ := json.Marshal(page)
	if err := os.WriteFile(pagePath, pageBytes, 0600); err != nil {
		t.Fatal(err)
	}
	confirmed := roomCall[ReadConfirmation](t, h, h.confirm(a, page.ReadReceipt))
	// Simulate loss of the coordinator's settlement after the native commit.
	saved, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	proof := saved.ReadProofs[a.MembershipID]
	if proof.Snapshot != 4 || proof.From != 0 || !reflect.DeepEqual(proof.Entries, []uint64{1, 2}) || proof.Confirmation != confirmed {
		t.Fatal("not a same-page proof", proof)
	}
	h.reload(t, true)
	var pending ReadPage
	pageBytes, err = os.ReadFile(pagePath)
	if err != nil || json.Unmarshal(pageBytes, &pending) != nil {
		t.Fatal("fixture page lost", err)
	}
	if pending.ReadReceipt != proof.Confirmation.ReadReceipt || pending.SnapshotSeq != proof.Snapshot || pending.FromExclusive != proof.From || pending.ScannedThrough != proof.Confirmation.ReadThrough {
		t.Fatal("cannot settle different page")
	}
	seqs := []uint64{}
	for _, message := range pending.Messages {
		seqs = append(seqs, message.Seq)
	}
	if !reflect.DeepEqual(seqs, proof.Entries) {
		t.Fatal("native proof does not cover this precise set")
	}
	replay := roomCall[ReadConfirmation](t, h, h.confirm(a, page.ReadReceipt))
	if replay != confirmed {
		t.Fatal("native proof was not replayed", replay, confirmed)
	}
	roomError[ReadConfirmation](t, h, h.confirm(a, "different-page-same-through"), ErrReadExpired)
	b := agentCaller("B", "generation-B")
	roomError[ReadConfirmation](t, h, h.confirm(b, page.ReadReceipt), ErrReadExpired)
	after := h.list(t)
	if after.Members[0].ReadThrough != 2 || after.Members[1].ReadThrough != 0 || after.Revision != saved.Revision {
		t.Fatal("settlement changed authoritative state", after)
	}
	broken := saved
	broken.ReadProofs = map[string]roomReadProof{a.MembershipID: proof}
	proof.Entries = []uint64{1, 3}
	broken.ReadProofs[a.MembershipID] = proof
	if broken.validate() == nil {
		t.Fatal("corrupt proof accepted")
	}
	// New Memberships do not inherit the previous member's confirmation.
	roomCall[MemberInfo](t, h, RemoveMember{MemberIdentity: MemberIdentity{RoomIdentity: h.id, AgentID: "A", MembershipID: a.MembershipID}})
	saved, err = h.store.LoadRoom()
	if err != nil || len(saved.ReadProofs) != 0 {
		t.Fatal("removed proof retained", saved.ReadProofs, err)
	}
}
