//go:build darwin || linux

package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Finish only the local communication assertions after an already saved,
// successful remote action. Every action and platform tool RPC is blocked.
// This does not reconcile unknown writes or reexecute the original comment.
func TestHandlingContinueRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_AN_HANDLE_CONTINUE_REAL") != "1" {
		t.Skip("opt-in installed DSH continuation; remote GET only, no comment execution")
	}
	root := os.Getenv("TEAMX_AN_HANDLE_EVIDENCE")
	if !filepath.IsAbs(root) {
		t.Fatal("absolute retained evidence directory required")
	}
	var previous struct {
		Target string          `json:"target"`
		First  MatterPage      `json:"first_turn"`
		Audit  policyRealAudit `json:"audit"`
	}
	if err := jsonFile(filepath.Join(root, "report.json"), &previous); err != nil {
		t.Fatal(err)
	}
	if len(previous.First.Records) != 1 || len(previous.First.Records[0].Matters) != 1 {
		t.Fatal("exactly one retained matter required")
	}
	id := previous.First.Records[0].MemberIdentity
	main := previous.First.Records[0].Matters[0]
	if len(main.Actions) != 1 || main.Actions[0].Status != "succeeded" || main.Actions[0].Execution == nil || main.Status != "waiting_user" || len(main.Notices) != 2 || previous.Audit.Calls["/v1/actions/execute"] != 1 {
		t.Fatal("saved successful single-execute evidence and notices required; unknown actions cannot continue here")
	}
	action := main.Actions[0]
	if !actionDecimal(action.RemoteID) {
		t.Fatal("valid saved remote ID required")
	}
	file, err := os.OpenFile(filepath.Join(root, "continuation-report.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("fresh continuation report required", err)
	}
	audit := &policyRealAudit{Calls: map[string]int{}}
	report := map[string]any{"scenario": "policy-action-S3-local-continuation", "status": "failed", "original_report": "report.json", "target": previous.Target, "note_id": action.RemoteID, "remote_writes": 0}
	defer func() {
		audit.mu.Lock()
		defer audit.mu.Unlock()
		report["audit"] = audit
		if err := json.NewEncoder(file).Encode(report); err != nil {
			t.Error(err)
		}
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	get := func(path string, out any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, action.Target.Instance+"/api/v4/"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("PRIVATE-TOKEN", os.Getenv("GITLAB_PAT"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal("independent GET failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatal("GET status", resp.StatusCode)
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	type note struct {
		ID     int64  `json:"id"`
		Body   string `json:"body"`
		MRID   int64  `json:"noteable_id"`
		Author struct {
			Username string `json:"username"`
		} `json:"author"`
	}
	path := "projects/" + action.Execution.Target.ProjectID + "/merge_requests/" + action.Execution.Target.MRIID + "/notes"
	var remote note
	get(path+"/"+action.RemoteID, &remote)
	if strconv.FormatInt(remote.ID, 10) != action.RemoteID || remote.Body != action.Intent || remote.Author.Username != action.Target.Account || strconv.FormatInt(remote.MRID, 10) != action.Target.Object.ID.Value {
		t.Fatal("specific saved note identity/account/body mismatch")
	}
	report["verified_note"] = remote
	var notes []note
	get(path+"?per_page=100", &notes)
	if len(notes) >= 100 {
		t.Fatal("explicit pagination needed before evaluating duplicates")
	}
	matches := []int64{}
	for _, n := range notes {
		if n.Body == action.Intent && n.Author.Username == action.Target.Account {
			matches = append(matches, n.ID)
		}
	}
	report["matching_note_ids"] = matches
	report["duplicate_origin"] = "unverified; the saved action records one attempt; do not infer server retry"
	var profiles map[string]dshProfile
	if err := jsonFile(filepath.Join(root, "dsh-profiles.json"), &profiles); err != nil {
		t.Fatal(err)
	}
	p := profiles["reviewer"]
	p.Model.ContextWindow, p.Model.MaxTokens, p.Model.ReasoningEffort = 1048576, 32768, "low"
	// Recover the exact frozen role, rather than replacing a retained Session's
	// instructions. dsh.CheckResumeConfig independently verifies the full digest.
	instructions, err := os.ReadFile(filepath.Join(root, "rooms-root", "rooms", id.RoomID, "agents", id.MembershipID, "session.json.dsh", "home", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	role, _, ok := strings.Cut(string(instructions), "\n\nYou are member ")
	if !ok {
		t.Fatal("original frozen role not found")
	}
	p.Instructions = role
	m := testManager(t, filepath.Join(root, "rooms-root"), managerTestProfiles(t))
	server := httptest.NewServer(audit.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/actions/") || r.URL.Path == "/v1/tools/run" {
			writeAPIError(w, ErrRequest)
			return
		}
		m.ServeHTTP(w, r)
	})))
	defer server.Close()
	cfg := serviceConfig{endpoint: server.URL, cliPath: filepath.Join(root, "teamx"), maxTurns: 2, persistentScheduling: true, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": p}}
	svc, err := startService(m.roomDir(id.RoomID), id.RoomID, cfg, func(s *service, state RoomSnapshot) error {
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
	m.mu.Lock()
	m.runtimes[id.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	m.mu.Unlock()
	page, err := svc.store.listMatters(id, "", 0, 25)
	if err != nil || !reflect.DeepEqual(page, previous.First) {
		t.Fatal("retained matter differs from original report", err)
	}
	var question MatterNotice
	for _, n := range main.Notices {
		if n.ID == "finish-question" {
			question = n
		}
	}
	if question.Seq == 0 {
		t.Fatal("saved question required")
	}
	_, err = svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "acceptance-confirm", ReplyTo: question.Seq, Text: "已独立 GET 确认动作回执中的 note " + action.RemoteID + " 的目标、账号和准确正文，本地事项可以办结。另有同文评论的来源未核实，动作 attempts 为 1，不能归因为服务端重试；请纠正本地事项证据中的该推测。只关联本条回复并办结本地事项，不再发公开消息、评论或其他远端写入，不删除任何评论。"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber >= 2 })
	page, err = svc.store.listMatters(id, "", 0, 25)
	if err != nil || len(page.Records) != 1 {
		t.Fatal("continued matter missing", err)
	}
	final := page.Records[0].Matters[0]
	if status.Error != "" || status.Agent.Turn.Status != "completed" || final.Status != "completed" || !reflect.DeepEqual(final.Actions, main.Actions) {
		t.Fatal("local continuation failed or changed the saved action", status.Error)
	}
	linked := false
	for _, n := range final.Notices {
		if n.ID == question.ID && len(n.Answers) == 1 {
			linked = true
		}
	}
	state, err := svc.store.LoadRoom()
	if !linked || err != nil || len(state.Messages) != 3 {
		t.Fatal("reply linkage or public message count incorrect", err)
	}
	audit.mu.Lock()
	valid := audit.Calls["/v1/actions/execute"] == 0 && audit.Calls["/v1/tools/run"] == 0 && audit.Calls["/v1/policy/read"] == 0 && audit.Calls["/v1/decisions/read"] == 0 && len(audit.Turns) == 1 && audit.Turns[0].EffectiveBytes > 0
	audit.mu.Unlock()
	if !valid {
		t.Fatal("continuation attempted unnecessary policy reads, tools or actions; inspect audit")
	}
	report["status"], report["final"], report["turns_used"], report["room_messages"] = "passed", page, status.TurnBudget.Used, len(state.Messages)
	t.Log("retained exact successful comment verified with GET; same DSH Session resumed with full policy; local human reply linked and matter completed; no new tool/action calls")
}
