//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestLarkPlatformAndMembershipReadBoundary(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, _, members := cliToolFixture(t, "exit 0")
	m.toolCatalog = append(m.toolCatalog, tool.Definition{Name: "lark", Provider: "lark", InternalOnly: true})
	dir := t.TempDir()
	bin := filepath.Join(dir, "lark-cli")
	if err := os.WriteFile(bin, []byte(larkFixture), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	q := managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("b", 32), Name: "飞书", Provider: "lark", URL: lark.Origin("feishu"), CLIProfile: &platform.CLIProfile{Version: 1, Executable: bin, Profile: "fixture", Brand: "feishu", CLIVersion: lark.CLIVersion}}
	c := platformTestSave(t, m, q)
	bind := toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Enabled: true, Binding: tool.Binding{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID, Tool: "lark", PlatformID: c.ID}, PlatformVersion: c.Version}
	if r := managerHTTP(m, "/v1/tools/bind", bind); r.Code == 200 {
		t.Fatal("unverified profile bound")
	}
	c, err := m.verifyPlatform(context.Background(), managedPlatformRequest{ID: c.ID, Version: c.Version})
	if err != nil || c.Validation.Status != "valid" || c.Validation.Username != "中文用户" || c.Validation.UserID != 0 {
		t.Fatal(c, err)
	}
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "lark", 0)
	req := larkReadRequest{ServiceInfo: svc.info, Tool: c.ToolEntryID, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Messages: &lark.MessagesRequest{ChatID: "oc_chat", Start: 99, End: 101, PageSize: 2}}
	response := managerHTTP(m, "/v1/platform/lark/messages", req)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "fixture message") {
		t.Fatal(response.Code, response.Body.String())
	}
	invalid := req
	invalid.Messages = &lark.MessagesRequest{ChatID: "oc_chat", Start: 99, End: 101, PageSize: 51}
	if r := managerHTTP(m, "/v1/platform/lark/messages", invalid); r.Code != 400 {
		t.Fatal("invalid local read request was not rejected", r.Code)
	}
	other := req
	other.Caller = agentCaller(members[1].AgentID, members[1].MembershipID)
	if r := managerHTTP(m, "/v1/platform/lark/messages", other); r.Code == 200 {
		t.Fatal("borrowed other member binding")
	}
	other.Caller = humanCaller
	if r := managerHTTP(m, "/v1/platform/lark/messages", other); r.Code == 200 {
		t.Fatal("human bypassed member admission")
	}
	m.mu.Lock()
	_, _, _, err = m.prepareToolLocked(context.Background(), toolsRequest{ServiceInfo: svc.info, Caller: req.Caller, Tool: "lark", Args: []string{"api", "POST", "/open-apis/im/v1/messages"}})
	m.mu.Unlock()
	if err == nil {
		t.Fatal("opened arbitrary lark CLI")
	}
	file, err := m.platforms.Load()
	if err != nil || len(file.OAuthCredentials) != 1 {
		t.Fatal("profile copied credentials", err)
	}
	// A profile switch after the page read discards candidate and pauses the
	// connection, while preserving the explicit member binding for re-verification.
	_ = os.Remove(filepath.Join(dir, "read"))
	_ = os.WriteFile(filepath.Join(dir, "mode"), []byte("switch"), 0600)
	response = managerHTTP(m, "/v1/platform/lark/messages", req)
	if response.Code == 200 || strings.Contains(response.Body.String(), "fixture message") || !strings.Contains(response.Body.String(), "identity_mismatch") {
		t.Fatal(response.Code, response.Body.String())
	}
	listed := platformTestList(t, m)
	for _, x := range listed {
		if x.ID == c.ID && (x.Validation.Status != "failed" || x.Validation.Account.Valid()) {
			t.Fatal("profile switch did not pause", x)
		}
	}
	if r := managerHTTP(m, "/v1/platform/lark/messages", req); r.Code == 200 {
		t.Fatal("paused connection still reads")
	}
	// The original profile is restored in an isolated fixture, never by changing
	// the operator's real login. Explicit verification resumes admission.
	_ = os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600)
	for _, x := range listed {
		if x.ID == c.ID {
			c = x
		}
	}
	c, err = m.verifyPlatform(context.Background(), managedPlatformRequest{ID: c.ID, Version: c.Version})
	if err != nil || c.Validation.Status != "valid" {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "read"))
	_ = os.WriteFile(filepath.Join(dir, "mode"), []byte("block"), 0600)
	done := make(chan error, 1)
	go func() { _, err := m.readLark(context.Background(), req); done <- err }()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "read")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("read not admitted")
		case <-time.After(10 * time.Millisecond):
		}
	}
	response = managerHTTP(m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Revision: 1, Binding: tool.Binding{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID, Tool: "lark", PlatformID: c.ID}})
	if response.Code != 200 {
		t.Fatal("revoke did not settle", response.Code, response.Body.String())
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked candidate accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoke leaked process")
	}
	var bindings toolBindingsFile
	if json.Unmarshal(response.Body.Bytes(), &bindings) != nil || len(bindings.Bindings) != 0 {
		t.Fatal("binding not revoked")
	}
}
