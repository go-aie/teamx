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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/scion"
)

// Opt-in: real models create/update ONE Review and publish ordinary comments in
// the authorized demo. Never replay this test's output directory after failure:
// inspect its Room and remote objects first. No merge, close or formal verdict.
func TestScionRealDSHCollaboration(t *testing.T) {
	if os.Getenv("TEAMX_SCION_S4_REAL") != "1" {
		t.Skip("explicit S4 real model and remote write opt-in required")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "scope": "S4 real DSH development/review/revision/re-review", "recorded_at": time.Now().UTC(), "browser": "not exercised"}
	defer imageRealReport(t, output, report)
	config, err := loadStartupConfig(os.Getenv("TEAMX_SCION_CONFIG"), true)
	if err != nil || config.Runtime == nil {
		t.Fatal("current runtime config required", err)
	}
	key := config.Runtime.Model.APIKeyEnv
	if os.Getenv(key) == "" && config.Env[key] != "" {
		t.Setenv(key, config.Env[key])
	}
	imageRealCredential(t, key)
	report["model"] = config.Runtime.Model.ModelID
	if value := os.Getenv("TEAMX_SCION_S4_MAX_TOKENS"); value != "" {
		limit, e := strconv.Atoi(value)
		if e != nil || limit < 1024 {
			t.Fatal("invalid acceptance max token limit")
		}
		config.Runtime.Model.MaxTokens = limit
	}
	report["max_tokens"] = config.Runtime.Model.MaxTokens
	credentials, err := os.ReadFile(os.Getenv("TEAMX_SCION_TEST_CREDENTIALS"))
	if err != nil {
		t.Fatal("isolated logged-in credentials required")
	}
	native := os.Getenv("TEAMX_SCION_BIN")
	if !filepath.IsAbs(native) {
		t.Fatal("absolute compiled TEAMX_SCION_BIN required")
	}
	binary := filepath.Join(output, "teamx")
	if err := exec.Command("go", "build", "-o", binary, "../../cmd/teamx").Run(); err != nil {
		t.Fatal(err)
	}
	// This fault adapter runs the real thin wrapper and hides exactly one successful
	// comment response. The model must reconcile by reading, without a second post.
	wrapped := filepath.Join(output, "fault-scion")
	script := fmt.Sprintf(`#!/usr/bin/env -S python3 -S
import os,sys,subprocess,json
args=sys.argv[1:]
root=os.path.dirname(os.path.realpath(__file__))
if 'review' in args:
 with open(root+'/review-calls.jsonl','a') as f: f.write(json.dumps(args)+'\n')
p=subprocess.run([%s]+args,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
if p.returncode==0 and 'review' in args and 'comment' in args and '--help' not in args and '-h' not in args and not os.path.exists(root+'/comment-response-lost'):
 open(root+'/comment-response-lost','x').write('native succeeded; response intentionally withheld\n')
 sys.stderr.write('Controlled lost write response: result unknown. Read remote review comments; do not resend.\n');sys.exit(4)
sys.stdout.buffer.write(p.stdout);sys.stderr.buffer.write(p.stderr);sys.exit(p.returncode)
`, strconv.Quote(native))
	if err := os.WriteFile(wrapped, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(output, "rooms-root")
	m, err := newTeamRoomManager(root, "../../.teamx/teams", 24, config.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := m.close(); err != nil {
			t.Error(err)
		}
	}()
	server := httptest.NewServer(m)
	defer server.Close()
	m.config = serviceConfig{endpoint: server.URL, cliPath: binary, maxTurns: 24, dshTurnTimeout: 6 * time.Minute, persistentScheduling: true}
	// Freeze the actual production templates; the short task arrives through Room.
	r := managerHTTP(m, "/v1/rooms/create", roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller, roomSelection: roomSelection{CreateID: "scion-s4", TeamID: "software-development"}})
	var room roomSummary
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &room) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	report["room"] = room.RoomIdentity
	profile := &platform.CLIProfile{Version: 1, Brand: "scion", Profile: strings.Repeat("c", 32), Executable: wrapped, CLIVersion: scion.CLIVersion}
	dir := filepath.Join(root, "scion-profiles", profile.Profile)
	if os.MkdirAll(dir, 0700) != nil || os.WriteFile(filepath.Join(dir, "credentials.json"), credentials, 0600) != nil {
		t.Fatal("profile setup failed")
	}
	c := platformTestSave(t, m, platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "S4 isolated Scion", Provider: "scion", URL: "https://scion.opencsg.com", CLIProfile: profile})
	c = verifyScion(t, m, c)
	report["account"] = c.Validation.Account
	openRoomOK(t, m, room, "s4-before-workspace")
	applyWorkspace(t, m, scionWorkspaceRequest(m, room, c))
	if op := awaitWorkspace(t, m); op.Status != "completed" {
		t.Fatal(op.Status, op.ErrorCode)
	}
	// Applying a Workspace may leave the Room closed. Open normally; no substituted
	// Session factory, model instructions or tool implementation is used.
	svc := openRoomOK(t, m, room, "s4-open")
	saved, _ := readManagedRoom(t, m, room)
	revision := uint64(0)
	members := map[string]MemberState{}
	for _, member := range saved.Members {
		ms := MemberState{AgentID: member.AgentID, MembershipID: member.MembershipID}
		members[member.AgentID] = ms
		revision = bindCLITool(t, m, room.RoomIdentity, ms, c, "scion", revision).Revision
	}
	report["members"] = saved.Members
	client, endpoint := toolPipeServer(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Minute)
	defer cancel()
	readTool := func(args ...string) []byte {
		t.Helper()
		var out, stderr bytes.Buffer
		err := runToolsWithClient(ctx, append([]string{"run", "scion", "--json"}, args...), bytes.NewReader(nil), &out, &stderr, cliToolEnv(endpoint, svc, members["developer"]), client)
		if err != nil {
			t.Fatal("read-only verification failed", args, err, stderr.String())
		}
		return out.Bytes()
	}
	baseline := readTool("branch", "ls")
	report["initial_branches"] = json.RawMessage(baseline)
	marker := "scion-s4-" + room.RoomID[:8]
	post := func(id, text string) uint64 {
		t.Helper()
		receipt, e := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: room.RoomIdentity, Caller: humanCaller, ClientMessageID: id, Text: text})
		if e != nil {
			t.Fatal(e)
		}
		return receipt.Seq
	}
	waitReview := func(after uint64, phase string) RoomSnapshot {
		t.Helper()
		deadline := time.Now().Add(10 * time.Minute)
		var last uint64
		for time.Now().Before(deadline) {
			state, e := svc.store.LoadRoom()
			if e != nil {
				t.Fatal(e)
			}
			delivered := false
			for _, msg := range state.Messages {
				if msg.Seq > after && msg.Author.ID == "reviewer" && strings.Contains(msg.Text, "[评审结果]") {
					delivered = true
				}
			}
			idle := true
			for _, member := range members {
				id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
				status, e := svc.target(member.MembershipID).Call[MemberStatus](ctx, InspectMember{id})
				if e != nil {
					t.Fatal(e)
				}
				if status.Paused || status.Error != "" {
					t.Fatalf("%s: %s paused=%v error=%s", phase, member.AgentID, status.Paused, status.Error)
				}
				idle = idle && !status.Active
			}
			if state.LatestSeq != last {
				t.Logf("%s: Room seq=%d", phase, state.LatestSeq)
				last = state.LatestSeq
			}
			if delivered && idle {
				return state
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(2 * time.Second):
			}
		}
		t.Fatal("real collaboration did not deliver a settled reviewer result", phase)
		return RoomSnapshot{}
	}
	type review struct {
		Number                                           uint64
		Repo, Author, Target, State, Head, Title, Landed string
		Revision                                         uint32
		Approved                                         bool
		Revisions                                        []struct {
			Revision   uint32
			Base, Head string
		}
	}
	findReview := func() review {
		t.Helper()
		var page struct {
			Reviews   []review
			Truncated bool
		}
		if json.Unmarshal(readTool("review", "ls", "--state", "all", "--limit", "1000"), &page) != nil || page.Truncated {
			t.Fatal("incomplete review list")
		}
		var found []review
		for _, v := range page.Reviews {
			if strings.Contains(v.Title, marker) {
				found = append(found, v)
			}
		}
		if len(found) != 1 {
			t.Fatal("expected one task review", len(found))
		}
		var v review
		raw := readTool("review", "show", fmt.Sprint(found[0].Number))
		if json.Unmarshal(raw, &v) != nil || v.Repo != "OpenCSG/teamx-demo" || v.State != "open" || v.Target != "main" || v.Head == "" || v.Approved || v.Landed != "" {
			t.Fatal("invalid live review", string(raw))
		}
		return v
	}
	firstSeq := post("task", "任务名："+marker+"（交付标题保留此标识）。做一个只依赖 Python 标准库的命令行 Todo 看板：add 添加、list 查看、done 完成，关闭再打开数据仍在；写必要的自动测试和简短使用说明。完成后交独立评审。")
	for _, member := range members {
		if _, err := svc.control(ctx, apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}, "run"); err != nil {
			t.Fatal(err)
		}
	}
	first := waitReview(firstSeq, "initial")
	v1 := findReview()
	report["first_review"] = v1
	report["first_room"] = first.Messages
	comments1 := readTool("review", "comments", fmt.Sprint(v1.Number))
	report["first_comments"] = json.RawMessage(comments1)
	nextSeq := post("revision", "补充验收：请在刚才同一个交付中增加 stats 子命令，输出总数、未完成数和已完成数；覆盖空列表以及完成任务后的统计，更新使用说明。developer 修改并交复审，reviewer 对新版本独立测试后给出结论。")
	final := waitReview(nextSeq, "revision")
	v2 := findReview()
	report["final_review"] = v2
	report["final_room"] = final.Messages
	if v2.Number != v1.Number || v2.Revision <= v1.Revision || v2.Head == v1.Head {
		t.Fatal("same Review must gain a new revision/head")
	}
	var comments struct {
		Comments []struct {
			ID           uint64
			Revision     uint32
			Body, Author string
		}
	}
	raw := readTool("review", "comments", fmt.Sprint(v2.Number))
	report["final_comments"] = json.RawMessage(raw)
	if json.Unmarshal(raw, &comments) != nil {
		t.Fatal("invalid comments")
	}
	for _, v := range []review{v1, v2} {
		base := ""
		for _, rev := range v.Revisions {
			if rev.Revision == v.Revision && rev.Head == v.Head {
				base = rev.Base
			}
		}
		if base == "" {
			t.Fatal("revision base missing")
		}
		matches := 0
		for _, comment := range comments.Comments {
			if comment.Revision == v.Revision && strings.Contains(comment.Body, v.Head) && strings.Contains(comment.Body, base) && comment.ID > 0 && comment.Author == v.Author {
				matches++
			}
		}
		if matches != 1 {
			t.Fatalf("revision %d: expected exactly one verified comment including full base/head; got %d", v.Revision, matches)
		}
	}
	if _, err := os.Stat(filepath.Join(output, "comment-response-lost")); err != nil {
		t.Fatal("unknown-write fault not exercised")
	}
	// Check actual reviewer working-copy identity independently of public claims.
	for _, member := range saved.Members {
		var local struct {
			Base     string
			Detached bool
			Head     *string
		}
		raw, err := os.ReadFile(filepath.Join(m.roomDir(room.RoomID), member.Workspace, ".scion/state.json"))
		if err != nil || json.Unmarshal(raw, &local) != nil {
			t.Fatal("missing native working copy")
		}
		if member.AgentID == "reviewer" && (local.Base != v2.Head || !local.Detached || local.Head != nil) {
			t.Fatal("reviewer did not inspect the final exact head", local)
		}
		if member.AgentID == "architect" {
			for _, msg := range final.Messages {
				if msg.Author.ID == "architect" {
					t.Fatal("simple task should not require architect participation")
				}
			}
		}
	}
	if !bytes.Equal(baseline, readTool("branch", "ls")) {
		// Task branches legitimately change; the initial target branch must not.
		var before, after struct {
			Branches []struct{ Name, Changeset string }
		}
		json.Unmarshal(baseline, &before)
		json.Unmarshal(readTool("branch", "ls"), &after)
		tip := ""
		for _, b := range before.Branches {
			if b.Name == "main" {
				tip = b.Changeset
			}
		}
		match := false
		for _, b := range after.Branches {
			if b.Name == "main" && b.Changeset == tip && tip != "" {
				match = true
			}
		}
		if !match {
			t.Fatal("target branch changed")
		}
	}
	report["unknown_write"] = "one successful comment response hidden; read-only reconciliation, no duplicate version comments"
	report["status"] = "passed"
}
