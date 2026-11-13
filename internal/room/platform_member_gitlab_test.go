package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func gitlabMemberFixture(t *testing.T) (*roomManager, *service, MemberState, platformMemberRequest) {
	t.Helper()
	return gitlabMemberFixtureFor(t, assistantConnectFixture)
}

func gitlabMemberFixtureFor(t *testing.T, fixture platformConnectionFixture) (*roomManager, *service, MemberState, platformMemberRequest) {
	t.Helper()
	g, svc, member, connect := gitlabConnectFixtureFor(t, fixture)
	m := g.h.m
	op := connectHTTP(t, m, "start", connect)
	v, cookie := authorizeGitlabConnect(t, g, connect, op)
	if w := g.callback(v, cookie, nil); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	op = connectHTTP(t, m, "status", connectObservation(connect))
	id, _ := newOpaqueID()
	enabled := true
	return m, svc, member, platformMemberRequest{ServiceInfo: svc.info, Caller: connect.Caller, OperationID: id, SourceSeq: connect.SourceSeq, Input: &platformMemberInput{PlatformID: op.PlatformID, PlatformVersion: op.Connection.Version, TodosEnabled: &enabled}}
}

func gitlabMemberGet(t *testing.T, m *roomManager, req platformMemberRequest) platformMemberView {
	t.Helper()
	req.OperationID, req.SourceSeq = "", 0
	req.Input = &platformMemberInput{PlatformID: req.Input.PlatformID, PlatformVersion: req.Input.PlatformVersion}
	w := managerHTTP(m, "/v1/platforms/member/get", req)
	var v platformMemberView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{"candidate-access", "candidate-refresh", "private-client-secret", "fingerprint"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("member view leaked credentials", secret)
		}
	}
	return v
}

func gitlabMemberApply(t *testing.T, m *roomManager, kind string, req platformMemberRequest) platformMemberOperation {
	t.Helper()
	w := managerHTTP(m, "/v1/platforms/member/"+kind, req)
	var op platformMemberOperation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &op) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return op
}

func TestGitLabMemberPersonalTodosWithoutToolsOrWorkspace(t *testing.T) {
	m, svc, member, req := gitlabMemberFixture(t)
	before := gitlabMemberGet(t, m, req)
	if before.ToolEnabled || before.ToolRevision != 0 || before.NotificationVersion != 0 || before.GitLab == nil {
		t.Fatal(before)
	}
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, member)
	args := []string{"member", "notifications", "--platform-id", req.Input.PlatformID, "--platform-version", strconv.FormatUint(req.Input.PlatformVersion, 10), "--notification-version", "0", "--operation-id", req.OperationID, "--source-seq", strconv.FormatUint(req.SourceSeq, 10), "--todos-enabled", "true"}
	var out bytes.Buffer
	if err := runPlatformConnectWithClient(context.Background(), args, &out, &out, env, client); err != nil {
		t.Fatal(err, out.String())
	}
	var op platformMemberOperation
	if json.Unmarshal(out.Bytes(), &op) != nil || op.Status != "completed" || op.Result.Status != "applied" || op.Result.Version != 1 || op.Config != nil || op.GitLabConfig == nil {
		t.Fatal(out.String())
	}
	v := gitlabMemberGet(t, m, req)
	cfg := v.GitLab.Config
	if v.MemberIdentity != req.member() || cfg.Target != platformMemberTarget(req.member()) || !cfg.Enabled || !cfg.Todos.Enabled || cfg.Events.Enabled || cfg.Events.Project != "" || v.ToolEnabled || v.ToolRevision != 0 || v.NotificationVersion != 1 || v.Baseline.IsZero() || !reflect.DeepEqual(cfg, *op.GitLabConfig) || len(v.GitLab.Errors) != 0 || v.GitLab.Pending["todos"] || v.GitLab.Pending["events"] {
		t.Fatal("wrong opt-in/target", v)
	}
	// Synchronous returns, active observations and late result handling reuse the
	// same receipt, without another config revision or reception baseline.
	for i := 0; i < 2; i++ {
		if replay := gitlabMemberApply(t, m, "notifications", req); !reflect.DeepEqual(replay, op) {
			t.Fatal("configuration replay changed receipt")
		}
	}
	if after := gitlabMemberGet(t, m, req); after.NotificationVersion != 1 || after.Baseline != v.Baseline {
		t.Fatal("replay reset version/start", after)
	}
	bad := req
	bad.OperationID, _ = newOpaqueID()
	if w := managerHTTP(m, "/v1/platforms/member/notifications", bad); w.Code != 409 {
		t.Fatal("new-ID replay allowed", w.Code, w.Body.String())
	}
	for _, mutate := range []func(*platformMemberInput){
		func(in *platformMemberInput) { in.PlatformVersion++ },
		func(in *platformMemberInput) { in.TodosEnabled = nil; in.Enabled = boolPointer(true) },
		func(in *platformMemberInput) { in.AddChatIDs = []string{"oc_wrong"} },
	} {
		bad := req
		in := *req.Input
		bad.Input = &in
		mutate(&in)
		if w := managerHTTP(m, "/v1/platforms/member/notifications", bad); w.Code == 200 {
			t.Fatal("changed or cross-provider input accepted")
		}
	}
	// Agent context cannot call the original human configuration endpoint.
	if w := managerHTTP(m, "/v1/platform/gitlab/notifications/configure", map[string]any{"service_epoch": m.epoch, "room_id": req.RoomID, "room_instance": req.RoomInstance, "caller": req.Caller, "version": 1, "config": cfg}); w.Code != 403 {
		t.Fatal("human endpoint relaxed", w.Code)
	}
}

func boolPointer(v bool) *bool { return &v }

func TestGitLabMemberPatchPreservesOtherSourcesFiltersAndStart(t *testing.T) {
	m, _, _, req := gitlabMemberFixture(t)
	m.mu.Lock()
	n, err := m.gitlabNotificationViewForTargetLocked(req.RoomIdentity, req.Input.PlatformID, platformMemberTarget(req.member()))
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	n.Version = 7
	n.Config.Enabled, n.Config.Todos.Enabled = true, false
	n.Config.Events.Enabled, n.Config.Events.Project = true, "group/project"
	n.Config.Events.Filter.Types = []string{"note"}
	n.Config.Todos.Filter = gitlab.TodosFilter{Reasons: []string{"mentioned"}, Types: []string{"merge_request"}}
	n.Config.BackfillStart, n.Config.IntervalSeconds = time.Unix(1000, 0).UTC(), 61
	v := platformMemberView{MemberIdentity: req.member(), PlatformID: req.Input.PlatformID, GitLab: &n}
	before, _ := json.Marshal(n.Config)
	m.mu.Lock()
	cfg, err := m.gitlabMemberNotificationConfigLocked(v, req)
	m.mu.Unlock()
	if err != nil || !cfg.Todos.Enabled || !reflect.DeepEqual(cfg.Events, n.Config.Events) || !reflect.DeepEqual(cfg.Todos.Filter, n.Config.Todos.Filter) || cfg.BackfillStart != n.Config.BackfillStart || cfg.IntervalSeconds != 61 {
		t.Fatal("patch replaced existing settings", cfg, err)
	}
	after, _ := json.Marshal(n.Config)
	if !bytes.Equal(before, after) {
		t.Fatal("patch mutated input")
	}
	n.Config.Enabled = false
	m.mu.Lock()
	_, err = m.gitlabMemberNotificationConfigLocked(v, req)
	m.mu.Unlock()
	if !errors.Is(err, ErrConflict) {
		t.Fatal("enabled dormant project activity", err)
	}
	// Disabling todos leaves the global switch and project activity untouched.
	req.Input.TodosEnabled = boolPointer(false)
	m.mu.Lock()
	cfg, err = m.gitlabMemberNotificationConfigLocked(v, req)
	m.mu.Unlock()
	if err != nil || cfg.Enabled || cfg.Todos.Enabled || !cfg.Events.Enabled {
		t.Fatal(cfg, err)
	}
}

func TestGitLabMemberUnknownSubmissionRestartNeverReplays(t *testing.T) {
	m, _, _, req := gitlabMemberFixture(t)
	real := m.store.ops
	m.mu.Lock()
	m.store.ops.rename = func(from, to string) error {
		if to == m.platformMemberPath(req.OperationID) {
			var op platformMemberOperation
			data, _ := os.ReadFile(from)
			if json.Unmarshal(data, &op) == nil && op.Status == "completed" {
				return os.ErrPermission
			}
		}
		return real.rename(from, to)
	}
	m.mu.Unlock()
	if w := managerHTTP(m, "/v1/platforms/member/notifications", req); w.Code == 200 {
		t.Fatal("unknown receipt reported success")
	}
	m.mu.Lock()
	m.store.ops = real
	m.mu.Unlock()
	before, _ := os.ReadFile(m.gitlabNotificationPath(req.RoomIdentity))
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, m.store.dir, nil)
	svc := eventLiveService(t, &eventHarness{m: reopened, room: roomSummary{RoomIdentity: req.RoomIdentity}})
	req.ServiceInfo = svc.info
	if op := gitlabMemberApply(t, reopened, "notifications", req); op.Status != "unknown" {
		t.Fatal(op)
	}
	after, _ := os.ReadFile(reopened.gitlabNotificationPath(req.RoomIdentity))
	if !bytes.Equal(before, after) {
		t.Fatal("unknown replay changed authoritative settings")
	}
}

func TestGitLabMemberVersionsAndReplacementAreRejected(t *testing.T) {
	for _, field := range []string{"platform", "notifications", "tool", "member"} {
		t.Run(field, func(t *testing.T) {
			m, svc, member, req := gitlabMemberFixtureFor(t, connectFixture)
			action := "notifications"
			switch field {
			case "platform":
				req.Input.PlatformVersion++
			case "notifications":
				req.Input.NotificationVersion++
			case "tool":
				action = "tool"
				req.Input.TodosEnabled, req.Input.Enabled, req.Input.ToolRevision = nil, boolPointer(true), 1
			case "member":
				if _, err := svc.remove(apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}); err != nil {
					t.Fatal(err)
				}
				inboxRoutingMember(t, svc, member.AgentID, "replacement")
			}
			w := managerHTTP(m, "/v1/platforms/member/"+action, req)
			if w.Code == 200 {
				t.Fatal("stale identity/version accepted", w.Body.String())
			}
			if _, err := os.Stat(m.platformMemberPath(req.OperationID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected request persisted a submission", err)
			}
		})
	}
}

func TestGitLabMemberPartialNotificationsAndIndependentTool(t *testing.T) {
	m, svc, _, req := gitlabMemberFixture(t)
	real := m.store.ops
	m.mu.Lock()
	m.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		// Fail before replacement, so the service stays writable and can record
		// the collector error separately from the saved desired configuration.
		if strings.HasSuffix(dir, "/notifications") {
			return nil, os.ErrPermission
		}
		return real.createTemp(dir, pattern)
	}
	m.mu.Unlock()
	op := gitlabMemberApply(t, m, "notifications", req)
	m.mu.Lock()
	m.store.ops = real
	m.mu.Unlock()
	if op.Status != "unknown" || op.ResultVersion != 0 {
		t.Fatal("partial notification apply reported complete", op)
	}
	v := gitlabMemberGet(t, m, req)
	if v.NotificationVersion != 1 || !v.GitLab.Config.Todos.Enabled || !v.GitLab.Pending["todos"] || v.GitLab.Errors["todos"] == "" || v.ToolEnabled {
		t.Fatal(v)
	}
	toolReq := req
	in := *req.Input
	toolReq.Input, toolReq.OperationID = &in, strings.Repeat("a", 32)
	in.TodosEnabled, in.Enabled = nil, boolPointer(true)
	toolOp := gitlabMemberApply(t, m, "tool", toolReq)
	if toolOp.Status != "completed" {
		t.Fatal("notification failure blocked independent tools", toolOp)
	}
	v = gitlabMemberGet(t, m, req)
	if !v.ToolEnabled || v.Tool == nil || v.Tool.ActionAuthorization != nil || v.NotificationVersion != 1 {
		t.Fatal("new binding changed notifications or granted writes", v)
	}
	// Repeated enable under another human request preserves saved grants.
	m.mu.Lock()
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err == nil {
		f.Bindings[0].ActionAuthorization = &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: "comment", TargetKind: "object", ObjectType: "gitlab.merge_request"}}}
		err = m.store.save(m.toolBindingsPath(req.RoomIdentity), f, 1<<20)
	}
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(m.toolBindingsPath(req.RoomIdentity))
	source, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller, ClientMessageID: "enable-again", Text: "启用你的 GitLab 工具"})
	if err != nil {
		t.Fatal(err)
	}
	toolReq.SourceSeq, toolReq.OperationID, in.ToolRevision = source.Seq, strings.Repeat("b", 32), v.ToolRevision
	if again := gitlabMemberApply(t, m, "tool", toolReq); again.Status != "completed" || again.Result.Status != "preserved" {
		t.Fatal(again)
	}
	after, _ := os.ReadFile(m.toolBindingsPath(req.RoomIdentity))
	if !bytes.Equal(before, after) {
		t.Fatal("repeated tool enable changed grants")
	}
}

func TestGitLabMemberExistingSubscriptionsPreserveProgressAndSibling(t *testing.T) {
	m, _, _, req := gitlabMemberFixture(t)
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 7, nil }
	cfg := gitlab.NotificationConfig{Enabled: true, PlatformID: req.Input.PlatformID, Target: platformMemberTarget(req.member()), IntervalSeconds: 61,
		BackfillStart: time.Now().Add(-time.Hour).UTC().Truncate(time.Second),
		Events:        gitlab.EventSettings{Enabled: true, Project: "group/project", Filter: gitlab.EventsFilter{Types: []string{"note"}}},
		Todos:         gitlab.TodoSettings{Filter: gitlab.TodosFilter{Reasons: []string{"mentioned"}, Types: []string{"merge_request"}}}}
	before, err := m.configureGitLabNotifications(context.Background(), req.RoomIdentity, 0, cfg)
	if err != nil || len(before.Errors) != 0 {
		t.Fatal(before, err)
	}
	siblingCfg := cfg
	siblingCfg.Target, siblingCfg.Events.Enabled, siblingCfg.Todos.Enabled = notification.Target{}, false, true
	sibling, err := m.configureGitLabNotifications(context.Background(), req.RoomIdentity, 0, siblingCfg)
	if err != nil || len(sibling.Errors) != 0 {
		t.Fatal(sibling, err)
	}
	req.Input.NotificationVersion = before.Version
	op := gitlabMemberApply(t, m, "notifications", req)
	after := gitlabMemberGet(t, m, req)
	if op.Status != "completed" || !after.GitLab.Config.Todos.Enabled || !reflect.DeepEqual(after.GitLab.Config.Events, before.Config.Events) || !reflect.DeepEqual(after.GitLab.Config.Todos.Filter, before.Config.Todos.Filter) || after.GitLab.Config.BackfillStart != before.Config.BackfillStart || after.GitLab.Events.SubscriptionID != before.Events.SubscriptionID || after.GitLab.Events.Version != before.Events.Version {
		t.Fatal("lost project progress/filter/baseline", after)
	}
	m.mu.Lock()
	other, err := m.gitlabNotificationViewForTargetLocked(req.RoomIdentity, req.Input.PlatformID, notification.Target{})
	m.mu.Unlock()
	if err != nil || other.Version != sibling.Version || !reflect.DeepEqual(other.Config, sibling.Config) || other.Todos.SubscriptionID != sibling.Todos.SubscriptionID || other.Todos.Version != sibling.Todos.Version {
		t.Fatal("changed sibling subscription", other, err)
	}
}

func TestDefaultAssistantGitLabConnectAutomaticallyConfiguresPersonalTodos(t *testing.T) {
	g, svc, _, req := gitlabConnectFixtureFor(t, assistantConnectFixture)
	m := g.h.m
	op := connectHTTP(t, m, "start", req)
	v, cookie := authorizeGitlabConnect(t, g, req, op)
	b := newControlledBackend()
	id := op.MemberIdentity
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	member, err := spawnScheduledMember(svc.rt, &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Backend: b, Store: svc.store, Room: svc.room, Inbox: func(ctx context.Context) (InboxStatus, error) { return svc.memberInboxStatus(ctx, id) }}, svc.scheduler)
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(id.MembershipID, member)
	if _, err = member.Call[MemberStatus](ctx, ControlMember{MemberIdentity: id, Action: "run", State: UnreadChanged{MemberIdentity: id}}); err != nil {
		t.Fatal(err)
	}
	query, mode := connectObservation(req), "inbox"
	query.Notify = &mode
	connectHTTP(t, m, "notify", query)
	if w := g.callback(v, cookie, nil); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case input := <-b.entered:
		if input.Inbox == nil || input.Inbox.SnapshotSeq != 1 || input.LatestSeq != req.SourceSeq {
			t.Fatal(input)
		}
	case <-ctx.Done():
		t.Fatal("no automatic continuation")
	}
	done := connectHTTP(t, m, "status", connectObservation(req))
	stepID, _ := newOpaqueID()
	step := platformMemberRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, SourceSeq: req.SourceSeq, OperationID: stepID, Input: &platformMemberInput{PlatformID: done.PlatformID, PlatformVersion: done.Connection.Version, TodosEnabled: boolPointer(true)}}
	if result := gitlabMemberApply(t, m, "notifications", step); result.Status != "completed" {
		t.Fatal(result)
	}
	actual := gitlabMemberGet(t, m, step)
	if actual.MemberIdentity != id || !actual.GitLab.Config.Todos.Enabled || actual.GitLab.Config.Events.Enabled || actual.ToolEnabled {
		t.Fatal(actual)
	}
	page := consumeConnectTestInbox(t, svc, id, 1)
	if len(page.Entries) != 1 || page.Entries[0].TaskEvent.OperationID != op.ID {
		t.Fatal(page)
	}
	b.finish <- nil
	waitMember(t, ctx, member, id, func(s MemberStatus) bool { return !s.Active && s.Inbox != nil && s.Inbox.ConsumedThrough == 1 })
	snapshot, err := svc.store.LoadRoom()
	if err != nil || len(snapshot.Messages) != 1 {
		t.Fatal("continuation needed another chat", err)
	}
}

func TestGitLabMemberDormantProjectActivityRequiresExplicitChoice(t *testing.T) {
	m, _, _, req := gitlabMemberFixture(t)
	cfg := gitlab.NotificationConfig{PlatformID: req.Input.PlatformID, Target: platformMemberTarget(req.member()), IntervalSeconds: 10,
		Events: gitlab.EventSettings{Enabled: true, Project: "group/project"}}
	v, err := m.configureGitLabNotifications(context.Background(), req.RoomIdentity, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	req.Input.NotificationVersion = v.Version
	before, _ := os.ReadFile(m.gitlabNotificationPath(req.RoomIdentity))
	w := managerHTTP(m, "/v1/platforms/member/notifications", req)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "notifications.enabled") {
		t.Fatal("silently expanded reception", w.Code, w.Body.String())
	}
	after, _ := os.ReadFile(m.gitlabNotificationPath(req.RoomIdentity))
	if !bytes.Equal(before, after) {
		t.Fatal("conflict changed existing settings")
	}
	if _, err := os.Stat(m.platformMemberPath(req.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("conflict persisted submission", err)
	}
}

func TestGitLabMemberAccountChangeAfterDesiredSaveStopsCollectorWrite(t *testing.T) {
	m, _, _, req := gitlabMemberFixture(t)
	real := m.store.ops
	m.mu.Lock()
	m.store.ops.rename = func(from, to string) error {
		if err := real.rename(from, to); err != nil {
			return err
		}
		if to == m.gitlabNotificationPath(req.RoomIdentity) {
			// Reproduce a changed account revision at the durable desired-config
			// boundary. No collector may use the older admission after that point.
			file, err := m.platforms.Load()
			if err != nil {
				return err
			}
			file.Platforms[0].Version++
			return m.store.save(m.platformsPathForTest(), file, 2<<20)
		}
		return nil
	}
	m.mu.Unlock()
	op := gitlabMemberApply(t, m, "notifications", req)
	m.mu.Lock()
	m.store.ops = real
	todos, err := m.currentTodosForTargetLocked(req.RoomIdentity, req.Input.PlatformID, platformMemberTarget(req.member()))
	m.mu.Unlock()
	if op.Status != "unknown" || todos != nil || err != nil {
		t.Fatal("wrote collector after changed account", op, err)
	}
	if replay := gitlabMemberApply(t, m, "notifications", req); !reflect.DeepEqual(replay, op) {
		t.Fatal("unknown admission replayed")
	}
	req.Input.PlatformVersion++
	v := gitlabMemberGet(t, m, req)
	if v.NotificationVersion != 1 || !v.GitLab.Pending["todos"] || !v.GitLab.Config.Todos.Enabled {
		t.Fatal("lost durable partial effect", v)
	}
}
