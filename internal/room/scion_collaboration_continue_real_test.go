//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Explicit, bounded continuation after inspecting an interrupted S4 run. This
// never recreates a Review or replays an unknown write. It requires durable
// settlement, reads the existing object first, and uses the normal enable/reset
// gate. Single-member scheduling keeps this acceptance within gateway limits.
func TestScionRealDSHReviewContinuation(t *testing.T) {
	if os.Getenv("TEAMX_SCION_S4_CONTINUE") != "1" {
		t.Skip("explicit continuation of one inspected S4 Room required")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "scope": "continue inspected S4 Room and same Review; sequential real DSH turns", "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, output, report)
	source := os.Getenv("TEAMX_SCION_S4_SOURCE")
	if !filepath.IsAbs(source) {
		t.Fatal("absolute existing S4 evidence source required")
	}
	root := filepath.Join(source, "rooms-root")
	m, err := newRoomManager(root, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := m.close(); e != nil {
			t.Error(e)
		}
	}()
	if len(m.index.Rooms) != 1 {
		t.Fatal("exactly one inspected Room required")
	}
	room := m.index.Rooms[0].roomSummary
	saved, _ := readManagedRoom(t, m, room)
	config, err := loadStartupConfig(os.Getenv("TEAMX_SCION_CONFIG"), true)
	if err != nil || saved.Runtime == nil {
		t.Fatal("current credential source and frozen runtime required")
	}
	key := saved.Runtime.Model.APIKeyEnv
	if os.Getenv(key) == "" && config.Env[key] != "" {
		t.Setenv(key, config.Env[key])
	}
	imageRealCredential(t, key)
	// Verify every old execution's settlement before opening or resetting anything.
	for _, member := range saved.Members {
		state := filepath.Join(m.roomDir(room.RoomID), "agents", member.MembershipID)
		var ref struct{ Directory string }
		raw, e := os.ReadFile(filepath.Join(state, "session.json"))
		if e != nil || json.Unmarshal(raw, &ref) != nil || ref.Directory == "" {
			t.Fatal("missing old Session reference")
		}
		if err := dsh.CheckReset(filepath.Join(state, ref.Directory)); err != nil {
			t.Fatal("old process settlement unavailable", err)
		}
	}
	binary := filepath.Join(output, "teamx")
	if err := exec.Command("go", "build", "-o", binary, "../../cmd/teamx").Run(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(m)
	defer server.Close()
	m.config = serviceConfig{endpoint: server.URL, cliPath: binary, maxTurns: 24, dshTurnTimeout: 6 * time.Minute, persistentScheduling: true}
	svc := openRoomOK(t, m, room, "s4-continuation-"+filepath.Base(output))
	report["room"], report["source"], report["model"] = room.RoomIdentity, source, saved.Runtime.Model.ModelID
	members := map[string]MemberState{}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	for _, member := range saved.Members {
		members[member.AgentID] = MemberState{AgentID: member.AgentID, MembershipID: member.MembershipID}
		if _, err := svc.control(ctx, apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}, "pause"); err != nil {
			t.Fatal(err)
		}
	}
	client, endpoint := toolPipeServer(t, m)
	readTool := func(args ...string) []byte {
		t.Helper()
		var out, stderr bytes.Buffer
		if e := runToolsWithClient(ctx, append([]string{"run", "scion", "--json"}, args...), bytes.NewReader(nil), &out, &stderr, cliToolEnv(endpoint, svc, members["developer"]), client); e != nil {
			t.Fatal(e, stderr.String())
		}
		return out.Bytes()
	}
	type review struct {
		Repo, Target, State, Head, Author, Landed string
		Number                                    uint64
		Revision                                  uint32
		Approved                                  bool
		Revisions                                 []struct {
			Revision   uint32
			Head, Base string
		}
	}
	readReview := func() review {
		t.Helper()
		var v review
		raw := readTool("review", "show", "1")
		if json.Unmarshal(raw, &v) != nil || v.Number != 1 || v.Repo != "OpenCSG/teamx-demo" || v.Target != "main" || v.State != "open" || v.Approved || v.Landed != "" {
			t.Fatal("existing Review mismatch", string(raw))
		}
		return v
	}
	resumeCorrection := os.Getenv("TEAMX_SCION_S4_START") == "correction"
	v1 := readReview()
	initialComments := readTool("review", "comments", "1")
	var initial struct{ Comments []json.RawMessage }
	if json.Unmarshal(initialComments, &initial) != nil || (!resumeCorrection && len(initial.Comments) != 0) || (resumeCorrection && len(initial.Comments) != 1) {
		t.Fatal("this continuation requires the inspected Review with no prior comments; inspect before proceeding")
	}
	report["initial_review"] = v1
	initialBranches := readTool("branch", "ls")
	post := func(id, text string) {
		t.Helper()
		if _, e := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: room.RoomIdentity, Caller: humanCaller, ClientMessageID: id, Text: text}); e != nil {
			t.Fatal(e)
		}
	}
	run := func(agent, phase string) {
		t.Helper()
		member := members[agent]
		id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: agent, MembershipID: member.MembershipID}
		old := inspectMember(t, ctx, svc.target(member.MembershipID), id)
		if _, e := svc.control(ctx, apiRequest{AgentID: agent, MembershipID: member.MembershipID}, "enable"); e != nil {
			t.Fatal(phase, e)
		}
		deadline := time.Now().Add(6 * time.Minute)
		for time.Now().Before(deadline) {
			status := inspectMember(t, ctx, svc.target(member.MembershipID), id)
			if status.Agent.TurnNumber > old.Agent.TurnNumber && !status.Active && status.Agent.Turn != nil {
				if status.Agent.Turn.Status != "completed" || status.Error != "" {
					t.Fatal(phase, status.Error)
				}
				if _, e := svc.control(ctx, apiRequest{AgentID: agent, MembershipID: member.MembershipID}, "pause"); e != nil {
					t.Fatal(e)
				}
				raw, e := os.ReadFile(filepath.Join(m.roomDir(room.RoomID), "agents", member.MembershipID, "dsh-turn.json"))
				if e != nil {
					t.Fatal(e)
				}
				var diagnostic dshDiagnostic
				if json.Unmarshal(raw, &diagnostic) != nil {
					t.Fatal("invalid model evidence")
				}
				report[phase] = diagnostic
				if e := os.WriteFile(filepath.Join(output, phase+".json"), raw, 0600); e != nil {
					t.Fatal(e)
				}
				t.Log(phase, "completed", status.Agent.TurnNumber)
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Second):
			}
		}
		t.Fatal(phase, "did not settle")
	}
	// No commands are prescribed: the frozen role and current tool skill must
	// discover the native protocol and test the actual delivered implementation.
	if !resumeCorrection {
		post("s4-review-continue", "继续刚才的交付：Review #1 已创建，网页链接为 https://scion.opencsg.com/OpenCSG/teamx-demo/reviews/1 。请 reviewer 独立核实当前版本、测试并发布版本化普通评审；其他成员先等待评审结果。")
		run("reviewer", "first_review")
	} else {
		raw, e := os.ReadFile(filepath.Join(os.Getenv("TEAMX_SCION_S4_PRIOR_EVIDENCE"), "first_review.json"))
		var diagnostic dshDiagnostic
		if e != nil || json.Unmarshal(raw, &diagnostic) != nil {
			t.Fatal("verified prior first-review model evidence required")
		}
		report["first_review"] = diagnostic
	}
	report["first_comments"] = json.RawMessage(readTool("review", "comments", "1"))
	post("s4-correction", "补充验收：请在刚才同一个交付中增加 stats 子命令，输出总数、未完成数和已完成数；覆盖空列表以及完成任务后的统计，更新使用说明。developer 修正并更新原 Review 交复审，reviewer 对新版本独立测试后给出结论。")
	if resumeCorrection {
		post("s4-correction-continue-"+filepath.Base(output), "继续尚未发布的 stats 修正：上次已修改 todo.py、test_todo.py、README.md，并执行过 14 项测试，但尚未提交发布。保留本成员已有修改，先核实当前状态和版本，再完成原任务分支发布、更新同一个 Review #1 并交复审；不要重新创建 Review。")
	}
	run("developer", "correction")
	v2 := readReview()
	report["final_review"] = v2
	if v2.Revision <= v1.Revision || v2.Head == v1.Head {
		t.Fatal("same Review must gain a new revision")
	}
	run("reviewer", "second_review")
	finalRaw := readTool("review", "comments", "1")
	report["final_comments"] = json.RawMessage(finalRaw)
	var comments struct {
		Comments []struct {
			ID           uint64
			Revision     uint32
			Author, Body string
		}
	}
	if json.Unmarshal(finalRaw, &comments) != nil {
		t.Fatal("invalid comments")
	}
	for _, v := range []review{v1, v2} {
		base := ""
		for _, revision := range v.Revisions {
			if revision.Revision == v.Revision && revision.Head == v.Head {
				base = revision.Base
			}
		}
		if base == "" {
			t.Fatal("missing revision base")
		}
		count := 0
		for _, c := range comments.Comments {
			if c.Revision == v.Revision && c.ID > 0 && c.Author == v.Author && strings.Contains(c.Body, base) && strings.Contains(c.Body, v.Head) {
				count++
			}
		}
		if count != 1 {
			t.Fatal("expected one exact-version ordinary comment", v.Revision, count)
		}
	}
	if _, e := os.Stat(filepath.Join(source, "comment-response-lost")); e != nil {
		t.Fatal("controlled unknown write not exercised")
	}
	for _, member := range saved.Members {
		if member.AgentID != "reviewer" {
			continue
		}
		var state struct {
			Base     string
			Head     *string
			Detached bool
		}
		raw, e := os.ReadFile(filepath.Join(m.roomDir(room.RoomID), member.Workspace, ".scion/state.json"))
		if e != nil || json.Unmarshal(raw, &state) != nil || state.Base != v2.Head || !state.Detached || state.Head != nil {
			t.Fatal("reviewer final exact version missing")
		}
	}
	state, e := svc.store.LoadRoom()
	if e != nil {
		t.Fatal(e)
	}
	report["messages"] = state.Messages
	for _, msg := range state.Messages {
		if msg.Author.ID == "architect" {
			t.Fatal("simple task triggered architect participation")
		}
	}
	report["final_branches"] = json.RawMessage(readTool("branch", "ls"))
	report["initial_branches"] = json.RawMessage(initialBranches)
	// Target tip is independently inspected, rather than trusting a model's report.
	var before, after struct {
		Branches []struct{ Name, Changeset string }
	}
	json.Unmarshal(initialBranches, &before)
	json.Unmarshal(readTool("branch", "ls"), &after)
	tip := ""
	for _, b := range before.Branches {
		if b.Name == "main" {
			tip = b.Changeset
		}
	}
	unchanged := false
	for _, b := range after.Branches {
		if b.Name == "main" && b.Changeset == tip && tip != "" {
			unchanged = true
		}
	}
	if !unchanged {
		t.Fatal("target branch changed")
	}
	// Both reviewer turns must contain actual successful independent test output.
	for _, phase := range []string{"first_review", "second_review"} {
		diagnostic := report[phase].(dshDiagnostic)
		tested := false
		for _, update := range diagnostic.Result.Tools {
			if update.Status == "completed" && strings.Contains(update.Input, "unittest") && strings.Contains(update.Output, "OK") && !strings.Contains(update.Output, "FAILED") {
				tested = true
			}
		}
		if !tested {
			t.Fatal("no successful independent model-driven test evidence", phase)
		}
	}
	report["status"] = "passed"
	fmt.Println("S4 same-Review correction/re-review and one controlled unknown comment reconciliation verified")
}
