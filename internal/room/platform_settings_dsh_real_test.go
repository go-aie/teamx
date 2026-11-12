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
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// This input contains explicit acceptance targets, never credentials. No source
// installation is copied or changed. Only the fresh member's settings are saved.
type conversationSettingsInput struct {
	Config         string              `json:"config"`
	Lark           platform.CLIProfile `json:"lark"`
	Groups         []string            `json:"groups"`
	PrivateQuery   string              `json:"private_query"`
	PrivateChat    string              `json:"private_chat"`
	AmbiguousQuery string              `json:"ambiguous_query"`
}

// Real DSH/model, production CLI/HTTP/receipts, optional real provider reads.
// Fixture and real-provider runs use separate gates and are reported separately.
func TestConversationSettingsRealDSH(t *testing.T) {
	mode := os.Getenv("TEAMX_SETTINGS_DSH")
	if mode != "fixture" && mode != "lark" {
		t.Skip("set TEAMX_SETTINGS_DSH=fixture|lark, runtime config and fresh TEAMX_DSH_OUTPUT")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "incomplete", "provider": mode, "business_writes": 0, "collection": "not started", "delivery": "not tested", "browser": "not requested", "gitlab_real": "not tested", "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, out, report)
	input := conversationSettingsInput{Config: os.Getenv("TEAMX_SETTINGS_CONFIG"), Groups: []string{"oc_group_one", "oc_group_two"}, PrivateQuery: "唯一同事", PrivateChat: "oc_private_one", AmbiguousQuery: "同事"}
	if mode == "lark" {
		input = conversationSettingsInput{}
		if jsonFile(os.Getenv("TEAMX_SETTINGS_INPUT"), &input) != nil || !filepath.IsAbs(input.Config) || len(input.Groups) < 1 || len(input.Groups) > 2 || len(input.Groups) == 2 && input.Groups[0] == input.Groups[1] || (input.PrivateQuery == "") != (input.PrivateChat == "") || input.AmbiguousQuery != "" && input.PrivateQuery == "" || input.Lark.Profile == "" {
			t.Fatal("explicit runtime/profile, one or two different groups, and paired optional personnel query/chat required")
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
	h, _ := platformMemberHarness(t)
	installSettingsPersonnelFixture(t, h)
	c := h.c
	if mode == "lark" {
		h.m.notifications.Clock = time.Now
		c = platformTestSave(t, h.m, managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("e", 32), Name: "对话设置隔离验收", Provider: "lark", URL: lark.Origin(input.Lark.Brand), CLIProfile: &input.Lark})
		c, err = h.m.verifyPlatform(t.Context(), managedPlatformRequest{ID: c.ID, Version: c.Version})
		if err != nil || c.Validation.Status != "valid" {
			t.Fatal("specified Lark account verification failed")
		}
		client, e := lark.New(input.Lark)
		if e != nil {
			t.Fatal("invalid explicit CLI profile")
		}
		for _, id := range input.Groups {
			chat, e := client.Chat(t.Context(), c.Validation.Account, id)
			if e != nil || chat.Mode != "group" && chat.Mode != "topic" {
				t.Fatal("specified group cannot be verified", lark.ErrorCode(e))
			}
		}
	}
	binary := productionFixtureCLI(t)
	workspace := filepath.Join(out, "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	p := dshProfile{BinaryPath: runtime.DSH.BinaryPath, BinaryArgs: runtime.DSH.BinaryArgs, Artifact: runtime.DSH.Artifact, Workspace: workspace, Model: runtime.Model.Clone(), Instructions: `You are a TeamX acceptance assistant. Read the platforms skill and CLI help, then handle actual human Room requests using the production member configuration protocol. Reply and ask clarification through Room send; normal output is private. Use only the configured TeamX CLI. Do not read files, environment or credentials, use native tools/network, perform external actions, change tools/policies/members, delegate, or create conversations. Do not infer a target from an ambiguous name. Configuration saves need the actual human source_seq, current versions and a stable operation ID; inspect the original receipt after a save. Stop on unknown submission, never change its ID. Finish each request without polling for a future human message.`}
	r := managerCreate(t, h.m, "conversation-settings-acceptance")
	audit := &policyRealAudit{Calls: map[string]int{}}
	server := httptest.NewServer(audit.serve(h.m))
	defer server.Close()
	h.m.config.endpoint = server.URL
	svc, err := newService(h.m.roomDir(r.RoomID), r.RoomID, serviceConfig{endpoint: server.URL, cliPath: binary, maxTurns: 16, persistentScheduling: true, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": p}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	h.m.bindPlatformReferences(svc)
	observePolicyReal(svc, audit)
	h.m.mu.Lock()
	h.m.runtimes[r.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	h.m.mu.Unlock()
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	req := platformMemberRequest{ServiceInfo: svc.info, Caller: agentCaller(id.AgentID, id.MembershipID), Input: &platformMemberInput{PlatformID: c.ID, PlatformVersion: c.Version}}
	// Establish the original baseline before the first real human turn. The
	// notification engine is never started, including on failure/cleanup paths.
	initialConfig := h.m.defaultLarkConfig(c.ID)
	initialConfig.Enabled, initialConfig.Private.Enabled = true, false
	initialConfig.Target = platformMemberTarget(id)
	initialConfig.Groups = lark.GroupConfig{Enabled: true, Scope: "specified", ChatIDs: slices.Clone(input.Groups), MessageMode: "mentions", AtMe: true, AtAll: true}
	initial, err := h.m.configureLarkSubscription(t.Context(), notification.Key{Owner: r.RoomIdentity, Source: lark.MessagesSourceID}, 0, initialConfig)
	if err != nil {
		t.Fatal(err)
	}
	bindCLITool(t, h.m, r.RoomIdentity, MemberState{AgentID: id.AgentID, MembershipID: id.MembershipID}, c, "lark", 0)
	bindings, err := os.ReadFile(h.m.toolBindingsPath(r.RoomIdentity))
	if err != nil {
		t.Fatal(err)
	}
	current := func() lark.State {
		t.Helper()
		h.m.mu.Lock()
		defer h.m.mu.Unlock()
		s, e := h.m.currentLarkForTargetLocked(r.RoomIdentity, c.ID, initial.Config.Target)
		if e != nil || s == nil {
			t.Fatal("missing authoritative subscription", e)
		}
		return *s
	}
	var evidence []map[string]any
	defer func() {
		state, _ := svc.store.LoadRoom()
		h.m.mu.Lock()
		ops, _ := h.m.platformMemberOperationsLocked()
		h.m.mu.Unlock()
		var own []platformMemberOperation
		for _, op := range ops {
			if op.MemberIdentity == id {
				own = append(own, op)
			}
		}
		report["member"], report["account"], report["cases"], report["receipts"], report["messages"], report["final"], report["audit"] = id, c.Validation.Account, evidence, own, state.Messages, current(), audit
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 24*time.Minute)
	defer cancel()
	turn := uint64(0)
	post := func(name, text string, change bool, check func(lark.Config) bool) {
		t.Helper()
		t.Log("real model case:", name)
		report["phase"] = name
		before := current()
		source, e := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: name, Text: fmt.Sprintf("平台账号 %s（%s），当前助理。%s", c.Name, c.ID, text)})
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
		after := current()
		h.m.mu.Lock()
		ops, e := h.m.platformMemberOperationsLocked()
		selections, se := h.m.platformSelectionsLocked()
		h.m.mu.Unlock()
		if e != nil || se != nil {
			t.Fatal(e, se)
		}
		var own []platformMemberOperation
		for _, op := range ops {
			if op.MemberIdentity == id && op.Source.Seq == source.Seq {
				own = append(own, op)
			}
		}
		want := 0
		if change {
			want = 1
		}
		if len(own) != want || change && (own[0].Status != "completed" || own[0].Result.Status != "applied" || own[0].Result.Version != before.SubscriptionVersion+1 || own[0].Account != c.Validation.Account || !reflect.DeepEqual(own[0].Config, &after.Config)) || !change && !reflect.DeepEqual(before, after) {
			t.Fatal("request/receipt/authoritative state mismatch", name)
		}
		if check != nil && !check(after.Config) {
			t.Fatal("model saved incorrect configuration", name)
		}
		if after.Baseline != initial.Baseline || after.Config.BackfillStart != initial.Config.BackfillStart || after.Config.IntervalSeconds != initial.Config.IntervalSeconds {
			t.Fatal("configuration edit changed original start/interval")
		}
		b, e := os.ReadFile(h.m.toolBindingsPath(r.RoomIdentity))
		if e != nil || !bytes.Equal(b, bindings) || len(selections) != 0 {
			t.Fatal("direct request changed tools or created a selection", e)
		}
		if change {
			op := own[0]
			replay := req
			replay.OperationID, replay.SourceSeq, replay.Input = op.ID, source.Seq, &op.Input
			got, e := h.m.configurePlatformMember(ctx, "notifications", replay)
			if e != nil || !reflect.DeepEqual(op, got) || !reflect.DeepEqual(after, current()) {
				t.Fatal("original receipt replay changed configuration", e)
			}
		}
		evidence = append(evidence, map[string]any{"case": name, "source_seq": source.Seq, "before_version": before.SubscriptionVersion, "after_version": after.SubscriptionVersion, "operations": own, "config": after.Config})
	}
	last := len(input.Groups) - 1
	post("remove", "取消关注群 "+input.Groups[last]+"，其他设置保留。", true, func(c lark.Config) bool {
		return len(c.Groups.ChatIDs) == last && (last == 0 && !c.Enabled && !c.Groups.Enabled || last != 0 && reflect.DeepEqual(c.Groups.ChatIDs, input.Groups[:last]))
	})
	post("add", "关注群 "+input.Groups[last]+"，其他设置保留。", true, func(c lark.Config) bool {
		want := slices.Clone(input.Groups)
		slices.Sort(want)
		return reflect.DeepEqual(c.Groups.ChatIDs, want)
	})
	post("replace-and-conditions", "以后群只接收 "+input.Groups[last]+"，私聊关掉；群消息只接收 @我，不接收 @所有人。直接保存，工具保持开启。", true, func(c lark.Config) bool {
		return c.Enabled && c.Groups.Enabled && c.Groups.Scope == "specified" && reflect.DeepEqual(c.Groups.ChatIDs, input.Groups[last:]) && c.Groups.MessageMode == "mentions" && c.Groups.AtMe && !c.Groups.AtAll && !c.Private.Enabled
	})
	if len(input.Groups) == 1 {
		report["second_group"] = "not tested: user specified only one group; last-group shutdown/removal and re-add verified"
	}
	if input.PrivateQuery != "" {
		searchPeople := func(query string) lark.ChatPage {
			t.Helper()
			search := req
			search.Type, search.Query = "p2p", query
			page, e := h.m.listLarkChats(ctx, search.chatsRequest())
			if e != nil {
				t.Fatal("personnel scenario precondition cannot be verified", e)
			}
			return page
		}
		unique := searchPeople(input.PrivateQuery)
		if len(unique.Chats) != 1 || unique.Chats[0].ID != input.PrivateChat || unique.HasMore || len(unique.SearchWarnings) != 0 {
			t.Fatal("unique-person scenario needs one complete verified search result")
		}
		report["unique_candidate"] = unique
		post("unique-private", "私聊只接收“"+input.PrivateQuery+"”，先按姓名核验已存在的人员会话，再开启指定私聊，其他设置保留。", true, func(c lark.Config) bool {
			return c.Private.Enabled && c.Private.Scope == "specified" && reflect.DeepEqual(c.Private.ChatIDs, []string{input.PrivateChat})
		})
		if input.AmbiguousQuery != "" {
			ambiguous := searchPeople(input.AmbiguousQuery)
			byName := map[string][]string{}
			for _, chat := range ambiguous.Chats {
				byName[chat.Name] = append(byName[chat.Name], chat.ID)
			}
			var sameNameIDs []string
			for _, ids := range byName {
				if len(ids) > 1 {
					sameNameIDs = append(sameNameIDs, ids...)
				}
			}
			if len(sameNameIDs) < 2 {
				t.Fatal("same-name scenario needs at least two verified native candidates with the same name")
			}
			report["same_name_candidates"] = ambiguous
			post("ambiguous-private", "私聊改成只接收“"+input.AmbiguousQuery+"”。有同名时先问我，不要选择第一项。", false, nil)
			state, _ := svc.store.LoadRoom()
			var reply string
			seq := evidence[len(evidence)-1]["source_seq"].(uint64)
			for _, message := range state.Messages {
				if message.Seq > seq && message.Author.Kind == "agent" {
					reply += message.Text
				}
			}
			presented := 0
			for _, chatID := range sameNameIDs {
				if strings.Contains(reply, chatID) {
					presented++
				}
			}
			if presented < 2 {
				t.Fatal("same-name clarification must expose distinguishable native conversation IDs")
			}
			post("clarified-private", "选择会话 "+input.PrivateChat+"；同时关闭群接收，私聊保持开启。", true, func(c lark.Config) bool {
				return !c.Groups.Enabled && c.Private.Enabled && reflect.DeepEqual(c.Private.ChatIDs, []string{input.PrivateChat})
			})
		} else {
			report["same_name"] = "not tested: no explicit ambiguous query"
		}
	} else {
		report["private_personnel"] = "not tested: user did not specify personnel targets"
	}
	// A known group is an independently verified wrong-type target, even when
	// a real directory has no nonexistent-name example available.
	post("unverifiable-private", "把私聊指定列表替换成 "+input.Groups[0]+"；若它不是可核验的人员私聊，请说明并保持配置。", false, nil)
	if os.Getenv("TEAMX_SETTINGS_BROWSER") == "1" {
		if _, e := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "pause"); e != nil {
			t.Fatal(e)
		}
		before := current()
		path := filepath.Join(out, "browser-input.json")
		data, _ := json.Marshal(map[string]any{"room": r.RoomInstance, "platform_id": c.ID, "platform_name": c.Name, "state": before})
		if os.WriteFile(path, data, 0600) != nil {
			t.Fatal("cannot save browser checkpoint")
		}
		browser := exec.CommandContext(ctx, "node", "testdata/conversation_settings_browser.cjs", server.URL, path, out)
		if b, e := browser.CombinedOutput(); e != nil {
			t.Fatal("browser checkpoint", e, string(b))
		}
		after := current()
		if after.SubscriptionVersion != before.SubscriptionVersion+1 || after.Config.Groups.AtAll == before.Config.Groups.AtAll {
			t.Fatal("browser save did not change authoritative state")
		}
		// Submit a distinct genuine request with the version observed before the
		// independent browser save. Reject it without creating a receipt.
		source, e := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "stale-version", Text: "把群接收改成全部消息，保留其他设置。"})
		if e != nil {
			t.Fatal(e)
		}
		stale := req
		stale.OperationID, _ = newOpaqueID()
		stale.SourceSeq = source.Seq
		in := *req.Input
		in.NotificationVersion, in.GroupMessageMode = before.SubscriptionVersion, "all"
		stale.Input = &in
		if w := managerHTTP(h.m, "/v1/platforms/member/notifications", stale); w.Code != 409 || !reflect.DeepEqual(after, current()) {
			t.Fatal("stale Agent write overwrote browser save", w.Code)
		}
		if _, e := os.Stat(h.m.platformMemberPath(stale.OperationID)); !os.IsNotExist(e) {
			t.Fatal("rejected stale input reserved an operation", e)
		}
		report["browser_conflict"] = map[string]any{"source_seq": source.Seq, "rejected_operation_id": stale.OperationID, "stale_version": before.SubscriptionVersion, "current_version": after.SubscriptionVersion, "http_status": 409}
		// The real Agent consumes this actual human request and has to read the
		// new state rather than replaying the rejected patch with a newer version.
		if _, e := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); e != nil {
			t.Fatal(e)
		}
		turn++
		s := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && (s.Agent.TurnNumber >= turn || s.Paused) })
		final := current()
		h.m.mu.Lock()
		query := req
		query.SourceSeq = source.Seq
		ops, e := h.m.platformMemberStatusLocked(query)
		h.m.mu.Unlock()
		if s.Error != "" || s.Agent.Turn == nil || s.Agent.Turn.Status != "completed" || s.Agent.TurnNumber != turn || final.Config.Groups.MessageMode != "all" || !final.Config.Groups.AtAll || final.SubscriptionVersion != after.SubscriptionVersion+1 || e != nil || len(ops) != 1 || ops[0].Status != "completed" || ops[0].Result.Status != "applied" || !reflect.DeepEqual(ops[0].Config, &final.Config) || !reflect.DeepEqual(final.Config.Private, after.Config.Private) || final.Baseline != initial.Baseline {
			t.Fatal("model did not preserve independent browser change after conflict")
		}
		evidence = append(evidence, map[string]any{"case": "after-browser-conflict", "source_seq": source.Seq, "before_version": after.SubscriptionVersion, "after_version": final.SubscriptionVersion, "operations": ops, "config": final.Config})
		report["browser"] = "passed: DOM, independent save, stale write rejection and model reread"
	}
	report["status"] = "passed"
	if len(input.Groups) == 1 || input.PrivateQuery == "" || input.AmbiguousQuery == "" {
		report["status"] = "partial: only explicitly supplied target scenarios executed"
	}
	report["configuration_disposition"] = "fresh isolated member only; no collectors started; retained in private report; service stopped on exit"
}

func installSettingsPersonnelFixture(t *testing.T, h *larkHarness) {
	t.Helper()
	installMemberPrivateFixture(t, h)
	path := filepath.Join(h.dir, "lark-cli")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte("assert a[a.index('--query')+1]=='同事'"), []byte("assert a[a.index('--query')+1] in ['同事','唯一同事']"), 1)
	b = bytes.Replace(b, []byte(" reply({'users':["), []byte(" if a[a.index('--query')+1]=='唯一同事':reply({'users':[{'open_id':'ou_one','localized_name':'唯一同事','p2p_chat_id':'oc_private_one'}],'has_more':False})\n reply({'users':["), 1)
	b = bytes.Replace(b, []byte("path=a[2]\nif path.startswith"), []byte("path=a[2]\nif path in ['/open-apis/im/v1/chats/oc_group_two','/open-apis/im/v1/chats/oc_group_three']:reply({'chat_mode':'group','name':'产品讨论群'})\nif path.startswith"), 1)
	if err = os.WriteFile(path, b, 0700); err != nil {
		t.Fatal(err)
	}
}
