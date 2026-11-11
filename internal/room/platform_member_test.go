package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestPlatformMemberNotificationPatchPreservesRangeAndStart(t *testing.T) {
	m, _, _, req := connectFixture(t)
	id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	platformID := strings.Repeat("c", 32)
	in := platformMemberInput{PlatformID: platformID, PlatformVersion: 2, AddChatIDs: []string{"oc_selected"}}
	v := platformMemberView{MemberIdentity: id}
	cfg, err := memberNotificationConfig(m, v, in)
	if err != nil || !cfg.Enabled || !cfg.Groups.Enabled || cfg.Private.Enabled || cfg.Groups.Scope != "specified" || cfg.Groups.MessageMode != "all" || cfg.Groups.AtMe || cfg.Groups.AtAll || cfg.BackfillStart.IsZero() || cfg.Target != inboxTarget(id) {
		t.Fatal("unexpected opt-in", cfg, err)
	}
	in.GroupMessageMode = "mentions"
	mentions, err := memberNotificationConfig(m, v, in)
	if err != nil || mentions.Groups.MessageMode != "mentions" || !mentions.Groups.AtMe || mentions.Groups.AtAll {
		t.Fatal("explicit mentions did not receive mentions", mentions, err)
	}
	in.GroupMessageMode = ""
	cfg.Private.Enabled, cfg.Private.Scope, cfg.Private.ChatIDs = true, "specified", []string{"oc_private"}
	cfg.Groups.ChatIDs, cfg.Groups.MessageMode, cfg.Groups.AtMe = []string{"oc_old"}, "mentions", true
	cfg.IntervalSeconds, cfg.BackfillStart = 61, time.Unix(40, 0).UTC()
	v.Config = &cfg
	before, _ := json.Marshal(cfg)
	got, err := memberNotificationConfig(m, v, in)
	if err != nil || !reflect.DeepEqual(got.Private, cfg.Private) || got.Groups.MessageMode != cfg.Groups.MessageMode || got.Groups.AtMe != cfg.Groups.AtMe || got.IntervalSeconds != cfg.IntervalSeconds || got.BackfillStart != cfg.BackfillStart || !reflect.DeepEqual(got.Groups.ChatIDs, []string{"oc_old", "oc_selected"}) {
		t.Fatal("patch lost unrelated settings", got, err)
	}
	after, _ := json.Marshal(cfg)
	if !bytes.Equal(before, after) {
		t.Fatal("patch mutated its input")
	}
	cfg.Groups.Scope = "all"
	if _, err := memberNotificationConfig(m, v, in); err == nil {
		t.Fatal("silently replaced all-group scope")
	}
}

func memberToolFixture(t *testing.T) (*roomManager, *service, MemberState, platformMemberRequest) {
	t.Helper()
	m, svc, member, connect := connectFixture(t)
	_, session := connectPending(t, m, svc, connect)
	connectSucceed(t, m, session)
	op := connectHTTP(t, m, "status", platformConnectRequest{ServiceInfo: connect.ServiceInfo, Caller: connect.Caller, OperationID: connect.OperationID})
	validation := connectValidation()
	validation.Account.ID.Namespace = "lark:open_id:cli_fixture"
	validation.Account.Tenant.Namespace = "lark:tenant_key"
	m.mu.Lock()
	ticket, err := m.platforms.BeginVerification(op.PlatformID, op.Connection.Version)
	if err == nil {
		var c platform.Platform
		c, err = m.platforms.CompleteVerification(ticket, validation)
		op.Connection.Version = c.Version
	}
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := newOpaqueID()
	enabled := true
	return m, svc, member, platformMemberRequest{ServiceInfo: connect.ServiceInfo, Caller: connect.Caller, OperationID: id, SourceSeq: connect.SourceSeq, Input: &platformMemberInput{PlatformID: op.PlatformID, PlatformVersion: op.Connection.Version, Enabled: &enabled}}
}

func TestPlatformMemberToolReceiptsIdentityAndIndependentVersions(t *testing.T) {
	m, svc, member, req := memberToolFixture(t)
	w := managerHTTP(m, "/v1/platforms/member/tool", req)
	var op platformMemberOperation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &op) != nil || op.Status != "completed" || op.Result.Version != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Seed an existing operator grant to verify repeated enable keeps it exactly.
	m.mu.Lock()
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err == nil {
		f.Bindings[0].ActionAuthorization = &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: "reply", TargetKind: "message", ObjectType: "lark.message"}}}
		err = m.store.save(m.toolBindingsPath(req.RoomIdentity), f, 1<<20)
	}
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(m.toolBindingsPath(req.RoomIdentity))
	// Both successful retries and pure status preserve the exact saved receipt.
	if replay := managerHTTP(m, "/v1/platforms/member/tool", req); replay.Body.String() != w.Body.String() {
		t.Fatal("replay changed receipt", replay.Body.String())
	}
	query := platformMemberRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, SourceSeq: req.SourceSeq}
	status := managerHTTP(m, "/v1/platforms/member/status", query)
	if status.Code != 200 || !strings.Contains(status.Body.String(), op.ID) {
		t.Fatal(status.Code, status.Body.String())
	}
	bad := req
	in := *req.Input
	bad.Input = &in
	in.PlatformVersion++
	if r := managerHTTP(m, "/v1/platforms/member/tool", bad); r.Code != 409 {
		t.Fatal("changed input accepted", r.Code)
	}
	bad = req
	bad.OperationID, _ = newOpaqueID()
	if r := managerHTTP(m, "/v1/platforms/member/tool", bad); r.Code != 409 {
		t.Fatal("new-ID replay accepted", r.Code, r.Body.String())
	}
	// Enable under a later real human request is a no-op that keeps all grants.
	source, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller, ClientMessageID: "enable-again", Text: "启用我的飞书工具"})
	if err != nil {
		t.Fatal(err)
	}
	bad.SourceSeq, in = source.Seq, *req.Input
	in.ToolRevision = op.Result.Version
	bad.Input = &in
	if r := managerHTTP(m, "/v1/platforms/member/tool", bad); r.Code != 200 || !strings.Contains(r.Body.String(), "preserved") {
		t.Fatal(r.Code, r.Body.String())
	}
	after, _ := os.ReadFile(m.toolBindingsPath(req.RoomIdentity))
	if !bytes.Equal(before, after) {
		t.Fatal("repeated enable changed bindings")
	}
	read := platformMemberRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, Input: &platformMemberInput{PlatformID: req.Input.PlatformID, PlatformVersion: req.Input.PlatformVersion}}
	if r := managerHTTP(m, "/v1/platforms/member/get", read); r.Code != 200 || !strings.Contains(r.Body.String(), `"tool_enabled":true`) || strings.Contains(r.Body.String(), "fingerprint") {
		t.Fatal(r.Code, r.Body.String())
	}
	// Reverification changes account version even when native identity stays equal.
	m.mu.Lock()
	ticket, err := m.platforms.BeginVerification(req.Input.PlatformID, req.Input.PlatformVersion)
	if err == nil {
		_, err = m.platforms.CompleteVerification(ticket, connectValidation())
	}
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if r := managerHTTP(m, "/v1/platforms/member/get", read); r.Code != 409 {
		t.Fatal("stale account version accepted", r.Code)
	}
	query.OperationID, query.SourceSeq = req.OperationID, 0
	if _, err := svc.remove(apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}); err != nil {
		t.Fatal(err)
	}
	replacement := inboxRoutingMember(t, svc, member.AgentID, "member-config-replacement")
	query.Caller = agentCaller(replacement.AgentID, replacement.MembershipID)
	if r := managerHTTP(m, "/v1/platforms/member/status", query); r.Code == 200 {
		t.Fatal("replacement inherited receipt")
	}
	// Human settings endpoints continue to reject the Agent identity.
	if r := managerHTTP(m, "/v1/platform/lark/chats", larkChatsRequest{ServiceEpoch: m.epoch, RoomIdentity: req.RoomIdentity, Caller: query.Caller, PlatformID: req.Input.PlatformID, PlatformVersion: req.Input.PlatformVersion}); r.Code == 200 {
		t.Fatal("human endpoint was relaxed")
	}
}

func TestPlatformMemberUnknownSubmissionNeverReplays(t *testing.T) {
	m, _, _, req := memberToolFixture(t)
	real := m.store.ops
	m.mu.Lock()
	m.store.ops.rename = func(from, to string) error {
		if to == m.platformMemberPath(req.OperationID) {
			var op platformMemberOperation
			data, _ := os.ReadFile(from)
			if json.Unmarshal(data, &op) == nil && op.Status == "completed" {
				return os.ErrPermission
			}
		}
		return real.rename(from, to)
	}
	m.mu.Unlock()
	if r := managerHTTP(m, "/v1/platforms/member/tool", req); r.Code == 200 {
		t.Fatal("uncertain save reported success")
	}
	m.mu.Lock()
	m.store.ops = real
	m.mu.Unlock()
	before, _ := os.ReadFile(m.toolBindingsPath(req.RoomIdentity))
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, m.store.dir, nil)
	// Recreate the current runtime identity; operation ownership does not use epoch.
	room := roomSummary{RoomIdentity: req.RoomIdentity}
	svc := eventLiveService(t, &eventHarness{m: reopened, room: room})
	req.ServiceInfo = svc.info
	r := managerHTTP(reopened, "/v1/platforms/member/tool", req)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"status":"unknown"`) {
		t.Fatal(r.Code, r.Body.String())
	}
	query := platformMemberRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, OperationID: req.OperationID}
	if r := managerHTTP(reopened, "/v1/platforms/member/status", query); r.Code != 200 || !strings.Contains(r.Body.String(), `"status":"unknown"`) {
		t.Fatal(r.Code, r.Body.String())
	}
	after, _ := os.ReadFile(reopened.toolBindingsPath(req.RoomIdentity))
	if !bytes.Equal(before, after) {
		t.Fatal("unknown operation replayed after restart")
	}
	req.OperationID, _ = newOpaqueID()
	req.Input.ToolRevision = 1
	if r := managerHTTP(reopened, "/v1/platforms/member/tool", req); r.Code != 409 {
		t.Fatal("unknown submission bypassed with a new ID", r.Code, r.Body.String())
	}
}

func TestPlatformMemberRejectsFabricatedAuthorityAndUnsupportedInput(t *testing.T) {
	m, svc, member, req := memberToolFixture(t)
	posted, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: req.Caller, ClientMessageID: "agent-config-source", Text: "开启工具"})
	if err != nil {
		t.Fatal(err)
	}
	bad := req
	bad.SourceSeq = posted.Seq
	if r := managerHTTP(m, "/v1/platforms/member/tool", bad); r.Code != 403 {
		t.Fatal("Agent text accepted as human authority", r.Code, r.Body.String())
	}
	for _, field := range []string{"source", "source_text", "agent_id", "membership_id", "cli_profile"} {
		data, _ := json.Marshal(req)
		var wire map[string]any
		_ = json.Unmarshal(data, &wire)
		wire[field] = "invented"
		if r := managerHTTP(m, "/v1/platforms/member/tool", wire); r.Code != 400 {
			t.Fatal("invented field accepted", field, r.Code)
		}
	}
	data, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/v1/platforms/member/tool", bytes.NewReader(data))
	r.Header.Set("Origin", "http://localhost")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("browser invoked Agent endpoint", w.Code)
	}
	client, endpoint := toolPipeServer(t, m)
	for _, args := range [][]string{
		{"member", "tool", "--notification-version", "1"},
		{"member", "notifications", "--tool-revision", "1"},
		{"chats", "--operation-id", req.OperationID},
		{"member", "get", "--source-seq", "1"},
		{"member", "status", "--operation-id", req.OperationID, "--source-seq", "1"},
		{"member", "tool", "--enabled", "1"},
	} {
		var out bytes.Buffer
		if err := runPlatformConnectWithClient(context.Background(), args, &out, &out, cliToolEnv(endpoint, svc, member), client); err == nil {
			t.Fatal("unsupported CLI input accepted", args)
		}
	}
	if _, err := os.Stat(m.platformMemberPath(req.OperationID)); !os.IsNotExist(err) {
		t.Fatal("invalid input reserved an operation", err)
	}
}
