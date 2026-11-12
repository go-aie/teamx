//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

type conversationGitLabSettingsInput struct {
	Config        string `json:"config"`
	Origin        string `json:"origin"`
	OAuthSnapshot string `json:"oauth_snapshot"`
	ConnectionID  string `json:"connection_id"`
	ProjectID     int64  `json:"project_id"`
	ProjectPath   string `json:"project_path"`
}

// Real model/CLI and durable source application. Real-provider mode accepts an
// explicitly selected, still-valid access grant and denies refresh/revoke/write.
func TestConversationGitLabSettingsRealDSH(t *testing.T) {
	mode := os.Getenv("TEAMX_SETTINGS_GITLAB_DSH")
	if mode != "fixture" && mode != "gitlab" {
		t.Skip("explicit model/fixture opt-in, runtime and fresh output required")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "incomplete", "provider": "GitLab contract fixture", "business_writes": 0, "collection": "not started", "delivery": "not tested", "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, out, report)
	input := conversationGitLabSettingsInput{Config: os.Getenv("TEAMX_SETTINGS_CONFIG"), ProjectID: 7, ProjectPath: "team/project"}
	if mode == "gitlab" {
		input = conversationGitLabSettingsInput{}
		if jsonFile(os.Getenv("TEAMX_SETTINGS_GITLAB_INPUT"), &input) != nil || !filepath.IsAbs(input.Config) || !filepath.IsAbs(input.OAuthSnapshot) || input.ConnectionID == "" || input.ProjectID <= 0 || !workspaceProjectPath(input.ProjectPath) {
			t.Fatal("explicit config, private OAuth snapshot, connection and native project ID/path required")
		}
	}
	cfg, err := loadStartupConfig(input.Config, true)
	if err != nil || cfg.Runtime == nil {
		t.Fatal("current runtime required", err)
	}
	runtime := cfg.Runtime
	if key := runtime.Model.APIKeyEnv; os.Getenv(key) == "" && cfg.Env[key] != "" {
		t.Setenv(key, cfg.Env[key])
	}
	imageRealCredential(t, runtime.Model.APIKeyEnv)
	report["model"] = runtime.Model.ModelID
	var m *roomManager
	var target platformMemberInput
	if mode == "fixture" {
		var memberReq platformMemberRequest
		m, _, _, memberReq, _ = gitlabProjectSelectionFixture(t)
		target = *memberReq.Input
	} else {
		t.Setenv("TEAMX_REAL_OAUTH_SNAPSHOT", input.OAuthSnapshot)
		t.Setenv("TEAMX_REAL_OAUTH_CONNECTION", input.ConnectionID)
		file, c := selectedRealOAuthSnapshot(t, input.Origin)
		if !file.OAuthCredentials[0].ExpiresAt.After(time.Now().Add(7 * time.Minute)) {
			t.Fatal("selected access grant needs at least 7 minutes remaining for the bounded six-minute run; refresh is disabled")
		}
		m = testManager(t, t.TempDir(), managerTestProfiles(t))
		m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Scheme+"://"+r.URL.Host != input.Origin {
				return nil, platform.ErrCredential
			}
			return http.DefaultTransport.RoundTrip(r)
		})
		realOAuthPlatform(t, m, input.Origin)
		target.PlatformID, target.PlatformVersion = c.ID, c.Version
		report["provider"] = "real GitLab; existing access grant; GET only"
	}
	// The OAuth fixture uses a historical notification clock. This semantic run
	// does not collect events and establishes its own current reception baseline.
	m.notifications.Clock = time.Now
	r := managerCreate(t, m, "gitlab-settings-acceptance")
	workspace := filepath.Join(out, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	p := dshProfile{BinaryPath: runtime.DSH.BinaryPath, BinaryArgs: runtime.DSH.BinaryArgs, Artifact: runtime.DSH.Artifact, Workspace: workspace, Model: runtime.Model.Clone(), Instructions: `You are a TeamX acceptance assistant. Read the platforms skill and CLI help, then act on actual human Room requests using member configuration commands. Read current versions, use actual human source_seq, one stable operation per request, inspect the original receipt and authoritative configuration. Reply through Room send; normal output is private. Only use the configured TeamX CLI. Never access files, environment, credentials, native tools/network, external actions, policy/tool/member management or delegation. Finish the current request without polling for another human message. Stop on unknown submission, never replace its ID.`}
	audit := &policyRealAudit{Calls: map[string]int{}}
	server := httptest.NewServer(audit.serve(m))
	defer server.Close()
	m.config.endpoint = server.URL
	svc, err := newService(m.roomDir(r.RoomID), r.RoomID, serviceConfig{endpoint: server.URL, cliPath: productionFixtureCLI(t), maxTurns: 8, persistentScheduling: true, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": p}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	m.bindPlatformReferences(svc)
	observePolicyReal(svc, audit)
	m.mu.Lock()
	m.runtimes[r.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	m.mu.Unlock()
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	req := platformMemberRequest{ServiceInfo: svc.info, Caller: agentCaller(id.AgentID, id.MembershipID), Input: &platformMemberInput{PlatformID: target.PlatformID, PlatformVersion: target.PlatformVersion}}
	initial, err := m.configureGitLabNotifications(t.Context(), r.RoomIdentity, 0, gitlab.NotificationConfig{
		Enabled: true, PlatformID: req.Input.PlatformID, Target: platformMemberTarget(id), IntervalSeconds: 61,
		BackfillStart: time.Now().Add(-time.Minute).UTC().Truncate(time.Second),
		Todos:         gitlab.TodoSettings{Enabled: true, Filter: gitlab.TodosFilter{Reasons: []string{"mentioned"}, Types: []string{"work_item"}}},
		Events:        gitlab.EventSettings{Enabled: true, Project: input.ProjectPath, Filter: gitlab.EventsFilter{Types: []string{"note"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var evidence []map[string]any
	defer func() {
		state, _ := svc.store.LoadRoom()
		report["member"], report["messages"], report["cases"], report["audit"] = id, state.Messages, evidence, audit
	}()
	runBudget := 12 * time.Minute
	if mode == "gitlab" {
		runBudget = 6 * time.Minute
	}
	ctx, cancel := context.WithTimeout(t.Context(), runBudget)
	defer cancel()
	turn := uint64(0)
	post := func(name, text string, check func(gitlab.NotificationConfig, gitlab.NotificationConfig) bool) {
		t.Helper()
		t.Log("real model case:", name)
		report["phase"] = name
		before := gitlabMemberGet(t, m, req)
		source, e := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: name, Text: fmt.Sprintf("当前助理，GitLab 平台 %s。%s", req.Input.PlatformID, text)})
		if e != nil {
			t.Fatal(e)
		}
		if turn == 0 {
			if _, e = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); e != nil {
				t.Fatal(e)
			}
		}
		turn++
		status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && (s.Agent.TurnNumber >= turn || s.Paused) })
		if status.Error != "" || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" || status.Agent.TurnNumber != turn {
			t.Fatal("real model turn did not complete", status.Error)
		}
		after := gitlabMemberGet(t, m, req)
		query := req
		query.SourceSeq = source.Seq
		m.mu.Lock()
		ops, e := m.platformMemberStatusLocked(query)
		selections, se := m.platformSelectionsLocked()
		m.mu.Unlock()
		if e != nil || se != nil || len(ops) != 1 || ops[0].Status != "completed" || ops[0].Result.Status != "applied" || ops[0].MemberIdentity != id || ops[0].Account != after.Account || ops[0].Source.Seq != source.Seq || ops[0].Result.Version != before.NotificationVersion+1 || after.NotificationVersion != ops[0].Result.Version || !reflect.DeepEqual(ops[0].GitLabConfig, &after.GitLab.Config) {
			t.Fatal("GitLab request/receipt/authoritative configuration mismatch", e, se)
		}
		if !check(before.GitLab.Config, after.GitLab.Config) || after.Baseline != initial.Config.BackfillStart || after.GitLab.Config.IntervalSeconds != 61 || after.ToolRevision != before.ToolRevision || after.ToolEnabled != before.ToolEnabled || len(selections) != 0 || len(after.GitLab.Errors) != 0 || after.GitLab.Pending["events"] || after.GitLab.Pending["todos"] {
			t.Fatal("GitLab change violated requested fields or source application boundary")
		}
		op := ops[0]
		replay := req
		replay.OperationID, replay.SourceSeq, replay.Input = op.ID, source.Seq, &op.Input
		got, e := m.configurePlatformMember(ctx, "notifications", replay)
		if e != nil || !reflect.DeepEqual(op, got) || !reflect.DeepEqual(after, gitlabMemberGet(t, m, req)) {
			t.Fatal("GitLab receipt replay changed configuration", e)
		}
		evidence = append(evidence, map[string]any{"case": name, "source_seq": source.Seq, "before": before, "receipt": op, "after": after})
	}
	post("todos-filter", "待办只保留分配给我或请求我审核的合并请求。项目活动设置保持原样，直接保存。", func(before, after gitlab.NotificationConfig) bool {
		return reflect.DeepEqual(after.Todos.Filter.Reasons, []string{"assigned", "review_requested"}) && reflect.DeepEqual(after.Todos.Filter.Types, []string{"merge_request"}) && reflect.DeepEqual(before.Events, after.Events)
	})
	post("events-filter", fmt.Sprintf("项目活动使用原生项目 ID %d、完整路径 %s，只接收评论和推送事件。待办配置保持原样，直接保存。", input.ProjectID, input.ProjectPath), func(before, after gitlab.NotificationConfig) bool {
		return after.Events.Enabled && after.Events.Project == input.ProjectPath && reflect.DeepEqual(after.Events.Filter.Types, []string{"note", "push"}) && reflect.DeepEqual(before.Todos, after.Todos)
	})
	post("empty-todos-reasons", "将待办原因筛选列表明确清空，表示不匹配任何原因；保留合并请求类型和全部项目活动设置。直接保存。", func(before, after gitlab.NotificationConfig) bool {
		return len(after.Todos.Filter.Reasons) == 0 && reflect.DeepEqual(before.Todos.Filter.Types, after.Todos.Filter.Types) && reflect.DeepEqual(before.Events, after.Events)
	})
	report["status"] = "passed"
	report["configuration_disposition"] = "isolated member; collectors never started; service stopped on exit"
}
