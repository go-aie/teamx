package room

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func integrationPost(owner RoomIdentity, id int64) postIntegrationMessage {
	return postIntegrationMessage{RoomIdentity: owner, DeliveryID: fmt.Sprintf("gitlab-%d", id), Text: fmt.Sprintf("GitLab · event %d", id), Source: IntegrationSource{Provider: "gitlab", Channel: "events", Instance: "https://gitlab.example.com", ProjectID: 7, ItemKey: fmt.Sprintf("https://gitlab.example.com|7|%d", id), SubscriptionID: strings.Repeat("a", 32)}}
}

func TestGitLabIntegrationReceiptsSurviveClearAndReopen(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), nil)
	post := integrationPost(h.id, 1)
	first := roomCall[PostReceipt](t, h, post)
	roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: first.Seq})
	roomCall[PostReceipt](t, h, post)
	saved, err := h.store.LoadRoom()
	if err != nil || len(saved.Messages) != 0 || saved.LatestSeq != 1 || len(saved.IntegrationReceipts) != 1 {
		t.Fatal(saved, err)
	}
	// Reload the actor from the actual disk snapshot, while retaining root ownership.
	if err := h.rt.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.rt, err = actorx.NewRuntime(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRoom(h.store)
	if err != nil {
		t.Fatal(err)
	}
	h.addr, err = h.rt.Spawn(r)
	if err != nil {
		t.Fatal(err)
	}
	retry := roomCall[PostReceipt](t, h, post)
	if retry != first {
		t.Fatal(first, retry)
	}
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll})
	if len(page.Messages) != 0 || page.SnapshotSeq != 1 {
		t.Fatal(page)
	}
	missing := post
	missing.Source.Channel = ""
	roomError[PostReceipt](t, h, missing, ErrRequest)
	missing = post
	missing.Source.ItemKey = ""
	roomError[PostReceipt](t, h, missing, ErrRequest)
	bad := post
	bad.Source.ItemKey += "-other"
	roomError[PostReceipt](t, h, bad, ErrConflict)
	roomError[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: Caller{Author: Author{Kind: "integration", ID: "gitlab"}}, ClientMessageID: "spoof", Text: "spoof"}, ErrIdentity)
	roomError[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: Caller{Author: Author{Kind: "integration", ID: "gitlab"}}, ThroughSeq: 1}, ErrIdentity)
}

func TestGitLabIntegrationCommitFailureDoesNotPublishReceipt(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), func(r *Room, _ *Store) {
		save := r.save
		calls := 0
		r.save = func(next RoomSnapshot) error {
			calls++
			if calls == 1 {
				return errors.New("injected save failure")
			}
			return save(next)
		}
	})
	post := integrationPost(h.id, 1)
	if _, err := h.addr.Call[PostReceipt](h.ctx, post); err == nil {
		t.Fatal("save failure accepted")
	}
	saved, _ := h.store.LoadRoom()
	if saved.LatestSeq != 0 || len(saved.IntegrationReceipts) != 0 {
		t.Fatal(saved)
	}
	if got := roomCall[PostReceipt](t, h, post); got.Seq != 1 {
		t.Fatal(got)
	}
}

// A real Room actor/store with service lifecycle guards; no member/model is
// assembled here. Scheduler behavior is covered independently below.
func eventLiveService(t *testing.T, h *eventHarness) *service {
	t.Helper()
	svc, err := startService(h.m.roomDir(h.room.RoomID), h.room.RoomID, serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	h.m.runtimes[h.room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	h.m.mu.Unlock()
	return svc
}
func seedEventInbox(t *testing.T, h *eventHarness, id int64) {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	s, _ := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	s.ProjectID = 7
	if _, _, err := s.Accept([]gitlab.Event{h.event(id, 0)}, h.clock.get()); err != nil {
		t.Fatal(err)
	}
	if err := h.m.notifications.SaveLocked(s); err != nil {
		t.Fatal(err)
	}
}

func TestGitLabIntegrationNotificationsRespectPauseBudgetAndFault(t *testing.T) {
	for _, mode := range []string{"pause", "budget", "fault", "running"} {
		t.Run(mode, func(t *testing.T) {
			b := newControlledBackend()
			h, target, id := memberHarness(t, b, nil, func(m *MemberAgent) {
				m.MaxTurns = 2
				if mode == "budget" {
					m.MaxTurns = 1
				}
			})
			roomCall[MemberInfo](t, h, AttachMember{MemberIdentity: id, Target: target})
			if mode != "pause" {
				n := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
				controlMember(t, h, target, id, "run", n, false, false)
				awaitTurn(t, h, b)
				if mode == "fault" {
					b.finish <- errors.Join(errDSHPaused, errors.New("interrupted external operation"))
				} else {
					b.finish <- nil
				}
				waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
			}
			receipt := roomCall[PostReceipt](t, h, integrationPost(h.id, 2))
			status := waitMember(t, h.ctx, target, id, func(s MemberStatus) bool {
				return s.Notification != nil && s.Notification.NewestUnreadSeq == receipt.Seq
			})
			if mode == "running" {
				awaitTurn(t, h, b)
				b.finish <- nil
				status = waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
			}
			if mode == "fault" && (!status.Paused || !strings.Contains(status.Error, "interrupted external operation")) {
				t.Fatal(status)
			}
			if status.Active || mode == "pause" && (!status.Paused || b.opens.Load() != 0) || (mode == "budget" || mode == "fault") && status.Agent.TurnNumber != 1 {
				t.Fatal(status)
			}
			page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: Caller{Author: Author{Kind: "agent", ID: id.AgentID}, MembershipID: id.MembershipID}, Mode: ReadUnread, Peek: true})
			if len(page.Messages) != 2 || page.Messages[1].Author.Kind != "integration" {
				t.Fatal(page)
			}
		})
	}
}
