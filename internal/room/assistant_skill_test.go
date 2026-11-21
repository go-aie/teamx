package room

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func assistantConnectFixture(t *testing.T) (*roomManager, *service, MemberState, platformConnectRequest) {
	t.Helper()
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	ensureAssistant(t, m)
	r := assistantSummary(t, m)
	_, snapshot := readManagedRoom(t, m, r)
	svc := eventLiveService(t, &eventHarness{m: m, room: r})
	source, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{
		RoomIdentity: r.RoomIdentity, Caller: humanCaller, ClientMessageID: "assistant-connect", Text: "连接飞书，名称为工作飞书",
	})
	if err != nil {
		t.Fatal(err)
	}
	member := snapshot.Members[0]
	id, err := newOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	return m, svc, member, platformConnectRequest{
		ServiceInfo: svc.info, Caller: agentCaller(member.AgentID, member.MembershipID),
		OperationID: id, SourceSeq: source.Seq, Input: &platformConnectInput{Provider: "lark", Name: "工作飞书"},
	}
}

// Discover the packaged resource through the actual member CLI protocol, before
// connections or bindings exist. Reading it must not start onboarding or install
// a tool, including for an independently created DM using the same role.
func TestDefaultAssistantSkillWithoutPlatformBinding(t *testing.T) {
	m, svc, member, req := assistantConnectFixture(t)
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, member)
	want, err := tool.ReadSkill(tool.Definition{Skill: "skills/platforms/SKILL.md"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runPlatformConnectWithClient(context.Background(), []string{"skill"}, &out, &out, env, client); err != nil || !bytes.Equal(out.Bytes(), want) {
		t.Fatal("embedded Skill unavailable to unbound assistant", err)
	}
	completeSize := len(want)
	for _, topic := range []string{"notifications", "selection"} {
		t.Run(topic, func(t *testing.T) {
			want, err := tool.ReadPlatformSkill(topic)
			if err != nil {
				t.Fatal(err)
			}
			out.Reset()
			if err := runPlatformConnectWithClient(t.Context(), []string{"skill", "--topic", topic}, &out, &out, env, client); err != nil || !bytes.Equal(out.Bytes(), want) {
				t.Fatal("topic unavailable through member CLI before binding", err, out.String())
			}
			if len(want) >= completeSize {
				t.Fatal("task read returned complete guidance")
			}
		})
	}
	for _, topic := range []string{"unknown", "../SKILL", "Notifications"} {
		out.Reset()
		if err := runPlatformConnectWithClient(t.Context(), []string{"skill", "--topic", topic}, &out, &out, env, client); err == nil || !strings.Contains(err.Error(), "topic") || !strings.Contains(err.Error(), "selection") {
			t.Fatal("unknown topic did not return actionable server error", topic, err)
		}
		bad := platformConnectRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, Topic: topic}
		if w := managerHTTP(m, "/v1/platforms/skill", bad); w.Code != http.StatusBadRequest {
			t.Fatal("unknown HTTP topic was not rejected", topic, w.Code, w.Body.String())
		}
	}
	// topic is a resource selector, never a silently ignored operation argument.
	for _, action := range []string{"list", "connect/status"} {
		bad := platformConnectRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, Topic: "notifications"}
		if w := managerHTTP(m, "/v1/platforms/"+action, bad); w.Code != http.StatusBadRequest {
			t.Fatal("topic accepted by another operation", action, w.Code)
		}
		out.Reset()
		args := append(strings.Split(action, "/"), "--topic", "notifications")
		if err := runPlatformConnectWithClient(t.Context(), args, &out, &out, env, client); err == nil {
			t.Fatal("CLI accepted topic for another operation", action)
		}
	}
	for _, topic := range []string{"", "notifications", "selection"} {
		bad := platformConnectRequest{ServiceInfo: req.ServiceInfo, Caller: humanCaller, Topic: topic}
		if w := managerHTTP(m, "/v1/platforms/skill", bad); w.Code != http.StatusForbidden {
			t.Fatal("resource access bypassed bound-member identity", topic, w.Code)
		}
		bad.Caller = req.Caller
		bad.Caller.MembershipID = strings.Repeat("f", 32)
		if w := managerHTTP(m, "/v1/platforms/skill", bad); w.Code != http.StatusForbidden {
			t.Fatal("stale membership read guidance", topic, w.Code)
		}
	}
	if w := managerHTTP(m, "/v1/platforms/list", platformConnectRequest{ServiceInfo: svc.info, Caller: req.Caller}); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`[]`)) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := managerHTTP(m, "/v1/platforms/connect/status", platformConnectRequest{ServiceInfo: svc.info, Caller: req.Caller, SourceSeq: req.SourceSeq}); w.Code != 404 {
		t.Fatal("reading Skill started a connection", w.Code, w.Body.String())
	}
	m.mu.Lock()
	bindings, err := m.readToolBindingsLocked(req.RoomIdentity)
	m.mu.Unlock()
	if err != nil || len(bindings.Bindings) != 0 {
		t.Fatal("reading Skill installed tools", err)
	}
	other := createDirectConversation(t, m, directSelection("independent-assistant"))
	_, otherSnapshot := readManagedRoom(t, m, other)
	otherService := eventLiveService(t, &eventHarness{m: m, room: other})
	otherMember := otherSnapshot.Members[0]
	out.Reset()
	if err := runPlatformConnectWithClient(context.Background(), []string{"skill"}, &out, &out, cliToolEnv(endpoint, otherService, otherMember), client); err != nil || !bytes.Equal(out.Bytes(), want) {
		t.Fatal("Skill incorrectly limited to default instance", err)
	}
	// Skill access confers no authority to reuse another conversation's source.
	foreign := req
	foreign.ServiceInfo = otherService.info
	foreign.Caller = agentCaller(otherMember.AgentID, otherMember.MembershipID)
	if w := managerHTTP(m, "/v1/platforms/connect/start", foreign); w.Code != 403 {
		t.Fatal("other DM inherited human authority", w.Code, w.Body.String())
	}
}
