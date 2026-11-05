//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Deliberately narrow syntax for the opt-in live fixture, not a general glab
// classifier: api PATH --method GET [--hostname HOST] [--paginate].
func handlingRealGET(args []string, host string) bool {
	if reflect.DeepEqual(args, []string{"--help"}) || reflect.DeepEqual(args, []string{"api", "--help"}) {
		return true
	}
	if len(args) < 4 || args[0] != "api" || strings.HasPrefix(args[1], "-") || strings.Contains(args[1], "://") || args[2] != "--method" || args[3] != "GET" {
		return false
	}
	for i := 4; i < len(args); i++ {
		switch args[i] {
		case "--paginate":
		case "--hostname":
			if i+1 >= len(args) || args[i+1] != host {
				return false
			}
			i++
		default:
			return false
		}
	}
	return true
}

func TestHandlingRealGETBoundary(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		allowed bool
	}{
		{[]string{"--help"}, true},
		{[]string{"api", "--help"}, true},
		{[]string{"api", "user", "--method", "GET"}, true},
		{[]string{"api", "projects/123/merge_requests/3/notes", "--method", "GET", "--hostname", "gitlab.example", "--paginate"}, true},
		{[]string{"api", "user"}, false},
		{[]string{"api", "user", "--method", "POST"}, false},
		{[]string{"api", "user", "--method", "GET", "-X", "POST"}, false},
		{[]string{"api", "user", "--method", "GET", "--raw-field", "body=unapproved"}, false},
		{[]string{"api", "user", "--method", "GET", "--hostname", "other.example"}, false},
		{[]string{"api", "https://other.example/api/v4/user", "--method", "GET"}, false},
		{[]string{"mr", "note", "create", "3"}, false},
	} {
		if got := handlingRealGET(tc.args, "gitlab.example"); got != tc.allowed {
			t.Fatalf("%v allowed=%v, want %v", tc.args, got, tc.allowed)
		}
	}
}

// A manually posted identical note must not invalidate the independently
// verified ID returned by this action, or be mistaken for its retry count.
type handlingRealNote struct {
	ID     int64  `json:"id"`
	Body   string `json:"body"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
}

func handlingRealMatches(notes []handlingRealNote, body, account, remoteID string) (count int, verified bool) {
	for _, n := range notes {
		if n.Body == body && n.Author.Username == account {
			count++
			verified = verified || strconv.FormatInt(n.ID, 10) == remoteID
		}
	}
	return
}

func TestHandlingRealReceiptWithManualSameTextNote(t *testing.T) {
	manual, sent := handlingRealNote{ID: 10, Body: "exact"}, handlingRealNote{ID: 11, Body: "exact"}
	manual.Author.Username, sent.Author.Username = "bot", "bot"
	notes := []handlingRealNote{manual, sent}
	if count, verified := handlingRealMatches(notes, "exact", "bot", "11"); count != 2 || !verified {
		t.Fatal("manual note preceding the action's exact ID invalidated receipt")
	}
	for _, q := range [][3]string{{"different", "bot", "11"}, {"exact", "other", "11"}, {"exact", "bot", "12"}} {
		if _, verified := handlingRealMatches(notes, q[0], q[1], q[2]); verified {
			t.Fatal("wrong body/account/remote ID accepted", q)
		}
	}
}

// Recheck the retained real run with current code, without a model or network.
// This is also the safe entry point after a successful/uncertain live run;
// TestHandlingRealDSH itself must never be used as a write retry.
func TestHandlingSavedRealEvidence(t *testing.T) {
	root := os.Getenv("TEAMX_AN_HANDLE_EVIDENCE")
	if root == "" {
		t.Skip("set TEAMX_AN_HANDLE_EVIDENCE for offline recovery of a retained run")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute evidence directory required")
	}
	var report struct {
		Status string     `json:"status"`
		Final  MatterPage `json:"final"`
	}
	reportName := os.Getenv("TEAMX_AN_HANDLE_REPORT")
	if reportName == "" {
		reportName = "report.json"
	}
	if reportName != "report.json" && reportName != "continuation-report.json" {
		t.Fatal("unsupported evidence report")
	}
	if err := jsonFile(filepath.Join(root, reportName), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "passed" || len(report.Final.Records) != 1 {
		t.Fatal("successful complete real-run report required")
	}
	id := report.Final.Records[0].MemberIdentity
	store, err := OpenStore(filepath.Join(root, "rooms-root", "rooms", id.RoomID), id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page, err := store.listMatters(id, "", 0, 25)
	if err != nil || !reflect.DeepEqual(page, report.Final) {
		t.Fatal("recovered evidence differs from the actual real run", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rt, err := actorx.NewRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(ctx)
	inbox, err := SpawnAgentInbox(rt, store, id, InboxLimits{})
	if err != nil {
		t.Fatal(err)
	}
	status, err := inbox.Call[InboxStatus](ctx, InspectInbox{id})
	if err != nil || status.ConsumedThrough != 1 || status.LatestSeq != 1 {
		t.Fatal("real Inbox progress not recovered", status, err)
	}
	t.Log("current code recovered real action, source, result, notices, user reply and Inbox consumption; no model or remote calls")
}

// Opt-in real write: the operator must explicitly supply the approved MR URL,
// exact comment and a NEW evidence directory. Never rerun after an uncertain
// write: inspect the retained record and query the MR first. The operator
// accepts that the server may retry a transient failure once (possible duplicate).
func TestHandlingRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_AN_HANDLE_REAL") != "1" {
		t.Skip("explicit real MR comment authorization required")
	}
	target := os.Getenv("TEAMX_AN_HANDLE_MR")
	body := os.Getenv("TEAMX_AN_HANDLE_BODY")
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || !handlingText(body) {
		t.Fatal("exact HTTPS MR URL and comment required")
	}
	project, iidText, ok := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/-/merge_requests/")
	iid, err := strconv.ParseInt(iidText, 10, 64)
	if !ok || err != nil || iid <= 0 {
		t.Fatal("invalid MR target")
	}
	origin := u.Scheme + "://" + u.Host
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("new absolute TEAMX_DSH_OUTPUT required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(output, "driver-room")}, output: output, report: map[string]any{"scenario": "policy-action-S3-comment", "status": "failed", "target": target}}
	audit := &policyRealAudit{Calls: map[string]int{}}
	defer func() {
		audit.mu.Lock()
		defer audit.mu.Unlock()
		d.report["audit"] = audit
		data, _ := json.MarshalIndent(d.report, "", "  ")
		if err := os.WriteFile(filepath.Join(output, "report.json"), append(data, '\n'), 0600); err != nil {
			t.Error(err)
		}
	}()
	if err := d.setup(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(d.report["apiKeyEnv"].(string), d.secret)
	// Build the adapter from this checkout; a retained pre-S2 executable is not
	// evidence for actions execute. glab remains the explicitly configured pin.
	toolBinary := filepath.Join(output, "teamx-gitlab")
	if err := exec.CommandContext(ctx, "go", "build", "-o", toolBinary, "../../tools/gitlab").Run(); err != nil {
		t.Fatal("build current GitLab tool", err)
	}
	t.Setenv("TEAMX_GITLAB_BIN", toolBinary)
	if os.Getenv("GITLAB_PAT") == "" {
		t.Fatal("GITLAB_PAT required via environment")
	}
	get := func(path string, value any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, "GET", origin+"/api/v4/"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("PRIVATE-TOKEN", os.Getenv("GITLAB_PAT"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal("real GET failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal("real GET status", resp.StatusCode)
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(value); err != nil {
			t.Fatal(err)
		}
	}
	var mr struct {
		ID        int64  `json:"id"`
		IID       int64  `json:"iid"`
		ProjectID int64  `json:"project_id"`
		WebURL    string `json:"web_url"`
	}
	get("projects/"+url.PathEscape(project)+"/merge_requests/"+iidText, &mr)
	if mr.ID <= 0 || mr.IID != iid || mr.WebURL != target {
		t.Fatal("MR identity mismatch")
	}
	notesPath := fmt.Sprintf("projects/%d/merge_requests/%d/notes?per_page=100", mr.ProjectID, iid)

	var before []handlingRealNote
	get(notesPath, &before)
	if len(before) >= 100 {
		t.Fatal("notes exceed the acceptance query budget; paginate and verify before authorizing any write")
	}
	for _, n := range before {
		if n.Body == body {
			t.Fatal("approved comment already exists; reconcile it instead of rerunning")
		}
	}
	d.report["preflight"] = map[string]any{"checked_at": time.Now().UTC(), "existing_exact_comments": 0, "notes_read": len(before)}
	var profiles map[string]dshProfile
	if err := jsonFile(d.profilePath, &profiles); err != nil {
		t.Fatal(err)
	}
	p := profiles["reviewer"]
	p.Model.ContextWindow, p.Model.MaxTokens, p.Model.ReasoningEffort = 1048576, 32768, "low"
	p.Instructions = fmt.Sprintf(`You are a bounded S3 acceptance assistant. The ONLY remote logical write allowed is ONE ordinary comment on %s with exact body %q. Never approve, merge, close, mark Todos done, or write anything else. Use only configured TeamX commands; read complete JSON output without head/truncation. Never inspect credentials/files/environment, edit files, delegate or loop in background.
On the first Inbox batch, read inbox and matters (including effective decisions), use runtime-supplied policy and current_turn, discover tools/account, GET the exact MR and its notes with tools run gitlab api PATH --method GET (optional --hostname HOST and --paginate; this fixture accepts only that GET syntax and tools help), verify ID/IID/project/account and no duplicate body, then call actions execute ONCE with stable id acceptance-comment, supplied policy_token, active exact rule/version and current matter version. Use operation gitlab.mr.comment, target project_id %q and mr_iid %q, and input.body equal to the exact authorized body. The server saves intent, executes and records the result; its bounded transient retry may duplicate the comment. Do not manually POST with tools run or call retired action commands. Do not layer retries or change the ID after uncertainty. The full policy body is supplied at the start of this first turn: use it directly and do not query policy just to confirm it is available. Only restore policy with policy read if its body is actually missing; no fixed policy or decision queries.
If execute reports succeeded, GET notes afterwards and independently verify note ID/account/body, then update the matter to waiting_followup with the condition that the operator checks the comment. Save actual note URL/ID in evidence. If unknown or not_applied, keep an accurate unresolved matter and report the result; never claim success or complete it. Notify one important result (id comment-result, kind result) and one necessary question (id finish-question, kind question, mention the human): has the operator checked this acceptance comment so this local matter can be completed? End privately and wait; no more remote writes.
On the next Room turn, read the human reply and existing matter, link it to finish-question with matters reply. If the user confirms the successful comment, record completed with existing remote evidence and that exact human reply. Do not send another public message, ask again, create any standing rule or call a remote write. All model output stays private except the two explicit matter notices.`, target, body, strconv.FormatInt(mr.ProjectID, 10), iidText)
	m := testManager(t, filepath.Join(output, "rooms-root"), managerTestProfiles(t))
	room := managerCreate(t, m, "policy-action-s3")
	// Bound this live fixture to the exact approved logical comment. Raw
	// platform commands are GET-only here; production tool routing is unchanged.
	var executions atomic.Int32
	server := httptest.NewServer(audit.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/actions/execute" || r.URL.Path == "/v1/tools/run" {
			data, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			if err != nil {
				writeAPIError(w, ErrRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
			allowed := false
			if r.URL.Path == "/v1/actions/execute" {
				var req apiRequest
				if json.Unmarshal(data, &req) == nil && req.ActionRequest != nil {
					q := req.ActionRequest
					allowed = q.ID == "acceptance-comment" && q.Operation == "gitlab.mr.comment" && q.Target == (CommentTarget{strconv.FormatInt(mr.ProjectID, 10), iidText}) && q.Input.Body == body && executions.Add(1) == 1
				}
			} else {
				var req toolsRequest
				allowed = json.Unmarshal(data, &req) == nil && req.Tool == "gitlab" && handlingRealGET(req.Args, u.Host)
			}
			if !allowed {
				writeAPIError(w, ErrRequest)
				return
			}
		}
		m.ServeHTTP(w, r)
	})))
	defer server.Close()
	svc, err := newService(m.roomDir(room.RoomID), room.RoomID, serviceConfig{endpoint: server.URL, cliPath: d.binary, maxTurns: 2, persistentScheduling: true, dshTurnTimeout: 5 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": p}})
	if err != nil {
		t.Fatal(err)
	}
	observePolicyReal(svc, audit)
	defer func() {
		closeCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if err := svc.close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	m.mu.Lock()
	m.runtimes[room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	m.mu.Unlock()
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	conn, err := m.updatePlatformConnection(platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("a", 32), Name: "S3 scoped acceptance", Provider: "gitlab", URL: origin, TokenEnv: "GITLAB_PAT"}, false)
	if err != nil {
		t.Fatal("connection save failed")
	}
	conn, err = m.verifyPlatformConnection(ctx, platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: conn.ID, Version: conn.Version})
	if err != nil || conn.Validation.Status != "valid" {
		t.Fatal("account verification failed")
	}
	d.report["account"] = conn.Validation.Username
	bindCLITool(t, m, room.RoomIdentity, member.MemberState, conn, "gitlab", 0)
	rule := PolicyChange{ID: "acceptance-comment", Kind: "rule", Status: "active", Text: "在指定 MR 发送一条准确的验收评论", Scope: PolicyScope{Description: target, Provider: "gitlab", Instance: origin, Account: conn.Validation.Username, Project: strconv.FormatInt(mr.ProjectID, 10), ObjectType: "MergeRequest"},
		Actions: "仅发送一条评论，准确内容：" + body, Conditions: "先核实当前对象、账号及评论没有重复；通过 actions execute 保存意图并执行，接受暂时故障最多尝试两次可能重复", Exceptions: "禁止其他对象、其他内容及其他写入；最终未知先查询，不重建动作或自行补发", SourceText: "用户明确提供此 MR 作为单评论验收对象；测试仅允许指定内容"}
	if _, err := svc.store.changePolicy(id, humanCaller, rule); err != nil {
		t.Fatal(err)
	}
	// Real existing Events API data, using the production source adapter and
	// template. Only the approved MR's event is delivered in this isolated store.
	binding := gitlab.Binding{Version: 1, RoomID: id.RoomID, RoomInstance: id.RoomInstance, URL: origin, Project: project, TokenEnv: "GITLAB_PAT"}
	fetcher, err := gitlab.NewEventFetcher(ctx, binding, gitlab.NewReadAPI(origin, platform.NewSecret(os.Getenv("GITLAB_PAT")), nil))
	if err != nil {
		t.Fatal(err)
	}
	var selected *gitlab.Event
	for page := 1; page <= 3; page++ {
		batch, err := fetcher.FetchEvents(ctx, gitlab.EventQuery{Mode: "quick", Page: page, PerPage: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range batch.Events {
			if e.TargetType == "MergeRequest" && e.TargetID == mr.ID && e.TargetIID == iid {
				copy := e
				selected = &copy
				break
			}
		}
		if selected != nil || batch.Complete {
			break
		}
	}
	if selected == nil {
		t.Fatal("no real existing event for the approved MR; do not fabricate source evidence")
	}
	item := gitlab.EventsMessage(gitlab.EventsState{Binding: platform.ProjectBinding{Provider: "gitlab", URL: origin, Project: project, TokenEnv: "GITLAB_PAT"}, ProjectID: mr.ProjectID, UserID: conn.Validation.UserID, SubscriptionID: strings.Repeat("b", 32)}, gitlab.EventsInboxEntry{Event: *selected, DeliveryID: "s3-comment-event-" + strconv.FormatInt(selected.ID, 10)})
	if err := svc.receiveInbox(ctx, notification.Target{Mode: "inbox", AgentID: id.AgentID, MembershipID: id.MembershipID}, item); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber >= 1 })
	if status.Error != "" || status.Agent.Turn.Status != "completed" {
		t.Fatal("DSH handling turn failed; query remote before any retry", status.Error)
	}
	page, err := svc.store.listMatters(id, "", 0, 25)
	if err != nil || len(page.Records) != 1 {
		t.Fatal("missing matter", err)
	}
	main := page.Records[0].Matters[0]
	d.report["first_turn"] = page
	var after []handlingRealNote
	get(notesPath, &after)
	remoteID := ""
	if len(main.Actions) == 1 && main.Actions[0].Execution != nil && main.Actions[0].Execution.Input.Body == body {
		remoteID = main.Actions[0].RemoteID
	}
	count, verifiedReceipt := handlingRealMatches(after, body, conn.Validation.Username, remoteID)
	if verifiedReceipt {
		d.report["note_id"] = remoteID
	}
	d.report["matching_comments"] = count
	if len(main.Actions) == 0 && count > 0 {
		t.Fatal("exact comment exists but this Agent did not execute it; preserve evidence, do not resend or claim chain acceptance")
	}
	if len(main.Actions) != 1 || main.Actions[0].Status != "succeeded" || main.Actions[0].Execution == nil || main.Actions[0].Attempts < 1 || main.Actions[0].Attempts > 2 || len(main.Notices) != 2 || main.Status != "waiting_user" {
		t.Fatal("missing action/communication evidence; inspect retained store")
	}
	if !verifiedReceipt {
		t.Fatal("independent query did not verify the exact saved remote ID/account/body")
	}
	if count > 1 {
		d.report["duplicate_observation"] = "Additional identical comments observed; do not infer their origin or attribute them to retries without evidence."
	}
	var question MatterNotice
	for _, n := range main.Notices {
		if n.ID == "finish-question" {
			question = n
		}
	}
	if question.Seq == 0 {
		t.Fatal("no durable public question receipt")
	}
	_, err = svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "acceptance-confirm", ReplyTo: question.Seq, Text: "已独立查询确认验收评论，本事项可以办结。只办结本地事项，不再发送任何评论或其他远端写入。"})
	if err != nil {
		t.Fatal(err)
	}
	status = waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber >= 2 })
	page, err = svc.store.listMatters(id, "", 0, 25)
	if err != nil || len(page.Records) != 1 {
		t.Fatal(err)
	}
	main = page.Records[0].Matters[0]
	if status.Error != "" || status.Agent.Turn.Status != "completed" || main.Status != "completed" || len(main.Actions) != 1 {
		t.Fatal("reply did not resume and complete the local matter", status.Error)
	}
	linked := false
	for _, n := range main.Notices {
		if n.ID == question.ID && len(n.Answers) == 1 {
			linked = true
		}
	}
	if !linked {
		t.Fatal("human reply evidence not linked")
	}
	state, err := svc.store.LoadRoom()
	if err != nil || len(state.Messages) != 3 {
		t.Fatal("unexpected raw notice or duplicate public output", err)
	}
	audit.mu.Lock()
	validCalls := audit.Calls["/v1/actions/execute"] == 1 && audit.Calls["/v1/actions/prepare"] == 0 && audit.Calls["/v1/actions/record"] == 0 && audit.Calls["/v1/policy/read"] == 0 && audit.Calls["/v1/decisions/read"] == 0
	validSupply := len(audit.Turns) == 2 && audit.Turns[0].EffectiveBytes > 0 && audit.Turns[1].EffectiveBytes == 0
	audit.mu.Unlock()
	if !validCalls || !validSupply {
		t.Fatal("actual Agent did not use the single execute / runtime policy flow; inspect retained audit")
	}
	d.report["comparison"] = map[string]any{"task": "one authorized ordinary MR comment", "policy_read_calls": 0, "decision_read_calls": 0, "action_calls": 1, "legacy_protocol_action_calls": 2, "legacy_protocol_direct_writes": 1, "legacy_counts": "specified old fixture sequence, not a measured baseline model run", "attempts": main.Actions[0].Attempts, "cost_measured": false}
	d.report["status"], d.report["final"], d.report["turns_used"], d.report["room_messages"] = "passed", page, status.TurnBudget.Used, len(state.Messages)
	t.Log("real Events -> Inbox -> installed DSH -> ONE actions execute -> independently verified MR comment -> Room question/reply -> completed local matter; fixed policy/decision queries=0")
}
