//go:build e2e && darwin

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Read-only continuation of saved successful sends, including a send test whose
// later local recovery check failed. It cannot enter a sending preparer or POST
// to GitLab, and never edits the original send report to conceal a failed check.
func TestRealGitlabImageReceiptAndReturn(t *testing.T) {
	if os.Getenv("TEAMX_GITLAB_IMAGE_SEND_RECOVERY") != "1" {
		t.Skip("explicit original report and read-only recovery required")
	}
	q, _ := gitlabImageSendRealSample(t)
	path := os.Getenv("TEAMX_GITLAB_IMAGE_SEND_REPORT")
	if !filepath.IsAbs(path) {
		t.Fatal("absolute original report required")
	}
	var old struct {
		Manifest gitlabImageSendRealInput `json:"manifest"`
		Member   MemberIdentity           `json:"member"`
		Cases    []struct {
			ID     string          `json:"id"`
			Action referenceAction `json:"action"`
		} `json:"cases"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &old) != nil || len(old.Cases) != 2 || !bytes.Equal(mustContractJSON(old.Manifest), mustContractJSON(q)) {
		t.Fatal("original successful complete actions and unchanged manifest required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	read := gitlabImageSendPreflight(t, ctx, q)
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "remote_writes": 0, "original_report": path, "scope": "independent OS process; authenticated CLI; real Events GET and delivery through formal input storage"}
	defer imageRealReport(t, out, report)
	cfg, entry, credential := inputAcceptanceModel(t)
	const key = "TEAMX_GITLAB_IMAGE_SEND_MODEL_KEY"
	t.Setenv(key, credential.Value)
	runtime := *cfg.Runtime
	runtime.Model, runtime.Model.APIKeyEnv = entry.materialize(), key
	runtime.Model.InputModalities = []string{"text"}
	m := directTestManager(t, filepath.Join(filepath.Dir(path), "rooms"), &runtime)
	m.maxTurns, m.config.unifiedInputs = 8, true
	server := browserTestServer(t, m)
	defer server.Close()
	m.config.endpoint, m.config.cliPath = server.URL, productionFixtureCLI(t)
	requests := []map[string]any{}
	var requestsMu sync.Mutex
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Scheme+"://"+r.URL.Host != q.Origin {
			return nil, platform.ErrCredential
		}
		res, err := http.DefaultTransport.RoundTrip(r)
		requestsMu.Lock()
		row := map[string]any{"method": r.Method, "path": r.URL.Path}
		if res != nil {
			row["status"] = res.StatusCode
		}
		requests = append(requests, row)
		requestsMu.Unlock()
		return res, err
	})
	room := assistantSummary(t, m)
	if room.RoomIdentity != old.Member.RoomIdentity {
		t.Fatal("recovery opened a different Room")
	}
	// Ledger recovery needs the persisted Store and authenticated CLI, not a
	// running backend. Preserve any interrupted input attempt from earlier runs.
	svc := eventLiveService(t, &eventHarness{m: m, room: room})
	m.bindPlatformReferences(svc)
	id := old.Member
	m.referenceExecutors["gitlab"] = func(context.Context, MemberIdentity, platform.AccessContext) (referenceActionSession, error) {
		t.Error("receipt recovery entered sending preparer")
		return referenceActionSession{}, ErrConflict
	}
	proof := []map[string]any{}
	for i, c := range old.Cases {
		a := c.Action
		if a.Status != "succeeded" || a.Result == nil || a.Attempts != 1 {
			t.Fatal("only exact successful original receipts may continue")
		}
		body, hashes := q.Caption, []string{q.Images[0].SHA256, q.Images[1].SHA256}
		if i == 1 {
			body, hashes = "", hashes[1:]
		}
		native := verifyRealGitlabImageNote(t, ctx, q, body, hashes, a, read)
		detail := gitlabImageReceiptCLI(t, m, svc, id, server.URL, []string{"actions", "read", "--id", c.ID})
		wire, err := json.Marshal(gitlabAgentWire(a.Request))
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			receipt := gitlabImageReceiptCLI(t, m, svc, id, server.URL, []string{"actions", "execute", "--request", string(wire)})
			if !bytes.Contains(receipt, []byte(a.Result.ResultID.Value)) || !bytes.Contains(receipt, []byte(`"succeeded"`)) {
				t.Fatal("same-ID receipt changed on independent recovery")
			}
		}
		proof = append(proof, map[string]any{"id": c.ID, "native_note": native, "receipt": json.RawMessage(detail), "same_id_reads": 2})
	}
	report["receipts"] = proof
	if len(requests) != 0 {
		t.Fatal("receipt query or same-ID replay made a provider request")
	}
	// A fresh observer Room exercises actual notification intake. The
	// sender's interrupted fixture input stays intact; no reset or data removal
	// is used to obtain a pass. Its native receipts were checked above.
	senderStore := svc.store
	m, svc, id, server, audit, connection := gitlabImageReturnObserver(t, out, &runtime, q)
	report["observer_member"], report["audit"] = id, audit
	operator := Caller{Author: Author{Kind: "human", ID: humanOperator()}}
	state, err := m.configureEventsSubscription(ctx, notification.Key{Owner: id.RoomIdentity, Source: gitlab.EventsSourceID}, 0, gitlab.EventsConfig{
		Enabled: true, PlatformID: connection.ID, Target: inboxTarget(id), Project: q.Project,
		BackfillStart: time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second), Filter: gitlab.EventsFilter{Types: []string{"note"}},
	})
	if err != nil {
		t.Fatal("formal read-only subscription setup failed", err)
	}
	// Fetch actual project events rather than synthesizing a notification from
	// note text. Delivery of the two selected actual events is explicit; this is
	// not claimed as a new periodic subscription/pagination acceptance.
	file, _ := selectedRealOAuthSnapshot(t, q.Origin)
	binding := gitlab.Binding{Version: 1, RoomID: id.RoomID, RoomInstance: id.RoomInstance, URL: q.Origin, Project: q.Project, PlatformID: connection.ID, PlatformGeneration: file.Platforms[0].Generation, ProjectID: q.ProjectID}
	fetcher, err := gitlab.NewEventFetcher(ctx, binding, read)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := fetcher.FetchEvents(ctx, gitlab.EventQuery{Mode: "quick", Page: 1, PerPage: 100})
	if err != nil {
		t.Fatal("real return Events unavailable", err)
	}
	selected := []gitlab.Event{}
	for _, event := range batch.Events {
		for _, c := range old.Cases {
			if event.Kind == "note" && fmt.Sprint(event.NoteID) == c.Action.Result.ResultID.Value {
				selected = append(selected, event)
			}
		}
	}
	if len(selected) != 2 {
		t.Fatal("two real authored comments absent from current project Events; no synthetic replacement")
	}
	state.ProjectID = fetcher.ProjectID()
	if _, _, err := state.Accept(selected, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	err = m.notifications.SaveLocked(state)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	items := []map[string]any{}
	for _, pending := range state.PendingItems() {
		item, exists := state.Notification(pending.Key)
		if !exists {
			t.Fatal("accepted native event disappeared")
		}
		for _, c := range old.Cases {
			if item.Source.Reference.NoteID != c.Action.Result.ResultID {
				continue
			}
			if !projectNotification(item).ContextOnly {
				t.Fatal("real native own-account event was not safe context")
			}
			e := InboxEntry{Version: 1, MemberIdentity: old.Member, Seq: 1, Item: item, ReceivedAt: time.Now().UTC()}
			senderStore.mu.Lock()
			origins, originErr := senderStore.inboxMessageOriginsLocked([]InboxEntry{e})
			senderStore.mu.Unlock()
			origin := origins[inboxEntryPosition(e)]
			if originErr != nil || origin.Kind != "agent" || origin.ActionID != c.ID {
				t.Fatal("real returned native event did not match original sender ledger", origin, originErr)
			}
			if err := svc.receiveInbox(ctx, inboxTarget(id), item); err != nil {
				t.Fatal(err)
			}
			found[c.ID] = true
			items = append(items, map[string]any{"item": item, "sender_ledger_origin": origin})
			state = state.Acknowledge(item.Key).(gitlab.EventsState)
		}
	}
	if len(found) != 2 {
		t.Fatal("two real authored comments absent from current project Events; no synthetic replacement")
	}
	m.mu.Lock()
	err = m.notifications.SaveLocked(state)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	closeService(t, svc)
	m.mu.Lock()
	delete(m.runtimes, id.RoomIdentity)
	m.mu.Unlock()
	room = assistantSummary(t, m)
	svc = openRoomOK(t, m, room, "gitlab-images-observer-open")
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool {
		return !s.Active && (s.Agent.TurnNumber >= 1 || s.Paused && s.Error != "")
	})
	if status.Error != "" || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
		t.Fatal("real own-account return turn failed", status.Error)
	}
	actions, err := svc.store.queryActions(id, "", "", "")
	matters, matterErr := svc.store.listFollowups(id, followupQuery{Limit: 25})
	if err != nil || matterErr != nil || len(actions) != 0 || matters.Total != 0 {
		t.Fatal("own return created action or new task", len(actions), matters.Total, err, matterErr)
	}
	preview, err := svc.previewInbox(ctx, apiRequest{ServiceInfo: svc.info, Caller: operator, AgentID: id.AgentID, MembershipID: id.MembershipID})
	if err != nil {
		t.Fatal(err)
	}
	report["return_events"], report["inbox_preview"], report["actions_after_return"] = items, preview, actions
	audit.mu.Lock()
	turns := append([]policyRealTurn(nil), audit.Turns...)
	audit.mu.Unlock()
	turns = gitlabImageFullToolTrace(t, svc, id, turns)
	report["full_turns"] = turns
	report["costs"] = gitlabImageAgentCosts(t, turns)
	report["receipt_provider_requests"], report["audit"] = requests, audit
	if os.Getenv("TEAMX_GITLAB_IMAGE_SEND_BROWSER") == "1" {
		ready := map[string]any{"page": server.URL, "member": id, "output": out, "notes": []string{old.Cases[0].Action.Result.URL, old.Cases[1].Action.Result.URL}}
		if err := os.WriteFile(filepath.Join(out, "browser-ready.json"), mustContractJSON(ready), 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("browser ready:", filepath.Join(out, "browser-ready.json"))
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-timer.C:
				t.Fatal("browser not acknowledged; screenshots required")
			case <-tick.C:
				if _, err := os.Stat(filepath.Join(out, "browser-done")); err == nil {
					report["browser"] = "external browser driver acknowledged; see retained screenshots"
					report["status"] = "passed"
					return
				}
			}
		}
	}
	report["status"] = "passed"
}

// Reopen the accepted observer for visual checks without another model turn,
// replaying intake, modifying consumed records, or touching the sender's input.
func TestRealGitlabImageSavedReturnBrowser(t *testing.T) {
	path := os.Getenv("TEAMX_GITLAB_IMAGE_SEND_BROWSER_REPORT")
	if !filepath.IsAbs(path) {
		t.Skip("explicit successful observer report required")
	}
	var prior struct {
		Status string         `json:"status"`
		Member MemberIdentity `json:"observer_member"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &prior) != nil || prior.Status != "passed" {
		t.Fatal("accepted real observer report required")
	}
	q, _ := gitlabImageSendRealSample(t)
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "remote_writes": 0, "model_calls": 0, "original_observer_report": path}
	defer imageRealReport(t, out, report)
	cfg, entry, credential := inputAcceptanceModel(t)
	const key = "TEAMX_GITLAB_IMAGE_SEND_MODEL_KEY"
	t.Setenv(key, credential.Value)
	runtime := *cfg.Runtime
	runtime.Model, runtime.Model.APIKeyEnv = entry.materialize(), key
	runtime.Model.InputModalities = []string{"text"}
	m := directTestManager(t, filepath.Join(filepath.Dir(path), "observer"), &runtime)
	m.maxTurns, m.config.unifiedInputs = 8, true
	server := browserTestServer(t, m)
	m.config.endpoint, m.config.cliPath = server.URL, productionFixtureCLI(t)
	m.factory = func(dir, id string, config serviceConfig) (*service, error) {
		return startService(dir, id, config, func(s *service, state RoomSnapshot) error {
			if err := s.loadMembers(state); err != nil {
				return err
			}
			return s.attachMembers(state)
		})
	}
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Scheme+"://"+r.URL.Host != q.Origin {
			return nil, platform.ErrCredential
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	room := assistantSummary(t, m)
	svc := openRoomOK(t, m, room, "gitlab-images-browser-"+filepath.Base(out))
	before, err := svc.store.LoadAgent(prior.Member)
	if err != nil {
		t.Fatal(err)
	}
	ready := map[string]any{"page": server.URL, "member": prior.Member, "output": out}
	if err := os.WriteFile(filepath.Join(out, "browser-ready.json"), mustContractJSON(ready), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("browser ready:", filepath.Join(out, "browser-ready.json"))
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	timer := time.NewTimer(4 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("browser not acknowledged")
		case <-tick.C:
			if _, err := os.Stat(filepath.Join(out, "browser-done")); err == nil {
				after, err := svc.store.LoadAgent(prior.Member)
				if err != nil || after.TurnNumber != before.TurnNumber {
					t.Fatal("browser check started an unexpected model turn", err)
				}
				report["status"] = "passed"
				return
			}
		}
	}
}

func gitlabImageReturnObserver(t *testing.T, out string, runtime *teamRuntime, q gitlabImageSendRealInput) (*roomManager, *service, MemberIdentity, *httptest.Server, *policyRealAudit, managedPlatform) {
	t.Helper()
	audit := &policyRealAudit{Calls: map[string]int{}}
	m := directTestManager(t, filepath.Join(out, "observer"), runtime)
	m.maxTurns, m.config.unifiedInputs = 8, true
	server := browserTestServer(t, m, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tools/run" {
			http.Error(w, "native proxy disabled for read-only observation", http.StatusForbidden)
			return
		}
		audit.serve(m).ServeHTTP(w, r)
	}))
	m.config.endpoint, m.config.cliPath = server.URL, productionFixtureCLI(t)
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Scheme+"://"+r.URL.Host != q.Origin {
			return nil, platform.ErrCredential
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	m.factory = func(dir, id string, config serviceConfig) (*service, error) {
		config.dshTurnTimeout = 3 * time.Minute
		return startService(dir, id, config, func(s *service, state RoomSnapshot) error {
			observePolicyReal(s, audit)
			if err := s.loadMembers(state); err != nil {
				return err
			}
			return s.attachMembers(state)
		})
	}
	ensureAssistant(t, m)
	room := assistantSummary(t, m)
	svc := eventLiveService(t, &eventHarness{m: m, room: room})
	_, snapshot := readManagedRoom(t, m, room)
	member := snapshot.Members[0]
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	connection := realOAuthPlatform(t, m, q.Origin)
	bindCLITool(t, m, room.RoomIdentity, member, connection, "gitlab", 0)
	if err := svc.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	m.bindPlatformReferences(svc)
	return m, svc, id, server, audit, connection
}
