package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
)

func memberPatchBool(v bool) *bool { return &v }

func memberPatchIDs(ids ...string) *[]string {
	v := append([]string{}, ids...)
	return &v
}

func TestPlatformMemberLarkRangeAndConditionChanges(t *testing.T) {
	m, _, _, req := connectFixture(t)
	base := lark.Config{
		Enabled: true, PlatformID: "connection", Target: platformMemberTarget(MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}),
		Private:         lark.PrivateConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_private"}},
		Groups:          lark.GroupConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_a", "oc_b"}, MessageMode: "mentions", AtMe: true, AtAll: true},
		IntervalSeconds: 61, BackfillStart: time.Unix(40, 0).UTC(),
	}
	for _, tc := range []struct {
		name   string
		before func(*lark.Config)
		input  platformMemberInput
		want   func(*lark.Config)
	}{
		{"remove preserves private and conditions", nil, platformMemberInput{RemoveChatIDs: []string{"oc_a"}}, func(c *lark.Config) { c.Groups.ChatIDs = []string{"oc_b"} }},
		{"add and remove in one operation", nil, platformMemberInput{AddChatIDs: []string{"oc_c"}, RemoveChatIDs: []string{"oc_a"}}, func(c *lark.Config) { c.Groups.ChatIDs = []string{"oc_b", "oc_c"} }},
		{"replace does not enable dormant group", func(c *lark.Config) { c.Groups.Enabled = false }, platformMemberInput{Groups: &platformMemberGroupPatch{ChatIDs: memberPatchIDs("oc_c")}}, func(c *lark.Config) { c.Groups.ChatIDs = []string{"oc_c"} }},
		{"clear groups keeps private reception", nil, platformMemberInput{Groups: &platformMemberGroupPatch{ChatIDs: memberPatchIDs()}}, func(c *lark.Config) { c.Groups.ChatIDs, c.Groups.Enabled = []string{}, false }},
		{"last group closes total without other branch", func(c *lark.Config) { c.Private.Enabled = false }, platformMemberInput{RemoveChatIDs: []string{"oc_a", "oc_b"}}, func(c *lark.Config) { c.Groups.ChatIDs, c.Groups.Enabled, c.Enabled = []string{}, false, false }},
		{"disable groups retains list", nil, platformMemberInput{Groups: &platformMemberGroupPatch{Enabled: memberPatchBool(false)}}, func(c *lark.Config) { c.Groups.Enabled = false }},
		{"only at me", nil, platformMemberInput{GroupMessageMode: "mentions", Groups: &platformMemberGroupPatch{AtMe: memberPatchBool(true), AtAll: memberPatchBool(false)}}, func(c *lark.Config) { c.Groups.AtAll = false }},
		{"all messages retains dormant mention choices", nil, platformMemberInput{GroupMessageMode: "all"}, func(c *lark.Config) { c.Groups.MessageMode = "all" }},
		{"mentions defaults only when not explicit", func(c *lark.Config) { c.Groups.MessageMode, c.Groups.AtMe, c.Groups.AtAll = "all", false, false }, platformMemberInput{GroupMessageMode: "mentions"}, func(c *lark.Config) { c.Groups.MessageMode, c.Groups.AtMe = "mentions", true }},
		{"all groups preserves saved list", nil, platformMemberInput{Groups: &platformMemberGroupPatch{Scope: "all"}}, func(c *lark.Config) { c.Groups.Scope = "all" }},
		{"all to specified needs full replacement", func(c *lark.Config) { c.Groups.Scope = "all" }, platformMemberInput{Groups: &platformMemberGroupPatch{Scope: "specified", ChatIDs: memberPatchIDs("oc_c")}}, func(c *lark.Config) { c.Groups.Scope, c.Groups.ChatIDs = "specified", []string{"oc_c"} }},
		{"private disable retains specified range", nil, platformMemberInput{Private: &platformMemberPrivatePatch{Enabled: memberPatchBool(false)}}, func(c *lark.Config) { c.Private.Enabled = false }},
		{"private resume keeps specified range", func(c *lark.Config) { c.Private.Enabled = false }, platformMemberInput{Private: &platformMemberPrivatePatch{Enabled: memberPatchBool(true)}}, func(c *lark.Config) { c.Private.Enabled = true }},
		{"explicit all private preserves saved selection", nil, platformMemberInput{Private: &platformMemberPrivatePatch{Scope: "all"}}, func(c *lark.Config) { c.Private.Scope = "all" }},
		{"resume one source with other explicitly off", func(c *lark.Config) { c.Enabled = false }, platformMemberInput{Private: &platformMemberPrivatePatch{Enabled: memberPatchBool(true)}, Groups: &platformMemberGroupPatch{Enabled: memberPatchBool(false)}}, func(c *lark.Config) { c.Enabled, c.Groups.Enabled = true, false }},
		{"resume both sources explicitly", func(c *lark.Config) { c.Enabled = false }, platformMemberInput{Private: &platformMemberPrivatePatch{Enabled: memberPatchBool(true)}, Groups: &platformMemberGroupPatch{Enabled: memberPatchBool(true)}}, func(c *lark.Config) { c.Enabled = true }},
		{"explicit total off overrides branch enable", func(c *lark.Config) { c.Enabled = false }, platformMemberInput{Enabled: memberPatchBool(false), Groups: &platformMemberGroupPatch{Enabled: memberPatchBool(true)}}, func(c *lark.Config) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := base
			if tc.before != nil {
				tc.before(&initial)
			}
			frozen, _ := json.Marshal(initial)
			want := initial
			tc.want(&want)
			input := tc.input
			input.PlatformID = initial.PlatformID
			got, err := memberNotificationConfig(m, platformMemberView{Config: &initial}, input)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, err %v; want %#v", got, err, want)
			}
			after, _ := json.Marshal(initial)
			if !bytes.Equal(frozen, after) {
				t.Fatal("patch mutated the authoritative input")
			}
		})
	}
}

func TestPlatformMemberLarkConflictingIntent(t *testing.T) {
	m, _, _, req := connectFixture(t)
	base := lark.Config{Enabled: false, PlatformID: "connection", Target: platformMemberTarget(MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}), Private: lark.PrivateConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_private"}}, Groups: lark.GroupConfig{Enabled: true, Scope: "all", ChatIDs: []string{"oc_group"}, MessageMode: "mentions", AtMe: true}, IntervalSeconds: 10}
	for _, in := range []platformMemberInput{
		{RemoveChatIDs: []string{"oc_group"}},
		{Groups: &platformMemberGroupPatch{Scope: "specified"}},
		{GroupMessageMode: "mentions", Groups: &platformMemberGroupPatch{AtMe: memberPatchBool(false), AtAll: memberPatchBool(false)}},
		{Groups: &platformMemberGroupPatch{Enabled: memberPatchBool(true)}},
		{Private: &platformMemberPrivatePatch{Enabled: memberPatchBool(true)}},
		{Enabled: memberPatchBool(true)},
		{Groups: &platformMemberGroupPatch{Scope: "specified", Enabled: memberPatchBool(true), ChatIDs: memberPatchIDs()}},
	} {
		if _, err := memberNotificationConfig(m, platformMemberView{Config: &base}, in); err == nil {
			t.Fatalf("accepted ambiguous or contradictory intent: %#v", in)
		}
	}
	empty := base
	empty.Private.Enabled, empty.Groups.Enabled = false, false
	empty.Private.ChatIDs = nil
	for _, in := range []platformMemberInput{
		{Private: &platformMemberPrivatePatch{Enabled: memberPatchBool(true)}},
		{Enabled: memberPatchBool(true)},
	} {
		if _, err := memberNotificationConfig(m, platformMemberView{Config: &empty}, in); err == nil {
			t.Fatal("enabled an empty specified range")
		}
	}
}

func TestPlatformMemberLarkInputAndEmptyListPersistence(t *testing.T) {
	base := platformMemberInput{PlatformID: "cccccccccccccccccccccccccccccccc", PlatformVersion: 1}
	for _, raw := range []string{
		`{"groups":{"chat_ids":[]}}`,
		`{"groups":{"enabled":false}}`,
		`{"groups":{"scope":"all"}}`,
		`{"private":{"enabled":true,"scope":"all"}}`,
		`{"remove_chat_ids":["oc_old"]}`,
	} {
		in := base
		if err := json.Unmarshal([]byte(raw), &in); err != nil || !validPlatformMemberInput("notifications", in) || !validPlatformMemberProviderInput("lark", "notifications", in) {
			t.Fatal(raw, in, err)
		}
		data, err := json.Marshal(in)
		var restored platformMemberInput
		if err != nil || json.Unmarshal(data, &restored) != nil || !reflect.DeepEqual(in, restored) {
			t.Fatal("explicit empty or false lost on round trip", string(data), restored, err)
		}
		if validPlatformMemberProviderInput("gitlab", "notifications", in) || validPlatformMemberInput("tool", in) {
			t.Fatal("Feishu fields crossed provider or tool boundary", raw)
		}
	}
	for _, raw := range []string{
		`{"groups":{}}`,
		`{"private":{}}`,
		`{"private":{"scope":"specified"}}`,
		`{"add_chat_ids":["oc_a"],"remove_chat_ids":["oc_a"]}`,
		`{"remove_chat_ids":["oc_a","oc_a"]}`,
		`{"add_chat_ids":["oc_a"],"groups":{"chat_ids":[]}}`,
		`{"remove_chat_ids":["oc_a"],"groups":{"chat_ids":["oc_b"]}}`,
		`{"groups":{"chat_ids":["oc_a","oc_a"]}}`,
		`{"groups":{"scope":"exclude"}}`,
		`{"groups":{"chat_ids":["ou_person"]}}`,
		`{"add_chat_ids":["oc_a"],"groups":{"enabled":false}}`,
		`{"remove_chat_ids":["oc_a"],"groups":{"scope":"all"}}`,
	} {
		in := base
		if json.Unmarshal([]byte(raw), &in) == nil && validPlatformMemberInput("notifications", in) {
			t.Fatal("invalid patch accepted", raw)
		}
	}
}

func TestPlatformMemberLarkCLIRejectsAmbiguousAndUnsupportedFields(t *testing.T) {
	m, svc, member, req := memberToolFixture(t)
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, member)
	base := []string{"member", "notifications", "--platform-id", req.Input.PlatformID, "--platform-version", strconv.FormatUint(req.Input.PlatformVersion, 10), "--notification-version", "0", "--operation-id", req.OperationID, "--source-seq", strconv.FormatUint(req.SourceSeq, 10)}
	for _, flags := range [][]string{
		{"--group-enabled", "1"},
		{"--private-enabled", "yes"},
		{"--group-at-me", "False"},
		{"--group-scope", "exclude"},
		{"--private-scope", "specified"},
		{"--private-chat-id", "oc_private"},
		{"--interval-seconds", "20"},
		{"--group-chat-id", "", "--group-chat-id", "oc_a"},
		{"--group-chat-id", "oc_a", "--group-chat-id", ""},
		{"--group-chat-id", "oc_a", "--remove-chat-id", "oc_b"},
		{"--add-chat-id", "oc_a", "--remove-chat-id", "oc_a"},
	} {
		var out bytes.Buffer
		args := append(append([]string{}, base...), flags...)
		if err := runPlatformConnectWithClient(context.Background(), args, &out, &out, env, client); err == nil {
			t.Fatal("unsupported or contradictory CLI flags accepted", flags, out.String())
		}
	}
	// Unknown nested input must fail HTTP decoding, rather than silently accepting
	// future specified-private fields or an unrestricted provider config.
	for _, raw := range []string{`{"scope":"specified"}`, `{"enabled":false,"chat_ids":[]}`} {
		in := *req.Input
		in.Enabled, in.ToolRevision, in.Actions = nil, 0, nil
		body, _ := json.Marshal(platformMemberRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, OperationID: req.OperationID, SourceSeq: req.SourceSeq, Input: &in})
		var wire map[string]any
		_ = json.Unmarshal(body, &wire)
		var private map[string]any
		_ = json.Unmarshal([]byte(raw), &private)
		wire["input"].(map[string]any)["private"] = private
		if r := managerHTTP(m, "/v1/platforms/member/notifications", wire); r.Code != 400 {
			t.Fatal("specified-private input accepted before query support", r.Code, r.Body.String())
		}
	}
	if _, err := os.Stat(m.platformMemberPath(req.OperationID)); !os.IsNotExist(err) {
		t.Fatal("invalid input reserved an operation", err)
	}
}

func TestPlatformMemberLarkAbsentDisableDoesNotCreateSubscription(t *testing.T) {
	m, svc, member, req := memberToolFixture(t)
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, member)
	base := []string{"member", "notifications", "--platform-id", req.Input.PlatformID, "--platform-version", strconv.FormatUint(req.Input.PlatformVersion, 10), "--notification-version", "0"}
	for i, flags := range [][]string{
		{"--enabled", "false"},
		{"--private-enabled", "false", "--group-enabled", "false"},
		{"--remove-chat-id", "oc_inaccessible"},
		{"--group-chat-id", ""},
	} {
		source, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller, ClientMessageID: "absent-disable-" + strconv.Itoa(i), Text: "取消不存在的关注或关闭通知"})
		if err != nil {
			t.Fatal(err)
		}
		id, _ := newOpaqueID()
		args := append(append([]string{}, base...), "--operation-id", id, "--source-seq", strconv.FormatUint(source.Seq, 10))
		args = append(args, flags...)
		var out, stderr bytes.Buffer
		if err := runPlatformConnectWithClient(context.Background(), args, &out, &stderr, env, client); err != nil {
			t.Fatal(err, stderr.String())
		}
		var op platformMemberOperation
		if json.Unmarshal(out.Bytes(), &op) != nil || op.Status != "completed" || op.Result.Status != "preserved" || op.Result.Version != 0 || op.Config.Enabled || len(op.Chats) != 0 {
			t.Fatal(out.String())
		}
		m.mu.Lock()
		current, err := m.currentLarkForTargetLocked(req.RoomIdentity, req.Input.PlatformID, platformMemberTarget(req.member()))
		stored, readErr := m.readPlatformMemberLocked(id)
		m.mu.Unlock()
		if err != nil || current != nil || readErr != nil || !reflect.DeepEqual(stored, op) {
			t.Fatal("no-op created a subscription or lost its frozen receipt", current, stored, err, readErr)
		}
		out.Reset()
		if err := runPlatformConnectWithClient(context.Background(), args, &out, &stderr, env, client); err != nil {
			t.Fatal("no-op replay failed", err)
		}
		var replay platformMemberOperation
		if json.Unmarshal(out.Bytes(), &replay) != nil || !reflect.DeepEqual(replay, op) {
			t.Fatal("no-op replay changed receipt")
		}
	}
	if _, err := os.Stat(m.toolBindingsPath(req.RoomIdentity)); !os.IsNotExist(err) {
		t.Fatal("notification changes created tool bindings", err)
	}
}
