//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func notificationSettingsQuery(m *roomManager, owner RoomIdentity, target *notification.Target) url.Values {
	q := url.Values{"service_epoch": {m.epoch}, "caller_id": {"operator"}, "room_id": {owner.RoomID}, "room_instance": {owner.RoomInstance}}
	if target != nil {
		mode := target.Mode
		if mode == "" {
			mode = "room"
		}
		q.Set("target_mode", mode)
		if target.Inbox() {
			q.Set("agent_id", target.AgentID)
			q.Set("membership_id", target.MembershipID)
		}
	}
	return q
}
func saveGitLabNotificationConfigHTTP(t *testing.T, m *roomManager, owner RoomIdentity, version uint64, cfg gitlab.NotificationConfig) gitlabNotificationView {
	t.Helper()
	if cfg.Target.Mode == "" {
		cfg.Target.Mode = "room"
	}
	r := managerHTTP(m, "/v1/platform/gitlab/notifications/configure", map[string]any{"service_epoch": m.epoch, "room_id": owner.RoomID, "room_instance": owner.RoomInstance, "caller": humanCaller, "version": version, "config": cfg})
	var v gitlabNotificationView
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &v) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	return v
}
func getGitLabNotificationConfigHTTP(t *testing.T, m *roomManager, owner RoomIdentity, id string, target notification.Target) gitlabNotificationView {
	t.Helper()
	q := notificationSettingsQuery(m, owner, &target)
	q.Set("platform_id", id)
	r := managerHTTP(m, "/v1/platform/gitlab/notifications?"+q.Encode(), nil)
	var v gitlabNotificationView
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &v) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	return v
}
func stepNotificationCollector(t *testing.T, m *roomManager, key notification.Key, n int) {
	t.Helper()
	m.mu.Lock()
	runner := m.notifications.NewRunnerLocked(key)
	m.mu.Unlock()
	if runner == nil {
		t.Fatal("missing collector", key)
	}
	for range n {
		if _, stop := runner.Step(context.Background()); stop {
			t.Fatal("collector stopped", key)
		}
	}
}
func notificationSnapshotForTest(t *testing.T, m *roomManager, key notification.Key) notification.State {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.notifications.StateLocked(key)
	if !ok {
		t.Fatal("missing snapshot", key)
	}
	return s
}
func gitLabNotificationKeyFromView(owner RoomIdentity, v notificationView) notification.Key {
	return notification.Key{Owner: owner, Source: v.SourceID, SubscriptionID: v.SubscriptionID}
}

func TestMemberNotificationGitLabMembersAndRoomHTTPIndependentDeliveryAndRestart(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)
	svc := eventLiveService(t, h)
	h.m.bindPlatformReferences(svc)
	_, snapshot := readManagedRoom(t, h.m, h.room)
	targets := []notification.Target{inboxTarget(notificationMemberIdentity(h.room.RoomIdentity, snapshot.Members[0])), inboxTarget(notificationMemberIdentity(h.room.RoomIdentity, snapshot.Members[1])), {Mode: "room"}}
	views := make([]gitlabNotificationView, 3)
	for i, target := range targets {
		cfg := gitlab.DefaultNotificationConfig(c.ID)
		cfg.Target = target
		cfg.Events = gitlab.EventSettings{Enabled: true, Project: "remote/project"}
		views[i] = saveGitLabNotificationConfigHTTP(t, h.m, h.room.RoomIdentity, 0, cfg)
		if len(views[i].Errors) != 0 || views[i].Pending["events"] || views[i].Pending["todos"] {
			t.Fatal(views[i])
		}
	}
	if views[0].Events.SubscriptionID == views[1].Events.SubscriptionID || views[0].Todos.SubscriptionID == views[1].Todos.SubscriptionID {
		t.Fatal("member subscriptions merged")
	}
	h.pages.events = []gitlab.Event{h.event(1, 1)}
	h.m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(_ context.Context, _ string, _ RoomIdentity, q gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return todoFetchFunc(func(_ context.Context, query gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			batch := gitlab.TodoBatch{Version: 1, Complete: true, ResponseBytes: 200}
			if query.State == "pending" {
				batch.Todos = []gitlab.Todo{{ID: 1, RecipientID: q.Binding.UserID, ProjectID: 7, Project: "remote/project", Action: "mentioned", Reason: "mentioned", Kind: "merge_request", TargetType: "MergeRequest", TargetID: 70, TargetIID: 42, Title: "review", URL: q.Binding.URL + "/remote/project/-/merge_requests/42", CreatedAt: h.clock.get(), State: "pending"}}
			}
			return batch, nil
		}), nil
	}
	h.clock.add(5 * time.Second)
	for _, v := range views {
		for _, source := range []notificationView{v.Events, v.Todos} {
			key := gitLabNotificationKeyFromView(h.room.RoomIdentity, source)
			stepNotificationCollector(t, h.m, key, 3)
			state := notificationSnapshotForTest(t, h.m, key)
			if state.Metadata().Pending != 1 {
				t.Fatal("missing independently accepted fact", state)
			}
			if err := h.m.notifications.Deliver(context.Background(), key, state.Metadata().Version); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, member := range snapshot.Members[:2] {
		if p := inboxRoutingPreview(t, svc, notificationMemberIdentity(h.room.RoomIdentity, member)); p.SnapshotSeq != 2 {
			t.Fatal("member did not receive both channels", p)
		}
	}
	savedRoom, err := svc.store.LoadRoom()
	if err != nil || len(savedRoom.Messages) != 2 || len(savedRoom.IntegrationReceipts) != 2 {
		t.Fatal("room delivery mixed with members", savedRoom, err)
	}
	// A's pause preserves all B and Room source evidence and versions.
	before := map[notification.Key]notification.State{}
	for _, v := range views[1:] {
		for _, source := range []notificationView{v.Events, v.Todos} {
			key := gitLabNotificationKeyFromView(h.room.RoomIdentity, source)
			before[key] = notificationSnapshotForTest(t, h.m, key)
		}
	}
	cfg := views[0].Config
	cfg.Enabled = false
	views[0] = saveGitLabNotificationConfigHTTP(t, h.m, h.room.RoomIdentity, views[0].Version, cfg)
	for key, state := range before {
		if !reflect.DeepEqual(state, notificationSnapshotForTest(t, h.m, key)) {
			t.Fatal("A changed another scope", key)
		}
	}
	// All three records remain separately addressable; no ambiguous convenience record.
	q := notificationSettingsQuery(h.m, h.room.RoomIdentity, nil)
	r := managerHTTP(h.m, "/v1/platform/notifications?"+q.Encode(), nil)
	var all struct {
		GitLab    *gitlabNotificationView  `json:"gitlab"`
		Platforms []gitlabNotificationView `json:"gitlab_platforms"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &all) != nil || len(all.Platforms) != 3 || all.GitLab != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	target := targets[1]
	q = notificationSettingsQuery(h.m, h.room.RoomIdentity, &target)
	r = managerHTTP(h.m, "/v1/platform/notifications?"+q.Encode(), nil)
	if json.Unmarshal(r.Body.Bytes(), &all) != nil || len(all.Platforms) != 1 || all.Platforms[0].Config.Target != target {
		t.Fatal(r.Body.String())
	}
	// Failed application in A leaves B's independent version usable.
	cfg = views[0].Config
	cfg.Enabled = true
	cfg.Events.Project = "remote/missing"
	views[0] = saveGitLabNotificationConfigHTTP(t, h.m, h.room.RoomIdentity, views[0].Version, cfg)
	if !views[0].Pending["events"] || views[0].Errors["events"] == "" {
		t.Fatal("expected A's failed verification")
	}
	b := getGitLabNotificationConfigHTTP(t, h.m, h.room.RoomIdentity, c.ID, targets[1])
	b.Config.IntervalSeconds = 60
	views[1] = saveGitLabNotificationConfigHTTP(t, h.m, h.room.RoomIdentity, b.Version, b.Config)
	if views[1].Version != 2 || len(views[1].Errors) != 0 {
		t.Fatal("A's failure affected B", views[1])
	}
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	svc = eventLiveService(t, h)
	h.m.bindPlatformReferences(svc)
	for i, target := range targets {
		v := getGitLabNotificationConfigHTTP(t, h.m, h.room.RoomIdentity, c.ID, target)
		if v.Version != views[i].Version || !reflect.DeepEqual(v.Config, views[i].Config) || v.Events.SubscriptionID != views[i].Events.SubscriptionID || v.Todos.SubscriptionID != views[i].Todos.SubscriptionID {
			t.Fatal("restart changed receiver", i, v)
		}
	}
	for key, state := range before {
		current := notificationSnapshotForTest(t, h.m, key)
		for _, item := range current.PendingItems() {
			t.Fatal("acknowledged item replayed", item)
		}
		if current.Metadata().Key != state.Metadata().Key {
			t.Fatal("source identity changed")
		}
	}
	for _, member := range snapshot.Members[:2] {
		if inboxRoutingPreview(t, svc, notificationMemberIdentity(h.room.RoomIdentity, member)).SnapshotSeq != 2 {
			t.Fatal("Inbox receipt lost")
		}
	}
}

func TestMemberNotificationGitLabHTTPRequiresExactTargetAndVersions(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)
	_, snapshot := readManagedRoom(t, h.m, h.room)
	a, b := inboxTarget(notificationMemberIdentity(h.room.RoomIdentity, snapshot.Members[0])), inboxTarget(notificationMemberIdentity(h.room.RoomIdentity, snapshot.Members[1]))
	cfg := gitlab.DefaultNotificationConfig(c.ID)
	cfg.Target = a
	va := saveGitLabNotificationConfigHTTP(t, h.m, h.room.RoomIdentity, 0, cfg)
	cfg.Target = b
	vb := saveGitLabNotificationConfigHTTP(t, h.m, h.room.RoomIdentity, 0, cfg)
	for _, q := range []url.Values{notificationSettingsQuery(h.m, h.room.RoomIdentity, nil), notificationSettingsQuery(h.m, h.room.RoomIdentity, &a)} {
		if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications?"+q.Encode(), nil); r.Code == 200 {
			t.Fatal("missing platform/target accepted")
		}
	}
	q := notificationSettingsQuery(h.m, h.room.RoomIdentity, nil)
	q.Set("platform_id", c.ID)
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications?"+q.Encode(), nil); r.Code == 200 {
		t.Fatal("target guessed")
	}
	missing := notification.Target{Mode: "inbox", AgentID: "missing", MembershipID: "missing-membership"}
	q = notificationSettingsQuery(h.m, h.room.RoomIdentity, &missing)
	q.Set("platform_id", c.ID)
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications?"+q.Encode(), nil); r.Code != 403 {
		t.Fatal("read invented a missing member", r.Code)
	}
	q = notificationSettingsQuery(h.m, h.room.RoomIdentity, &a)
	q.Set("platform_id", strings.Repeat("f", 32))
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications?"+q.Encode(), nil); r.Code != 400 {
		t.Fatal("read invented a platform", r.Code)
	}
	request := map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller, "version": va.Version, "platform_id": c.ID}
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications/sync", request); r.Code == 200 {
		t.Fatal("sync guessed target")
	}
	request["target"] = a
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications/sync", request); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	cfg = va.Config
	cfg.Target.AgentID = "impostor"
	request["config"] = cfg
	delete(request, "target")
	request["version"] = 0
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", request); r.Code != 403 {
		t.Fatal("membership acquired second agent", r.Code, r.Body.String())
	}
	request["config"] = map[string]any{"platform_id": c.ID, "enabled": false}
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", request); r.Code != 400 {
		t.Fatal("save guessed room", r.Code)
	}
	cfg = vb.Config
	cfg.IntervalSeconds = 60
	vb = saveGitLabNotificationConfigHTTP(t, h.m, h.room.RoomIdentity, vb.Version, cfg)
	request["config"] = cfg
	request["version"] = vb.Version - 1
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", request); r.Code != 409 {
		t.Fatal("stale version accepted", r.Code)
	}
}

func TestMemberNotificationMemberRemovalCancelsQueriesRetainsEvidenceAndOtherMember(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "exit 0")
	m.bindPlatformReferences(svc)
	cfg := gitlab.DefaultNotificationConfig(c.ID)
	cfg.Target = inboxTarget(notificationMemberIdentity(room.RoomIdentity, members[0]))
	a := saveGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, 0, cfg)
	cfg.Target = inboxTarget(notificationMemberIdentity(room.RoomIdentity, members[1]))
	b := saveGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, 0, cfg)
	aKey, bKey := gitLabNotificationKeyFromView(room.RoomIdentity, a.Todos), gitLabNotificationKeyFromView(room.RoomIdentity, b.Todos)
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(_ context.Context, directory string, _ RoomIdentity, _ gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return todoFetchFunc(func(ctx context.Context, _ gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			if strings.Contains(directory, aKey.SubscriptionID) {
				calls.Add(1)
				close(entered)
				<-ctx.Done()
				close(cancelled)
				<-release
				return gitlab.TodoBatch{}, ctx.Err()
			}
			return gitlab.TodoBatch{Version: 1, Complete: true, ResponseBytes: 200}, nil
		}), nil
	}
	if err := m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	awaitEventSignal(t, entered, "A query did not start")
	removed := make(chan error, 1)
	go func() {
		_, err := svc.remove(apiRequest{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID})
		removed <- err
	}()
	awaitEventSignal(t, cancelled, "member removal did not cancel A query")
	// Waiting for A must release manager.mu and leave B configurable.
	b.Config.IntervalSeconds = 60
	b = saveGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, b.Version, b.Config)
	close(release)
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	if err := m.notifications.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := notificationSnapshotForTest(t, m, aKey)
	stepRemovedMemberCollector := func() {
		m.mu.Lock()
		runner := m.notifications.NewRunnerLocked(aKey)
		m.mu.Unlock()
		runner.Step(context.Background())
	}
	stepRemovedMemberCollector()
	if calls.Load() != 1 || !reflect.DeepEqual(before, notificationSnapshotForTest(t, m, aKey)) {
		t.Fatal("removed target queried or lost evidence")
	}
	av := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, inboxTarget(notificationMemberIdentity(room.RoomIdentity, members[0])))
	if !av.Config.Enabled || av.Todos.TargetErrorCode != "target_removed" || !av.Todos.RuntimePaused {
		t.Fatal("removed config hidden or user intent changed", av)
	}
	av.Config.Enabled = false
	av = saveGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, av.Version, av.Config)
	if av.Config.Enabled {
		t.Fatal("orphan could not be paused")
	}
	if _, err := m.configureGitLabNotifications(context.Background(), room.RoomIdentity, av.Version, a.Config); !errors.Is(err, ErrIdentity) {
		t.Fatal("orphan reopened", err)
	}
	m.mu.Lock()
	refs, err := m.managedPlatformReferencesLocked(c.ID)
	m.mu.Unlock()
	if err != nil || len(refs) != 1 {
		t.Fatal("orphan lost platform reference", refs, err)
	}
	if notificationSnapshotForTest(t, m, bKey).Metadata().Key != bKey {
		t.Fatal("B disappeared")
	}
	replacement := inboxRoutingMember(t, svc, members[0].AgentID, "replacement-membership")
	fresh := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, inboxTarget(replacement))
	if fresh.Version != 0 || fresh.Todos.SubscriptionID != "" {
		t.Fatal("replacement inherited removed membership", fresh)
	}
}

func TestMemberNotificationLarkMembersHTTPIndependentProgressAndSyncIdentity(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	states := []lark.State{}
	save := func(cfg lark.Config, key notification.Key, version uint64) lark.State {
		t.Helper()
		r := managerHTTP(h.m, "/v1/platform/notifications/configure", map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller, "provider": "lark", "channel": "messages", "subscription_id": key.SubscriptionID, "subscription_version": version, "config": cfg})
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		var v notificationView
		if json.Unmarshal(r.Body.Bytes(), &v) != nil {
			t.Fatal(r.Body.String())
		}
		return notificationSnapshotForTest(t, h.m, notification.Key{Owner: h.room.RoomIdentity, Source: lark.MessagesSourceID, SubscriptionID: v.SubscriptionID}).(lark.State)
	}
	for _, member := range h.members[:2] {
		cfg := lark.Config{Enabled: true, PlatformID: h.c.ID, Private: lark.PrivateConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_chat"}}, Target: inboxTarget(notificationMemberIdentity(h.room.RoomIdentity, member))}
		states = append(states, save(cfg, notification.Key{}, 0))
	}
	h.clock.add(3 * time.Second)
	for _, s := range states {
		stepNotificationCollector(t, h.m, s.Metadata().Key, 2)
		current := notificationSnapshotForTest(t, h.m, s.Metadata().Key)
		if current.Metadata().Pending != 1 {
			t.Fatal("member missing Lark fact", current)
		}
		if err := h.m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion); err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range h.members[:2] {
		if inboxRoutingPreview(t, h.svc, notificationMemberIdentity(h.room.RoomIdentity, member)).SnapshotSeq != 1 {
			t.Fatal("Lark members merged")
		}
	}
	a, b := notificationSnapshotForTest(t, h.m, states[0].Metadata().Key).(lark.State), notificationSnapshotForTest(t, h.m, states[1].Metadata().Key).(lark.State)
	cfg := a.Config
	cfg.Enabled = false
	a = save(cfg, a.Metadata().Key, a.SubscriptionVersion)
	if !reflect.DeepEqual(b, notificationSnapshotForTest(t, h.m, b.Metadata().Key)) {
		t.Fatal("A pause changed B")
	}
	req := map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller, "provider": "lark", "channel": "messages", "subscription_id": b.SubscriptionID, "subscription_version": b.SubscriptionVersion}
	for _, target := range []*notification.Target{nil, &a.Config.Target} {
		if target != nil {
			req["target"] = *target
		}
		if r := managerHTTP(h.m, "/v1/platform/notifications/sync", req); r.Code == 200 {
			t.Fatal("cross-member/missing sync target accepted")
		}
	}
	req["target"] = b.Config.Target
	if r := managerHTTP(h.m, "/v1/platform/notifications/sync", req); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	cfg = b.Config
	cfg.Target = notification.Target{Mode: "room"}
	req["config"] = cfg
	delete(req, "target")
	if r := managerHTTP(h.m, "/v1/platform/notifications/configure", req); r.Code == 200 {
		t.Fatal("Lark accepted room delivery")
	}
	// Reopen exactly the same source identities and per-member receipts.
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	for i, s := range states {
		current := notificationSnapshotForTest(t, h.m, s.Metadata().Key).(lark.State)
		if !current.Baseline.Equal(s.Baseline) || len(current.Inbox) != 0 || current.Config.Target != s.Config.Target {
			t.Fatal("restart mixed Lark state")
		}
		if i == 0 && current.Config.Enabled {
			t.Fatal("restart reopened A")
		}
		if inboxRoutingPreview(t, h.svc, notificationMemberIdentity(h.room.RoomIdentity, h.members[i])).SnapshotSeq != 1 {
			t.Fatal("receipt lost")
		}
	}
}

func notificationMemberIdentity(owner RoomIdentity, member MemberState) MemberIdentity {
	return MemberIdentity{RoomIdentity: owner, AgentID: member.AgentID, MembershipID: member.MembershipID}
}

func TestMemberNotificationLarkRemovedTargetPreservesBacklogAndSourceLocator(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	old := h.state()
	if len(old.Inbox) != 1 {
		t.Fatal("fixture missing backlog")
	}
	cfg := old.Config
	cfg.Target = inboxTarget(notificationMemberIdentity(h.room.RoomIdentity, h.members[1]))
	b, err := h.m.configureLarkSubscription(context.Background(), notification.Key{Owner: h.room.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	beforeB := notificationSnapshotForTest(t, h.m, b.Metadata().Key)
	member := h.members[0]
	if _, err := h.svc.remove(apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}); err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	runner := h.m.notifications.NewRunnerLocked(old.Metadata().Key)
	h.m.mu.Unlock()
	source := h.m.notifications.Source(lark.MessagesSourceID).(*lark.Source)
	originalReader := source.Reader
	reads := 0
	source.Reader = func(lark.QueryScope) (lark.MessageReader, error) { reads++; return nil, ErrRequest }
	runner.Step(context.Background())
	source.Reader = originalReader
	if reads != 0 {
		t.Fatal("removed Lark member kept querying")
	}
	if !reflect.DeepEqual(old, notificationSnapshotForTest(t, h.m, old.Metadata().Key)) || !reflect.DeepEqual(beforeB, notificationSnapshotForTest(t, h.m, b.Metadata().Key)) {
		t.Fatal("removal changed source evidence")
	}
	q := notificationSettingsQuery(h.m, h.room.RoomIdentity, nil)
	q.Set("provider", "lark")
	q.Set("channel", "messages")
	q.Set("subscription_id", old.SubscriptionID)
	r := managerHTTP(h.m, "/v1/platform/notifications?"+q.Encode(), nil)
	var all struct {
		Subscriptions []notificationView `json:"subscriptions"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &all) != nil || len(all.Subscriptions) != 1 || all.Subscriptions[0].Target != old.Config.Target || all.Subscriptions[0].TargetErrorCode != "target_removed" || all.Subscriptions[0].Pending != 1 {
		t.Fatal("source selected another member or hid orphan", r.Code, r.Body.String())
	}
	q.Set("channel", "events")
	if r := managerHTTP(h.m, "/v1/platform/notifications?"+q.Encode(), nil); r.Code != 403 {
		t.Fatal("locator guessed a source", r.Code)
	}
	q.Del("channel")
	if r := managerHTTP(h.m, "/v1/platform/notifications?"+q.Encode(), nil); r.Code != 400 {
		t.Fatal("partial locator accepted", r.Code)
	}
	cfg = old.Config
	cfg.Enabled = false
	paused, err := h.m.configureLarkSubscription(context.Background(), old.Metadata().Key, old.SubscriptionVersion, cfg)
	if err != nil || paused.Config.Enabled || !reflect.DeepEqual(old.Inbox, paused.Inbox) {
		t.Fatal("orphan pause lost backlog", err)
	}
	// Account operations retain their full platform scope, including orphaned A.
	h.m.mu.Lock()
	err = h.m.notifications.InvalidatePlatformLocked(h.c.ID)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.verifyPlatform(context.Background(), managedPlatformRequest{ID: h.c.ID, Version: h.c.Version}); err != nil {
		t.Fatal(err)
	}
	currentB := notificationSnapshotForTest(t, h.m, b.Metadata().Key).(lark.State)
	currentA := notificationSnapshotForTest(t, h.m, old.Metadata().Key).(lark.State)
	if currentB.QueryPaused() || !currentB.Config.Enabled || currentA.Config.Enabled || !reflect.DeepEqual(old.Inbox, currentA.Inbox) {
		t.Fatal("account recovery changed receiver intent/evidence")
	}
}

func TestMemberNotificationLarkRetirementWaitsOnlyForItsOwnAttachments(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	a := h.state()
	cfg := a.Config
	cfg.Target = inboxTarget(notificationMemberIdentity(h.room.RoomIdentity, h.members[1]))
	b, err := h.m.configureLarkSubscription(context.Background(), notification.Key{Owner: h.room.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve an old native-account generation, as after an explicit replacement.
	a.Binding.Generation = strings.Repeat("f", 32)
	callCtx, cancel := context.WithCancel(context.Background())
	call := &platformCall{owner: h.room.RoomIdentity, platformID: h.c.ID, subscription: a.Metadata().Key, cancel: cancel, done: make(chan struct{})}
	h.m.mu.Lock()
	err = h.m.notifications.SaveLocked(a)
	if h.m.platformCalls == nil {
		h.m.platformCalls = map[*platformCall]bool{}
	}
	h.m.platformCalls[call] = true
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	finished := false
	t.Cleanup(func() {
		if !finished {
			h.m.finishPlatformCall(call)
		}
	})
	result := make(chan error, 1)
	var replacement lark.State
	go func() {
		var e error
		replacement, e = h.m.configureLarkSubscription(context.Background(), a.Metadata().Key, a.SubscriptionVersion, a.Config)
		result <- e
	}()
	awaitEventSignal(t, callCtx.Done(), "A replacement did not cancel its saved-image call")
	h.m.mu.Lock()
	_, readErr := h.m.currentLarkForTargetLocked(h.room.RoomIdentity, h.c.ID, a.Config.Target)
	h.m.mu.Unlock()
	if !errors.Is(readErr, ErrBusy) {
		t.Fatal("A replacement ignored unsettled image call", readErr)
	}
	cfg = b.Config
	cfg.IntervalSeconds = 60
	if _, err := h.m.configureLarkSubscription(context.Background(), b.Metadata().Key, b.SubscriptionVersion, cfg); err != nil {
		t.Fatal("A image settlement blocked B", err)
	}
	h.m.finishPlatformCall(call)
	finished = true
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if replacement.SubscriptionID == a.SubscriptionID {
		t.Fatal("account replacement reused retired source")
	}
	q := notificationSettingsQuery(h.m, h.room.RoomIdentity, nil)
	q.Set("provider", "lark")
	q.Set("channel", "messages")
	q.Set("subscription_id", a.SubscriptionID)
	r := managerHTTP(h.m, "/v1/platform/notifications?"+q.Encode(), nil)
	var all struct {
		Current []notificationView `json:"subscriptions"`
		Retired []notificationView `json:"retired_subscriptions"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &all) != nil || len(all.Current) != 0 || len(all.Retired) != 1 || all.Retired[0].SubscriptionID != a.SubscriptionID || all.Retired[0].Target != a.Config.Target {
		t.Fatal("source navigation selected replacement", r.Code, r.Body.String())
	}
}

func TestMemberNotificationFailedMemberChangeResumesAfterLateQuerySettlement(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "exit 0")
	m.bindPlatformReferences(svc)
	cfg := gitlab.DefaultNotificationConfig(c.ID)
	cfg.Target = inboxTarget(notificationMemberIdentity(room.RoomIdentity, members[0]))
	v := saveGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, 0, cfg)
	entered, cancelled, release, resumed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(_ context.Context, _ string, _ RoomIdentity, _ gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return todoFetchFunc(func(ctx context.Context, _ gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			n := calls.Add(1)
			if n == 1 {
				close(entered)
				<-ctx.Done()
				close(cancelled)
				<-release
				return gitlab.TodoBatch{}, ctx.Err()
			}
			if n == 2 {
				close(resumed)
			}
			return gitlab.TodoBatch{Version: 1, Complete: true, ResponseBytes: 200}, nil
		}), nil
	}
	if err := m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	awaitEventSignal(t, entered, "collector did not start")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		finish, err := m.beginPlatformMemberChange(ctx, svc, notificationMemberIdentity(room.RoomIdentity, members[0]))
		if finish != nil {
			finish()
		}
		result <- err
	}()
	awaitEventSignal(t, cancelled, "member change did not cancel collector")
	cancel()
	if err := <-result; !errors.Is(err, ErrBusy) {
		close(release)
		t.Fatal("member change unexpectedly succeeded", err)
	}
	close(release)
	awaitEventSignal(t, resumed, "failed member change left a valid subscription stopped after settlement")
	if err := m.notifications.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	current := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, cfg.Target)
	if current.Version != v.Version || !current.Config.Enabled || current.Todos.SubscriptionID != v.Todos.SubscriptionID || current.Todos.TargetErrorCode != "" {
		t.Fatal("failed change altered membership or subscription", current)
	}
}
