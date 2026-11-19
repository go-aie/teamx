package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func memberNotificationToggleFixture(t *testing.T, provider string) (*roomManager, *service, platformMemberRequest) {
	t.Helper()
	fixture := memberToolFixture
	if provider == "gitlab" {
		fixture = func(t *testing.T) (*roomManager, *service, MemberState, platformMemberRequest) {
			return gitlabMemberFixtureFor(t, connectFixture)
		}
	}
	m, svc, _, req := fixture(t)
	m.notifications.Clock = time.Now
	req.Input = &platformMemberInput{PlatformID: req.Input.PlatformID, PlatformVersion: req.Input.PlatformVersion, Enabled: boolPointer(true)}
	return m, svc, req
}

func nextMemberNotificationToggle(t *testing.T, svc *service, req platformMemberRequest, version uint64, enabled bool) platformMemberRequest {
	t.Helper()
	id, err := newOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	source, err := svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller, ClientMessageID: id, Text: "切换通知 " + strconv.FormatBool(enabled)})
	if err != nil {
		t.Fatal(err)
	}
	req.OperationID, req.SourceSeq = id, source.Seq
	req.Input = &platformMemberInput{PlatformID: req.Input.PlatformID, PlatformVersion: req.Input.PlatformVersion, NotificationVersion: version, Enabled: boolPointer(enabled)}
	return req
}

func TestPlatformMemberNotificationToggleDefaultsAndPreservedReceipts(t *testing.T) {
	for _, provider := range []string{"lark", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			m, svc, req := memberNotificationToggleFixture(t, provider)
			now := time.Now().UTC().Truncate(time.Second)
			m.notifications.Clock = func() time.Time { return now }
			// Reading a draft and closing it must leave all authoritative state absent.
			before := gitlabMemberGet(t, m, req)
			req.Input.Enabled = boolPointer(false)
			client, endpoint := toolPipeServer(t, m)
			env := cliToolEnv(endpoint, svc, MemberState{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID})
			var out bytes.Buffer
			args := []string{"member", "notifications", "--platform-id", req.Input.PlatformID, "--platform-version", strconv.FormatUint(req.Input.PlatformVersion, 10), "--notification-version", "0", "--source-seq", strconv.FormatUint(req.SourceSeq, 10), "--operation-id", req.OperationID, "--enabled", "false"}
			if err := runPlatformConnectWithClient(t.Context(), args, &out, &out, env, client); err != nil {
				t.Fatal(err, out.String())
			}
			var op platformMemberOperation
			if err := json.Unmarshal(out.Bytes(), &op); err != nil {
				t.Fatal(err, out.String())
			}
			if op.Status != "completed" || op.Result.Status != "preserved" || op.Result.Version != 0 {
				t.Fatal(op)
			}
			absent := gitlabMemberGet(t, m, req)
			m.mu.Lock()
			states := m.notifications.StatesLocked(req.RoomIdentity)
			m.mu.Unlock()
			if !reflect.DeepEqual(before, absent) || len(states) != 0 || !absent.Baseline.IsZero() {
				t.Fatal("closing draft created configuration or reception start", absent)
			}
			if provider == "gitlab" {
				if _, err := os.Stat(m.gitlabNotificationPath(req.RoomIdentity)); !os.IsNotExist(err) {
					t.Fatal("closing draft saved desired config", err)
				}
			}
			req = nextMemberNotificationToggle(t, svc, req, 0, true)
			op = gitlabMemberApply(t, m, "notifications", req)
			v := gitlabMemberGet(t, m, req)
			if op.Status != "completed" || op.Result.Status != "applied" || v.NotificationVersion != 1 || v.ToolEnabled || v.BindingRevision != before.BindingRevision || v.Baseline != now {
				t.Fatal("wrong first enable or changed tool binding", op, v)
			}
			if provider == "lark" {
				want := m.defaultLarkConfig(req.Input.PlatformID)
				want.Target, want.BackfillStart = platformMemberTarget(req.member()), now
				want, err := lark.NormalizeConfig(want)
				if err != nil || !reflect.DeepEqual(*v.Config, want) {
					t.Fatal("first enable differs from manual defaults", v.Config, want, err)
				}
			} else {
				want := gitlab.DefaultNotificationConfig(req.Input.PlatformID)
				want.Target, want.BackfillStart = platformMemberTarget(req.member()), now
				want, err := gitlab.NormalizeNotificationConfig(want)
				if err != nil || !reflect.DeepEqual(v.GitLab.Config, want) {
					t.Fatal("first enable differs from manual defaults", v.GitLab, want, err)
				}
			}
			// A new human request for the already-applied state also preserves the
			// config version and collectors, rather than only recovering an old ID.
			m.mu.Lock()
			frozen, _ := json.Marshal(m.notifications.StatesLocked(req.RoomIdentity))
			m.mu.Unlock()
			req = nextMemberNotificationToggle(t, svc, req, v.NotificationVersion, true)
			op = gitlabMemberApply(t, m, "notifications", req)
			if op.Result.Status != "preserved" || op.Status != "completed" || op.Result.Version != v.NotificationVersion || !reflect.DeepEqual(gitlabMemberGet(t, m, req), v) {
				t.Fatal("unchanged toggle rewrote config", op)
			}
			m.mu.Lock()
			after, _ := json.Marshal(m.notifications.StatesLocked(req.RoomIdentity))
			stored, err := m.readPlatformMemberLocked(op.ID)
			m.mu.Unlock()
			if string(frozen) != string(after) || err != nil || !reflect.DeepEqual(stored, op) {
				t.Fatal("preserved operation changed collectors or failed receipt round trip", err)
			}
		})
	}
}

func TestPlatformMemberNotificationToggleRestoresSavedSources(t *testing.T) {
	for _, provider := range []string{"lark", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			m, svc, req := memberNotificationToggleFixture(t, provider)
			baseline := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
			target := platformMemberTarget(req.member())
			if provider == "lark" {
				cfg := lark.Config{Enabled: true, PlatformID: req.Input.PlatformID, Target: target, IntervalSeconds: 61, BackfillStart: baseline,
					Private: lark.PrivateConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_private"}},
					Groups:  lark.GroupConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_group"}, MessageMode: "mentions", AtAll: true}}
				if _, err := m.configureLarkSubscription(t.Context(), notification.Key{Owner: req.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg); err != nil {
					t.Fatal(err)
				}
			} else {
				m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 7, nil }
				cfg := gitlab.NotificationConfig{Enabled: true, PlatformID: req.Input.PlatformID, Target: target, IntervalSeconds: 61, BackfillStart: baseline,
					Events: gitlab.EventSettings{Enabled: true, Project: "group/project", Filter: gitlab.EventsFilter{Types: []string{"note"}}},
					Todos:  gitlab.TodoSettings{Enabled: true, Filter: gitlab.TodosFilter{Reasons: []string{"mentioned"}, Types: []string{"merge_request"}}}}
				if v, err := m.configureGitLabNotifications(t.Context(), req.RoomIdentity, 0, cfg); err != nil || len(v.Errors) != 0 {
					t.Fatal(v, err)
				}
			}
			// Seed durable collection progress, so a reset cannot pass merely by
			// retaining an empty new subscription and its baseline.
			checkpoint := baseline.Add(time.Minute)
			m.mu.Lock()
			for _, state := range m.notifications.StatesLocked(req.RoomIdentity) {
				var next notification.State
				switch s := state.(type) {
				case lark.State:
					s = s.Clone()
					s.LastCheck, s.LastFull = checkpoint, checkpoint
					for i := range s.Search.Branches {
						s.Search.Branches[i].Completed = checkpoint.Unix()
					}
					next = s
				case gitlab.EventsState:
					s = s.Clone()
					s.ProjectID = 7
					s.LastCheck, s.LastFull, s.Watermark = checkpoint, checkpoint, checkpoint
					s.IgnoreBefore = checkpoint.Add(-gitlab.EventsOverlap)
					if s.IgnoreBefore.Before(baseline) {
						s.IgnoreBefore = baseline
					}
					next = s
				case gitlab.TodosState:
					s = s.Clone()
					s.LastCheck, s.LastFull = checkpoint, checkpoint
					s.Observed[gitlab.TodosKey(s.Binding, 11)] = gitlab.TodosObservation{ID: 11, CreatedAt: checkpoint, Reason: "reason"}
					next = s
				}
				if err := m.saveNotificationLocked(next, state); err != nil {
					m.mu.Unlock()
					t.Fatal(err)
				}
			}
			m.mu.Unlock()
			before := gitlabMemberGet(t, m, req)
			for i, enabled := range []bool{false, false, true, true} {
				v := gitlabMemberGet(t, m, req)
				req = nextMemberNotificationToggle(t, svc, req, v.NotificationVersion, enabled)
				op := gitlabMemberApply(t, m, "notifications", req)
				after := gitlabMemberGet(t, m, req)
				wantStatus, wantVersion := "applied", v.NotificationVersion+1
				if i%2 == 1 {
					wantStatus, wantVersion = "preserved", v.NotificationVersion
				}
				if op.Status != "completed" || op.Result.Status != wantStatus || after.NotificationVersion != wantVersion || after.Baseline != baseline || after.ToolEnabled || after.BindingRevision != before.BindingRevision {
					t.Fatal("wrong toggle result", op, after)
				}
				m.mu.Lock()
				for _, state := range m.notifications.StatesLocked(req.RoomIdentity) {
					switch s := state.(type) {
					case lark.State:
						if s.LastCheck != checkpoint || s.LastFull != checkpoint || s.Search.Branches[0].Completed != checkpoint.Unix() {
							t.Error("Lark collection progress reset", s)
						}
					case gitlab.EventsState:
						if s.LastCheck != checkpoint || s.LastFull != checkpoint || s.Watermark != checkpoint {
							t.Error("GitLab Events progress reset", s)
						}
					case gitlab.TodosState:
						if s.LastCheck != checkpoint || s.LastFull != checkpoint || len(s.Observed) != 1 {
							t.Error("GitLab Todos progress reset", s)
						}
					}
				}
				m.mu.Unlock()
				if provider == "lark" {
					want := *before.Config
					want.Enabled = enabled
					if !reflect.DeepEqual(*after.Config, want) || before.SubscriptionID != after.SubscriptionID {
						t.Fatal("toggle replaced saved Lark scope or subscription", after)
					}
				} else {
					want := before.GitLab.Config
					want.Enabled = enabled
					if !reflect.DeepEqual(after.GitLab.Config, want) || after.GitLab.Events.SubscriptionID != before.GitLab.Events.SubscriptionID || after.GitLab.Todos.SubscriptionID != before.GitLab.Todos.SubscriptionID {
						t.Fatal("toggle replaced saved GitLab scope or subscriptions", after)
					}
				}
			}
		})
	}
}

func TestGitLabMemberNotificationToggleRequiresSourcesAndSettledApplication(t *testing.T) {
	m, _, req := memberNotificationToggleFixture(t, "gitlab")
	cfg := gitlab.NotificationConfig{PlatformID: req.Input.PlatformID, Target: platformMemberTarget(req.member())}
	v, err := m.configureGitLabNotifications(t.Context(), req.RoomIdentity, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	req.Input.NotificationVersion = v.Version
	if w := managerHTTP(m, "/v1/platforms/member/notifications", req); w.Code != 409 {
		t.Fatal("saved empty config was replaced with defaults", w.Code, w.Body.String())
	}
	cfg.Enabled = true
	if _, err := m.configureGitLabNotifications(t.Context(), req.RoomIdentity, v.Version, cfg); err == nil {
		t.Fatal("manual write enabled a config without sources")
	}
	if after := gitlabMemberGet(t, m, req); after.NotificationVersion != v.Version || after.GitLab.Config.Enabled || !after.Baseline.IsZero() {
		t.Fatal("rejected enable changed configuration", after)
	}
	for _, mutate := range []func(*gitlabNotificationView){
		func(n *gitlabNotificationView) { n.Applying = true },
		func(n *gitlabNotificationView) { n.Pending = map[string]bool{"todos": true} },
		func(n *gitlabNotificationView) { n.Errors = map[string]string{"todos": "save_failed"} },
	} {
		n := v
		n.Config.Todos.Enabled = true
		mutate(&n)
		view := platformMemberView{GitLab: &n}
		if _, err := m.gitlabMemberNotificationConfigLocked(view, req); err == nil || memberGitLabNotificationUnchanged(view, *req.Input, n.Config) {
			t.Fatal("unsettled application bypassed recovery")
		}
	}
	if _, err := os.Stat(m.platformMemberPath(req.OperationID)); !os.IsNotExist(err) {
		t.Fatal("rejected enable reserved an operation", err)
	}
}

func TestGitLabMemberCustomizationAndTotalSwitch(t *testing.T) {
	m, _, req := memberNotificationToggleFixture(t, "gitlab")
	v := gitlabMemberGet(t, m, req)
	for _, enabled := range []bool{true, false} {
		in := platformMemberInput{PlatformID: req.Input.PlatformID, PlatformVersion: req.Input.PlatformVersion, Enabled: boolPointer(enabled), EventsEnabled: boolPointer(true), ProjectID: 7, ProjectPath: "group/project"}
		if !validPlatformMemberInput("notifications", in) || !validPlatformMemberProviderInput("gitlab", "notifications", in) || validPlatformMemberProviderInput("lark", "notifications", in) {
			t.Fatal("combined GitLab fields crossed provider boundary", in)
		}
		req.Input = &in
		cfg, err := m.gitlabMemberNotificationConfigLocked(v, req)
		if err != nil || cfg.Enabled != enabled || !cfg.Events.Enabled || cfg.Events.Project != in.ProjectPath || cfg.Todos.Enabled {
			t.Fatal("first project customization inherited defaults or ignored total switch", cfg, err)
		}
	}
	// Even when a selected project is dormant, explicit total false can update
	// another source without inadvertently starting either collector.
	v.GitLab.Version = 1
	v.GitLab.Config.Enabled = false
	v.GitLab.Config.Events.Enabled, v.GitLab.Config.Events.Project = true, "group/project"
	req.Input = &platformMemberInput{Enabled: boolPointer(false), TodosEnabled: boolPointer(true)}
	cfg, err := m.gitlabMemberNotificationConfigLocked(v, req)
	if err != nil || cfg.Enabled || !cfg.Events.Enabled || !cfg.Todos.Enabled {
		t.Fatal("explicit false did not override source enable", cfg, err)
	}
}

func TestGitLabMemberNotificationTogglePreservesLegacyCollector(t *testing.T) {
	m, _, req := memberNotificationToggleFixture(t, "gitlab")
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 7, nil }
	cfg := gitlab.EventsConfig{Enabled: true, PlatformID: req.Input.PlatformID, Target: platformMemberTarget(req.member()), Project: "group/project", IntervalSeconds: 61, Filter: gitlab.EventsFilter{Types: []string{"note"}}}
	s, err := m.configureEventsSubscription(t.Context(), notification.Key{Owner: req.RoomIdentity, Source: gitlab.EventsSourceID}, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	before := gitlabMemberGet(t, m, req)
	if before.NotificationVersion != 0 || !before.GitLab.BackfillLocked || before.GitLab.Events.SubscriptionID != s.SubscriptionID {
		t.Fatal("legacy collector fixture did not establish existing intent", before)
	}
	req.Input.Enabled = boolPointer(false)
	op := gitlabMemberApply(t, m, "notifications", req)
	after := gitlabMemberGet(t, m, req)
	want := before.GitLab.Config
	want.Enabled = false
	if op.Status != "completed" || after.NotificationVersion != 1 || !reflect.DeepEqual(after.GitLab.Config, want) || after.GitLab.Todos.SubscriptionID != "" || after.GitLab.Events.SubscriptionID != s.SubscriptionID || after.Baseline != before.Baseline {
		t.Fatal("version-zero legacy collector was replaced by first-use defaults", op, after)
	}
}
