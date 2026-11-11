package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

// Real configuration/selection services and OAuth credential leases, simulated
// remote project metadata. No tool binding or Workspace repository is needed.
func gitlabProjectSelectionFixture(t *testing.T) (*roomManager, *service, MemberState, platformMemberRequest, platformSelectionRequest) {
	t.Helper()
	m, svc, member, req := gitlabMemberFixtureFor(t, connectFixture)
	original := m.platformHTTP
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/api/v4/projects") {
			body := `{"id":7,"name":"Project","path_with_namespace":"team/project","http_url_to_repo":"https://gitlab.example/team/project.git"}`
			header := http.Header{}
			if r.URL.Path == "/api/v4/projects/8" {
				body = `{"id":8,"name":"Project","path_with_namespace":"other/project"}`
			}
			if r.URL.Path == "/api/v4/projects" {
				if r.URL.Query().Get("page") == "2" {
					body = `[{"id":8,"name":"Project","path_with_namespace":"other/project"}]`
				} else {
					body = `[{"id":7,"name":"Project","path_with_namespace":"team/project"}]`
					header.Set("X-Next-Page", "2")
				}
			}
			return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}
		return original.RoundTrip(r)
	})
	// Existing project configuration service performs an independent path->ID
	// verification. Preserve that boundary while the fixture owns metadata reads.
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 7, nil }
	id, _ := newOpaqueID()
	selection := platformSelectionRequest{ServiceInfo: svc.info, Caller: req.Caller, OperationID: id, SourceSeq: req.SourceSeq, Input: &platformSelectionInput{Provider: "gitlab", Kind: "projects", PlatformID: req.Input.PlatformID, PlatformVersion: req.Input.PlatformVersion, Query: "Project"}}
	return m, svc, member, req, selection
}
func chooseGitLabProject(t *testing.T, m *roomManager, req platformSelectionRequest) platformSelectionView {
	t.Helper()
	v := selectionCall(t, m, "start", req, false)
	q := selectionQuery(req, true)
	q.Version = v.Version
	q.Draft = &platformSelectionDraft{ProjectID: 7, ProjectPath: "team/project", EventsFilter: &gitlab.EventsFilter{Types: []string{"note"}}}
	v = selectionCall(t, m, "update", q, true)
	q.Draft = nil
	q.Version = v.Version
	return selectionCall(t, m, "submit", q, true)
}
func TestGitLabProjectSelectionAndMemberCLIExactTarget(t *testing.T) {
	m, svc, member, req, selection := gitlabProjectSelectionFixture(t)
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, member)
	var out bytes.Buffer
	args := []string{"projects", "--platform-id", req.Input.PlatformID, "--platform-version", strconv.FormatUint(req.Input.PlatformVersion, 10), "--query", "Project"}
	if err := runPlatformConnectWithClient(t.Context(), args, &out, &out, env, client); err != nil {
		t.Fatal(err, out.String())
	}
	var page gitlab.ProjectPage
	if json.Unmarshal(out.Bytes(), &page) != nil || len(page.Projects) != 1 || page.NextPage != 2 {
		t.Fatal(out.String())
	}
	selectionCall(t, m, "start", selection, false)
	human := selectionQuery(selection, true)
	human.Query = "Project"
	human.PageToken = "2"
	w := managerHTTP(m, "/v1/platforms/selection-interactions/candidates", human)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Projects[0].ID != 8 || page.Projects[0].Path != "other/project" {
		t.Fatal(w.Code, w.Body.String())
	}
	receipt := chooseGitLabProject(t, m, selection)
	if receipt.Project == nil || receipt.Project.ID != 7 || receipt.Project.Path != "team/project" || receipt.Account.Provider != "gitlab" || receipt.Draft.EventsFilter.Types[0] != "note" {
		t.Fatal(receipt)
	}
	human = selectionQuery(selection, true)
	human.Version = receipt.SubmittedVersion
	if again := selectionCall(t, m, "submit", human, true); !reflect.DeepEqual(receipt, again) {
		t.Fatal("duplicate submission changed receipt")
	}
	current := gitlabMemberGet(t, m, req)
	if current.NotificationVersion != 0 || current.ToolEnabled {
		t.Fatal("form applied settings", current)
	}
	out.Reset()
	args = []string{"member", "notifications", "--platform-id", req.Input.PlatformID, "--platform-version", strconv.FormatUint(req.Input.PlatformVersion, 10), "--notification-version", "0", "--operation-id", req.OperationID, "--source-seq", strconv.FormatUint(req.SourceSeq, 10), "--events-enabled", "true", "--project-id", "7", "--project-path", "team/project", "--event-type", "note"}
	if err := runPlatformConnectWithClient(t.Context(), args, &out, &out, env, client); err != nil {
		t.Fatal(err, out.String())
	}
	var op platformMemberOperation
	if json.Unmarshal(out.Bytes(), &op) != nil || op.Status != "completed" || op.Project == nil || op.Project.ID != 7 {
		t.Fatal(out.String())
	}
	after := gitlabMemberGet(t, m, req)
	if after.MemberIdentity != req.member() || !after.GitLab.Config.Events.Enabled || after.GitLab.Config.Todos.Enabled || after.GitLab.Config.Events.Project != "team/project" || after.ToolEnabled || after.Baseline.IsZero() {
		t.Fatal(after)
	}
	for i := 0; i < 2; i++ {
		out.Reset()
		if err := runPlatformConnectWithClient(t.Context(), args, &out, &out, env, client); err != nil {
			t.Fatal(err)
		}
	}
	if got := gitlabMemberGet(t, m, req); got.NotificationVersion != after.NotificationVersion || got.Baseline != after.Baseline {
		t.Fatal("replay changed config")
	}
}

func TestGitLabProjectSelectionRejectsStaleOrUnauthorizedCandidates(t *testing.T) {
	for _, fault := range []string{"renamed", "denied", "missing", "account", "draft", "expired", "member"} {
		t.Run(fault, func(t *testing.T) {
			m, svc, member, req, selection := gitlabProjectSelectionFixture(t)
			v := selectionCall(t, m, "start", selection, false)
			q := selectionQuery(selection, true)
			q.Version = v.Version
			q.Draft = &platformSelectionDraft{ProjectID: 7, ProjectPath: "team/project"}
			v = selectionCall(t, m, "update", q, true)
			q.Draft = nil
			q.Version = v.Version
			original := m.platformHTTP
			m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/v4/projects/7" {
					switch fault {
					case "renamed":
						return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":7,"name":"Project","path_with_namespace":"renamed/project"}`))}, nil
					case "denied", "missing":
						status := 403
						if fault == "missing" {
							status = 404
						}
						return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("private remote error"))}, nil
					case "account":
						m.mu.Lock()
						file, _ := m.platforms.Load()
						file.Platforms[0].Version++
						err := m.store.save(m.platformsPathForTest(), file, 2<<20)
						m.mu.Unlock()
						if err != nil {
							t.Fatal(err)
						}
					case "draft":
						edit := selectionQuery(selection, false)
						edit.Version = v.Version
						edit.Draft = &platformSelectionDraft{ProjectID: 8, ProjectPath: "other/project"}
						selectionCall(t, m, "update", edit, false)
					case "member":
						if _, err := svc.remove(apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}); err != nil {
							t.Fatal(err)
						}
						inboxRoutingMember(t, svc, member.AgentID, "project-replacement")
					}
				}
				return original.RoundTrip(r)
			})
			if fault == "expired" {
				m.mu.Lock()
				s, _ := m.readPlatformSelectionLocked(selection.OperationID)
				s.ExpiresAt = s.CreatedAt.Add(1)
				s.CreatedAt = s.CreatedAt.Add(-1)
				m.savePlatformSelectionLocked(s)
				m.mu.Unlock()
			}
			w := managerHTTP(m, "/v1/platforms/selection-interactions/submit", q)
			if w.Code == 200 || strings.Contains(w.Body.String(), "private remote error") {
				t.Fatal(fault, w.Code, w.Body.String())
			}
			if fault == "denied" && !strings.Contains(w.Body.String(), "permission_denied") {
				t.Fatal(w.Body.String())
			}
			m.mu.Lock()
			s, err := m.readPlatformSelectionLocked(selection.OperationID)
			m.mu.Unlock()
			if err != nil || s.Status != "waiting" || s.ResultVersion != 0 {
				t.Fatal("failed selection finalized", s, err)
			}
			if _, err := os.Stat(m.platformMemberPath(req.OperationID)); !os.IsNotExist(err) {
				t.Fatal("failed selection configured member")
			}
		})
	}
}

func TestGitLabAccountSelectionSameInstanceMultipleAccounts(t *testing.T) {
	g, svc, _, connect := gitlabConnectFixture(t)
	m := g.h.m
	for i := 0; i < 2; i++ {
		if i > 0 {
			source, err := svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: connect.RoomIdentity, Caller: humanCaller, ClientMessageID: "second-account", Text: "连接另一个 GitLab 账号"})
			if err != nil {
				t.Fatal(err)
			}
			connect.SourceSeq = source.Seq
			connect.OperationID, _ = newOpaqueID()
			connect.Input.Name = "Other GitLab"
			g.user.Store(102)
		}
		op := connectHTTP(t, m, "start", connect)
		v, cookie := authorizeGitlabConnect(t, g, connect, op)
		if w := g.callback(v, cookie, nil); w.Code != 303 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	id, _ := newOpaqueID()
	req := platformSelectionRequest{ServiceInfo: svc.info, Caller: connect.Caller, OperationID: id, SourceSeq: connect.SourceSeq, Input: &platformSelectionInput{Provider: "gitlab", Kind: "account", Origin: connect.Input.Origin}}
	v := selectionCall(t, m, "start", req, false)
	q := selectionQuery(req, true)
	w := managerHTTP(m, "/v1/platforms/selection-interactions/candidates", q)
	var accounts []platformSelectionAccount
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &accounts) != nil || len(accounts) != 2 || accounts[0].Account == accounts[1].Account {
		t.Fatal(w.Code, w.Body.String())
	}
	selected := accounts[1]
	q.Version = v.Version
	q.Draft = &platformSelectionDraft{PlatformID: selected.ID, PlatformVersion: selected.Version, Name: "untrusted name"}
	v = selectionCall(t, m, "update", q, true)
	q.Draft = nil
	q.Version = v.Version
	receipt := selectionCall(t, m, "submit", q, true)
	if receipt.Account != selected.Account || receipt.Draft.Name != selected.Name || receipt.Draft.PlatformID != selected.ID || receipt.Draft.PlatformVersion != selected.Version {
		t.Fatal(receipt)
	}
	// The origin is part of the immutable choice, not an editable connection field.
	req.Input.Origin = "https://other.example"
	if w := managerHTTP(m, "/v1/platforms/selection/start", req); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestGitLabProjectSelectionAutomaticallyContinues(t *testing.T) {
	m, svc, _, _, req := gitlabProjectSelectionFixture(t)
	req.Draft = &platformSelectionDraft{ProjectID: 7, ProjectPath: "team/project"}
	platformSelectionAutomaticContinuation(t, m, svc, req)
}

func TestGitLabProjectConfigurationRejectsMismatchedIDAndPreservesSources(t *testing.T) {
	m, _, _, req, _ := gitlabProjectSelectionFixture(t)
	req.Input.TodosEnabled = nil
	req.Input.EventsEnabled = boolPointer(true)
	req.Input.ProjectID = 7
	req.Input.ProjectPath = "team/project"
	req.Input.EventsFilter = &gitlab.EventsFilter{Types: []string{"note"}}
	// A renamed project is rejected before the unknown fence or desired settings.
	req.Input.ProjectPath = "other/project"
	if w := managerHTTP(m, "/v1/platforms/member/notifications", req); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if v := gitlabMemberGet(t, m, req); v.NotificationVersion != 0 {
		t.Fatal(v)
	}
	req.Input.ProjectPath = "team/project"
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 8, nil }
	op := gitlabMemberApply(t, m, "notifications", req)
	if op.Status != "unknown" {
		t.Fatal("accepted path rebound to different native ID", op)
	}
	m.mu.Lock()
	events, err := m.currentEventsForTargetLocked(req.RoomIdentity, req.Input.PlatformID, platformMemberTarget(req.member()))
	m.mu.Unlock()
	if err != nil || events != nil {
		t.Fatal("wrong project saved", events, err)
	}
	v := gitlabMemberGet(t, m, req)
	if !v.GitLab.Pending["events"] || v.GitLab.Errors["events"] == "" || v.ToolEnabled {
		t.Fatal(v)
	}
	if again := gitlabMemberApply(t, m, "notifications", req); !reflect.DeepEqual(again, op) {
		t.Fatal("unknown resubmitted")
	}
}

func TestGitLabMemberSourceAndFilterPatches(t *testing.T) {
	m, _, _, req := gitlabMemberFixture(t)
	n := gitlabNotificationView{Version: 5, Config: gitlab.NotificationConfig{Enabled: true, PlatformID: req.Input.PlatformID, Target: platformMemberTarget(req.member()), IntervalSeconds: 61, Events: gitlab.EventSettings{Enabled: true, Project: "team/old", Filter: gitlab.EventsFilter{Types: []string{"push"}}}, Todos: gitlab.TodoSettings{Enabled: true, Filter: gitlab.TodosFilter{Reasons: []string{"assigned"}, Types: []string{"work_item"}}}}}
	v := platformMemberView{MemberIdentity: req.member(), PlatformID: req.Input.PlatformID, GitLab: &n}
	in := *req.Input
	in.TodosEnabled = nil
	in.EventsEnabled = boolPointer(true)
	in.ProjectID = 7
	in.ProjectPath = "team/project"
	in.EventsFilter = &gitlab.EventsFilter{Types: []string{"note"}}
	in.TodosFilter = &gitlab.TodosFilter{Reasons: []string{"mentioned"}}
	req.Input = &in
	m.mu.Lock()
	cfg, err := m.gitlabMemberNotificationConfigLocked(v, req)
	m.mu.Unlock()
	if err != nil || cfg.Events.Project != "team/project" || cfg.Events.Filter.Types[0] != "note" || !cfg.Todos.Enabled || cfg.Todos.Filter.Reasons[0] != "mentioned" || cfg.Todos.Filter.Types[0] != "work_item" || cfg.IntervalSeconds != 61 {
		t.Fatal(cfg, err)
	}
	n.Config.Enabled = false
	if _, err = m.gitlabMemberNotificationConfigLocked(v, req); err == nil {
		t.Fatal("implicitly enabled dormant todos")
	}
	in.TodosEnabled = boolPointer(false)
	if cfg, err = m.gitlabMemberNotificationConfigLocked(v, req); err != nil || cfg.Todos.Enabled || !cfg.Events.Enabled || !cfg.Enabled {
		t.Fatal(cfg, err)
	}
}

func TestGitLabProjectSelectionReceiptSurvivesRestart(t *testing.T) {
	m, _, _, _, req := gitlabProjectSelectionFixture(t)
	saved := chooseGitLabProject(t, m, req)
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, m.store.dir, nil)
	svc := eventLiveService(t, &eventHarness{m: reopened, room: roomSummary{RoomIdentity: req.RoomIdentity}})
	q := selectionQuery(req, false)
	q.ServiceInfo = svc.info
	got := selectionCall(t, reopened, "status", q, false)
	if !reflect.DeepEqual(got.platformSelection, saved.platformSelection) {
		t.Fatal("restart changed exact project receipt", got)
	}
}
