//go:build e2e && (darwin || linux)

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
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/tool"
)

type referenceLiveNote struct {
	ID     int64  `json:"id"`
	Body   string `json:"body"`
	Author struct {
		ID int64 `json:"id"`
	} `json:"author"`
}
type referenceLiveDiscussion struct {
	ID         string              `json:"id"`
	Individual bool                `json:"individual_note"`
	Notes      []referenceLiveNote `json:"notes"`
}

// Real API + built-in adapter + manager/CLI/Store. This test doesn't
// substitute an API transport or involve a model. Each send has a durable
// one-attempt gate; reruns require a new directory, and duplicate bodies refuse.
func TestReferenceFollowupsRealGitLab(t *testing.T) {
	if os.Getenv("TEAMX_REFERENCE_GITLAB_REAL") != "1" {
		t.Skip("explicit opt-in MR, bodies and fresh output directory required")
	}
	output, target := os.Getenv("TEAMX_REFERENCE_OUTPUT"), os.Getenv("TEAMX_REFERENCE_MR")
	body, replyBody := os.Getenv("TEAMX_REFERENCE_COMMENT_BODY"), os.Getenv("TEAMX_REFERENCE_REPLY_BODY")
	threadURL := os.Getenv("TEAMX_REFERENCE_DISCUSSION")
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || !filepath.IsAbs(output) || (body != "" && !handlingText(body)) || (body == "" && (!handlingText(replyBody) || threadURL == "" && os.Getenv("TEAMX_REFERENCE_CREATE_DISCUSSION") != "1")) {
		t.Fatal("explicit exact MR, comment body and fresh absolute output required")
	}
	project, iid, ok := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/-/merge_requests/")
	iidNumber, parseErr := strconv.ParseInt(iid, 10, 64)
	if !ok || parseErr != nil || iidNumber <= 0 || strconv.FormatInt(iidNumber, 10) != iid || os.Getenv("GITLAB_PAT") == "" {
		t.Fatal("MR IID and GITLAB_PAT required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	origin := u.Scheme + "://" + u.Host
	report := map[string]any{"scenario": "R6-real-GitLab-reference-actions", "status": "failed", "target": target, "comment_body": body, "reply_body": replyBody, "recorded_at": time.Now().UTC(), "model": "not involved"}
	defer func() {
		b, e := json.MarshalIndent(report, "", "  ")
		if e == nil {
			e = os.WriteFile(filepath.Join(output, "report.json"), append(b, '\n'), 0600)
		}
		if e != nil {
			t.Error(e)
		}
		t.Log("retained report:", filepath.Join(output, "report.json"))
	}()
	api := func(method, path string, input any, out any) http.Header {
		t.Helper()
		var payload io.Reader
		if input != nil {
			payload = bytes.NewReader(mustContractJSON(input))
		}
		req, e := http.NewRequestWithContext(ctx, method, origin+"/api/v4/"+path, payload)
		if e != nil {
			t.Fatal("invalid API request")
		}
		req.Header.Set("PRIVATE-TOKEN", os.Getenv("GITLAB_PAT"))
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, e := client.Do(req)
		if e != nil {
			t.Fatal("real API request failed; preserve state and do not replay writes")
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			t.Fatalf("real API %s failed HTTP %d", method, res.StatusCode)
		}
		if out != nil && json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out) != nil {
			t.Fatal("invalid API response; preserve state")
		}
		return res.Header
	}
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	var mr struct {
		ID        int64  `json:"id"`
		IID       int64  `json:"iid"`
		ProjectID int64  `json:"project_id"`
		WebURL    string `json:"web_url"`
	}
	api("GET", "user", nil, &user)
	api("GET", "projects/"+url.PathEscape(project)+"/merge_requests/"+iid, nil, &mr)
	if user.ID <= 0 || mr.ID <= 0 || mr.ProjectID <= 0 || fmt.Sprint(mr.IID) != iid || mr.WebURL != target {
		t.Fatal("remote account/object mismatch")
	}
	report["account_id"], report["account"], report["mr_id"], report["project_id"] = user.ID, user.Username, mr.ID, mr.ProjectID
	base := fmt.Sprintf("projects/%d/merge_requests/%s", mr.ProjectID, iid)
	var discussions []referenceLiveDiscussion
	for page := 1; ; page++ {
		var rows []referenceLiveDiscussion
		h := api("GET", fmt.Sprintf("%s/discussions?per_page=100&page=%d", base, page), nil, &rows)
		discussions = append(discussions, rows...)
		if h.Get("X-Next-Page") == "" {
			break
		}
		if page >= 10 || h.Get("X-Next-Page") != fmt.Sprint(page+1) {
			t.Fatal("discussion pagination incomplete")
		}
	}
	for _, d := range discussions {
		for _, n := range d.Notes {
			if n.Body == body || replyBody != "" && n.Body == replyBody {
				t.Fatal("authorized body already exists; reconcile instead of resending")
			}
		}
	}
	var discussion referenceLiveDiscussion
	var noteID int64
	if threadURL != "" {
		parsed, e := url.Parse(threadURL)
		if e != nil || strings.Split(threadURL, "#")[0] != target || !strings.HasPrefix(parsed.Fragment, "note_") || !handlingText(replyBody) {
			t.Fatal("exact discussion note URL and reply body required")
		}
		noteID, e = strconv.ParseInt(strings.TrimPrefix(parsed.Fragment, "note_"), 10, 64)
		if e != nil || noteID <= 0 {
			t.Fatal("invalid note URL")
		}
		for _, d := range discussions {
			for _, n := range d.Notes {
				if n.ID == noteID {
					discussion = d
				}
			}
		}
		if discussion.ID == "" || discussion.Individual {
			t.Fatal("selected note is not a native discussion; no fallback or write")
		}
	}
	// An operator may separately authorize creating a bounded fixture thread.
	if os.Getenv("TEAMX_REFERENCE_CREATE_DISCUSSION") == "1" {
		if threadURL != "" || !handlingText(replyBody) {
			t.Fatal("choose existing or explicitly authorized new discussion")
		}
		seed := os.Getenv("TEAMX_REFERENCE_DISCUSSION_BODY")
		if !handlingText(seed) {
			t.Fatal("exact authorized fixture discussion body required")
		}
		report["fixture_discussion_body"] = seed
		for _, d := range discussions {
			for _, n := range d.Notes {
				if n.Body == seed {
					t.Fatal("fixture discussion already exists; reconcile")
				}
			}
		}
		// Save the exact authorized intent before the sole fixture POST.
		if e := writeRoomResource(filepath.Join(output, "fixture-discussion-intent.json"), mustContractJSON(map[string]string{"target": target, "body": seed}), 0600); e != nil {
			t.Fatal(e)
		}
		api("POST", base+"/discussions", map[string]string{"body": seed}, &discussion)
		if discussion.ID == "" || discussion.Individual || len(discussion.Notes) != 1 || discussion.Notes[0].Body != seed || discussion.Notes[0].Author.ID != user.ID {
			t.Fatal("fixture POST result unverified; do not replay")
		}
		noteID = discussion.Notes[0].ID
		report["created_fixture"] = discussion
		if e := writeRoomResource(filepath.Join(output, "fixture-discussion-result.json"), mustContractJSON(discussion), 0600); e != nil {
			t.Fatal(e)
		}
	}
	// Internal resolution/actions must work without the independent Agent CLI.
	t.Setenv("TEAMX_GITLAB_BIN", filepath.Join(output, "missing-teamx-gitlab"))
	report["execution_path"] = "built-in GitLab HTTP adapter; no tool process"

	m := testManager(t, filepath.Join(output, "rooms-root"), managerTestProfiles(t))
	r := managerCreate(t, m, "r6-gitlab")
	svc := eventLiveService(t, &eventHarness{m: m, room: r})
	// Share the production static provider registry. Only execution is wrapped
	// to enforce this acceptance's one-send-per-operation budget on disk.
	m.bindPlatformReferences(svc)
	state, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	member := state.Members[0]
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	report["member"] = id
	conn, err := m.updatePlatformConnection(platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("a", 32), Name: "R6 authorized acceptance", Provider: "gitlab", URL: origin, TokenEnv: "GITLAB_PAT"}, false)
	if err != nil {
		t.Fatal("connection save failed")
	}
	conn, err = m.verifyPlatformConnection(ctx, platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: conn.ID, Version: conn.Version})
	if err != nil || conn.Validation.Status != "valid" || conn.Validation.UserID != user.ID {
		t.Fatal("connection account verification failed")
	}
	m.toolCatalog = tool.Catalog()
	bindCLITool(t, m, r.RoomIdentity, member, conn, "gitlab", 0)
	original := m.referenceExecutors["gitlab"]
	m.referenceExecutors["gitlab"] = func(ctx context.Context, id MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
		session, e := original(ctx, id, old)
		if e != nil {
			return session, e
		}
		for op, capability := range session.Capabilities.Actions {
			capability.Execute = func(send platform.ExecuteTargetAction, op platform.Operation) platform.ExecuteTargetAction {
				return func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
					want := body
					if op == platform.OperationDiscussionReply {
						want = replyBody
					}
					var input CommentInput
					if strictToolJSON(q.Input, &input) != nil || input.Body != want || q.Target.Object.ID.Value != fmt.Sprint(mr.ID) || len(q.Target.Containers) != 1 || q.Target.Containers[0].ID.Value != fmt.Sprint(mr.ProjectID) || a.Account.ID.Value != fmt.Sprint(user.ID) || (op == platform.OperationDiscussionReply && q.Target.Discussion.Value != discussion.ID) {
						return platform.TargetActionResult{}, ErrIdentity
					}
					f, e := os.OpenFile(filepath.Join(output, string(op)+".attempt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if e != nil {
						return platform.TargetActionResult{}, ErrConflict
					}
					if e = f.Sync(); e != nil {
						_ = f.Close()
						return platform.TargetActionResult{}, e
					}
					if e = f.Close(); e != nil {
						return platform.TargetActionResult{}, e
					}
					if e = syncDirectory(output); e != nil {
						return platform.TargetActionResult{}, e
					}
					return send(ctx, q, a)
				}
			}(capability.Execute, op)
			session.Capabilities.Actions[op] = capability
		}
		return session, nil
	}
	server := httptest.NewServer(m)
	defer server.Close()
	cli := contractCLI(t, server.URL, svc, id)
	ext := func(n int64, ns string) platform.ExternalID {
		return platform.ExternalID{Namespace: "gitlab:rest:" + ns, Value: fmt.Sprint(n)}
	}
	source := platform.SourceRef{Provider: "gitlab", Instance: origin, Channel: "events", SubscriptionID: strings.Repeat("b", 32), ItemKey: "approved-object", URL: target,
		Reference: platform.NotificationReference{Version: 1, RecipientID: ext(user.ID, "id"), Scope: "project", ProjectID: ext(mr.ProjectID, "id"), Object: platform.ObjectReference{Type: "MergeRequest", ID: ext(mr.ID, "id"), IID: ext(mr.IID, "iid")}}}
	contractDeliver(t, m, svc, id, source, "用户授权的 R6 MR 普通评论验收；原生 ID 已通过真实 GET 核实。")
	count := uint64(1)
	if discussion.ID != "" {
		source.ItemKey, source.URL, source.Reference.NoteID = "approved-discussion", target+"#note_"+fmt.Sprint(noteID), ext(noteID, "id")
		contractDeliver(t, m, svc, id, source, "用户授权的 R6 已有讨论回复验收；必须解析原生讨论，不得降级。")
		count++
	}
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: count, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: count}}); err != nil {
		t.Fatal(err)
	}
	var page agentReferenceInboxPage
	if err := cli([]string{"inbox", "read"}, &page); err != nil || len(page.Followups) != 1 || len(page.Entries) != int(count) {
		t.Fatal("reference read failed", err)
	}
	u0, err := svc.store.readFollowup(id, page.Followups[0].ObjectRef, page.Followups[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	units := []followupUnit{u0}
	if discussion.ID != "" {
		var thread agentFollowupReceipt
		if err := cli([]string{"matters", "ensure", "--seq", "2", "--target", page.Entries[1].Source.TargetRef, "--turn", "1"}, &thread); err != nil {
			t.Fatal(err)
		}
		stored, err := svc.store.readFollowup(id, thread.ObjectRef, thread.UnitRef)
		if err != nil {
			t.Fatal(err)
		}
		units = append(units, stored)
	}
	var receipts []referenceActionReceipt
	for i, unit := range units {
		if i == 0 && body == "" {
			continue
		} // separately authorized discussion-only continuation
		op, exact := platform.OperationObjectComment, body
		if i == 1 {
			op, exact = platform.OperationDiscussionReply, replyBody
		}
		question := followupQuestionChange{followupCommand: conversationCommand(unit, 1), Text: "@human 请确认本次授权的 R6 验收正文：" + exact, Proposed: &followupIntent{Operation: op, Body: exact}}
		var notified agentFollowupReceipt
		if err := cli(entryChange(t, "matters", "notify", question), &notified); err != nil {
			t.Fatal(err)
		}
		answer, e := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: fmt.Sprintf("approved-%d", i), ReplyTo: notified.Question.Seq, Text: "授权在上述准确目标发送这条验收正文。"})
		if e != nil {
			t.Fatal(e)
		}
		var detail struct {
			Unit agentFollowupUnit `json:"unit"`
		}
		if err := cli([]string{"matters", "read", "--object", unit.ObjectRef, "--unit", unit.Ref, "--turn", "1"}, &detail); err != nil {
			t.Fatal(err)
		}
		unit = followupFixture(detail.Unit)
		policy, e := svc.store.readAgentPolicy(id)
		if e != nil {
			t.Fatal(e)
		}
		q := referenceActionRequest{followupCommand: conversationCommand(unit, 1), TargetRef: unit.TargetRef, ID: fmt.Sprintf("approved-%d", i), PolicyToken: policy.PolicyToken, Authority: referenceActionAuthority{RoomSeq: answer.Seq, QuestionRef: unit.Questions[0].Ref, Reason: "用户已明确授权此 MR／讨论及验收正文，当前答复确认准确目标和正文"}, Operation: op, Input: CommentInput{Body: exact}}
		var receipt referenceActionReceipt
		if err := cli(referenceExecuteArgs(t, q), &receipt); err != nil {
			t.Fatal("execute failed; inspect retained state, do not replay", err)
		}
		receipts = append(receipts, receipt)
		report["receipts"] = receipts
		if receipt.Status != "succeeded" || receipt.Result == nil || receipt.Attempts != 1 {
			t.Fatal("remote result unverified; preserve receipt and stop")
		}
		var note referenceLiveNote
		api("GET", base+"/notes/"+receipt.Result.ResultID.Value, nil, &note)
		if fmt.Sprint(note.ID) != receipt.Result.ResultID.Value || note.Body != exact || note.Author.ID != user.ID {
			t.Fatal("independent note check failed")
		}
		if i == 1 {
			var d referenceLiveDiscussion
			api("GET", base+"/discussions/"+discussion.ID, nil, &d)
			found := false
			for _, n := range d.Notes {
				if n.ID == note.ID {
					found = true
				}
			}
			if d.ID != discussion.ID || !found {
				t.Fatal("reply not found in exact original discussion")
			}
		}
		if err := cli(referenceExecuteArgs(t, q), &receipt); err != nil || receipt.Status != "succeeded" {
			t.Fatal("same ID receipt read failed", err)
		}
	}
	report["independent_GET_verification"], report["status"] = "passed", "passed"
	report["notification_source"] = "operator-selected real API evidence; isolated delivery, not live subscription polling"
	if os.Getenv("TEAMX_REFERENCE_BROWSER_HOLD") == "1" {
		referenceBrowserHold(t, output, server.URL, id, svc, units[len(units)-1], report)
	}
}

func referenceBrowserHold(t *testing.T, output, endpoint string, id MemberIdentity, svc *service, unit followupUnit, report map[string]any) {
	t.Helper()
	// A local-only question exercises Room reply UI; it grants no remote action.
	var err error
	unit, err = svc.store.readFollowup(id, unit.ObjectRef, unit.Ref)
	if err != nil {
		t.Fatal(err)
	}
	unit, err = svc.askFollowup(context.Background(), id, followupQuestionChange{followupCommand: conversationCommand(unit, 1), Text: "@human R6 浏览器验收：请在 Room 就地回复“浏览器已核对”。此答复仅作本地 UI 记录，不触发远端动作。"})
	if err != nil {
		t.Fatal(err)
	}
	rule := PolicyChange{ID: "browser-preference", Status: "active", Text: "R6 浏览器测试长期规则", SourceText: "用户授权 R6 浏览器验收，仅作本地纠正撤销验证"}
	if _, err := svc.store.changePolicy(id, humanCaller, rule); err != nil {
		t.Fatal(err)
	}
	ready := map[string]any{"endpoint": endpoint, "member": id, "question_seq": unit.Questions[len(unit.Questions)-1].Seq, "expires_at": time.Now().Add(15 * time.Minute)}
	if err := os.WriteFile(filepath.Join(output, "browser-ready.json"), mustContractJSON(ready), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated browser ready:", endpoint)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(output, "browser-done.json")); err == nil {
				var result struct {
					Status string `json:"status"`
					Reason string `json:"reason,omitempty"`
				}
				if err := jsonFile(filepath.Join(output, "browser-done.json"), &result); err != nil || result.Status != "passed" {
					report["browser_result"] = result
					t.Fatal("browser operator did not report a passed review; retained result")
				}
				state, e := svc.store.LoadRoom()
				if e != nil {
					t.Fatal(e)
				}
				found := false
				for _, msg := range state.Messages {
					if msg.Author.Kind == "human" && msg.ReplyTo == ready["question_seq"].(uint64) && msg.Text == "浏览器已核对" {
						found = true
					}
				}
				if !found {
					t.Fatal("browser reply not persisted with exact reply_to")
				}
				policy, e := svc.store.readPolicy(id)
				if e != nil {
					t.Fatal(e)
				}
				revoked := false
				for _, p := range policy.History {
					if p.ID == rule.ID && p.Status == "revoked" {
						revoked = true
					}
				}
				if !revoked {
					t.Fatal("browser preference revocation missing")
				}
				report["browser_persisted_reply_and_revocation"] = "passed"
				return
			}
		case <-deadline.C:
			t.Fatal("browser verification timed out; retained fixture, no remote resend")
		}
	}
}
