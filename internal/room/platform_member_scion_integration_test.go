//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/scion"
)

func scionMemberRequest(t *testing.T, m *roomManager, svc *service, id MemberIdentity, platformID string, version uint64) platformMemberRequest {
	t.Helper()
	op, _ := newOpaqueID()
	source, err := svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: op, Text: "给你开启 Scion 工具，并关注 Owner/repo 从今天开始的新 Issue"})
	if err != nil {
		t.Fatal(err)
	}
	return platformMemberRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), OperationID: op, SourceSeq: source.Seq, Input: &platformMemberInput{PlatformID: platformID, PlatformVersion: version}}
}

func TestScionMemberCLIIndependentToolAndNotification(t *testing.T) {
	m, _, svc, c, id, _ := scionNotificationHarness(t)
	m.toolCatalog = nil
	req := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
	get := func() platformMemberView { return gitlabMemberGet(t, m, req) }
	if v := get(); v.NotificationVersion != 0 || v.ToolEnabled || v.Config != nil || v.GitLab != nil {
		t.Fatal(v)
	}
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, MemberState{AgentID: id.AgentID, MembershipID: id.MembershipID})
	run := func(args []string) platformMemberOperation {
		t.Helper()
		var out bytes.Buffer
		if err := runPlatformConnectWithClient(t.Context(), args, &out, &out, env, client); err != nil {
			t.Fatal(err, out.String())
		}
		var op platformMemberOperation
		if json.Unmarshal(out.Bytes(), &op) != nil || op.Status != "completed" || op.Schema != 4 {
			t.Fatal(out.String())
		}
		return op
	}
	base := []string{"--platform-id", c.ID, "--platform-version", strconv.FormatUint(c.Version, 10), "--source-seq", strconv.FormatUint(req.SourceSeq, 10)}
	toolID, _ := newOpaqueID()
	run(append([]string{"member", "tool", "--operation-id", toolID, "--enabled", "true"}, base...))
	if v := get(); !v.ToolEnabled || v.NotificationVersion != 0 {
		t.Fatal("tool enabled notifications", v)
	}
	args := append([]string{"member", "notifications", "--operation-id", req.OperationID, "--notification-version", "0", "--scion-repository", "Owner/repo", "--scion-object-type", "issue", "--scion-backfill-seconds", "86400", "--scion-interval-seconds", "3600"}, base...)
	op := run(args)
	v := get()
	if v.Scion == nil || v.Config != nil || v.GitLab != nil || v.NotificationVersion != 1 || v.SubscriptionID == "" || v.Baseline.IsZero() || !v.ToolEnabled || !reflect.DeepEqual(v.Scion, op.ScionConfig) || v.Scion.Target != inboxTarget(id) {
		t.Fatal(v, op)
	}
	if replay := run(args); !reflect.DeepEqual(replay, op) {
		t.Fatal("lost response changed receipt")
	}
	// The management page reads the same state; the sibling member has no source.
	m.mu.Lock()
	s, _ := m.notifications.CurrentLocked(id.RoomIdentity, scion.WorkitemsSourceID, c.ID, inboxTarget(id))
	state := s.(scion.State)
	page, e := m.scionNotificationViewLocked(id.RoomIdentity, &state)
	m.mu.Unlock()
	if e != nil || page.Version != v.NotificationVersion || !reflect.DeepEqual(page.Config, *v.Scion) {
		t.Fatal("UI disagrees", page, e)
	}
	snapshot, e := svc.store.LoadRoom()
	if e != nil {
		t.Fatal(e)
	}
	for _, sibling := range snapshot.Members {
		if sibling.MembershipID == id.MembershipID {
			continue
		}
		other := req
		other.Caller = agentCaller(sibling.AgentID, sibling.MembershipID)
		if view := gitlabMemberGet(t, m, other); view.ToolEnabled || view.NotificationVersion != 0 {
			t.Fatal("member settings leaked", view)
		}
	}
	intervalReq := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
	intervalReq.Input.NotificationVersion = v.NotificationVersion
	intervalReq.Input.Scion = &platformMemberScionPatch{IntervalSeconds: ptrScion(120)}
	changed := gitlabMemberApply(t, m, "notifications", intervalReq)
	afterInterval := get()
	if changed.Result.Version != 2 || afterInterval.Scion.IntervalSeconds != 120 || !afterInterval.Baseline.Equal(v.Baseline) || afterInterval.SubscriptionID != v.SubscriptionID {
		t.Fatal("interval reset scope", afterInterval)
	}
	for _, enabled := range []bool{false, true} {
		next := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
		next.ObserveCurrent = true
		next.OperationID = ""
		next.Input = &platformMemberInput{PlatformID: c.ID, Enabled: &enabled}
		got, err := m.configurePlatformMember(t.Context(), "notifications", next)
		if err != nil || got.Status != "completed" {
			t.Fatal(got, err)
		}
		current := get()
		if !current.Baseline.Equal(v.Baseline) || current.SubscriptionID != v.SubscriptionID || current.Scion.Repository != v.Scion.Repository || current.Scion.Enabled != enabled || !current.ToolEnabled {
			t.Fatal(current)
		}
		before, _ := os.ReadFile(m.platformMemberPath(got.ID))
		if _, err = m.configurePlatformMember(t.Context(), "notifications", next); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(m.platformMemberPath(got.ID))
		if !bytes.Equal(before, after) {
			t.Fatal("replay rewrote receipt")
		}
	}
	// A tool disable uses the same saved binding and leaves reception enabled.
	disable := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
	off := false
	disable.OperationID = ""
	disable.ObserveCurrent = true
	disable.Input = &platformMemberInput{PlatformID: c.ID, Enabled: &off}
	if _, err := m.configurePlatformMember(t.Context(), "tool", disable); err != nil {
		t.Fatal(err)
	}
	if current := get(); current.ToolEnabled || !current.Scion.Enabled {
		t.Fatal(current)
	}
}

func TestScionMemberRangeLocksUnknownAndAdmission(t *testing.T) {
	m, _, svc, c, id, _ := scionNotificationHarness(t)
	req := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
	on := true
	req.Input.Enabled = &on
	if _, err := m.configurePlatformMember(t.Context(), "notifications", req); err == nil || !strings.Contains(err.Error(), "仓库") {
		t.Fatal("missing repository accepted", err)
	}
	repo := "Owner/repo"
	req.Input.Scion = &platformMemberScionPatch{Repository: &repo}
	op := gitlabMemberApply(t, m, "notifications", req)
	v := gitlabMemberGet(t, m, req)
	for _, patch := range []platformMemberScionPatch{
		{Repository: ptrScion("Owner/other")}, {ObjectTypes: ptrScion([]string{"review"})}, {BackfillSeconds: ptrScion(86400)}, {BackfillStart: ptrScion(time.Now().Add(time.Hour))},
	} {
		next := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
		next.Input.NotificationVersion = v.NotificationVersion
		next.Input.Scion = &patch
		if _, err := m.configurePlatformMember(t.Context(), "notifications", next); err == nil {
			t.Fatal("range edit accepted", patch)
		}
	}
	// Persisted unknown remains an at-most-once fence, including a reload.
	m.mu.Lock()
	op.Status = "unknown"
	op.ResultVersion = 0
	op.Result.Status = "unknown"
	if err := m.savePlatformMemberLocked(op); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	for i := 0; i < 2; i++ {
		got := gitlabMemberApply(t, m, "notifications", req)
		if got.Status != "unknown" || gitlabMemberGet(t, m, req).NotificationVersion != v.NotificationVersion {
			t.Fatal("unknown reapplied", got)
		}
	}
	// The configuration service rechecks the frozen member/account/version even
	// when a caller reaches it after another management write.
	next := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
	next.Input.NotificationVersion = v.NotificationVersion
	m.mu.Lock()
	old, err := m.platformMemberViewLocked(t.Context(), next)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), platformMemberContextKey{}, platformMemberAdmission{Request: next, Account: old.Account, AccountSnapshot: old.account, SubscriptionID: old.SubscriptionID, Kind: "notifications"})
	cfg := *v.Scion
	cfg.Enabled = false
	state, err := m.configureScionSubscription(t.Context(), notification.Key{Owner: id.RoomIdentity, Source: scion.WorkitemsSourceID, SubscriptionID: v.SubscriptionID}, v.NotificationVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.configureScionSubscription(ctx, state.Metadata().Key, state.SubscriptionVersion, cfg); err == nil {
		t.Fatal("stale member admission accepted")
	}
}

func ptrScion[T any](v T) *T { return &v }

func TestScionMemberAccountSelectionAndNoImplicitSetup(t *testing.T) {
	m, _, svc, c, id, _ := scionNotificationHarness(t)
	secondProfile := *c.CLIProfile
	secondProfile.Profile = strings.Repeat("e", 32)
	scionWriteCredential(t, m, secondProfile.Profile, "bob")
	second := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("f", 32), Name: "Scion two", Provider: "scion", URL: c.URL, CLIProfile: &secondProfile})
	second = verifyScion(t, m, second)
	req := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
	s := platformSelectionRequest{ServiceInfo: svc.info, Caller: req.Caller, SourceSeq: req.SourceSeq, OperationID: req.OperationID, Input: &platformSelectionInput{Provider: "scion", Kind: "account"}}
	first := selectionCall(t, m, "start", s, false)
	m.mu.Lock()
	candidates, err := m.selectionAccountsForInputLocked(*s.Input)
	m.mu.Unlock()
	if err != nil || len(candidates) != 2 || candidates[0].ID != c.ID || candidates[1].ID != second.ID || candidates[1].Account.ID.Value != "bob" {
		t.Fatal(candidates, err)
	}
	human := selectionQuery(s, true)
	human.Version = first.Version
	if w := managerHTTP(m, "/v1/platforms/selection-interactions/submit", human); w.Code != 400 {
		t.Fatal("created a connection", w.Code)
	}
	human.Draft = &platformSelectionDraft{PlatformID: c.ID, PlatformVersion: c.Version}
	updated := selectionCall(t, m, "update", human, true)
	human.Draft = nil
	human.Version = updated.Version
	result := selectionCall(t, m, "submit", human, true)
	if result.Account.Provider != "scion" || result.Draft.PlatformID != c.ID || result.Draft.PlatformVersion != c.Version || result.Draft.Name != c.Name {
		t.Fatal(result)
	}
	if v := gitlabMemberGet(t, m, req); v.ToolEnabled || v.NotificationVersion != 0 {
		t.Fatal("selection enabled settings", v)
	}
	for _, kind := range []string{"groups", "projects"} {
		if validSelectionInput(platformSelectionInput{Provider: "scion", Kind: kind, PlatformID: c.ID, PlatformVersion: c.Version}) {
			t.Fatal("Scion fell through", kind)
		}
	}
}

func TestScionMemberAbsentPauseAndAccountReplacement(t *testing.T) {
	m, _, svc, c, id, _ := scionNotificationHarness(t)
	off := false
	req := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
	req.Input.Enabled = &off
	op := gitlabMemberApply(t, m, "notifications", req)
	if op.Schema != 4 || op.ScionConfig != nil || op.Result.Status != "preserved" || gitlabMemberGet(t, m, req).NotificationVersion != 0 {
		t.Fatal(op)
	}
	repo := "Owner/repo"
	req = scionMemberRequest(t, m, svc, id, c.ID, c.Version)
	req.Input.Scion = &platformMemberScionPatch{Repository: &repo}
	gitlabMemberApply(t, m, "notifications", req)
	old := gitlabMemberGet(t, m, req)
	// A different verified subject may never inherit a matching old config or
	// receive a false preserved result from the shortcut.
	scionWriteCredential(t, m, c.CLIProfile.Profile, "bob")
	c = verifyScion(t, m, c)
	next := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
	on := true
	next.Input.Enabled = &on
	next.Input.NotificationVersion = old.NotificationVersion
	if _, err := m.configurePlatformMember(t.Context(), "notifications", next); err == nil {
		t.Fatal("new account inherited source")
	}
	m.mu.Lock()
	_, err := m.readPlatformMemberLocked(next.OperationID)
	m.mu.Unlock()
	if !os.IsNotExist(err) {
		t.Fatal("identity conflict reserved a receipt", err)
	}
}

func TestScionMemberUnknownSurvivesRestart(t *testing.T) {
	m, room, svc, c, id, _ := scionNotificationHarness(t)
	req := scionMemberRequest(t, m, svc, id, c.ID, c.Version)
	req.Input.Scion = &platformMemberScionPatch{Repository: ptrScion("Owner/repo")}
	real := m.store.ops
	m.mu.Lock()
	m.store.ops.rename = func(from, to string) error {
		if to == m.platformMemberPath(req.OperationID) {
			b, _ := os.ReadFile(from)
			var op platformMemberOperation
			if json.Unmarshal(b, &op) == nil && op.Status == "completed" {
				return os.ErrPermission
			}
		}
		return real.rename(from, to)
	}
	m.mu.Unlock()
	if w := managerHTTP(m, "/v1/platforms/member/notifications", req); w.Code == 200 {
		t.Fatal("unconfirmed receipt reported success")
	}
	m.mu.Lock()
	m.store.ops = real
	state, err := m.notifications.CurrentLocked(id.RoomIdentity, scion.WorkitemsSourceID, c.ID, inboxTarget(id))
	m.mu.Unlock()
	if err != nil || state == nil {
		t.Fatal("missing saved source", err)
	}
	before := state.(scion.State)
	if before.SubscriptionVersion != 1 {
		t.Fatal("config was not saved", before.SubscriptionVersion)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, m.store.dir, nil)
	next := eventLiveService(t, &eventHarness{m: reopened, room: room})
	req.ServiceInfo = next.info
	op := gitlabMemberApply(t, reopened, "notifications", req)
	if op.Schema != 4 || op.Status != "unknown" {
		t.Fatal(op)
	}
	after := gitlabMemberGet(t, reopened, req)
	if before.SubscriptionVersion != after.NotificationVersion || before.SubscriptionID != after.SubscriptionID || !before.Baseline.Equal(after.Baseline) {
		t.Fatal("restart replayed unknown", before, after)
	}
	// A new operation ID on the original human source cannot bypass the fence.
	req.OperationID, _ = newOpaqueID()
	if w := managerHTTP(reopened, "/v1/platforms/member/notifications", req); w.Code != 409 {
		t.Fatal("new ID bypassed unknown", w.Code, w.Body.String())
	}
}
