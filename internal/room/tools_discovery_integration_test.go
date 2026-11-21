//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/extension"
	"github.com/go-aie/teamx/internal/app/extensions"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
	publicplatform "github.com/go-aie/teamx/platform"
)

// The pre-change GitLab workflow is unchanged by this projection. This measures
// the actual former wire shape, including A2/B2's already shortened workflow.
func toolDiscoveryBaseline(selections []toolSelection) []any {
	rows := make([]any, 0, len(selections))
	for _, s := range selections {
		if s.Provider == "gitlab" {
			workflow := projectToolSelections([]toolSelection{s})[0].Workflow
			if s.Skill != "" {
				s.Skill = `"$TEAMX_CLI" tools skill ` + s.Name
			}
			rows = append(rows, struct {
				toolSelection
				Workflow string `json:"workflow"`
			}{s, workflow})
		} else {
			rows = append(rows, s)
		}
	}
	return rows
}

func TestToolDiscoverySelectedCompositionsAndExecution(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "printf discovered-local")
	bin := m.toolCatalog[1].Executable
	m.toolCatalog[0].Skill = "skills/gitlab/SKILL.md"
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	bindCLITool(t, m, room.RoomIdentity, members[0], managedPlatform{}, "local", 0)
	bindingsBefore, err := os.ReadFile(m.toolBindingsPath(room.RoomIdentity))
	if err != nil {
		t.Fatal(err)
	}
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, members[0])
	read := func() []agentToolEntry {
		t.Helper()
		var out, stderr bytes.Buffer
		if err := runToolsWithClient(t.Context(), []string{"list"}, nil, &out, &stderr, env, client); err != nil {
			t.Fatal(err)
		}
		var rows []agentToolEntry
		if json.Unmarshal(out.Bytes(), &rows) != nil {
			t.Fatal(out.String())
		}
		return rows
	}
	// Use the chosen default catalog, then an explicit empty composition and a
	// public external declaration. Old bindings never reintroduce absent tools.
	m.toolCatalog = nil
	if rows := read(); len(rows) != 1 || rows[0].Name != c.ToolEntryID {
		t.Fatal("default selection", rows)
	}
	empty, err := extension.Compose()
	if err != nil {
		t.Fatal(err)
	}
	m.platformCatalog, err = extensions.Catalog(empty)
	if err != nil {
		t.Fatal(err)
	}
	if rows := read(); len(rows) != 0 {
		t.Fatal("empty composition acquired tools", rows)
	}
	selected, err := extension.Compose(extension.Extension{Name: "local-extension", Tools: []publicplatform.Tool{{Name: "local", Description: "Read local build results", Version: "fixture", Executable: bin}}})
	if err != nil {
		t.Fatal(err)
	}
	m.platformCatalog, err = extensions.Catalog(selected)
	if err != nil {
		t.Fatal(err)
	}
	rows := read()
	if len(rows) != 1 || rows[0].Name != "local" || rows[0].Description != "Read local build results" || !reflect.DeepEqual(rows[0].Capabilities, []string{"run"}) || rows[0].PlatformID != "" || rows[0].Skill != "" || rows[0].Workflow != "" || rows[0].Account != nil || rows[0].ConfiguredActions != nil {
		t.Fatal("external local declaration changed", rows)
	}
	// Discovery immediately supplies the executable entry: no detail, account,
	// Skill or Workspace query between list and run.
	var out, stderr bytes.Buffer
	if err := runToolsWithClient(t.Context(), []string{"run", rows[0].Name}, strings.NewReader(""), &out, &stderr, env, client); err != nil || out.String() != "discovered-local" {
		t.Fatal("discovered local entry did not execute", err, out.String(), stderr.String())
	}
	definition := m.toolDefinitions()[0]
	definition.Executable = filepath.Join(t.TempDir(), "missing-tool")
	m.toolCatalog = []tool.Definition{definition}
	rows = read()
	if len(rows) != 1 || len(rows[0].Capabilities) != 0 || rows[0].UnavailableReason != "executable_unavailable" {
		t.Fatal("unavailable bound tool disappeared", rows)
	}
	m.toolCatalog[0].Executable = bin
	if rows := read(); len(rows) != 1 || !slices.Contains(rows[0].Capabilities, "run") || rows[0].UnavailableReason != "" {
		t.Fatal("repair not visible", rows)
	}
	// Unbound members and disabled historical choices remain excluded.
	if w := managerHTTP(m, "/v1/tools/list", toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[1].AgentID, members[1].MembershipID)}); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("other member inherited entries", w.Code, w.Body.String())
	}
	bindingsAfter, err := os.ReadFile(m.toolBindingsPath(room.RoomIdentity))
	if err != nil || !bytes.Equal(bindingsBefore, bindingsAfter) {
		t.Fatal("discovery changed stored bindings", err)
	}
	m.mu.Lock()
	f, err := m.readToolBindingsLocked(room.RoomIdentity)
	selections, selectionErr := m.selectionsLocked(f, agentCaller(members[0].AgentID, members[0].MembershipID))
	m.mu.Unlock()
	if err != nil || selectionErr != nil {
		t.Fatal(err, selectionErr)
	}
	before, after := mustContractJSON(toolDiscoveryBaseline(selections)), mustContractJSON(projectToolSelections(selections))
	if len(after) >= len(before) {
		t.Fatal("local discovery did not shrink")
	}
	t.Logf("external local response bytes: before=%d after=%d", len(before), len(after))
}

func TestToolDiscoveryAccountFailureAndRepair(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, a, members := cliToolFixture(t, "printf native")
	m.toolCatalog[0].Skill = "skills/gitlab/SKILL.md"
	b := sharedToolAccount(t, m)
	bindCLITool(t, m, room.RoomIdentity, members[0], a, "gitlab", 0)
	bindCLITool(t, m, room.RoomIdentity, members[0], b, "gitlab", 0)
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID)}
	list := func() []agentToolEntry {
		t.Helper()
		w := managerHTTP(m, "/v1/tools/list", req)
		var rows []agentToolEntry
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return rows
	}
	rows := list()
	if len(rows) != 2 || rows[0].Name != a.ToolEntryID || rows[1].Name != b.ToolEntryID || rows[0].Instance != rows[1].Instance || rows[0].Username == rows[1].Username || rows[0].PlatformID == rows[1].PlatformID {
		t.Fatal("account disambiguation lost", rows)
	}
	m.mu.Lock()
	f, err := m.readToolBindingsLocked(room.RoomIdentity)
	selections, selectionErr := m.selectionsLocked(f, req.Caller)
	file, loadErr := m.platforms.Load()
	m.mu.Unlock()
	if err != nil || selectionErr != nil || loadErr != nil {
		t.Fatal(err, selectionErr, loadErr)
	}
	before, after := mustContractJSON(toolDiscoveryBaseline(selections)), mustContractJSON(projectToolSelections(selections))
	if len(after) >= len(before) {
		t.Fatal("multi-account discovery did not shrink")
	}
	t.Logf("two GitLab accounts response bytes: before=%d after=%d", len(before), len(after))
	// A credential failure removes readiness but preserves the exact bound
	// account and readable Skill. It cannot cause selection of the other account.
	oldState := file.OAuthCredentials[0].State
	file.OAuthCredentials[0].State = "reauthorization_required"
	if err := m.store.save(filepath.Join(m.store.dir, "platform-connections.json"), file, 1<<20); err != nil {
		t.Fatal(err)
	}
	rows = list()
	if len(rows) != 2 || rows[0].Name != a.ToolEntryID || len(rows[0].Capabilities) != 0 || rows[0].UnavailableReason != "account_unavailable" || rows[0].Skill != `"$TEAMX_CLI" tools skill `+a.ToolEntryID || len(rows[1].Capabilities) == 0 {
		t.Fatal("account failure hidden or substituted", rows)
	}
	file.OAuthCredentials[0].State = oldState
	if err := m.store.save(filepath.Join(m.store.dir, "platform-connections.json"), file, 1<<20); err != nil {
		t.Fatal(err)
	}
	if rows := list(); rows[0].UnavailableReason != "" || len(rows[0].Capabilities) == 0 {
		t.Fatal("account repair not visible", rows)
	}
	// Connection replacement cannot advertise the provider alias as an ENTRY.
	f.Bindings[0].PlatformGeneration = strings.Repeat("e", 32)
	if err := m.store.save(m.toolBindingsPath(room.RoomIdentity), f, 1<<20); err != nil {
		t.Fatal(err)
	}
	rows = list()
	if len(rows) != 2 || rows[0].Name != "" || rows[0].PlatformID != a.ID || rows[0].UnavailableReason != "platform_changed" || rows[0].Skill != "" || rows[0].Workflow != "" {
		t.Fatal("replaced connection exposed an alias", rows)
	}
}

func TestToolDiscoveryLarkReadinessAndDisabledIntent(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	f := bindCLITool(t, h.m, h.room.RoomIdentity, h.members[0], h.c, "lark", 0)
	req := toolsRequest{ServiceInfo: h.svc.info, Caller: agentCaller(h.members[0].AgentID, h.members[0].MembershipID)}
	list := func() []agentToolEntry {
		t.Helper()
		w := managerHTTP(h.m, "/v1/tools/list", req)
		var rows []agentToolEntry
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return rows
	}
	rows := list()
	if len(rows) != 1 || rows[0].Account == nil || rows[0].Availability == nil || !rows[0].Availability.ToolEnabled {
		t.Fatal("missing account/readiness", rows)
	}
	// A stale successful platform check must not be presented as live readiness.
	h.m.mu.Lock()
	file, err := h.m.platforms.Load()
	if err != nil {
		h.m.mu.Unlock()
		t.Fatal(err)
	}
	c := file.Platforms[0]
	for _, candidate := range file.Platforms {
		if candidate.ID == h.c.ID {
			c = candidate
			break
		}
	}
	if c.ID != h.c.ID {
		h.m.mu.Unlock()
		t.Fatal("Lark fixture connection missing")
	}
	h.m.larkPermissionChecks = map[string]larkPlatformCapabilityResult{c.ID: {PlatformID: c.ID, PlatformVersion: c.Version, Generation: c.Generation, CredentialVersion: c.CredentialVersion, PermissionCheck: lark.PermissionCheck{Account: c.Validation.Account, CheckedAt: time.Now(), Capabilities: []lark.Capability{{Name: "message.read", State: "blocked", ReasonCode: "user_scope_missing", MissingUserScopes: []string{"im:message:readonly"}, EvidenceState: "not_required", NextAction: "authorize_user"}}}}}
	h.m.mu.Unlock()
	rows = list()
	missingScope := false
	for _, cap := range rows[0].Availability.Capabilities {
		if cap.Name == "message.read" {
			missingScope = cap.State == "blocked" && cap.ReasonCode == "user_scope_missing" && cap.NextAction == "authorize_user" && reflect.DeepEqual(cap.MissingUserScopes, []string{"im:message:readonly"})
		}
	}
	if !missingScope {
		t.Fatal("permission repair evidence lost")
	}
	h.m.mu.Lock()
	h.m.larkPermissionChecks = map[string]larkPlatformCapabilityResult{c.ID: {PlatformID: c.ID, PlatformVersion: c.Version, Generation: c.Generation, CredentialVersion: c.CredentialVersion, PermissionCheck: lark.PermissionCheck{Account: c.Validation.Account, CheckedAt: time.Now().Add(-2 * larkCapabilityMaxAge), Capabilities: []lark.Capability{{Name: "message.read", State: "ready"}}}}}
	h.m.mu.Unlock()
	rows = list()
	found := false
	for _, cap := range rows[0].Availability.Capabilities {
		if cap.Name == "message.read" {
			found = cap.State == "unchecked" && cap.ReasonCode == "check_expired" && cap.NextAction == "retry_check"
		}
		if cap.Name == "message.reply" && (cap.State != "blocked" || cap.ReasonCode != "member_action_not_configured") {
			t.Fatal("missing action intent became authority", cap)
		}
	}
	if !found {
		t.Fatal("expired check became ready")
	}
	// Turning the binding off retains history for settings/recovery, while the
	// Agent business directory remains empty.
	b := f.Bindings[0]
	b.PlatformGeneration = ""
	w := managerHTTP(h.m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ExpectedBindingRevision: new(uint64(f.Revision)), Binding: b, Enabled: false})
	if w.Code != http.StatusOK || len(list()) != 0 {
		t.Fatal("disabled binding still advertised", w.Code, w.Body.String())
	}
}

func TestToolDiscoveryScionDirectGuidanceAndHelp(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members, _ := scionRoomFixture(t)
	c = verifyScion(t, m, c)
	f := bindCLITool(t, m, room.RoomIdentity, members[0], c, "scion", 0)
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID)}
	w := managerHTTP(m, "/v1/tools/list", req)
	var rows []agentToolEntry
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != 1 || rows[0].Name != c.ToolEntryID || rows[0].Skill != `"$TEAMX_CLI" tools skill `+c.ToolEntryID || !slices.Contains(rows[0].Capabilities, "run") {
		t.Fatal("Scion discovery", w.Code, w.Body.String())
	}
	client, endpoint := toolPipeServer(t, m)
	var out, stderr bytes.Buffer
	if err := runToolsWithClient(t.Context(), []string{"run", rows[0].Name, "--help"}, strings.NewReader(""), &out, &stderr, cliToolEnv(endpoint, svc, members[0]), client); err != nil || !strings.Contains(out.String(), "native help") {
		t.Fatal("discovered Scion help", err, out.String(), stderr.String())
	}
	m.mu.Lock()
	selections, err := m.selectionsLocked(f, req.Caller)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	before, after := mustContractJSON(toolDiscoveryBaseline(selections)), mustContractJSON(projectToolSelections(selections))
	if len(after) >= len(before) {
		t.Fatal("Scion discovery did not shrink")
	}
	t.Logf("Scion response bytes: before=%d after=%d", len(before), len(after))
}
