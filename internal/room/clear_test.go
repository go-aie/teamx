package room

import (
	"context"
	"errors"
	"testing"
)

func TestClearMessagesPreservesNewArrivalsAndReadProgress(t *testing.T) {
	h := newRoomHarness(t, historyRoom(3, "old message"), nil)
	caller := agentCaller("A", "generation-A")
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: caller, Mode: ReadUnread})
	clear := ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 3}
	// This arrival happens after the operator's observed snapshot.
	arrival := h.post(humanCaller, "new-arrival", "keep me")
	arrival.ReplyTo = 3
	roomCall[PostReceipt](t, h, arrival)
	receipt := roomCall[ClearReceipt](t, h, clear)
	if receipt.ClearedThrough != 3 {
		t.Fatal(receipt)
	}
	list := h.list(t)
	if list.LatestSeq != 4 || list.Members[0].ReadThrough != 3 || list.Members[0].UnreadCount != 1 {
		t.Fatal(list)
	}
	roomError[ReadConfirmation](t, h, h.confirm(caller, page.ReadReceipt), ErrReadExpired)
	oldSnapshot := uint64(2)
	roomError[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll, SnapshotSeq: &oldSnapshot}, ErrReadExpired)
	for _, mode := range []ReadMode{ReadAll, ReadLatest, ReadRange, ReadUnread} {
		req := ReadMessages{RoomIdentity: h.id, Caller: caller, Mode: mode, Peek: mode == ReadUnread}
		if mode == ReadLatest {
			req.Latest = 50
		}
		if mode == ReadRange {
			after := uint64(0)
			req.After = &after
		}
		read := roomCall[ReadPage](t, h, req)
		if read.ClearedThrough != 3 || read.FromExclusive != 3 || len(read.Messages) != 1 || read.Messages[0].Seq != 4 || read.Messages[0].Text != "keep me" {
			t.Fatal(mode, read)
		}
	}
	// Retrying the same clear cannot delete messages that arrived afterward.
	roomCall[ClearReceipt](t, h, clear)
	if h.list(t).Revision != list.Revision {
		t.Fatal("retry changed revision")
	}
	// An already retained send still deduplicates if its quote was cleared.
	if got := roomCall[PostReceipt](t, h, arrival); got.Seq != 4 {
		t.Fatal(got)
	}
	post := h.post(humanCaller, "stale-reply", "reply")
	post.ReplyTo = 1
	roomError[PostReceipt](t, h, post, ErrRequest)
	post.ReplyTo = 4
	if got := roomCall[PostReceipt](t, h, post); got.Seq != 5 {
		t.Fatal(got)
	}
	clear.ThroughSeq = 5
	roomCall[ClearReceipt](t, h, clear)
	saved, err := h.store.LoadRoom()
	if err != nil || len(saved.Messages) != 0 || saved.LatestSeq != 5 || saved.ClearedThrough != 5 {
		t.Fatal(saved, err)
	}
	// Reload from the persisted empty history and continue numbering.
	h.reload(t, true)
	got, err := h.addr.Call[PostReceipt](h.ctx, h.post(humanCaller, "after-reload", "new discussion"))
	if err != nil || got.Seq != 6 {
		t.Fatal(got, err)
	}
}

func TestClearMessagesFailurePreservesHistory(t *testing.T) {
	failure := errors.New("disk unavailable")
	h := newRoomHarness(t, historyRoom(1, "keep on failure"), func(r *Room, _ *Store) {
		r.save = func(RoomSnapshot) error { return failure }
	})
	clear := ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 1}
	roomError[ClearReceipt](t, h, clear, failure)
	read := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll})
	if read.ClearedThrough != 0 || len(read.Messages) != 1 || h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal(read)
	}
	clear.Caller = agentCaller("A", "generation-A")
	roomError[ClearReceipt](t, h, clear, ErrIdentity)
}

func TestWebClearMessagesRequiresOperatorAndSnapshot(t *testing.T) {
	svc, client := testService(t)
	agent := addFixtureMemberCLI(t, svc, client, "A")
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, Text: "clear me", ClientMessageID: "one"}
	var posted PostReceipt
	if err := client.request(context.Background(), "/v1/send", &req, &posted); err != nil {
		t.Fatal(err)
	}
	var receipt ClearReceipt
	if err := client.request(context.Background(), "/v1/messages/clear", &req, &receipt); err == nil {
		t.Fatal("missing snapshot accepted")
	}
	agent.SnapshotSeq = &posted.Seq
	if err := client.request(context.Background(), "/v1/messages/clear", &agent, &receipt); err == nil {
		t.Fatal("agent cleared messages")
	}
	stale := req
	stale.ServiceEpoch = "old-service"
	stale.SnapshotSeq = &posted.Seq
	if err := client.request(context.Background(), "/v1/messages/clear", &stale, &receipt); err == nil {
		t.Fatal("stale service cleared messages")
	}
	req.SnapshotSeq = &posted.Seq
	if err := client.request(context.Background(), "/v1/messages/clear", &req, &receipt); err != nil || receipt.ClearedThrough != posted.Seq {
		t.Fatal(receipt, err)
	}
	page, err := svc.room.Call[ReadPage](context.Background(), ReadMessages{RoomIdentity: svc.info.RoomIdentity, Caller: humanCaller, Mode: ReadAll})
	if err != nil || len(page.Messages) != 0 || page.ClearedThrough != posted.Seq {
		t.Fatal(page, err)
	}
}
