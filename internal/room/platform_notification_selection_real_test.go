//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

// Use the configured model and shipped role, including managed credentials.
// No extra routing hints or special acceptance role are added to Instructions.
func settingsRealProfile(t *testing.T, path, workspace string) dshProfile {
	t.Helper()
	cfg, err := loadStartupConfig(path, true)
	if err != nil || cfg.Runtime == nil {
		t.Fatal("configured runtime required", err)
	}
	model := cfg.Runtime.Model.Clone()
	if cfg.Models != nil {
		entry, err := cfg.Models.resolve(modelSelection{Mode: "default"})
		if err != nil {
			t.Fatal(err)
		}
		credentials, err := newModelCredentials(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		credential, err := credentials.get(entry.CredentialRef)
		if err != nil {
			t.Fatal(err)
		}
		model = entry.materialize()
		const key = "TEAMX_SETTINGS_ACCEPTANCE_MODEL_KEY"
		t.Setenv(key, credential.Value)
		model.APIKeyEnv = key
	} else {
		if key := model.APIKeyEnv; os.Getenv(key) == "" && cfg.Env[key] != "" {
			t.Setenv(key, cfg.Env[key])
		}
		imageRealCredential(t, model.APIKeyEnv)
	}
	return dshProfile{BinaryPath: cfg.Runtime.DSH.BinaryPath, BinaryArgs: cfg.Runtime.DSH.BinaryArgs, Artifact: cfg.Runtime.DSH.Artifact, Workspace: workspace, Model: model, Instructions: assistantInstructions}
}

type settingsRequestTrace struct {
	Path string          `json:"path"`
	Body json.RawMessage `json:"body"`
	At   time.Time       `json:"at"`
}
type settingsRequestAudit struct {
	mu       sync.Mutex
	Requests []settingsRequestTrace
}

func (a *settingsRequestAudit) serve(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/platforms/") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read", 400)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			a.mu.Lock()
			a.Requests = append(a.Requests, settingsRequestTrace{r.URL.Path, body, time.Now()})
			a.mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}
func (a *settingsRequestAudit) snapshot() []settingsRequestTrace {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]settingsRequestTrace(nil), a.Requests...)
}

// Count simulated read commands separately from CLI version/identity checks.
// These are fixture observations, never a claim about real network requests.
func settingsProviderReadCounts(provider string, calls []string) map[string]int {
	counts := map[string]int{"metadata_reads": 0, "account_verification_reads": 0, "cli_checks": 0, "other_calls": 0}
	for _, call := range calls {
		if provider == "gitlab" {
			switch {
			case strings.HasPrefix(call, "GET /api/v4/projects"):
				counts["metadata_reads"]++
			case strings.HasPrefix(call, "GET /api/v4/user"):
				counts["account_verification_reads"]++
			default:
				counts["other_calls"]++
			}
			continue
		}
		var args []string
		if json.Unmarshal([]byte(call), &args) != nil || len(args) < 3 {
			counts["other_calls"]++
			continue
		}
		args = args[2:] // fixed fixture --profile NAME prefix
		switch {
		case len(args) >= 3 && args[0] == "api" && args[1] == "GET":
			if strings.Contains(args[2], "/authen/v1/user_info") {
				counts["account_verification_reads"]++
			} else {
				counts["metadata_reads"]++
			}
		case len(args) >= 2 && args[0] == "im" && (args[1] == "+chat-list" || args[1] == "+chat-search"):
			counts["metadata_reads"]++
		case args[0] == "--version" || args[0] == "whoami":
			counts["cli_checks"]++
		default:
			counts["other_calls"]++
		}
	}
	return counts
}

func settingsTurnEvidence(a *policyRealAudit) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.Turns) == 0 {
		return nil
	}
	turn := a.Turns[len(a.Turns)-1]
	ids := map[string]bool{}
	for _, tool := range turn.Result.Tools {
		ids[tool.ID] = true
	}
	return map[string]any{"tool_calls": len(ids), "tools": turn.Result.Tools, "error": turn.Error}
}

// Independent model runs create cards themselves; Chromium submits those exact
// cards and the same member reads/acknowledges the Room result. Providers are
// simulated, collectors stay stopped, and no installed subscriptions are touched.
func TestPlatformNotificationSelectionRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_NOTIFICATION_SELECTION_REAL") != "1" {
		t.Skip("opt-in real model and Chromium; explicit settings config and fresh output required")
	}
	root := imageRealOutput(t)
	for _, provider := range []string{"lark", "gitlab"} {
		for run := 1; run <= 2; run++ {
			t.Run(fmt.Sprintf("%s/run%d", provider, run), func(t *testing.T) { settingsSelectionRealRun(t, root, provider, run) })
		}
	}
}
func settingsSelectionRealRun(t *testing.T, root, provider string, run int) {
	t.Setenv("USER", humanCaller.ID)
	out := filepath.Join(root, fmt.Sprintf("%s-%d", provider, run))
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "incomplete", "provider": provider + " simulated metadata", "business_writes": 0, "collectors_started": false, "real_provider": "not run", "role": "shipped default assistant", "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, out, report)
	workspace := filepath.Join(out, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	profile := settingsRealProfile(t, os.Getenv("TEAMX_SETTINGS_CONFIG"), workspace)
	report["model"] = profile.Model.ModelID
	var m *roomManager
	var platformID string
	var larkDir string
	if provider == "lark" {
		h, _ := platformMemberHarness(t)
		m, platformID, larkDir = h.m, h.c.ID, h.dir
		scriptPath := filepath.Join(h.dir, "lark-cli")
		script, err := os.ReadFile(scriptPath)
		if err != nil {
			t.Fatal(err)
		}
		logged := strings.Replace(string(script), "base=os.path.dirname", "open(os.path.join(os.path.dirname(os.path.realpath(sys.argv[0])), 'calls'), 'a').write(json.dumps(sys.argv[1:])+'\\n')\nbase=os.path.dirname", 1)
		if err := os.WriteFile(scriptPath, []byte(logged), 0700); err != nil {
			t.Fatal(err)
		}
	} else {
		var req platformMemberRequest
		m, _, _, req, _ = gitlabProjectSelectionFixture(t)
		platformID = req.Input.PlatformID
	}
	var providerMu sync.Mutex
	var providerReads []string
	if provider == "gitlab" {
		original := m.platformHTTP
		m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
			providerMu.Lock()
			providerReads = append(providerReads, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
			providerMu.Unlock()
			return original.RoundTrip(r)
		})
	}
	providerTrace := func() []string {
		if provider == "lark" {
			b, _ := os.ReadFile(filepath.Join(larkDir, "calls"))
			if len(b) == 0 {
				return nil
			}
			return strings.Split(strings.TrimSpace(string(b)), "\n")
		}
		providerMu.Lock()
		defer providerMu.Unlock()
		return append([]string(nil), providerReads...)
	}
	m.notifications.Clock = time.Now
	m.config.unifiedInputs = true
	m.defaultRuntime = testTeamDefinition(t).Runtime
	ensureAssistant(t, m)
	r := assistantSummary(t, m)
	m.mu.Lock()
	prior := m.runtimes[r.RoomIdentity]
	delete(m.runtimes, r.RoomIdentity)
	m.mu.Unlock()
	if prior != nil && prior.service != nil {
		closeService(t, prior.service)
	}
	_, saved := readManagedRoom(t, m, r)
	member := saved.Members[0]
	// The second independent run retains an older saved role without card routing.
	// The public guide must carry navigation even after a controlled rotation.
	if run == 2 {
		profile.Instructions = strings.Replace(assistantInstructions, "For platform onboarding, changes to your notifications/tools, or an account/group/project selection card,", "For platform onboarding or changes to your notifications/tools,", 1)
		report["role"] = "saved default assistant role without card navigation"
	}
	audit := &policyRealAudit{Calls: map[string]int{}}
	requests := &settingsRequestAudit{}
	server := browserTestServer(t, m, requests.serve(audit.serve(m)))
	m.config.endpoint = server.URL
	svc, err := startService(m.roomDir(r.RoomID), r.RoomID, serviceConfig{endpoint: server.URL, cliPath: productionFixtureCLI(t), maxTurns: 12, persistentScheduling: true, unifiedInputs: true, updates: m.updates, access: m.config.access, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{member.Runner.Profile: profile}}, func(s *service, state RoomSnapshot) error {
		observePolicyReal(s, audit)
		if err := s.loadMembers(state); err != nil {
			return err
		}
		return s.attachMembers(state)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	m.bindPlatformReferences(svc)
	state, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.runtimes[r.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	m.catalogChangedLocked()
	m.mu.Unlock()
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	query := platformMemberRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), Input: &platformMemberInput{PlatformID: platformID}, ObserveCurrent: true}
	view := func() platformMemberView {
		t.Helper()
		m.mu.Lock()
		defer m.mu.Unlock()
		_, v, err := m.observePlatformMemberLocked(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	backend := svc.memberBackend(member.Runner)
	options, err := backend.componentOptions(SessionOptions{MemberIdentity: id, StatePath: filepath.Join(svc.store.dir, "agents", id.MembershipID, "session.json"), CLIPath: svc.cliPath, CLIEnv: svc.toolEnv(id)}, dshReference{})
	if err != nil {
		t.Fatal(err)
	}
	report["instructions"] = options.Instructions
	var cases []map[string]any
	defer func() {
		report["cases"], report["audit"], report["requests"] = cases, audit, requests.snapshot()
		report["final"] = view()
		state, _ := svc.store.LoadRoom()
		report["messages"] = state.Messages
		report["provider_calls"] = providerTrace()
		report["provider_call_count"] = len(providerTrace())
		report["provider_read_counts"] = settingsProviderReadCounts(provider, providerTrace())
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	turn := uint64(0)
	wait := func(n uint64) {
		t.Helper()
		s := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && (s.Agent.TurnNumber >= n || s.Paused) })
		if s.Error != "" || s.Agent.Turn == nil || s.Agent.Turn.Status != "completed" || s.Agent.TurnNumber != n {
			t.Fatal("real turn did not complete", s.Error, s.Agent.Turn)
		}
	}
	post := func(text string) (PostReceipt, time.Time, []settingsRequestTrace) {
		t.Helper()
		t.Log("real model request:", text)
		start := time.Now()
		before := len(requests.snapshot())
		source, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: fmt.Sprintf("request-%d", turn+1), Text: text})
		if err != nil {
			t.Fatal(err)
		}
		// A rotation leaves execution paused, so explicitly restore its enable intent.
		if turn == 0 {
			if _, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
				t.Fatal(err)
			}
		}
		turn++
		wait(turn)
		return source, start, requests.snapshot()[before:]
	}
	enabled := func(v platformMemberView) bool {
		if provider == "lark" {
			return v.Config != nil && v.Config.Enabled
		}
		return v.GitLab.Config.Enabled
	}
	label := map[string]string{"lark": "飞书", "gitlab": "GitLab"}[provider]
	toggle := func(text string, want bool) {
		t.Helper()
		before := view()
		readsBefore := len(providerTrace())
		source, start, trace := post(text)
		after := view()
		var writes []platformMemberRequest
		for _, r := range trace {
			switch r.Path {
			case "/v1/platforms/member/get":
				if len(writes) == 0 {
					t.Error("pure toggle forced a preliminary get")
				}
			case "/v1/platforms/member/notifications":
				var req platformMemberRequest
				if json.Unmarshal(r.Body, &req) != nil {
					t.Fatal("bad write trace")
				}
				writes = append(writes, req)
			case "/v1/platforms/selection/start":
				t.Error("pure switch opened a picker")
			}
		}
		if len(writes) != 1 || !writes[0].ObserveCurrent || writes[0].SourceSeq != source.Seq || writes[0].Input.PlatformID != platformID || writes[0].Input.Enabled == nil || *writes[0].Input.Enabled != want {
			t.Fatal("model did not use one short switch", trace)
		}
		m.mu.Lock()
		ops, err := m.platformMemberStatusLocked(platformMemberRequest{ServiceInfo: svc.info, Caller: query.Caller, SourceSeq: source.Seq})
		m.mu.Unlock()
		if err != nil || len(ops) != 1 || ops[0].Status != "completed" || ops[0].Source.Seq != source.Seq || ops[0].Input.NotificationVersion != before.NotificationVersion || ops[0].Result.Version != after.NotificationVersion || enabled(after) != want || after.ToolEnabled || after.BindingRevision != 0 {
			t.Fatal("receipt/frozen version/authoritative config mismatch", err, ops)
		}
		cases = append(cases, map[string]any{"text": text, "source_seq": source.Seq, "elapsed_seconds": time.Since(start).Seconds(), "receipt": ops[0], "before": before, "after": after, "requests": trace, "provider_calls": providerTrace()[readsBefore:], "provider_read_counts": settingsProviderReadCounts(provider, providerTrace()[readsBefore:]), "model": settingsTurnEvidence(audit)})
	}
	showCard := func() {
		beforeCard := view()
		readsBefore := len(providerTrace())
		cardText := map[string]string{"lark": "飞书选择群的卡片", "gitlab": "GitLab 项目选择卡片"}[provider]
		source, start, trace := post(cardText)
		var selection platformSelectionView
		for _, req := range trace {
			if req.Path == "/v1/platforms/chats" || req.Path == "/v1/platforms/projects" {
				t.Error("enumerated candidates before showing a card")
			}
		}
		m.mu.Lock()
		selections, err := m.platformSelectionsLocked()
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range selections {
			if s.MemberIdentity == id && s.Source.Seq == source.Seq {
				if selection.ID != "" {
					t.Fatal("duplicate selection")
				}
				m.mu.Lock()
				selection, err = m.selectionViewLocked(s)
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		if selection.ID == "" || selection.Input.PlatformID != platformID || selection.Status != "waiting" || selection.Continuation.Mode != "inbox" {
			t.Fatal("model did not publish subscribed card", selection)
		}
		if !reflect.DeepEqual(beforeCard, view()) {
			t.Fatal("showing a card changed settings")
		}
		if len(providerTrace()) != readsBefore {
			t.Error("Agent read provider candidates before showing card")
		}
		cases = append(cases, map[string]any{"text": cardText, "source_seq": source.Seq, "elapsed_seconds": time.Since(start).Seconds(), "receipt": selection, "card_created_seconds": selection.CreatedAt.Sub(start).Seconds(), "requests": trace, "model": settingsTurnEvidence(audit)})
		browserStart := time.Now()
		browserOut := filepath.Join(out, fmt.Sprintf("card-%d", source.Seq))
		if err := os.Mkdir(browserOut, 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, "node", "testdata/platform_notification_selection_browser.cjs", server.URL, selection.ID, r.RoomInstance, provider, browserOut)
		output, err := cmd.CombinedOutput()
		cases[len(cases)-1]["browser_output"] = string(output)
		cases[len(cases)-1]["source_to_browser_finished_seconds"] = time.Since(start).Seconds()
		if err != nil {
			t.Fatal("Chromium", err, string(output))
		}
		cases[len(cases)-1]["browser_seconds"] = time.Since(browserStart).Seconds()
		var browserEvidence struct {
			VisibleMS   float64 `json:"component_visible_ms"`
			VisibleAtMS int64   `json:"component_visible_at_ms"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(output), &browserEvidence); err != nil {
			t.Fatal("invalid Chromium evidence", err)
		}
		cases[len(cases)-1]["browser_navigation_to_component_ms"] = browserEvidence.VisibleMS
		cases[len(cases)-1]["component_observed_seconds"] = float64(browserEvidence.VisibleAtMS-start.UnixMilli()) / 1000
		turn++
		wait(turn)
		m.mu.Lock()
		selected, readErr := m.readPlatformSelectionLocked(selection.ID)
		if readErr == nil {
			selection, readErr = m.selectionViewLocked(selected)
		}
		m.mu.Unlock()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if selection.Status != "completed" || selection.Continuation.HandledVersion != selection.ResultVersion {
			t.Fatal("model did not acknowledge selected result", selection)
		}
		if !reflect.DeepEqual(beforeCard, view()) {
			t.Fatal("card-only selection changed notification settings")
		}
		state, err = svc.store.LoadRoom()
		if err != nil {
			t.Fatal(err)
		}
		events := 0
		for _, msg := range state.Messages {
			if msg.Event != nil && msg.Event.OperationID == selection.ID {
				events++
			}
		}
		if events != 1 {
			t.Fatal("selection emitted duplicate/missing event", events)
		}
		cases[len(cases)-1]["selected_result"] = selection
		cases[len(cases)-1]["provider_calls"] = providerTrace()[readsBefore:]
		cases[len(cases)-1]["provider_read_counts"] = settingsProviderReadCounts(provider, providerTrace()[readsBefore:])
		cases[len(cases)-1]["total_seconds"] = time.Since(start).Seconds()
		cases[len(cases)-1]["continuation_model"] = settingsTurnEvidence(audit)

	}
	toggle("开启"+label+"通知", true)
	initial := view()
	if provider == "lark" {
		want := m.defaultLarkConfig(platformID)
		if !reflect.DeepEqual(initial.Config.Private, want.Private) || !reflect.DeepEqual(initial.Config.Groups, want.Groups) {
			t.Fatal("first enable differs from manual default")
		}
	} else {
		defaults := gitlab.DefaultNotificationConfig(platformID)
		defaults.Target = platformMemberTarget(id)
		want, err := gitlab.NormalizeNotificationConfig(defaults)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(initial.GitLab.Config.Todos, want.Todos) || initial.GitLab.Config.Events.Enabled {
			t.Fatal("first enable differs from manual default")
		}
	}
	showCard()
	if provider == "lark" {
		before := view()
		readsBefore := len(providerTrace())
		source, start, trace := post("只接收这个群")
		after := view()
		if after.Config == nil || !after.Config.Enabled || !after.Config.Groups.Enabled || after.Config.Groups.Scope != "specified" || !reflect.DeepEqual(after.Config.Groups.ChatIDs, []string{"oc_group_three"}) || after.Config.Private.Enabled || after.Baseline != before.Baseline || after.ToolEnabled {
			t.Fatal("selected-group-only intent not preserved", after)
		}
		m.mu.Lock()
		ops, err := m.platformMemberStatusLocked(platformMemberRequest{ServiceInfo: svc.info, Caller: query.Caller, SourceSeq: source.Seq})
		m.mu.Unlock()
		if err != nil || len(ops) != 1 || ops[0].Status != "completed" || ops[0].Input.NotificationVersion != before.NotificationVersion || ops[0].Result.Version != after.NotificationVersion {
			t.Fatal("complex group receipt mismatch", err, ops)
		}
		cases = append(cases, map[string]any{"text": "只接收这个群", "source_seq": source.Seq, "elapsed_seconds": time.Since(start).Seconds(), "receipt": ops[0], "before": before, "after": after, "requests": trace, "provider_calls": providerTrace()[readsBefore:], "provider_read_counts": settingsProviderReadCounts(provider, providerTrace()[readsBefore:]), "model": settingsTurnEvidence(audit)})
	}
	preserved := view()
	// Exercise the public controlled rotation on an already used real Session.
	rotate := apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}
	if _, err = svc.rotateSession(ctx, rotate); err != nil {
		t.Fatal("settled rotation", err)
	}
	report["controlled_rotation"] = true
	// run is required after rotation, before posting the next actual request.
	if _, err = svc.control(ctx, rotate, "run"); err != nil {
		t.Fatal(err)
	}
	toggle("关闭"+label+"通知", false)
	toggle("开启"+label+"通知", true)
	restored := view()
	if preserved.Baseline != restored.Baseline || provider == "lark" && !reflect.DeepEqual(preserved.Config, restored.Config) || provider == "gitlab" && !reflect.DeepEqual(preserved.GitLab.Config, restored.GitLab.Config) {
		t.Fatal("toggle changed saved scope, filters or reception start")
	}
	showCard()

	report["status"] = "passed"
}
