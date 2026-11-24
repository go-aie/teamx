//go:build e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	computerdocker "github.com/go-aie/teamx/internal/computer/docker"
)

// Explicit disposable Runtime only. Browser and native tools are real; Agent
// scheduling defaults to a controlled backend; combined mode explicitly uses DSH.
func TestComputerRealViewingAndHandoff(t *testing.T) {
	if os.Getenv("TEAMX_COMPUTER_VIEW_REAL") != "1" {
		t.Skip("explicit disposable Runtime/browser acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	managed := os.Getenv("TEAMX_COMPUTER_VIEW_MANAGED") == "1"
	var key []byte
	var err error
	if !managed {
		key, err = os.ReadFile(os.Getenv("TEAMX_COMPUTER_TOKEN_FILE"))
		if err != nil {
			t.Fatal(err)
		}
	}
	m, c, x, _ := managedComputerFixture(t)
	m.config.unifiedInputs = true
	m.factory = controlledActivationFactory(nil, nil)
	combined := os.Getenv("TEAMX_COMPUTER_COMBINED_REAL") == "1"
	var modelMu sync.Mutex
	modelCalls := map[string]int{}
	var modelSchemas []map[string]any
	defer func() {
		if managed && combined {
			if dir := os.Getenv("TEAMX_COMPUTER_VIEW_EVIDENCE"); dir != "" {
				modelMu.Lock()
				raw, _ := json.MarshalIndent(modelSchemas, "", "  ")
				modelMu.Unlock()
				_ = os.WriteFile(filepath.Join(dir, "model-schema-checks.json"), raw, 0600)
			}
		}
	}()
	var modelName string
	if combined {
		cfg, entry, credential := inputAcceptanceModel(t)
		modelName = entry.ModelID
		var schemaGateway *httptest.Server
		if managed {
			upstream, err := url.Parse(entry.materialize().BaseURL)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			direct := proxy.Director
			proxy.Director = func(r *http.Request) { direct(r); r.Host = upstream.Host }
			schemaGateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
				if err != nil {
					http.Error(w, "request read failed", 400)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(raw))
				var body struct {
					Tools []struct {
						Name     string `json:"name"`
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
				}
				_ = json.Unmarshal(raw, &body)
				names := []string{}
				hasComputer := false
				for _, tool := range body.Tools {
					name := tool.Function.Name
					if name == "" {
						name = tool.Name
					}
					names = append(names, name)
					if strings.Contains(name, "computer_status") || strings.Contains(name, "browser_") {
						hasComputer = true
					}
				}
				modelMu.Lock()
				modelSchemas = append(modelSchemas, map[string]any{"global_enabled": c.current.Load().Enabled, "tools": names, "has_computer": hasComputer})
				modelMu.Unlock()
				proxy.ServeHTTP(w, r)
			}))
			defer schemaGateway.Close()
		}
		m.factory = func(dir, roomID string, config serviceConfig) (*service, error) {
			config.models, config.modelTargets = nil, nil
			config.cliPath = os.Getenv("TEAMX_COMPUTER_CLI")
			config.dshTurnTimeout = 2 * time.Minute
			for name, profile := range config.dshProfiles {
				profile.BinaryPath, profile.BinaryArgs, profile.Artifact = cfg.Runtime.DSH.BinaryPath, cfg.Runtime.DSH.BinaryArgs, cfg.Runtime.DSH.Artifact
				profile.Model = entry.materialize()
				if schemaGateway != nil {
					profile.Model.BaseURL = schemaGateway.URL
				}
				profile.Model.APIKeyEnv = "TEAMX_COMBINED_MODEL_KEY"
				profile.Instructions = `Isolated read-only Computer acceptance. Consume current Room input using input tools. Use browser_get_text to observe your own current browser page and return the observed heading/account state privately. Do not navigate, use Bash, other tools, host files, external websites, delegate or post public messages. When receiving a human handoff event, observe the current page again without repeating login. If human_control is returned, finish immediately. Computer tools are the only allowed way to read browser content.`
				if managed {
					profile.Instructions += ` On EVERY turn with Computer tools available, after consuming the newest input, make a fresh browser_get_text call and report the exact heading it returns. This is required even after a handoff event or an earlier successful read. Never infer the current account state from messages, events, screenshots or prior tool results. If Computer tools are absent because the global setting is off, read only the current input and report Computer disabled; do not use other resources.`
				}
				config.dshProfiles[name] = profile
			}
			return newConfiguredService(dir, roomID, config)
		}
		t.Setenv("TEAMX_COMBINED_MODEL_KEY", credential.Value)
		// Only names/timing are observed; model credentials and content stay private.
	}
	server := browserTestServer(t, m, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if managed && r.URL.Path == "/acceptance/site-status" {
			managedAcceptanceStatus(w, r, c)
			return
		}
		if r.URL.Path == "/acceptance/held" {
			modelMu.Lock()
			n := modelCalls["held"]
			modelMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]int{"completed": n})
			return
		}
		m.ServeHTTP(w, r)
	}))
	m.config.endpoint = server.URL
	if combined {
		m.config.cliPath = os.Getenv("TEAMX_COMPUTER_CLI")
	}
	if managed {
		t.Cleanup(func() { _ = m.close(); cleanupManagedAcceptance(t, c) })
		applyManagedBrowser(t, c, server.URL, true)
		managedAcceptanceSite(t, c)
	} else {
		input := connectionInput(c, os.Getenv("TEAMX_COMPUTER_ENDPOINT"))
		input.Caller.ID = humanOperator()
		input.Credential = strings.TrimSpace(string(key))
		if op := applyConnection(t, c, input); op.Status != "completed" {
			t.Fatal(op)
		}
	}

	groupMode := os.Getenv("TEAMX_COMPUTER_VIEW_GROUP") == "1"
	var room roomSummary
	if groupMode {
		team := testTeamDefinition(t)
		team.ID = "computer-browser-team"
		if combined {
			team.Scheduling = schedulingParallel
		}
		peer := team.Members[0]
		peer.ID = "writer"
		peer.Name = "Writer"
		team.Members = append(team.Members, peer)
		m.teams[team.ID] = team
		room = managerCreateTeam(t, m, "computer-browser-group", team.ID)
	} else {
		room = createDirectConversation(t, m, directSelection("computer-browser"))
	}
	saved, _ := readManagedRoom(t, m, room)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: saved.Members[0].AgentID, MembershipID: saved.Members[0].MembershipID}
	other := createDirectConversation(t, m, directSelection("computer-browser-B"))
	secondSaved, _ := readManagedRoom(t, m, other)
	secondID := MemberIdentity{RoomIdentity: other.RoomIdentity, AgentID: secondSaved.Members[0].AgentID, MembershipID: secondSaved.Members[0].MembershipID}
	if groupMode {
		secondID = MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: saved.Members[1].AgentID, MembershipID: saved.Members[1].MembershipID}
	}
	openRoomOK(t, m, room, "computer-browser-open")
	openRoomOK(t, m, other, "computer-browser-B-open")
	// Screen startup under emulation has its own finite budget.
	t.Setenv("TEAMX_COMPUTER_AGENT_REAL", "1")
	applyMemberComputer(t, x, id, true)
	applyMemberComputer(t, x, secondID, true)
	a, err := x.display(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	b, err := x.display(t.Context(), secondID)
	if err != nil {
		t.Fatal(err)
	}
	nativeCall := func(s computer.Screen, name string, args any) (computer.Outcome, error) {
		raw, _ := json.Marshal(args)
		return s.Call(t.Context(), computer.Call{ID: newComputerID(), Scope: computer.Scope{Session: "browser-acceptance", Turn: "1"}, Tool: name, ContractDigest: c.current.Load().ContractDigest, Arguments: raw})
	}
	out, err := nativeCall(a, "browser_navigate", map[string]any{"url": "http://127.0.0.1:18765/form"})
	if err != nil || !out.ResultAvailable {
		t.Fatal(out, err)
	}
	out, err = nativeCall(b, "browser_navigate", map[string]any{"url": "http://127.0.0.1:18765/"})
	if err != nil || !out.ResultAvailable {
		t.Fatal(out, err)
	}
	var concurrent atomic.Bool
	if combined {
		sampling, cancelSample := context.WithCancel(t.Context())
		sampled := make(chan struct{})
		go func() {
			defer close(sampled)
			for {
				select {
				case <-sampling.Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
				a, ea := x.runtime(id).target(id.MembershipID).Call[MemberStatus](sampling, InspectMember{id})
				b, eb := x.runtime(secondID).target(secondID.MembershipID).Call[MemberStatus](sampling, InspectMember{secondID})
				if ea == nil && eb == nil && a.Active && b.Active {
					concurrent.Store(true)
				}
			}
		}()
		defer func() { cancelSample(); <-sampled }()
	}
	// B repeatedly observes its own page throughout human control of A.
	var observed, whileHeld atomic.Int32
	var realTurns atomic.Int32
	background, cancel := context.WithCancel(t.Context())
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-background.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			if managed && combined && observed.Load() > 0 {
				if whileHeld.Load() >= 2 {
					return
				}
				e := x.entry(id)
				e.mu.Lock()
				held := time.Now().Before(e.humanUntil)
				e.mu.Unlock()
				if !held {
					continue
				}
			}
			if combined {
				svc := x.runtime(secondID)
				_, err := svc.room.Call[PostReceipt](background, PostMessage{RoomIdentity: secondID.RoomIdentity, Caller: humanCaller, ClientMessageID: newComputerID(), Text: "Read your current page using browser_get_text and privately report its heading/account state."})
				if err != nil {
					return
				}
				for {
					status, err := svc.target(secondID.MembershipID).Call[MemberStatus](background, InspectMember{secondID})
					if err != nil {
						return
					}
					if !status.Active && status.Agent.TurnNumber > uint64(realTurns.Load()) && status.Agent.Turn != nil {
						if status.Agent.Turn.Status != "completed" {
							return
						}
						realTurns.Store(int32(status.Agent.TurnNumber))
						break
					}
					select {
					case <-background.Done():
						return
					case <-time.After(100 * time.Millisecond):
					}
				}
			}
			out, err := nativeCall(b, "browser_get_text", map[string]any{})
			if err == nil && out.ResultAvailable && !computerNativeResultError(out.Result) {
				observed.Add(1)
				e := x.entry(id)
				e.mu.Lock()
				held := time.Now().Before(e.humanUntil)
				e.mu.Unlock()
				if held {
					whileHeld.Add(1)
					if combined {
						modelMu.Lock()
						modelCalls["held"]++
						modelMu.Unlock()
					}
				}
			}
		}
	}()
	defer func() { cancel(); <-finished }()
	if combined {
		peerService := x.runtime(secondID)
		if _, err = peerService.control(peerService.ctx, apiRequest{AgentID: secondID.AgentID, MembershipID: secondID.MembershipID}, "run"); err != nil {
			t.Fatal(err)
		}
	}
	svc := x.runtime(id)
	if _, err = svc.control(svc.ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	if combined {
		_, err = svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: newComputerID(), Text: "Read your own current page using browser_get_text and privately report its heading."})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool {
			return !s.Active && s.Agent.TurnNumber >= 1 && s.Agent.Turn != nil && s.Agent.Turn.Status == "completed"
		})
		cancel()
	}
	fixtureStatus := os.Getenv("TEAMX_COMPUTER_FIXTURE_URL") + "/status"
	if managed {
		fixtureStatus = server.URL + "/acceptance/site-status"
	}
	fixture, _ := json.Marshal(map[string]any{"combined": combined, "held_probe": server.URL + "/acceptance/held", "group": groupMode, "first_screen_id": environmentID(m.epoch, id), "second_screen_id": environmentID(m.epoch, secondID), "room": room.RoomIdentity, "other_room": other.RoomIdentity, "fixture_status": fixtureStatus, "viewer_url": "/v1/environment/" + environmentID(m.epoch, id) + "/viewer"})
	ctx, stop := context.WithTimeout(t.Context(), 6*time.Minute)
	defer stop()
	cmd := exec.CommandContext(ctx, "node", "testdata/computer_view_browser.cjs", server.URL, string(fixture))
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	t.Log(string(output))
	cancel()
	<-finished
	if combined {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool {
			return !s.Active && s.Agent.TurnNumber >= 2 && s.Agent.Turn != nil && s.Agent.Turn.Status == "completed"
		})
		for _, member := range []MemberIdentity{id, secondID} {
			var diagnostic backendDiagnostic
			if err := readSnapshot(filepath.Join(x.runtime(member).store.dir, "agents", member.MembershipID, "dsh-turn.json"), &diagnostic, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			seen := false
			for _, call := range diagnostic.Result.Tools {
				if strings.Contains(call.Title+call.Input, "browser_get_text") {
					seen = true
				}
			}
			if !seen {
				t.Fatal("real Agent did not use native browser", member.AgentID, diagnostic.Result.Text)
			}
			if member == id && !strings.Contains(diagnostic.Result.Text, "authenticated") {
				t.Fatal("Agent did not observe human login after handoff", diagnostic.Result.Text)
			}
		}
	}
	if observed.Load() < 2 || whileHeld.Load() < 1 {
		t.Fatal("B did not continue during A takeover", observed.Load(), whileHeld.Load())
	}
	if combined && !concurrent.Load() {
		t.Fatal("two real DSH turns never overlapped")
	}
	// C2's verified stop preserves the profile. Restart the screen and prove the
	// dummy login cookie persists via the native browser, without replaying login.
	// Managed mode proves the stronger whole-container stop/start below instead.
	if !managed {
		if err = x.settleScope(t.Context(), id, computer.Scope{}, true); err != nil {
			t.Fatal(err)
		}
		a, err = x.display(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = nativeCall(a, "browser_navigate", map[string]any{"url": "http://127.0.0.1:18765/private"}); err != nil {
			t.Fatal(err)
		}
		out, err = nativeCall(a, "browser_get_text", map[string]any{})
		if err != nil || !strings.Contains(string(out.Result), "authenticated") {
			t.Fatalf("profile login not preserved: %v %s", err, out.Result)
		}
		out, err = nativeCall(b, "browser_navigate", map[string]any{"url": "http://127.0.0.1:18765/private"})
		if err != nil {
			t.Fatal(err)
		}
		out, err = nativeCall(b, "browser_get_text", map[string]any{})
		if err != nil || strings.Contains(string(out.Result), "authenticated") {
			t.Fatalf("B inherited A login: %v %s", err, out.Result)
		}
	}
	report := map[string]any{"passed": true, "real_runtime": true, "real_browser": true, "agent_backend": map[bool]string{false: "controlled; no model", true: "real DSH and model"}[combined], "real_model": modelName, "B_model_turns": realTurns.Load(), "two_real_DSH_turns_overlapped": concurrent.Load(), "same_room_picker": groupMode, "B_native_calls": observed.Load(), "B_calls_during_A_takeover": whileHeld.Load(), "login_persists_after_screen_stop_restart": true, "B_does_not_inherit_A_login": true}
	if managed {
		t.Log("disabling managed Computer after browser takeover")
		c.mu.Lock()
		original := *c.journal.Docker
		c.mu.Unlock()
		applyManagedBrowser(t, c, server.URL, false)
		assertManagedMemberCapabilitiesOff(t, x, id, secondID)
		if combined {
			before, err := svc.target(id.MembershipID).Call[MemberStatus](t.Context(), InspectMember{id})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: newComputerID(), Text: "Computer is globally disabled. Read this message through input tools, do not access a browser or other resources, and reply briefly that Computer is disabled."}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool {
				return !s.Active && s.Agent.TurnNumber > before.Agent.TurnNumber && s.Agent.Turn != nil && s.Agent.Turn.Status == "completed"
			})
			cancel()
			modelMu.Lock()
			on, off := false, false
			for _, r := range modelSchemas {
				if r["global_enabled"] == true && r["has_computer"] == true {
					on = true
				}
				if r["global_enabled"] == false {
					off = true
					if r["has_computer"] == true {
						modelMu.Unlock()
						t.Fatal("actual model request retained Computer schemas after managed disable")
					}
				}
			}
			modelMu.Unlock()
			if !on || !off {
				t.Fatal("actual model request schema comparison missing", on, off)
			}
			report["actual_model_schema_removed_after_managed_disable"] = true
		}
		c.mu.Lock()
		stopped := c.journal.Docker.Stopped
		c.mu.Unlock()
		if !stopped {
			t.Fatal("global disable did not stop managed container")
		}
		applyManagedBrowser(t, c, server.URL, true)
		t.Log("managed Computer re-enabled; restoring both member screens")
		managedAcceptanceSite(t, c)
		a, err = x.display(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		b, err = x.display(t.Context(), secondID)
		if err != nil {
			t.Fatal(err)
		}
		for i, screen := range []computer.Screen{a, b} {
			if _, err = nativeCall(screen, "browser_navigate", map[string]any{"url": "http://127.0.0.1:18765/private"}); err != nil {
				t.Fatal(err)
			}
			out, err = nativeCall(screen, "browser_get_text", map[string]any{})
			if err != nil || !out.ResultAvailable || computerNativeResultError(out.Result) || strings.Contains(string(out.Result), "authenticated") != (i == 0) {
				t.Fatal("global stop/start lost or shared login", i, err, string(out.Result))
			}
		}
		c.mu.Lock()
		reused := *c.journal.Docker
		c.mu.Unlock()
		if reused.Container != original.Container || reused.Instance != original.Instance || reused.KeyRef != original.KeyRef {
			t.Fatal("global re-enable replaced owned resources")
		}
		applyManagedBrowser(t, c, server.URL, false)
		report["managed_docker"] = true
		report["real_settings_ui_created_started_and_stopped_container"] = true
		report["fixed_image"] = computerdocker.DefaultImage()
		report["global_stop_start_preserves_login_and_isolation"] = true
		report["global_disable_removed_session_capability"] = true
	}
	raw, _ := json.MarshalIndent(report, "", "  ")
	if dir := os.Getenv("TEAMX_COMPUTER_VIEW_EVIDENCE"); dir != "" {
		if err = os.WriteFile(filepath.Join(dir, "integration.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
		if managed && combined {
			modelMu.Lock()
			schemaRaw, _ := json.MarshalIndent(modelSchemas, "", "  ")
			modelMu.Unlock()
			if err = os.WriteFile(filepath.Join(dir, "model-schema-checks.json"), schemaRaw, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
