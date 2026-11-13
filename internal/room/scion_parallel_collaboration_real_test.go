//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Uses the persistent, prepared Room and the existing managed model/platform.
// Stop its server first. A fresh output directory is mandatory on EVERY call.
// inspect/verify only read; start requires an empty Room and remote-write opt-in.
// resume only enables inspected paused members; it never reposts the task.
func TestScionParallelRealDSHCollaboration(t *testing.T) {
	action := os.Getenv("TEAMX_SCION_PARALLEL_ACTION")
	if action == "" {
		t.Skip("explicit inspect/start/observe action required")
	}
	if action != "inspect" && action != "start" && action != "resume" && action != "observe" && action != "verify" {
		t.Fatal("invalid action")
	}
	if (action == "start" || action == "resume") && os.Getenv("TEAMX_SCION_PARALLEL_WRITE") != "OpenCSG/teamx-demo/task-branches" {
		t.Fatal("explicit demo task-branch write opt-in required")
	}
	configPath, cli := os.Getenv("TEAMX_SCION_CONFIG"), os.Getenv("TEAMX_SCION_TEAMX_BIN")
	id := RoomIdentity{RoomID: os.Getenv("TEAMX_SCION_ROOM_ID"), RoomInstance: os.Getenv("TEAMX_SCION_ROOM_INSTANCE")}
	if !filepath.IsAbs(configPath) || !filepath.IsAbs(cli) || !safeMembershipID(id.RoomID) || !safeMembershipID(id.RoomInstance) {
		t.Fatal("absolute config/CLI and exact prepared Room identity required")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "action": action, "recorded_at": time.Now().UTC(), "room": id, "account_mode": "shared", "expected_subject": "csgship", "browser": "not exercised"}
	defer imageRealReport(t, output, report)
	config, err := readStartupConfig(configPath, true, false)
	if err != nil || config.Runtime == nil || config.Server.DataDir == nil || config.Server.TeamsDir == nil || config.Server.SessionMaxTurns == nil {
		t.Fatal("managed startup config required", err)
	}
	models, err := loadModelExecution(config)
	if err != nil {
		t.Fatal(err)
	}
	runtime := *config.Runtime
	runtime.ManagedModels = true
	m, err := newTeamRoomManager(*config.Server.DataDir, *config.Server.TeamsDir, *config.Server.SessionMaxTurns, &runtime)
	if err != nil {
		t.Fatal("stop normal server first", err)
	}
	defer func() {
		if err := m.close(); err != nil {
			t.Error(err)
		}
	}()
	m.settings = newSystemSettings(config)
	server := httptest.NewUnstartedServer(m)
	endpoint := "http://" + server.Listener.Addr().String()
	access, err := newServerAccess(config, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = access.wrap(m)
	server.Start()
	defer server.Close()
	m.config = serviceConfig{models: models, settings: m.settings, access: access, endpoint: endpoint, cliPath: cli, dshTurnTimeout: 15 * time.Minute}
	admin, err := readAdminToken(adminTokenPath(config))
	if err != nil {
		t.Fatal(err)
	}
	client := cliClient{endpoint: endpoint, http: server.Client(), token: admin}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	human := Caller{Author: Author{Kind: "human", ID: config.Access.OperatorID}}
	request := func(path string, body, out any) {
		t.Helper()
		if err := client.request(ctx, path, body, out); err != nil {
			t.Fatal(path, err)
		}
	}
	var target roomSummary
	for _, candidate := range m.index.Rooms {
		if candidate.RoomIdentity == id {
			target = candidate.roomSummary
		}
	}
	if target.RoomID == "" {
		t.Fatal("prepared Room not found")
	}
	saved, state := readManagedRoom(t, m, target)
	if saved.Scheduling != schedulingParallel || saved.TeamID != "scion-parallel-development" || len(saved.Members) != 4 || saved.Workspace == nil || saved.Workspace.Binding == nil || saved.Workspace.Binding.Project != "OpenCSG/teamx-demo" {
		t.Fatal("unexpected prepared Room")
	}
	if (action == "start" || action == "inspect") && state.LatestSeq != 0 {
		t.Fatal("start/inspect require an unused prepared Room; never replay")
	}
	if action == "verify" {
		integrated, covered := false, false
		for _, msg := range state.Messages {
			integrated = integrated || (msg.Author.ID == "developer-a" && strings.Contains(msg.Text, "[集成完成]"))
			covered = covered || (msg.Author.ID == "architect" && strings.Contains(msg.Text, "[需求覆盖完成]"))
		}
		if !integrated || !covered {
			t.Fatal("verify requires completed collaboration; inspect existing evidence before resuming")
		}
		for _, member := range saved.Members {
			var checkpoint AgentSnapshot
			if err := readSnapshot(filepath.Join(m.roomDir(id.RoomID), "agents", member.MembershipID, "agent.json"), &checkpoint, maxPrivateFileBytes); err != nil || checkpoint.MemberIdentity != (MemberIdentity{RoomIdentity: id, AgentID: member.AgentID, MembershipID: member.MembershipID}) || checkpoint.Turn == nil || checkpoint.Turn.Status != "completed" {
				t.Fatal("verify requires all member turns settled", member.AgentID, err)
			}
		}
	}
	openID, _ := newOpaqueID()
	var opened roomOpen
	request("/v1/rooms/open", roomOpenRequest{RoomIdentity: id, ServiceEpoch: m.epoch, Caller: human, OpenID: openID}, &opened)
	m.mu.Lock()
	done := m.runtimes[id].operationDone
	m.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	m.mu.Lock()
	svc := m.runtimes[id].service
	m.mu.Unlock()
	if svc == nil {
		t.Fatal("Room did not open")
	}
	var executionModels []map[string]any
	for profile, target := range svc.modelTargets {
		executionModels = append(executionModels, map[string]any{"profile": profile, "entry_id": target.EntryID, "entry_revision": target.EntryRevision, "model_id": target.Profile.Model.ModelID, "dsh_artifact": target.Profile.Artifact, "generation": target.Generation})
	}
	report["execution_models"] = executionModels
	runtimeInfo, _ := json.Marshal(map[string]any{"endpoint": endpoint, "service": svc.info})
	if err := os.WriteFile(filepath.Join(output, "runtime.json"), runtimeInfo, 0600); err != nil {
		t.Fatal(err)
	}
	platformID := saved.Workspace.Binding.PlatformID
	var catalog struct {
		Platforms []managedPlatform `json:"platforms"`
	}
	request(fmt.Sprintf("/v1/platforms?service_epoch=%s&caller_id=%s", m.epoch, human.ID), nil, &catalog)
	var platform managedPlatform
	for _, candidate := range catalog.Platforms {
		if candidate.ID == platformID {
			platform = candidate
		}
	}
	if platform.Provider != "scion" || platform.URL != "https://scion.opencsg.com" || platform.Validation.Account.ID.Value != "csgship" {
		t.Fatal("platform mismatch")
	}
	var bindings toolBindingsFile
	request(fmt.Sprintf("/v1/tools/bindings?service_epoch=%s&caller_id=%s&room_id=%s&room_instance=%s", m.epoch, human.ID, id.RoomID, id.RoomInstance), nil, &bindings)
	// Every verifier invocation uses the actual membership CLI/HTTP binding. It
	// receives the same short-lived execution credential as the DSH adapter.
	readers := map[string]func(...string) []byte{}
	var members []map[string]any
	seenWorkspaces := map[string]bool{}
	baseline := ""
	for _, member := range saved.Members {
		bound := false
		for _, b := range bindings.Bindings {
			if b.AgentID == member.AgentID && b.MembershipID == member.MembershipID && b.PlatformID == platform.ID && b.PlatformGeneration == saved.Workspace.Binding.PlatformGeneration {
				bound = true
			}
		}
		if !bound {
			t.Fatal("member is not bound to shared platform", member.AgentID)
		}
		caller := Caller{Author: Author{Kind: "agent", ID: member.AgentID}, MembershipID: member.MembershipID}
		token, revoke, err := access.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: caller})
		if err != nil {
			t.Fatal(err)
		}
		defer revoke()
		env := []string{}
		for _, entry := range os.Environ() {
			if !isRoomToolEnv(entry) {
				env = append(env, entry)
			}
		}
		for k, v := range map[string]string{"TEAMX_ENDPOINT": endpoint, "TEAMX_ROOM_ID": id.RoomID, "TEAMX_ROOM_INSTANCE": id.RoomInstance, "TEAMX_SERVICE_EPOCH": svc.info.ServiceEpoch, "TEAMX_AGENT_ID": member.AgentID, "TEAMX_MEMBERSHIP_ID": member.MembershipID, "TEAMX_EXECUTION_TOKEN": token} {
			env = append(env, k+"="+v)
		}
		run := func(args ...string) []byte {
			t.Helper()
			cmd := exec.CommandContext(ctx, cli, append([]string{"tools", "run", platform.ToolEntryID}, args...)...)
			cmd.Env = env
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s read %v: %v %s", member.AgentID, args, err, stderr.String())
			}
			return out
		}
		readers[member.AgentID] = run
		who := run("--json", "whoami", platform.URL)
		var identity struct{ Subject, Endpoint string }
		if json.Unmarshal(who, &identity) != nil || identity.Subject != "csgship" || identity.Endpoint != platform.URL {
			t.Fatal("member subject mismatch")
		}
		local := run("--json", "status")
		var status struct {
			Ref, Base                            string
			Pending                              *string
			Added, Modified, Deleted, Conflicted []string
		}
		if json.Unmarshal(local, &status) != nil {
			t.Fatal("bad status")
		}
		if action == "start" && (status.Ref != "main" || status.Pending != nil || len(status.Added)+len(status.Modified)+len(status.Deleted)+len(status.Conflicted) > 0) {
			t.Fatal("unclean prepared copy", member.AgentID)
		}
		if baseline == "" {
			baseline = status.Base
		}
		if action == "start" && status.Base != baseline {
			t.Fatal("different baselines")
		}
		workspace := filepath.Join(m.roomDir(id.RoomID), member.Workspace)
		var native struct{ Endpoint, Repo, Base string }
		nativeState, e := os.ReadFile(filepath.Join(workspace, ".scion", "state.json"))
		if e != nil || json.Unmarshal(nativeState, &native) != nil || native.Endpoint != platform.URL || native.Repo != "OpenCSG/teamx-demo" || native.Base != status.Base || seenWorkspaces[workspace] || member.Workspace != workspaceMemberPath(*saved.Workspace, member.MembershipID) {
			t.Fatal("native Workspace identity mismatch", member.AgentID)
		}
		seenWorkspaces[workspace] = true
		members = append(members, map[string]any{"agent_id": member.AgentID, "membership_id": member.MembershipID, "workspace": workspace, "whoami": json.RawMessage(who), "status": json.RawMessage(local), "platform_id": platform.ID, "native_repo": native.Repo, "native_endpoint": native.Endpoint})
	}
	report["members"], report["platform_id"] = members, platform.ID
	read := readers["architect"]
	branches := read("--json", "branch", "ls")
	report["initial_branches"] = json.RawMessage(branches)
	if action == "inspect" {
		for _, args := range [][]string{{"--json", "show", baseline}, {"--json", "cat", baseline + ":README.md"}, {"--json", "ls", baseline}, {"archive", "--help"}} {
			t.Logf("native %v: %s", args, read(args...))
		}
		report["status"] = "inspected"
		return
	}
	if action == "verify" {
		verifyScionParallelDelivery(t, output, m.roomDir(id.RoomID), report, saved, state, read)
		return
	}
	if action == "start" {
		var page struct {
			Branches []struct{ Name, Changeset string }
		}
		if json.Unmarshal(branches, &page) != nil {
			t.Fatal("invalid branches")
		}
		main := ""
		for _, b := range page.Branches {
			if b.Name == "main" {
				main = b.Changeset
			}
			if strings.HasPrefix(b.Name, "teamx/"+id.RoomID+"/") {
				t.Fatal("existing task branch; inspect before continuation")
			}
		}
		if main != baseline {
			t.Fatal("main no longer matches prepared baseline")
		}
		report["baseline"] = baseline
		text := fmt.Sprintf(`本轮实施共享账号真实并发验收。实例 https://scion.opencsg.com；仓库 OpenCSG/teamx-demo；目标基线 main / %s；Room %s。四个成员继续使用各自 membership Workspace、已绑定的共享 csgship 平台。允许远端写入仅为 teamx/%s/<本人成员完整 membership ID>/ 下的 DESIGN、T1、T2、T3、INTEGRATION 任务分支（architect 只用 DESIGN；developer-a 只用 T1、INTEGRATION；developer-b 只用 T2；developer-c 只用 T3）。明确不同 --author，提交只含指定文件，发布后回读完整实际 head。不要直接发布 main、不要 Review/评论/approve/合入/关闭/部署/删分支。

需求：实现仅依赖 Python 标准库的小型记账摘要库，所有新增代码置于 parallel_ledger/（模块可用 namespace package，不需要 __init__.py）。architect 首先在自己的 DESIGN 分支发布根目录 design.md，给出 V1 完整接口/文件范围/任务表及集成验收标准；其他成员取得准确已发布设计后并行开始。三个独立模块建议：T1 developer-a 金额解析 parse_amount(text)->整数分：接受可去首尾空白的十进制字符串、可有正负号、最多两位小数，拒绝空/多于两位小数/NaN/Inf；文件 parallel_ledger/amount.py、test_amount.py。T2 developer-b 分类汇总 summarize(entries)->dict[str,int]：每条为 (category,整数分)，按分类相加，空输入为 {}，保留零合计分类；文件 parallel_ledger/summary.py、test_summary.py。T3 developer-c 确定性 JSON 报告 render_report(totals)->str：输出 {{"categories":[{{"category":名称,"cents":整数}}...],"total_cents":整数}}，分类字典序、支持中文、不修改输入；文件 parallel_ledger/report.py、test_report.py。各任务写覆盖边界的 unittest，单项测试只导入自己模块。architect 确认和细化接口，T1/T2/T3 之间无开发前置依赖。

三个准确版本完整交付后，developer-a 执行 INTEGRATION：从本轮基线建立独立集成分支，按准确完整 head 取得三项发布内容和 design.md（仅在自己 Workspace 操作，读取远端 cat/archive 或合适原生组合命令；不读同伴目录），新增 parallel_ledger/test_integration.py 和 parallel_ledger/README.md。整体验收必须实际串联 parse_amount -> summarize -> render_report，覆盖正常/负数/零/中文/空数据、非法金额拒绝及输入不变；运行 python3 -m unittest discover -s parallel_ledger -p 'test_*.py' -v，发布并回读集成版本。在 Room 交付使用 [任务完成] T1/T2/T3 / 成员；集成使用 [集成完成]，包含采用的设计、三任务完整 head、自身分支和实际发布 head、测试结果；architect 核对后使用 [需求覆盖完成] 报告。开发者无分配时等待，互不代做；公开消息只交付新信息。`, baseline, id.RoomID, id.RoomID)
		var receipt PostReceipt
		request("/v1/send", apiRequest{ServiceInfo: svc.info, Caller: human, ClientMessageID: "scion-parallel-ledger-v1", Text: text}, &receipt)
		report["task_receipt"] = receipt
	}
	if action == "resume" {
		if len(state.Messages) == 0 || state.Messages[0].ClientMessageID != "scion-parallel-ledger-v1" {
			t.Fatal("expected existing task; never repost")
		}
		for _, member := range saved.Members {
			identity := MemberIdentity{RoomIdentity: id, AgentID: member.AgentID, MembershipID: member.MembershipID}
			status, e := svc.target(member.MembershipID).Call[MemberStatus](ctx, InspectMember{identity})
			if e != nil {
				t.Fatal(e)
			}
			if status.Paused && !status.Active {
				deadline := time.Now().Add(30 * time.Second)
				for {
					_, e = svc.control(ctx, apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}, "enable")
					if e == nil {
						break
					}
					if !errors.Is(e, ErrBusy) || time.Now().After(deadline) {
						t.Fatal("resume", member.AgentID, e)
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(100 * time.Millisecond):
					}
				}
			}
		}
	}
	// Sample real MemberStatus and copy every completed turn before the diagnostic
	// file is replaced by a later turn. No fake backend, barrier or delayed tool.
	samples, err := os.OpenFile(filepath.Join(output, "execution.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer samples.Close()
	encoder := json.NewEncoder(samples)
	seen := map[string]uint64{}
	seenDiagnostics := map[string]bool{}
	lastSeq := uint64(0)
	for {
		state, err = svc.store.LoadRoom()
		if err != nil {
			t.Fatal(err)
		}
		report["messages"] = state.Messages
		idle := true
		var statuses []MemberStatus
		for _, member := range saved.Members {
			identity := MemberIdentity{RoomIdentity: id, AgentID: member.AgentID, MembershipID: member.MembershipID}
			status, e := svc.target(member.MembershipID).Call[MemberStatus](ctx, InspectMember{identity})
			if e != nil {
				t.Fatal(e)
			}
			statuses = append(statuses, status)
			idle = idle && !status.Active
			if raw, e := os.ReadFile(filepath.Join(m.roomDir(id.RoomID), "agents", member.MembershipID, "dsh-turn.json")); e == nil {
				name := fmt.Sprintf("%s-diagnostic-%x.json", member.AgentID, sha256.Sum256(raw))
				if !seenDiagnostics[name] {
					if e = os.WriteFile(filepath.Join(output, name), raw, 0600); e != nil {
						t.Fatal(e)
					}
					seenDiagnostics[name] = true
				}
			}
			if status.Agent != nil && status.Agent.TurnNumber > seen[member.AgentID] && !status.Active {
				raw, e := os.ReadFile(filepath.Join(m.roomDir(id.RoomID), "agents", member.MembershipID, "dsh-turn.json"))
				if e == nil {
					if e = os.WriteFile(filepath.Join(output, fmt.Sprintf("%s-turn-%d.json", member.AgentID, status.Agent.TurnNumber)), raw, 0600); e != nil {
						t.Fatal(e)
					}
					seen[member.AgentID] = status.Agent.TurnNumber
				}
			}
			if status.Error != "" {
				t.Logf("%s paused=%v active=%v error=%s", member.AgentID, status.Paused, status.Active, status.Error)
			}
		}
		if err = encoder.Encode(map[string]any{"at": time.Now().UTC(), "members": statuses}); err != nil {
			t.Fatal(err)
		}
		if state.LatestSeq != lastSeq {
			for _, msg := range state.Messages {
				if msg.Seq > lastSeq {
					t.Logf("seq=%d %s: %s", msg.Seq, msg.Author.ID, msg.Text)
				}
			}
			lastSeq = state.LatestSeq
		}
		integrated, covered := false, false
		for _, msg := range state.Messages {
			integrated = integrated || (msg.Author.ID == "developer-a" && strings.Contains(msg.Text, "[集成完成]"))
			covered = covered || (msg.Author.ID == "architect" && strings.Contains(msg.Text, "[需求覆盖完成]"))
		}
		if integrated && covered && idle {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("collaboration incomplete; inspect before any continuation", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	report["final_branches"] = json.RawMessage(read("--json", "branch", "ls"))
	report["status"] = "collaboration_settled_requires_independent_verification"
}

// Independent reads: never switches a member copy or trusts its local code as
// the delivered tree. All executable files are downloaded at the exact head.
func verifyScionParallelDelivery(t *testing.T, output, roomDir string, report map[string]any, saved managedRoomConfig, state RoomSnapshot, read func(...string) []byte) {
	t.Helper()
	var prior struct {
		Baseline        string
		InitialBranches json.RawMessage `json:"initial_branches"`
	}
	raw, err := os.ReadFile(os.Getenv("TEAMX_SCION_PARALLEL_PRIOR_REPORT"))
	if err != nil || json.Unmarshal(raw, &prior) != nil || len(prior.Baseline) != 66 {
		t.Fatal("initial pre-write report required")
	}
	type branch struct{ Name, Changeset string }
	type page struct {
		Branches  []branch
		Truncated bool
	}
	var before, after page
	if json.Unmarshal(prior.InitialBranches, &before) != nil || json.Unmarshal(read("--json", "branch", "ls"), &after) != nil || before.Truncated || after.Truncated {
		t.Fatal("complete branch inventory required")
	}
	tips := map[string]string{}
	for _, b := range after.Branches {
		tips[b.Name] = b.Changeset
	}
	for _, b := range before.Branches {
		if tips[b.Name] != b.Changeset {
			t.Fatal("pre-existing branch changed", b.Name)
		}
	}
	if tips["main"] != prior.Baseline {
		t.Fatal("target baseline changed")
	}
	owners := map[string]string{"DESIGN": "architect", "T1": "developer-a", "T2": "developer-b", "T3": "developer-c", "INTEGRATION": "developer-a"}
	files := map[string][]string{"DESIGN": {"design.md"}, "T1": {"parallel_ledger/amount.py", "parallel_ledger/test_amount.py"}, "T2": {"parallel_ledger/summary.py", "parallel_ledger/test_summary.py"}, "T3": {"parallel_ledger/report.py", "parallel_ledger/test_report.py"}}
	heads := map[string]string{}
	allowed := map[string]bool{}
	var deliveries []map[string]any
	for _, task := range []string{"DESIGN", "T1", "T2", "T3", "INTEGRATION"} {
		agent := owners[task]
		membership := ""
		for _, member := range saved.Members {
			if member.AgentID == agent {
				membership = member.MembershipID
			}
		}
		name := fmt.Sprintf("teamx/%s/%s/%s", saved.RoomID, membership, task)
		allowed[name] = true
		head := tips[name]
		if len(head) != 66 || !strings.HasPrefix(head, "c1") || head == prior.Baseline {
			t.Fatal("missing published task head", task)
		}
		heads[task] = head
		show := read("--json", "show", head)
		var version struct {
			Commit struct{ ID, Author, Email string }
			Diff   struct{ Files []struct{ Path string } }
		}
		if json.Unmarshal(show, &version) != nil || version.Commit.ID != head || version.Commit.Author != "TeamX "+agent || version.Commit.Email != agent+"@teamx.local" {
			t.Fatal("remote commit author mismatch", task, string(show))
		}
		if task != "INTEGRATION" {
			if len(version.Diff.Files) != len(files[task]) {
				t.Fatal("task file boundary mismatch", task)
			}
			for _, file := range version.Diff.Files {
				found := false
				for _, path := range files[task] {
					found = found || file.Path == path
				}
				if !found {
					t.Fatal("unexpected task file", task, file.Path)
				}
			}
		}
		matched := false
		branchHandoff := false
		for _, msg := range state.Messages {
			if msg.Author.ID == agent && strings.Contains(msg.Text, name) {
				branchHandoff = true
			}
		}
		for _, msg := range state.Messages {
			if msg.Author.ID == agent && strings.Contains(msg.Text, head) && (strings.Contains(msg.Text, name) || task == "DESIGN" && branchHandoff) {
				matched = true
			}
		}
		if !matched {
			t.Fatal("remote version has no exact Room handoff", task)
		}
		reflog := read("--json", "reflog", name)
		var history struct {
			Ref     string
			Entries []struct{ Changeset, Actor, Move string }
		}
		if json.Unmarshal(reflog, &history) != nil || history.Ref != name {
			t.Fatal("invalid remote operation history", task)
		}
		published := false
		for _, entry := range history.Entries {
			if entry.Actor != "csgship" {
				t.Fatal("unexpected remote operation subject", task, entry.Actor)
			}
			published = published || (entry.Changeset == head && entry.Move == "fast-forward")
		}
		if !published {
			t.Fatal("no shared-subject publication receipt", task)
		}
		deliveries = append(deliveries, map[string]any{"task": task, "agent_id": agent, "membership_id": membership, "branch": name, "head": head, "show": json.RawMessage(show), "reflog": json.RawMessage(reflog)})
		report["deliveries"] = deliveries
	}
	for _, b := range after.Branches {
		if strings.HasPrefix(b.Name, "teamx/"+saved.RoomID+"/") && !allowed[b.Name] {
			t.Fatal("unexpected task branch", b.Name)
		}
	}
	previous := map[string]bool{}
	for _, b := range before.Branches {
		previous[b.Name] = true
	}
	var unexpected []branch
	for _, b := range after.Branches {
		if !previous[b.Name] && !allowed[b.Name] {
			unexpected = append(unexpected, b)
		}
	}
	report["unexpected_branches"] = unexpected
	cat := func(head, path string) string {
		t.Helper()
		var file struct {
			Revision, Path, Content string
			Binary                  bool
		}
		if json.Unmarshal(read("--json", "cat", head+":"+path), &file) != nil || file.Revision != head || file.Path != path || file.Binary {
			t.Fatal("invalid remote file", path)
		}
		return file.Content
	}
	root := filepath.Join(output, "published-integration")
	if os.Mkdir(root, 0700) != nil {
		t.Fatal("fresh integration directory required")
	}
	paths := []string{"README.md", "design.md", "parallel_ledger/test_integration.py", "parallel_ledger/README.md"}
	for _, task := range []string{"T1", "T2", "T3"} {
		paths = append(paths, files[task]...)
	}
	diff := read("--json", "diff", prior.Baseline, heads["INTEGRATION"])
	var changes struct {
		Files []struct{ Path, Status string }
	}
	if json.Unmarshal(diff, &changes) != nil || len(changes.Files) != len(paths)-1 {
		t.Fatal("integration file inventory mismatch")
	}
	for _, file := range changes.Files {
		found := false
		for _, path := range paths {
			found = found || (path != "README.md" && file.Path == path)
		}
		if !found || file.Status != "A" {
			t.Fatal("unexpected integrated change", file.Path, file.Status)
		}
	}
	report["integration_diff"] = json.RawMessage(diff)
	for _, path := range paths {
		content := cat(heads["INTEGRATION"], path)
		for _, task := range []string{"DESIGN", "T1", "T2", "T3"} {
			for _, owned := range files[task] {
				if path == owned && cat(heads[task], path) != content {
					t.Fatal("integrated content differs from delivered task", task, path)
				}
			}
		}
		if path == "README.md" && cat(prior.Baseline, path) != content {
			t.Fatal("baseline file changed")
		}
		dest := filepath.Join(root, filepath.FromSlash(path))
		if os.MkdirAll(filepath.Dir(dest), 0700) != nil || os.WriteFile(dest, []byte(content), 0600) != nil {
			t.Fatal("export failed", path)
		}
	}
	cmd := exec.Command("python3", "-m", "unittest", "discover", "-s", "parallel_ledger", "-p", "test_*.py", "-v")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if e := os.WriteFile(filepath.Join(output, "independent-tests.txt"), out, 0600); e != nil {
		t.Fatal(e)
	}
	report["independent_tests"] = string(out)
	if err != nil || !strings.Contains(string(out), "\nOK") {
		t.Fatal("published integration tests failed", err, string(out))
	}
	// An extra external contract test exercises the actual combination, independent
	// of the model-authored tests and without altering the published artifact.
	contract := `from parallel_ledger.amount import parse_amount
from parallel_ledger.summary import summarize
from parallel_ledger.report import render_report
import json
entries=[("餐饮",parse_amount(" 12.34 ")),("餐饮",parse_amount("-2.34")),("零",parse_amount("0")),("收入",parse_amount("+20.01"))]
totals=summarize(entries); before=dict(totals)
assert totals=={"餐饮":1000,"零":0,"收入":2001}
expected={"categories":[{"category":k,"cents":totals[k]} for k in sorted(totals)],"total_cents":3001}
assert json.loads(render_report(totals))==expected
assert totals==before
assert json.loads(render_report(summarize([])))=={"categories":[],"total_cents":0}
for bad in ("", "1.001", "NaN", "Inf"):
 try: parse_amount(bad)
 except (ValueError, TypeError): pass
 else: raise AssertionError("accepted invalid amount: "+repr(bad))
assert render_report(dict(reversed(list(totals.items()))))==render_report(totals)
print("external integration contract OK")
`
	cmd = exec.Command("python3", "-c", contract)
	cmd.Dir = root
	out, err = cmd.CombinedOutput()
	report["external_contract"] = string(out)
	if err != nil {
		t.Fatal("external integration contract failed", err, string(out))
	}
	if err = os.WriteFile(filepath.Join(output, "external-contract.py"), []byte(contract), 0600); err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"DESIGN", "T1", "T2", "T3"} {
		found := false
		for _, msg := range state.Messages {
			if msg.Author.ID == "developer-a" && strings.Contains(msg.Text, "[集成完成]") && strings.Contains(msg.Text, heads[task]) {
				found = true
			}
		}
		if !found {
			t.Fatal("integration handoff omits exact dependency", task)
		}
	}
	cmd = exec.Command("python3", "../../scripts/scion-parallel-evidence.py", "--room-dir", roomDir, "--output", filepath.Join(output, "development-overlap.json"))
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatal("native DSH overlap verification failed", err, string(out))
	}
	overlap, err := os.ReadFile(filepath.Join(output, "development-overlap.json"))
	if err != nil {
		t.Fatal(err)
	}
	report["development_overlap"] = json.RawMessage(overlap)
	report["messages"], report["heads"], report["baseline"] = state.Messages, heads, prior.Baseline
	report["status"] = "remote_delivery_and_integration_verified"
	if len(unexpected) > 0 {
		report["functional_status"] = "passed"
		t.Error("functional checks passed; remote write scope has unexpected branches", unexpected)
	}
}
