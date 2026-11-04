package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/actorx"
)

func TestNotificationObserverVersions(t *testing.T) {
	rt, err := actorx.NewRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(context.Background())
	id := MemberIdentity{RoomIdentity: emptyRoom().RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	addr, err := rt.Spawn(&notificationObserver{Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	latest := UnreadChanged{MemberIdentity: id, Revision: 10, LatestSeq: 8, NewestUnreadSeq: 8, UnreadCount: 3}
	for _, msg := range []UnreadChanged{latest, latest, {MemberIdentity: id, Revision: 9, UnreadCount: 99}} {
		if _, err := addr.Call[any](ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	foreign := latest
	foreign.MembershipID = "old-generation"
	if _, err := addr.Call[any](ctx, foreign); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	foreign = latest
	foreign.RoomInstance = "old-room"
	if _, err := addr.Call[any](ctx, foreign); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	status, err := addr.Call[MemberStatus](ctx, InspectMember{id})
	if err != nil || status.Notification == nil || *status.Notification != latest {
		t.Fatal(status, err)
	}
	data, err := json.Marshal(status.Notification)
	if err != nil || strings.Contains(string(data), "text") || strings.Contains(string(data), "messages") {
		t.Fatal(string(data), err)
	}
	zero := latest
	zero.Revision, zero.UnreadCount, zero.NewestUnreadSeq = 11, 0, 0
	if _, err := addr.Call[any](ctx, zero); err != nil {
		t.Fatal(err)
	}
	status, err = addr.Call[MemberStatus](ctx, InspectMember{id})
	if err != nil || *status.Notification != zero {
		t.Fatal(status, err)
	}
}

type blockedNotifications struct {
	entered chan struct{}
	release chan struct{}
	seen    chan UnreadChanged
}

func (b *blockedNotifications) Bindings() []actorx.Binding {
	return []actorx.Binding{
		actorx.HandleMessage(func(ctx actorx.Context, _ struct{}) error {
			select {
			case b.entered <- struct{}{}:
			default:
			}
			select {
			case <-b.release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}),
		actorx.HandleMessage(func(_ actorx.Context, n UnreadChanged) error { b.seen <- n; return nil }),
	}
}

func waitNotification(t *testing.T, ctx context.Context, addr actorx.Addr, id MemberIdentity, predicate func(UnreadChanged) bool) UnreadChanged {
	t.Helper()
	timer := time.NewTicker(5 * time.Millisecond)
	defer timer.Stop()
	for {
		status, err := addr.Call[MemberStatus](ctx, InspectMember{id})
		if err != nil {
			t.Fatal(err)
		}
		if status.Notification != nil && predicate(*status.Notification) {
			return *status.Notification
		}
		select {
		case <-ctx.Done():
			t.Fatal("notification not delivered", ctx.Err())
		case <-timer.C:
		}
	}
}

func TestRoomNotificationFullTargetRetriesAndDoesNotBlockOthers(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), func(r *Room, _ *Store) { r.maintenanceInterval = 20 * time.Millisecond }, actorx.WithMailboxCapacity(8))
	block := &blockedNotifications{entered: make(chan struct{}, 1), release: make(chan struct{}), seen: make(chan UnreadChanged, 64)}
	defer onceRelease(block.release)()
	full, err := h.rt.Spawn(block)
	if err != nil {
		t.Fatal(err)
	}
	if err := full.Send(h.ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	<-block.entered
	for i := 0; i < 8; i++ {
		if err := full.Send(h.ctx, struct{}{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := full.Send(h.ctx, struct{}{}); !errors.Is(err, actorx.ErrFull) {
		t.Fatal("target not full", err)
	}
	a, b := h.memberID("A", "generation-A"), h.memberID("B", "generation-B")
	roomCall[MemberInfo](t, h, AddMember{MemberIdentity: a, Target: full, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	healthy, err := h.rt.Spawn(&notificationObserver{Identity: b})
	if err != nil {
		t.Fatal(err)
	}
	roomCall[MemberInfo](t, h, AddMember{MemberIdentity: b, Target: healthy, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	for _, key := range []string{"one", "two", "three"} {
		roomCall[PostReceipt](t, h, h.post(humanCaller, key, "private-body"))
	}
	preview := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll})
	if len(preview.Messages) != 3 {
		t.Fatal(preview)
	}
	waitNotification(t, h.ctx, healthy, b, func(n UnreadChanged) bool { return n.UnreadCount == 3 })
	onceRelease(block.release)()
	// No additional Room mutation is needed: maintenance retries the dirty state.
	for {
		select {
		case n := <-block.seen:
			if n.UnreadCount == 3 && n.NewestUnreadSeq == 3 {
				return
			}
		case <-h.ctx.Done():
			t.Fatal("full target never retried")
		}
	}
}

// Delay one actual delivery while later Room commits complete.
type pausedNotificationChannel struct {
	actorx.Channel
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (c *pausedNotificationChannel) Deliver(ctx context.Context, d actorx.Delivery, reply actorx.Responder) error {
	if _, ok := d.Message.(UnreadChanged); ok {
		var err error
		c.once.Do(func() {
			close(c.entered)
			select {
			case <-c.release:
			case <-ctx.Done():
				err = ctx.Err()
			}
		})
		if err != nil {
			return err
		}
	}
	return c.Channel.Deliver(ctx, d, reply)
}

func TestRoomOldNotificationCompletionPreservesNewStateAndMembership(t *testing.T) {
	for _, rejoin := range []bool{false, true} {
		t.Run(fmt.Sprint(rejoin), func(t *testing.T) {
			channel := &pausedNotificationChannel{Channel: actorx.NewLocalChannel(), entered: make(chan struct{}), release: make(chan struct{})}
			h := newRoomHarness(t, emptyRoom(), func(r *Room, _ *Store) { r.notificationTimeout = 5 * time.Second }, actorx.WithChannel(channel))
			defer onceRelease(channel.release)()
			id := h.memberID("A", "generation-A")
			target, err := h.rt.Spawn(&notificationObserver{Identity: id})
			if err != nil {
				t.Fatal(err)
			}
			roomCall[MemberInfo](t, h, AddMember{MemberIdentity: id, Target: target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
			select {
			case <-channel.entered:
			case <-h.ctx.Done():
				t.Fatal("delivery never started")
			}
			if rejoin {
				roomCall[MemberInfo](t, h, RemoveMember{id})
				id.MembershipID = "generation-A-new"
				target, err = h.rt.Spawn(&notificationObserver{Identity: id})
				if err != nil {
					t.Fatal(err)
				}
				roomCall[MemberInfo](t, h, AddMember{MemberIdentity: id, Target: target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
			}
			roomCall[PostReceipt](t, h, h.post(humanCaller, "newer", "private-body"))
			onceRelease(channel.release)()
			waitNotification(t, h.ctx, target, id, func(n UnreadChanged) bool { return n.UnreadCount == 1 && n.NewestUnreadSeq == 1 })
		})
	}
}

type occupyTask struct{}
type roomWithOccupiedTask struct {
	*Room
	entered chan struct{}
	release chan struct{}
}

func (r *roomWithOccupiedTask) Bindings() []actorx.Binding {
	return append(r.Room.Bindings(), actorx.HandleMessage(func(ctx actorx.Context, _ occupyTask) error {
		_, err := actorx.StartTask(ctx.WithContext(context.WithoutCancel(ctx)), func(ctx context.Context) (struct{}, error) {
			close(r.entered)
			select {
			case <-r.release:
				return struct{}{}, nil
			case <-ctx.Done():
				return struct{}{}, ctx.Err()
			}
		}, func(struct{}, error) bool { return true }, nil)
		return err
	}))
}

func TestRoomNotificationTaskAdmissionRetriesWithoutNewMutation(t *testing.T) {
	store, err := OpenStore(t.TempDir(), "design-review")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveRoom(emptyRoom()); err != nil {
		t.Fatal(err)
	}
	room, err := NewRoom(store)
	if err != nil {
		t.Fatal(err)
	}
	room.maintenanceInterval = time.Second
	rt, err := actorx.NewRuntime(context.Background(), actorx.WithTaskLimit(3))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(context.Background())
	wrapper := &roomWithOccupiedTask{Room: room, entered: make(chan struct{}), release: make(chan struct{})}
	defer onceRelease(wrapper.release)()
	addr, err := rt.Spawn(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := addr.Call[any](ctx, occupyTask{}); err != nil {
		t.Fatal(err)
	}
	<-wrapper.entered
	id := MemberIdentity{RoomIdentity: emptyRoom().RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	target, err := rt.Spawn(&notificationObserver{Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	// Maintenance + held task + commit occupy every task slot when Add's
	// completion first tries notification delivery.
	if _, err := addr.Call[MemberInfo](ctx, AddMember{MemberIdentity: id, Target: target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}}); err != nil {
		t.Fatal(err)
	}
	status, err := target.Call[MemberStatus](ctx, InspectMember{id})
	if err != nil || status.Notification != nil {
		t.Fatal("delivery unexpectedly admitted", status, err)
	}
	onceRelease(wrapper.release)()
	waitNotification(t, ctx, target, id, func(n UnreadChanged) bool { return n.Revision == 1 && n.UnreadCount == 0 })
}
