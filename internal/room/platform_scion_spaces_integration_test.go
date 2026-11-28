//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/scion"
)

func scionSpacesProgram(t *testing.T) string {
	t.Helper()
	exe := scionAuthProgram(t)
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	branch := `if a[:3]==['--json','repo','info']:
 print(json.dumps({'schema':'scion.cli.repo/v1','name':a[3],'created_at_ms':100}));sys.exit()
if a[:8]==['--json','--repo','Owner/repo','issue','ls','--state','open','--limit']:
 print(json.dumps({'schema':'scion.cli.issue-list/v1','repo':'Owner/repo','issues':[],'truncated':False}));sys.exit()
`
	data = []byte(strings.Replace(string(data), "assert a==['--json','whoami',origin]", branch+"assert a==['--json','whoami',origin]", 1))
	if err := os.WriteFile(exe, data, 0700); err != nil {
		t.Fatal(err)
	}
	return exe
}

func createReadyScionSpace(t *testing.T, s *spaceServer, endpoint, id string) *SpaceRuntime {
	t.Helper()
	if code := spaceHTTPRequest(t, endpoint, "/v1/spaces", spaceChangeInput{RequestID: id, Name: "Scion private"}, nil); code != 202 {
		t.Fatal("create Space", code)
	}
	waitSpaceState(t, s, id, "setup_required")
	model := setupModelInput{RequestID: "scion-space-model", ExpectedVersion: 1, ModelParameters: modelTestEntry("private-scion").ModelParameters, APIKey: "synthetic-space-model-key"}
	if code := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup/model", model, nil); code != 202 {
		t.Fatal("setup Space", code)
	}
	return waitSpaceState(t, s, id, "ready")
}

func scionSpaceHTTP(t *testing.T, endpoint string, r *SpaceRuntime, path string, fields map[string]any, out any) int {
	t.Helper()
	body := map[string]any{"service_epoch": r.manager.epoch, "caller": Caller{Author: Author{Kind: "human", ID: r.OwnerUserID}}}
	for key, value := range fields {
		body[key] = value
	}
	return spaceHTTPRequest(t, endpoint, spaceRoute(r.SpaceID)+path, body, out)
}

func loginScionSpace(t *testing.T, endpoint string, r *SpaceRuntime, sessionID, pat string) scionAuthView {
	t.Helper()
	if code := scionSpaceHTTP(t, endpoint, r, "/v1/platforms/scion/login", map[string]any{"session_id": sessionID, "url": "https://scion.invalid", "pat": pat}, nil); code != 200 {
		t.Fatal("Space login denied", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var view scionAuthView
		if code := scionSpaceHTTP(t, endpoint, r, "/v1/platforms/scion/status", map[string]any{"session_id": sessionID}, &view); code != 200 {
			t.Fatal("Space login status", code)
		}
		if view.Status != "starting" {
			if view.Status != "succeeded" || view.Profile == nil {
				t.Fatal("Space login failed", view.Status, view.ErrorCode)
			}
			return view
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Space login did not settle")
	return scionAuthView{}
}

func saveScionSpace(t *testing.T, endpoint string, r *SpaceRuntime, id string, auth scionAuthView) managedPlatform {
	t.Helper()
	var saved managedPlatform
	if code := scionSpaceHTTP(t, endpoint, r, "/v1/platforms/save", map[string]any{"id": id, "version": 0, "name": "Scion", "provider": "scion", "url": "https://scion.invalid", "cli_profile": auth.Profile}, &saved); code != 200 {
		t.Fatal("save Scion connection", code)
	}
	if code := scionSpaceHTTP(t, endpoint, r, "/v1/platforms/verify", map[string]any{"id": saved.ID, "version": saved.Version}, &saved); code != 200 || saved.Validation.Status != "valid" {
		t.Fatal("verify Scion connection", code, saved.Validation.Status)
	}
	return saved
}

// The caller crosses the real Space execution-token middleware, including the
// generic provider envelope which originally rejected new-Space account cards.
func scionSpaceAgentHTTP(t *testing.T, endpoint string, r *SpaceRuntime, info ServiceInfo, caller Caller, path string, input, out any) int {
	t.Helper()
	token, revoke, err := r.config.access.issueExecution(executionBinding{ServiceInfo: info, Caller: caller})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+spaceRoute(r.SpaceID)+path, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if out != nil {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			t.Fatal("decode scoped Agent response", path, response.StatusCode, err)
		}
	}
	return response.StatusCode
}

func scionSpaceAccountCard(t *testing.T, endpoint string, r *SpaceRuntime, expectedAccounts int) platformSelectionView {
	t.Helper()
	svc := waitSpaceAssistant(t, r)
	snapshot, err := svc.store.LoadRoom()
	if err != nil || len(snapshot.Members) == 0 {
		t.Fatal("missing default assistant", err)
	}
	member := snapshot.Members[0]
	caller := agentCaller(member.AgentID, member.MembershipID)
	var listed struct {
		ScionEnabled bool `json:"scion_enabled"`
		Platforms    []struct {
			Provider  string `json:"provider"`
			Connected bool   `json:"connected"`
		} `json:"platforms"`
	}
	if code := scionSpaceAgentHTTP(t, endpoint, r, svc.info, caller, "/v1/platforms/list", platformConnectRequest{ServiceInfo: svc.info, Caller: caller}, &listed); code != 200 || !listed.ScionEnabled {
		t.Fatal("Agent did not discover Scion availability", code)
	}
	if len(listed.Platforms) != expectedAccounts {
		t.Fatal("Agent cannot discover saved Scion accounts", len(listed.Platforms))
	}
	for _, account := range listed.Platforms {
		if account.Provider != "scion" || !account.Connected {
			t.Fatal("Agent listed an unexpected account")
		}
	}
	id, _ := newOpaqueID()
	source, err := svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: svc.info.RoomIdentity, Caller: Caller{Author: Author{Kind: "human", ID: r.OwnerUserID}}, ClientMessageID: id, Text: "接入 Scion，并开启工具和 Owner/repo 的新 Issue 通知"})
	if err != nil {
		t.Fatal(err)
	}
	mode := "inbox"
	req := platformSelectionRequest{ServiceInfo: svc.info, Caller: agentCaller(member.AgentID, member.MembershipID), SourceSeq: source.Seq, OperationID: id, Input: &platformSelectionInput{Provider: "scion", Kind: "account", Origin: "https://scion.invalid"}, Notify: &mode}
	var card platformSelectionView
	if code := scionSpaceAgentHTTP(t, endpoint, r, svc.info, req.Caller, "/v1/platforms/selection/start", req, &card); code != 200 || card.Status != "waiting" {
		t.Fatal("new Space account card denied", code, card.Status)
	}
	return card
}

func TestScionSpacesCredentialIsolationLogoutAndRestart(t *testing.T) {
	t.Setenv("USER", humanCaller.ID)
	exe := scionSpacesProgram(t)
	t.Setenv("TEAMX_SCION_BIN", exe)
	home := t.TempDir()
	s, endpoint, stop := startSpaceServerFixture(t, home)
	a := waitSpaceState(t, s, s.initial.SpaceID, "ready")
	bID := strings.Repeat("b", 32)
	b := createReadyScionSpace(t, s, endpoint, bID)
	var directory struct {
		Spaces []struct {
			ID           string          `json:"id"`
			Capabilities map[string]bool `json:"capabilities"`
		} `json:"spaces"`
	}
	if code := spaceHTTPRequest(t, endpoint, "/v1/spaces", nil, &directory); code != 200 || len(directory.Spaces) != 2 {
		t.Fatal("Space directory", code)
	}
	for _, v := range directory.Spaces {
		if !v.Capabilities["scion"] {
			t.Fatal("personal Space omitted Scion capability", v.ID)
		}
	}
	// Identical native session and connection IDs are scoped by Space, not global.
	sessionID, connectionID := strings.Repeat("d", 32), strings.Repeat("c", 32)
	authA := loginScionSpace(t, endpoint, a, sessionID, "alice-pat")
	connA := saveScionSpace(t, endpoint, a, connectionID, authA)
	var empty scion.Discovery
	if code := scionSpaceHTTP(t, endpoint, b, "/v1/platforms/scion/discover", map[string]any{"url": "https://scion.invalid"}, &empty); code != 200 || len(empty.Profiles) != 0 {
		t.Fatal("new Space borrowed default credentials", code)
	}
	if code := scionSpaceHTTP(t, endpoint, b, "/v1/platforms/scion/status", map[string]any{"session_id": sessionID}, nil); code != 409 {
		t.Fatal("foreign login session accepted", code)
	}
	// Copying a public profile reference cannot reach another Space's file.
	var borrowed managedPlatform
	if code := scionSpaceHTTP(t, endpoint, b, "/v1/platforms/save", map[string]any{"id": strings.Repeat("e", 32), "version": 0, "provider": "scion", "name": "Borrowed", "url": "https://scion.invalid", "cli_profile": authA.Profile}, &borrowed); code != 200 {
		t.Fatal(code)
	}
	if code := scionSpaceHTTP(t, endpoint, b, "/v1/platforms/verify", map[string]any{"id": borrowed.ID, "version": borrowed.Version}, &borrowed); code != 200 || borrowed.Validation.Status != "failed" {
		t.Fatal("foreign profile authenticated", code)
	}
	if code := scionSpaceHTTP(t, endpoint, b, "/v1/platforms/delete", map[string]any{"id": borrowed.ID, "version": borrowed.Version}, nil); code != 200 {
		t.Fatal(code)
	}
	authB := loginScionSpace(t, endpoint, b, sessionID, "bob-pat")
	connB := saveScionSpace(t, endpoint, b, connectionID, authB)
	pathA := filepath.Join(a.roots.Data, "scion-profiles", authA.Profile.Profile, "credentials.json")
	pathB := filepath.Join(b.roots.Data, "scion-profiles", authB.Profile.Profile, "credentials.json")
	beforeA, errA := os.ReadFile(pathA)
	beforeB, errB := os.ReadFile(pathB)
	if errA != nil || errB != nil || pathA == pathB || bytes.Equal(beforeA, beforeB) {
		t.Fatal("credentials not independently owned", errA, errB)
	}
	card := scionSpaceAccountCard(t, endpoint, b, 1)
	q := platformSelectionRequest{ServiceInfo: waitSpaceAssistant(t, b).info, Caller: Caller{Author: Author{Kind: "human", ID: b.OwnerUserID}}, OperationID: card.ID}
	var candidates []platformSelectionAccount
	if code := spaceHTTPRequest(t, endpoint, spaceRoute(bID)+"/v1/platforms/selection-interactions/candidates", q, &candidates); code != 200 || len(candidates) != 1 || candidates[0].Account.ID.Value != "bob" {
		t.Fatal("new Space card mixed accounts", code)
	}
	q.Version, q.Draft = card.Version, &platformSelectionDraft{PlatformID: connB.ID, PlatformVersion: connB.Version}
	if code := spaceHTTPRequest(t, endpoint, spaceRoute(bID)+"/v1/platforms/selection-interactions/update", q, &card); code != 200 {
		t.Fatal(code)
	}
	q.Version, q.Draft = card.Version, nil
	if code := spaceHTTPRequest(t, endpoint, spaceRoute(bID)+"/v1/platforms/selection-interactions/submit", q, &card); code != 200 || card.Status != "completed" || card.Account.ID.Value != "bob" {
		t.Fatal("card submission failed", code)
	}
	// Continue the same human request through the ordinary tool/notification owner.
	on, repo, interval := true, "Owner/repo", 3600
	member := platformMemberRequest{ServiceInfo: q.ServiceInfo, Caller: agentCaller(card.AgentID, card.MembershipID), SourceSeq: card.Source.Seq, Input: &platformMemberInput{PlatformID: connB.ID, PlatformVersion: connB.Version, Enabled: &on}}
	member.OperationID, _ = newOpaqueID()
	var operation platformMemberOperation
	if code := scionSpaceAgentHTTP(t, endpoint, b, member.ServiceInfo, member.Caller, "/v1/platforms/member/tool", member, &operation); code != 200 || operation.Status != "completed" {
		t.Fatal("new Space tool setup", code, operation.Status)
	}
	member.OperationID, _ = newOpaqueID()
	member.Input.Scion = &platformMemberScionPatch{Repository: &repo, IntervalSeconds: &interval}
	if code := scionSpaceAgentHTTP(t, endpoint, b, member.ServiceInfo, member.Caller, "/v1/platforms/member/notifications", member, &operation); code != 200 || operation.Status != "completed" {
		t.Fatal("new Space notification setup", code, operation.Status)
	}
	member.OperationID = ""
	member.SourceSeq = 0
	member.Input = &platformMemberInput{PlatformID: connB.ID, PlatformVersion: connB.Version}
	var configured platformMemberView
	if code := scionSpaceAgentHTTP(t, endpoint, b, member.ServiceInfo, member.Caller, "/v1/platforms/member/get", member, &configured); code != 200 || !configured.ToolEnabled || configured.Scion == nil || !configured.Scion.Enabled || configured.SubscriptionID == "" {
		t.Fatal("new Space configuration did not become usable", code)
	}
	aSvc := waitSpaceAssistant(t, a)
	aSnapshot, snapshotErr := aSvc.store.LoadRoom()
	if snapshotErr != nil || len(aSnapshot.Members) == 0 {
		t.Fatal("A assistant missing", snapshotErr)
	}
	aTarget := MemberIdentity{RoomIdentity: aSvc.info.RoomIdentity, AgentID: aSnapshot.Members[0].AgentID, MembershipID: aSnapshot.Members[0].MembershipID}
	a.manager.mu.Lock()
	aFile, loadAErr := a.manager.readPlatforms()
	aSource, sourceAErr := a.manager.notifications.CurrentLocked(aTarget.RoomIdentity, scion.WorkitemsSourceID, connA.ID, platformMemberTarget(aTarget))
	a.manager.mu.Unlock()
	if loadAErr != nil || len(aFile.Platforms) != 1 || sourceAErr != nil || aSource != nil {
		t.Fatal("B configuration changed A connections or subscriptions", loadAErr, sourceAErr)
	}
	var current struct {
		Platforms []managedPlatform `json:"platforms"`
	}
	query := url.Values{"service_epoch": {b.manager.epoch}, "caller_id": {b.OwnerUserID}}
	if code := spaceHTTPRequest(t, endpoint, spaceRoute(b.SpaceID)+"/v1/platforms?"+query.Encode(), nil, &current); code != 200 || len(current.Platforms) != 1 {
		t.Fatal("read current logout impact", code)
	}
	refs := []RoomIdentity{}
	for _, ref := range current.Platforms[0].References {
		refs = append(refs, RoomIdentity{RoomID: ref.RoomID, RoomInstance: ref.RoomInstance})
	}
	if len(refs) != 1 || refs[0] != card.RoomIdentity {
		t.Fatal("logout impact crossed Space boundary", refs)
	}
	if code := scionSpaceHTTP(t, endpoint, b, "/v1/platforms/scion/logout", map[string]any{"url": connB.URL, "cli_profile": connB.CLIProfile, "affected": []scionAuthPlatform{{ID: connB.ID, Version: connB.Version, ReferencedRooms: refs}}}, nil); code != 200 {
		t.Fatal("logout failed", code)
	}
	if _, err := os.Stat(pathB); !os.IsNotExist(err) {
		t.Fatal("B credentials survived logout", err)
	}
	afterA, err := os.ReadFile(pathA)
	if err != nil || !bytes.Equal(beforeA, afterA) {
		t.Fatal("B logout altered A", err)
	}
	stop()
	s2, endpoint2, _ := startSpaceServerFixture(t, home)
	a2 := waitSpaceState(t, s2, a.SpaceID, "ready")
	b2 := waitSpaceState(t, s2, bID, "ready")
	var checked managedPlatform
	if code := scionSpaceHTTP(t, endpoint2, a2, "/v1/platforms/verify", map[string]any{"id": connA.ID, "version": connA.Version}, &checked); code != 200 || checked.Validation.Username != "alice" || checked.Validation.Status != "valid" {
		t.Fatal("A account did not survive restart", code)
	}
	if code := scionSpaceHTTP(t, endpoint2, b2, "/v1/platforms/scion/discover", map[string]any{"url": connB.URL}, &empty); code != 200 || len(empty.Profiles) != 0 {
		t.Fatal("B logout lost across restart", code)
	}
	b2.manager.mu.Lock()
	restored, err := b2.manager.readPlatformSelectionLocked(card.ID)
	savedSubscription, subscriptionErr := b2.manager.currentScionForMemberLocked(platformMemberRequest{ServiceInfo: ServiceInfo{RoomIdentity: card.RoomIdentity}, Caller: agentCaller(card.AgentID, card.MembershipID), Input: &platformMemberInput{PlatformID: connB.ID}})
	b2.manager.mu.Unlock()
	if err != nil || restored.Status != "completed" || restored.Account.ID.Value != "bob" {
		t.Fatal("selection receipt lost", err)
	}
	if subscriptionErr != nil || savedSubscription == nil || savedSubscription.SubscriptionID != configured.SubscriptionID || savedSubscription.Config.Repository != configured.Scion.Repository || !savedSubscription.Config.BackfillStart.Equal(configured.Baseline) {
		t.Fatal("B logout/restart lost subscription identity or reception start", subscriptionErr)
	}
}
